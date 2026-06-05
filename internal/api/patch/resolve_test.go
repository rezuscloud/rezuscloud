package patch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolvePatches_AllEnabled(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

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

	patches, err := ResolvePatches(store, "prod", "")
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
	api := NewAPI(store)

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

	patches, err := ResolvePatches(store, "prod", "controlplane")
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
	api := NewAPI(store)

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

	patches, err := ResolvePatches(store, "prod", "")
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

	patches, err := ResolvePatches(store, "prod", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %d, want 0", len(patches))
	}
}

func TestResolvePatches_NoTenant(t *testing.T) {
	store := newTestStore(t)

	patches, err := ResolvePatches(store, "nonexistent", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(patches) != 0 {
		t.Errorf("patches = %d, want 0", len(patches))
	}
}
