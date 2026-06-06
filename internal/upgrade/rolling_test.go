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

// --- Rolling Upgrader Tests ---

type mockUpgrader struct {
	upgradeErr  map[string]error
	healthErr   map[string]error
	rollbackErr map[string]error
}

func (m *mockUpgrader) UpgradeMachine(_ context.Context, id, _ string) error {
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

func TestRollingUpgrade_Success(t *testing.T) {
	upgrader := &mockUpgrader{}
	lister := &mockLister{machines: []string{"m1", "m2", "m3"}}
	ru := NewRollingUpgrader(upgrader, lister)

	status := ru.Upgrade(context.Background(), "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseComplete {
		t.Errorf("phase = %s, want complete", status.Phase)
	}
	if status.Completed != 3 {
		t.Errorf("completed = %d, want 3", status.Completed)
	}
	if status.TotalMachines != 3 {
		t.Errorf("total = %d, want 3", status.TotalMachines)
	}
}

func TestRollingUpgrade_SameVersion(t *testing.T) {
	ru := NewRollingUpgrader(&mockUpgrader{}, &mockLister{})

	status := ru.Upgrade(context.Background(), "prod", "1.12.6", "1.12.6", "talos")

	if status.Phase != PhaseComplete {
		t.Errorf("phase = %s, want complete (no-op)", status.Phase)
	}
}

func TestRollingUpgrade_NoMachines(t *testing.T) {
	ru := NewRollingUpgrader(&mockUpgrader{}, &mockLister{machines: nil})

	status := ru.Upgrade(context.Background(), "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseComplete {
		t.Errorf("phase = %s, want complete (no machines)", status.Phase)
	}
}

func TestRollingUpgrade_UpgradeFails_Rollback(t *testing.T) {
	upgrader := &mockUpgrader{
		upgradeErr: map[string]error{"m2": errors.New("upgrade failed")},
	}
	lister := &mockLister{machines: []string{"m1", "m2", "m3"}}
	ru := NewRollingUpgrader(upgrader, lister)

	status := ru.Upgrade(context.Background(), "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseFailed {
		t.Errorf("phase = %s, want failed", status.Phase)
	}
	if status.Completed != 1 {
		t.Errorf("completed = %d, want 1 (m1 succeeded)", status.Completed)
	}
	if !strings.Contains(status.Error, "upgrade failed") {
		t.Errorf("error = %q, should mention upgrade failure", status.Error)
	}
}

func TestRollingUpgrade_HealthCheckFails_Rollback(t *testing.T) {
	upgrader := &mockUpgrader{
		healthErr: map[string]error{"m1": errors.New("not ready")},
	}
	lister := &mockLister{machines: []string{"m1"}}
	ru := NewRollingUpgrader(upgrader, lister)

	status := ru.Upgrade(context.Background(), "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseFailed {
		t.Errorf("phase = %s, want failed", status.Phase)
	}
	if !strings.Contains(status.Error, "health check failed") {
		t.Errorf("error = %q, should mention health check", status.Error)
	}
}

func TestRollingUpgrade_ListFails(t *testing.T) {
	lister := &mockLister{err: errors.New("list failed")}
	ru := NewRollingUpgrader(&mockUpgrader{}, lister)

	status := ru.Upgrade(context.Background(), "prod", "1.12.6", "1.13.0", "talos")

	if status.Phase != PhaseFailed {
		t.Errorf("phase = %s, want failed", status.Phase)
	}
}

// --- API Tests ---

func TestPreCheck_Success(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	setupTenant(t, store, "prod")

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	api := NewAPI(store, nil)

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	setupTenant(t, store, "prod")

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	store := newTestStore(t)
	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/nope/upgrade-status", nil)
	req.SetPathValue("name", "nope")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestStartAndListRuns(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	setupMachine(t, store, "prod", "m1")

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/upgrades", strings.NewReader(`{"component":"talos","version":"1.13.0"}`))
	startReq.SetPathValue("name", "prod")
	startW := httptest.NewRecorder()
	mux.ServeHTTP(startW, startReq)
	if startW.Code != http.StatusCreated {
		t.Fatalf("start status = %d, want 201: %s", startW.Code, startW.Body.String())
	}

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
	store := newTestStore(t)
	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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

	mgr := GetManager(store)
	run, err := mgr.StartRun("prod", "talos", "1.13.0", "test")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := mgr.CancelRun(run.Metadata.Name); err != nil {
		t.Fatalf("cancel run: %v", err)
	}

	var final *Run
	for i := 0; i < 30; i++ {
		final, _ = mgr.GetRun("prod", run.Metadata.Name)
		if final != nil && (final.Status.Phase == PhaseCanceled || final.Status.Phase == PhaseComplete) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final == nil {
		t.Fatalf("run not found after cancel")
	}
	if final.Status.Phase != PhaseCanceled && final.Status.Phase != PhaseComplete {
		t.Fatalf("expected canceled or complete, got %s", final.Status.Phase)
	}
}

func TestGetRunAndCancelEndpoints(t *testing.T) {
	store := newTestStore(t)
	setupTenant(t, store, "prod")
	for i := 0; i < 6; i++ {
		setupMachine(t, store, "prod", fmt.Sprintf("m%d", i))
	}

	api := NewAPI(store, nil)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/upgrades/"+started.Run.Metadata.Name, nil)
	getReq.SetPathValue("name", "prod")
	getReq.SetPathValue("id", started.Run.Metadata.Name)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get status = %d", getW.Code)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/upgrades/"+started.Run.Metadata.Name+"/cancel", nil)
	cancelReq.SetPathValue("name", "prod")
	cancelReq.SetPathValue("id", started.Run.Metadata.Name)
	cancelW := httptest.NewRecorder()
	mux.ServeHTTP(cancelW, cancelReq)
	if cancelW.Code != http.StatusNoContent && cancelW.Code != http.StatusBadRequest {
		t.Fatalf("cancel status = %d", cancelW.Code)
	}
}

func parseJSON(body string, v any) error {
	return json.Unmarshal([]byte(body), v)
}
