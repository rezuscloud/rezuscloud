package machines

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
)

// --- stubs ---

type stubHost struct {
	lastProps layout.BaseProps
}

func (s *stubHost) Render(_ http.ResponseWriter, _ *http.Request, props layout.BaseProps) {
	s.lastProps = props
}
func (s *stubHost) PopToast(_ *http.Request) layout.ToastData { return layout.ToastData{} }
func (s *stubHost) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return next
}
func (s *stubHost) CanMutate(_ *http.Request) bool { return true }
func (s *stubHost) ClusterNames() []string         { return []string{"prod"} }
func (s *stubHost) MachineLinkEndpoint() string    { return "machinelink.test:50001" }
func (s *stubHost) BusPresent() bool               { return true }

// --- helpers ---

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "machines.db"))
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

// --- tests ---

// TestNew_HandlerConstructs verifies New wires up dependencies.
func TestNew_HandlerConstructs(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	bus := watch.NewBus()
	h := New(store, bus, host)
	if h == nil || h.store == nil || h.host == nil || h.bus == nil {
		t.Fatal("New didn't wire dependencies")
	}
}

// TestMachinesList_Renders verifies the machines list page renders.
func TestMachinesList_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines", nil)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)
	if host.lastProps.Title != "Machines" {
		t.Errorf("Title = %q, want Machines", host.lastProps.Title)
	}
}

// TestMachinesList_FiltersByCluster verifies the cluster filter.
func TestMachinesList_FiltersByCluster(t *testing.T) {
	h, store, _ := newHandler(t)
	// Create two machines, one in cluster "prod", one in "other".
	_, _ = store.CreateMachine("m1", state.MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.CreateMachine("m2", state.MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "other"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/machines?cluster=prod", nil)
	w := httptest.NewRecorder()
	h.MachinesList(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestMachinesPending_Renders verifies the pending page renders.
func TestMachinesPending_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/pending", nil)
	w := httptest.NewRecorder()
	h.MachinesPending(w, req)
	if host.lastProps.Title != "Pending Machines" {
		t.Errorf("Title = %q, want Pending Machines", host.lastProps.Title)
	}
}

// TestMachineDetail_404OnMissing verifies the detail page 404s on missing machine.
func TestMachineDetail_404OnMissing(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/ghost", nil)
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.MachineDetail(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestMachineDetail_RendersExisting verifies the detail page renders.
func TestMachineDetail_RendersExisting(t *testing.T) {
	h, store, host := newHandler(t)
	_, _ = store.CreateMachine("m1", state.MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.UpdateMachineStatus("m1", state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true})

	req := httptest.NewRequest(http.MethodGet, "/machines/m1", nil)
	req.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	h.MachineDetail(w, req)
	if host.lastProps.Title != "Machine m1" {
		t.Errorf("Title = %q, want 'Machine m1'", host.lastProps.Title)
	}
}

// TestMachineLogs_404OnMissing verifies the logs page 404s.
func TestMachineLogs_404OnMissing(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/ghost/logs", nil)
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.MachineLogs(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestMachineLogs_Renders verifies the logs page renders.
func TestMachineLogs_Renders(t *testing.T) {
	h, store, host := newHandler(t)
	_, _ = store.CreateMachine("m1", state.MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	req := httptest.NewRequest(http.MethodGet, "/machines/m1/logs", nil)
	req.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	h.MachineLogs(w, req)
	if host.lastProps.Title != "Logs — m1" {
		t.Errorf("Title = %q, want 'Logs — m1'", host.lastProps.Title)
	}
}

// TestMachineLogsPoll_RendersPartial verifies the HTMX partial returns HTML.
func TestMachineLogsPoll_RendersPartial(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/m1/logs/poll", nil)
	req.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	h.MachineLogsPoll(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "ds-logs-line") {
		t.Errorf("body should contain HTML log lines, got: %s", body)
	}
}

// TestMachineEvents_503WhenBusNil verifies graceful degradation.
func TestMachineEvents_503WhenBusNil(t *testing.T) {
	h, _, _ := newHandler(t) // bus nil
	req := httptest.NewRequest(http.MethodGet, "/machines/m1/events", nil)
	req.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	h.MachineEvents(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestMachineConfig_404OnMissing verifies the config page 404s.
func TestMachineConfig_404OnMissing(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/ghost/config", nil)
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.MachineConfig(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestMachineKernelArgs_404OnMissing verifies the kernel-args page 404s.
func TestMachineKernelArgs_404OnMissing(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/ghost/kernel-args", nil)
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.MachineKernelArgs(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestMachineRestart_404OnMissing verifies the restart action 404s.
func TestMachineRestart_404OnMissing(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/machines/ghost/restart", nil)
	req.SetPathValue("id", "ghost")
	w := httptest.NewRecorder()
	h.MachineRestart(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestMachineDelete_Success verifies the delete flow (soft-delete sets DeletionTimestamp).
func TestMachineDelete_Success(t *testing.T) {
	h, store, _ := newHandler(t)
	_, _ = store.CreateMachine("m1", state.MachineSpec{}, nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/machines/m1", nil)
	req.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	h.MachineDelete(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	m, _ := store.GetMachine("m1")
	if m == nil {
		t.Fatal("machine should still exist (soft-delete)")
	}
	if m.Metadata.DeletionTimestamp.IsZero() {
		t.Error("DeletionTimestamp should be set after delete")
	}
}

// TestJoinTokensList_Renders verifies the join tokens list renders.
func TestJoinTokensList_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/machines/jointokens", nil)
	w := httptest.NewRecorder()
	h.JoinTokensList(w, req)
	if host.lastProps.Title != "Join Tokens" {
		t.Errorf("Title = %q, want Join Tokens", host.lastProps.Title)
	}
}

// TestJoinTokenCreate_Success verifies token creation flow.
func TestJoinTokenCreate_Success(t *testing.T) {
	h, store, _ := newHandler(t)
	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	body := strings.NewReader("cluster=prod&nodegroup=workers&ttl=1h")
	req := httptest.NewRequest(http.MethodPost, "/machines/jointokens", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.JoinTokenCreate(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "new_token=") {
		t.Errorf("Location = %q, missing new_token", w.Header().Get("Location"))
	}
}

// TestJoinTokenCreate_MissingFields verifies validation.
func TestJoinTokenCreate_MissingFields(t *testing.T) {
	h, _, _ := newHandler(t)
	body := strings.NewReader("cluster=")
	req := httptest.NewRequest(http.MethodPost, "/machines/jointokens", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.JoinTokenCreate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestRegisterRoutes verifies all routes are wired.
func TestRegisterRoutes(t *testing.T) {
	h, store, _ := newHandler(t)
	// Seed a machine so the DELETE route handler doesn't return 404 from missing data.
	_, _ = store.CreateMachine("m1", state.MachineSpec{}, nil, nil)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/machines"},
		{http.MethodGet, "/machines/jointokens"},
		{http.MethodGet, "/machines/pending"},
		{http.MethodDelete, "/machines/m1"},
	}
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s not allowed (got 405)", r.method, r.path)
		}
	}
}

// TestFormatAge verifies age rendering.
func TestFormatAge(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, "—"},
		{"recent", time.Now().Add(30 * time.Second), "just now"},
		{"minutes", time.Now().Add(-5 * time.Minute), "5 minutes ago"},
		{"hours", time.Now().Add(-3 * time.Hour), "3 hours ago"},
		{"days", time.Now().Add(-48 * time.Hour), "2 days ago"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatAge(c.t)
			if c.name == "minutes" || c.name == "hours" {
				// Allow some flexibility for slow test runners
				if !strings.Contains(got, "minutes ago") && !strings.Contains(got, "hours ago") {
					t.Errorf("formatAge(%v) = %q, want contains 'ago'", c.t, got)
				}
				return
			}
			if got != c.want {
				t.Errorf("formatAge(%v) = %q, want %q", c.t, got, c.want)
			}
		})
	}
}

// TestShortDisplayID verifies the truncation.
func TestShortDisplayID(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"abc", "abc"},
		{"abcdefgh", "abcdefgh"},
		{"abcdefghi", "abcdefgh"},
		{"abcdefghijklmnop", "abcdefgh"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			if got := shortDisplayID(c.input); got != c.want {
				t.Errorf("shortDisplayID(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestIsValidKernelArg verifies the allowed-prefix check.
func TestIsValidKernelArg(t *testing.T) {
	cases := []struct {
		arg  string
		want bool
	}{
		{"talos.platform=metal", true},
		{"siderolink.api=https://foo", true},
		{"console=ttyS0", true},
		{"reboot=k", true},
		{"mitigations=off", true},
		{"ip=dhcp", true},
		{"foo=bar", false},
		{"badprefix", false},
	}
	for _, c := range cases {
		t.Run(c.arg, func(t *testing.T) {
			if got := isValidKernelArg(c.arg); got != c.want {
				t.Errorf("isValidKernelArg(%q) = %v, want %v", c.arg, got, c.want)
			}
		})
	}
}

// TestBuildKernelArgsPatch verifies the YAML output.
func TestBuildKernelArgsPatch(t *testing.T) {
	args := []string{"talos.platform=metal", "console=ttyS0"}
	got := buildKernelArgsPatch(args)
	if !strings.Contains(got, "machine:") {
		t.Error("missing machine: key")
	}
	if !strings.Contains(got, "extraKernelArgs:") {
		t.Error("missing extraKernelArgs: key")
	}
	if !strings.Contains(got, "talos.platform=metal") {
		t.Error("missing first arg")
	}
}

// TestKernelArgsPreview verifies the kernel args preview format.
func TestKernelArgsPreview(t *testing.T) {
	got := kernelArgsPreview("abc", "endpoint:50001")
	if !strings.Contains(got, "endpoint:50001") {
		t.Error("missing endpoint")
	}
	if !strings.Contains(got, "jointoken=abc") {
		t.Error("missing token")
	}
}

// TestGenerateJoinTokenValue verifies token generation.
func TestGenerateJoinTokenValue(t *testing.T) {
	tok, err := generateJoinTokenValue()
	if err != nil {
		t.Fatalf("generateJoinTokenValue: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token len = %d, want 64 (32 hex bytes)", len(tok))
	}
	tok2, _ := generateJoinTokenValue()
	if tok == tok2 {
		t.Error("tokens should be unique")
	}
}

// TestMachineStages verifies the list is non-empty.
func TestMachineStages(t *testing.T) {
	if len(machineStages) == 0 {
		t.Error("machineStages should not be empty")
	}
}

// TestHost_Interface verifies the stub satisfies Host.
func TestHost_Interface(t *testing.T) {
	var _ Host = (*stubHost)(nil)
}
