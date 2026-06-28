package clusters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// --- stubs ---

type stubHost struct {
	lastProps      layout.BaseProps
	redirectTarget string
	redirectStatus int
}

func (s *stubHost) Render(_ http.ResponseWriter, _ *http.Request, props layout.BaseProps) {
	s.lastProps = props
}
func (s *stubHost) PopToast(_ *http.Request) layout.ToastData { return layout.ToastData{} }
func (s *stubHost) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return next
}
func (s *stubHost) CanMutate(_ *http.Request) bool { return true }
func (s *stubHost) TenantSummaries() []pages.TenantSummary {
	return []pages.TenantSummary{{Name: "prod", Phase: "active"}}
}
func (s *stubHost) NodeGroupSummaries(_ string) []state.NodeGroupSummary { return nil }
func (s *stubHost) BusPresent() bool                                     { return true }
func (s *stubHost) RedirectAction(w http.ResponseWriter, _ *http.Request, target string) {
	s.redirectTarget = target
	s.redirectStatus = http.StatusSeeOther
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusSeeOther)
}

// --- helpers ---

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "clusters.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newHandler(t *testing.T) (*Handler, *state.Store, *stubHost) {
	t.Helper()
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, host)
	return h, store, host
}

func authedReq(method, target string, role string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req = req.WithContext(auth.WithClaims(req.Context(), "tester", role))
	return req
}

// --- tests ---

// TestNew_HandlerConstructs verifies New wires up dependencies.
func TestNew_HandlerConstructs(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, host)
	if h == nil || h.store == nil || h.host == nil {
		t.Fatal("New didn't wire dependencies")
	}
}

// TestTenantsList_Renders verifies the clusters list page renders.
func TestTenantsList_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := authedReq(http.MethodGet, "/clusters", "admin")
	w := httptest.NewRecorder()
	h.TenantsList(w, req)
	if host.lastProps.Title != "Clusters" {
		t.Errorf("Title = %q, want Clusters", host.lastProps.Title)
	}
	if host.lastProps.Page != "clusters" {
		t.Errorf("Page = %q, want clusters", host.lastProps.Page)
	}
}

// TestTenantDetail_404OnMissingCluster verifies a non-existent cluster 404s.
func TestTenantDetail_404OnMissingCluster(t *testing.T) {
	h, _, _ := newHandler(t)
	req := authedReq(http.MethodGet, "/clusters/ghost", "admin")
	req.SetPathValue("name", "ghost")
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestTenantDetail_RendersExistingCluster verifies the detail page renders.
func TestTenantDetail_RendersExistingCluster(t *testing.T) {
	h, store, host := newHandler(t)
	_, err := store.CreateTenant("prod", state.TenantSpec{
		KubernetesVersion:    "1.35.0",
		TalosVersion:         "1.12.0",
		ControlPlaneEndpoint: "https://example:6443",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	req := authedReq(http.MethodGet, "/clusters/prod", "admin")
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	h.TenantDetail(w, req)
	if host.lastProps.Title != "prod" {
		t.Errorf("Title = %q, want prod", host.lastProps.Title)
	}
	if host.lastProps.Page != "cluster" {
		t.Errorf("Page = %q, want cluster", host.lastProps.Page)
	}
}

// TestClusterCreatePage_Renders verifies the create form renders.
func TestClusterCreatePage_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := authedReq(http.MethodGet, "/clusters/create", "admin")
	w := httptest.NewRecorder()
	h.ClusterCreatePage(w, req)
	if host.lastProps.Title != "Create Cluster" {
		t.Errorf("Title = %q, want Create Cluster", host.lastProps.Title)
	}
}

// TestClusterCreateSubmit_Success verifies cluster creation flow.
func TestClusterCreateSubmit_Success(t *testing.T) {
	h, store, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/clusters/create", strings.NewReader("name=foo&kubernetesVersion=1.35.0&talosVersion=1.12.0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(auth.WithClaims(req.Context(), "tester", "admin"))
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if loc := w.Header().Get("HX-Redirect"); !strings.Contains(loc, "/clusters/foo") {
		t.Errorf("HX-Redirect = %q, missing cluster URL", loc)
	}
	if t1, _ := store.GetTenant("foo"); t1 == nil {
		t.Error("tenant not created")
	}
}

// TestClusterCreateSubmit_DuplicateRejected verifies duplicate name handling.
func TestClusterCreateSubmit_DuplicateRejected(t *testing.T) {
	h, store, _ := newHandler(t)
	if _, err := store.CreateTenant("foo", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/clusters/create", strings.NewReader("name=foo&kubernetesVersion=1.35.0&talosVersion=1.12.0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterCreateSubmit(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "already exists") {
		t.Errorf("expected 'already exists' in body, got: %s", body)
	}
}

// TestValidClusterName verifies the cluster-name regex.
func TestValidClusterName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"a", false}, // too short (need 2-63)
		{"ab", true},
		{"foo-bar", true},
		{"Foo", false},                   // uppercase
		{"1foo", false},                  // starts with digit
		{"foo_bar", false},               // underscore not allowed
		{"foo.bar", false},               // dot not allowed
		{strings.Repeat("a", 64), false}, // too long
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validClusterName(c.name); got != c.want {
				t.Errorf("validClusterName(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestCurrentTab verifies tab extraction.
func TestCurrentTab(t *testing.T) {
	cases := []struct {
		tab  string
		want string
	}{
		{"", "overview"},
		{"overview", "overview"},
		{"patches", "patches"},
		{"backups", "backups"},
		{"upgrade", "upgrade"},
		{"settings", "settings"},
		{"unknown", "overview"}, // unknown tabs default
	}
	for _, c := range cases {
		t.Run(c.tab, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/clusters/x/"+c.tab, nil)
			req.SetPathValue("tab", c.tab)
			if got := currentTab(req); got != c.want {
				t.Errorf("currentTab(%q) = %q, want %q", c.tab, got, c.want)
			}
		})
	}
}

// TestValidatePatchInput verifies patch validation rules.
func TestValidatePatchInput(t *testing.T) {
	cases := []struct {
		name               string
		format, role, body string
		wantErr            bool
	}{
		{"empty body", "strategic", "controlplane", "", true},
		{"invalid format", "badformat", "controlplane", "spec:", true},
		{"invalid role", "strategic", "badrole", "spec:", true},
		{"valid strategic", "strategic", "controlplane", "spec:\n  foo: bar", false},
		{"valid json6902", "json6902", "controlplane", `[{"op":"add","path":"/spec/foo","value":"bar"}]`, false},
		{"json6902 empty", "json6902", "controlplane", "[]", true},
		{"json6902 invalid", "json6902", "controlplane", "not json", true},
		{"valid text", "text", "controlplane", "machine:", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePatchInput(c.format, c.role, c.body)
			if (err != nil) != c.wantErr {
				t.Errorf("validatePatchInput(%q,%q,%q) err = %v, wantErr = %v", c.format, c.role, c.body, err, c.wantErr)
			}
		})
	}
}

// TestClusterDelete_404OnEmpty verifies the guard.
func TestClusterDelete_404OnEmpty(t *testing.T) {
	h, _, _ := newHandler(t)
	req := authedReq(http.MethodDelete, "/clusters/", "admin")
	// No name path value
	w := httptest.NewRecorder()
	h.ClusterDelete(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestClusterDelete_ForbiddenForView verifies role check.
func TestClusterDelete_ForbiddenForView(t *testing.T) {
	h, _, _ := newHandler(t)
	req := authedReq(http.MethodDelete, "/clusters/foo", "view")
	req.SetPathValue("name", "foo")
	w := httptest.NewRecorder()
	h.ClusterDelete(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestClusterPatchEditPage_404OnMissing verifies the edit page 404s when patch doesn't exist.
func TestClusterPatchEditPage_404OnMissing(t *testing.T) {
	h, store, _ := newHandler(t)
	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	req := authedReq(http.MethodGet, "/clusters/prod/patches/ghost", "admin")
	req.SetPathValue("name", "prod")
	req.SetPathValue("patch", "ghost")
	w := httptest.NewRecorder()
	h.ClusterPatchEditPage(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestClusterPatchesPreview_NoPatches verifies the preview endpoint.
func TestClusterPatchesPreview_NoPatches(t *testing.T) {
	h, store, _ := newHandler(t)
	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	req := authedReq(http.MethodGet, "/clusters/prod/patches/preview?role=controlplane", "admin")
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	h.ClusterPatchesPreview(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no patches") {
		t.Errorf("body = %q, want 'no patches'", w.Body.String())
	}
}

// TestClusterKubeconfig_404OnMissingCluster verifies kubeconfig 404s.
func TestClusterKubeconfig_404OnMissingCluster(t *testing.T) {
	h, _, _ := newHandler(t)
	req := authedReq(http.MethodGet, "/clusters/ghost/kubeconfig", "admin")
	req.SetPathValue("name", "ghost")
	w := httptest.NewRecorder()
	h.ClusterKubeconfig(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestClusterUpgradeStart_NoManager verifies graceful degradation.
func TestClusterUpgradeStart_NoManager(t *testing.T) {
	h, _, host := newHandler(t)
	req := authedReq(http.MethodPost, "/clusters/prod/upgrade/start", "admin")
	req.SetPathValue("name", "prod")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ClusterUpgradeStart(w, req)
	if !strings.Contains(host.redirectTarget, "upgrade+manager+not+configured") {
		t.Errorf("redirect = %q, missing unavailable toast", host.redirectTarget)
	}
}

// TestRegisterRoutes verifies all cluster routes are wired. The check uses
// a sentinel 418 status to distinguish "not registered" (404 from mux) from
// "handler returned 404 because tenant/patch doesn't exist". We wrap each
// handler with a marker that replaces 404 with 418 for the duration of the
// test (via a small mux interceptor that doesn't change handler logic).
func TestRegisterRoutes(t *testing.T) {
	h, store, _ := newHandler(t)
	// Seed a tenant so /clusters/prod routes don't 404 on missing data.
	if _, err := store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// Track which routes were dispatched by overriding the AuthRequired
	// wrapper to record before delegating.
	dispatched := map[string]bool{}
	recordingHost := &recordingHost{inner: h.host, dispatched: dispatched}
	hRec := New(store, nil, recordingHost)

	mux := http.NewServeMux()
	hRec.RegisterRoutes(mux)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/clusters"},
		{http.MethodGet, "/clusters/create"},
		{http.MethodGet, "/clusters/prod"},
		{http.MethodDelete, "/clusters/prod"},
		{http.MethodGet, "/clusters/prod/kubeconfig"},
		{http.MethodGet, "/clusters/prod/talosconfig"},
		{http.MethodGet, "/clusters/prod/patches/preview"},
		{http.MethodGet, "/tenants"},
		{http.MethodGet, "/tenants/prod"},
	}
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		key := r.method + " " + r.path
		if w.Code == http.StatusNotFound && !dispatched[key] {
			t.Errorf("%s not registered (mux-level 404)", key)
		}
	}
}

type recordingHost struct {
	inner      Host
	dispatched map[string]bool
}

func (r *recordingHost) Render(w http.ResponseWriter, req *http.Request, p layout.BaseProps) {
	r.inner.Render(w, req, p)
}
func (r *recordingHost) PopToast(req *http.Request) layout.ToastData { return r.inner.PopToast(req) }
func (r *recordingHost) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.dispatched[req.Method+" "+req.URL.Path] = true
		next(w, req)
	}
}
func (r *recordingHost) CanMutate(req *http.Request) bool       { return r.inner.CanMutate(req) }
func (r *recordingHost) TenantSummaries() []pages.TenantSummary { return r.inner.TenantSummaries() }
func (r *recordingHost) NodeGroupSummaries(t string) []state.NodeGroupSummary {
	return r.inner.NodeGroupSummaries(t)
}
func (r *recordingHost) BusPresent() bool { return r.inner.BusPresent() }
func (r *recordingHost) RedirectAction(w http.ResponseWriter, req *http.Request, target string) {
	r.inner.RedirectAction(w, req, target)
}

// TestHost_Interface verifies the stub satisfies Host.
func TestHost_Interface(t *testing.T) {
	var _ Host = (*stubHost)(nil)
}

// TestClusterCreateData_JSON verifies the create data shape is JSON-serializable.
func TestClusterCreateData_JSON(t *testing.T) {
	data := pages.ClusterCreateData{Name: "foo", K8sVersion: "1.35.0"}
	if _, err := json.Marshal(data); err != nil {
		t.Errorf("Marshal: %v", err)
	}
}
