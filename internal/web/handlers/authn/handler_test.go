package authn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	oidc "github.com/rezuscloud/rezuscloud/internal/auth/oidc"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
)

// stubRenderer captures the last props passed to Render so tests can inspect
// what the handler would have rendered without spinning up a full layout.
type stubRenderer struct {
	last layout.BaseProps
}

func (s *stubRenderer) Render(_ http.ResponseWriter, _ *http.Request, props layout.BaseProps) {
	s.last = props
}

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "authn.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createUser(t *testing.T, store *state.Store, name, password, role string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, err = store.CreateUser(name, state.UserSpec{Role: role, PasswordHash: hash})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func TestLoginPage_Renders(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	h.LoginPage(w, req)

	if r.last.Title != "Sign In" {
		t.Errorf("Title = %q, want %q", r.last.Title, "Sign In")
	}
	if r.last.Page != "login" {
		t.Errorf("Page = %q, want %q", r.last.Page, "login")
	}
}

func TestLoginSubmit_SuccessSetsCookie(t *testing.T) {
	store := newTestStore(t)
	createUser(t, store, "alice", "pw", "admin")
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("expected rezuscloud_session cookie to be set")
	}
	if cookie.Value == "" {
		t.Error("cookie value is empty")
	}
}

func TestLoginSubmit_BadCredentialsRendersError(t *testing.T) {
	store := newTestStore(t)
	createUser(t, store, "alice", "pw", "admin")
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)

	if w.Code != http.StatusOK {
		// Re-renders the form with HTTP 200 (no redirect on auth failure).
		t.Errorf("status = %d, want 200", w.Code)
	}
	// The content should include the error message — we can't easily inspect
	// the templ output without rendering, but the renderer captured the props.
	if !strings.Contains(strings.ToLower(r.last.Title), "sign in") {
		t.Errorf("Title = %q, want Sign In", r.last.Title)
	}
}

func TestLoginSubmit_EmptyFieldsErrors(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LoginSubmit(w, req)

	if r.last.Title != "Sign In" {
		t.Errorf("Title = %q, want Sign In", r.last.Title)
	}
}

func TestLogout_ClearsCookieAndRedirects(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	w := httptest.NewRecorder()
	h.Logout(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if w.Header().Get("Location") != "/login" {
		t.Errorf("Location = %q, want /login", w.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "rezuscloud_session" {
			cookie = c
		}
	}
	if cookie == nil || cookie.MaxAge != -1 {
		t.Error("expected session cookie with MaxAge=-1 (deleted)")
	}
}

func TestRegisterRoutes(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Verify each route is registered by issuing a request and checking it
	// doesn't 404. We use the method+path pattern matching.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/login"},
		{http.MethodPost, "/login"},
		{http.MethodGet, "/logout"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s not registered (got 404)", c.method, c.path)
		}
	}
}

// testOIDCHandler builds a real flow handler pointed at an unreachable
// issuer — construction is offline; only the button label is exercised.
// renderContent renders the captured templ component to HTML for
// content-level assertions.
func renderContent(t *testing.T, r *stubRenderer) string {
	t.Helper()
	var sb strings.Builder
	if err := r.last.Content.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render content: %v", err)
	}
	return sb.String()
}

func testOIDCHandler() *oidc.Handler {
	h, _ := oidc.NewHandler(oidc.Config{
		Issuer:       "https://127.0.0.1:1/app", // unreachable by construction
		ClientID:     "cid",
		ClientSecret: "csecret",
	}, nil, nil)
	return h
}

func TestLoginPage_ShowsOIDCButtonWhenEnabled(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r).WithOIDC(testOIDCHandler())

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	h.LoginPage(w, req)

	body := renderContent(t, r)
	if !strings.Contains(body, "/auth/oidc/login") {
		t.Error("login page must link the OIDC flow when enabled")
	}
	if !strings.Contains(body, "127.0.0.1") {
		t.Error("login page must show the provider name (issuer host)")
	}
}

func TestLoginPage_HidesOIDCButtonWhenDisabled(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	h.LoginPage(w, req)

	if strings.Contains(renderContent(t, r), "/auth/oidc/login") {
		t.Error("login page must not offer OIDC when disabled")
	}
}

func TestLoginPage_ErrorParamSurfaced(t *testing.T) {
	store := newTestStore(t)
	r := &stubRenderer{}
	h := New(store, auth.NewJWTManager("k"), r).WithOIDC(testOIDCHandler())

	req := httptest.NewRequest(http.MethodGet, "/login?error=id+token+rejected", nil)
	w := httptest.NewRecorder()
	h.LoginPage(w, req)

	if !strings.Contains(renderContent(t, r), "id token rejected") {
		t.Error("the OIDC error banner must surface on the login page")
	}
}

func TestRegisterRoutes_OIDCWiring(t *testing.T) {
	store := newTestStore(t)
	h := New(store, auth.NewJWTManager("k"), &stubRenderer{}).WithOIDC(testOIDCHandler())
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Discovery against the unreachable issuer fails fast → bounce to login.
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "error=") {
		t.Errorf("/auth/oidc/login via mux = %d %q, want a 303 bounce to /login?error=", w.Code, w.Header().Get("Location"))
	}
}
