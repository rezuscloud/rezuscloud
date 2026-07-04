// Package oci implements the RezusCloud provider for Oracle Cloud
// Infrastructure. It renders standard `.tf.json` using the off-the-shelf `oci`,
// `talos`, and `random` registry providers — it is the RezusCloud equivalent of
// talos-iac/modules/oci-cluster/ (ADR 22).
//
// The renderer reproduces the proven patterns from the reference module:
//   - stable naming via random_pet with keepers (a re-apply that touches the
//     resources must NOT regenerate pets → cascading instance recreation,
//     new IPs, stale k8s nodes, orphaned LB backends)
//   - lifecycle.ignore_changes on metadata.user_data (Talos config changes go
//     through talosctl apply-config, never VM recreation)
//   - lifecycle.ignore_changes on defined_tags (OCI may tag instances out-of-band)
//
// Cloud credentials are NOT embedded. The provider block omits explicit auth;
// the OCI provider reads them from the standard OCI_* environment variables,
// which RezusCloud injects into the tofu process as bootstrap credentials
// (ADR 22 step 4).
package oci

import (
	"encoding/json"
	"fmt"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// obj is a local alias for the shared TF-config JSON object type, keeping the
// render code terse. The builder itself lives in internal/provider (shared by
// all provider modules).
type obj = provider.Obj

// Provider is the OCI implementation of provider.Provider.
type Provider struct{}

// New returns the OCI provider. It holds no state — config comes per Render call.
func New() *Provider { return &Provider{} }

// Type returns "oci".
func (p *Provider) Type() string { return "oci" }

// Mappings declares the TF resource types OCI creates and their RezusCloud Kind.
// oci_core_instance → Machine (Phase 4 projects each instance to a Machine's
// status: provider ID, addresses, shape).
func (p *Provider) Mappings() []provider.TFResourceMapping {
	return []provider.TFResourceMapping{
		{TFType: "oci_core_instance", Kind: "Machine"},
	}
}

// NodeGroupConfig is the OCI-specific configuration carried in a NodeGroup's
// ProviderConfig (JSON). It carries the fields the OCI provider needs that are
// not expressible in the generic NodeGroupSpec (which only has ProviderClass +
// an opaque ProviderConfig blob).
//
// Required: CompartmentOCID, Shape, SubnetID. Others default sensibly.
type NodeGroupConfig struct {
	// Region is the OCI region, e.g. "us-phoenix-1". Tenant-level: all OCI
	// node groups in a tenant use the same region. If empty, the operator
	// must supply TF_VAR_region.
	Region string `json:"region,omitempty"`
	// CompartmentOCID is the OCI compartment the instances live in.
	CompartmentOCID string `json:"compartmentOcid"`
	// Shape is the OCI instance shape, e.g. "VM.Standard.A1.Flex" (ARM) or
	// "VM.Standard.E4.Flex" (AMD). May also be carried in ProviderClass.
	Shape string `json:"shape"`
	// SubnetID is the OCID of the subnet to attach the primary VNIC to.
	SubnetID string `json:"subnetId"`
	// ImageOCID is the Talos image OCID. If empty, the operator is expected to
	// supply it via a tenant-level data source / variable (the renderer emits a
	// variable reference, not a hardcoded value).
	ImageOCID string `json:"imageOcid,omitempty"`
	// NSGID is the network security group OCID for the primary VNIC.
	NSGID string `json:"nsgId,omitempty"`
	// OCPUs for Flex shapes. 0 ⇒ omit shape_config (fixed shapes ignore it).
	OCPUs int `json:"ocpus,omitempty"`
	// MemoryGB for Flex shapes. 0 ⇒ omit shape_config.
	MemoryGB int `json:"memoryGb,omitempty"`
	// BootVolumeGB defaults to 50 when zero (matches the reference module).
	BootVolumeGB int `json:"bootVolumeGb,omitempty"`
	// AssignPublicIP defaults to true (matches reference: controlplane needs a
	// routable address for the API endpoint).
	AssignPublicIP *bool `json:"assignPublicIp,omitempty"`
	// Role is derived from the NodeGroupSpec.Role if empty ("controlplane"/"worker").
	// Used for display_name prefix (c/w) and resource naming.
	Role string `json:"role,omitempty"`
}

const (
	defaultBootVolumeGB = 50
)

// Render generates the `.tf.json` for the tenant's OCI node groups.
func (p *Provider) Render(req provider.RenderRequest) ([]byte, error) {
	if req.Tenant == nil {
		return nil, fmt.Errorf("oci: render request missing tenant")
	}
	if len(req.NodeGroups) == 0 {
		return nil, fmt.Errorf("oci: render request has no node groups")
	}

	tenantName := req.Tenant.Metadata.Name

	root := provider.NewTFConfig()

	// --- terraform block: required providers (off-the-shelf registry only) ---
	root.AddRequiredProviders(
		provider.ReqProvider{Name: "oci", Source: "oracle/oci", Version: ">= 6.0"},
		provider.ReqProvider{Name: "talos", Source: "siderolabs/talos"},
		provider.ReqProvider{Name: "random", Source: "hashicorp/random"},
	)

	// --- provider block: OCI reads creds from OCI_* env (no hardcoded auth) ---
	root.AddProvider("oci", obj{
		// tenancy/region/user/fingerprint/private_key come from the process
		// environment; the operator's bootstrap credentials are injected per
		// ADR 22. Leaving them unset means "use the standard provider env".
		"region": strVar("region"),
	})

	// --- data: availability domains (spread instances across ADs, like ref) ---
	root.AddDataSource("oci_identity_availability_domains", "ads", obj{
		"compartment_id": strVar("compartment_ocid"),
	})

	// --- data: talos_machine_configuration per role ---
	// Each role gets one data source (shared across all node groups of that
	// role). The instances reference it via userDataRef(). The secrets bundle
	// (machine_secrets, client_configuration) is injected via terraform.tfvars.json.
	for _, role := range rolesPresent(req.NodeGroups) {
		renderTalosConfigDataSource(&root, role)
	}

	// Render each node group → random_pet + oci_core_instance pair.
	for _, ng := range req.NodeGroups {
		if err := renderNodeGroup(&root, tenantName, ng); err != nil {
			return nil, fmt.Errorf("oci: node group %q: %w", ng.Name, err)
		}
	}

	// TFConfig.MarshalJSON emits stable, diff-friendly JSON.
	return json.Marshal(root)
}

// renderNodeGroup emits the random_pet (stable name) + oci_core_instance pair
// for one node group. The instance count comes from ng.Count via for_each over
// the random_pet resources.
func renderNodeGroup(root *provider.TFConfig, tenantName string, ng state.NodeGroupSpec) error {
	cfg, err := parseNodeGroupConfig(ng)
	if err != nil {
		return err
	}
	if cfg.CompartmentOCID == "" {
		return fmt.Errorf("compartmentOcid is required")
	}
	if cfg.Shape == "" {
		return fmt.Errorf("shape is required (set in providerClass or providerConfig)")
	}
	if cfg.SubnetID == "" {
		return fmt.Errorf("subnetId is required")
	}

	role := cfg.Role
	if role == "" {
		role = ng.Role
	}
	prefix := rolePrefix(role) // "c" or "w"
	petName := resourceName(ng.Name, "pet")
	instName := resourceName(ng.Name, "instance")

	bootGB := cfg.BootVolumeGB
	if bootGB == 0 {
		bootGB = defaultBootVolumeGB
	}
	assignPub := true
	if cfg.AssignPublicIP != nil {
		assignPub = *cfg.AssignPublicIP
	}
	displayName := fmt.Sprintf("talos-oci-%s-${%s}", prefix, petRef(petName))

	// random_pet: count = ng.Count, with a keepers.index so each pet is stable.
	root.AddResource("random_pet", petName, obj{
		"count":     ng.Count,
		"length":    2,
		"separator": "-",
		"keepers":   obj{"index": "${count.index}"},
	})

	// oci_core_instance: for_each over the pets, one instance per pet.
	// Build the VNIC details once so optional fields (nsg_ids) can be added to
	// the same object without a type assertion (errcheck check-type-assertions).
	vnic := obj{
		"assign_public_ip": assignPub,
		"subnet_id":        cfg.SubnetID,
	}
	if cfg.NSGID != "" {
		vnic["nsg_ids"] = []string{cfg.NSGID}
	}

	inst := obj{
		"for_each": fmt.Sprintf("${{ for idx, val in random_pet.%s : idx => val }}", petName),
		"availability_domain": fmt.Sprintf(
			"${data.oci_identity_availability_domains.ads.availability_domains[each.key %% length(data.oci_identity_availability_domains.ads.availability_domains)].name}",
		),
		"compartment_id":      cfg.CompartmentOCID,
		"shape":               cfg.Shape,
		"create_vnic_details": []obj{vnic},
		"source_details": []obj{{
			"source_type":             "image",
			"source_id":               imageRef(cfg.ImageOCID),
			"boot_volume_size_in_gbs": fmt.Sprintf("%d", bootGB),
		}},
		"display_name": displayName,
		"metadata": obj{
			"user_data": userDataRef(role, tenantName),
		},
	}
	// shape_config only for Flex shapes (fixed shapes reject it).
	if isFlex(cfg.Shape) && cfg.OCPUs > 0 {
		inst["shape_config"] = []obj{{
			"ocpus":         cfg.OCPUs,
			"memory_in_gbs": cfg.MemoryGB,
		}}
	}

	// lifecycle: the proven patterns — never recreate on user_data/defined_tags.
	inst["lifecycle"] = []obj{{
		"create_before_destroy": true,
		"ignore_changes":        []string{"metadata.user_data", "defined_tags"},
	}}

	root.AddResource("oci_core_instance", instName, inst)
	return nil
}

// --- helpers ---

func parseNodeGroupConfig(ng state.NodeGroupSpec) (NodeGroupConfig, error) {
	var cfg NodeGroupConfig
	// Role defaults from the node group.
	cfg.Role = ng.Role
	// ProviderClass may carry the shape (convention: "oci:VM.Standard.A1.Flex").
	if ng.ProviderClass != "" {
		if s := shapeFromClass(ng.ProviderClass); s != "" {
			cfg.Shape = s
		}
	}
	if len(ng.ProviderConfig) > 0 {
		if err := json.Unmarshal(ng.ProviderConfig, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid providerConfig JSON: %w", err)
		}
	}
	return cfg, nil
}

// shapeFromClass extracts a shape from a "oci:<shape>" provider class. Returns
// "" if the class doesn't follow that convention.
func shapeFromClass(class string) string {
	const prefix = "oci:"
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

// resourceName builds a TF-safe resource name from the node group name + suffix,
// sanitized (non-alphanumeric → underscore) and the suffix appended.
func resourceName(ngName, suffix string) string {
	return sanitize(ngName) + "_" + suffix
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

func isFlex(shape string) bool {
	// "VM.Standard.A1.Flex", "VM.Standard.E4.Flex", etc.
	return len(shape) >= 4 && shape[len(shape)-4:] == "Flex"
}

// imageRef returns the TF expression for the image source id: the explicit OCID
// when set, else a variable reference the operator supplies.
func imageRef(ocid string) string {
	if ocid != "" {
		return ocid
	}
	return strVar("talos_image_ocid")
}

// userDataRef returns the TF expression for instance user_data. The talos
// provider generates the machine config; this references the
// talos_machine_configuration data source rendered in Render(). The role
// selects controlplane vs worker config.
func userDataRef(role, tenant string) string {
	cfgType := "worker"
	if role == "controlplane" {
		cfgType = "controlplane"
	}
	_ = tenant
	return fmt.Sprintf("${data.talos_machine_configuration.%s.machine_configuration}", cfgType)
}

// petRef returns the TF expression for a pet's id (used in display_name).
func petRef(petResource string) string {
	return fmt.Sprintf("random_pet.%s[each.value].id", petResource)
}

// rolesPresent returns the distinct set of roles across the given node groups
// ("controlplane" and/or "worker"), preserving first-seen order.
func rolesPresent(ngs []state.NodeGroupSpec) []string {
	seen := make(map[string]bool, 2)
	var roles []string
	for _, ng := range ngs {
		role := ng.Role
		if role == "" {
			continue
		}
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	return roles
}

// renderTalosConfigDataSource emits a data.talos_machine_configuration.<role>
// data source. Each role gets one (shared across all node groups of that role).
// The cluster secrets bundle (machine_secrets, client_configuration) is injected
// via terraform.tfvars.json written to the tenant workdir at apply time.
func renderTalosConfigDataSource(root *provider.TFConfig, role string) {
	root.AddDataSource("talos_machine_configuration", role, obj{
		"cluster_name":       "${var.cluster_name}",
		"cluster_endpoint":   "${var.cluster_endpoint}",
		"machine_type":       role,
		"machine_secrets":    "${talos_machine_secrets.this.machine_secrets}",
		"kubernetes_version": strVar("kubernetes_version"),
		"talos_version":      strVar("talos_version"),
	})
}

func strVar(name string) string { return "${var." + name + "}" }
