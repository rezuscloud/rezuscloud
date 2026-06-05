package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// mockStore implements backup.Store for testing.
type mockStore struct {
	uploads map[string]string
	err     error
}

func newMockStore() *mockStore {
	return &mockStore{uploads: make(map[string]string)}
}

func (m *mockStore) Upload(_ context.Context, key string, data io.Reader) error {
	if m.err != nil {
		return m.err
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(data)
	m.uploads[key] = buf.String()
	return nil
}

// --- Manager Tests ---

func TestBackupDatabase(t *testing.T) {
	ms := newMockStore()
	mgr := NewManager(ms, Config{Prefix: "backups", Bucket: "test"})

	snap, err := mgr.BackupDatabase(context.Background(), strings.NewReader("db-content"), 11)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	if snap.Type != "database" {
		t.Errorf("type = %q, want database", snap.Type)
	}
	if snap.Size != 11 {
		t.Errorf("size = %d, want 11", snap.Size)
	}
	if len(ms.uploads) != 1 {
		t.Errorf("uploads = %d, want 1", len(ms.uploads))
	}
}

func TestBackupResources(t *testing.T) {
	ms := newMockStore()
	mgr := NewManager(ms, Config{Prefix: "snapshots"})

	snap, err := mgr.BackupResources(context.Background(), strings.NewReader(`{"tenants":[]}`), 14)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	if snap.Type != "resources" {
		t.Errorf("type = %q, want resources", snap.Type)
	}
}

func TestBackupDatabase_UploadFails(t *testing.T) {
	ms := newMockStore()
	ms.err = errors.New("network error")
	mgr := NewManager(ms, Config{})

	_, err := mgr.BackupDatabase(context.Background(), strings.NewReader("data"), 4)
	if err == nil {
		t.Error("should fail when upload fails")
	}
}

func TestSnapshotKey(t *testing.T) {
	tests := []struct {
		prefix, typ string
		want        string
	}{
		{"backups", "database", "backups/2025-06-01/database.db"},
		{"snapshots", "resources", "snapshots/2025-06-01/resources.json"},
	}

	for _, tt := range tests {
		t.Run(tt.prefix+"/"+tt.typ, func(t *testing.T) {
			got := SnapshotKey(tt.prefix, tt.typ, time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC))
			if got != tt.want {
				t.Errorf("SnapshotKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewManager_Defaults(t *testing.T) {
	ms := newMockStore()
	mgr := NewManager(ms, Config{})

	if mgr.config.Prefix != "backups" {
		t.Errorf("prefix = %q, want 'backups'", mgr.config.Prefix)
	}
	if mgr.config.Retention != 7 {
		t.Errorf("retention = %d, want 7", mgr.config.Retention)
	}
}

// --- API Tests ---

func TestAPI_BackupDatabase_Success(t *testing.T) {
	store := newTestStore(t)
	ms := newMockStore()
	mgr := NewManager(ms, Config{Bucket: "test"})
	api := NewAPI(mgr, store)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups/database", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	if len(ms.uploads) != 1 {
		t.Errorf("should have uploaded 1 file, got %d", len(ms.uploads))
	}
}

func TestAPI_BackupResources_Success(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.CreateResource("tenant", "prod", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil, nil)

	ms := newMockStore()
	mgr := NewManager(ms, Config{Bucket: "test"})
	api := NewAPI(mgr, store)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups/resources", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", w.Code, w.Body.String())
	}

	if len(ms.uploads) != 1 {
		t.Errorf("should have uploaded 1 file, got %d", len(ms.uploads))
	}
}

func TestAPI_BackupDatabase_NotConfigured(t *testing.T) {
	store := newTestStore(t)
	api := NewAPI(nil, store)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups/database", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestAPI_List(t *testing.T) {
	store := newTestStore(t)
	api := NewAPI(nil, store)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
