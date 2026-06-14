package openstack

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

func render(t *testing.T, ng state.NodeGroupSpec) map[string]interface{} {
	t.Helper()
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	out, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: []state.NodeGroupSpec{ng}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("generated config is not valid JSON: %v\n%s", err, out)
	}
	return m
}

// baseNG is a worker node group with a data volume.
func baseNG() state.NodeGroupSpec {
	return state.NodeGroupSpec{
		Name:          "workers",
		Role:          "worker",
		Count:         2,
		TalosVersion:  "v1.13.4",
		ProviderClass: "openstack:SCS-16V-32-100",
		ProviderConfig: []byte(`{
			"bootVolumeSizeGb": 50,
			"dataVolumeSizeGb": 250
		}`),
	}
}

func resByType(t *testing.T, m map[string]interface{}, typ string) map[string]interface{} {
	t.Helper()
	resources := m["resource"].(map[string]interface{})
	byName, ok := resources[typ].(map[string]interface{})
	if !ok {
		t.Fatalf("no %q resources in config (have %v)", typ, resources)
	}
	return byName
}

func oneResource(t *testing.T, m map[string]interface{}, typ string) map[string]interface{} {
	t.Helper()
	byName := resByType(t, m, typ)
	for _, v := range byName {
		return v.(map[string]interface{})
	}
	t.Fatalf("no %q resource instances", typ)
	return nil
}

func TestRender_DeclaresRequiredProviders(t *testing.T) {
	m := render(t, baseNG())
	terraform := m["terraform"].(map[string]interface{})
	rps := terraform["required_providers"].(map[string]interface{})
	for _, want := range []string{"openstack", "talos", "random"} {
		if _, ok := rps[want]; !ok {
			t.Errorf("missing required provider %q (have %v)", want, rps)
		}
	}
	if rps["openstack"].(map[string]interface{})["source"] != "terraform-provider-openstack/openstack" {
		t.Errorf("openstack source wrong: %v", rps["openstack"])
	}
}

func TestRender_InstanceUsesConfigDrive(t *testing.T) {
	// The headline delivery mechanism: config_drive = true (OpenStack platform
	// reads the config-drive 'config-2' label; nocloud would not).
	inst := oneResource(t, render(t, baseNG()), "openstack_compute_instance_v2")
	if inst["config_drive"] != true {
		t.Errorf("config_drive = %v, want true", inst["config_drive"])
	}
	ud, _ := inst["user_data"].(string)
	if !strings.Contains(ud, "base64encode(data.talos_machine_configuration.worker") {
		t.Errorf("user_data should be base64 of talos config: %q", ud)
	}
}

func TestRender_TwoCinderVolumes(t *testing.T) {
	// boot + data volumes with the correct Cinder volume types.
	m := render(t, baseNG())
	vols := resByType(t, m, "openstack_blockstorage_volume_v3")
	if len(vols) != 2 {
		t.Fatalf("expected 2 volume resources (boot + data), got %d: %v", len(vols), vols)
	}
	boot := oneResource(t, m, "openstack_blockstorage_volume_v3")
	// find boot by size 50 vs data 250
	for _, v := range vols {
		body := v.(map[string]interface{})
		switch int(body["size"].(float64)) {
		case 50:
			boot = body
		}
	}
	if boot["volume_type"] != "ssd-ephemeral" {
		t.Errorf("boot volume_type = %v, want ssd-ephemeral (lvm-2)", boot["volume_type"])
	}
	if boot["image_id"] == nil {
		t.Error("boot volume must reference the Glance image id")
	}
	// boot volume ignores image_id changes (upgrades via talosctl, not recreate)
	lc := boot["lifecycle"].([]interface{})[0].(map[string]interface{})
	if !contains(toStrSlice(lc["ignore_changes"]), "image_id") {
		t.Errorf("boot volume should ignore_changes image_id: %v", lc["ignore_changes"])
	}
}

func TestRender_DataVolumeOmittedWhenZero(t *testing.T) {
	ng := baseNG()
	ng.ProviderConfig = []byte(`{"bootVolumeSizeGb":50,"dataVolumeSizeGb":0}`)
	m := render(t, ng)
	vols := resByType(t, m, "openstack_blockstorage_volume_v3")
	if len(vols) != 1 {
		t.Fatalf("expected 1 volume (boot only), got %d", len(vols))
	}
	// And the instance should have a single block_device.
	inst := oneResource(t, m, "openstack_compute_instance_v2")
	bds := inst["block_device"].([]interface{})
	if len(bds) != 1 {
		t.Errorf("expected 1 block_device, got %d", len(bds))
	}
}

func TestRender_InstanceBootedFromVolume(t *testing.T) {
	inst := oneResource(t, render(t, baseNG()), "openstack_compute_instance_v2")
	bds := inst["block_device"].([]interface{})
	boot := bds[0].(map[string]interface{})
	if boot["source_type"] != "volume" {
		t.Errorf("boot block_device source_type = %v, want volume", boot["source_type"])
	}
	if boot["destination_type"] != "volume" {
		t.Errorf("boot block_device destination_type = %v, want volume", boot["destination_type"])
	}
	if int(boot["boot_index"].(float64)) != 0 {
		t.Errorf("boot_index = %v, want 0", boot["boot_index"])
	}
}

func TestRender_InstanceLifecycleIgnoresUserData(t *testing.T) {
	// Talos config changes go through talosctl apply-config, never VM recreation.
	inst := oneResource(t, render(t, baseNG()), "openstack_compute_instance_v2")
	lc := inst["lifecycle"].([]interface{})[0].(map[string]interface{})
	ignores := toStrSlice(lc["ignore_changes"])
	for _, want := range []string{"image_id", "block_device", "user_data"} {
		if !contains(ignores, want) {
			t.Errorf("instance ignore_changes missing %q: %v", want, ignores)
		}
	}
}

func TestRender_InstanceStopsBeforeDestroy(t *testing.T) {
	inst := oneResource(t, render(t, baseNG()), "openstack_compute_instance_v2")
	if inst["stop_before_destroy"] != true {
		t.Error("stop_before_destroy should be true")
	}
}

func TestRender_FlavorFromProviderClass(t *testing.T) {
	inst := oneResource(t, render(t, baseNG()), "openstack_compute_instance_v2")
	if inst["flavor_name"] != "SCS-16V-32-100" {
		t.Errorf("flavor_name = %v, want SCS-16V-32-100", inst["flavor_name"])
	}
}

func TestRender_ImageNameDefaultsFromTalosVersion(t *testing.T) {
	m := render(t, baseNG())
	ds := m["data"].(map[string]interface{})["openstack_images_image_v2"].(map[string]interface{})
	for _, v := range ds {
		img := v.(map[string]interface{})
		if img["name"] != "talos-v1.13.4-openstack-amd64" {
			t.Errorf("image name = %v, want talos-v1.13.4-openstack-amd64", img["name"])
		}
	}
}

func TestRender_UsesRandomPetWithKeepers(t *testing.T) {
	pet := oneResource(t, render(t, baseNG()), "random_pet")
	if int(pet["count"].(float64)) != 2 {
		t.Errorf("pet count = %v, want 2", pet["count"])
	}
	keepers := pet["keepers"].(map[string]interface{})
	if keepers["index"] != "${count.index}" {
		t.Errorf("keepers.index = %v, want ${count.index}", keepers["index"])
	}
}

func TestRender_PermissiveSecurityGroup(t *testing.T) {
	// 1 secgroup + 4 rules (ingress/egress × v4/v6).
	m := render(t, baseNG())
	sg := resByType(t, m, "openstack_networking_secgroup_v2")
	if len(sg) != 1 {
		t.Fatalf("expected 1 secgroup, got %d", len(sg))
	}
	rules := resByType(t, m, "openstack_networking_secgroup_rule_v2")
	if len(rules) != 4 {
		t.Fatalf("expected 4 secgroup rules, got %d", len(rules))
	}
}

func TestRender_CountThreadsThrough(t *testing.T) {
	ng := baseNG()
	ng.Count = 3
	pet := oneResource(t, render(t, ng), "random_pet")
	if int(pet["count"].(float64)) != 3 {
		t.Errorf("pet count = %v, want 3", pet["count"])
	}
}

func TestRender_FixedIPv4Optional(t *testing.T) {
	ng := baseNG()
	ng.ProviderConfig = []byte(`{"bootVolumeSizeGb":50,"dataVolumeSizeGb":250,"fixedIpV4":"192.168.7.150"}`)
	inst := oneResource(t, render(t, ng), "openstack_compute_instance_v2")
	net := inst["network"].([]interface{})[0].(map[string]interface{})
	if net["fixed_ip_v4"] != "192.168.7.150" {
		t.Errorf("fixed_ip_v4 = %v, want 192.168.7.150", net["fixed_ip_v4"])
	}
}

func TestRender_NoCredentialsEmbedded(t *testing.T) {
	out := rawRender(t, baseNG())
	lower := strings.ToLower(string(out))
	for _, bad := range []string{"password", "token", "os_auth", "application_credential"} {
		if strings.Contains(lower, bad) {
			t.Errorf("config embeds %q (must use env provider auth)", bad)
		}
	}
}

func TestRender_RejectsMissingFlavor(t *testing.T) {
	ng := baseNG()
	ng.ProviderClass = ""
	ng.ProviderConfig = []byte(`{"bootVolumeSizeGb":50}`)
	if _, err := New().Render(provider.RenderRequest{Tenant: &state.Tenant{}, NodeGroups: []state.NodeGroupSpec{ng}}); err == nil {
		t.Fatal("want error for missing flavor")
	}
}

func TestRender_RejectsMissingTenant(t *testing.T) {
	if _, err := New().Render(provider.RenderRequest{NodeGroups: []state.NodeGroupSpec{baseNG()}}); err == nil {
		t.Fatal("want error for missing tenant")
	}
}

func TestProvider_Type(t *testing.T) {
	if New().Type() != "openstack" {
		t.Error("Type != openstack")
	}
}

func TestProvider_Mappings(t *testing.T) {
	ms := New().Mappings()
	found := false
	for _, m := range ms {
		if m.TFType == "openstack_compute_instance_v2" && m.Kind == "Machine" {
			found = true
		}
	}
	if !found {
		t.Errorf("mappings missing openstack_compute_instance_v2→Machine: %v", ms)
	}
}

func TestRegistry_BothProvidersCoexist(t *testing.T) {
	// The shared TFConfig builder means OCI + OpenStack can register together.
	// (oci.New can't be imported here without a cross-package test; a fake
	// provider proves the registry holds heterogeneous providers.)
	r := provider.NewRegistry()
	r.Register(New())
	r.Register(fakeProvider{typ: "oci"})
	if r.Lookup("oci") == nil || r.Lookup("openstack") == nil {
		t.Error("registry lost one of the two providers")
	}
}

// fakeProvider is a minimal Provider for registry tests.
type fakeProvider struct{ typ string }

func (f fakeProvider) Type() string                                  { return f.typ }
func (f fakeProvider) Render(provider.RenderRequest) ([]byte, error) { return nil, nil }
func (f fakeProvider) Mappings() []provider.TFResourceMapping        { return nil }

// --- helpers ---

func rawRender(t *testing.T, ng state.NodeGroupSpec) []byte {
	t.Helper()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	out, err := New().Render(provider.RenderRequest{Tenant: tenant, NodeGroups: []state.NodeGroupSpec{ng}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func toStrSlice(v interface{}) []string {
	s := v.([]interface{})
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.(string)
	}
	return out
}
