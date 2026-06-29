// Package patch provides HTTP handlers for ConfigPatch CRUD.
// ConfigPatches are tenant-scoped overlays applied to Talos machine configs.
package patch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"sigs.k8s.io/yaml"
)

// ConfigPatch represents a Talos config overlay.
type ConfigPatch struct {
	Metadata state.Metadata `json:"metadata"`
	Spec     PatchSpec      `json:"spec"`
}

// PatchSpec defines the patch content and when it applies.
type PatchSpec struct {
	// Patch is the YAML content of the Talos config patch.
	Patch string `json:"patch"`
	// Format is the patch format: "strategic" (default) or "json6902".
	Format string `json:"format,omitempty"`
	// TargetRole filters which machine types the patch applies to.
	// Empty means all roles. Values: "controlplane", "worker".
	TargetRole string `json:"targetRole,omitempty"`
	// Enabled controls whether the patch is active.
	Enabled bool `json:"enabled"`
}

// ValidFormat values.
var validFormats = map[string]bool{
	"strategic": true,
	"json6902":  true,
	"text":      true,
	"":          true, // defaults to strategic
}

// ValidTargetRole values.
var validTargetRoles = map[string]bool{
	"":             true,
	"all":          true,
	"controlplane": true,
	"worker":       true,
	"kernel":       true,
}

// API provides HTTP handlers for ConfigPatch CRUD.
type API struct {
	store state.StoreAPI
}

// NewAPI creates a ConfigPatch API handler.
func NewAPI(store state.StoreAPI) *API {
	return &API{store: store}
}

// RegisterRoutes registers patch routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/patches", a.List)
	mux.HandleFunc("POST /api/v1/tenants/{tenant}/patches", a.Create)
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/patches/{name}", a.Get)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant}/patches/{name}", a.Update)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant}/patches/{name}", a.Delete)
}

// --- Store helpers ---

func (a *API) storePatch(p *ConfigPatch) error {
	labels := map[string]string{
		"rezuscloud.io/tenant": p.Metadata.Labels["rezuscloud.io/tenant"],
	}
	if p.Spec.TargetRole != "" {
		labels["rezuscloud.io/role"] = p.Spec.TargetRole
	}
	for k, v := range p.Metadata.Labels {
		if k != "rezuscloud.io/tenant" && k != "rezuscloud.io/role" {
			labels[k] = v
		}
	}

	meta, err := a.store.CreateResource("configpatch", p.Metadata.Name, p.Spec, nil, labels, p.Metadata.Annotations)
	if err != nil {
		return err
	}
	p.Metadata = meta
	return nil
}

func (a *API) getPatch(tenant, name string) (*ConfigPatch, error) {
	p := &ConfigPatch{}
	meta, err := a.store.GetResource("configpatch", name, &p.Spec, nil)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	p.Metadata = meta
	if meta.Labels["rezuscloud.io/tenant"] != tenant {
		return nil, nil
	}
	return p, nil
}

// --- Handlers ---

type createRequest struct {
	Metadata state.Metadata `json:"metadata"`
	Spec     PatchSpec      `json:"spec"`
}

type listResponse struct {
	Items []ConfigPatch `json:"items"`
	Total int           `json:"total"`
}

// Create handles POST /api/v1/tenants/{tenant}/patches.
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

	if req.Spec.Patch == "" {
		writeError(w, "spec.patch is required", "BadRequest", http.StatusBadRequest)
		return
	}

	if err := validatePatchSpec(req.Spec); err != nil {
		writeError(w, err.Error(), "BadRequest", http.StatusBadRequest)
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

	// Check if patch already exists.
	existing, err := a.getPatch(tenant, req.Metadata.Name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		writeError(w, "config patch already exists", "Conflict", http.StatusConflict)
		return
	}

	if req.Metadata.Labels == nil {
		req.Metadata.Labels = make(map[string]string)
	}
	req.Metadata.Labels["rezuscloud.io/tenant"] = tenant

	// Default format.
	if req.Spec.Format == "" {
		req.Spec.Format = "strategic"
	}

	p := &ConfigPatch{
		Metadata: req.Metadata,
		Spec:     req.Spec,
	}

	if err := a.storePatch(p); err != nil {
		writeError(w, "create failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(p)
}

// List handles GET /api/v1/tenants/{tenant}/patches.
func (a *API) List(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")

	items, total, err := state.ListTypedByTenant(a.store, "configpatch", tenant,
		func(meta state.Metadata, specRaw, statusRaw json.RawMessage) (ConfigPatch, error) {
			var p ConfigPatch
			p.Metadata = meta
			if err := json.Unmarshal(specRaw, &p.Spec); err != nil {
				return p, err
			}
			return p, nil
		})
	if err != nil {
		writeError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Items: items, Total: total})
}

// Get handles GET /api/v1/tenants/{tenant}/patches/{name}.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	p, err := a.getPatch(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if p == nil {
		writeError(w, "config patch not found", "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

type updateRequest struct {
	Metadata state.Metadata `json:"metadata"`
	Spec     PatchSpec      `json:"spec"`
}

// Update handles PUT /api/v1/tenants/{tenant}/patches/{name}.
func (a *API) Update(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Spec.Patch == "" {
		writeError(w, "spec.patch is required", "BadRequest", http.StatusBadRequest)
		return
	}

	if err := validatePatchSpec(req.Spec); err != nil {
		writeError(w, err.Error(), "BadRequest", http.StatusBadRequest)
		return
	}

	existing, err := a.getPatch(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		writeError(w, "config patch not found", "NotFound", http.StatusNotFound)
		return
	}

	// Preserve tenant label.
	labels := make(map[string]string)
	for k, v := range existing.Metadata.Labels {
		labels[k] = v
	}
	labels["rezuscloud.io/tenant"] = tenant
	if req.Spec.TargetRole != "" {
		labels["rezuscloud.io/role"] = req.Spec.TargetRole
	}

	// Default format.
	if req.Spec.Format == "" {
		req.Spec.Format = "strategic"
	}

	meta, err := a.store.UpdateResource("configpatch", name, existing.Metadata.ResourceVersion, req.Spec, labels, existing.Metadata.Annotations)
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			writeError(w, "conflict: resource version mismatch", "Conflict", http.StatusConflict)
			return
		}
		writeError(w, "update failed", "InternalError", http.StatusInternalServerError)
		return
	}

	updated := &ConfigPatch{
		Metadata: meta,
		Spec:     req.Spec,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// Delete handles DELETE /api/v1/tenants/{tenant}/patches/{name}.
func (a *API) Delete(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	p, err := a.getPatch(tenant, name)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if p == nil {
		writeError(w, "config patch not found", "NotFound", http.StatusNotFound)
		return
	}

	if err := a.store.RemoveResource("configpatch", name); err != nil {
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

func validatePatchSpec(spec PatchSpec) error {
	if strings.TrimSpace(spec.Patch) == "" {
		return fmt.Errorf("spec.patch must not be empty")
	}
	if !validFormats[spec.Format] {
		return fmt.Errorf("spec.format must be one of: strategic, json6902, text")
	}
	if !validTargetRoles[spec.TargetRole] {
		return fmt.Errorf("spec.targetRole must be one of: all, controlplane, worker, kernel, or empty")
	}

	switch spec.Format {
	case "", "strategic":
		var out any
		if err := yaml.Unmarshal([]byte(spec.Patch), &out); err != nil {
			return fmt.Errorf("invalid YAML for strategic patch: %v", err)
		}
	case "json6902":
		var ops []map[string]any
		if err := json.Unmarshal([]byte(spec.Patch), &ops); err != nil {
			return fmt.Errorf("invalid JSON for json6902 patch: %v", err)
		}
		if len(ops) == 0 {
			return fmt.Errorf("json6902 patch must contain at least one operation")
		}
	case "text":
		// No format validation for free-form text patches.
	}
	return nil
}
