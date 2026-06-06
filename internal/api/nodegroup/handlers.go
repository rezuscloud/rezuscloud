// Package nodegroup provides HTTP handlers for NodeGroup CRUD.
// NodeGroups are tenant-scoped resources defining sets of machines
// with the same role and provider configuration.
package nodegroup

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// NodeGroup represents a group of machines within a tenant.
type NodeGroup struct {
	Metadata state.Metadata   `json:"metadata"`
	Spec     NodeGroupSpecAPI `json:"spec"`
	Status   NodeGroupStatus  `json:"status"`
}

// NodeGroupSpecAPI is the API-facing spec (no internal fields).
type NodeGroupSpecAPI struct {
	Count          int             `json:"count"`
	Role           string          `json:"role"`
	ProviderClass  string          `json:"providerClass,omitempty"`
	ProviderConfig json.RawMessage `json:"providerConfig,omitempty"`
	TalosVersion   string          `json:"talosVersion,omitempty"`
}

// NodeGroupStatus tracks the observed state of a node group.
type NodeGroupStatus struct {
	Phase         string `json:"phase"`
	ReadyMachines int    `json:"readyMachines"`
	TotalMachines int    `json:"totalMachines"`
}

// NodeGroupPhase constants.
const (
	PhaseForming   = "forming"
	PhaseActive    = "active"
	PhaseShrinking = "shrinking"
)

// ValidRole values.
var validRoles = map[string]bool{
	"controlplane": true,
	"worker":       true,
}

// API provides HTTP handlers for NodeGroup CRUD.
type API struct {
	store *state.Store
}

// NewAPI creates a NodeGroup API handler.
func NewAPI(store *state.Store) *API {
	return &API{store: store}
}

// RegisterRoutes registers node group routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/node-groups", a.List)
	mux.HandleFunc("POST /api/v1/tenants/{tenant}/node-groups", a.Create)
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/node-groups/{name}", a.Get)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant}/node-groups/{name}", a.Update)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant}/node-groups/{name}", a.Delete)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant}/node-groups/{name}/status", a.UpdateStatus)
}

// --- Store helpers ---

// storeNodeGroup persists a NodeGroup as a generic resource.
func (a *API) storeNodeGroup(ng *NodeGroup) error {
	labels := map[string]string{
		"rezuscloud.io/tenant": ng.Metadata.Labels["rezuscloud.io/tenant"],
		"rezuscloud.io/role":   ng.Spec.Role,
	}
	// Copy user labels.
	for k, v := range ng.Metadata.Labels {
		if k != "rezuscloud.io/tenant" && k != "rezuscloud.io/role" {
			labels[k] = v
		}
	}

	meta, err := a.store.CreateResource("nodegroup", ng.Metadata.Name, ng.Spec, ng.Status, labels, ng.Metadata.Annotations)
	if err != nil {
		return err
	}
	ng.Metadata = meta
	return nil
}

// getNodeGroup loads a NodeGroup from the store.
func (a *API) getNodeGroup(tenant, name string) (*NodeGroup, error) {
	ng := &NodeGroup{}
	labels := map[string]string{"rezuscloud.io/tenant": tenant}
	meta, err := a.store.GetResource("nodegroup", name, &ng.Spec, &ng.Status)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	ng.Metadata = meta

	// Verify tenant ownership.
	if meta.Labels["rezuscloud.io/tenant"] != tenant {
		return nil, nil
	}

	// We need to re-read with labels.
	_ = labels
	return ng, nil
}

// --- Handlers ---

type createRequest struct {
	Metadata state.Metadata   `json:"metadata"`
	Spec     NodeGroupSpecAPI `json:"spec"`
}

type listResponse struct {
	Items []NodeGroup `json:"items"`
	Total int         `json:"total"`
}

// Create handles POST /api/v1/tenants/{tenant}/node-groups.
func (a *API) Create(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Metadata.Name == "" {
		writeError(w, "metadata.name is required", "BadRequest", http.StatusBadRequest)
		return
	}

	if !validRoles[req.Spec.Role] {
		writeError(w, "spec.role must be 'controlplane' or 'worker'", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Spec.Count < 1 {
		writeError(w, "spec.count must be >= 1", "BadRequest", http.StatusBadRequest)
		return
	}

	// Verify tenant exists.
	t, err := a.store.GetTenant(tenant)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if t == nil {
		writeError(w, fmt.Sprintf("tenant %q not found", tenant), "NotFound", http.StatusNotFound)
		return
	}

	// Check if node group already exists.
	existing, err := a.getNodeGroup(tenant, req.Metadata.Name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		writeError(w, "node group already exists", "Conflict", http.StatusConflict)
		return
	}

	// Ensure tenant label.
	if req.Metadata.Labels == nil {
		req.Metadata.Labels = make(map[string]string)
	}
	req.Metadata.Labels["rezuscloud.io/tenant"] = tenant

	ng := &NodeGroup{
		Metadata: req.Metadata,
		Spec:     req.Spec,
		Status:   NodeGroupStatus{Phase: PhaseForming},
	}

	if err := a.storeNodeGroup(ng); err != nil {
		writeError(w, "create failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ng)
}

// List handles GET /api/v1/tenants/{tenant}/node-groups.
func (a *API) List(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")

	items, total, err := state.ListTypedByTenant(a.store, "nodegroup", tenant,
		func(meta state.Metadata, specRaw, statusRaw json.RawMessage) (NodeGroup, error) {
			var ng NodeGroup
			ng.Metadata = meta
			if err := json.Unmarshal(specRaw, &ng.Spec); err != nil {
				return ng, err
			}
			_ = json.Unmarshal(statusRaw, &ng.Status)
			return ng, nil
		})
	if err != nil {
		writeError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Items: items, Total: total})
}

// Get handles GET /api/v1/tenants/{tenant}/node-groups/{name}.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	ng, err := a.getNodeGroup(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if ng == nil {
		writeError(w, "node group not found", "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ng)
}

type updateRequest struct {
	Metadata state.Metadata   `json:"metadata"`
	Spec     NodeGroupSpecAPI `json:"spec"`
}

// Update handles PUT /api/v1/tenants/{tenant}/node-groups/{name}.
func (a *API) Update(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if !validRoles[req.Spec.Role] {
		writeError(w, "spec.role must be 'controlplane' or 'worker'", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Spec.Count < 1 {
		writeError(w, "spec.count must be >= 1", "BadRequest", http.StatusBadRequest)
		return
	}

	existing, err := a.getNodeGroup(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		writeError(w, "node group not found", "NotFound", http.StatusNotFound)
		return
	}

	// Update labels with new role.
	labels := make(map[string]string)
	for k, v := range existing.Metadata.Labels {
		labels[k] = v
	}
	labels["rezuscloud.io/role"] = req.Spec.Role
	labels["rezuscloud.io/tenant"] = tenant

	meta, err := a.store.UpdateResource("nodegroup", name, existing.Metadata.ResourceVersion, req.Spec, labels, existing.Metadata.Annotations)
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			writeError(w, "conflict: resource version mismatch", "Conflict", http.StatusConflict)
			return
		}
		writeError(w, "update failed", "InternalError", http.StatusInternalServerError)
		return
	}

	updated := &NodeGroup{
		Metadata: meta,
		Spec:     req.Spec,
		Status:   existing.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// Delete handles DELETE /api/v1/tenants/{tenant}/node-groups/{name}.
func (a *API) Delete(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	ng, err := a.getNodeGroup(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if ng == nil {
		writeError(w, "node group not found", "NotFound", http.StatusNotFound)
		return
	}

	if err := a.store.RemoveResource("nodegroup", name); err != nil {
		writeError(w, "delete failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type statusRequest struct {
	Status NodeGroupStatus `json:"status"`
}

// UpdateStatus handles PUT /api/v1/tenants/{tenant}/node-groups/{name}/status.
func (a *API) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	existing, err := a.getNodeGroup(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		writeError(w, "node group not found", "NotFound", http.StatusNotFound)
		return
	}

	var req statusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	_, err = a.store.UpdateStatus("nodegroup", name, req.Status)
	if err != nil {
		writeError(w, "status update failed", "InternalError", http.StatusInternalServerError)
		return
	}

	existing.Status = req.Status
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(existing)
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
