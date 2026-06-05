package jointoken

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func setupTest(t *testing.T) (*state.Store, *API) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Create a tenant and node group.
	_, err = store.CreateTenant("test-tenant", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Create a node group via resource table.
	_, err = store.CreateResource("nodegroup", "workers", state.NodeGroupSpec{
		Name:  "workers",
		Role:  "worker",
		Count: 3,
	}, nil, map[string]string{
		"rezuscloud.io/tenant": "test-tenant",
		"rezuscloud.io/role":   "worker",
	}, nil)
	if err != nil {
		t.Fatalf("CreateResource nodegroup: %v", err)
	}

	return store, NewAPI(store)
}

func TestJoinToken_Create(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"spec": map[string]any{
			"nodeGroup": "workers",
			"singleUse": true,
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/join-tokens", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp createResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Token == "" {
		t.Error("token should not be empty")
	}
	if len(resp.Token) != 64 { // 32 bytes hex
		t.Errorf("token length = %d, want 64", len(resp.Token))
	}
	if resp.Spec.NodeGroup != "workers" {
		t.Errorf("nodeGroup = %q, want %q", resp.Spec.NodeGroup, "workers")
	}
	if !resp.Spec.SingleUse {
		t.Error("singleUse should be true")
	}
}

func TestJoinToken_Create_MissingNodeGroup(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"spec": map[string]any{
			"singleUse": true,
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/join-tokens", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestJoinToken_Create_TenantNotFound(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"spec": map[string]any{
			"nodeGroup": "workers",
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/nonexistent/join-tokens", bytes.NewReader(b))
	req.SetPathValue("tenant", "nonexistent")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestJoinToken_List(t *testing.T) {
	store, api := setupTest(t)

	// Create two tokens.
	_, _ = store.CreateJoinToken("token-1", state.JoinTokenSpec{
		NodeGroup: "workers",
		ExpiresAt: fakeExpiry(),
	}, "test-tenant", "workers")
	_, _ = store.CreateJoinToken("token-2", state.JoinTokenSpec{
		NodeGroup: "workers",
		ExpiresAt: fakeExpiry(),
	}, "test-tenant", "workers")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/join-tokens", nil)
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
}

func TestJoinToken_Delete(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateJoinToken("token-to-delete", state.JoinTokenSpec{
		NodeGroup: "workers",
		ExpiresAt: fakeExpiry(),
	}, "test-tenant", "workers")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/test-tenant/join-tokens/token-to-delete", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("id", "token-to-delete")
	w := httptest.NewRecorder()
	api.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestJoinToken_Delete_NotFound(t *testing.T) {
	_, api := setupTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/test-tenant/join-tokens/nonexistent", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	api.Delete(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func fakeExpiry() time.Time {
	return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
}
