package upgrade

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// API provides HTTP handlers for upgrade management.
type API struct {
	store    *state.Store
	upgrader *RollingUpgrader
	manager  *Manager
}

// NewAPI creates an upgrade API handler.
func NewAPI(store *state.Store, upgrader *RollingUpgrader) *API {
	return &API{store: store, upgrader: upgrader, manager: GetManager(store)}
}

// RegisterRoutes registers upgrade routes.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tenants/{name}/prechecks", a.PreCheck)
	mux.HandleFunc("GET /api/v1/tenants/{name}/upgrade-status", a.GetStatus)
	mux.HandleFunc("POST /api/v1/tenants/{name}/upgrades", a.Start)
	mux.HandleFunc("GET /api/v1/tenants/{name}/upgrades", a.ListRuns)
	mux.HandleFunc("GET /api/v1/tenants/{name}/upgrades/{id}", a.GetRun)
	mux.HandleFunc("POST /api/v1/tenants/{name}/upgrades/{id}/cancel", a.Cancel)
}

type preCheckRequest struct {
	Component string `json:"component"` // "talos" or "kubernetes"
	Version   string `json:"version"`
}

type preCheckResponse struct {
	CanUpgrade bool     `json:"canUpgrade"`
	Reasons    []string `json:"reasons,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

type startRequest struct {
	Component string `json:"component"`
	Version   string `json:"version"`
}

type runResponse struct {
	Run *Run `json:"run"`
}

type runListResponse struct {
	Items []*Run `json:"items"`
	Total int    `json:"total"`
}

// PreCheck validates whether a tenant can be upgraded.
func (a *API) PreCheck(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")

	var req preCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeUpgradeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Component != "talos" && req.Component != "kubernetes" {
		writeUpgradeError(w, "component must be 'talos' or 'kubernetes'", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Version == "" {
		writeUpgradeError(w, "version is required", "BadRequest", http.StatusBadRequest)
		return
	}

	ok, reasons, notFound := a.canStart(tenant, req.Component, req.Version)
	if notFound != "" {
		writeUpgradeError(w, notFound, "NotFound", http.StatusNotFound)
		return
	}

	var spec state.TenantSpec
	_, _ = a.store.GetResource("tenant", tenant, &spec, nil)
	resp := preCheckResponse{CanUpgrade: ok, Reasons: reasons}
	currentVersion := spec.TalosVersion
	if req.Component == "kubernetes" {
		currentVersion = spec.KubernetesVersion
	}
	if currentVersion != "" && currentVersion > req.Version {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("downgrade from %s to %s", currentVersion, req.Version))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type upgradeStatusResponse struct {
	Status Status `json:"status"`
}

// Start starts an upgrade run for a tenant.
func (a *API) Start(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")

	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeUpgradeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Component != "talos" && req.Component != "kubernetes" {
		writeUpgradeError(w, "component must be 'talos' or 'kubernetes'", "BadRequest", http.StatusBadRequest)
		return
	}
	if req.Version == "" {
		writeUpgradeError(w, "version is required", "BadRequest", http.StatusBadRequest)
		return
	}

	pc := preCheckResponse{}
	if ok, reasons, notFound := a.canStart(tenant, req.Component, req.Version); !ok {
		if notFound != "" {
			writeUpgradeError(w, notFound, "NotFound", http.StatusNotFound)
			return
		}
		pc.CanUpgrade = false
		pc.Reasons = reasons
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(pc)
		return
	}

	run, err := a.manager.StartRun(tenant, req.Component, req.Version, "api")
	if err != nil {
		writeUpgradeError(w, err.Error(), "BadRequest", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(runResponse{Run: run})
}

// ListRuns returns upgrade runs for a tenant.
func (a *API) ListRuns(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")
	runs, err := a.manager.ListRuns(tenant)
	if err != nil {
		writeUpgradeError(w, err.Error(), "InternalError", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runListResponse{Items: runs, Total: len(runs)})
}

// GetRun returns a single upgrade run.
func (a *API) GetRun(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")
	id := r.PathValue("id")
	run, err := a.manager.GetRun(tenant, id)
	if err != nil {
		writeUpgradeError(w, err.Error(), "InternalError", http.StatusInternalServerError)
		return
	}
	if run == nil {
		writeUpgradeError(w, "upgrade run not found", "NotFound", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runResponse{Run: run})
}

// Cancel cancels a running upgrade run.
func (a *API) Cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.manager.CancelRun(id); err != nil {
		writeUpgradeError(w, err.Error(), "BadRequest", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetStatus returns the current upgrade status for a tenant.
func (a *API) GetStatus(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")

	// Verify tenant exists.
	var spec state.TenantSpec
	meta, err := a.store.GetResource("tenant", tenant, &spec, nil)
	if err != nil || meta.Name == "" {
		writeUpgradeError(w, "tenant not found", "NotFound", http.StatusNotFound)
		return
	}

	status := Status{
		Phase:     PhaseIdle,
		Component: "",
		Target:    "",
		Current:   spec.TalosVersion,
	}

	runs, err := a.manager.ListRuns(tenant)
	if err == nil && len(runs) > 0 {
		status = runs[0].Status.Status
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(upgradeStatusResponse{Status: status})
}

func (a *API) canStart(tenant, component, version string) (bool, []string, string) {
	// Verify tenant exists.
	var spec state.TenantSpec
	md, err := a.store.GetResource("tenant", tenant, &spec, nil)
	if err != nil || md.Name == "" {
		return false, nil, "tenant not found"
	}

	reasons := make([]string, 0)

	// Check machines exist.
	machOpts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + tenant}
	_, _, _, total, _ := a.store.ListResources("machine", machOpts)
	if total == 0 {
		reasons = append(reasons, "no machines in tenant")
	}

	currentVersion := spec.TalosVersion
	if component == "kubernetes" {
		currentVersion = spec.KubernetesVersion
	}
	if currentVersion == version {
		reasons = append(reasons, fmt.Sprintf("already at version %s", version))
	}

	return len(reasons) == 0, reasons, ""
}

func writeUpgradeError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
