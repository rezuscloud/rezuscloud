// Package machine provides HTTP handlers for Machine CRUD.
// Machines are cluster-wide resources representing physical or virtual nodes.
package machine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/talosconfig"
)

// API provides HTTP handlers for Machine CRUD.
type API struct {
	store *state.Store
}

// NewAPI creates a Machine API handler.
func NewAPI(store *state.Store) *API {
	return &API{store: store}
}

// RegisterRoutes registers machine routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	// Cluster-wide (includes unassigned machines).
	mux.HandleFunc("GET /api/v1/machines", a.List)
	mux.HandleFunc("GET /api/v1/machines/{id}", a.Get)

	// Tenant-scoped machines.
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/machines", a.ListByTenant)
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/machines/{id}", a.GetByTenant)
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/machines/{id}/config", a.Config)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant}/machines/{id}/status", a.UpdateStatus)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant}/machines/{id}", a.Delete)
}

type listResponse struct {
	Items []*state.Machine `json:"items"`
	Total int              `json:"total"`
}

// List handles GET /api/v1/machines (all machines, including unassigned).
func (a *API) List(w http.ResponseWriter, _ *http.Request) {
	machines, total, err := a.store.ListMachines()
	if err != nil {
		writeError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Items: machines, Total: total})
}

// Get handles GET /api/v1/machines/{id}.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	machine, err := a.store.GetMachine(id)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if machine == nil {
		writeError(w, "machine not found", "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(machine)
}

// ListByTenant handles GET /api/v1/tenants/{tenant}/machines.
func (a *API) ListByTenant(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")

	// Verify tenant exists.
	t, err := a.store.GetTenant(tenant)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if t == nil {
		writeError(w, "tenant not found", "NotFound", http.StatusNotFound)
		return
	}

	machines, total, err := a.store.ListMachinesByTenant(tenant)
	if err != nil {
		writeError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Items: machines, Total: total})
}

// GetByTenant handles GET /api/v1/tenants/{tenant}/machines/{id}.
func (a *API) GetByTenant(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	id := r.PathValue("id")

	machine, err := a.store.GetMachine(id)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if machine == nil {
		writeError(w, "machine not found", "NotFound", http.StatusNotFound)
		return
	}

	// Verify tenant ownership.
	if machine.Metadata.Labels["rezuscloud.io/tenant"] != tenant {
		writeError(w, "machine not found in tenant", "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(machine)
}

type statusRequest struct {
	Status state.MachineStatus `json:"status"`
}

// UpdateStatus handles PUT /api/v1/tenants/{tenant}/machines/{id}/status.
func (a *API) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	id := r.PathValue("id")

	// Verify machine exists and belongs to tenant.
	machine, err := a.store.GetMachine(id)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if machine == nil {
		writeError(w, "machine not found", "NotFound", http.StatusNotFound)
		return
	}
	if machine.Metadata.Labels["rezuscloud.io/tenant"] != tenant {
		writeError(w, "machine not found in tenant", "NotFound", http.StatusNotFound)
		return
	}

	var req statusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	updated, err := a.store.UpdateMachineStatus(id, req.Status)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeError(w, "machine not found", "NotFound", http.StatusNotFound)
			return
		}
		writeError(w, "status update failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// Config handles GET /api/v1/tenants/{tenant}/machines/{id}/config.
// Returns the generated Talos machine config YAML for this machine.
func (a *API) Config(w http.ResponseWriter, r *http.Request) {
	tenantName := r.PathValue("tenant")
	id := r.PathValue("id")

	// Look up machine + verify tenant.
	m, err := a.store.GetMachine(id)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if m == nil {
		writeError(w, "machine not found", "NotFound", http.StatusNotFound)
		return
	}
	if m.Metadata.Labels["rezuscloud.io/tenant"] != tenantName {
		writeError(w, "machine not found in tenant", "NotFound", http.StatusNotFound)
		return
	}

	// Look up tenant + secrets.
	tenant, err := a.store.GetTenant(tenantName)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if tenant == nil {
		writeError(w, "tenant not found", "NotFound", http.StatusNotFound)
		return
	}

	bundleJSON, err := a.store.LoadTenantSecrets(tenantName)
	if err != nil {
		writeError(w, fmt.Sprintf("load secrets: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if bundleJSON == nil {
		writeError(w, "no secrets bundle found for tenant — create the tenant or generate credentials", "NotFound", http.StatusNotFound)
		return
	}

	// Determine machine type from role.
	machineType := talosconfig.DetermineMachineType(m.Status.Role, false)

	// Collect config patches for this role.
	patches, err := patch.ResolvePatches(a.store, tenantName, m.Status.Role)
	if err != nil {
		writeError(w, fmt.Sprintf("resolve patches: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	result, err := talosconfig.GenerateConfig(talosconfig.ConfigRequest{
		ClusterName:       tenantName,
		ClusterEndpoint:   tenant.Spec.ControlPlaneEndpoint,
		KubernetesVersion: tenant.Spec.KubernetesVersion,
		TalosVersion:      tenant.Spec.TalosVersion,
		MachineType:       machineType,
		SecretsBundle:     bundleJSON,
		ConfigPatches:     patches,
		MachineID:         id,
	})
	if err != nil {
		writeError(w, fmt.Sprintf("generate config: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	// Honour ?download=true to send as attachment.
	if r.URL.Query().Get("download") == "true" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+"-config.yaml"))
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(result.MachineConfig))
}

// Delete handles DELETE /api/v1/tenants/{tenant}/machines/{id}.
func (a *API) Delete(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	id := r.PathValue("id")

	// Verify machine exists and belongs to tenant.
	machine, err := a.store.GetMachine(id)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if machine == nil {
		writeError(w, "machine not found", "NotFound", http.StatusNotFound)
		return
	}
	if machine.Metadata.Labels["rezuscloud.io/tenant"] != tenant {
		writeError(w, "machine not found in tenant", "NotFound", http.StatusNotFound)
		return
	}

	if err := a.store.DeleteMachine(id); err != nil {
		writeError(w, "delete failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
