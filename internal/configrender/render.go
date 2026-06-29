// Package configrender provides high-level helpers that compose the Talos
// config generation pipeline: load machine + tenant + secrets + patches →
// call talosconfig.GenerateConfig. Both the REST API and the WebUI call
// into this module so the pipeline exists in exactly one place.
//
// The module is testable in isolation: callers pass a StoreReader
// implementation (typically state.StoreAPI in production, a fake in tests).
package configrender

import (
	"context"
	"errors"
	"fmt"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/talosconfig"
)

// StoreReader is the subset of state.StoreAPI the render pipeline needs.
// Defined as an interface so this package doesn't import state for the
// concrete type and tests can substitute a fake.
type StoreReader interface {
	GetMachine(id string) (*state.Machine, error)
	GetTenant(name string) (*state.Tenant, error)
	LoadTenantSecrets(name string) ([]byte, error)
}

// PatchResolver resolves the ConfigPatch list for a tenant + role.
// api/patch.ResolvePatches satisfies this signature.
//
// Note: takes state.StoreAPI because patch.ResolvePatches uses the concrete
// store type today. Future refactors of patch/ may switch this to StoreReader.
type PatchResolver func(store state.StoreAPI, tenant, role string) ([]string, error)

// MachineConfigRequest identifies a machine for which to render a Talos config.
type MachineConfigRequest struct {
	TenantName string
	MachineID  string
	// Patches, if non-nil, overrides the default PatchResolver lookup.
	// Useful for callers that already have the patches in hand.
	Patches []string
}

// MachineConfigResult is the rendered output.
type MachineConfigResult struct {
	YAML        string
	Machine     *state.Machine
	Tenant      *state.Tenant
	MachineType talosconfig.MachineType
}

// ErrNotFound is returned when the machine, tenant, or secrets bundle is missing.
var ErrNotFound = errors.New("configrender: resource not found")

// GenerateMachineConfig loads everything required to render a Talos machine
// config, calls into talosconfig.GenerateConfig, and returns the YAML string.
//
// The error wraps ErrNotFound when the machine, tenant, or secrets bundle
// is missing — callers should map this to HTTP 404.
//
// All I/O uses ctx.Discard() — the store's LoadTenantSecrets is sync today,
// but the context is preserved for a future async store.
func GenerateMachineConfig(ctx context.Context, store StoreReader, stateStore state.StoreAPI, resolver PatchResolver, req MachineConfigRequest) (*MachineConfigResult, error) {
	_ = ctx

	m, err := store.GetMachine(req.MachineID)
	if err != nil {
		return nil, fmt.Errorf("load machine: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("%w: machine %q", ErrNotFound, req.MachineID)
	}

	tenant, err := store.GetTenant(req.TenantName)
	if err != nil {
		return nil, fmt.Errorf("load tenant: %w", err)
	}
	if tenant == nil {
		return nil, fmt.Errorf("%w: tenant %q", ErrNotFound, req.TenantName)
	}

	bundleJSON, err := store.LoadTenantSecrets(req.TenantName)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}
	if bundleJSON == nil {
		return nil, fmt.Errorf("%w: no secrets bundle for tenant %q", ErrNotFound, req.TenantName)
	}

	patches := req.Patches
	if patches == nil && resolver != nil {
		patches, err = resolver(stateStore, req.TenantName, m.Status.Role)
		if err != nil {
			return nil, fmt.Errorf("resolve patches: %w", err)
		}
	}

	machineType := talosconfig.DetermineMachineType(m.Status.Role, false)

	result, err := talosconfig.GenerateConfig(talosconfig.ConfigRequest{
		ClusterName:       req.TenantName,
		ClusterEndpoint:   tenant.Spec.ControlPlaneEndpoint,
		KubernetesVersion: tenant.Spec.KubernetesVersion,
		TalosVersion:      tenant.Spec.TalosVersion,
		MachineType:       machineType,
		SecretsBundle:     bundleJSON,
		ConfigPatches:     patches,
		MachineID:         req.MachineID,
	})
	if err != nil {
		return nil, fmt.Errorf("generate config: %w", err)
	}

	return &MachineConfigResult{
		YAML:        result.MachineConfig,
		Machine:     m,
		Tenant:      tenant,
		MachineType: machineType,
	}, nil
}

// --- Kubeconfig / Talosconfig render helpers (deferred to a future PR) ---
