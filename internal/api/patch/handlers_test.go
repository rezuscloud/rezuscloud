package patch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func setupTenant(t *testing.T, store *state.Store) {
	t.Helper()
	_, err := store.CreateResource("tenant", "prod", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func TestCreate_Success(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"disk-patch"},"spec":{"patch":"machine:\n  disks:\n    - device: /dev/sda\n      partitions: []","enabled":true}}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", w.Code, w.Body.String())
	}

	var result ConfigPatch
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Spec.Patch == "" {
		t.Error("patch should not be empty")
	}
	if result.Spec.Format != "strategic" {
		t.Errorf("format = %q, want strategic (default)", result.Spec.Format)
	}
}

func TestCreate_WithTargetRole(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"cp-patch"},"spec":{"patch":"machine:\n  type: controlplane","format":"strategic","targetRole":"controlplane","enabled":true}}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	var result ConfigPatch
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Spec.TargetRole != "controlplane" {
		t.Errorf("targetRole = %q, want controlplane", result.Spec.TargetRole)
	}
}

func TestCreate_NoName(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"spec":{"patch":"yaml: here","enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreate_NoPatch(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"empty"},"spec":{"enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreate_InvalidFormat(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"bad"},"spec":{"patch":"yaml: yes","format":"invalid","enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreate_InvalidTargetRole(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"bad"},"spec":{"patch":"yaml: yes","targetRole":"superworker","enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreate_Duplicate(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"dup"},"spec":{"patch":"yaml: here","enabled":true}}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req1.SetPathValue("tenant", "prod")
	w1 := httptest.NewRecorder()
	api.Create(w1, req1)

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req2.SetPathValue("tenant", "prod")
	w2 := httptest.NewRecorder()
	api.Create(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w2.Code)
	}
}

func TestCreate_TenantNotFound(t *testing.T) {
	store := newTestStore(t)
	api := NewAPI(store)

	body := `{"metadata":{"name":"p"},"spec":{"patch":"yaml: here","enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/nonexistent/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "nonexistent")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGet_Success(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	// Create first.
	body := `{"metadata":{"name":"my-patch"},"spec":{"patch":"machine:\n  install:\n    disk: /dev/sda","enabled":true}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	createReq.SetPathValue("tenant", "prod")
	createW := httptest.NewRecorder()
	api.Create(createW, createReq)

	// Get.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/patches/my-patch", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "my-patch")
	w := httptest.NewRecorder()

	api.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result ConfigPatch
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Metadata.Name != "my-patch" {
		t.Errorf("name = %q, want my-patch", result.Metadata.Name)
	}
}

func TestGet_NotFound(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/patches/nope", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()

	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGet_WrongTenant(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	// Create under prod.
	body := `{"metadata":{"name":"my-patch"},"spec":{"patch":"yaml: yes","enabled":true}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	createReq.SetPathValue("tenant", "prod")
	createW := httptest.NewRecorder()
	api.Create(createW, createReq)

	// Try to get as different tenant.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/other/patches/my-patch", nil)
	req.SetPathValue("tenant", "other")
	req.SetPathValue("name", "my-patch")
	w := httptest.NewRecorder()

	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestList(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	// Create two patches.
	for _, name := range []string{"patch-a", "patch-b"} {
		body := `{"metadata":{"name":"` + name + `"},"spec":{"patch":"yaml: ` + name + `","enabled":true}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
		req.SetPathValue("tenant", "prod")
		w := httptest.NewRecorder()
		api.Create(w, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/patches", nil)
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result listResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Total != 2 {
		t.Errorf("total = %d, want 2", result.Total)
	}
	if len(result.Items) != 2 {
		t.Errorf("items = %d, want 2", len(result.Items))
	}
}

func TestUpdate_Success(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	// Create.
	body := `{"metadata":{"name":"my-patch"},"spec":{"patch":"old: yaml","enabled":true}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	createReq.SetPathValue("tenant", "prod")
	createW := httptest.NewRecorder()
	api.Create(createW, createReq)

	var created ConfigPatch
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	// Update.
	updateBody := `{"metadata":{"resourceVersion":` + fmt.Sprintf("%d", created.Metadata.ResourceVersion) + `},"spec":{"patch":"new: yaml","enabled":false}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/prod/patches/my-patch", strings.NewReader(updateBody))
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "my-patch")
	w := httptest.NewRecorder()

	api.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}

	var updated ConfigPatch
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.Spec.Patch != "new: yaml" {
		t.Errorf("patch = %q, want 'new: yaml'", updated.Spec.Patch)
	}
	if updated.Spec.Enabled {
		t.Error("enabled should be false")
	}
}

func TestUpdate_NotFound(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"spec":{"patch":"yaml: yes","enabled":true}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/prod/patches/nope", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()

	api.Update(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestUpdate_NoPatch(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	// Create first.
	body := `{"metadata":{"name":"p"},"spec":{"patch":"old: yaml","enabled":true}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	createReq.SetPathValue("tenant", "prod")
	createW := httptest.NewRecorder()
	api.Create(createW, createReq)

	// Update with empty patch.
	updateBody := `{"spec":{"enabled":false}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/prod/patches/p", strings.NewReader(updateBody))
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "p")
	w := httptest.NewRecorder()

	api.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestDelete_Success(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	// Create.
	body := `{"metadata":{"name":"del-me"},"spec":{"patch":"yaml: here","enabled":true}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	createReq.SetPathValue("tenant", "prod")
	createW := httptest.NewRecorder()
	api.Create(createW, createReq)

	// Delete.
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/prod/patches/del-me", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "del-me")
	w := httptest.NewRecorder()

	api.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}

	// Verify gone.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/patches/del-me", nil)
	getReq.SetPathValue("tenant", "prod")
	getReq.SetPathValue("name", "del-me")
	getW := httptest.NewRecorder()
	api.Get(getW, getReq)

	if getW.Code != http.StatusNotFound {
		t.Errorf("get after delete: status = %d, want 404", getW.Code)
	}
}

func TestDelete_NotFound(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/prod/patches/nope", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()

	api.Delete(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestList_Empty(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/patches", nil)
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result listResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Total != 0 {
		t.Errorf("total = %d, want 0", result.Total)
	}
}

func TestCreate_Json6902Format(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store)
	api := NewAPI(store)

	body := `{"metadata":{"name":"json-patch"},"spec":{"patch":"[{\"op\":\"replace\",\"path\":\"/machine/install/disk\",\"value\":\"/dev/nvme0n1\"}]","format":"json6902","enabled":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
	req.SetPathValue("tenant", "prod")
	w := httptest.NewRecorder()

	api.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", w.Code, w.Body.String())
	}

	var result ConfigPatch
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Spec.Format != "json6902" {
		t.Errorf("format = %q, want json6902", result.Spec.Format)
	}
}
