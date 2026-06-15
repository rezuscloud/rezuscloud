package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/provider/metal"
	"github.com/rezuscloud/rezuscloud/internal/provider/oci"
	"github.com/rezuscloud/rezuscloud/internal/provider/openstack"
)

// fakeSource returns a fixed state blob per tenant.
type fakeSource struct {
	mu    sync.Mutex
	state map[string][]byte
}

func (f *fakeSource) State(_ context.Context, tenant string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[tenant], nil
}

func (f *fakeSource) set(tenant string, blob []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[tenant] = blob
}

func newFakeSource() *fakeSource {
	return &fakeSource{state: make(map[string][]byte)}
}

// stateBlob builds a TF state JSON blob from a list of resources.
func stateBlob(serial int64, resources ...stateResource) []byte {
	s := parsedState{Version: 4, Serial: serial, Lineage: "test", Resources: resources}
	b, _ := jsonMarshal(s)
	return b
}

// inst builds a single-instance resource.
func inst(tfType, name string, attrs map[string]interface{}) stateResource {
	return stateResource{Mode: "managed", Type: tfType, Name: name, Instances: []stateInstance{{Attributes: attrs}}}
}

// forEachInst builds a for_each resource with the given instance keys → attrs.
func forEachInst(tfType, name string, instances map[string]map[string]interface{}) stateResource {
	r := stateResource{Mode: "managed", Type: tfType, Name: name}
	for key, attrs := range instances {
		keyJSON, _ := json.Marshal(key)
		r.Instances = append(r.Instances, stateInstance{IndexKey: keyJSON, Attributes: attrs})
	}
	return r
}

// registry with all three real providers, so mappings resolve correctly.
func testRegistry() *provider.Registry {
	r := provider.NewRegistry()
	r.Register(oci.New())
	r.Register(openstack.New())
	r.Register(metal.New())
	return r
}

// newIndex wires the real registry + the Machine extractor.
func newIndex(src StateSource) *Index {
	idx := New(src, testRegistry())
	idx.RegisterExtractor("Machine", extractMachine)
	return idx
}

// =====================================================================
// State parsing
// =====================================================================

func TestParseState_ValidBlob(t *testing.T) {
	blob := stateBlob(7, inst("null_resource", "x", map[string]interface{}{"id": "abc"}))
	s, err := parseState(blob)
	if err != nil {
		t.Fatalf("parseState: %v", err)
	}
	if s.Serial != 7 {
		t.Errorf("Serial = %d, want 7", s.Serial)
	}
	if len(s.Resources) != 1 {
		t.Fatalf("Resources = %d, want 1", len(s.Resources))
	}
	if s.Resources[0].Type != "null_resource" {
		t.Errorf("Type = %q", s.Resources[0].Type)
	}
}

func TestParseState_InvalidJSON(t *testing.T) {
	if _, err := parseState([]byte("not json")); err == nil {
		t.Fatal("want error for invalid JSON")
	}
}

func TestInstanceName_Singleton(t *testing.T) {
	if got := instanceName("cp", nil); got != "cp" {
		t.Errorf("singleton name = %q, want cp", got)
	}
}

func TestInstanceName_ForEachStringKey(t *testing.T) {
	key, _ := json.Marshal("2a01::1")
	if got := instanceName("worker", key); got != "worker-2a01::1" {
		t.Errorf("for_each name = %q, want worker-2a01::1", got)
	}
}

func TestInstanceIndexSuffix_ForEachString(t *testing.T) {
	key, _ := json.Marshal("alpha")
	suf := stateInstance{IndexKey: key}.IndexSuffix()
	// for_each string keys are JSON-quoted in the address: ["alpha"]
	if suf != `["alpha"]` {
		t.Errorf("for_each suffix = %q, want [\"alpha\"]", suf)
	}
}

func TestInstanceIndexSuffix_CountNumber(t *testing.T) {
	suf := stateInstance{IndexKey: json.RawMessage(`0`)}.IndexSuffix()
	if suf != `[0]` {
		t.Errorf("count suffix = %q, want [0]", suf)
	}
}

func TestInstanceIndexSuffix_Singleton(t *testing.T) {
	if suf := (stateInstance{}).IndexSuffix(); suf != "" {
		t.Errorf("singleton suffix = %q, want empty", suf)
	}
}

// =====================================================================
// Index rebuild + lookup + list
// =====================================================================

func TestRebuild_ProjectsMappedResources(t *testing.T) {
	src := newFakeSource()
	src.set("t1", stateBlob(3,
		inst("oci_core_instance", "cp", map[string]interface{}{
			"id":           "ocid1.instance.x",
			"display_name": "talos-oci-c-panther",
			"shape":        "VM.Standard.A1.Flex",
		}),
	))
	idx := newIndex(src)

	n, err := idx.Rebuild(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if n != 1 {
		t.Fatalf("projected %d, want 1", n)
	}
	got, ok := idx.Lookup("t1", "oci_core_instance", "cp")
	if !ok {
		t.Fatal("Lookup failed")
	}
	if got.Kind != "Machine" {
		t.Errorf("Kind = %q, want Machine", got.Kind)
	}
	if got.StateSerial != 3 {
		t.Errorf("StateSerial = %d, want 3", got.StateSerial)
	}
	spec := got.Spec
	if spec["providerId"] != "ocid1.instance.x" {
		t.Errorf("providerId = %v", spec["providerId"])
	}
	if spec["hostname"] != "talos-oci-c-panther" {
		t.Errorf("hostname = %v", spec["hostname"])
	}
	if spec["shape"] != "VM.Standard.A1.Flex" {
		t.Errorf("shape = %v", spec["shape"])
	}
}

func TestRebuild_SkipsUnmappedTypes(t *testing.T) {
	// null_resource is not declared by any provider → skipped.
	src := newFakeSource()
	src.set("t1", stateBlob(1,
		inst("null_resource", "x", map[string]interface{}{"id": "abc"}),
	))
	idx := newIndex(src)

	n, err := idx.Rebuild(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if n != 0 {
		t.Errorf("projected %d unmapped resources, want 0", n)
	}
	if got := idx.List("t1", ""); len(got) != 0 {
		t.Errorf("List returned %d, want 0", len(got))
	}
}

func TestRebuild_EmptyStateClearsIndex(t *testing.T) {
	// State present, then deleted (empty blob) → index cleared.
	src := newFakeSource()
	src.set("t1", stateBlob(1, inst("oci_core_instance", "cp", map[string]interface{}{"id": "x"})))
	idx := newIndex(src)
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if got := idx.List("t1", ""); len(got) != 1 {
		t.Fatalf("expected 1 after first rebuild, got %d", len(got))
	}
	// Now empty state.
	src.set("t1", nil)
	if n, err := idx.Rebuild(context.Background(), "t1"); err != nil || n != 0 {
		t.Fatalf("second rebuild: n=%d err=%v", n, err)
	}
	if got := idx.List("t1", ""); len(got) != 0 {
		t.Errorf("index not cleared after empty state: %d", len(got))
	}
	if idx.Serial("t1") != 0 {
		t.Errorf("serial not reset: %d", idx.Serial("t1"))
	}
}

func TestRebuild_IsIdempotentReproducible(t *testing.T) {
	// The headline acceptance criterion: delete + reapply reproduces the index
	// exactly. Two rebuilds from the same blob yield identical resources.
	src := newFakeSource()
	blob := stateBlob(5,
		inst("openstack_compute_instance_v2", "w", map[string]interface{}{
			"id":          "uuid-1",
			"name":        "talos-os-w-phoenix",
			"flavor_name": "SCS-16V-32-100",
		}),
	)
	src.set("t1", blob)
	idx := newIndex(src)

	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	first := idx.List("t1", "")
	if len(first) != 1 {
		t.Fatalf("first rebuild: %d, want 1", len(first))
	}
	// Rebuild again — must be identical.
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	second := idx.List("t1", "")
	if len(second) != 1 {
		t.Fatalf("second rebuild: %d, want 1", len(second))
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Errorf("index not reproducible:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestRebuild_ForEachInstancesAllProjected(t *testing.T) {
	// A for_each resource with N instances → N projected resources, each with
	// a distinct name (resource-key).
	src := newFakeSource()
	src.set("t1", stateBlob(2,
		forEachInst("talos_machine_configuration_apply", "edge",
			map[string]map[string]interface{}{
				"2a01::1": {"node": "2a01::1"},
				"2a01::2": {"node": "2a01::2"},
			}),
	))
	idx := newIndex(src)

	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	all := idx.List("t1", "Machine")
	if len(all) != 2 {
		t.Fatalf("projected %d machines, want 2", len(all))
	}
	// Each instance's Name is the resource name + key.
	names := map[string]bool{}
	for _, r := range all {
		names[r.Name] = true
	}
	if !names["edge-2a01::1"] || !names["edge-2a01::2"] {
		t.Errorf("instance names wrong: %v", names)
	}
}

func TestRebuild_SerialBumpsWithState(t *testing.T) {
	src := newFakeSource()
	idx := newIndex(src)

	src.set("t1", stateBlob(3, inst("oci_core_instance", "cp", map[string]interface{}{"id": "a"})))
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if idx.Serial("t1") != 3 {
		t.Errorf("serial = %d, want 3", idx.Serial("t1"))
	}
	src.set("t1", stateBlob(4, inst("oci_core_instance", "cp", map[string]interface{}{"id": "a"})))
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if idx.Serial("t1") != 4 {
		t.Errorf("serial = %d, want 4", idx.Serial("t1"))
	}
}

func TestRebuild_MultipleProvidersSameTenant(t *testing.T) {
	// A tenant can run multiple providers — OCI + OpenStack + metal machines
	// all project to Kind=Machine, each with its own extractor shape.
	src := newFakeSource()
	src.set("mixed", stateBlob(1,
		inst("oci_core_instance", "cp", map[string]interface{}{"id": "ocid-x", "display_name": "oci-cp", "shape": "Flex"}),
		inst("openstack_compute_instance_v2", "w", map[string]interface{}{"id": "uuid-y", "name": "os-w", "flavor_name": "SCS-16V-32-100"}),
		inst("talos_machine_configuration_apply", "edge", map[string]interface{}{"node": "2a01::1"}),
	))
	idx := newIndex(src)

	if _, err := idx.Rebuild(context.Background(), "mixed"); err != nil {
		t.Fatal(err)
	}
	machines := idx.List("mixed", "Machine")
	if len(machines) != 3 {
		t.Fatalf("projected %d machines, want 3 (one per provider)", len(machines))
	}
}

func TestList_FilterByKind(t *testing.T) {
	src := newFakeSource()
	src.set("t1", stateBlob(1, inst("oci_core_instance", "cp", map[string]interface{}{"id": "x"})))
	idx := newIndex(src)
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if got := idx.List("t1", "Machine"); len(got) != 1 {
		t.Errorf("Machine filter: %d, want 1", len(got))
	}
	if got := idx.List("t1", "NodeGroup"); len(got) != 0 {
		t.Errorf("NodeGroup filter: %d, want 0", len(got))
	}
}

func TestList_StableOrder(t *testing.T) {
	src := newFakeSource()
	src.set("t1", stateBlob(1,
		inst("oci_core_instance", "zzz", map[string]interface{}{"id": "1"}),
		inst("oci_core_instance", "aaa", map[string]interface{}{"id": "2"}),
	))
	idx := newIndex(src)
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	all := idx.List("t1", "")
	if len(all) != 2 {
		t.Fatalf("got %d, want 2", len(all))
	}
	if all[0].Name != "aaa" || all[1].Name != "zzz" {
		t.Errorf("order not stable (sorted by address): %s, %s", all[0].Name, all[1].Name)
	}
}

func TestLookup_MissingTenant(t *testing.T) {
	idx := newIndex(newFakeSource())
	if _, ok := idx.Lookup("nope", "oci_core_instance", "cp"); ok {
		t.Error("Lookup for unknown tenant should be false")
	}
}

func TestDrop_RemovesTenant(t *testing.T) {
	src := newFakeSource()
	src.set("t1", stateBlob(1, inst("oci_core_instance", "cp", map[string]interface{}{"id": "x"})))
	idx := newIndex(src)
	if _, err := idx.Rebuild(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	idx.Drop("t1")
	if got := idx.List("t1", ""); len(got) != 0 {
		t.Errorf("Drop did not clear tenant: %d", len(got))
	}
}

// =====================================================================
// Machine extractors (the three provider shapes)
// =====================================================================

func TestExtractMachine_OCI(t *testing.T) {
	spec := extractMachine("oci_core_instance", map[string]interface{}{
		"id":           "ocid1.instance.abc",
		"display_name": "talos-oci-c-panther",
		"shape":        "VM.Standard.A1.Flex",
		"region":       "eu-milan-1",
	})
	if spec["providerId"] != "ocid1.instance.abc" {
		t.Errorf("providerId = %v", spec["providerId"])
	}
	if spec["shape"] != "VM.Standard.A1.Flex" {
		t.Errorf("shape = %v", spec["shape"])
	}
	if spec["region"] != "eu-milan-1" {
		t.Errorf("region = %v", spec["region"])
	}
	// Empty fields must be omitted (metal/cloud-only fields don't appear).
	if _, has := spec["address"]; has {
		t.Errorf("address should be omitted when empty: %v", spec["address"])
	}
}

func TestExtractMachine_OpenStack(t *testing.T) {
	spec := extractMachine("openstack_compute_instance_v2", map[string]interface{}{
		"id":           "instance-uuid",
		"name":         "talos-os-w-phoenix",
		"flavor_name":  "SCS-16V-32-100",
		"access_ip_v6": "2001:db8::1",
	})
	if spec["providerId"] != "instance-uuid" {
		t.Errorf("providerId = %v", spec["providerId"])
	}
	if spec["shape"] != "SCS-16V-32-100" {
		t.Errorf("shape = %v", spec["shape"])
	}
	if spec["address"] != "2001:db8::1" {
		t.Errorf("address = %v", spec["address"])
	}
}

func TestExtractMachine_OpenStack_NetworkBlock(t *testing.T) {
	// When access_ip is absent, fall back to network[0].fixed_ip_v4.
	spec := extractMachine("openstack_compute_instance_v2", map[string]interface{}{
		"id":   "x",
		"name": "n",
		"network": []interface{}{
			map[string]interface{}{"fixed_ip_v4": "192.168.1.10"},
		},
	})
	if spec["address"] != "192.168.1.10" {
		t.Errorf("address = %v, want 192.168.1.10", spec["address"])
	}
}

func TestExtractMachine_Metal(t *testing.T) {
	spec := extractMachine("talos_machine_configuration_apply", map[string]interface{}{
		"node":     "2a01:e11:2440:2430::1",
		"hostname": "talos-edge-w-foal",
	})
	if spec["address"] != "2a01:e11:2440:2430::1" {
		t.Errorf("address = %v", spec["address"])
	}
	if spec["hostname"] != "talos-edge-w-foal" {
		t.Errorf("hostname = %v", spec["hostname"])
	}
	// Metal has no shape/providerId — must be omitted.
	if _, has := spec["shape"]; has {
		t.Errorf("shape should be omitted for metal: %v", spec["shape"])
	}
	if _, has := spec["providerId"]; has {
		t.Errorf("providerId should be omitted for metal: %v", spec["providerId"])
	}
}

func TestExtractMachine_EmptyAttrsReturnsNil(t *testing.T) {
	if spec := extractMachine("oci_core_instance", nil); spec != nil {
		t.Errorf("nil attrs should return nil spec, got %v", spec)
	}
}

func TestExtractMachine_AllEmptyReturnsNil(t *testing.T) {
	// A machine with no recognizable fields → empty MachineSpec → nil map
	// (the API renders nothing rather than an all-empty object).
	if spec := extractMachine("oci_core_instance", map[string]interface{}{}); spec != nil {
		t.Errorf("all-empty attrs should return nil spec, got %v", spec)
	}
}

// =====================================================================
// StateSource adapter
// =====================================================================

func TestStateSourceFunc(t *testing.T) {
	called := false
	f := StateSourceFunc(func(_ context.Context, tenant string) ([]byte, error) {
		called = true
		if tenant != "t1" {
			t.Errorf("tenant = %q", tenant)
		}
		return []byte("blob"), nil
	})
	b, err := f.State(context.Background(), "t1")
	if err != nil || !called || string(b) != "blob" {
		t.Fatalf("StateSourceFunc misbehaved: called=%v b=%s err=%v", called, b, err)
	}
}

func TestRebuild_ReadError(t *testing.T) {
	src := StateSourceFunc(func(_ context.Context, _ string) ([]byte, error) {
		return nil, fmt.Errorf("io error")
	})
	idx := New(src, testRegistry())
	if _, err := idx.Rebuild(context.Background(), "t1"); err == nil {
		t.Fatal("want error on state read failure")
	}
}
