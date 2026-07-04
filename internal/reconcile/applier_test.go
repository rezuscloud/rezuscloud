package reconcile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/applyqueue"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProviderTypeOf(t *testing.T) {
	cases := []struct {
		class string
		want  string
	}{
		{"oci:VM.Standard.A1.Flex", "oci"},
		{"openstack:m1.small", "openstack"},
		{"metal", "metal"},
		{"static", "static"},
		{"", ""},
		{":weird", ""},
	}
	for _, c := range cases {
		t.Run(c.class, func(t *testing.T) {
			if got := providerTypeOf(c.class); got != c.want {
				t.Errorf("providerTypeOf(%q) = %q, want %q", c.class, got, c.want)
			}
		})
	}
}

func TestFilterByProvider(t *testing.T) {
	ngs := []state.NodeGroupSpec{
		{Name: "cp", Role: "controlplane", Count: 3, ProviderClass: "oci:VM.Standard.A1.Flex"},
		{Name: "w1", Role: "worker", Count: 2, ProviderClass: "oci:VM.Standard.E4.Flex"},
		{Name: "w2", Role: "worker", Count: 1, ProviderClass: "openstack:m1.small"},
		{Name: "w3", Role: "worker", Count: 1, ProviderClass: "static"},
	}

	oci := filterByProvider(ngs, "oci")
	if len(oci) != 2 {
		t.Fatalf("oci: got %d nodegroups, want 2", len(oci))
	}
	for _, ng := range oci {
		if providerTypeOf(ng.ProviderClass) != "oci" {
			t.Errorf("oci filter leaked %q", ng.ProviderClass)
		}
	}

	os := filterByProvider(ngs, "openstack")
	if len(os) != 1 {
		t.Fatalf("openstack: got %d, want 1", len(os))
	}

	static := filterByProvider(ngs, "static")
	if len(static) != 1 {
		t.Fatalf("static: got %d, want 1", len(static))
	}

	none := filterByProvider(ngs, "hetzner")
	if len(none) != 0 {
		t.Fatalf("hetzner: got %d, want 0", len(none))
	}
}

func TestEnqueueBus_TenantEvent(t *testing.T) {
	calls := make(chan string, 10)
	bus := &testQueue{calls: calls}

	eb := NewEnqueueBus(bus)
	eb.Publish("tenant", state.ResourceEvent{
		Type:         "MODIFIED",
		ResourceType: "tenant",
		Metadata:     state.Metadata{Name: "personal"},
	})

	select {
	case got := <-calls:
		if got != "personal" {
			t.Errorf("enqueued %q, want %q", got, "personal")
		}
	default:
		t.Fatal("tenant event did not enqueue")
	}
}

func TestEnqueueBus_NodeGroupEvent(t *testing.T) {
	calls := make(chan string, 10)
	bus := &testQueue{calls: calls}

	eb := NewEnqueueBus(bus)
	eb.Publish("nodegroup", state.ResourceEvent{
		Type:         "ADDED",
		ResourceType: "nodegroup",
		Metadata: state.Metadata{
			Name:   "workers",
			Labels: map[string]string{"rezuscloud.io/tenant": "personal"},
		},
	})

	select {
	case got := <-calls:
		if got != "personal" {
			t.Errorf("enqueued %q, want %q", got, "personal")
		}
	default:
		t.Fatal("nodegroup event did not enqueue")
	}
}

func TestEnqueueBus_UnrelatedEventSkipped(t *testing.T) {
	calls := make(chan string, 10)
	bus := &testQueue{calls: calls}

	eb := NewEnqueueBus(bus)
	// A machine event without a tenant label.
	eb.Publish("machine", state.ResourceEvent{
		Type:         "MODIFIED",
		ResourceType: "machine",
		Metadata:     state.Metadata{Name: "node-1"},
	})

	if len(calls) != 0 {
		t.Errorf("unrelated event enqueued %d tenants, want 0", len(calls))
	}
}

func TestEnqueueBus_StatusMutationSkipped(t *testing.T) {
	calls := make(chan string, 10)
	bus := &testQueue{calls: calls}

	eb := NewEnqueueBus(bus)
	eb.Publish("tenant", state.ResourceEvent{
		Type:         "MODIFIED",
		Mutation:     state.MutationStatus,
		ResourceType: "tenant",
		Metadata:     state.Metadata{Name: "personal"},
	})

	if len(calls) != 0 {
		t.Fatalf("status-only mutation enqueued %d tenants, want 0", len(calls))
	}
}

func TestStatusTracker_PersistsReconciliationStatus(t *testing.T) {
	store := openTestStore(t)

	_, err := store.CreateTenant("t1", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ngSpecJSON{Count: 2, Role: "worker", ProviderClass: "oci:test"})
	_, err = store.CreateResource("nodegroup", "workers", json.RawMessage(raw), nodeGroupStatusJSON{Phase: "forming"},
		map[string]string{"rezuscloud.io/tenant": "t1", "rezuscloud.io/role": "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	tracker := NewStatusTracker(store)
	tracker.Start(ctx)
	defer func() {
		cancel()
		tracker.Stop()
	}()
	listener := tracker.Listener()

	listener("t1", applyqueue.PhaseQueued, nil)
	listener("t1", applyqueue.PhaseApplying, nil)
	listener("t1", applyqueue.PhaseFailed, context.DeadlineExceeded)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tenant, _ := store.GetTenant("t1")
		if tenant != nil && tenant.Status.Reconciliation != nil && tenant.Status.Reconciliation.Phase == string(applyqueue.PhaseFailed) {
			var ng nodeGroupStatusJSON
			_, err := store.GetResource("nodegroup", "workers", &struct{}{}, &ng)
			if err == nil && ng.Reconciliation != nil && ng.Reconciliation.Phase == string(applyqueue.PhaseFailed) {
				if tenant.Status.Reconciliation.LastError == "" || ng.Reconciliation.LastError == "" {
					t.Fatalf("expected lastError to be preserved on failure")
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reconciliation status was not persisted to tenant and nodegroup")
}

func TestLoadNodeGroups(t *testing.T) {
	store := openTestStore(t)

	// Seed tenant + node groups.
	_, err := store.CreateTenant("t1", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	ng1 := ngSpecJSON{Count: 3, Role: "controlplane", ProviderClass: "oci:VM.Standard.A1.Flex"}
	ng2 := ngSpecJSON{Count: 2, Role: "worker", ProviderClass: "static"}
	raw1, _ := json.Marshal(ng1)
	raw2, _ := json.Marshal(ng2)

	// Pass as json.RawMessage so CreateResource stores the JSON as-is, not
	// base64-encoded (json.Marshal on a plain []byte base64-encodes it).
	_, err = store.CreateResource("nodegroup", "cp", json.RawMessage(raw1), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "t1", "rezuscloud.io/role": "controlplane"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateResource("nodegroup", "workers", json.RawMessage(raw2), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "t1", "rezuscloud.io/role": "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := loadNodeGroups(store, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d nodegroups, want 2", len(loaded))
	}

	names := map[string]bool{}
	for _, ng := range loaded {
		names[ng.Name] = true
		if ng.Count == 0 {
			t.Errorf("nodegroup %q has count 0", ng.Name)
		}
	}
	if !names["cp"] || !names["workers"] {
		t.Errorf("missing nodegroups, got %v", names)
	}
}

// testQueue is a minimal Enqueuer that records Enqueue calls.
type testQueue struct {
	calls chan string
}

func (q *testQueue) Enqueue(tenant string) {
	// Non-blocking; tests read from the buffered channel.
	select {
	case q.calls <- tenant:
	default:
	}
}

func TestRenderTFVars_ClusterLevelOnly(t *testing.T) {
	dir := t.TempDir()
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "my-cluster"},
		Spec: state.TenantSpec{
			KubernetesVersion:    "1.35.0",
			TalosVersion:         "1.12.6",
			ControlPlaneEndpoint: "https://10.0.0.1:6443",
		},
	}
	if err := renderTFVars(dir, tenant); err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck
	data, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	var v map[string]string
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v["cluster_name"] != "my-cluster" {
		t.Errorf("cluster_name = %q", v["cluster_name"])
	}
	if v["kubernetes_version"] != "1.35.0" {
		t.Errorf("kubernetes_version = %q", v["kubernetes_version"])
	}
	if _, ok := v["compartment_ocid"]; ok {
		t.Error("compartment_ocid should be absent for non-OCI tenant")
	}
}

func TestRenderTFVars_OCIProviderConfig(t *testing.T) {
	dir := t.TempDir()
	ociConfig, _ := json.Marshal(map[string]string{
		"region":          "us-phoenix-1",
		"compartmentOcid": "ocid1.compartment.oc1..abc",
		"imageOcid":       "ocid1.image.oc1.phx.abc",
	})
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "oci-cluster"},
		Spec: state.TenantSpec{
			TalosVersion:      "1.12.6",
			KubernetesVersion: "1.35.0",
			NodeGroups: []state.NodeGroupSpec{{
				Name:           "cp",
				Role:           "controlplane",
				Count:          1,
				ProviderClass:  "oci:VM.Standard.A1.Flex",
				ProviderConfig: ociConfig,
			}},
		},
	}
	if err := renderTFVars(dir, tenant); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	var v map[string]string
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v["region"] != "us-phoenix-1" {
		t.Errorf("region = %q, want us-phoenix-1", v["region"])
	}
	if v["compartment_ocid"] != "ocid1.compartment.oc1..abc" {
		t.Errorf("compartment_ocid = %q", v["compartment_ocid"])
	}
	if v["talos_image_ocid"] != "ocid1.image.oc1.phx.abc" {
		t.Errorf("talos_image_ocid = %q", v["talos_image_ocid"])
	}
}

func TestCommonVariables_OptionalDefaults(t *testing.T) {
	vars := commonVariables()
	for _, name := range []string{"region", "compartment_ocid", "talos_image_ocid"} {
		v := vars[name].(map[string]any)
		if v["default"] != "" {
			t.Errorf("variable %q default = %v, want empty string", name, v["default"])
		}
	}
	// Required variables (no default)
	for _, name := range []string{"cluster_name", "cluster_endpoint", "talos_version", "kubernetes_version"} {
		v := vars[name].(map[string]any)
		if _, hasDefault := v["default"]; hasDefault {
			t.Errorf("variable %q should not have a default", name)
		}
	}
}
