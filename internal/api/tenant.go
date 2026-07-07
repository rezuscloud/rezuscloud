// Package api provides HTTP handlers for the management plane REST API.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/pagination"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/validation"
	"github.com/rezuscloud/rezuscloud/internal/watch"
)

// TenantAPI handles tenant CRUD operations.
type TenantAPI struct {
	store    state.StoreAPI
	bus      watch.Bus
	registry *validation.Registry
}

// NewTenantAPI creates a tenant API handler. bus and registry may be nil.
func NewTenantAPI(store state.StoreAPI, bus watch.Bus, registry *validation.Registry) *TenantAPI {
	if registry != nil {
		registry.RegisterFunc("tenant", validateTenantSpec)
	}
	return &TenantAPI{store: store, bus: bus, registry: registry}
}

// RegisterRoutes registers tenant API routes on the given mux.
func (a *TenantAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tenants", a.Create)
	mux.HandleFunc("GET /api/v1/tenants", a.List)
	mux.HandleFunc("GET /api/v1/tenants/{name}", a.Get)
	mux.HandleFunc("PUT /api/v1/tenants/{name}", a.Update)
	mux.HandleFunc("DELETE /api/v1/tenants/{name}", a.Delete)
	mux.HandleFunc("PUT /api/v1/tenants/{name}/status", a.UpdateStatus)
	mux.HandleFunc("GET /api/v1/tenants/{name}/kubeconfig", a.Kubeconfig)
	mux.HandleFunc("GET /api/v1/tenants/{name}/talosconfig", a.Talosconfig)
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
	Items              []TenantResponse `json:"items"`
	Total              int              `json:"total"`
	RemainingItemCount int              `json:"remainingItemCount,omitempty"`
}

// tenantsSnapshot lists existing tenants for the watch initial-state ADDED
// burst (#172). Each entry matches the wire shape the live events carry
// (metadata + spec + status).
func (a *TenantAPI) tenantsSnapshot() ([]any, error) {
	tenants, _, err := a.store.ListTenants()
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, tenantToResponse(t))
	}
	return out, nil
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

	if err := a.registry.Validate("tenant", req.Spec); err != nil {
		writeError(w, err.Error(), "BadRequest", http.StatusBadRequest)
		return
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

	// Auto-generate a secrets bundle so kubeconfig/talosconfig work immediately.
	// Failure here is non-fatal — the tenant exists, but credentials download
	// will return 404 until a bundle is added (via future credentials API).
	talosVersion := req.Spec.TalosVersion
	if talosVersion == "" {
		talosVersion = "1.12.0" // sensible default; matches default Kubernetes version 1.35.0.
	}
	bundle, err := credentials.GenerateSecretsBundle(talosVersion)
	if err == nil {
		bundleJSON, err := credentials.SecretsBundleJSON(bundle)
		if err == nil {
			_ = a.store.SaveTenantSecrets(req.Metadata.Name, bundleJSON)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tenantToResponse(tenant))
}

// List handles GET /api/v1/tenants.
func (a *TenantAPI) List(w http.ResponseWriter, r *http.Request) {
	// ?watch=true upgrades to an SSE stream of tenant change events (#172).
	if r.URL.Query().Get("watch") == "true" {
		if a.bus == nil {
			writeError(w, "watch not available", "ServiceUnavailable", http.StatusServiceUnavailable)
			return
		}
		watch.ServeWatch(w, r, a.bus, "tenant", watch.WatchOptions{
			InitialState: a.tenantsSnapshot,
		})
		return
	}

	pg := pagination.Parse(r)
	tenants, total, err := a.store.ListTenants(state.WithLimit(pg.Limit), state.WithOffset(pg.Offset))
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
		Items:              responses,
		Total:              total,
		RemainingItemCount: pagination.RemainingItemCount(total, pg.Offset, len(responses)),
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

	// Return the tenant with deletionTimestamp and finalizers set. 202 Accepted
	// signals the deletion is asynchronous (finalizers block immediate GC) —
	// the controller runs tofu destroy, then clears finalizers (#171).
	tenant, _ := a.store.GetTenant(name)
	if tenant != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(tenantToResponse(tenant))
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// Kubeconfig handles GET /api/v1/tenants/{name}/kubeconfig.
// Returns a YAML admin kubeconfig for the tenant.
func (a *TenantAPI) Kubeconfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	tenant, err := a.store.GetTenant(name)
	if err != nil {
		writeError(w, fmt.Sprintf("get tenant: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if tenant == nil {
		writeError(w, fmt.Sprintf("tenant %q not found", name), "NotFound", http.StatusNotFound)
		return
	}

	bundleJSON, err := a.store.LoadTenantSecrets(name)
	if err != nil {
		writeError(w, fmt.Sprintf("load secrets: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if bundleJSON == nil {
		writeError(w, "no secrets bundle for tenant; recreate the tenant or wait for the controller", "NotFound", http.StatusNotFound)
		return
	}

	bundle, err := credentials.UnmarshalSecretsBundle(bundleJSON)
	if err != nil {
		writeError(w, fmt.Sprintf("unmarshal secrets: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	kc, err := credentials.GenerateKubeconfig(credentials.KubeconfigRequest{
		ClusterName:     name,
		ClusterEndpoint: tenant.Spec.ControlPlaneEndpoint,
		Bundle:          bundle,
	})
	if err != nil {
		writeError(w, fmt.Sprintf("generate kubeconfig: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+"-kubeconfig.yaml"))
	_, _ = w.Write(kc)
}

// Talosconfig handles GET /api/v1/tenants/{name}/talosconfig.
// Returns a YAML admin talosconfig for the tenant.
func (a *TenantAPI) Talosconfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	tenant, err := a.store.GetTenant(name)
	if err != nil {
		writeError(w, fmt.Sprintf("get tenant: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if tenant == nil {
		writeError(w, fmt.Sprintf("tenant %q not found", name), "NotFound", http.StatusNotFound)
		return
	}

	bundleJSON, err := a.store.LoadTenantSecrets(name)
	if err != nil {
		writeError(w, fmt.Sprintf("load secrets: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}
	if bundleJSON == nil {
		writeError(w, "no secrets bundle for tenant; recreate the tenant or wait for the controller", "NotFound", http.StatusNotFound)
		return
	}

	bundle, err := credentials.UnmarshalSecretsBundle(bundleJSON)
	if err != nil {
		writeError(w, fmt.Sprintf("unmarshal secrets: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	tc, err := credentials.GenerateTalosconfig(credentials.TalosconfigRequest{
		ClusterName: name,
		Endpoint:    tenant.Spec.ControlPlaneEndpoint,
		Bundle:      bundle,
	})
	if err != nil {
		writeError(w, fmt.Sprintf("generate talosconfig: %v", err), "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+"-talosconfig.yaml"))
	_, _ = w.Write(tc)
}
