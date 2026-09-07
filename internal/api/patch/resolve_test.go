package patch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/validation"
)

func TestResolvePatches_AllEnabled(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store, nil, validation.NewRegistry())

	for _, p := range []struct {
		name, patch, role string
	}{
		{"common", "common: yaml", ""},
		{"cp-only", "cp: yaml", "controlplane"},
		{"worker-only", "worker: yaml", "worker"},
	} {
		body := `{"metadata":{"name":"` + p.name + `"},"spec":{"patch":"` + p.patch + `","targetRole":"` + p.role + `","enabled":true}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
		req.SetPathValue("tenant", "prod")
		w := httptest.NewRecorder()
		api.Create(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", p.name, w.Code)
		}
	}

	patches, err := ResolvePatches(store, "prod", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 3 {
		t.Errorf("patches = %d, want 3", len(patches))
	}
}

func TestResolvePatches_FilterByRole(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store, nil, validation.NewRegistry())

	for _, p := range []struct {
		name, patch, role string
	}{
		{"common", "common: yaml", ""},
		{"cp-only", "cp: yaml", "controlplane"},
		{"worker-only", "worker: yaml", "worker"},
	} {
		body := `{"metadata":{"name":"` + p.name + `"},"spec":{"patch":"` + p.patch + `","targetRole":"` + p.role + `","enabled":true}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
		req.SetPathValue("tenant", "prod")
		w := httptest.NewRecorder()
		api.Create(w, req)
	}

	patches, err := ResolvePatches(store, "prod", "controlplane", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 2 {
		t.Errorf("patches = %d, want 2 (common + controlplane)", len(patches))
	}
}

func TestResolvePatches_DisabledExcluded(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store, nil, validation.NewRegistry())

	body1 := `{"metadata":{"name":"on"},"spec":{"patch":"on: yaml","enabled":true}}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body1))
	req1.SetPathValue("tenant", "prod")
	w1 := httptest.NewRecorder()
	api.Create(w1, req1)

	body2 := `{"metadata":{"name":"off"},"spec":{"patch":"off: yaml","enabled":false}}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body2))
	req2.SetPathValue("tenant", "prod")
	w2 := httptest.NewRecorder()
	api.Create(w2, req2)

	patches, err := ResolvePatches(store, "prod", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 1 {
		t.Errorf("patches = %d, want 1 (disabled excluded)", len(patches))
	}
}

func TestResolvePatches_NoPatches(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)

	patches, err := ResolvePatches(store, "prod", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %d, want 0", len(patches))
	}
}

func TestResolvePatches_NoTenant(t *testing.T) {
	store := newTestStore(t)

	patches, err := ResolvePatches(store, "nonexistent", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %d, want 0", len(patches))
	}
}

// createPatch is a test helper creating a config patch via the API.
func createPatch(t *testing.T, store *state.Store, name, specJSON string) {
	t.Helper()
	api := NewAPI(store, nil, validation.NewRegistry())
	body := `{"metadata":{"name":"` + name + `"},"spec":` + specJSON + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create %s: %d: %s", name, w.Code, w.Body.String())
	}
}

func TestResolvePatches_MachineScopePrecedence(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)

	// Cluster patch sets a base value; a machine patch on node-1 overrides it.
	// Names chosen so alphabetical order alone would NOT produce the required
	// precedence (a-machine < z-cluster would put machine first).
	createPatch(t, store, "z-cluster-base", `{"patch":"cluster: base","scope":"cluster","enabled":true}`)
	createPatch(t, store, "a-machine-override", `{"patch":"machine: override","scope":"machine","targetMachine":"node-1","enabled":true}`)

	// node-1: cluster patch first, machine patch last (last writer wins).
	patches, err := ResolvePatches(store, "prod", "", "node-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 2 || patches[0] != "cluster: base" || patches[1] != "machine: override" {
		t.Errorf("node-1 patches = %v, want [cluster base, machine override] in that order", patches)
	}

	// node-2: only the cluster patch applies.
	patches, err = ResolvePatches(store, "prod", "", "node-2")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 1 || patches[0] != "cluster: base" {
		t.Errorf("node-2 patches = %v, want only the cluster patch", patches)
	}
}

func TestResolvePatches_MachineScopeRoleFilter(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)

	// Machine patch targeted at the controlplane role on node-1.
	createPatch(t, store, "cp-machine", `{"patch":"cp: machine","scope":"machine","targetMachine":"node-1","targetRole":"controlplane","enabled":true}`)

	// node-1 as a controlplane: applies.
	patches, err := ResolvePatches(store, "prod", "controlplane", "node-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 1 {
		t.Errorf("controlplane node-1 patches = %v, want 1", patches)
	}

	// node-1 as a worker: role filter excludes it.
	patches, err = ResolvePatches(store, "prod", "worker", "node-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("worker node-1 patches = %v, want 0", patches)
	}
}

func TestResolvePatches_MachineScopeOtherMachineExcluded(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)

	createPatch(t, store, "for-node-1", `{"patch":"x: 1","scope":"machine","targetMachine":"node-1","enabled":true}`)

	patches, err := ResolvePatches(store, "prod", "controlplane", "node-9")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %v, want 0 (machine patch must not leak to other machines)", patches)
	}
}

func TestResolvePatches_DisabledMachinePatchExcluded(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)

	createPatch(t, store, "off-patch", `{"patch":"x: 1","scope":"machine","targetMachine":"node-1","enabled":false}`)

	patches, err := ResolvePatches(store, "prod", "", "node-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %v, want 0 (disabled)", patches)
	}
}

func TestResolvePatches_DeterministicOrderWithinScope(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)

	// Create in non-alphabetical order; output must be name-sorted per scope.
	for _, n := range []string{"c-patch", "a-patch", "b-patch"} {
		createPatch(t, store, n, `{"patch":"`+n+`","enabled":true}`)
	}

	patches, err := ResolvePatches(store, "prod", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []string{"a-patch", "b-patch", "c-patch"}
	if len(patches) != 3 || patches[0] != want[0] || patches[1] != want[1] || patches[2] != want[2] {
		t.Errorf("patches = %v, want %v (deterministic order)", patches, want)
	}
}

func TestValidateScope(t *testing.T) {
	cases := []struct {
		name              string
		scope, targetMach string
		wantErr           bool
	}{
		{"empty defaults to cluster", "", "", false},
		{"cluster", "cluster", "", false},
		{"machine with target", "machine", "node-1", false},
		{"machine without target", "machine", "", true},
		{"machine with blank target", "machine", "   ", true},
		{"cluster with target", "cluster", "node-1", true},
		{"unknown scope", "fleet", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateScope(c.scope, c.targetMach)
			if (err != nil) != c.wantErr {
				t.Errorf("ValidateScope(%q,%q) = %v, wantErr = %v", c.scope, c.targetMach, err, c.wantErr)
			}
		})
	}
}
