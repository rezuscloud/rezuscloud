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
}

// NewAPI creates an upgrade API handler.
func NewAPI(store *state.Store, upgrader *RollingUpgrader) *API {
	return &API{store: store, upgrader: upgrader}
}

// RegisterRoutes registers upgrade routes.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tenants/{name}/prechecks", a.PreCheck)
	mux.HandleFunc("GET /api/v1/tenants/{name}/upgrade-status", a.GetStatus)
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

	// Verify tenant exists.
	var spec state.TenantSpec
	_, err := a.store.GetResource("tenant", tenant, &spec, nil)
	if err != nil {
		writeUpgradeError(w, "tenant not found", "NotFound", http.StatusNotFound)
		return
	}

	resp := preCheckResponse{CanUpgrade: true}

	// Check machines exist.
	machOpts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + tenant}
	_, _, _, total, _ := a.store.ListResources("machine", machOpts)
	if total == 0 {
		resp.CanUpgrade = false
		resp.Reasons = append(resp.Reasons, "no machines in tenant")
	}

	// Version downgrade check.
	currentVersion := spec.TalosVersion
	if req.Component == "kubernetes" {
		currentVersion = spec.KubernetesVersion
	}
	if currentVersion != "" && currentVersion > req.Version {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("downgrade from %s to %s", currentVersion, req.Version))
	}

	// Already at target version.
	if currentVersion == req.Version {
		resp.CanUpgrade = false
		resp.Reasons = append(resp.Reasons, fmt.Sprintf("already at version %s", req.Version))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type upgradeStatusResponse struct {
	Status Status `json:"status"`
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

	// Check upgrade status from annotations.
	status := Status{
		Phase:     PhaseIdle,
		Component: "",
		Target:    "",
		Current:   spec.TalosVersion,
	}

	if us, ok := meta.Annotations["rezuscloud.io/upgrade-status"]; ok {
		_ = json.Unmarshal([]byte(us), &status)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(upgradeStatusResponse{Status: status})
}

func writeUpgradeError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
