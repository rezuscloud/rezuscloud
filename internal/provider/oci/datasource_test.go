package oci

import (
	"encoding/json"
	"strings"
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

func TestRender_EmitsTalosConfigDataSource_PerRole(t *testing.T) {
	for _, role := range []string{"controlplane", "worker"} {
		t.Run(role, func(t *testing.T) {
			cfg := renderForTest(t, role)
			data, ok := cfg["data"].(map[string]any)
			if !ok {
				t.Fatal("no data block in config")
			}
			talosDS, ok := data["talos_machine_configuration"].(map[string]any)
			if !ok {
				t.Fatal("no talos_machine_configuration data source")
			}
			body, ok := talosDS[role].(map[string]any)
			if !ok {
				t.Fatalf("no data source named %q", role)
			}
			if body["machine_type"] != role {
				t.Errorf("machine_type = %v, want %q", body["machine_type"], role)
			}
			if body["machine_secrets"] != "${var.machine_secrets}" {
				t.Errorf("machine_secrets = %v, want var reference", body["machine_secrets"])
			}
			if body["cluster_endpoint"] != "${var.cluster_endpoint}" {
				t.Errorf("cluster_endpoint = %v, want var reference", body["cluster_endpoint"])
			}
		})
	}
}

func TestRender_TalosConfigRequiredProviderDeclared(t *testing.T) {
	out, err := New().Render(provider.RenderRequest{
		Tenant: &state.Tenant{Metadata: state.Metadata{Name: "demo"}},
		NodeGroups: []state.NodeGroupSpec{{
			Name: "cp", Role: "controlplane", Count: 1, ProviderClass: "oci:test",
			ProviderConfig: []byte(`{"compartmentOcid":"x","subnetId":"y","imageOcid":"z"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The talos provider must be in required_providers so the data source resolves.
	if !strings.Contains(string(out), `"talos"`) {
		t.Error("talos provider not declared in required_providers")
	}
}

func TestRender_MultipleNodeGroupsShareDataSourceByRole(t *testing.T) {
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	ngs := []state.NodeGroupSpec{
		{Name: "cp-a", Role: "controlplane", Count: 1, ProviderClass: "oci:test",
			ProviderConfig: []byte(`{"compartmentOcid":"x","subnetId":"y","imageOcid":"z"}`)},
		{Name: "cp-b", Role: "controlplane", Count: 1, ProviderClass: "oci:test",
			ProviderConfig: []byte(`{"compartmentOcid":"x","subnetId":"y","imageOcid":"z"}`)},
		{Name: "workers", Role: "worker", Count: 2, ProviderClass: "oci:test",
			ProviderConfig: []byte(`{"compartmentOcid":"x","subnetId":"y","imageOcid":"z"}`)},
	}
	out, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: ngs})
	if err != nil {
		t.Fatal(err)
	}

	// Parse the config and verify the data block has exactly one entry per role.
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	data := cfg["data"].(map[string]any)
	talosDS := data["talos_machine_configuration"].(map[string]any)

	// Two node groups with role=controlplane must share ONE data source.
	if _, ok := talosDS["controlplane"]; !ok {
		t.Error("missing controlplane data source")
	}
	if _, ok := talosDS["worker"]; !ok {
		t.Error("missing worker data source")
	}
	// The data source map should have exactly 2 keys (controlplane + worker).
	if len(talosDS) != 2 {
		t.Errorf("expected exactly 2 talos_machine_configuration data sources, got %d: %v", len(talosDS), talosDS)
	}
}
