package machine

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

	// Create a tenant.
	_, err = store.CreateTenant("test-tenant", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	return store, NewAPI(store)
}

func TestMachine_List(t *testing.T) {
	store, api := setupTest(t)

	// Create machines — one assigned, one unassigned.
	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)
	_, _ = store.CreateMachine("hw-002", state.MachineSpec{Connected: false}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
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

func TestMachine_Get(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{
		Connected: true,
	}, map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines/hw-001", nil)
	req.SetPathValue("id", "hw-001")
	w := httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var machine state.Machine
	_ = json.NewDecoder(w.Body).Decode(&machine)
	if machine.Metadata.Name != "hw-001" {
		t.Errorf("id = %q, want %q", machine.Metadata.Name, "hw-001")
	}
}

func TestMachine_GetNotFound(t *testing.T) {
	_, api := setupTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMachine_ListByTenant(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)
	_, _ = store.CreateMachine("hw-002", state.MachineSpec{Connected: false}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/machines", nil)
	req.SetPathValue("tenant", "test-tenant")
	w := httptest.NewRecorder()
	api.ListByTenant(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1 (only assigned machines)", resp.Total)
	}
}

func TestMachine_ListByTenant_NotFound(t *testing.T) {
	_, api := setupTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/nonexistent/machines", nil)
	req.SetPathValue("tenant", "nonexistent")
	w := httptest.NewRecorder()
	api.ListByTenant(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMachine_GetByTenant(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test-tenant/machines/hw-001", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("id", "hw-001")
	w := httptest.NewRecorder()
	api.GetByTenant(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestMachine_GetByTenant_WrongTenant(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/other-tenant/machines/hw-001", nil)
	req.SetPathValue("tenant", "other-tenant")
	req.SetPathValue("id", "hw-001")
	w := httptest.NewRecorder()
	api.GetByTenant(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMachine_UpdateStatus(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)

	body := map[string]any{
		"status": map[string]any{
			"stage": "ready",
			"ready": true,
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/test-tenant/machines/hw-001/status", bytes.NewReader(b))
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("id", "hw-001")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.UpdateStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var updated state.Machine
	_ = json.NewDecoder(w.Body).Decode(&updated)
	if updated.Status.Stage != state.StageReady {
		t.Errorf("stage = %q, want %q", updated.Status.Stage, state.StageReady)
	}
	if !updated.Status.Ready {
		t.Error("ready should be true")
	}
}

func TestMachine_Delete(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/test-tenant/machines/hw-001", nil)
	req.SetPathValue("tenant", "test-tenant")
	req.SetPathValue("id", "hw-001")
	w := httptest.NewRecorder()
	api.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
	}

	// Verify deletion timestamp set (graceful deletion with finalizers).
	machine, _ := store.GetMachine("hw-001")
	if machine == nil {
		t.Fatal("machine should still exist (graceful deletion)")
	}
	if machine.Metadata.DeletionTimestamp == nil {
		t.Error("deletionTimestamp should be set")
	}
}

func TestMachine_Delete_WrongTenant(t *testing.T) {
	store, api := setupTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "test-tenant"}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/other-tenant/machines/hw-001", nil)
	req.SetPathValue("tenant", "other-tenant")
	req.SetPathValue("id", "hw-001")
	w := httptest.NewRecorder()
	api.Delete(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
