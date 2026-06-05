// Package integration — WebUI tests exercise the full WebUI stack against a
// real HTTP server backed by a real SQLite database. Auth flow + live SSE
// + correct phase/stage rendering.
package integration

import (
	"bufio"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/watchbus"
	"github.com/rezuscloud/rezuscloud/internal/web"
)

// newTestJar returns an in-memory cookie jar for tests that follow redirects.
func newTestJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}

// webuiServer is a full WebUI stack: store + watch bus + handler + HTTP server.
type webuiServer struct {
	store   *state.Store
	bus     *watch.Bus
	handler *web.Handler
	server  *httptest.Server
}

func newWebUIServer(t *testing.T) *webuiServer {
	t.Helper()

	path := filepath.Join(t.TempDir(), "webui.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bus := watch.NewBus()
	store.SetBus(watchbus.New(bus))

	jwtManager := auth.NewJWTManager("webui-test-secret")
	handler := web.NewHandler(store, jwtManager, bus)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &webuiServer{
		store:   store,
		bus:     bus,
		handler: handler,
		server:  server,
	}
}

// createUserWithRole creates a user in the store with the given role and a known password.
func (s *webuiServer) createUser(t *testing.T, username, password, role string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, err = s.store.CreateUser(username, state.UserSpec{
		Role:         role,
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// login posts the login form and returns the session cookie on success.
// Uses a cookie jar because PostForm follows redirects by default and the
// cookie returned by PostForm is from the *final* response, not the login.
func (s *webuiServer) login(t *testing.T, username, password string) *http.Cookie {
	t.Helper()
	client := &http.Client{
		Jar:       newTestJar(),
		Transport: s.server.Client().Transport,
	}

	form := url.Values{
		"username": {username},
		"password": {password},
	}
	resp, err := client.PostForm(s.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The cookie was set on the login response (303). Pull it from the jar.
	u, _ := url.Parse(s.server.URL)
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == "rezuscloud_session" {
			return c
		}
	}

	// If not in jar, check the final response (status should be 303 → / → 303 → /login).
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status = %d, want 303; body: %s", resp.StatusCode, body)
	}
	t.Fatal("login did not set rezuscloud_session cookie")
	return nil
}

// getWithCookie fetches a path with the given session cookie and returns status + body.
// followRedirects=false allows tests to verify redirect responses (303).
func (s *webuiServer) getWithCookie(t *testing.T, path string, cookie *http.Cookie) (int, string) {
	t.Helper()
	return s.doRequest(t, http.MethodGet, path, cookie, false)
}

// getNoRedirect is like getWithCookie but returns the immediate response (does not follow redirects).
func (s *webuiServer) getNoRedirect(t *testing.T, path string, cookie *http.Cookie) (int, string) {
	t.Helper()
	return s.doRequest(t, http.MethodGet, path, cookie, true)
}

// doRequest executes a GET with optional redirect control.
func (s *webuiServer) doRequest(t *testing.T, method, path string, cookie *http.Cookie, noRedirect bool) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.server.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := s.server.Client()
	if noRedirect {
		client = &http.Client{
			Transport: s.server.Client().Transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestWebUI_UnauthenticatedDashboard_RedirectsToLogin(t *testing.T) {
	s := newWebUIServer(t)
	status, _ := s.getNoRedirect(t, "/", nil)
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
}

func TestWebUI_Login_RedirectsWithCookie(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")
	if cookie == nil {
		t.Fatal("cookie should be set")
	}
	if cookie.Value == "" || cookie.Value == "placeholder" {
		t.Errorf("cookie value = %q, expected real JWT", cookie.Value)
	}

	// HttpOnly flag is checked separately: cookies retrieved from a jar lose
	// that flag, so we verify it via the raw response header.
	form := url.Values{
		"username": {"alice"},
		"password": {"secret"},
	}
	client := &http.Client{
		Transport: s.server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.PostForm(s.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw := resp.Header.Get("Set-Cookie")
	if !strings.Contains(raw, "HttpOnly") {
		t.Errorf("Set-Cookie header missing HttpOnly: %s", raw)
	}
}

func TestWebUI_LoginBadPassword_DoesNotSetCookie(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)

	form := url.Values{
		"username": {"alice"},
		"password": {"wrong"},
	}
	client := &http.Client{
		Transport: s.server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.PostForm(s.server.URL+"/login", form)
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Invalid username or password") {
		t.Errorf("body should contain error; got: %s", body)
	}
}

func TestWebUI_AuthedDashboard_Renders(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	status, body := s.getWithCookie(t, "/", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}
	if !strings.Contains(body, "Overview") {
		t.Error("body should contain 'Overview' page title")
	}
	if !strings.Contains(body, "alice") {
		t.Error("body should contain the username in sidebar")
	}
}

func TestWebUI_TenantDetail_RealMachineStage(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	// Create tenant.
	_, err := s.store.CreateTenant("prod", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// Create machine with stage "ready".
	_, err = s.store.CreateMachine("machine-uuid", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod", "rezuscloud.io/role": "worker"}, nil)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	_, _ = s.store.UpdateMachineStatus("machine-uuid", state.MachineStatus{
		Stage: state.StageReady,
		Ready: true,
		Role:  "worker",
	})

	status, body := s.getWithCookie(t, "/tenants/prod", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "ready") {
		t.Errorf("body should contain 'ready' stage; got: %s", body)
	}
}

func TestWebUI_Dashboard_RealPhase(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	// Empty tenant (no machines, no node groups).
	_, err := s.store.CreateTenant("prod", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	status, body := s.getWithCookie(t, "/", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "forming") {
		t.Errorf("empty tenant should be in 'forming' phase; got: %s", body)
	}
}

// TestWebUI_EventsStream verifies the SSE endpoint publishes events when the
// store is mutated. Uses a real HTTP server and SSE parser.
func TestWebUI_EventsStream(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	req, err := http.NewRequest(http.MethodGet, s.server.URL+"/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(cookie)

	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatalf("get events stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Mutate the store in a goroutine — events should arrive on the SSE stream.
	received := make(chan string, 5)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				received <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
	}()

	// Trigger a store mutation → bus → SSE.
	_, err = s.store.CreateTenant("live-test", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	data := <-received
	if !strings.Contains(data, "live-test") {
		t.Errorf("event data = %q, expected to contain 'live-test'", data)
	}
	if !strings.Contains(data, "ADDED") {
		t.Errorf("event data = %q, expected to contain 'ADDED'", data)
	}
}

// --- W2 Navigation shell tests ---

func TestWebUI_ClustersAlias_WorksWithCookie(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	// Create a tenant directly in the store so the page has content.
	_, _ = s.store.CreateTenant("alpha", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	status, body := s.getWithCookie(t, "/clusters", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "alpha") {
		t.Errorf("body should list tenant alpha")
	}
}

func TestWebUI_ClusterDetail_BreadcrumbWorks(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	_, _ = s.store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	status, body := s.getWithCookie(t, "/clusters/prod", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	// Should have breadcrumb with Home / Clusters / prod
	if !strings.Contains(body, "ds-breadcrumb") {
		t.Error("body should contain breadcrumb")
	}
	if !strings.Contains(body, `href="/clusters"`) {
		t.Error("breadcrumb should link to /clusters")
	}
}

func TestWebUI_ToastFlashMessageRenders(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	status, body := s.getWithCookie(t, "/?toast=hello&toast-type=success", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "hello") {
		t.Errorf("body should contain toast message")
	}
	if !strings.Contains(body, "ds-toast--success") {
		t.Errorf("body should have ds-toast--success class")
	}
}

func TestWebUI_Sidebar_HasAllNavEntries(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "alice", "secret", auth.RoleAdmin)
	cookie := s.login(t, "alice", "secret")

	status, body := s.getWithCookie(t, "/", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	expectedLinks := []string{
		`href="/"`, `href="/clusters"`, `href="/machines"`,
		`href="/machines/jointokens"`, `href="/providers"`,
		`href="/settings/users"`, `href="/settings/api-tokens"`,
		`href="/settings/audit"`, `href="/settings/backups"`,
	}
	for _, link := range expectedLinks {
		if !strings.Contains(body, link) {
			t.Errorf("body should contain sidebar link %q", link)
		}
	}
}
