package backup

import (
	"encoding/json"
	"net/http"
)

// API provides HTTP handlers for backup management.
type API struct {
	service *Service
}

// NewAPI creates a backup API handler backed by the given Service.
// Construct the Service once (typically via backup.NewComponent) and share
// it across API + WebUI to avoid duplicate wiring.
func NewAPI(svc *Service) *API {
	return &API{service: svc}
}

// RegisterRoutes registers backup routes.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/backups/database", a.BackupDatabase)
	mux.HandleFunc("POST /api/v1/backups/resources", a.BackupResources)
	mux.HandleFunc("GET /api/v1/backups", a.List)
	mux.HandleFunc("GET /api/v1/backups/policy", a.GetPolicy)
	mux.HandleFunc("PUT /api/v1/backups/policy", a.UpdatePolicy)
	mux.HandleFunc("POST /api/v1/backups/restore/dry-run", a.RestoreDryRun)
	mux.HandleFunc("POST /api/v1/backups/restore", a.Restore)
}

type backupResponse struct {
	Snapshot SnapshotRecord `json:"snapshot"`
	Status   string         `json:"status"`
}

// BackupDatabase triggers a real SQLite database backup.
func (a *API) BackupDatabase(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.service.TriggerDatabase(r.Context())
	if err != nil {
		if err.Error() == "backup not configured" {
			writeBackupError(w, err.Error(), "NotConfigured", http.StatusServiceUnavailable)
			return
		}
		writeBackupError(w, err.Error(), "InternalError", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(backupResponse{Snapshot: *snapshot, Status: "created"})
}

// BackupResources triggers a resources export backup.
func (a *API) BackupResources(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.service.TriggerResources(r.Context())
	if err != nil {
		if err.Error() == "backup not configured" {
			writeBackupError(w, err.Error(), "NotConfigured", http.StatusServiceUnavailable)
			return
		}
		writeBackupError(w, err.Error(), "InternalError", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(backupResponse{Snapshot: *snapshot, Status: "created"})
}

type listResponse struct {
	Backups []SnapshotRecord `json:"backups"`
}

// List returns recent snapshots.
func (a *API) List(w http.ResponseWriter, _ *http.Request) {
	backups, err := a.service.ListSnapshots()
	if err != nil {
		writeBackupError(w, err.Error(), "InternalError", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listResponse{Backups: backups})
}

// GetPolicy returns backup retention policy.
func (a *API) GetPolicy(w http.ResponseWriter, _ *http.Request) {
	policy, err := a.service.GetPolicy()
	if err != nil {
		writeBackupError(w, err.Error(), "InternalError", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(policy)
}

// UpdatePolicy updates backup retention policy.
func (a *API) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	var policy Policy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		writeBackupError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}
	if err := a.service.UpdatePolicy(policy); err != nil {
		writeBackupError(w, err.Error(), "BadRequest", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type restoreRequest struct {
	SnapshotID string `json:"snapshotId"`
}

// RestoreDryRun validates restore input and returns what would be restored.
func (a *API) RestoreDryRun(w http.ResponseWriter, r *http.Request) {
	a.restore(w, r, true)
}

// Restore applies a resources snapshot restore.
func (a *API) Restore(w http.ResponseWriter, r *http.Request) {
	a.restore(w, r, false)
}

func (a *API) restore(w http.ResponseWriter, r *http.Request, dryRun bool) {
	var req restoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SnapshotID == "" {
		writeBackupError(w, "snapshotId is required", "BadRequest", http.StatusBadRequest)
		return
	}
	result, err := a.service.Restore(r.Context(), req.SnapshotID, dryRun)
	if err != nil {
		writeBackupError(w, err.Error(), "BadRequest", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func writeBackupError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
