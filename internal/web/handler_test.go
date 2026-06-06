package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
)

// newTestHandler constructs a Handler backed by a fresh in-memory store.
func newTestHandler(t *testing.T) (*Handler, *state.Store) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jwt := auth.NewJWTManager("test-secret")
	return NewHandler(store, jwt, nil), store
}

// TestNewHandler_Constructs verifies NewHandler wires dependencies.
func TestNewHandler_Constructs(t *testing.T) {
	h, _ := newTestHandler(t)
	if h == nil || h.store == nil || h.jwtManager == nil {
		t.Fatal("NewHandler didn't wire dependencies")
	}
}

// TestWith_Injectors verifies all With* dependency-injection methods.
func TestWith_Injectors(t *testing.T) {
	h, _ := newTestHandler(t)

	if h.WithUpgradeManager(nil) != h {
		t.Error("WithUpgradeManager should return h")
	}
	if h.WithBackupService(nil) != h {
		t.Error("WithBackupService should return h")
	}
	if h.WithBackupComponent(nil) != h {
		t.Error("WithBackupComponent should return h")
	}
	if h.WithAuditStore(nil) != h {
		t.Error("WithAuditStore should return h")
	}
	if h.WithAuditComponent(nil) != h {
		t.Error("WithAuditComponent should return h")
	}
}

// TestAuthRequired_RedirectsWhenNoCookie verifies unauthenticated requests
// redirect to /login.
func TestAuthRequired_RedirectsWhenNoCookie(t *testing.T) {
	h, _ := newTestHandler(t)
	called := false
	wrapped := h.AuthRequired(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	if called {
		t.Error("inner handler should not be called when no cookie")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("Location = %q, want /login", w.Header().Get("Location"))
	}
}

// TestAuthRequired_RedirectsOnInvalidToken verifies invalid JWT cookies
// trigger redirect + cookie clear.
func TestAuthRequired_RedirectsOnInvalidToken(t *testing.T) {
	h, _ := newTestHandler(t)
	wrapped := h.AuthRequired(func(_ http.ResponseWriter, _ *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "rezuscloud_session", Value: "not-a-jwt"})
	w := httptest.NewRecorder()
	wrapped(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_session" && c.MaxAge == -1 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie should be cleared (MaxAge=-1)")
	}
}

// TestRender_RedirectsWhenNoUser verifies Render redirects when no auth
// context (defensive — AuthRequired should have caught this).
func TestRender_RedirectsWhenNoUser(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Render(w, req, layout.BaseProps{Page: "dashboard"})

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

// TestRender_AllowsLoginPageWithoutUser verifies the login page is exempt.
// Note: we can't render a full page in this test because Content would be
// nil and templ panics on nil components; the redirect-path tests cover the
// important branches.

// TestPopToast verifies toast extraction from query params.
func TestPopToast(t *testing.T) {
	h, _ := newTestHandler(t)

	// No toast.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if toast := h.PopToast(req); toast.Message != "" {
		t.Errorf("expected empty toast, got %+v", toast)
	}

	// Toast present.
	req = httptest.NewRequest(http.MethodGet, "/?toast=hello&toast-type=success", nil)
	toast := h.PopToast(req)
	if toast.Message != "hello" {
		t.Errorf("Message = %q, want hello", toast.Message)
	}
	if toast.Type != "success" {
		t.Errorf("Type = %q, want success", toast.Type)
	}
}

// TestCanMutate verifies role-based mutation gate.
func TestCanMutate(t *testing.T) {
	h, _ := newTestHandler(t)

	for _, role := range []string{"admin", "edit"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(auth.WithClaims(req.Context(), "u", role))
		if !h.CanMutate(req) {
			t.Errorf("role %q should be able to mutate", role)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithClaims(req.Context(), "u", "view"))
	if h.CanMutate(req) {
		t.Error("role 'view' should NOT be able to mutate")
	}
}

// TestIsAdmin verifies admin-only check.
func TestIsAdmin(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithClaims(req.Context(), "u", "admin"))
	if !h.IsAdmin(req) {
		t.Error("admin role should be admin")
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithClaims(req.Context(), "u", "edit"))
	if h.IsAdmin(req) {
		t.Error("edit role should NOT be admin")
	}
}

// TestRedirectAction_PlainRedirect verifies non-HTMX requests get a 303.
func TestRedirectAction_PlainRedirect(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.RedirectAction(w, req, "/target")
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if w.Header().Get("Location") != "/target" {
		t.Errorf("Location = %q, want /target", w.Header().Get("Location"))
	}
}

// TestRedirectAction_HTMXRedirect verifies HTMX requests get HX-Redirect header.
func TestRedirectAction_HTMXRedirect(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.RedirectAction(w, req, "/target")
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if w.Header().Get("HX-Redirect") != "/target" {
		t.Errorf("HX-Redirect = %q, want /target", w.Header().Get("HX-Redirect"))
	}
}

// TestBusPresent verifies the bus presence check.
func TestBusPresent(t *testing.T) {
	h, _ := newTestHandler(t)
	if h.BusPresent() {
		t.Error("BusPresent should be false when bus is nil")
	}
}

// TestMachineLinkEndpoint verifies the placeholder endpoint.
func TestMachineLinkEndpoint(t *testing.T) {
	h, _ := newTestHandler(t)
	if ep := h.MachineLinkEndpoint(); ep == "" {
		t.Error("MachineLinkEndpoint should not be empty")
	}
}

// TestTenantSummaries_EmptyStore verifies the summary loader on an empty store.
func TestTenantSummaries_EmptyStore(t *testing.T) {
	h, _ := newTestHandler(t)
	summaries := h.TenantSummaries()
	if len(summaries) != 0 {
		t.Errorf("expected 0 summaries, got %d", len(summaries))
	}
}

// TestClusterNames_EqualsTenantNames verifies the two helpers agree.
func TestClusterNames_EqualsTenantNames(t *testing.T) {
	h, store := newTestHandler(t)
	if _, err := store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := h.ClusterNames()
	t2 := h.TenantNames()
	if len(c) != 1 || len(t2) != 1 {
		t.Fatalf("ClusterNames=%v TenantNames=%v", c, t2)
	}
	if c[0] != "prod" || t2[0] != "prod" {
		t.Errorf("expected both to be [prod], got %v and %v", c, t2)
	}
}

// TestRegisterRoutes_NoPanic verifies all section sub-packages wire without
// panicking on a fresh Handler. (Functional route tests live in each
// sub-package's *_test.go.)
func TestRegisterRoutes_NoPanic(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RegisterRoutes panicked: %v", r)
		}
	}()
	h.RegisterRoutes(mux)
}

// TestRegisterRoutes_WebUIRoutesResolve verifies the canonical WebUI routes
// are registered by sampling a few from each sub-package.
func TestRegisterRoutes_WebUIRoutesResolve(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/login"},
		{http.MethodPost, "/login"},
		{http.MethodGet, "/logout"},
		{http.MethodGet, "/clusters"},
		{http.MethodGet, "/machines"},
		{http.MethodGet, "/settings"},
		{http.MethodGet, "/providers"},
		{http.MethodGet, "/static/logs-stream.js"},
	}
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s not registered (got 404)", r.method, r.path)
		}
	}
}
