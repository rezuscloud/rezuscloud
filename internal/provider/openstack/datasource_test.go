package openstack

import (
	"encoding/json"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

func renderForTest(t *testing.T, role string) map[string]any {
	t.Helper()
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	ng := state.NodeGroupSpec{
		Name:          role,
		Role:          role,
		Count:         1,
		ProviderClass: "openstack:m1.small",
		ProviderConfig: []byte(`{
			"extNetName": "public",
			"imageName": "talos",
			"flavorName": "m1.small",
			"bootVolumeSizeGb": 50
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
		})
	}
}
