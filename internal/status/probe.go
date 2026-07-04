package status

import (
	"context"
	"fmt"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TalosMachineProbe is the production MachineProbe. It checks tenant health by
// probing the first control-plane machine (or any machine if none) via the
// Talos API through the injected version-checking function. The function is
// typically (*talosupgrade.MachineUpgrader).MachineVersion, but is injected as
// a function type to avoid a hard import dependency from status → upgrade/talos
// (which would create a heavy coupling). main.go wires the concrete adapter.
type TalosMachineProbe struct {
	store   state.StoreAPI
	version func(ctx context.Context, machineID string) (string, error)
}

// VersionFunc is the signature of a function that returns the Talos version
// for a given machine ID (e.g., (*talosupgrade.MachineUpgrader).MachineVersion).
type VersionFunc func(ctx context.Context, machineID string) (string, error)

// NewTalosMachineProbe creates a MachineProbe backed by the Talos API.
func NewTalosMachineProbe(store state.StoreAPI, vf VersionFunc) *TalosMachineProbe {
	return &TalosMachineProbe{store: store, version: vf}
}

// ProbeTenant checks the health of a tenant by probing its machines via the
// Talos API. It tries control-plane machines first, then falls back to any
// machine. Returns the observed Talos version of the first reachable machine.
// The bundle parameter is unused — the injected version function resolves
// credentials internally via the SecretsCache.
func (p *TalosMachineProbe) ProbeTenant(ctx context.Context, tenant string, _ interface{}) (string, error) {
	machines, _, err := p.store.ListMachinesByTenant(tenant)
	if err != nil {
		return "", fmt.Errorf("list machines: %w", err)
	}
	if len(machines) == 0 {
		return "", fmt.Errorf("no machines for tenant %q", tenant)
	}

	// Try control-plane machines first.
	var firstErr error
	for _, m := range machines {
		if m.Status.Role != "controlplane" {
			continue
		}
		ver, err := p.version(ctx, m.Metadata.Name)
		if err != nil {
			firstErr = err
			continue
		}
		return ver, nil
	}

	// Fall back to any machine.
	for _, m := range machines {
		if m.Status.Role == "controlplane" {
			continue // already tried
		}
		ver, err := p.version(ctx, m.Metadata.Name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return ver, nil
	}

	if firstErr != nil {
		return "", firstErr
	}
	return "", fmt.Errorf("no reachable machines for tenant %q", tenant)
}
