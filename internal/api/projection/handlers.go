// Package projection exposes the projected TF-state read model (ADR 0005) over
// the REST API. It is the spec-plane read surface: what TF state declared and
// created, projected through provider resource mappings.
//
// This is intentionally separate from the machine/nodegroup CRUD handlers,
// which still read from the store's resources table (management-plane data).
// The projection endpoint shows the infrastructure that tofu actually created —
// cloud instances (oci_core_instance → Machine), etc. — keyed by the TF state
// it was projected from. Over time, the two converge; for now this endpoint is
// the observable bridge that proves the projection pipeline works end-to-end.
package projection

import (
	"encoding/json"
	"net/http"

	proj "github.com/rezuscloud/rezuscloud/internal/projection"
)

// API exposes the projection index over HTTP.
type API struct {
	index *proj.Index
}

// NewAPI creates a projection API. index may be nil — in that case routes
// return 503 Service Unavailable, so the endpoint degrades gracefully when the
// projection subsystem isn't configured (e.g. in tests).
func NewAPI(index *proj.Index) *API {
	return &API{index: index}
}

// RegisterRoutes registers projection read routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/projected", a.ListByTenant)
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/projected/{kind}", a.ListByTenantKind)
	mux.HandleFunc("GET /api/v1/projected", a.ListAll)
}

type listResponse struct {
	Items []proj.Resource `json:"items"`
	Total int             `json:"total"`
}

// ListByTenant handles GET /api/v1/tenants/{tenant}/projected.
// Returns every projected resource for the tenant, optionally filtered by
// ?kind= (e.g. ?kind=Machine).
func (a *API) ListByTenant(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	kind := r.URL.Query().Get("kind")

	if a.index == nil {
		writeUnavailable(w)
		return
	}

	items := a.index.List(tenant, kind)
	if items == nil {
		items = []proj.Resource{}
	}
	writeJSON(w, listResponse{Items: items, Total: len(items)})
}

// ListByTenantKind handles GET /api/v1/tenants/{tenant}/projected/{kind}.
// Convenience path for a Kind filter (e.g. /projected/Machine).
func (a *API) ListByTenantKind(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	kind := r.PathValue("kind")

	if a.index == nil {
		writeUnavailable(w)
		return
	}

	items := a.index.List(tenant, kind)
	if items == nil {
		items = []proj.Resource{}
	}
	writeJSON(w, listResponse{Items: items, Total: len(items)})
}

// ListAll handles GET /api/v1/projected.
// Returns projected resources across all tenants, optionally filtered by
// ?kind=. Useful for a fleet-wide view of what TF created.
func (a *API) ListAll(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")

	if a.index == nil {
		writeUnavailable(w)
		return
	}

	// ListAll iterates every tenant the index knows about. The index doesn't
	// expose a tenant list directly, so we derive it from Tenants() (best
	// effort — empty if no applies have run yet).
	tenants := a.index.Tenants()
	var out []proj.Resource
	for _, t := range tenants {
		out = append(out, a.index.List(t, kind)...)
	}
	if out == nil {
		out = []proj.Resource{}
	}
	writeJSON(w, listResponse{Items: out, Total: len(out)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": "projection subsystem not configured",
		"reason":  "ServiceUnavailable",
		"code":    http.StatusServiceUnavailable,
	})
}
