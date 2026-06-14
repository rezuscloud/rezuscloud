package oci

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// render is a test helper: render a single node group and return the parsed
// config as a generic map (so assertions can navigate the structure).
func render(t *testing.T, ng state.NodeGroupSpec) map[string]interface{} {
	t.Helper()
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo-tenant"
	out, err := p.Render(provider.RenderRequest{
		Tenant:     tenant,
		NodeGroups: []state.NodeGroupSpec{ng},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("generated config is not valid JSON: %v\n%s", err, out)
	}
	return m
}

func baseNG() state.NodeGroupSpec {
	return state.NodeGroupSpec{
		Name:           "controlplane",
		Role:           "controlplane",
		Count:          3,
		ProviderClass:  "oci:VM.Standard.A1.Flex",
		ProviderConfig: []byte(`{"compartmentOcid":"ocid1.compartment.oc1..demo","subnetId":"ocid1.subnet.oc1.phx.demo","imageOcid":"ocid1.image.oc1.phx..talos","nsgId":"ocid1.nsg.demo","ocpus":4,"memoryGb":24,"bootVolumeGb":50}`),
	}
}

func TestRender_DeclaresRequiredProviders(t *testing.T) {
	m := render(t, baseNG())
	terraform := m["terraform"].(map[string]interface{})
	rps := terraform["required_providers"].(map[string]interface{})
	sources := map[string]string{}
	for name, body := range rps {
		sources[name] = body.(map[string]interface{})["source"].(string)
	}
	for _, want := range []string{"oci", "talos", "random"} {
		if _, ok := sources[want]; !ok {
			t.Errorf("missing required provider %q (have %v)", want, sources)
		}
	}
	if sources["oci"] != "oracle/oci" {
		t.Errorf("oci source = %q, want oracle/oci", sources["oci"])
	}
	if sources["talos"] != "siderolabs/talos" {
		t.Errorf("talos source = %q, want siderolabs/talos", sources["talos"])
	}
}

func TestRender_InstanceHasProvenLifecycle(t *testing.T) {
	// The headline safety property: lifecycle.ignore_changes must include
	// metadata.user_data and defined_tags. Without it, a Talos config change
	// forces instance recreation → downtime, new IPs, stale nodes.
	m := render(t, baseNG())
	inst := firstInstance(t, m)
	lifecycle := inst["lifecycle"].([]interface{})[0].(map[string]interface{})
	ignoresRaw := lifecycle["ignore_changes"].([]interface{})
	ignores := toStringSlice(ignoresRaw)
	if !contains(ignores, "metadata.user_data") {
		t.Errorf("ignore_changes missing metadata.user_data: %v", ignores)
	}
	if !contains(ignores, "defined_tags") {
		t.Errorf("ignore_changes missing defined_tags: %v", ignores)
	}
	if lifecycle["create_before_destroy"] != true {
		t.Error("create_before_destroy not set")
	}
}

func TestRender_UsesRandomPetForStableNaming(t *testing.T) {
	// No keepers → pets regenerate on re-apply → cascading recreation.
	m := render(t, baseNG())
	pet := firstPet(t, m)
	if pet["count"].(float64) != 3 {
		t.Errorf("pet count = %v, want 3", pet["count"])
	}
	keepers := pet["keepers"].(map[string]interface{})
	if keepers["index"] != "${count.index}" {
		t.Errorf("pet keepers.index = %v, want ${count.index}", keepers["index"])
	}
}

func TestRender_InstanceShapeAndConfig(t *testing.T) {
	m := render(t, baseNG())
	inst := firstInstance(t, m)
	if inst["shape"] != "VM.Standard.A1.Flex" {
		t.Errorf("shape = %v, want VM.Standard.A1.Flex", inst["shape"])
	}
	// Flex shape must emit shape_config.
	sc, ok := inst["shape_config"].([]interface{})
	if !ok {
		t.Fatal("Flex shape missing shape_config block")
	}
	cfg := sc[0].(map[string]interface{})
	if cfg["ocpus"].(float64) != 4 {
		t.Errorf("ocpus = %v, want 4", cfg["ocpus"])
	}
	if cfg["memory_in_gbs"].(float64) != 24 {
		t.Errorf("memory_in_gbs = %v, want 24", cfg["memory_in_gbs"])
	}
}

func TestRender_FixedShapeOmitsShapeConfig(t *testing.T) {
	ng := baseNG()
	ng.ProviderClass = "oci:VM.Standard.E3.Flex" // still flex; use a real fixed shape
	ng.ProviderClass = "oci:VM.Standard2.1"      // fixed shape
	// also override shape via config to be sure
	m := render(t, ng)
	inst := firstInstance(t, m)
	if _, ok := inst["shape_config"]; ok {
		t.Error("fixed shape should NOT emit shape_config")
	}
}

func TestRender_CountFromNodeGroup(t *testing.T) {
	ng := baseNG()
	ng.Count = 5
	m := render(t, ng)
	inst := firstInstance(t, m)
	forEach, _ := inst["for_each"].(string)
	if !strings.Contains(forEach, "random_pet") {
		t.Errorf("for_each should iterate random_pet: %q", forEach)
	}
	if firstPet(t, m)["count"].(float64) != 5 {
		t.Error("pet count should be 5")
	}
}

func TestRender_DefaultsBootVolumeToFifty(t *testing.T) {
	ng := baseNG()
	ng.ProviderConfig = []byte(`{"compartmentOcid":"ocid1.c","subnetId":"ocid1.s"}`) // no bootVolumeGb
	ng.ProviderClass = "oci:VM.Standard2.1"
	m := render(t, ng)
	inst := firstInstance(t, m)
	sd := inst["source_details"].([]interface{})[0].(map[string]interface{})
	if sd["boot_volume_size_in_gbs"] != "50" {
		t.Errorf("default boot volume = %v, want 50", sd["boot_volume_size_in_gbs"])
	}
}

func TestRender_NoCredentialsEmbedded(t *testing.T) {
	// ADR 22: creds come from the process env, never in the config.
	out := rawRender(t, baseNG())
	if strings.Contains(string(out), "private_key") || strings.Contains(string(out), "fingerprint") {
		t.Error("generated config embeds credentials (must use env provider auth)")
	}
}

func TestRender_MultipleNodeGroupsGetDistinctResources(t *testing.T) {
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	cp := baseNG()
	cp.Name = "cp"
	wk := baseNG()
	wk.Name = "workers"
	wk.Role = "worker"
	out, err := p.Render(provider.RenderRequest{
		Tenant:     tenant,
		NodeGroups: []state.NodeGroupSpec{cp, wk},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "cp_instance") || !strings.Contains(string(out), "workers_instance") {
		t.Errorf("expected distinct instance resources for both node groups:\n%s", out)
	}
}

func TestRender_RejectsMissingCompartment(t *testing.T) {
	ng := baseNG()
	ng.ProviderConfig = []byte(`{"subnetId":"ocid1.s"}`) // no compartment
	_, err := New().Render(provider.RenderRequest{
		Tenant:     &state.Tenant{},
		NodeGroups: []state.NodeGroupSpec{ng},
	})
	if err == nil || !strings.Contains(err.Error(), "compartmentOcid") {
		t.Fatalf("want error about compartmentOcid, got %v", err)
	}
}

func TestRender_RejectsMissingTenant(t *testing.T) {
	_, err := New().Render(provider.RenderRequest{NodeGroups: []state.NodeGroupSpec{baseNG()}})
	if err == nil {
		t.Fatal("want error for missing tenant")
	}
}

func TestRender_RejectsEmptyNodeGroups(t *testing.T) {
	_, err := New().Render(provider.RenderRequest{Tenant: &state.Tenant{}})
	if err == nil {
		t.Fatal("want error for no node groups")
	}
}

func TestRender_DisplayNameUsesRolePrefix(t *testing.T) {
	m := render(t, baseNG()) // controlplane
	inst := firstInstance(t, m)
	dn := inst["display_name"].(string)
	if !strings.Contains(dn, "talos-oci-c-") {
		t.Errorf("controlplane display_name should use c- prefix: %q", dn)
	}
	// worker variant
	ng := baseNG()
	ng.Role = "worker"
	m2 := render(t, ng)
	if dn2 := firstInstance(t, m2)["display_name"].(string); !strings.Contains(dn2, "talos-oci-w-") {
		t.Errorf("worker display_name should use w- prefix: %q", dn2)
	}
}

func TestProvider_Type(t *testing.T) {
	if got := New().Type(); got != "oci" {
		t.Errorf("Type = %q, want oci", got)
	}
}

func TestProvider_Mappings(t *testing.T) {
	ms := New().Mappings()
	found := false
	for _, m := range ms {
		if m.TFType == "oci_core_instance" && m.Kind == "Machine" {
			found = true
		}
	}
	if !found {
		t.Errorf("mappings missing oci_core_instance→Machine: %v", ms)
	}
}

// --- registry test ---

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := provider.NewRegistry()
	p := New()
	r.Register(p)
	if r.Lookup("oci") != p {
		t.Error("Lookup(oci) did not return registered provider")
	}
	if r.Lookup("nope") != nil {
		t.Error("Lookup for unregistered type should be nil")
	}
	if len(r.All()) != 1 {
		t.Errorf("All() = %d providers, want 1", len(r.All()))
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(New())
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	r.Register(New())
}

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

func firstInstance(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	resources := m["resource"].(map[string]interface{})
	instObj := resources["oci_core_instance"].(map[string]interface{})
	for _, v := range instObj {
		return v.(map[string]interface{})
	}
	t.Fatal("no oci_core_instance in rendered config")
	return nil
}

// firstPet returns the first random_pet body in the rendered config.
func firstPet(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	resources := m["resource"].(map[string]interface{})
	petObj := resources["random_pet"].(map[string]interface{})
	for _, v := range petObj {
		return v.(map[string]interface{})
	}
	t.Fatal("no random_pet in rendered config")
	return nil
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func toStringSlice(v []interface{}) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = x.(string)
	}
	return out
}
