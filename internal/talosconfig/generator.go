// Package talosconfig generates Talos machine configurations using
// the Talos config machinery. Generates init, controlplane, and worker
// configs from a tenant's secrets bundle and spec.
package talosconfig

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
)

// MachineType determines the Talos config type to generate.
type MachineType string

const (
	TypeInit         MachineType = "init"
	TypeControlPlane MachineType = "controlplane"
	TypeWorker       MachineType = "worker"
)

// ConfigRequest describes what config to generate.
type ConfigRequest struct {
	ClusterName       string
	ClusterEndpoint   string // e.g. "https://192.168.1.10:6443"
	KubernetesVersion string // e.g. "1.35.0"
	TalosVersion      string // e.g. "1.12.6"
	MachineType       MachineType
	PodNetwork        []string // e.g. ["10.244.0.0/16"]
	ServiceNetwork    []string // e.g. ["10.96.0.0/12"]
	CNIType           string   // e.g. "cilium"
	SecretsBundle     json.RawMessage
	ConfigPatches     []string // YAML patches to apply
	MachineID         string   // Hardware UUID
}

// ConfigResult contains the generated config and metadata.
type ConfigResult struct {
	MachineConfig string // Full Talos config YAML
	MachineType   MachineType
	MachineID     string
}

// GenerateConfig generates a Talos machine config from the request.
func GenerateConfig(req ConfigRequest) (*ConfigResult, error) {
	if req.ClusterName == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	if req.KubernetesVersion == "" {
		return nil, fmt.Errorf("kubernetes version is required")
	}
	if req.MachineType == "" {
		return nil, fmt.Errorf("machine type is required")
	}

	// Parse cluster endpoint.
	endpoint := req.ClusterEndpoint
	if endpoint == "" {
		endpoint = "https://127.0.0.1:6443"
	}
	if !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}

	// Default networks (applied via network options in future).
	_ = req.PodNetwork
	_ = req.ServiceNetwork

	// Determine Talos version.
	talosVersion := req.TalosVersion
	if talosVersion == "" {
		talosVersion = "1.12.6"
	}

	versionContract, err := config.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("parse talos version %q: %w", talosVersion, err)
	}

	// Generate or load secrets bundle.
	var bundle *secrets.Bundle
	if req.SecretsBundle != nil {
		bundle = &secrets.Bundle{}
		if err := json.Unmarshal(req.SecretsBundle, bundle); err != nil {
			return nil, fmt.Errorf("unmarshal secrets bundle: %w", err)
		}
	} else {
		bundle, err = secrets.NewBundle(secrets.NewFixedClock(time.Now().UTC()), versionContract)
		if err != nil {
			return nil, fmt.Errorf("generate secrets: %w", err)
		}
	}

	// Generate config options.
	opts := []generate.Option{
		generate.WithEndpointList([]string{endpointURL.Host}),
		generate.WithSecretsBundle(bundle),
		generate.WithVersionContract(versionContract),
	}

	// Create input.
	input, err := generate.NewInput(req.ClusterName, endpoint, req.KubernetesVersion, opts...)
	if err != nil {
		return nil, fmt.Errorf("new input: %w", err)
	}

	// Determine machine type.
	var machineType machine.Type
	switch req.MachineType {
	case TypeInit:
		machineType = machine.TypeInit
	case TypeControlPlane:
		machineType = machine.TypeControlPlane
	case TypeWorker:
		machineType = machine.TypeWorker
	default:
		return nil, fmt.Errorf("unknown machine type: %q", req.MachineType)
	}

	// Generate the config.
	cfg, err := input.Config(machineType)
	if err != nil {
		return nil, fmt.Errorf("generate config: %w", err)
	}

	// Render to YAML.
	yamlBytes, err := cfg.Bytes()
	if err != nil {
		return nil, fmt.Errorf("render config: %w", err)
	}

	return &ConfigResult{
		MachineConfig: string(yamlBytes),
		MachineType:   req.MachineType,
		MachineID:     req.MachineID,
	}, nil
}

// DetermineMachineType returns the Talos machine type based on role and position.
// The first controlplane machine in a tenant gets TypeInit (bootstraps etcd).
func DetermineMachineType(role string, isFirstControlPlane bool) MachineType {
	if role == "controlplane" {
		if isFirstControlPlane {
			return TypeInit
		}
		return TypeControlPlane
	}
	return TypeWorker
}
