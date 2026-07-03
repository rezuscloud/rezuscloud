package status

import (
	"encoding/json"
	"net/http"
)

// API exposes tenant health over HTTP (ADR 0016).
type API struct {
	gatherer *Gatherer
}

// NewAPI creates a status API. gatherer may be nil — routes return 503.
func NewAPI(gatherer *Gatherer) *API {
	return &API{gatherer: gatherer}
}

// RegisterRoutes registers health routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tenants/{name}/health", a.Health)
	mux.HandleFunc("GET /api/v1/health", a.HealthAll)
}

// Health handles GET /api/v1/tenants/{name}/health.
func (a *API) Health(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")
	if a.gatherer == nil {
		write503(w)
		return
	}
	h := a.gatherer.Gather(r.Context(), tenant)
	writeJSON(w, h)
}

// HealthAll handles GET /api/v1/health — fleet-wide tenant health.
func (a *API) HealthAll(w http.ResponseWriter, r *http.Request) {
	if a.gatherer == nil {
		write503(w)
		return
	}
	all := a.gatherer.GatherAll(r.Context())
	writeJSON(w, map[string]any{"items": all, "total": len(all)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func write503(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": "status gatherer not configured",
		"reason":  "ServiceUnavailable",
		"code":    http.StatusServiceUnavailable,
	})
}
