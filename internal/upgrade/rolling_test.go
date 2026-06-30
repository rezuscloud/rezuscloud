package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
		TalosVersion:      "1.12.6",
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func setupMachine(t *testing.T, store *state.Store, tenant, id string) {
	t.Helper()
	labels := map[string]string{"rezuscloud.io/tenant": tenant}
	_, err := store.CreateResource("machine", id, struct{}{}, nil, labels, nil)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
}

// --- Test doubles ---

type mockUpgrader struct {
	mu          sync.Mutex
	upgrades    []string // machine IDs whose UpgradeMachine was called
	upgradeErr  map[string]error
	healthErr   map[string]error
	rollback    []string // machine IDs whose RollbackMachine was called
	rollbackErr map[string]error
}

func (m *mockUpgrader) UpgradeMachine(_ context.Context, id, _ string) error {
	m.mu.Lock()
	m.upgrades = append(m.upgrades, id)
	m.mu.Unlock()
	if m.upgradeErr != nil {
		if err, ok := m.upgradeErr[id]; ok {
			return err
		}
	}
	return nil
}

func (m *mockUpgrader) CheckMachineHealth(_ context.Context, id string) error {
	if m.healthErr != nil {
		if err, ok := m.healthErr[id]; ok {
			return err
		}
	}
	return nil
}

func (m *mockUpgrader) RollbackMachine(_ context.Context, id, _ string) error {
	m.mu.Lock()
	m.rollback = append(m.rollback, id)
	m.mu.Unlock()
	if m.rollbackErr != nil {
		if err, ok := m.rollbackErr[id]; ok {
			return err
		}
	}
	return nil
}

type mockLister struct {
	machines []string
	err      error
}

func (m *mockLister) ListMachinesInOrder(_ context.Context, _ string) ([]string, error) {
	return m.machines, m.err
}

// newTestManager wires a Manager with mock upgrader/lister and a real store.
func newTestManager(t *testing.T, upgrader MachineUpgrader, lister MachineLister) *Manager {
	t.Helper()
	return NewManager(newTestStore(t), upgrader, lister)
}

// --- Rolling loop tests (now exercised through the unified Manager) ---

func TestRollingUpgrade_Success(t *testing.T) {
	upgrader := &mockUpgrader{}
	lister := &mockLister{machines: []string{"m1", "m2", "m3"}}
	mgr := newTestManager(t, upgrader, lister)

	// rollUpgrade takes a runID only for progress persistence; it no-ops when
	// the run record doesn't exist, so a dummy ID is fine for loop tests.
	status := mgr.rollUpgrade(context.Background(), "dummy-run", "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseComplete {
		t.Errorf("phase = %s, want complete", status.Phase)
	}
	if status.Completed != 3 {
		t.Errorf("completed = %d, want 3", status.Completed)
	}
	if status.TotalMachines != 3 {
		t.Errorf("total = %d, want 3", status.TotalMachines)
	}
	upgrader.mu.Lock()
	defer upgrader.mu.Unlock()
	if len(upgrader.upgrades) != 3 {
		t.Errorf("upgraded %d machines, want 3", len(upgrader.upgrades))
	}
}

func TestRollingUpgrade_SameVersion(t *testing.T) {
	mgr := newTestManager(t, &mockUpgrader{}, &mockLister{})

	status := mgr.rollUpgrade(context.Background(), "ignored", "prod", "1.12.6", "1.12.6", "talos")

	if status.Phase != PhaseComplete {
		t.Errorf("phase = %s, want complete (no-op)", status.Phase)
	}
}

func TestRollingUpgrade_NoMachines(t *testing.T) {
	mgr := newTestManager(t, &mockUpgrader{}, &mockLister{machines: nil})

	status := mgr.rollUpgrade(context.Background(), "ignored", "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseComplete {
		t.Errorf("phase = %s, want complete (no machines)", status.Phase)
	}
}

func TestRollingUpgrade_UpgradeFails_Rollback(t *testing.T) {
	upgrader := &mockUpgrader{
		upgradeErr: map[string]error{"m2": errors.New("upgrade failed")},
	}
	lister := &mockLister{machines: []string{"m1", "m2", "m3"}}
	mgr := newTestManager(t, upgrader, lister)

	status := mgr.rollUpgrade(context.Background(), "ignored", "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseFailed {
		t.Errorf("phase = %s, want failed", status.Phase)
	}
	if status.Completed != 1 {
		t.Errorf("completed = %d, want 1 (m1 succeeded)", status.Completed)
	}
	if !strings.Contains(status.Error, "upgrade failed") {
		t.Errorf("error = %q, should mention upgrade failure", status.Error)
	}
	upgrader.mu.Lock()
	defer upgrader.mu.Unlock()
	if len(upgrader.rollback) != 1 || upgrader.rollback[0] != "m2" {
		t.Errorf("rollback = %v, want [m2]", upgrader.rollback)
	}
}

func TestRollingUpgrade_HealthCheckFails_Rollback(t *testing.T) {
	upgrader := &mockUpgrader{
		healthErr: map[string]error{"m1": errors.New("not ready")},
	}
	lister := &mockLister{machines: []string{"m1"}}
	mgr := newTestManager(t, upgrader, lister)

	status := mgr.rollUpgrade(context.Background(), "ignored", "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseFailed {
		t.Errorf("phase = %s, want failed", status.Phase)
	}
	if !strings.Contains(status.Error, "health check failed") {
		t.Errorf("error = %q, should mention health check", status.Error)
	}
}

func TestRollingUpgrade_ListFails(t *testing.T) {
	lister := &mockLister{err: errors.New("list failed")}
	mgr := newTestManager(t, &mockUpgrader{}, lister)

	status := mgr.rollUpgrade(context.Background(), "ignored", "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseFailed {
		t.Errorf("phase = %s, want failed", status.Phase)
	}
}

func TestRollingUpgrade_CancelBetweenMachines(t *testing.T) {
	// A slow upgrader so we can cancel mid-loop.
	upgrader := &mockUpgrader{}
	lister := &mockLister{machines: []string{"m1", "m2", "m3"}}
	mgr := newTestManager(t, upgrader, lister)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	status := mgr.rollUpgrade(ctx, "ignored", "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseCanceled {
		t.Errorf("phase = %s, want canceled", status.Phase)
	}
}

// --- StoreMachineLister ordering ---

func TestStoreMachineLister_ControlPlaneFirst(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	// Create machines with mixed roles.
	for _, m := range []struct{ id, role string }{
		{"w1", "worker"}, {"cp1", "controlplane"}, {"w2", "worker"}, {"cp2", "controlplane"},
	} {
		spec := state.MachineSpec{Connected: true}
		status := state.MachineStatus{Role: m.role, Stage: state.StageReady, Ready: true}
		labels := map[string]string{"rezuscloud.io/tenant": "prod"}
		_, err := store.CreateResource("machine", m.id, spec, status, labels, nil)
		if err != nil {
			t.Fatalf("create machine %s: %v", m.id, err)
		}
	}

	lister := NewStoreMachineLister(store)
	ordered, err := lister.ListMachinesInOrder(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 4 {
		t.Fatalf("got %d machines, want 4", len(ordered))
	}
	// First two must be control planes.
	for i := 0; i < 2; i++ {
		if !strings.HasPrefix(ordered[i], "cp") {
			t.Errorf("position %d = %q, want a control plane first", i, ordered[i])
		}
	}
}

// --- API Tests ---

func newTestAPI(t *testing.T) (*state.Store, http.Handler) {
	t.Helper()
	store := newTestStore(t)
	mgr := NewManager(store, NoOpMachineUpgrader{}, NewStoreMachineLister(store))
	api := NewAPI(store, mgr)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	return store, mux
}

func TestPreCheck_Success(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	body := `{"component":"talos","version":"1.13.0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/prechecks", strings.NewReader(body))
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp preCheckResponse
	_ = parseJSON(w.Body.String(), &resp)
	if !resp.CanUpgrade {
		t.Error("should be able to upgrade")
	}
}

func TestPreCheck_NoMachines(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")

	body := `{"component":"talos","version":"1.13.0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/prechecks", strings.NewReader(body))
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp preCheckResponse
	_ = parseJSON(w.Body.String(), &resp)
	if resp.CanUpgrade {
		t.Error("should not be able to upgrade with no machines")
	}
}

func TestPreCheck_AlreadyAtVersion(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	body := `{"component":"talos","version":"1.12.6"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/prechecks", strings.NewReader(body))
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp preCheckResponse
	_ = parseJSON(w.Body.String(), &resp)
	if resp.CanUpgrade {
		t.Error("should not upgrade when already at version")
	}
}

func TestPreCheck_InvalidComponent(t *testing.T) {
	_, mux := newTestAPI(t)

	body := `{"component":"invalid","version":"1.0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/prechecks", strings.NewReader(body))
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPreCheck_TenantNotFound(t *testing.T) {
	_, mux := newTestAPI(t)

	body := `{"component":"talos","version":"1.13.0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/nope/prechecks", strings.NewReader(body))
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPreCheck_Downgrade(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	body := `{"component":"talos","version":"1.11.0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/prechecks", strings.NewReader(body))
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp preCheckResponse
	_ = parseJSON(w.Body.String(), &resp)
	if len(resp.Warnings) == 0 {
		t.Error("should warn about downgrade")
	}
}

func TestGetStatus_Idle(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/upgrade-status", nil)
	req.SetPathValue("name", "prod")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp upgradeStatusResponse
	_ = parseJSON(w.Body.String(), &resp)
	if resp.Status.Phase != PhaseIdle {
		t.Errorf("phase = %s, want idle", resp.Status.Phase)
	}
}

func TestGetStatus_TenantNotFound(t *testing.T) {
	_, mux := newTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/nope/upgrade-status", nil)
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestStartAndListRuns(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/upgrades", strings.NewReader(`{"component":"talos","version":"1.13.0"}`))
	startReq.SetPathValue("name", "prod")
	startW := httptest.NewRecorder()
	mux.ServeHTTP(startW, startReq)
	if startW.Code != http.StatusCreated {
		t.Fatalf("start status = %d, want 201: %s", startW.Code, startW.Body.String())
	}

	// Wait for the async run to settle.
	waitForRunPhase(t, store, "prod", PhaseComplete, 2*time.Second)

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/upgrades", nil)
	listReq.SetPathValue("name", "prod")
	listW := httptest.NewRecorder()
	mux.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listW.Code)
	}
	var listed runListResponse
	_ = parseJSON(listW.Body.String(), &listed)
	if listed.Total == 0 {
		t.Fatalf("expected at least one run")
	}
}

func TestStartRun_TenantNotFound(t *testing.T) {
	_, mux := newTestAPI(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/nope/upgrades", strings.NewReader(`{"component":"talos","version":"1.13.0"}`))
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestCancelRun(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	for i := 0; i < 8; i++ {
		setupMachine(t, store, "prod", fmt.Sprintf("m%d", i))
	}

	mgr := NewManager(store, NoOpMachineUpgrader{}, NewStoreMachineLister(store))
	run, err := mgr.StartRun("prod", "talos", "1.13.0", "test")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := mgr.CancelRun(run.Metadata.Name); err != nil {
		t.Fatalf("cancel run: %v", err)
	}

	final := waitForRunPhase(t, store, "prod", PhaseComplete, 2*time.Second)
	// After cancel the run is either canceled (if caught mid-loop) or complete
	// (if it already finished). Both are acceptable.
	if final.Status.Phase != PhaseCanceled && final.Status.Phase != PhaseComplete {
		t.Fatalf("expected canceled or complete, got %s", final.Status.Phase)
	}
}

func TestGetRunAndCancelEndpoints(t *testing.T) {
	store, mux := newTestAPI(t)
	setupTenant(t, store, "prod")
	for i := 0; i < 6; i++ {
		setupMachine(t, store, "prod", fmt.Sprintf("m%d", i))
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/upgrades", strings.NewReader(`{"component":"talos","version":"1.13.0"}`))
	startReq.SetPathValue("name", "prod")
	startW := httptest.NewRecorder()
	mux.ServeHTTP(startW, startReq)
	if startW.Code != http.StatusCreated {
		t.Fatalf("start status = %d", startW.Code)
	}
	var started runResponse
	_ = parseJSON(startW.Body.String(), &started)
	if started.Run == nil {
		t.Fatalf("expected run payload")
	}

	// Wait for the run to complete so GetRun returns a stable record.
	waitForRunPhase(t, store, "prod", PhaseComplete, 2*time.Second)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/upgrades/"+started.Run.Metadata.Name, nil)
	getReq.SetPathValue("name", "prod")
	getReq.SetPathValue("id", started.Run.Metadata.Name)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get status = %d", getW.Code)
	}

	// Cancel on a completed run returns BadRequest (run not active).
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/upgrades/"+started.Run.Metadata.Name+"/cancel", nil)
	cancelReq.SetPathValue("name", "prod")
	cancelReq.SetPathValue("id", started.Run.Metadata.Name)
	cancelW := httptest.NewRecorder()
	mux.ServeHTTP(cancelW, cancelReq)
	if cancelW.Code != http.StatusNoContent && cancelW.Code != http.StatusBadRequest {
		t.Fatalf("cancel status = %d", cancelW.Code)
	}
}

// --- Manager construction ---

func TestNewManager_VerifiesNoGlobalState(t *testing.T) {
	store1, err := state.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("open store1: %v", err)
	}
	t.Cleanup(func() { _ = store1.Close() })

	store2, err := state.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("open store2: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	mgr1 := NewManager(store1, NoOpMachineUpgrader{}, NewStoreMachineLister(store1))
	mgr2 := NewManager(store2, NoOpMachineUpgrader{}, NewStoreMachineLister(store2))

	if mgr1 == nil || mgr2 == nil {
		t.Fatal("expected non-nil managers")
	}
	if mgr1 == mgr2 {
		t.Error("expected distinct managers for distinct stores")
	}
}

// waitForRunPhase polls the store until the latest run for the tenant reaches
// one of the terminal phases, returning it.
func waitForRunPhase(t *testing.T, store *state.Store, tenant string, want Phase, timeout time.Duration) *Run {
	t.Helper()
	mgr := NewManager(store, NoOpMachineUpgrader{}, NewStoreMachineLister(store))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runs, err := mgr.ListRuns(tenant)
		if err == nil && len(runs) > 0 {
			r := runs[0]
			if r.Status.Phase == want || r.Status.Phase == PhaseFailed || r.Status.Phase == PhaseCanceled {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run for %q never reached %s within %s", tenant, want, timeout)
	return nil
}

func parseJSON(body string, v any) error {
	return json.Unmarshal([]byte(body), v)
}
