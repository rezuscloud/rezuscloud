package nodegroup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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

	// Create a tenant for node groups.
	_, err = store.CreateTenant("test-tenant", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	return store, NewAPI(store)
}

func TestNodeGroup_CRUD(t *testing.T) {
	_, api := setupTest(t)

	// Create.
	body := map[string]any{
		"metadata": map[string]any{
			"name":   "control-plane",
			"labels": map[string]string{"rezuscloud.io/role": "controlplane"},
		},
		"spec": map[string]any{
			"role":  "controlplane",
			"count": 3,
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want %d, body = %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var created NodeGroup
	_ = json.NewDecoder(w.Body).Decode(&created)
	if created.Metadata.Name != "control-plane" {
		t.Errorf("name = %q, want %q", created.Metadata.Name, "control-plane")
	}
	if created.Spec.Count != 3 {
		t.Errorf("count = %d, want 3", created.Spec.Count)
	}
	if created.Status.Phase != PhaseForming {
		t.Errorf("phase = %q, want %q", created.Status.Phase, PhaseForming)
	}
	if created.Metadata.Labels["rezuscloud.io/tenant"] != "test-tenant" {
		t.Error("missing tenant label")
	}
	if created.Metadata.Labels["rezuscloud.io/role"] != "controlplane" {
		t.Error("missing role label")
	}

	// Get.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/node-groups/control-plane", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "control-plane")
	w = httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want %d", w.Code, http.StatusOK)
	}

	// List.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/node-groups", nil)
	req.SetPathValue("tenant", "test-tenant")
	w = httptest.NewRecorder()
	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want %d", w.Code, http.StatusOK)
	}

	var list listResponse
	_ = json.NewDecoder(w.Body).Decode(&list)
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}

	// Update.
	updateBody := map[string]any{
		"metadata": map[string]any{"resourceVersion": created.Metadata.ResourceVersion},
		"spec": map[string]any{
			"role":  "controlplane",
			"count": 5,
		},
	}
	b, _ = json.Marshal(updateBody)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test-tenant/node-groups/control-plane", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "control-plane")
	w = httptest.NewRecorder()
	api.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var updated NodeGroup
	_ = json.NewDecoder(w.Body).Decode(&updated)
	if updated.Spec.Count != 5 {
		t.Errorf("count = %d, want 5", updated.Spec.Count)
	}

	// Update status.
	statusBody := map[string]any{
		"status": map[string]any{
			"phase":         "active",
			"readyMachines": 3,
			"totalMachines": 3,
		},
	}
	b, _ = json.Marshal(statusBody)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test-tenant/node-groups/control-plane/status", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "control-plane")
	w = httptest.NewRecorder()
	api.UpdateStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update status: status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Delete.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/test-tenant/node-groups/control-plane", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "control-plane")
	w = httptest.NewRecorder()
	api.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d, want %d", w.Code, http.StatusNoContent)
	}

	// Verify deleted.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/node-groups/control-plane", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "control-plane")
	w = httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("get after delete: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestNodeGroup_UpdateRequiresResourceVersion(t *testing.T) {
	_, api := setupTest(t)

	// Seed a node group.
	body := map[string]any{
		"metadata": map[string]any{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 2},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d", w.Code)
	}

	// Update without metadata.resourceVersion.
	updateBody := map[string]any{
		"metadata": map[string]any{},
		"spec": map[string]any{
			"role":  "worker",
			"count": 3,
		},
	}
	b, _ = json.Marshal(updateBody)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test-tenant/node-groups/workers", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "workers")
	w = httptest.NewRecorder()
	api.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("update missing rv: status = %d, want %d, body = %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestNodeGroup_UpdateConflictOnStaleResourceVersion(t *testing.T) {
	_, api := setupTest(t)

	// Seed a node group.
	body := map[string]any{
		"metadata": map[string]any{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 2},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d", w.Code)
	}

	var created NodeGroup
	_ = json.NewDecoder(w.Body).Decode(&created)

	// First update advances the resource version.
	updateBody := map[string]any{
		"metadata": map[string]any{"resourceVersion": created.Metadata.ResourceVersion},
		"spec": map[string]any{
			"role":  "worker",
			"count": 3,
		},
	}
	b, _ = json.Marshal(updateBody)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test-tenant/node-groups/workers", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "workers")
	w = httptest.NewRecorder()
	api.Update(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first update: status = %d, body = %s", w.Code, w.Body.String())
	}

	// Reuse the stale resource version from the original create.
	req = httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test-tenant/node-groups/workers", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("name", "workers")
	w = httptest.NewRecorder()
	api.Update(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("stale update: status = %d, want %d, body = %s", w.Code, http.StatusConflict, w.Body.String())
	}
}

func TestNodeGroup_CreateDuplicate(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"metadata": map[string]any{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 2},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w = httptest.NewRecorder()
	api.Create(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestNodeGroup_CreateInvalidRole(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"metadata": map[string]any{"name": "bad"},
		"spec":     map[string]any{"role": "superadmin", "count": 1},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestNodeGroup_CreateInvalidCount(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"metadata": map[string]any{"name": "zero"},
		"spec":     map[string]any{"role": "worker", "count": 0},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestNodeGroup_TenantNotFound(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"metadata": map[string]any{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 2},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/nonexistent/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "nonexistent")
	w := httptest.NewRecorder()
	api.Create(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestNodeGroup_GetWrongTenant(t *testing.T) {
	_, api := setupTest(t)

	// Create node group under test-tenant.
	body := map[string]any{
		"metadata": map[string]any{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 2},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.Create(w, req)

	// Try to get under different tenant.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/other-tenant/node-groups/workers", nil)
	req.SetPathValue("tenant", "other-tenant")
	req.SetPathValue("name", "workers")
	w = httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("wrong tenant: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestNodeGroup_MultipleNodeGroups(t *testing.T) {
	_, api := setupTest(t)

	for _, ng := range []struct {
		name  string
		role  string
		count int
	}{
		{"control-plane", "controlplane", 3},
		{"workers", "worker", 5},
		{"gpu-workers", "worker", 2},
	} {
		body := map[string]any{
			"metadata": map[string]any{"name": ng.name},
			"spec":     map[string]any{"role": ng.role, "count": ng.count},
		}
		b, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/test-tenant/node-groups", bytes.NewReader(b))
		req.SetPathValue("tenant", "test-tenant")
		w := httptest.NewRecorder()
		api.Create(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: status = %d", ng.name, w.Code)
		}
	}

	// List all.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/node-groups", nil)
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.List(w, req)

	var list listResponse
	_ = json.NewDecoder(w.Body).Decode(&list)
	if list.Total != 3 {
		t.Errorf("total = %d, want 3", list.Total)
	}
}
