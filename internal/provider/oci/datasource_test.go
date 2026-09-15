package oci

import (
	"encoding/json"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// renderForTest renders config for a single node group and returns the parsed
// top-level config so tests can assert on its shape.
func renderForTest(t *testing.T, role string) map[string]any {
	t.Helper()
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	ng := state.NodeGroupSpec{
		Name:          role,
		Role:          role,
		Count:         1,
		ProviderClass: "oci:VM.Standard.A1.Flex",
		ProviderConfig: []byte(`{
			"compartmentOcid": "ocid1.compartment.oc1..demo",
			"subnetId":        "ocid1.subnet.oc1..demo",
			"imageOcid":       "ocid1.image.oc1..talos"
		}`),
	}
	out, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: []state.NodeGroupSpec{ng}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func TestRender_UserDataIsBootstrapVariable(t *testing.T) {
	// ADR 0008: cloud instances boot with the minimal bootstrap (no cluster
	// secrets); the full config is pulled over the management link.
	for _, role := range []string{"controlplane", "worker"} {
		t.Run(role, func(t *testing.T) {
			cfg := renderForTest(t, role)
			inst := firstInstance(t, cfg)
			meta := inst["metadata"].(map[string]any)
			if meta["user_data"] != "${var.bootstrap_config}" {
				t.Errorf("user_data = %v, want the bootstrap variable", meta["user_data"])
			}
		})
	}
}

func TestRender_NoTalosDataSourceOrProvider(t *testing.T) {
	// The talos provider rode the full-config data source; with pull delivery
	// it has no reason to be in a cloud workspace at all.
	cfg := renderForTest(t, "worker")
	if data, ok := cfg["data"].(map[string]any); ok {
		if _, bad := data["talos_machine_configuration"]; bad {
			t.Errorf("talos_machine_configuration data source must be gone: %v", data)
		}
	}
	rp := cfg["terraform"].(map[string]any)["required_providers"].(map[string]any)
	if _, ok := rp["talos"]; ok {
		t.Errorf("talos provider must not be required by the cloud renderer: %v", rp)
	}
}
