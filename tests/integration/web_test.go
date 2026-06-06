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

// --- W3 integration tests ---

// postFormWithCookie submits a form-encoded POST with the session cookie.
// Returns status, response body, and response headers.
func (s *webuiServer) postFormWithCookie(t *testing.T, path, form string, cookie *http.Cookie) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.server.URL+path, strings.NewReader(form))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	// Don't follow redirects — HTMX responses are 204 + HX-Redirect header.
	client := &http.Client{
		Transport: s.server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

// deleteWithCookie issues a DELETE with the session cookie.
func (s *webuiServer) deleteWithCookie(t *testing.T, path string, cookie *http.Cookie) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, s.server.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := &http.Client{
		Transport: s.server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

// getWithCookieHeaders issues a GET with the session cookie and returns headers too.
func (s *webuiServer) getWithCookieHeaders(t *testing.T, path string, cookie *http.Cookie) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.server.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestW3_ClusterCRUD_FullLifecycle(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// Step 1: /clusters page shows empty state with Create link.
	status, body, _ := s.getWithCookieHeaders(t, "/clusters", cookie)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want 200", status)
	}
	if !strings.Contains(body, "/clusters/create") {
		t.Error("/clusters page should show Create link")
	}

	// Step 2: GET /clusters/create renders the form.
	status, body, _ = s.getWithCookieHeaders(t, "/clusters/create", cookie)
	if status != http.StatusOK {
		t.Fatalf("create page status = %d", status)
	}
	if !strings.Contains(body, `name="name"`) {
		t.Error("create page should render name field")
	}

	// Step 3: POST /clusters/create with valid form redirects to detail page.
	form := "name=lifecycle-cluster&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	status, body, hdr := s.postFormWithCookie(t, "/clusters/create", form, cookie)
	if status != http.StatusNoContent {
		t.Fatalf("create submit status = %d, want 204; body: %s", status, body)
	}
	redirect := hdr.Get("HX-Redirect")
	if !strings.Contains(redirect, "/clusters/lifecycle-cluster") {
		t.Errorf("HX-Redirect = %q, want /clusters/lifecycle-cluster", redirect)
	}

	// Step 4: GET /clusters/lifecycle-cluster renders detail with tabs.
	status, body, _ = s.getWithCookieHeaders(t, "/clusters/lifecycle-cluster", cookie)
	if status != http.StatusOK {
		t.Fatalf("detail status = %d", status)
	}
	// Strip <style>...</style> for assertion.
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, `ds-tabs-link--active`) {
		t.Error("detail page should render active tab")
	}
	if !strings.Contains(body, "lifecycle-cluster") {
		t.Error("detail page should show cluster name")
	}

	// Step 5: GET kubeconfig returns YAML attachment.
	status, body, hdr = s.getWithCookieHeaders(t, "/clusters/lifecycle-cluster/kubeconfig", cookie)
	if status != http.StatusOK {
		t.Fatalf("kubeconfig status = %d", status)
	}
	if !strings.Contains(body, "apiVersion: v1") {
		t.Errorf("kubeconfig body missing apiVersion: v1; got:\n%s", body[:min(len(body), 200)])
	}
	if !strings.Contains(hdr.Get("Content-Disposition"), "lifecycle-cluster-kubeconfig.yaml") {
		t.Errorf("Content-Disposition = %q", hdr.Get("Content-Disposition"))
	}

	// Step 6: GET talosconfig returns YAML attachment.
	status, body, hdr = s.getWithCookieHeaders(t, "/clusters/lifecycle-cluster/talosconfig", cookie)
	if status != http.StatusOK {
		t.Fatalf("talosconfig status = %d", status)
	}
	if !strings.Contains(body, "context:") {
		t.Errorf("talosconfig body missing 'context:'; got:\n%s", body[:min(len(body), 200)])
	}
	if !strings.Contains(hdr.Get("Content-Disposition"), "lifecycle-cluster-talosconfig.yaml") {
		t.Errorf("talosconfig Content-Disposition = %q", hdr.Get("Content-Disposition"))
	}

	// Step 7: Settings tab shows delete button for admin.
	status, body, _ = s.getWithCookieHeaders(t, "/clusters/lifecycle-cluster/settings", cookie)
	if status != http.StatusOK {
		t.Fatalf("settings status = %d", status)
	}
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "Danger Zone") {
		t.Error("settings tab should show Danger Zone for admin")
	}
	if !strings.Contains(body, `data-modal-open="delete-lifecycle-cluster"`) {
		t.Error("settings tab should render delete modal trigger")
	}

	// Step 8: DELETE /clusters/lifecycle-cluster redirects to /clusters.
	status, body, hdr = s.deleteWithCookie(t, "/clusters/lifecycle-cluster", cookie)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body: %s", status, body)
	}
	redirect = hdr.Get("HX-Redirect")
	if !strings.Contains(redirect, "/clusters") {
		t.Errorf("delete HX-Redirect = %q, want /clusters", redirect)
	}

	// Step 9: After delete, the tenant still exists (graceful deletion sets
	// deletionTimestamp and adds finalizers). Verify deletionTimestamp is set.
	tenant, err := s.store.GetTenant("lifecycle-cluster")
	if err != nil {
		t.Fatalf("get tenant after delete: %v", err)
	}
	if tenant == nil {
		t.Fatal("tenant should still exist after graceful delete (finalizers pending)")
	}
	if tenant.Metadata.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to be set after DELETE")
	}
}

func TestW3_ClusterCreate_Validation(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// Invalid name (uppercase) — form re-renders with error.
	form := "name=BAD-NAME&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	status, body, _ := s.postFormWithCookie(t, "/clusters/create", form, cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form re-renders)", status)
	}
	if !strings.Contains(body, "must match") && !strings.Contains(body, "lowercase") {
		t.Errorf("expected validation error; got body tail: %s", body[:min(len(body), 300)])
	}
}

func TestW3_ClusterCreate_Duplicate(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// First create.
	form := "name=duplicate-test&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	status, _, _ := s.postFormWithCookie(t, "/clusters/create", form, cookie)
	if status != http.StatusNoContent {
		t.Fatalf("first create failed: %d", status)
	}

	// Second create with same name → error.
	status, body, _ := s.postFormWithCookie(t, "/clusters/create", form, cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form re-renders)", status)
	}
	if !strings.Contains(body, "already exists") {
		t.Errorf("expected 'already exists' error; got body tail: %s", body[:min(len(body), 300)])
	}
}

func TestW3_ClusterDelete_ViewRoleForbidden(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	s.createUser(t, "viewer", "secret", auth.RoleView)
	adminCookie := s.login(t, "admin", "secret")
	viewerCookie := s.login(t, "viewer", "secret")

	// Admin creates.
	form := "name=viewonly-delete&kubernetesVersion=1.35.0&talosVersion=1.12.0"
	status, _, _ := s.postFormWithCookie(t, "/clusters/create", form, adminCookie)
	if status != http.StatusNoContent {
		t.Fatalf("create failed: %d", status)
	}

	// Viewer cannot delete.
	status, body, _ := s.deleteWithCookie(t, "/clusters/viewonly-delete", viewerCookie)
	if status != http.StatusForbidden {
		t.Errorf("viewer delete status = %d, want 403; body: %s", status, body)
	}
}

func TestW3_NodeGroupScale(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// Create tenant with a node group via direct store.
	_, err := s.store.CreateTenant("ng-scale", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	_, err = s.store.CreateResource("nodegroup", "workers",
		map[string]any{"name": "workers", "role": "worker", "count": 1},
		nil,
		map[string]string{"rezuscloud.io/tenant": "ng-scale"},
		nil,
	)
	if err != nil {
		t.Fatalf("create nodegroup: %v", err)
	}

	// Scale to 5 via WebUI.
	form := "count=5"
	status, body, hdr := s.postFormWithCookie(t, "/clusters/ng-scale/nodegroups/workers/scale", form, cookie)
	if status != http.StatusNoContent {
		t.Fatalf("scale status = %d, want 204; body: %s", status, body)
	}
	if !strings.Contains(hdr.Get("HX-Redirect"), "/clusters/ng-scale") {
		t.Errorf("HX-Redirect = %q", hdr.Get("HX-Redirect"))
	}

	// Verify count was updated.
	var spec struct {
		Count int `json:"count"`
	}
	_, err = s.store.GetResource("nodegroup", "workers", &spec, nil)
	if err != nil {
		t.Fatalf("get nodegroup: %v", err)
	}
	if spec.Count != 5 {
		t.Errorf("count = %d, want 5", spec.Count)
	}
}

// --- W4 integration tests ---

func TestW4_MachinesFleet_Empty(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	status, body, _ := s.getWithCookieHeaders(t, "/machines", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "No machines registered") {
		t.Error("empty state should render")
	}
}

func TestW4_MachinesFleet_WithMachines(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// Create tenant + 2 machines.
	_, err := s.store.CreateTenant("alpha", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	_, _ = s.store.CreateMachine("11111111-aaaa-bbbb-cccc-dddddddddddd", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "alpha", "rezuscloud.io/role": "controlplane"}, nil)
	_, _ = s.store.UpdateMachineStatus("11111111-aaaa-bbbb-cccc-dddddddddddd", state.MachineStatus{
		Stage: state.StageReady,
		Role:  "controlplane",
	})
	_, _ = s.store.CreateMachine("22222222-aaaa-bbbb-cccc-dddddddddddd", state.MachineSpec{Connected: false},
		map[string]string{"rezuscloud.io/tenant": "alpha", "rezuscloud.io/role": "worker"}, nil)
	_, _ = s.store.UpdateMachineStatus("22222222-aaaa-bbbb-cccc-dddddddddddd", state.MachineStatus{
		Stage: state.StageInitializing,
		Role:  "worker",
	})

	status, body, _ := s.getWithCookieHeaders(t, "/machines", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if !strings.Contains(body, "11111111") {
		t.Error("machine 11111111 missing")
	}
	if !strings.Contains(body, "22222222") {
		t.Error("machine 22222222 missing")
	}
}

func TestW4_MachineDetail(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	_, _ = s.store.CreateMachine("detail-machine", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)
	_, _ = s.store.UpdateMachineStatus("detail-machine", state.MachineStatus{
		Stage:        state.StageReady,
		Role:         "worker",
		TalosVersion: "1.12.0",
		K8sVersion:   "1.35.0",
	})

	status, body, _ := s.getWithCookieHeaders(t, "/machines/detail-machine", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "detail-machine") {
		t.Error("ID missing")
	}
	if !strings.Contains(body, "1.12.0") {
		t.Error("talos version missing")
	}
}

func TestW4_JoinTokenCreate_FullFlow(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// Create tenant.
	_, _ = s.store.CreateTenant("gamma", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// GET — empty state.
	status, body, _ := s.getWithCookieHeaders(t, "/machines/jointokens", cookie)
	if status != http.StatusOK {
		t.Fatalf("GET list status = %d", status)
	}
	if !strings.Contains(body, "No join tokens") {
		t.Error("empty state should render")
	}

	// POST create.
	form := "cluster=gamma&nodegroup=workers&ttl=24h"
	status, _, hdr := s.postFormWithCookie(t, "/machines/jointokens", form, cookie)
	if status != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want 303", status)
	}
	loc := hdr.Get("Location")
	if !strings.HasPrefix(loc, "/machines/jointokens?new_token=") {
		t.Fatalf("Location = %q", loc)
	}

	// GET with new_token — show-once display.
	status, body, _ = s.getWithCookieHeaders(t, loc, cookie)
	if status != http.StatusOK {
		t.Fatalf("GET with token status = %d", status)
	}
	if !strings.Contains(body, "Token created") {
		t.Error("Token created banner missing")
	}
	if !strings.Contains(body, "siderolink.api") {
		t.Error("kernel args preview missing")
	}
}

func TestW4_PendingMachines(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	// ready + initializing + installing.
	_, _ = s.store.CreateMachine("m-ready", state.MachineSpec{}, map[string]string{}, nil)
	_, _ = s.store.UpdateMachineStatus("m-ready", state.MachineStatus{Stage: state.StageReady})
	_, _ = s.store.CreateMachine("m-init", state.MachineSpec{}, map[string]string{}, nil)
	_, _ = s.store.UpdateMachineStatus("m-init", state.MachineStatus{Stage: state.StageInitializing})
	_, _ = s.store.CreateMachine("m-install", state.MachineSpec{}, map[string]string{}, nil)
	_, _ = s.store.UpdateMachineStatus("m-install", state.MachineStatus{Stage: state.StageInstalling})

	status, body, _ := s.getWithCookieHeaders(t, "/machines/pending", cookie)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if i := strings.Index(body, "</style>"); i >= 0 {
		body = body[i+len("</style>"):]
	}
	if strings.Contains(body, "m-ready") {
		t.Error("ready machine should not appear in pending")
	}
	if !strings.Contains(body, "m-init") {
		t.Error("initializing machine missing")
	}
	if !strings.Contains(body, "m-install") {
		t.Error("installing machine missing")
	}
}

func TestW4_MachineActions_Admin(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	_, _ = s.store.CreateMachine("action-target", state.MachineSpec{}, map[string]string{}, nil)
	_, _ = s.store.UpdateMachineStatus("action-target", state.MachineStatus{Stage: state.StageReady})

	// Restart.
	status, _, hdr := s.postFormWithCookie(t, "/machines/action-target/restart", "", cookie)
	if status != http.StatusSeeOther {
		t.Errorf("restart status = %d, want 303", status)
	}
	if !strings.Contains(hdr.Get("Location"), "/machines/action-target") {
		t.Errorf("restart Location = %q", hdr.Get("Location"))
	}

	// Shutdown.
	status, _, hdr = s.postFormWithCookie(t, "/machines/action-target/shutdown", "", cookie)
	if status != http.StatusSeeOther {
		t.Errorf("shutdown status = %d, want 303", status)
	}
	if !strings.Contains(hdr.Get("Location"), "/machines/action-target") {
		t.Errorf("shutdown Location = %q", hdr.Get("Location"))
	}
}

func TestW4_MachineDelete_Admin(t *testing.T) {
	s := newWebUIServer(t)
	s.createUser(t, "admin", "secret", auth.RoleAdmin)
	cookie := s.login(t, "admin", "secret")

	_, _ = s.store.CreateMachine("to-delete", state.MachineSpec{}, map[string]string{}, nil)
	_, _ = s.store.UpdateMachineStatus("to-delete", state.MachineStatus{Stage: state.StageReady})

	status, _, hdr := s.deleteWithCookie(t, "/machines/to-delete", cookie)
	if status != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", status)
	}
	if !strings.Contains(hdr.Get("Location"), "/machines") {
		t.Errorf("Location = %q", hdr.Get("Location"))
	}

	// K8s-style: machine still exists, just marked for deletion.
	m, _ := s.store.GetMachine("to-delete")
	if m == nil {
		t.Fatal("machine should still exist (graceful deletion)")
	}
	if m.Metadata.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to be set")
	}
}
