package metal

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// =====================================================================
// Provider renderer tests
// =====================================================================

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
	if err := unmarshal(out, &m); err != nil {
		t.Fatalf("generated config is not valid JSON: %v\n%s", err, out)
	}
	return m
}

// baseNG is a worker node group with two machines.
func baseNG() state.NodeGroupSpec {
	return state.NodeGroupSpec{
		Name:  "edge-workers",
		Role:  "worker",
		Count: 2,
		ProviderConfig: []byte(`{
			"schematicId": "ae1234deadbeef",
			"machines": {
				"2a01:e11:2440:2430:216:96ff:feec:93b6": {"installDisk": "/dev/nvme0n1", "storageDisk": "/dev/sda"},
				"2a01:e11:2440:2430:216:96ff:feec:93b7": {"installDisk": "/dev/nvme0n1", "storageDisk": "/dev/sda"}
			}
		}`),
	}
}

func TestRender_DeclaresRequiredProviders(t *testing.T) {
	m := render(t, baseNG())
	terraform := m["terraform"].(map[string]interface{})
	rps := terraform["required_providers"].(map[string]interface{})
	if _, ok := rps["talos"]; !ok {
		t.Errorf("missing required provider talos: %v", rps)
	}
	if _, ok := rps["random"]; !ok {
		t.Errorf("missing required provider random: %v", rps)
	}
}

func TestRender_UsesTalosMachineConfigurationApply(t *testing.T) {
	// The headline: config is pushed via talos_machine_configuration_apply
	// (TF resource), NOT SideroLink. This is the metal-distinguishing property.
	m := render(t, baseNG())
	resources := m["resource"].(map[string]interface{})
	if _, ok := resources["talos_machine_configuration_apply"]; !ok {
		t.Fatalf("missing talos_machine_configuration_apply resource (have %v)", resources)
	}
}

func TestRender_NodeTargetsByIP(t *testing.T) {
	// node = each.key → the machine IP. The map key IS the node target.
	apply := oneResource(t, render(t, baseNG()), "talos_machine_configuration_apply")
	if apply["node"] != "${each.key}" {
		t.Errorf("node = %v, want ${each.key} (the machine IP)", apply["node"])
	}
	forEach := apply["for_each"].(string)
	if forEach == "" {
		t.Error("apply must have for_each over machines")
	}
}

func TestRender_ApplyHasCleanOnDestroy(t *testing.T) {
	// on_destroy = { reset, graceful, reboot } — clean teardown.
	apply := oneResource(t, render(t, baseNG()), "talos_machine_configuration_apply")
	od := apply["on_destroy"].(map[string]interface{})
	if od["reset"] != true || od["graceful"] != true || od["reboot"] != true {
		t.Errorf("on_destroy wrong: %v", od)
	}
}

func TestRender_ApplyCreateBeforeDestroy(t *testing.T) {
	apply := oneResource(t, render(t, baseNG()), "talos_machine_configuration_apply")
	lc := apply["lifecycle"].([]interface{})[0].(map[string]interface{})
	if lc["create_before_destroy"] != true {
		t.Error("create_before_destroy should be true")
	}
}

func TestRender_UsesRandomPetWithKeepers(t *testing.T) {
	// Stable naming: one pet per machine keyed by IP, keeper = IP.
	pet := oneResource(t, render(t, baseNG()), "random_pet")
	keepers := pet["keepers"].(map[string]interface{})
	if keepers["ip"] != "${each.key}" {
		t.Errorf("keepers.ip = %v, want ${each.key}", keepers["ip"])
	}
}

func TestRender_HostnamePatchUsesPet(t *testing.T) {
	// The hostname patch references the pet id (talos-edge-w-<pet>), so it's
	// stable across applies.
	apply := oneResource(t, render(t, baseNG()), "talos_machine_configuration_apply")
	patches := apply["config_patches"].([]interface{})
	first := patches[0].(string)
	// The first patch must contain the pet id reference for hostname stability.
	if !contains(first, "random_pet") || !contains(first, "talos-edge-w-") {
		t.Errorf("first patch should be the pet-derived hostname, got %q", first)
	}
}

func TestRender_SchematicInstallImage(t *testing.T) {
	// schematicId set → install image patch emitted.
	apply := oneResource(t, render(t, baseNG()), "talos_machine_configuration_apply")
	patches := toStrSlice(apply["config_patches"])
	found := false
	for _, p := range patches {
		if contains(p, "factory.talos.dev/installer/ae1234deadbeef") {
			found = true
		}
	}
	if !found {
		t.Errorf("schematic install image patch missing: %v", patches)
	}
}

func TestRender_NoSchematicOmitsInstallImage(t *testing.T) {
	ng := baseNG()
	ng.ProviderConfig = []byte(`{"machines":{"2a01:e11::1":{"installDisk":"/dev/nvme0n1"}}}`)
	apply := oneResource(t, render(t, ng), "talos_machine_configuration_apply")
	for _, p := range toStrSlice(apply["config_patches"]) {
		if contains(p, "factory.talos.dev/installer") {
			t.Errorf("install image patch should be omitted without schematicId: %q", p)
		}
	}
}

func TestRender_NoSideroLinkReferences(t *testing.T) {
	// ADR 13 (rejected): NO SideroLink. The generated config must not mention it.
	out := rawRender(t, baseNG())
	lower := string(out)
	for _, bad := range []string{"siderolink", "jointoken", "wireguard_over_grpc", "kernel_arg"} {
		if contains(lower, bad) {
			t.Errorf("config references %q (SideroLink must not appear): %s", bad, out)
		}
	}
}

func TestRender_MultipleNodeGroupsDistinct(t *testing.T) {
	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	cp := baseNG()
	cp.Name = "controlplane"
	cp.Role = "controlplane"
	wk := baseNG()
	wk.Name = "workers"
	out, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: []state.NodeGroupSpec{cp, wk}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !contains(string(out), "controlplane_apply") || !contains(string(out), "workers_apply") {
		t.Errorf("expected distinct apply resources for both node groups:\n%s", out)
	}
}

func TestRender_RejectsMissingMachines(t *testing.T) {
	ng := baseNG()
	ng.ProviderConfig = []byte(`{"schematicId":"x"}`) // no machines
	if _, err := New().Render(provider.RenderRequest{Tenant: &state.Tenant{}, NodeGroups: []state.NodeGroupSpec{ng}}); err == nil {
		t.Fatal("want error for missing machines")
	}
}

func TestRender_RejectsMissingTenant(t *testing.T) {
	if _, err := New().Render(provider.RenderRequest{NodeGroups: []state.NodeGroupSpec{baseNG()}}); err == nil {
		t.Fatal("want error for missing tenant")
	}
}

func TestProvider_Type(t *testing.T) {
	if New().Type() != "metal" {
		t.Error("Type != metal")
	}
}

func TestProvider_Mappings(t *testing.T) {
	ms := New().Mappings()
	found := false
	for _, m := range ms {
		if m.TFType == "talos_machine_configuration_apply" && m.Kind == "Machine" {
			found = true
		}
	}
	if !found {
		t.Errorf("mappings missing talos_machine_configuration_apply→Machine: %v", ms)
	}
}

// =====================================================================
// Discovery scan tests
// =====================================================================

// startMaintenanceListener opens a TCP listener on localhost (a "Talos
// maintenance node" stub) and returns its address + a stop func.
func startMaintenanceListener(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestScan_FindsMaintenanceNode(t *testing.T) {
	// Spin up a fake Talos maintenance node on localhost and scan a /30 that
	// includes it. The scan must find it.
	addr, stop := startMaintenanceListener(t)
	defer stop()

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	// Build a /30 containing the listener's IP.
	ip := net.ParseIP(host).To4()
	ip[3] &= 0xFC // zero low 2 bits → /30 network base
	cidr := fmt.Sprintf("%s/30", ip.String())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodes, err := Scan(ctx, cidr, ScanConfig{Port: port, Timeout: 300 * time.Millisecond, Concurrency: 16})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	found := false
	for _, n := range nodes {
		if n.Address == host {
			found = true
		}
	}
	if !found {
		t.Fatalf("scan did not find the maintenance node at %s (found %v)", host, nodes)
	}
}

func TestScan_RespondsToCancellation(t *testing.T) {
	// A huge-but-bounded scan (/22 = 1022 hosts, under maxScanHosts) against a
	// dead range must abort promptly on ctx cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	start := time.Now()
	nodes, err := Scan(ctx, "127.0.0.0/30", ScanConfig{Timeout: time.Second})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	_ = nodes
	if elapsed > time.Second {
		t.Errorf("cancelled scan took %v, should abort promptly", elapsed)
	}
}

func TestScan_RejectsHugeSubnet(t *testing.T) {
	// A /8 is not enumerable. Scan must refuse.
	_, err := Scan(context.Background(), "10.0.0.0/8", ScanConfig{})
	if err == nil {
		t.Fatal("want error scanning a /8")
	}
}

func TestScan_RejectsInvalidCIDR(t *testing.T) {
	if _, err := Scan(context.Background(), "not-a-cidr", ScanConfig{}); err == nil {
		t.Fatal("want error for invalid CIDR")
	}
}

func TestScan_NoMaintenanceNodesReturnsEmpty(t *testing.T) {
	// A /30 on the loopback range with nothing listening (avoid 127.0.0.1).
	// Use 127.0.0.4/30 → hosts .5 .6 (4 is network, .7 is broadcast). Nothing
	// listens there, so the scan returns empty.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	nodes, err := Scan(ctx, "127.0.0.4/30", ScanConfig{Port: 50000, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes on a dead range, got %v", nodes)
	}
}

// =====================================================================
// helpers
// =====================================================================

func oneResource(t *testing.T, m map[string]interface{}, typ string) map[string]interface{} {
	t.Helper()
	resources := m["resource"].(map[string]interface{})
	byName, ok := resources[typ].(map[string]interface{})
	if !ok {
		t.Fatalf("no %q resources (have %v)", typ, resources)
	}
	for _, v := range byName {
		return v.(map[string]interface{})
	}
	t.Fatalf("no %q resource instances", typ)
	return nil
}

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

func contains(s, want string) bool {
	for i := 0; i+len(want) <= len(s); i++ {
		if s[i:i+len(want)] == want {
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

// unmarshal is a thin wrapper to keep imports tidy in the test file.
func unmarshal(b []byte, m *map[string]interface{}) error {
	return jsonUnmarshal(b, m)
}
