package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func setupTestAPI(t *testing.T) (*TenantAPI, *state.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewTenantAPI(store, nil, nil), store
}

func TestTenantAPI_Create(t *testing.T) {
	api, _ := setupTestAPI(t)

	body, _ := json.Marshal(CreateTenantRequest{
		Metadata: state.Metadata{Name: "test"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp TenantResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Metadata.Name != "test" {
		t.Errorf("name = %q, want %q", resp.Metadata.Name, "test")
	}
	if resp.Status.Phase != state.TenantForming {
		t.Errorf("phase = %q, want %q", resp.Status.Phase, state.TenantForming)
	}
}

func TestTenantAPI_Create_NoName(t *testing.T) {
	api, _ := setupTestAPI(t)

	body, _ := json.Marshal(CreateTenantRequest{
		Spec: state.TenantSpec{KubernetesVersion: "1.35.0"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestTenantAPI_Create_Duplicate(t *testing.T) {
	api, _ := setupTestAPI(t)

	body, _ := json.Marshal(CreateTenantRequest{
		Metadata: state.Metadata{Name: "dup"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0"},
	})

	// First create succeeds.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	w := httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d", w.Code)
	}

	// Second create fails with conflict.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	w = httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate create: status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestTenantAPI_Get(t *testing.T) {
	api, _ := setupTestAPI(t)

	// Create first.
	_, _ = api.store.CreateTenant("test", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test", nil)
	req.SetPathValue("name", "test")
	w := httptest.NewRecorder()

	api.Get(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp TenantResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Metadata.Name != "test" {
		t.Errorf("name = %q, want %q", resp.Metadata.Name, "test")
	}
}

func TestTenantAPI_Get_NotFound(t *testing.T) {
	api, _ := setupTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/nope", nil)
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()

	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestTenantAPI_List(t *testing.T) {
	api, _ := setupTestAPI(t)

	_, _ = api.store.CreateTenant("alpha", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	_, _ = api.store.CreateTenant("beta", state.TenantSpec{KubernetesVersion: "1.36.0"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	w := httptest.NewRecorder()

	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp TenantListResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
	if len(resp.Items) != 2 {
		t.Errorf("items = %d, want 2", len(resp.Items))
	}
}

func TestTenantAPI_Delete(t *testing.T) {
	api, _ := setupTestAPI(t)

	_, _ = api.store.CreateTenant("test", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/test", nil)
	req.SetPathValue("name", "test")
	w := httptest.NewRecorder()

	api.Delete(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d (202 Accepted — async deletion via finalizers)", w.Code, http.StatusAccepted)
	}

	var resp TenantResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Metadata.DeletionTimestamp == nil {
		t.Error("deletionTimestamp should be set")
	}
	if len(resp.Metadata.Finalizers) != 2 {
		t.Errorf("finalizers = %d, want 2", len(resp.Metadata.Finalizers))
	}
}

func TestTenantAPI_UpdateStatus(t *testing.T) {
	api, _ := setupTestAPI(t)

	_, _ = api.store.CreateTenant("test", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"status": state.TenantStatus{
			Phase:     state.TenantActive,
			Available: true,
			Ready:     true,
		},
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test/status", bytes.NewReader(body))
	req.SetPathValue("name", "test")
	w := httptest.NewRecorder()

	api.UpdateStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp TenantResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status.Phase != state.TenantActive {
		t.Errorf("phase = %q, want %q", resp.Status.Phase, state.TenantActive)
	}
}

func TestTenantAPI_Create_AutoGeneratesSecretsBundle(t *testing.T) {
	api, _ := setupTestAPI(t)

	body, _ := json.Marshal(CreateTenantRequest{
		Metadata: state.Metadata{Name: "auto-secrets"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0", TalosVersion: "1.12.0"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	// Verify the secrets bundle is stored.
	bundle, err := api.store.LoadTenantSecrets("auto-secrets")
	if err != nil {
		t.Fatalf("load secrets: %v", err)
	}
	if bundle == nil {
		t.Error("expected secrets bundle to be auto-generated on create, got nil")
	}
}

func TestTenantAPI_Kubeconfig_Success(t *testing.T) {
	api, _ := setupTestAPI(t)

	// Create tenant (auto-generates bundle).
	body, _ := json.Marshal(CreateTenantRequest{
		Metadata: state.Metadata{Name: "kc-test"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0", TalosVersion: "1.12.0"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	w := httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d", w.Code)
	}

	// Fetch kubeconfig.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/kc-test/kubeconfig", nil)
	req.SetPathValue("name", "kc-test")
	w = httptest.NewRecorder()
	api.Kubeconfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("kubeconfig status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "kc-test-kubeconfig.yaml") {
		t.Errorf("Content-Disposition = %q, missing filename", cd)
	}
	body2 := w.Body.String()
	if !strings.Contains(body2, "apiVersion: v1") {
		t.Errorf("kubeconfig body missing apiVersion; got:\n%s", body2)
	}
	if !strings.Contains(body2, "kind: Config") {
		t.Errorf("kubeconfig body missing kind: Config; got:\n%s", body2)
	}
	if !strings.Contains(body2, "name: kc-test") && !strings.Contains(body2, "kc-test") {
		t.Errorf("kubeconfig body missing cluster name reference; got:\n%s", body2)
	}
}

func TestTenantAPI_Kubeconfig_TenantNotFound(t *testing.T) {
	api, _ := setupTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/nope/kubeconfig", nil)
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()
	api.Kubeconfig(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestTenantAPI_Talosconfig_Success(t *testing.T) {
	api, _ := setupTestAPI(t)

	body, _ := json.Marshal(CreateTenantRequest{
		Metadata: state.Metadata{Name: "tc-test"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0", TalosVersion: "1.12.0"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	w := httptest.NewRecorder()
	api.Create(w, req)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/tc-test/talosconfig", nil)
	req.SetPathValue("name", "tc-test")
	w = httptest.NewRecorder()
	api.Talosconfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("talosconfig status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "tc-test-talosconfig.yaml") {
		t.Errorf("Content-Disposition = %q, missing filename", cd)
	}
	body2 := w.Body.String()
	if !strings.Contains(body2, "context:") {
		t.Errorf("talosconfig body missing 'context:'; got:\n%s", body2)
	}
}

func TestTenantAPI_ListPagination(t *testing.T) {
	api, store := setupTestAPI(t)

	// Seed 5 tenants.
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("t%d", i)
		_, _ = store.CreateTenant(name, state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	}

	// Page 1: limit=2, offset=0 → 2 items, remaining 3.
	req := httptest.NewRequest("GET", "/api/v1/tenants?limit=2&offset=0", nil)
	w := httptest.NewRecorder()
	api.List(w, req)

	var resp TenantListResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Items) != 2 {
		t.Fatalf("page 1: got %d items, want 2", len(resp.Items))
	}
	if resp.RemainingItemCount != 3 {
		t.Errorf("page 1: remaining = %d, want 3", resp.RemainingItemCount)
	}

	// Page 3: limit=2, offset=4 → 1 item (last), remaining 0.
	req = httptest.NewRequest("GET", "/api/v1/tenants?limit=2&offset=4", nil)
	w = httptest.NewRecorder()
	api.List(w, req)

	var resp3 TenantListResponse
	_ = json.NewDecoder(w.Body).Decode(&resp3)
	if len(resp3.Items) != 1 {
		t.Fatalf("page 3: got %d items, want 1", len(resp3.Items))
	}
	if resp3.Total != 5 {
		t.Errorf("page 3: total = %d, want 5", resp3.Total)
	}
}
