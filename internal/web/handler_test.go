package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
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
	return NewHandler(store, cfg.jwt, cfg.bus)
}

type handlerCfg struct {
	jwt *auth.JWTManager
	bus *watch.Bus
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
