package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
)

// TestMain lowers the bcrypt cost for the test suite so user creation/login
// don't dominate the runtime on slow CI runners (ARM64).
func TestMain(m *testing.M) {
	_ = os.Setenv("REZUSCLOUD_BCRYPT_COST", "6")
	os.Exit(m.Run())
}

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

// newTestHandler builds a WebUI handler with a JWT manager and (optional) bus.
// All WebUI handler tests go through this helper so the signature change stays
// in one place.
func newTestHandler(t *testing.T, store *state.Store, opts ...func(*handlerCfg)) *Handler {
	t.Helper()
	cfg := handlerCfg{
		jwt: auth.NewJWTManager("test-secret"),
		bus: watch.NewBus(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	h := NewHandler(store, cfg.jwt, cfg.bus)
	if cfg.auditStore != nil {
		h = h.WithAuditStore(cfg.auditStore)
	}
	return h
}

// withAuditStore opts into the audit page (Handler defaults to nil audit store).
func withAuditStore(s audit.Store) func(*handlerCfg) {
	return func(c *handlerCfg) { c.auditStore = s }
}

type handlerCfg struct {
	jwt        *auth.JWTManager
	bus        *watch.Bus
	auditStore audit.Store
}

// createUser adds a user with a known bcrypt-hashed password.
func createUser(t *testing.T, store *state.Store, name, password string, role string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, err = store.CreateUser(name, state.UserSpec{
		Role:         role,
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// loginCookie performs a full login flow and returns the session cookie for
// follow-up authenticated requests.
func loginCookie(t *testing.T, h *Handler, username, password string) *http.Cookie {
	t.Helper()
	form := "username=" + username + "&password=" + password
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_session" {
			return c
		}
	}
	t.Fatal("login did not set session cookie")
	return nil
}

// authedRequest builds a request with a valid session cookie.
func authedRequest(method, target string, cookie *http.Cookie, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
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

// --- Auth flow tests ---

func TestDashboard_RequiresAuth_RedirectsToLogin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 (redirect to /login)", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("location = %q, want /login", w.Header().Get("Location"))
	}
}

func TestDashboard_AuthedUser_Renders(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequest(http.MethodGet, "/", cookie, "")
	w := httptest.NewRecorder()

	// Bypass AuthRequired middleware; call Dashboard directly.
	// Inject context via auth.WithClaims (as the middleware would).
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "admin") {
		t.Error("dashboard should contain the username")
	}
}

func TestLoginSubmit_ValidCredentials_IssuesJWTCookie(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "secret", auth.RoleAdmin)

	form := "username=alice&password=secret"
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if w.Header().Get("Location") != "/" {
		t.Errorf("location = %q, want /", w.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie not set")
	}
	if cookie.Value == "" || cookie.Value == "placeholder" {
		t.Errorf("cookie value = %q, expected real JWT", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
}

func TestLoginSubmit_InvalidCredentials_RendersError(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "secret", auth.RoleAdmin)

	form := "username=alice&password=wrong"
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-render with error)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Invalid username or password") {
		t.Errorf("body should contain error message; got: %s", body)
	}
}

func TestLoginSubmit_UnknownUser_RendersError(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	form := "username=nobody&password=anything"
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Invalid username or password") {
		t.Errorf("body should contain error; got: %s", body)
	}
}

func TestLoginSubmit_EmptyFields(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

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

func TestLogout_ClearsCookie(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	w := httptest.NewRecorder()
	h.Logout(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("location = %q, want /login", w.Header().Get("Location"))
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_session" && c.MaxAge != -1 {
			t.Errorf("cookie MaxAge = %d, want -1 (cleared)", c.MaxAge)
		}
	}
}

// --- Dashboard rendering tests ---

func TestDashboard_WithRealPhases(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	// Empty tenant (no machines, no node groups) → phase "forming".
	setupTenant(t, store, "prod")
	// Empty + no machines: phase is forming, not active.
	w := httptest.NewRecorder()
	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	h.Dashboard(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "forming") {
		t.Errorf("expected forming phase; got: %s", body)
	}
}

func TestDashboard_Counts(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	setupTenant(t, store, "prod")
	_, _ = store.CreateResource("provider", "hetzner", struct{}{}, nil, nil, nil)

	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	if !strings.Contains(body, ">1<") {
		t.Error("should show count of 1 for tenants and providers")
	}
}

func TestDashboard_NoBorderRadius(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, "border-radius") && !strings.Contains(body, "border-radius: 0") {
		t.Error("should not have non-zero border-radius")
	}
}

// --- Tenant list tests ---

func TestTenantsList_Empty(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	req := authedRequest(http.MethodGet, "/tenants", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
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
	h := newTestHandler(t, store)
	setupTenant(t, store, "prod")

	req := authedRequest(http.MethodGet, "/tenants", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantsList(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "prod") {
		t.Error("should list tenant 'prod'")
	}
	// Phase is computed (not hardcoded "active").
	if !strings.Contains(body, "forming") {
		t.Error("empty tenant without node groups should be in forming phase")
	}
}

// --- Tenant detail tests ---

func TestTenantDetail_Found(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	setupTenant(t, store, "prod")

	req := authedRequest(http.MethodGet, "/tenants/prod", nil, "")
	req.SetPathValue("name", "prod")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
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
	h := newTestHandler(t, store)

	req := authedRequest(http.MethodGet, "/tenants/nonexistent", nil, "")
	req.SetPathValue("name", "nonexistent")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestTenantDetail_MachineStageIsReal(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	setupTenant(t, store, "prod")

	// Create a machine with a known stage.
	_, _ = store.CreateMachine("machine-uuid", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod", "rezuscloud.io/role": "worker"}, nil)
	// Update its status to a known stage.
	_, _ = store.UpdateMachineStatus("machine-uuid", state.MachineStatus{Stage: state.StageReady, Ready: true, Role: "worker"})

	req := authedRequest(http.MethodGet, "/tenants/prod", nil, "")
	req.SetPathValue("name", "prod")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "ready") {
		t.Errorf("body should contain 'ready' stage; got: %s", body)
	}
	if strings.Contains(body, "unknown") {
		t.Errorf("body should not contain 'unknown' stage; got: %s", body)
	}
}

// --- Login page tests ---

func TestLoginPage(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

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

// --- Auth middleware tests ---

func TestAuthRequired_NoCookie_Redirects(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	called := false
	wrapped := h.AuthRequired(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	if called {
		t.Error("handler should not be called when no cookie")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestAuthRequired_InvalidCookie_Redirects(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	called := false
	wrapped := h.AuthRequired(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "rezuscloud_session", Value: "invalid-token"})
	w := httptest.NewRecorder()
	wrapped(w, req)

	if called {
		t.Error("handler should not be called with invalid cookie")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestAuthRequired_ValidCookie_CallsHandler(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	called := false
	username := ""
	wrapped := h.AuthRequired(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		username = auth.UserFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	wrapped(w, req)

	if !called {
		t.Error("handler should be called with valid cookie")
	}
	if username != "admin" {
		t.Errorf("username = %q, want admin", username)
	}
}

// --- W2: Navigation shell tests ---

func TestDashboard_NoBreadcrumbByDefault(t *testing.T) {
	// Dashboard is the root page — no breadcrumb.
	store := newTestStore(t)
	h := newTestHandler(t, store)

	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	// Extract body after </style> so we don't match CSS definitions.
	body := w.Body.String()
	idx := strings.Index(body, "</style>")
	if idx >= 0 {
		body = body[idx+len("</style>"):]
	}
	if strings.Contains(body, "ds-breadcrumb") {
		t.Errorf("dashboard should not have breadcrumb; got: %s", body)
	}
}

func TestTenantsList_HasBreadcrumb(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	req := authedRequest(http.MethodGet, "/clusters", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantsList(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "ds-breadcrumb") {
		t.Errorf("clusters list should have breadcrumb; got: %s", body)
	}
	// Breadcrumb should have Home + "Clusters".
	if !strings.Contains(body, `href="/"`) {
		t.Error("breadcrumb should link to Home")
	}
	if !strings.Contains(body, "Clusters") {
		t.Error("breadcrumb should contain 'Clusters'")
	}
}

func TestTenantDetail_HasBreadcrumbWithClusterName(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	setupTenant(t, store, "prod")

	req := authedRequest(http.MethodGet, "/clusters/prod", nil, "")
	req.SetPathValue("name", "prod")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	body := w.Body.String()
	// Should have breadcrumb with: Home / Clusters / prod
	if !strings.Contains(body, "ds-breadcrumb") {
		t.Errorf("tenant detail should have breadcrumb")
	}
	if !strings.Contains(body, `href="/clusters"`) {
		t.Error("breadcrumb should link to /clusters")
	}
	if !strings.Contains(body, ">prod<") && !strings.Contains(body, ">prod <") {
		t.Error("breadcrumb should contain cluster name 'prod'")
	}
}

func TestClustersAlias_RendersTenantsList(t *testing.T) {
	// /clusters and /tenants should render the same handler.
	store := newTestStore(t)
	h := newTestHandler(t, store)
	setupTenant(t, store, "alpha")

	req := authedRequest(http.MethodGet, "/clusters", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "alpha") {
		t.Errorf("body should list tenant alpha; got: %s", body)
	}
}

func TestClustersNameAlias_RendersTenantDetail(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	setupTenant(t, store, "alpha")

	req := authedRequest(http.MethodGet, "/clusters/alpha", nil, "")
	req.SetPathValue("name", "alpha")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "alpha") {
		t.Error("body should show cluster alpha")
	}
}

func TestSidebar_HasAllNavEntries(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)

	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	expectedNav := []string{
		`href="/"`,
		`href="/clusters"`,
		`href="/machines"`,
		`href="/machines/jointokens"`,
		`href="/providers"`,
		`href="/settings/users"`,
		`href="/settings/api-tokens"`,
		`href="/settings/audit"`,
		`href="/settings/backups"`,
		"Overview",
		"Clusters",
		"Machines",
		"Join Tokens",
		"Providers",
		"Users",
		"API Tokens",
		"Audit Log",
		"Backups",
	}
	for _, expected := range expectedNav {
		if !strings.Contains(body, expected) {
			t.Errorf("sidebar should contain %q; body excerpt: %s", expected, body[min(2000, len(body)):])
		}
	}
}

func TestToastRenderedWhenQueryParam(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)

	req := authedRequest(http.MethodGet, "/?toast=Created+cluster&toast-type=success", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "ds-toast") {
		t.Errorf("should contain toast element")
	}
	if !strings.Contains(body, "Created cluster") {
		t.Errorf("should contain toast message; got: %s", body)
	}
	if !strings.Contains(body, "ds-toast--success") {
		t.Error("should have ds-toast--success class")
	}
}

func TestToastNotRenderedWhenMissingQueryParam(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)

	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	// Extract body after </style> so we don't match CSS definitions.
	body := w.Body.String()
	idx := strings.Index(body, "</style>")
	if idx >= 0 {
		body = body[idx+len("</style>"):]
	}
	if strings.Contains(body, "ds-toast-container") || strings.Contains(body, "role=\"status\"") {
		t.Errorf("should not render toast container when no flash message; got: %s", body)
	}
}

func TestSidebar_HasActiveHighlight(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)

	// Dashboard page should highlight the "Overview" link.
	req := authedRequest(http.MethodGet, "/", nil, "")
	ctx := auth.WithClaims(req.Context(), "admin", auth.RoleAdmin)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "ds-sidebar-link--active") {
		t.Error("dashboard should mark a sidebar link as active")
	}
}

// --- W3 tests ---

// authedRequestAs builds a request with a valid session cookie AND the
// user/role injected into the request context. Use this for tests that call
// the inner handler directly (without going through AuthRequired middleware).
func authedRequestAs(method, target string, cookie *http.Cookie, body, user, role string) *http.Request {
	req := authedRequest(method, target, cookie, body)
	ctx := auth.WithClaims(req.Context(), user, role)
	return req.WithContext(ctx)
}

func TestTenantsList_HasCreateButton(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/clusters", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.TenantsList(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "/clusters/create") {
		t.Errorf("expected /clusters/create link in /clusters page; got body tail: %s", body[len(body)-min(len(body), 400):])
	}
	if !strings.Contains(body, "Create Cluster") {
		t.Errorf("expected 'Create Cluster' button label; got body tail: %s", body[len(body)-min(len(body), 400):])
	}
}

func TestClusterCreatePage_Renders(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/clusters/create", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.ClusterCreatePage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Create Cluster") {
		t.Error("page should render 'Create Cluster' title")
	}
	if !strings.Contains(body, `name="name"`) {
		t.Error("page should render the name input")
	}
	if !strings.Contains(body, `name="kubernetesVersion"`) {
		t.Error("page should render the kubernetesVersion select")
	}
	if !strings.Contains(body, `name="talosVersion"`) {
		t.Error("page should render the talosVersion select")
	}
}

func TestClusterCreateSubmit_Valid(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	form := "name=my-new-cluster&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	req := authedRequestAs(http.MethodPost, "/clusters/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("HX-Redirect")
	if !strings.Contains(loc, "/clusters/my-new-cluster") {
		t.Errorf("HX-Redirect = %q, want /clusters/my-new-cluster...", loc)
	}
	if !strings.Contains(loc, "toast=") {
		t.Errorf("HX-Redirect = %q, missing toast query param", loc)
	}

	// Verify the tenant was actually created.
	tenant, err := store.GetTenant("my-new-cluster")
	if err != nil || tenant == nil {
		t.Errorf("tenant not found after create: err=%v, tenant=%v", err, tenant)
	}

	// Verify secrets were auto-generated.
	bundle, _ := store.LoadTenantSecrets("my-new-cluster")
	if bundle == nil {
		t.Error("expected auto-generated secrets bundle after create")
	}
}

func TestClusterCreateSubmit_InvalidName(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	form := "name=UPPERCASE&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	req := authedRequestAs(http.MethodPost, "/clusters/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)

	// No redirect — form re-renders with validation error.
	if loc := w.Header().Get("HX-Redirect"); loc != "" {
		t.Errorf("HX-Redirect should be empty on validation error, got %q", loc)
	}
	body := w.Body.String()
	if !strings.Contains(body, "must match") && !strings.Contains(body, "lowercase") {
		t.Errorf("expected validation error mentioning lowercase/must match; got body: %s", body)
	}
}

func TestClusterCreateSubmit_Duplicate(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// First create succeeds.
	form := "name=existing&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	req := authedRequestAs(http.MethodPost, "/clusters/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("first create failed: %d", w.Code)
	}

	// Second create with same name fails.
	req = authedRequestAs(http.MethodPost, "/clusters/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)

	if loc := w.Header().Get("HX-Redirect"); loc != "" {
		t.Errorf("HX-Redirect should be empty on duplicate, got %q", loc)
	}
	body := w.Body.String()
	if !strings.Contains(body, "already exists") {
		t.Errorf("expected 'already exists' error; got body: %s", body)
	}
}

func TestClusterDelete_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// Create a tenant via the store directly.
	setupTenant(t, store, "to-delete")

	req := authedRequestAs(http.MethodDelete, "/clusters/to-delete", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "to-delete")
	w := httptest.NewRecorder()
	h.ClusterDelete(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("HX-Redirect")
	if !strings.Contains(loc, "/clusters") {
		t.Errorf("HX-Redirect = %q, want /clusters", loc)
	}

	// Verify the tenant is marked deleted (deletionTimestamp set).
	tenant, _ := store.GetTenant("to-delete")
	if tenant == nil {
		// OK — depends on store behaviour. Either deleted outright or marked.
		return
	}
	if tenant.Metadata.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to be set after DELETE")
	}
}

func TestClusterDelete_ViewRole_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupTenant(t, store, "view-only")

	req := authedRequestAs(http.MethodDelete, "/clusters/view-only", cookie, "", "viewer", auth.RoleView)
	req.SetPathValue("name", "view-only")
	w := httptest.NewRecorder()
	h.ClusterDelete(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for view role; body: %s", w.Code, w.Body.String())
	}
}

func TestClusterKubeconfig_Download(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// Create tenant via the submit handler so secrets are auto-generated.
	form := "name=kc-cluster&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	req := authedRequestAs(http.MethodPost, "/clusters/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("create failed: %d; body: %s", w.Code, w.Body.String())
	}

	// Download kubeconfig.
	req = authedRequestAs(http.MethodGet, "/clusters/kc-cluster/kubeconfig", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "kc-cluster")
	w = httptest.NewRecorder()
	h.ClusterKubeconfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="kc-cluster-kubeconfig.yaml"`) {
		t.Errorf("Content-Disposition = %q, missing filename", cd)
	}
	body := w.Body.String()
	if !strings.Contains(body, "apiVersion: v1") {
		t.Errorf("kubeconfig missing apiVersion: v1; got:\n%s", body[:min(len(body), 200)])
	}
}

func TestClusterKubeconfig_NotFound(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/clusters/nope/kubeconfig", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()
	h.ClusterKubeconfig(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestClusterTalosconfig_Download(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	form := "name=tc-cluster&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	req := authedRequestAs(http.MethodPost, "/clusters/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)

	req = authedRequestAs(http.MethodGet, "/clusters/tc-cluster/talosconfig", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "tc-cluster")
	w = httptest.NewRecorder()
	h.ClusterTalosconfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="tc-cluster-talosconfig.yaml"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	body := w.Body.String()
	if !strings.Contains(body, "context:") {
		t.Errorf("talosconfig missing 'context:'; got:\n%s", body[:min(len(body), 200)])
	}
}

func TestNodeGroupScale_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// Create tenant + node group.
	setupTenant(t, store, "ng-test")
	_, err := store.CreateResource("nodegroup", "workers",
		map[string]any{"name": "workers", "role": "worker", "count": 3},
		nil,
		map[string]string{"rezuscloud.io/tenant": "ng-test"},
		nil,
	)
	if err != nil {
		t.Fatalf("create nodegroup: %v", err)
	}

	// Scale up to 5.
	form := "count=5"
	req := authedRequestAs(http.MethodPost, "/clusters/ng-test/nodegroups/workers/scale", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "ng-test")
	req.SetPathValue("ng", "workers")
	w := httptest.NewRecorder()
	h.NodeGroupScale(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("HX-Redirect")
	if !strings.Contains(loc, "/clusters/ng-test") {
		t.Errorf("HX-Redirect = %q, want /clusters/ng-test", loc)
	}
	if !strings.Contains(loc, "toast=") {
		t.Errorf("HX-Redirect = %q, missing toast param", loc)
	}

	// Verify the count was updated.
	var spec struct {
		Count int `json:"count"`
	}
	_, err = store.GetResource("nodegroup", "workers", &spec, nil)
	if err != nil {
		t.Fatalf("get nodegroup: %v", err)
	}
	if spec.Count != 5 {
		t.Errorf("count = %d, want 5", spec.Count)
	}
}

func TestNodeGroupScale_ViewRole_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupTenant(t, store, "view-test")
	_, _ = store.CreateResource("nodegroup", "w",
		map[string]any{"name": "w", "role": "worker", "count": 1},
		nil,
		map[string]string{"rezuscloud.io/tenant": "view-test"},
		nil,
	)

	req := authedRequestAs(http.MethodPost, "/clusters/view-test/nodegroups/w/scale", cookie, "count=3", "viewer", auth.RoleView)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "view-test")
	req.SetPathValue("ng", "w")
	w := httptest.NewRecorder()
	h.NodeGroupScale(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestNodeGroupScale_InvalidCount(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "bad-count")
	_, _ = store.CreateResource("nodegroup", "w",
		map[string]any{"name": "w", "role": "worker", "count": 1},
		nil,
		map[string]string{"rezuscloud.io/tenant": "bad-count"},
		nil,
	)

	// Negative count.
	req := authedRequestAs(http.MethodPost, "/clusters/bad-count/nodegroups/w/scale", cookie, "count=-5", "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "bad-count")
	req.SetPathValue("ng", "w")
	w := httptest.NewRecorder()
	h.NodeGroupScale(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for negative count", w.Code)
	}

	// Non-numeric. (Returns 403 — role check fires before count validation.)
	req = authedRequestAs(http.MethodPost, "/clusters/bad-count/nodegroups/w/scale", cookie, "count=not-a-number", "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "bad-count")
	req.SetPathValue("ng", "w")
	w = httptest.NewRecorder()
	h.NodeGroupScale(w, req)
	// Role check passes for admin (we're authenticated as admin above); expect 400.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for non-numeric count", w.Code)
	}
}

func TestTenantDetail_TabsRender(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "with-tabs")

	// Default URL: /clusters/{name} → overview tab.
	req := authedRequestAs(http.MethodGet, "/clusters/with-tabs", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "with-tabs")
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// Strip <style> content so class assertions don't match CSS rules.
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, `class="ds-tabs-link ds-tabs-link--active"`) {
		t.Errorf("expected exactly one active tab (Overview by default). Body tail:\n%s", body[:min(len(body), 800)])
	}
	if !strings.Contains(body, `aria-selected="true"`) {
		t.Errorf("expected aria-selected=\"true\" on active tab. Body tail:\n%s", body[:min(len(body), 800)])
	}
}

func TestTenantDetail_SettingsTab_AdminShowsDelete(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "destroy-me")

	req := authedRequestAs(http.MethodGet, "/clusters/destroy-me/settings", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "destroy-me")
	req.SetPathValue("tab", "settings")
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "Danger Zone") {
		t.Errorf("settings tab should show 'Danger Zone' section for admin. Body tail:\n%s", body[:min(len(body), 800)])
	}
	if !strings.Contains(body, `data-modal-open="delete-destroy-me"`) {
		t.Errorf("settings tab should render delete modal trigger. Body tail:\n%s", body[:min(len(body), 800)])
	}
}

func TestTenantDetail_SettingsTab_ViewHidesDelete(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")
	setupTenant(t, store, "no-delete")

	req := authedRequestAs(http.MethodGet, "/clusters/no-delete/settings", cookie, "", "viewer", auth.RoleView)
	req.SetPathValue("name", "no-delete")
	req.SetPathValue("tab", "settings")
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if strings.Contains(body, "Danger Zone") {
		t.Error("settings tab should NOT show 'Danger Zone' for view role")
	}
	if strings.Contains(body, "data-modal-open=\"delete-") {
		t.Error("settings tab should NOT render delete modal trigger for view role")
	}
}

func TestValidClusterName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"my-cluster", true},
		{"a", false}, // too short
		{"ab", true}, // min length
		{"abc123-xyz", true},
		{"UPPERCASE", false},
		{"1starts-with-digit", false},
		{"-starts-with-hyphen", false},
		{"has/slash", false},
		{"has_underscore", false},
		{strings.Repeat("a", 63), true},  // max length
		{strings.Repeat("a", 64), false}, // too long
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validClusterName(tc.name)
			if got != tc.want {
				t.Errorf("validClusterName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// --- W4: Machines & Join Tokens tests ---

func setupMachine(t *testing.T, store *state.Store, id string, tenant, role string, stage state.MachineStage, connected bool) {
	t.Helper()
	_, err := store.CreateMachine(id, state.MachineSpec{Connected: connected},
		map[string]string{
			"rezuscloud.io/tenant":     tenant,
			"rezuscloud.io/role":       role,
			"rezuscloud.io/node-group": "workers",
		},
		nil,
	)
	if err != nil {
		t.Fatalf("create machine %s: %v", id, err)
	}
	if _, err := store.UpdateMachineStatus(id, state.MachineStatus{
		Stage: stage,
		Role:  role,
		Ready: stage == state.StageReady,
	}); err != nil {
		t.Fatalf("update status %s: %v", id, err)
	}
}

func TestMachinesList_Empty(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/machines", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No machines registered") {
		t.Error("empty state should be shown")
	}
}

func TestMachinesList_WithMachines(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "11111111-aaaa-bbbb-cccc-dddddddddddd", "alpha", "controlplane", state.StageReady, true)
	setupMachine(t, store, "22222222-aaaa-bbbb-cccc-dddddddddddd", "alpha", "worker", state.StageInitializing, false)

	req := authedRequestAs(http.MethodGet, "/machines", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// Strip style block.
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "11111111") {
		t.Error("machine 11111111 not in list")
	}
	if !strings.Contains(body, "22222222") {
		t.Error("machine 22222222 not in list")
	}
	if !strings.Contains(body, "/machines/jointokens") {
		t.Error("join tokens link should be on machines page")
	}
}

func TestMachinesList_FilterByCluster(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupTenant(t, store, "beta")
	setupMachine(t, store, "machine-alpha", "alpha", "worker", state.StageReady, true)
	setupMachine(t, store, "machine-beta", "beta", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines?cluster=alpha", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "machine-alpha") {
		t.Error("alpha machine should be present")
	}
	if strings.Contains(body, "machine-beta") {
		t.Error("beta machine should be filtered out")
	}
}

func TestMachinesList_FilterByStage(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ready-machine", "alpha", "worker", state.StageReady, true)
	setupMachine(t, store, "init-machine", "alpha", "worker", state.StageInitializing, false)

	req := authedRequestAs(http.MethodGet, "/machines?stage=ready", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "ready-machine") {
		t.Error("ready machine should be present")
	}
	if strings.Contains(body, "init-machine") {
		t.Error("init machine should be filtered out")
	}
}

func TestMachinesList_FilterConnectedOnly(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "conn-machine", "alpha", "worker", state.StageReady, true)
	setupMachine(t, store, "disc-machine", "alpha", "worker", state.StageReady, false)

	req := authedRequestAs(http.MethodGet, "/machines?connected=true", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "conn-machine") {
		t.Error("connected machine should be present")
	}
	if strings.Contains(body, "disc-machine") {
		t.Error("disconnected machine should be filtered out")
	}
}

func TestMachineDetail_Found(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "11111111-aaaa-bbbb-cccc-dddddddddddd", "alpha", "controlplane", state.StageReady, true)
	_, _ = store.UpdateMachineStatus("11111111-aaaa-bbbb-cccc-dddddddddddd", state.MachineStatus{
		Stage:        state.StageReady,
		Role:         "controlplane",
		Ready:        true,
		TalosVersion: "1.12.0",
		K8sVersion:   "1.35.0",
	})

	req := authedRequestAs(http.MethodGet, "/machines/11111111-aaaa-bbbb-cccc-dddddddddddd", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "11111111-aaaa-bbbb-cccc-dddddddddddd")
	w := httptest.NewRecorder()
	h.MachineDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "11111111-aaaa-bbbb-cccc-dddddddddddd") {
		t.Error("machine ID not on page")
	}
	if !strings.Contains(body, "1.12.0") {
		t.Error("talos version not on page")
	}
	if !strings.Contains(body, "1.35.0") {
		t.Error("k8s version not on page")
	}
}

func TestMachineDetail_NotFound(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/machines/nonexistent", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	h.MachineDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMachineDetail_ViewRoleHidesActions(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "machine-actions", "alpha", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/machine-actions", cookie, "", "viewer", auth.RoleView)
	req.SetPathValue("id", "machine-actions")
	w := httptest.NewRecorder()
	h.MachineDetail(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if strings.Contains(body, `data-modal-open="restart-machine"`) {
		t.Error("view role should not see restart button")
	}
}

func TestMachineDetail_AdminShowsActions(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "machine-actions", "alpha", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/machine-actions", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "machine-actions")
	w := httptest.NewRecorder()
	h.MachineDetail(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, `data-modal-open="restart-machine"`) {
		t.Error("admin should see restart button")
	}
	if !strings.Contains(body, `data-modal-open="shutdown-machine"`) {
		t.Error("admin should see shutdown button")
	}
}

func TestMachineRestart_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupMachine(t, store, "restart-me", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodPost, "/machines/restart-me/restart", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "restart-me")
	w := httptest.NewRecorder()
	h.MachineRestart(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "/machines/restart-me") {
		t.Errorf("Location = %q", w.Header().Get("Location"))
	}
}

func TestMachineRestart_ViewRole_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupMachine(t, store, "restart-view", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodPost, "/machines/restart-view/restart", cookie, "", "viewer", auth.RoleView)
	req.SetPathValue("id", "restart-view")
	w := httptest.NewRecorder()
	h.MachineRestart(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestMachineShutdown_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupMachine(t, store, "shutdown-me", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodPost, "/machines/shutdown-me/shutdown", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "shutdown-me")
	w := httptest.NewRecorder()
	h.MachineShutdown(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestMachineDelete_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupMachine(t, store, "delete-me", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodDelete, "/machines/delete-me", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "delete-me")
	w := httptest.NewRecorder()
	h.MachineDelete(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	// Machine should be marked deleted (deletionTimestamp set).
	m, _ := store.GetMachine("delete-me")
	if m == nil {
		t.Error("machine should still exist (graceful deletion sets timestamp)")
	}
	if m != nil && m.Metadata.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to be set")
	}
}

func TestMachineDelete_ViewRole_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupMachine(t, store, "view-only", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodDelete, "/machines/view-only", cookie, "", "viewer", auth.RoleView)
	req.SetPathValue("id", "view-only")
	w := httptest.NewRecorder()
	h.MachineDelete(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestMachinesPending_ShowsNonReadyMachines(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ready-m", "alpha", "worker", state.StageReady, true)
	setupMachine(t, store, "init-m", "alpha", "worker", state.StageInitializing, false)
	setupMachine(t, store, "install-m", "alpha", "worker", state.StageInstalling, false)

	req := authedRequestAs(http.MethodGet, "/machines/pending", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.MachinesPending(w, req)

	body := w.Body.String()
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if strings.Contains(body, "ready-m") {
		t.Error("ready machine should NOT appear in pending")
	}
	if !strings.Contains(body, "init-m") {
		t.Error("initializing machine should appear in pending")
	}
	if !strings.Contains(body, "install-m") {
		t.Error("installing machine should appear in pending")
	}
}

func TestJoinTokensList_Empty(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/machines/jointokens", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.JoinTokensList(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "No join tokens") {
		t.Error("empty state should be shown")
	}
	if !strings.Contains(body, `data-modal-open="create-jointoken"`) {
		t.Error("admin should see create button")
	}
}

func TestJoinTokensList_WithTokens(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	_, err := store.CreateJoinToken("aabbccdd11223344", state.JoinTokenSpec{
		NodeGroup: "workers",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}, "alpha", "workers")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	req := authedRequestAs(http.MethodGet, "/machines/jointokens", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.JoinTokensList(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "aabbccdd") {
		t.Error("token (truncated) not in list")
	}
	if !strings.Contains(body, "alpha") {
		t.Error("cluster label not in list")
	}
	if !strings.Contains(body, "workers") {
		t.Error("nodegroup not in list")
	}
}

func TestJoinTokenCreate_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")

	form := "cluster=alpha&nodegroup=workers&ttl=24h"
	req := authedRequestAs(http.MethodPost, "/machines/jointokens", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.JoinTokenCreate(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/machines/jointokens?new_token=") {
		t.Errorf("Location = %q", loc)
	}
}

func TestJoinTokenCreate_MissingFields(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	form := "cluster=&nodegroup="
	req := authedRequestAs(http.MethodPost, "/machines/jointokens", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.JoinTokenCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestJoinTokenCreate_ClusterNotFound(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	form := "cluster=nonexistent&nodegroup=workers"
	req := authedRequestAs(http.MethodPost, "/machines/jointokens", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.JoinTokenCreate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestJoinTokenCreate_ViewRole_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupTenant(t, store, "alpha")
	form := "cluster=alpha&nodegroup=workers"
	req := authedRequestAs(http.MethodPost, "/machines/jointokens", cookie, form, "viewer", auth.RoleView)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.JoinTokenCreate(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestJoinTokensList_ShowNewToken(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	_, _ = store.CreateJoinToken("my-new-token-value", state.JoinTokenSpec{
		NodeGroup: "workers",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}, "alpha", "workers")

	req := authedRequestAs(http.MethodGet, "/machines/jointokens?new_token=my-new-token-value", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.JoinTokensList(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "my-new-token-value") {
		t.Error("new token should be displayed")
	}
	if !strings.Contains(body, "Token created") {
		t.Error("Token created banner should be visible")
	}
	if !strings.Contains(body, "siderolink.api") {
		t.Error("kernel args preview should be shown")
	}
}

// --- W5: Machine deep dive tests ---

func setupTenantWithSecrets(t *testing.T, store *state.Store, name string) {
	t.Helper()
	_, err := store.CreateTenant(name, state.TenantSpec{
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	bundle, err := credentials.GenerateSecretsBundle("1.12.0")
	if err != nil {
		t.Fatalf("generate bundle: %v", err)
	}
	bundleJSON, err := credentials.SecretsBundleJSON(bundle)
	if err != nil {
		t.Fatalf("serialize bundle: %v", err)
	}
	if err := store.SaveTenantSecrets(name, bundleJSON); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
}

func TestMachineLogs_Empty(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupMachine(t, store, "logs-machine", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/logs-machine/logs", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "logs-machine")
	w := httptest.NewRecorder()
	h.MachineLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Page renders even with no logs.
	if !strings.Contains(body, "Logs —") {
		t.Error("missing logs title")
	}
}

func TestMachineLogs_NotFound(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/machines/nonexistent/logs", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	h.MachineLogs(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMachineConfig_Found(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenantWithSecrets(t, store, "alpha")
	setupMachine(t, store, "config-machine", "alpha", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/config-machine/config", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "config-machine")
	w := httptest.NewRecorder()
	h.MachineConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "version:") {
		t.Error("config page should show generated YAML")
	}
	if !strings.Contains(body, "machine:") {
		t.Error("config page should show machine section")
	}
}

func TestMachineConfig_MissingSecrets(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// Tenant without secrets.
	setupTenant(t, store, "nosec")
	setupMachine(t, store, "no-bundle", "nosec", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/no-bundle/config", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "no-bundle")
	w := httptest.NewRecorder()
	h.MachineConfig(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (no secrets)", w.Code)
	}
}

func TestMachineConfig_NotFound(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/machines/nonexistent/config", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	h.MachineConfig(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMachineKernelArgs_NoExisting(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ka-machine", "alpha", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/ka-machine/kernel-args", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("id", "ka-machine")
	w := httptest.NewRecorder()
	h.MachineKernelArgs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Kernel args editor") {
		t.Error("editor card should render")
	}
	if !strings.Contains(body, `<textarea`) {
		t.Error("textarea should be present for admin")
	}
}

func TestMachineKernelArgs_ViewRole(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ka-view", "alpha", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodGet, "/machines/ka-view/kernel-args", cookie, "", "viewer", auth.RoleView)
	req.SetPathValue("id", "ka-view")
	w := httptest.NewRecorder()
	h.MachineKernelArgs(w, req)

	body := w.Body.String()
	if strings.Contains(body, `<textarea`) {
		t.Error("view role should not see textarea")
	}
	if !strings.Contains(body, "edit") || !strings.Contains(body, "role") {
		t.Error("view role should see permission message")
	}
}

func TestMachineKernelArgsSave_CreateNew(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ka-save", "alpha", "worker", state.StageReady, true)

	form := "args=console=ttyS0%0Areboot=k" // URL-encoded: console=ttyS0 + newline + reboot=k
	req := authedRequestAs(http.MethodPost, "/machines/ka-save/kernel-args", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "ka-save")
	w := httptest.NewRecorder()
	h.MachineKernelArgsSave(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "kernel+args+saved") {
		t.Errorf("Location = %q", loc)
	}

	// Verify a ConfigPatch was created.
	patch, _ := store.GetResource("configpatch", "kernel-args-alpha", nil, nil)
	if patch.Name == "" {
		t.Error("expected kernel-args-alpha patch to exist")
	}
}

func TestMachineKernelArgsSave_InvalidArg(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ka-bad", "alpha", "worker", state.StageReady, true)

	// "foo=bar" doesn't start with an allowed prefix.
	form := "args=foo=bar"
	req := authedRequestAs(http.MethodPost, "/machines/ka-bad/kernel-args", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "ka-bad")
	w := httptest.NewRecorder()
	h.MachineKernelArgsSave(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect with error toast)", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "error") {
		t.Errorf("Location should indicate error: %q", w.Header().Get("Location"))
	}
}

func TestMachineKernelArgsSave_WhitespaceInArg(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	setupTenant(t, store, "alpha")
	setupMachine(t, store, "ka-ws", "alpha", "worker", state.StageReady, true)

	// "console ttyS0" has whitespace.
	form := "args=console+ttyS0"
	req := authedRequestAs(http.MethodPost, "/machines/ka-ws/kernel-args", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "ka-ws")
	w := httptest.NewRecorder()
	h.MachineKernelArgsSave(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect with error toast)", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "error") {
		t.Errorf("Location should indicate error: %q", w.Header().Get("Location"))
	}
}

func TestMachineKernelArgsSave_ViewRole_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	setupMachine(t, store, "ka-view", "", "worker", state.StageReady, true)

	req := authedRequestAs(http.MethodPost, "/machines/ka-view/kernel-args", cookie, "args=console=ttyS0", "viewer", auth.RoleView)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "ka-view")
	w := httptest.NewRecorder()
	h.MachineKernelArgsSave(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestIsValidKernelArg(t *testing.T) {
	cases := []struct {
		arg  string
		want bool
	}{
		{"talos.platform=metal", true},
		{"siderolink.api=tcp://...", true},
		{"console=ttyS0", true},
		{"reboot=k", true},
		{"mitigations=auto", true},
		{"ip=dhcp", true},
		{"foo=bar", false},
		{"random-string", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			if got := isValidKernelArg(tc.arg); got != tc.want {
				t.Errorf("isValidKernelArg(%q) = %v, want %v", tc.arg, got, tc.want)
			}
		})
	}
}

func TestBuildKernelArgsPatch(t *testing.T) {
	args := []string{"console=ttyS0", "reboot=k"}
	got := buildKernelArgsPatch(args)
	if !strings.Contains(got, "machine:") {
		t.Error("patch should contain 'machine:'")
	}
	if !strings.Contains(got, "extraKernelArgs:") {
		t.Error("patch should contain 'extraKernelArgs:'")
	}
	if !strings.Contains(got, "- console=ttyS0") {
		t.Error("patch should contain first arg")
	}
	if !strings.Contains(got, "- reboot=k") {
		t.Error("patch should contain second arg")
	}
}

// --- W6: ConfigPatch management tests ---

func TestClusterPatchCreate_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "alpha")

	form := "name=my-patch&format=strategic&targetRole=worker&enabled=true&patch=machine:%0A++install:%0A++++extraKernelArgs:%0A++++++-+console=ttyS0"
	req := authedRequestAs(http.MethodPost, "/clusters/alpha/patches/create", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "alpha")
	w := httptest.NewRecorder()
	h.ClusterPatchCreate(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var ps patch.PatchSpec
	md, err := store.GetResource("configpatch", "my-patch", &ps, nil)
	if err != nil || md.Name == "" {
		t.Fatalf("expected patch created: %v", err)
	}
	if ps.TargetRole != "worker" {
		t.Errorf("targetRole=%q", ps.TargetRole)
	}
}

func TestClusterPatchCreate_ViewForbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")
	setupTenant(t, store, "alpha")

	req := authedRequestAs(http.MethodPost, "/clusters/alpha/patches/create", cookie, "name=x&format=text&patch=a", "viewer", auth.RoleView)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "alpha")
	w := httptest.NewRecorder()
	h.ClusterPatchCreate(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", w.Code)
	}
}

func TestClusterPatchEdit_SaveToggleDelete(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "alpha")

	_, _ = store.CreateResource("configpatch", "my-patch", patch.PatchSpec{Patch: "machine:\n  install:\n    extraKernelArgs:\n      - console=ttyS0", Format: "strategic", TargetRole: "worker", Enabled: true}, nil, map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)

	// GET edit page.
	req := authedRequestAs(http.MethodGet, "/clusters/alpha/patches/my-patch", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "alpha")
	req.SetPathValue("patch", "my-patch")
	w := httptest.NewRecorder()
	h.ClusterPatchEditPage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("edit page status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Edit Patch") {
		t.Error("edit page title missing")
	}

	// SAVE update.
	form := "format=text&targetRole=all&enabled=true&patch=plain+text+patch"
	req = authedRequestAs(http.MethodPost, "/clusters/alpha/patches/my-patch/save", cookie, form, "admin", auth.RoleAdmin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "alpha")
	req.SetPathValue("patch", "my-patch")
	w = httptest.NewRecorder()
	h.ClusterPatchSave(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save status=%d body=%s", w.Code, w.Body.String())
	}
	var ps patch.PatchSpec
	_, _ = store.GetResource("configpatch", "my-patch", &ps, nil)
	if ps.Format != "text" || ps.Patch != "plain text patch" {
		t.Errorf("patch not updated: %+v", ps)
	}
	if ps.TargetRole != "" {
		t.Errorf("targetRole should map all->empty, got %q", ps.TargetRole)
	}

	// TOGGLE enabled.
	req = authedRequestAs(http.MethodPost, "/clusters/alpha/patches/my-patch/toggle", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "alpha")
	req.SetPathValue("patch", "my-patch")
	w = httptest.NewRecorder()
	h.ClusterPatchToggle(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("toggle status=%d", w.Code)
	}
	_, _ = store.GetResource("configpatch", "my-patch", &ps, nil)
	if ps.Enabled {
		t.Error("expected enabled toggled to false")
	}

	// DELETE patch.
	req = authedRequestAs(http.MethodPost, "/clusters/alpha/patches/my-patch/delete", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "alpha")
	req.SetPathValue("patch", "my-patch")
	w = httptest.NewRecorder()
	h.ClusterPatchDelete(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete status=%d", w.Code)
	}
	md, _ := store.GetResource("configpatch", "my-patch", nil, nil)
	if md.Name != "" {
		t.Error("patch should be deleted")
	}
}

func TestBackupsPage(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/settings/backups", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.BackupsPage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Snapshot catalog") {
		t.Fatalf("expected backup page content")
	}
}

func TestBackupsRunResources(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodPost, "/settings/backups/resources", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.BackupsRunResources(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "/settings/backups") {
		t.Fatalf("expected redirect to backups page")
	}
}

func TestClusterPatchesPreview(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "alpha")

	_, _ = store.CreateResource("configpatch", "cp", patch.PatchSpec{Patch: "cluster:\n  foo: bar", Format: "strategic", TargetRole: "controlplane", Enabled: true}, nil, map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)
	_, _ = store.CreateResource("configpatch", "all", patch.PatchSpec{Patch: "machine:\n  time: yes", Format: "strategic", TargetRole: "all", Enabled: true}, nil, map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)

	req := authedRequestAs(http.MethodGet, "/clusters/alpha/patches/preview?role=controlplane", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "alpha")
	w := httptest.NewRecorder()
	h.ClusterPatchesPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("preview status=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "cluster:") || !strings.Contains(body, "machine:") {
		t.Errorf("preview should contain resolved patches, got: %s", body)
	}
}

func TestClusterUpgradeStart(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "alpha")
	setupMachine(t, store, "m1", "alpha", "controlplane", state.StageReady, true)

	req := authedRequestAs(http.MethodPost, "/clusters/alpha/upgrade/start", cookie, "component=talos&version=1.13.0", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "alpha")
	w := httptest.NewRecorder()
	h.ClusterUpgradeStart(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start status=%d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/clusters/alpha/upgrade") {
		t.Fatalf("expected redirect to upgrade tab, got %s", loc)
	}
}

func TestClusterUpgradeStart_HTMXRedirect(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "alpha")
	setupMachine(t, store, "m1", "alpha", "controlplane", state.StageReady, true)

	req := authedRequestAs(http.MethodPost, "/clusters/alpha/upgrade/start", cookie, "component=talos&version=1.13.0", "admin", auth.RoleAdmin)
	req.Header.Set("HX-Request", "true")
	req.SetPathValue("name", "alpha")
	w := httptest.NewRecorder()
	h.ClusterUpgradeStart(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("start status=%d", w.Code)
	}
	if !strings.Contains(w.Header().Get("HX-Redirect"), "/clusters/alpha/upgrade") {
		t.Fatalf("expected HX-Redirect to upgrade tab, got %s", w.Header().Get("HX-Redirect"))
	}
}

func TestTenantDetail_UpgradeTabRendersRuns(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")
	setupTenant(t, store, "alpha")
	setupMachine(t, store, "m1", "alpha", "controlplane", state.StageReady, true)

	startReq := authedRequestAs(http.MethodPost, "/clusters/alpha/upgrade/start", cookie, "component=talos&version=1.13.0", "admin", auth.RoleAdmin)
	startReq.SetPathValue("name", "alpha")
	startW := httptest.NewRecorder()
	h.ClusterUpgradeStart(startW, startReq)

	req := authedRequestAs(http.MethodGet, "/clusters/alpha/upgrade", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "alpha")
	req.SetPathValue("tab", "upgrade")
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail status=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Upgrade Runs") {
		t.Fatalf("expected upgrade runs section")
	}
	if !strings.Contains(body, "1.13.0") {
		t.Fatalf("expected target version in rendered table")
	}
}

// --- W9: Users + API Tokens ---

func TestUsersPage_AdminSeesCreateButton(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/settings/users", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.UsersPage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Create User") {
		t.Error("admin should see create button")
	}
	if !strings.Contains(body, `name="name"`) {
		t.Error("admin should see create form")
	}
}

func TestUsersPage_ViewerSeesReadOnly(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	req := authedRequestAs(http.MethodGet, "/settings/users", cookie, "", "viewer", auth.RoleView)
	w := httptest.NewRecorder()
	h.UsersPage(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Create User") {
		t.Error("viewer should NOT see create button")
	}
}

func TestUserCreate_Admin(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	form := strings.NewReader("name=alice&role=edit&password=secret123")
	req := authedRequestAs(http.MethodPost, "/settings/users", cookie, "", "admin", auth.RoleAdmin)
	req.Body = io.NopCloser(form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.UserCreate(w, req)

	if w.Code != http.StatusSeeOther && w.Code != http.StatusNoContent && !strings.HasPrefix(w.Header().Get("Location"), "/settings/users") {
		t.Fatalf("create: status=%d, location=%q", w.Code, w.Header().Get("Location"))
	}

	u, _ := store.GetUser("alice")
	if u == nil {
		t.Fatalf("user alice should exist")
	}
	if u.Spec.Role != auth.RoleEdit {
		t.Errorf("role = %q, want edit", u.Spec.Role)
	}
}

func TestUserCreate_NonAdmin_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "viewer", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "viewer", "pass")

	form := strings.NewReader("name=alice&role=admin&password=secret123")
	req := authedRequestAs(http.MethodPost, "/settings/users", cookie, "", "viewer", auth.RoleView)
	req.Body = io.NopCloser(form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.UserCreate(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin create should be forbidden, got %d", w.Code)
	}
}

func TestUserDelete_CannotDeleteSelf(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodPost, "/settings/users/admin/delete", cookie, "", "admin", auth.RoleAdmin)
	req.SetPathValue("name", "admin")
	w := httptest.NewRecorder()
	h.UserDelete(w, req)

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "cannot+delete+current+user") {
		t.Errorf("expected self-delete refusal, got location=%q", loc)
	}
}

func TestAPITokensPage_OwnerSeesOwnTokens(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	createUser(t, store, "bob", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "alice", "pass")

	_, _, hash, _ := auth.GenerateAPIToken()
	_, _ = store.CreateAPIToken("tok_alice", "alice", hash, nil)
	_, _, hash2, _ := auth.GenerateAPIToken()
	_, _ = store.CreateAPIToken("tok_bob", "bob", hash2, nil)

	req := authedRequestAs(http.MethodGet, "/settings/api-tokens", cookie, "", "alice", auth.RoleEdit)
	w := httptest.NewRecorder()
	h.APITokensPage(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "tok_alice") {
		t.Errorf("alice should see her token, body tail: %s", body[len(body)-min(len(body), 400):])
	}
	if strings.Contains(body, "tok_bob") {
		t.Errorf("alice should NOT see bob's tokens")
	}
}

func TestAPITokensPage_AdminSeesAll(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	_, _, hash, _ := auth.GenerateAPIToken()
	_, _ = store.CreateAPIToken("tok_alice", "alice", hash, nil)

	req := authedRequestAs(http.MethodGet, "/settings/api-tokens", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.APITokensPage(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "tok_alice") {
		t.Errorf("admin should see all tokens")
	}
}

func TestAPITokenCreate_OneTimeReveal(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	cookie := loginCookie(t, h, "alice", "pass")

	form := strings.NewReader("expiresInDays=30")
	req := authedRequestAs(http.MethodPost, "/settings/users/alice/api-tokens", cookie, "", "alice", auth.RoleEdit)
	req.SetPathValue("name", "alice")
	req.Body = io.NopCloser(form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.APITokenCreate(w, req)

	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/settings/api-tokens") {
		t.Fatalf("expected redirect to /settings/api-tokens, got %q", loc)
	}

	// Confirm reveal cookie set with id|secret|expires format.
	var reveal *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_token_reveal" {
			reveal = c
		}
	}
	if reveal == nil {
		t.Fatalf("expected rezuscloud_token_reveal cookie")
	}
	parts := strings.SplitN(reveal.Value, "|", 3)
	if len(parts) < 3 {
		t.Fatalf("reveal cookie should have 3 parts, got %q", reveal.Value)
	}
	if !strings.HasPrefix(parts[0], "tok_") {
		t.Errorf("id = %q, want tok_ prefix", parts[0])
	}
	if !strings.HasPrefix(parts[1], "rez_") {
		t.Errorf("secret = %q, want rez_ prefix", parts[1])
	}

	// Confirm token persisted with hash.
	tok, _ := store.GetAPIToken(parts[0])
	if tok == nil {
		t.Fatalf("token should be persisted")
	}
	if tok.TokenHash == "" {
		t.Errorf("token hash should be stored")
	}
	if tok.TokenHash == parts[1] {
		t.Errorf("plaintext must never equal stored hash")
	}
}

func TestAPITokenCreate_OtherUser_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	createUser(t, store, "bob", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "bob", "pass")

	form := strings.NewReader("expiresInDays=30")
	req := authedRequestAs(http.MethodPost, "/settings/users/alice/api-tokens", cookie, "", "bob", auth.RoleView)
	req.SetPathValue("name", "alice")
	req.Body = io.NopCloser(form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.APITokenCreate(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("non-owner, non-admin create should be forbidden, got %d", w.Code)
	}
}

func TestAPITokenDelete_Owner(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	cookie := loginCookie(t, h, "alice", "pass")

	_, _, hash, _ := auth.GenerateAPIToken()
	_, _ = store.CreateAPIToken("tok_del", "alice", hash, nil)

	req := authedRequestAs(http.MethodPost, "/settings/api-tokens/tok_del/delete", cookie, "", "alice", auth.RoleEdit)
	req.SetPathValue("id", "tok_del")
	w := httptest.NewRecorder()
	h.APITokenDelete(w, req)

	if w.Code != http.StatusSeeOther && w.Code != http.StatusNoContent && !strings.HasPrefix(w.Header().Get("Location"), "/settings/api-tokens") {
		t.Fatalf("delete: status=%d, location=%q", w.Code, w.Header().Get("Location"))
	}
	tok, _ := store.GetAPIToken("tok_del")
	if tok != nil {
		t.Errorf("token should be deleted")
	}
}

func TestAPITokenDelete_OtherUser_Forbidden(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	createUser(t, store, "bob", "pass", auth.RoleView)
	cookie := loginCookie(t, h, "bob", "pass")

	_, _, hash, _ := auth.GenerateAPIToken()
	_, _ = store.CreateAPIToken("tok_alice", "alice", hash, nil)

	req := authedRequestAs(http.MethodPost, "/settings/api-tokens/tok_alice/delete", cookie, "", "bob", auth.RoleView)
	req.SetPathValue("id", "tok_alice")
	w := httptest.NewRecorder()
	h.APITokenDelete(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("non-owner, non-admin delete should be forbidden, got %d", w.Code)
	}
}

func TestAPITokensPage_RevealCookieCleared(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store)
	createUser(t, store, "alice", "pass", auth.RoleEdit)
	cookie := loginCookie(t, h, "alice", "pass")

	// Simulate the cookie set by APITokenCreate.
	req := authedRequestAs(http.MethodGet, "/settings/api-tokens", cookie, "", "alice", auth.RoleEdit)
	req.AddCookie(&http.Cookie{Name: "rezuscloud_token_reveal", Value: "tok_reveal|rez_secret|2099-01-01T00:00:00Z"})
	w := httptest.NewRecorder()
	h.APITokensPage(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "rez_secret") {
		t.Errorf("expected plaintext secret in body, got tail: %s", body[len(body)-min(len(body), 400):])
	}

	// Confirm cookie cleared on response.
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_token_reveal" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("reveal cookie should be cleared after rendering")
	}

	// Second render without the cookie should NOT show the secret.
	req2 := authedRequestAs(http.MethodGet, "/settings/api-tokens", cookie, "", "alice", auth.RoleEdit)
	w2 := httptest.NewRecorder()
	h.APITokensPage(w2, req2)
	if strings.Contains(w2.Body.String(), "rez_secret") {
		t.Errorf("plaintext secret must NOT appear on subsequent render")
	}
}

// --- W10: Audit page ---

func TestAuditPage_Renders(t *testing.T) {
	store := newTestStore(t)
	auditStore := audit.NewSQLStore(store.DB())
	h := newTestHandler(t, store, withAuditStore(auditStore))
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// Seed one event.
	_ = auditStore.InsertEvent(context.Background(), audit.Event{
		Method: "POST", Path: "/api/v1/tenants", UserName: "admin",
		Role: "admin", Verb: "create", Resource: "tenants", Status: 201,
		Timestamp: "2026-06-06T12:00:00Z", SourceIP: "127.0.0.1",
	})

	req := authedRequestAs(http.MethodGet, "/settings/audit", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.AuditPage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Audit Log") {
		t.Error("expected page title")
	}
	if !strings.Contains(body, "/api/v1/tenants") {
		t.Error("expected audit row path in body")
	}
	if !strings.Contains(body, "admin") {
		t.Error("expected user in body")
	}
}

func TestAuditPage_Filters(t *testing.T) {
	store := newTestStore(t)
	auditStore := audit.NewSQLStore(store.DB())
	h := newTestHandler(t, store, withAuditStore(auditStore))
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	// Seed two events with different verbs.
	_ = auditStore.InsertEvent(context.Background(), audit.Event{
		Method: "POST", Path: "/api/v1/tenants", UserName: "admin",
		Verb: "create", Resource: "tenants", Status: 201,
		Timestamp: "2026-06-06T12:00:00Z",
	})
	_ = auditStore.InsertEvent(context.Background(), audit.Event{
		Method: "DELETE", Path: "/api/v1/tenants/prod", UserName: "admin",
		Verb: "delete", Resource: "tenants", Status: 204,
		Timestamp: "2026-06-06T12:01:00Z",
	})

	req := authedRequestAs(http.MethodGet, "/settings/audit?verb=delete", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.AuditPage(w, req)
	body := w.Body.String()

	if !strings.Contains(body, "DELETE") {
		t.Errorf("expected DELETE in body")
	}
	if strings.Contains(body, "/api/v1/tenants</td>") || strings.Contains(body, "/api/v1/tenants</") {
		// The "POST /api/v1/tenants" row should be filtered out.
		// (Loose check; the rendered path includes method.)
	}
	if strings.Count(body, "<tr class=\"ds-table-row\">") != 1 {
		t.Errorf("filter should produce 1 row, body had %d", strings.Count(body, "<tr class=\"ds-table-row\">"))
	}
}

func TestAuditPage_NoStoreReturnsUnavailable(t *testing.T) {
	store := newTestStore(t)
	h := newTestHandler(t, store) // no audit store injected
	createUser(t, store, "admin", "pass", auth.RoleAdmin)
	cookie := loginCookie(t, h, "admin", "pass")

	req := authedRequestAs(http.MethodGet, "/settings/audit", cookie, "", "admin", auth.RoleAdmin)
	w := httptest.NewRecorder()
	h.AuditPage(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
