package web

import (
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

func setupTenant(t *testing.T, store *state.Store, name string) {
	t.Helper()
	_, err := store.CreateResource("tenant", name, state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func TestDashboard_Empty(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.RegisterRoutes(http.DefaultServeMux)

	// Use handler directly.
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Dashboard") {
		t.Error("response should contain Dashboard heading")
	}
	if !strings.Contains(body, "<title>") {
		t.Error("response should contain HTML title")
	}
}

func TestDashboard_WithTenants(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	setupTenant(t, store, "staging")
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "prod") {
		t.Error("response should contain tenant 'prod'")
	}
	if !strings.Contains(body, "staging") {
		t.Error("response should contain tenant 'staging'")
	}
}

func TestTenantsList_Empty(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/tenants", nil)
	w := httptest.NewRecorder()

	h.TenantsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No clusters yet") {
		t.Error("should show empty state message")
	}
}

func TestTenantsList_WithData(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/tenants", nil)
	w := httptest.NewRecorder()

	h.TenantsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "prod") {
		t.Error("should list tenant 'prod'")
	}
}

func TestTenantDetail_Found(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/tenants/prod", nil)
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()

	h.TenantDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "prod") {
		t.Error("should show tenant name")
	}
	if !strings.Contains(body, "1.35.0") {
		t.Error("should show Kubernetes version")
	}
}

func TestTenantDetail_NotFound(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/tenants/nonexistent", nil)
	req.SetPathValue("name", "nonexistent")
	w := httptest.NewRecorder()

	h.TenantDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestLoginPage(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()

	h.LoginPage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Sign In") {
		t.Error("should contain sign in button")
	}
	if !strings.Contains(body, `name="username"`) {
		t.Error("should contain username input")
	}
}

func TestLoginSubmit_EmptyFields(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.LoginSubmit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-render with error)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "required") {
		t.Error("should show validation error")
	}
}

func TestLogout(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	w := httptest.NewRecorder()

	h.Logout(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("location = %q, want /login", loc)
	}

	// Check cookie is cleared.
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "rezuscloud_session" && c.MaxAge == -1 {
			found = true
		}
	}
	if !found {
		t.Error("session cookie should be cleared")
	}
}

func TestDashboard_Counts(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	h := NewHandler(store)

	// Create a provider.
	_, _ = store.CreateResource("provider", "hetzner", struct{}{}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	// Should have tenant count = 1, provider count = 1.
	if !strings.Contains(body, ">1<") {
		t.Error("should show count of 1 for tenants and providers")
	}
}

func TestDashboard_NoBorderRadius(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, "border-radius") && !strings.Contains(body, "border-radius: 0") {
		t.Error("should not have non-zero border-radius")
	}
}
