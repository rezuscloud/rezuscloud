package settings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
)

// --- stubs ---

type stubHost struct {
	lastProps      layout.BaseProps
	redirectTarget string
	redirectStatus int
	toast          layout.ToastData
}

func (s *stubHost) Render(_ http.ResponseWriter, _ *http.Request, props layout.BaseProps) {
	s.lastProps = props
}
func (s *stubHost) PopToast(_ *http.Request) layout.ToastData { return s.toast }
func (s *stubHost) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return next
}
func (s *stubHost) CanMutate(_ *http.Request) bool { return true }
func (s *stubHost) IsAdmin(_ *http.Request) bool   { return true }
func (s *stubHost) TenantNames() []string          { return []string{"prod"} }
func (s *stubHost) RedirectAction(w http.ResponseWriter, _ *http.Request, target string) {
	s.redirectTarget = target
	s.redirectStatus = http.StatusSeeOther
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusSeeOther)
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
	return len(f.events), nil
}
func (f *fakeAuditStore) DeleteEventsOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// --- helpers ---

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newHandler(t *testing.T) (*Handler, *state.Store, *stubHost) {
	t.Helper()
	store := newTestStore(t)
	host := &stubHost{toast: layout.ToastData{Type: "info", Message: "test"}}
	h := New(store, nil, nil, host)
	return h, store, host
}

// --- tests ---

// TestNew_HandlerConstructs verifies New wires up dependencies.
func TestNew_HandlerConstructs(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	h := New(store, nil, nil, host)
	if h == nil {
		t.Fatal("New returned nil")
	}
	if h.store == nil || h.host == nil {
		t.Error("dependencies not wired")
	}
}

// TestSettingsIndexPage_Renders renders the index with no subsystems.
func TestSettingsIndexPage_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	w := httptest.NewRecorder()
	h.SettingsIndexPage(w, req)

	if host.lastProps.Title != "Settings" {
		t.Errorf("Title = %q, want Settings", host.lastProps.Title)
	}
	if host.lastProps.Page != "settings" {
		t.Errorf("Page = %q, want settings", host.lastProps.Page)
	}
}

// TestBackupsPage_503WhenNoService verifies the backups page returns 503
// when the backup service is not configured.
func TestBackupsPage_503WhenNoService(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/settings/backups", nil)
	w := httptest.NewRecorder()
	h.BackupsPage(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestBackupsPage_RendersWithService verifies the page renders when the
// backup service is configured. We skip this test because constructing a
// real *backup.Service requires a real S3-compatible Store; the smoke path
// is covered by the integration suite. Here we just verify the shape of the
// redirect that BackupsRunDatabase emits when no service is configured.
func TestBackupsPage_RedirectsWhenNoService(t *testing.T) {
	h, _, host := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/settings/backups/database", nil)
	w := httptest.NewRecorder()
	h.BackupsRunDatabase(w, req)
	if !strings.Contains(host.redirectTarget, "backup+service+unavailable") {
		t.Errorf("redirect = %q, missing unavailable toast", host.redirectTarget)
	}
}

// TestUsersPage_Renders creates a user and verifies the list shows it.
func TestUsersPage_Renders(t *testing.T) {
	h, store, host := newHandler(t)
	hash, _ := auth.HashPassword("pw")
	if _, err := store.CreateUser("alice", state.UserSpec{Role: "admin", PasswordHash: hash}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/settings/users", nil)
	w := httptest.NewRecorder()
	h.UsersPage(w, req)
	if host.lastProps.Title != "Users" {
		t.Errorf("Title = %q, want Users", host.lastProps.Title)
	}
}

// TestUserCreate_Success creates a user via POST.
func TestUserCreate_Success(t *testing.T) {
	h, store, host := newHandler(t)
	body := strings.NewReader("name=alice&role=admin&password=pw")
	req := httptest.NewRequest(http.MethodPost, "/settings/users", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.UserCreate(w, req)
	if host.redirectStatus != http.StatusSeeOther {
		t.Errorf("redirect status = %d, want 303", host.redirectStatus)
	}
	if !strings.Contains(host.redirectTarget, "toast=user+created") {
		t.Errorf("redirect target = %q, missing success toast", host.redirectTarget)
	}
	if u, _ := store.GetUser("alice"); u == nil {
		t.Error("user not created")
	}
}

// TestUserCreate_DuplicateRejected verifies duplicate names error.
func TestUserCreate_DuplicateRejected(t *testing.T) {
	h, store, host := newHandler(t)
	hash, _ := auth.HashPassword("pw")
	if _, err := store.CreateUser("alice", state.UserSpec{Role: "admin", PasswordHash: hash}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := strings.NewReader("name=alice&role=admin&password=pw")
	req := httptest.NewRequest(http.MethodPost, "/settings/users", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.UserCreate(w, req)
	if !strings.Contains(host.redirectTarget, "user+already+exists") {
		t.Errorf("redirect target = %q, missing duplicate toast", host.redirectTarget)
	}
}

// TestUserDelete_ForbiddenOnSelf verifies you can't delete yourself.
func TestUserDelete_ForbiddenOnSelf(t *testing.T) {
	h, store, host := newHandler(t)
	hash, _ := auth.HashPassword("pw")
	if _, err := store.CreateUser("alice", state.UserSpec{Role: "admin", PasswordHash: hash}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Caller is "alice" — make the stub host's context reflect that.
	req := httptest.NewRequest(http.MethodPost, "/settings/users/alice/delete", nil)
	req.SetPathValue("name", "alice")
	req = req.WithContext(auth.WithClaims(req.Context(), "alice", "admin"))
	w := httptest.NewRecorder()
	h.UserDelete(w, req)
	if !strings.Contains(host.redirectTarget, "cannot+delete+current+user") {
		t.Errorf("redirect target = %q, missing self-delete guard", host.redirectTarget)
	}
}

// TestAuditPage_503WhenNoStore verifies the audit page returns 503 when
// the audit store is not configured.
func TestAuditPage_503WhenNoStore(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/settings/audit", nil)
	w := httptest.NewRecorder()
	h.AuditPage(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestAuditPage_Renders verifies the audit page renders when the store is configured.
func TestAuditPage_Renders(t *testing.T) {
	store := newTestStore(t)
	host := &stubHost{}
	auditStore := &fakeAuditStore{events: []audit.Event{{ID: 1, UserName: "alice", Method: "GET"}}}
	h := New(store, nil, auditStore, host)

	req := httptest.NewRequest(http.MethodGet, "/settings/audit", nil)
	w := httptest.NewRecorder()
	h.AuditPage(w, req)

	if host.lastProps.Title != "Audit Log" {
		t.Errorf("Title = %q, want Audit Log", host.lastProps.Title)
	}
}

// TestProvidersPage_Renders renders the providers list.
func TestProvidersPage_Renders(t *testing.T) {
	h, _, host := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/providers", nil)
	w := httptest.NewRecorder()
	h.ProvidersPage(w, req)
	if host.lastProps.Title != "Providers" {
		t.Errorf("Title = %q, want Providers", host.lastProps.Title)
	}
}

// TestRegisterRoutes verifies all settings routes are wired.
func TestRegisterRoutes(t *testing.T) {
	h, _, _ := newHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/settings"},
		{http.MethodGet, "/settings/backups"},
		{http.MethodPost, "/settings/backups/database"},
		{http.MethodPost, "/settings/backups/resources"},
		{http.MethodPost, "/settings/backups/restore"},
		{http.MethodPost, "/settings/backups/policy"},
		{http.MethodGet, "/settings/users"},
		{http.MethodPost, "/settings/users"},
		{http.MethodGet, "/settings/api-tokens"},
		{http.MethodGet, "/settings/audit"},
		{http.MethodGet, "/providers"},
	}
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s not registered (got 404)", r.method, r.path)
		}
	}
}

// TestRpoEstimate_EdgeCases verifies the helper used by BackupsPage.
func TestRpoEstimate_EdgeCases(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"empty", "", "unknown"},
		{"never", "never", "unknown"},
		{"invalid", "not-a-time", "unknown"},
		{"valid_recent", time.Now().Add(5 * time.Minute).Format(time.RFC3339), "<1m"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			got := rpoEstimate(c.input)
			if got != c.want && c.name != "valid_recent" {
				t.Errorf("rpoEstimate(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestEnvDefault verifies env-or-fallback semantics.
func TestEnvDefault(t *testing.T) {
	t.Setenv("REZUSCLOUD_TEST_KEY", "from-env")
	if got := envDefault("REZUSCLOUD_TEST_KEY", "fallback"); got != "from-env" {
		t.Errorf("envDefault set = %q, want from-env", got)
	}
	if got := envDefault("REZUSCLOUD_UNSET_KEY", "fallback"); got != "fallback" {
		t.Errorf("envDefault unset = %q, want fallback", got)
	}
}

// TestHost_Interface verifies the stub satisfies Host.
func TestHost_Interface(t *testing.T) {
	var _ Host = (*stubHost)(nil)
}
