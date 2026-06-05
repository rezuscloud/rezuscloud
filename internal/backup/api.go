package backup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// API provides HTTP handlers for backup management.
type API struct {
	manager *Manager
	store   *state.Store
}

// NewAPI creates a backup API handler.
func NewAPI(manager *Manager, store *state.Store) *API {
	return &API{manager: manager, store: store}
}

// RegisterRoutes registers backup routes.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/backups/database", a.BackupDatabase)
	mux.HandleFunc("POST /api/v1/backups/resources", a.BackupResources)
	mux.HandleFunc("GET /api/v1/backups", a.List)
}

// backupResponse represents a backup creation response.
type backupResponse struct {
	Snapshot Snapshot `json:"snapshot"`
	Status   string   `json:"status"`
}

var _ = backupResponse{} // used by future handlers

// BackupDatabase triggers a database backup to S3.
func (a *API) BackupDatabase(w http.ResponseWriter, r *http.Request) {
	// In production, this would:
	// 1. sqlite3 ".backup" to temp file
	// 2. Upload to S3
	// For now, create a placeholder snapshot.
	if a.manager == nil {
		writeBackupError(w, "backup not configured", "NotConfigured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	dbPath := filepath.Join("/data", "rezuscloud.db")
	key := SnapshotKey("backups", "database", now)

	// Use store path as a signal.
	_ = dbPath

	snap := &Snapshot{
		ID:        fmt.Sprintf("db-%d", now.Unix()),
		Timestamp: now,
		Type:      "database",
		Key:       key,
	}

	// Upload placeholder.
	if err := a.manager.store.Upload(ctx, key, strings.NewReader("placeholder")); err != nil {
		writeBackupError(w, "upload failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"snapshot":{"id":"%s","timestamp":"%s","type":"database","key":"%s"},"status":"created"}`,
		snap.ID, snap.Timestamp.Format(time.RFC3339), snap.Key)
}

// BackupResources triggers a CRD resources export to S3.
func (a *API) BackupResources(w http.ResponseWriter, r *http.Request) {
	if a.manager == nil {
		writeBackupError(w, "backup not configured", "NotConfigured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	key := SnapshotKey("backups", "resources", now)

	// Export all resources as JSON.
	var resources []map[string]any
	for _, resType := range []string{"tenant", "nodegroup", "machine", "provider", "configpatch"} {
		metas, specs, _, _, _ := a.store.ListResources(resType, state.ListOptions{})
		for i, m := range metas {
			var spec any
			_ = json.Unmarshal(specs[i], &spec)
			resources = append(resources, map[string]any{
				"type":   resType,
				"name":   m.Name,
				"spec":   spec,
				"labels": m.Labels,
			})
		}
	}

	data, _ := json.Marshal(resources)
	if err := a.manager.store.Upload(ctx, key, strings.NewReader(string(data))); err != nil {
		writeBackupError(w, "upload failed", "InternalError", http.StatusInternalServerError)
		return
	}

	snap := &Snapshot{
		ID:        fmt.Sprintf("res-%d", now.Unix()),
		Timestamp: now,
		Size:      int64(len(data)),
		Type:      "resources",
		Key:       key,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"snapshot":{"id":"%s","timestamp":"%s","type":"resources","size":%d,"key":"%s"},"status":"created"}`,
		snap.ID, snap.Timestamp.Format(time.RFC3339), snap.Size, snap.Key)
}

type listResponse struct {
	Backups []Snapshot `json:"backups"`
}

// List returns recent backup snapshots.
func (a *API) List(w http.ResponseWriter, _ *http.Request) {
	// In production, this would list from S3.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Backups: []Snapshot{}})
}

func writeBackupError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
