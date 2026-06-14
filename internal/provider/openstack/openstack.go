// Package openstack implements the RezusCloud provider for the on-prem
// OpenStack cluster. It renders standard `.tf.json` using the off-the-shelf
// `openstack`, `talos`, and `random` registry providers — it is the RezusCloud
// equivalent of talos-iac/modules/openstack-cluster/ (ADR 22). No custom TF
// plugin, no Nova gRPC provisioner (supersedes #77).
//
// The renderer reproduces the proven patterns from the reference module:
//   - config delivery via config_drive=true + base64 user_data (the OpenStack
//     platform reads the config-drive 'config-2' label; nocloud would not)
//   - two Cinder volumes: boot (ssd-ephemeral, lvm-2/Samsung 850 PRO) + optional
//     data (ssd, lvm-1/NVMe RAID1). The volume-type distinction is load-bearing
//     — the on-prem Cinder backends map types to different physical disk pools.
//   - booted from volume (block_device source_type=volume), NOT from image
//   - lifecycle.ignore_changes on image_id + block_device + user_data (Talos
//     config changes go through talosctl apply-config; image upgrades via
//     talosctl upgrade — never VM/volume recreation)
//   - permissive security group (IPv4+IPv6 ingress/egress); actual port
//     filtering is done by Talos' own Ingress Firewall on the host
//   - stable naming via random_pet with keepers.index
//
// Cloud credentials are NOT embedded. The provider block omits auth; the
// OpenStack provider reads them from OS_* environment variables, which
// RezusCloud injects into the tofu process as bootstrap credentials (ADR 22).
package openstack

import (
	"encoding/json"
	"fmt"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// obj is a local alias for the shared TF-config JSON object type.
type obj = provider.Obj

// Provider is the OpenStack implementation of provider.Provider.
type Provider struct{}

// New returns the OpenStack provider. It holds no state — config comes per
// Render call.
func New() *Provider { return &Provider{} }

// Type returns "openstack".
func (p *Provider) Type() string { return "openstack" }

// Mappings declares the TF resource types OpenStack creates and their RezusCloud
// Kind. openstack_compute_instance_v2 → Machine (Phase 4 projects each instance
// to a Machine's status: provider ID, addresses, flavor).
func (p *Provider) Mappings() []provider.TFResourceMapping {
	return []provider.TFResourceMapping{
		{TFType: "openstack_compute_instance_v2", Kind: "Machine"},
	}
}

// NodeGroupConfig is the OpenStack-specific configuration carried in a
// NodeGroup's ProviderConfig (JSON). It carries the fields the OpenStack
// provider needs that are not expressible in the generic NodeGroupSpec.
//
// Required: FlavorName. ImageName defaults from TalosVersion if empty.
// ExtNetName defaults to "ext-net". Volume types default to the on-prem
// Cinder backend mapping (boot=ssd-ephemeral, data=ssd).
type NodeGroupConfig struct {
	// FlavorName is the OpenStack flavor, e.g. "SCS-16V-32-100".
	FlavorName string `json:"flavorName"`
	// ImageName is the Glance image name. If empty, defaults to
	// "talos-<talosVersion>-openstack-amd64" (the deterministic name the
	// upload-openstack-image CI job creates).
	ImageName string `json:"imageName,omitempty"`
	// TalosVersion is used to build the default image name. Defaults from the
	// tenant spec / node group if empty.
	TalosVersion string `json:"talosVersion,omitempty"`
	// BootVolumeSizeGB defaults to 50 when zero (matches the reference module).
	BootVolumeSizeGB int `json:"bootVolumeSizeGb,omitempty"`
	// DataVolumeSizeGB > 0 emits an optional data volume attached as block_device[1].
	DataVolumeSizeGB int `json:"dataVolumeSizeGb,omitempty"`
	// BootVolumeType defaults to "ssd-ephemeral" (lvm-2, Samsung 850 PRO NVMe).
	BootVolumeType string `json:"bootVolumeType,omitempty"`
	// DataVolumeType defaults to "ssd" (lvm-1, NVMe RAID1).
	DataVolumeType string `json:"dataVolumeType,omitempty"`
	// ExtNetName defaults to "ext-net" (the cluster's flat provider network).
	ExtNetName string `json:"extNetName,omitempty"`
	// FixedIPv4 optionally pins a specific IPv4 (else Neutron allocates one).
	FixedIPv4 string `json:"fixedIpV4,omitempty"`
	// Role is derived from the NodeGroupSpec.Role if empty.
	Role string `json:"role,omitempty"`
}

const (
	defaultBootVolumeSizeGB = 50
	defaultExtNetName       = "ext-net"
	defaultBootVolumeType   = "ssd-ephemeral"
	defaultDataVolumeType   = "ssd"
	defaultImageArch        = "amd64"
)

// Render generates the `.tf.json` for the tenant's OpenStack node groups.
func (p *Provider) Render(req provider.RenderRequest) ([]byte, error) {
	if req.Tenant == nil {
		return nil, fmt.Errorf("openstack: render request missing tenant")
	}
	if len(req.NodeGroups) == 0 {
		return nil, fmt.Errorf("openstack: render request has no node groups")
	}

	root := provider.NewTFConfig()

	root.AddRequiredProviders(
		provider.ReqProvider{Name: "openstack", Source: "terraform-provider-openstack/openstack"},
		provider.ReqProvider{Name: "talos", Source: "siderolabs/talos"},
		provider.ReqProvider{Name: "random", Source: "hashicorp/random"},
	)

	// provider block: OpenStack reads creds from OS_* env (no hardcoded auth).
	root.AddProvider("openstack", obj{})

	// Render each node group → its own secgroup + pets + volumes + instance.
	// (One secgroup per node group keeps them isolated; the reference uses a
	// single cluster-wide secgroup because it has one workers map.)
	for _, ng := range req.NodeGroups {
		if err := renderNodeGroup(&root, ng); err != nil {
			return nil, fmt.Errorf("openstack: node group %q: %w", ng.Name, err)
		}
	}

	return json.Marshal(root)
}

// renderNodeGroup emits the data sources + secgroup + pets + volumes + instance
// for one node group.
func renderNodeGroup(root *provider.TFConfig, ng state.NodeGroupSpec) error {
	cfg, err := parseNodeGroupConfig(ng)
	if err != nil {
		return err
	}
	if cfg.FlavorName == "" {
		return fmt.Errorf("flavorName is required")
	}
	if cfg.BootVolumeSizeGB <= 0 {
		return fmt.Errorf("bootVolumeSizeGb must be > 0")
	}

	role := cfg.Role
	if role == "" {
		role = ng.Role
	}
	prefix := rolePrefix(role) // "c" or "w"
	base := sanitize(ng.Name)
	extNetDS := base + "_extnet"
	imageDS := base + "_image"
	secgroup := base + "_secgroup"
	petName := base + "_pet"
	bootVol := base + "_boot"
	dataVol := base + "_data"
	instName := base + "_instance"

	// --- data: ext-net + Glance image lookups ---
	root.AddDataSource("openstack_networking_network_v2", extNetDS, obj{
		"name":     cfg.ExtNetName,
		"external": true,
	})
	root.AddDataSource("openstack_images_image_v2", imageDS, obj{
		"name":        cfg.ImageName,
		"most_recent": true,
	})

	// --- security group: permissive ingress/egress, filtered at host layer ---
	renderSecGroup(root, secgroup)

	// --- random_pet: count = ng.Count, stable keepers ---
	root.AddResource("random_pet", petName, obj{
		"count":     ng.Count,
		"length":    2,
		"separator": "-",
		"keepers":   obj{"index": "${count.index}"},
	})

	// --- boot volume (ssd-ephemeral by default) ---
	root.AddResource("openstack_blockstorage_volume_v3", bootVol, obj{
		"for_each":    forEachPet(petName),
		"name":        fmt.Sprintf("talos-os-%s-${%s}", prefix, petEachRef(petName)),
		"size":        cfg.BootVolumeSizeGB,
		"volume_type": cfg.BootVolumeType,
		"image_id":    imageDSRef(imageDS),
		"description": fmt.Sprintf("Boot volume for node group %s", ng.Name),
		"lifecycle": []obj{{
			"ignore_changes": []string{"image_id"},
		}},
	})

	// --- optional data volume (ssd by default) ---
	hasData := cfg.DataVolumeSizeGB > 0
	if hasData {
		root.AddResource("openstack_blockstorage_volume_v3", dataVol, obj{
			"for_each":    forEachPet(petName),
			"name":        fmt.Sprintf("talos-os-%s-${%s}-data", prefix, petEachRef(petName)),
			"size":        cfg.DataVolumeSizeGB,
			"volume_type": cfg.DataVolumeType,
			"description": fmt.Sprintf("Data volume for node group %s", ng.Name),
		})
	}

	// --- instance: booted from volume, config_drive delivery ---
	blockDevices := []obj{{
		"uuid":                  bootVolRef(bootVol),
		"source_type":           "volume",
		"boot_index":            0,
		"destination_type":      "volume",
		"delete_on_termination": true,
	}}
	if hasData {
		blockDevices = append(blockDevices, obj{
			"uuid":                  dataVolRef(dataVol),
			"source_type":           "volume",
			"boot_index":            1,
			"destination_type":      "volume",
			"delete_on_termination": true,
		})
	}

	network := obj{"name": extNetDSRef(extNetDS)}
	if cfg.FixedIPv4 != "" {
		network["fixed_ip_v4"] = cfg.FixedIPv4
	}

	inst := obj{
		"for_each":            forEachPet(petName),
		"name":                fmt.Sprintf("talos-os-%s-${%s}", prefix, petEachRef(petName)),
		"flavor_name":         cfg.FlavorName,
		"block_device":        blockDevices,
		"network":             []obj{network},
		"security_groups":     []string{secgroupRef(secgroup)},
		"config_drive":        true,
		"user_data":           userDataRef(role),
		"stop_before_destroy": true,
		"lifecycle": []obj{{
			// Talos config changes → talosctl apply-config; image upgrades →
			// talosctl upgrade. Never recreate the VM/volumes for these.
			"ignore_changes": []string{"image_id", "block_device", "user_data"},
		}},
	}
	root.AddResource("openstack_compute_instance_v2", instName, inst)
	return nil
}

// renderSecGroup emits one permissive security group + 4 rules (ingress/egress,
// IPv4/IPv6). Actual port filtering is done by Talos' Ingress Firewall on the
// host — same layered approach as the edge node.
func renderSecGroup(root *provider.TFConfig, name string) {
	root.AddResource("openstack_networking_secgroup_v2", name, obj{
		"name":                 name,
		"description":          "Permissive ingress for Talos (filtered by Talos Ingress Firewall on the host)",
		"delete_default_rules": true,
	})
	sgID := secgroupIDRef(name)
	for _, r := range []struct{ dir, ether string }{
		{"ingress", "IPv4"}, {"ingress", "IPv6"},
		{"egress", "IPv4"}, {"egress", "IPv6"},
	} {
		ruleName := fmt.Sprintf("%s_%s_%s", name, r.dir, r.ether)
		prefix := "0.0.0.0/0"
		if r.ether == "IPv6" {
			prefix = "::/0"
		}
		root.AddResource("openstack_networking_secgroup_rule_v2", ruleName, obj{
			"direction":         r.dir,
			"ethertype":         r.ether,
			"remote_ip_prefix":  prefix,
			"security_group_id": sgID,
		})
	}
}

// --- helpers ---

func parseNodeGroupConfig(ng state.NodeGroupSpec) (NodeGroupConfig, error) {
	cfg := NodeGroupConfig{Role: ng.Role}
	// ProviderClass convention: "openstack:<flavor>" (e.g. "openstack:SCS-16V-32-100").
	if ng.ProviderClass != "" {
		if f := fromClassPrefix(ng.ProviderClass, "openstack:"); f != "" {
			cfg.FlavorName = f
		}
	}
	if len(ng.ProviderConfig) > 0 {
		if err := json.Unmarshal(ng.ProviderConfig, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid providerConfig JSON: %w", err)
		}
	}
	// Apply defaults after unmarshal (only fill truly-empty fields).
	if cfg.ImageName == "" {
		ver := cfg.TalosVersion
		if ver == "" {
			ver = ng.TalosVersion
		}
		if ver != "" {
			cfg.ImageName = fmt.Sprintf("talos-%s-openstack-%s", ver, defaultImageArch)
		}
	}
	if cfg.BootVolumeType == "" {
		cfg.BootVolumeType = defaultBootVolumeType
	}
	if cfg.DataVolumeType == "" {
		cfg.DataVolumeType = defaultDataVolumeType
	}
	if cfg.ExtNetName == "" {
		cfg.ExtNetName = defaultExtNetName
	}
	return cfg, nil
}

func fromClassPrefix(class, prefix string) string {
	if len(class) > len(prefix) && class[:len(prefix)] == prefix {
		return class[len(prefix):]
	}
	return ""
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

// --- TF expression helpers ---

// forEachPet is the for_each that maps random_pet indices → pet values.
func forEachPet(petResource string) string {
	return fmt.Sprintf("${{ for idx, val in random_pet.%s : idx => val }}", petResource)
}

// petEachRef references the pet id inside an instance/volume for_each block.
func petEachRef(petResource string) string {
	return fmt.Sprintf("random_pet.%s[each.key].id", petResource)
}

func imageDSRef(ds string) string { return fmt.Sprintf("${data.openstack_images_image_v2.%s.id}", ds) }
func bootVolRef(v string) string {
	return fmt.Sprintf("${openstack_blockstorage_volume_v3.%s[each.key].id}", v)
}
func dataVolRef(v string) string {
	return fmt.Sprintf("${openstack_blockstorage_volume_v3.%s[each.key].id}", v)
}
func extNetDSRef(ds string) string {
	return fmt.Sprintf("${data.openstack_networking_network_v2.%s.name}", ds)
}
func secgroupRef(sg string) string { return sg } // referenced by name in security_groups
func secgroupIDRef(sg string) string {
	return fmt.Sprintf("${openstack_networking_secgroup_v2.%s.id}", sg)
}

// userDataRef returns the TF expression for instance user_data (base64 of the
// talos machine config, delivered via config_drive).
func userDataRef(role string) string {
	cfgType := "worker"
	if role == "controlplane" {
		cfgType = "controlplane"
	}
	return fmt.Sprintf("${base64encode(data.talos_machine_configuration.%s.machine_configuration)}", cfgType)
}
