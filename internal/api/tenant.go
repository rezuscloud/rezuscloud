// Package api provides HTTP handlers for the management plane REST API.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TenantAPI handles tenant CRUD operations.
type TenantAPI struct {
	store *state.Store
}

// NewTenantAPI creates a tenant API handler.
func NewTenantAPI(store *state.Store) *TenantAPI {
	return &TenantAPI{store: store}
}

// RegisterRoutes registers tenant API routes on the given mux.
func (a *TenantAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tenants", a.Create)
	mux.HandleFunc("GET /api/v1/tenants", a.List)
	mux.HandleFunc("GET /api/v1/tenants/{name}", a.Get)
	mux.HandleFunc("PUT /api/v1/tenants/{name}", a.Update)
	mux.HandleFunc("DELETE /api/v1/tenants/{name}", a.Delete)
	mux.HandleFunc("PUT /api/v1/tenants/{name}/status", a.UpdateStatus)
}

// CreateTenantRequest is the JSON body for creating a tenant.
type CreateTenantRequest struct {
	Metadata state.Metadata   `json:"metadata"`
	Spec     state.TenantSpec `json:"spec"`
}

// TenantResponse is the JSON response for a tenant.
type TenantResponse struct {
	Metadata state.Metadata     `json:"metadata"`
	Spec     state.TenantSpec   `json:"spec"`
	Status   state.TenantStatus `json:"status"`
}

// TenantListResponse is the JSON response for listing tenants.
type TenantListResponse struct {
	Items []TenantResponse `json:"items"`
	Total int              `json:"total"`
}

// ErrorResponse is the structured error shape.
type ErrorResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

func writeError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{
		Status:  "failure",
		Message: message,
		Reason:  reason,
		Code:    code,
	})
}

func tenantToResponse(t *state.Tenant) TenantResponse {
	return TenantResponse{
		Metadata: t.Metadata,
		Spec:     t.Spec,
		Status:   t.Status,
	}
}

// Create handles POST /api/v1/tenants.
func (a *TenantAPI) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Metadata.Name == "" {
		writeError(w, "metadata.name is required", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Spec.KubernetesVersion == "" {
		req.Spec.KubernetesVersion = "1.35.0"
	}

	// Check if tenant already exists.
	existing, err := a.store.GetTenant(req.Metadata.Name)
	if err != nil {
		writeError(w, fmt.Sprintf("lookup failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		writeError(w, fmt.Sprintf("tenant %q already exists", req.Metadata.Name), "Conflict", http.StatusConflict)
		return
	}

	tenant, err := a.store.CreateTenant(req.Metadata.Name, req.Spec, req.Metadata.Labels, req.Metadata.Annotations)
	if err != nil {
		writeError(w, fmt.Sprintf("create failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tenantToResponse(tenant))
}

// List handles GET /api/v1/tenants.
func (a *TenantAPI) List(w http.ResponseWriter, r *http.Request) {
	tenants, total, err := a.store.ListTenants()
	if err != nil {
		writeError(w, fmt.Sprintf("list failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	responses := make([]TenantResponse, 0, len(tenants))
	for _, t := range tenants {
		responses = append(responses, tenantToResponse(t))
	}

	if responses == nil {
		responses = []TenantResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TenantListResponse{
		Items: responses,
		Total: total,
	})
}

// Get handles GET /api/v1/tenants/{name}.
func (a *TenantAPI) Get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	tenant, err := a.store.GetTenant(name)
	if err != nil {
		writeError(w, fmt.Sprintf("get failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if tenant == nil {
		writeError(w, fmt.Sprintf("tenant %q not found", name), "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tenantToResponse(tenant))
}

// Update handles PUT /api/v1/tenants/{name}.
func (a *TenantAPI) Update(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req struct {
		Metadata state.Metadata   `json:"metadata"`
		Spec     state.TenantSpec `json:"spec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Metadata.ResourceVersion == 0 {
		writeError(w, "metadata.resourceVersion is required for updates", "BadRequest", http.StatusBadRequest)
		return
	}

	tenant, err := a.store.UpdateTenantSpec(name, req.Metadata.ResourceVersion, req.Spec, req.Metadata.Labels, req.Metadata.Annotations)
	if err == state.ErrConflict {
		writeError(w, "the object has been modified; please apply your changes to the latest version", "Conflict", http.StatusConflict)
		return
	}
	if err == state.ErrNotFound {
		writeError(w, fmt.Sprintf("tenant %q not found", name), "NotFound", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, fmt.Sprintf("update failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tenantToResponse(tenant))
}

// UpdateStatus handles PUT /api/v1/tenants/{name}/status.
func (a *TenantAPI) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req struct {
		Status state.TenantStatus `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	tenant, err := a.store.UpdateTenantStatus(name, req.Status)
	if err == state.ErrNotFound {
		writeError(w, fmt.Sprintf("tenant %q not found", name), "NotFound", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, fmt.Sprintf("status update failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tenantToResponse(tenant))
}

// Delete handles DELETE /api/v1/tenants/{name}.
func (a *TenantAPI) Delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := a.store.DeleteTenant(name)
	if err == state.ErrNotFound {
		writeError(w, fmt.Sprintf("tenant %q not found", name), "NotFound", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, fmt.Sprintf("delete failed: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	// Return the tenant with deletionTimestamp and finalizers set.
	tenant, _ := a.store.GetTenant(name)
	if tenant != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tenantToResponse(tenant))
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}
