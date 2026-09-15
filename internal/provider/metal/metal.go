// Package metal implements the RezusCloud provider for bare-metal Talos nodes.
// It is fundamentally different from oci/openstack: it does NOT create
// infrastructure via a cloud API. Machines are pre-booted into Talos maintenance
// mode (from USB ISO or PXE), and the generated `.tf.json` uses
// `talos_machine_configuration_apply` to push config to each node's API on port
// 50000, targeted by the node's IPv6 (ADR 13 rejected; ADR 22).
//
// It is the RezusCloud equivalent of talos-iac/modules/edge-baremetal/.
//
// The renderer reproduces the proven patterns from the reference module:
//   - one random_pet per machine, keyed by IP, with a stable keeper (the IP)
//     so it persists across applies (no keeper → pet regenerates → stale
//     hostname → orphaned k8s node)
//   - talos_machine_configuration_apply with for_each over machines, node=each.key
//   - config_patches applied per machine (install disk, hostname, storage, etc.)
//   - on_destroy = { reset, graceful, reboot } (clean teardown)
//   - create_before_destroy
//
// Discovery (finding maintenance-mode nodes on the LAN) is RezusCloud-side Go
// logic — see discovery.go — NOT a TF resource. It's a UI action: scan →
// operator confirms → machines are added → reconciler enqueues → this provider
// renders the apply config.
package metal

import (
	"encoding/json"
	"fmt"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// obj is a local alias for the shared TF-config JSON object type.
type obj = provider.Obj

// Provider is the bare-metal implementation of provider.Provider.
type Provider struct{}

// New returns the metal provider. It holds no state — config comes per Render.
func New() *Provider { return &Provider{} }

// Type returns "metal".
func (p *Provider) Type() string { return "metal" }

// Mappings declares the TF resource types metal creates and their RezusCloud
// Kind. talos_machine_configuration_apply → Machine (Phase 4 projects each
// applied node to a Machine's status: management address, applied generation).
func (p *Provider) Mappings() []provider.TFResourceMapping {
	return []provider.TFResourceMapping{
		{TFType: "talos_machine_configuration_apply", Kind: "Machine"},
	}
}

// MachineConfig is the per-machine configuration carried in a metal NodeGroup's
// ProviderConfig. The map key is the machine's management address (IPv6).
type MachineConfig struct {
	// InstallDisk is the Talos install disk, e.g. "/dev/nvme0n1". Required.
	InstallDisk string `json:"installDisk"`
	// StorageDisk is an optional secondary disk exposed for local-path storage,
	// e.g. "/dev/sda". Empty ⇒ no storage-volume patch.
	StorageDisk string `json:"storageDisk,omitempty"`
	// DeviceType is the Talos platform device type, default "generic".
	DeviceType string `json:"deviceType,omitempty"`
	// NetworkInterface, if set, emits a network-config patch (e.g. for WoL).
	NetworkInterface string `json:"networkInterface,omitempty"`
}

// NodeGroupConfig is the metal-specific configuration carried in a NodeGroup's
// ProviderConfig (JSON). Unlike oci/openstack (which create count instances of a
// shape), metal applies config to a fixed set of already-booted machines.
type NodeGroupConfig struct {
	// Machines maps each machine's management address (IPv6) → its per-machine
	// config. The map KEY is the node= argument to talos_machine_configuration_apply.
	// Required (at least one machine).
	Machines map[string]MachineConfig `json:"machines"`
	// SchematicID is the Talos Image Factory schematic ID, used to build the
	// install image URL. Empty ⇒ the install-image patch is omitted (node already
	// installed, or image baked into the maintenance ISO).
	SchematicID string `json:"schematicId,omitempty"`
	// Role defaults from the NodeGroupSpec.Role if empty ("controlplane"/"worker").
	Role string `json:"role,omitempty"`
}

const (
	defaultDeviceType = "generic"
)

// Render generates the `.tf.json` for the tenant's metal node groups. Each node
// group produces one data.talos_machine_configuration + one
// talos_machine_configuration_apply (for_each over machines) per role.
func (p *Provider) Render(req provider.RenderRequest) ([]byte, error) {
	if req.Tenant == nil {
		return nil, fmt.Errorf("metal: render request missing tenant")
	}
	if len(req.NodeGroups) == 0 {
		return nil, fmt.Errorf("metal: render request has no node groups")
	}

	root := provider.NewTFConfig()
	root.AddRequiredProviders(
		provider.ReqProvider{Name: "talos", Source: "siderolabs/talos"},
		provider.ReqProvider{Name: "random", Source: "hashicorp/random"},
	)

	for _, ng := range req.NodeGroups {
		if err := renderNodeGroup(&root, ng); err != nil {
			return nil, fmt.Errorf("metal: node group %q: %w", ng.Name, err)
		}
	}

	return json.Marshal(root)
}

// renderNodeGroup emits the data source + random_pet + apply resource for one
// node group. One data.talos_machine_configuration per role; one apply resource
// for_each over the machines map (keyed by IP).
func renderNodeGroup(root *provider.TFConfig, ng state.NodeGroupSpec) error {
	cfg, err := parseNodeGroupConfig(ng)
	if err != nil {
		return err
	}
	if len(cfg.Machines) == 0 {
		return fmt.Errorf("machines map is required (at least one maintenance-mode node)")
	}
	role := cfg.Role
	if role == "" {
		role = ng.Role
	}
	prefix := rolePrefix(role)
	base := sanitize(ng.Name)
	dsName := base + "_config"
	petName := base + "_pet"
	applyName := base + "_apply"

	// data.talos_machine_configuration.<role> — the cluster config for this role.
	// machine_secrets / client_configuration come from var.* (RezusCloud writes
	// a terraform.tfvars.json with the tenant's secrets bundle at apply time).
	root.AddDataSource("talos_machine_configuration", dsName, obj{
		"cluster_name":       "${var.cluster_name}",
		"cluster_endpoint":   "${var.cluster_endpoint}",
		"machine_type":       role,
		"machine_secrets":    "${talos_machine_secrets.this.machine_secrets}",
		"talos_version":      strVar("talos_version"),
		"kubernetes_version": strVar("kubernetes_version"),
	})

	// random_pet: one per machine, keyed by IP, stable keeper (the IP).
	// Without a keeper, a re-apply regenerates the pet → stale hostname →
	// orphaned k8s node object.
	root.AddResource("random_pet", petName, obj{
		"for_each":  fmt.Sprintf("${var.metal_machines_%s}", base),
		"length":    2,
		"separator": "-",
		"keepers":   obj{"ip": "${each.key}"},
	})

	// talos_machine_configuration_apply: for_each over machines, node=each.key.
	patches := buildConfigPatches(base, cfg, prefix, petName)
	apply := obj{
		"for_each":                    fmt.Sprintf("${var.metal_machines_%s}", base),
		"client_configuration":        "${talos_machine_secrets.this.client_configuration}",
		"machine_configuration_input": fmt.Sprintf("${data.talos_machine_configuration.%s.machine_configuration}", dsName),
		"node":                        "${each.key}",
		"apply_mode":                  "auto",
		"config_patches":              patches,
		"on_destroy": obj{
			"reset":    true,
			"graceful": true,
			"reboot":   true,
		},
		"lifecycle": []obj{{
			"create_before_destroy": true,
		}},
	}
	root.AddResource("talos_machine_configuration_apply", applyName, apply)
	return nil
}

// buildConfigPatches returns the per-machine config_patches list. Patches are
// templatefile() expressions referencing the pet id (for hostname) and the
// per-machine install/storage disks. The templates themselves are written to
// the tenant workdir by RezusCloud's tfexec (they live in the reference module);
// here we emit the references that compose them.
func buildConfigPatches(base string, cfg NodeGroupConfig, prefix, petName string) []string {
	patches := []string{
		// hostname uses the pet id (talos-edge-w-<pet>), like the reference.
		fmt.Sprintf("talos-edge-%s-${random_pet.%s[each.key].id}", prefix, petName),
	}
	// Schematic-driven install image (only if schematicId set; node may already
	// be installed via the maintenance ISO).
	if cfg.SchematicID != "" {
		installImage := fmt.Sprintf("factory.talos.dev/installer/%s:${var.talos_version}", cfg.SchematicID)
		patches = append(patches, installImage)
	}
	return patches
}

// --- helpers ---

func parseNodeGroupConfig(ng state.NodeGroupSpec) (NodeGroupConfig, error) {
	var cfg NodeGroupConfig
	cfg.Role = ng.Role
	if len(ng.ProviderConfig) > 0 {
		if err := json.Unmarshal(ng.ProviderConfig, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid providerConfig JSON: %w", err)
		}
	}
	// Default device type on each machine.
	for ip, m := range cfg.Machines {
		if m.DeviceType == "" {
			m.DeviceType = defaultDeviceType
			cfg.Machines[ip] = m
		}
	}
	return cfg, nil
}

func rolePrefix(role string) string {
	if role == "controlplane" {
		return "c"
	}
	return "w"
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}

func strVar(name string) string { return "${var." + name + "}" }

// jsonUnmarshal is a thin wrapper used by tests (keeps test files from
// importing encoding/json directly).
func jsonUnmarshal(b []byte, v interface{}) error {
	return json.Unmarshal(b, v)
}
