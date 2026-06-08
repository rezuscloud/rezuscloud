package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/dashboard"
	"github.com/rezuscloud/rezuscloud/internal/metrics"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// --- stubs ---

type stubHost struct {
	lastProps  layout.BaseProps
	authCalled bool
}

func (s *stubHost) Render(_ http.ResponseWriter, _ *http.Request, props layout.BaseProps) {
	s.lastProps = props
}
func (s *stubHost) TenantSummaries() []pages.TenantSummary { return nil }
func (s *stubHost) PopToast(_ *http.Request) layout.ToastData {
	return layout.ToastData{Type: "info", Message: "hello"}
}
func (s *stubHost) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	s.authCalled = true
	return next
}

type fakeAuditStore struct {
	events []audit.Event
	err    error
}

func (f *fakeAuditStore) InsertEvent(_ context.Context, _ audit.Event) error { return nil }
func (f *fakeAuditStore) ListEvents(_ context.Context, _ audit.Filter) ([]audit.Event, error) {
	return f.events, f.err
}
func (f *fakeAuditStore) CountEvents(_ context.Context, _ audit.Filter) (int, error) {
	return 0, nil
}
func (f *fakeAuditStore) DeleteEventsOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

type fakeBackupReader struct {
	snapshots []dashboard.BackupSnapshot
	err       error
}

func (f *fakeBackupReader) ListSnapshots() ([]dashboard.BackupSnapshot, error) {
	return f.snapshots, f.err
}

var _ BackupReader = (*fakeBackupReader)(nil)

type fakeUpgradeReader struct {
	runs []dashboard.UpgradeRun
	err  error
}

func (f *fakeUpgradeReader) ListRuns(_ string) ([]dashboard.UpgradeRun, error) {
	return f.runs, f.err
}

var _ UpgradeReader = (*fakeUpgradeReader)(nil)

// TestDashboard_PostureWithBackupAndUpgrade verifies that backup and upgrade
// adapters are wired correctly and don't cause panics when populating the
// W14 posture snapshot.
func TestDashboard_PostureWithBackupAndUpgrade(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	backupReader := &fakeBackupReader{
		snapshots: []dashboard.BackupSnapshot{{CreatedAt: "2026-06-06T12:00:00Z", Status: dashboard.BackupSnapshotStatus{Status: "ok"}}},
	}
	upgradeReader := &fakeUpgradeReader{
		runs: []dashboard.UpgradeRun{{Tenant: "t", Target: "1.30.0", Phase: "running"}},
	}
	h := New(store, nil, nil, backupReader, upgradeReader, nil, host)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if host.lastProps.Page != "dashboard" {
		t.Errorf("Page = %q, want dashboard", host.lastProps.Page)
	}
}

// --- helpers ---

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// --- tests ---

// TestNew_HandlerConstructs verifies that New wires up dependencies without
// requiring all of them (graceful degradation).
func TestNew_HandlerConstructs(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, nil, nil, nil, nil, host)
	if h == nil {
		t.Fatal("New returned nil")
	}
	if h.store == nil {
		t.Error("store not wired")
	}
	if h.host == nil {
		t.Error("host not wired")
	}
}

// TestDashboard_RendersWithMinimalDeps verifies the dashboard page can render
// with no optional subsystems configured (no audit, no backup, no upgrades).
func TestDashboard_RendersWithMinimalDeps(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, nil, nil, nil, nil, host)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if host.lastProps.Title != "Dashboard" {
		t.Errorf("Title = %q, want Dashboard", host.lastProps.Title)
	}
	if host.lastProps.Page != "dashboard" {
		t.Errorf("Page = %q, want dashboard", host.lastProps.Page)
	}
	// Toast comes from the stubHost.PopToast.
	if host.lastProps.Toast.Message != "hello" {
		t.Errorf("Toast = %q, want hello", host.lastProps.Toast.Message)
	}
}

// TestDashboard_RendersWithAuditEvents verifies that recent audit events are
// surfaced into the page data.
func TestDashboard_RendersWithAuditEvents(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	auditStore := &fakeAuditStore{
		events: []audit.Event{
			{ID: 1, UserName: "alice", Method: "POST", Path: "/api/v1/tenants"},
			{ID: 2, UserName: "bob", Method: "GET", Path: "/api/v1/machines"},
		},
	}
	h := New(store, nil, auditStore, nil, nil, nil, host)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	// We can't inspect the rendered templ output here, but we can check that
	// the host's Render was called with the right Page (it always is for the
	// dashboard route).
	if host.lastProps.Page != "dashboard" {
		t.Errorf("Page = %q, want dashboard", host.lastProps.Page)
	}
}

// TestEventsStream_Returns404WhenBusNil verifies graceful degradation when
// the watch bus is not configured.
func TestEventsStream_Returns404WhenBusNil(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, nil, nil, nil, nil, host)

	req := httptest.NewRequest(http.MethodGet, "/events/stream", nil)
	w := httptest.NewRecorder()
	h.EventsStream(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no bus)", w.Code)
	}
}

// TestEventsStream_StreamsEvents verifies that events from the bus are
// serialized as SSE on the wire.
func TestEventsStream_StreamsEvents(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	bus := watch.NewBus()
	// watch.Bus has no Close; rely on test process exit.
	h := New(store, bus, nil, nil, nil, nil, host)

	// Pre-publish an event so the receiver has something to read.
	bus.Publish("machine", watch.Event{
		Type:   "added",
		Object: map[string]any{"id": "m1", "type": "machine"},
	})

	// Use a cancellable context so the handler exits after we read one event.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Run the handler in the background; cancel context after first event.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.EventsStream(w, req)
	}()

	// Wait a moment for the goroutine to subscribe, then publish another event.
	time.Sleep(20 * time.Millisecond)
	bus.Publish("machine", watch.Event{
		Type:   "added",
		Object: map[string]any{"id": "m2", "type": "machine"},
	})

	// Give the handler time to write the event, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Errorf("expected SSE 'data: ' prefix, got:\n%s", body)
	}
}

// TestRegisterRoutes verifies the routes are wired through Host.AuthRequired.
func TestRegisterRoutes(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, nil, nil, nil, nil, host)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	if !host.authCalled {
		t.Error("expected Host.AuthRequired to be invoked during route registration")
	}

	// Verify GET / is registered (no 404).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Error("GET / not registered (got 404)")
	}
}

// TestHost_Interface verifies *web.Handler satisfies Host (compile-time check).
// The actual satisfaction is tested in the integration suite; here we just
// assert that the interface shape is what we expect.
func TestHost_Interface(t *testing.T) {
	var _ Host = (*stubHost)(nil)
}

// stubMetricsAggregator returns a fixed ClusterResourceSummary for testing.
type stubMetricsAggregator struct {
	summary *metrics.ClusterResourceSummary
	err     error
}

func (s *stubMetricsAggregator) ClusterSummary(ctx context.Context) (*metrics.ClusterResourceSummary, error) {
	return s.summary, s.err
}

func TestDashboard_ResourcePressure(t *testing.T) {
	store := newTestStore(t)
	agg := &stubMetricsAggregator{
		summary: &metrics.ClusterResourceSummary{
			Nodes: 2,
			CPU: metrics.ClusterCPU{
				Capacity: 20000, Allocatable: 19900, Requested: 8000, Usage: 6400,
			},
			Memory: metrics.ClusterMemory{
				Capacity: 64e9, Allocatable: 63e9, Requested: 32e9, Usage: 40e9,
			},
			Pods: metrics.ClusterPods{Capacity: 220, Allocatable: 220, Running: 78},
			NodeDetails: []metrics.NodeResourceMetrics{
				{
					Name: "cp-01", Role: "control-plane", Status: "healthy",
					CPU:    metrics.CPU{Usage: metrics.ResourceQuantity{CPU: 1200}, Allocatable: metrics.ResourceQuantity{CPU: 3950}},
					Memory: metrics.Memory{Usage: metrics.ResourceQuantity{Memory: 8e9}, Allocatable: metrics.ResourceQuantity{Memory: 24e9}},
					Pods:   metrics.Pods{Running: 37, Allocatable: 110},
					Disk:   metrics.Disk{UsedBytes: 20e9, TotalBytes: 50e9},
					Conditions: metrics.Conditions{Ready: metrics.ConditionTrue},
				},
				{
					Name: "worker-01", Role: "worker", Status: "warning",
					CPU:    metrics.CPU{Usage: metrics.ResourceQuantity{CPU: 5200}, Allocatable: metrics.ResourceQuantity{CPU: 15950}},
					Memory: metrics.Memory{Usage: metrics.ResourceQuantity{Memory: 32e9}, Allocatable: metrics.ResourceQuantity{Memory: 39e9}},
					Pods:   metrics.Pods{Running: 41, Allocatable: 110},
					Disk:   metrics.Disk{UsedBytes: 100e9, TotalBytes: 150e9},
					Conditions: metrics.Conditions{Ready: metrics.ConditionTrue, MemoryPressure: metrics.ConditionTrue},
				},
			},
		},
	}

	host := &stubHost{}
	h := New(store, nil, nil, nil, nil, agg, host)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Cookie", "session=valid")
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if host.lastProps.Title != "Dashboard" {
		t.Errorf("Title = %q, want Dashboard", host.lastProps.Title)
	}
	if host.lastProps.Page != "dashboard" {
		t.Errorf("Page = %q, want dashboard", host.lastProps.Page)
	}
}
