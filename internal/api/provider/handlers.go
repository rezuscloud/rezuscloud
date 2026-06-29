// Package provider provides HTTP handlers for Provider CRUD.
// Providers connect outbound via gRPC and register themselves.
package provider

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// API provides HTTP handlers for Provider operations.
type API struct {
	store state.StoreAPI
}

// NewAPI creates a Provider API handler.
func NewAPI(store state.StoreAPI) *API {
	return &API{store: store}
}

// RegisterRoutes registers provider routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/providers", a.List)
	mux.HandleFunc("GET /api/v1/providers/{type}", a.Get)
	mux.HandleFunc("PUT /api/v1/providers/{type}/status", a.UpdateStatus)
}

type listResponse struct {
	Items []*state.Provider `json:"items"`
	Total int               `json:"total"`
}

// List handles GET /api/v1/providers.
func (a *API) List(w http.ResponseWriter, _ *http.Request) {
	providers, err := a.store.ListProviders()
	if err != nil {
		writeError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Items: providers, Total: len(providers)})
}

// Get handles GET /api/v1/providers/{type}.
func (a *API) Get(w http.ResponseWriter, r *http.Request) {
	providerType := r.PathValue("type")

	provider, err := a.store.GetProvider(providerType)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if provider == nil {
		writeError(w, "provider not found", "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(provider)
}

type statusRequest struct {
	Status state.ProviderStatus `json:"status"`
}

// UpdateStatus handles PUT /api/v1/providers/{type}/status.
// Used by the provider gRPC bridge to update heartbeat and connection status.
func (a *API) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	providerType := r.PathValue("type")

	var req statusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	// Verify provider exists.
	existing, err := a.store.GetProvider(providerType)
	if err != nil {
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		writeError(w, "provider not found", "NotFound", http.StatusNotFound)
		return
	}

	_, err = a.store.UpsertProvider(providerType, existing.Spec, req.Status, existing.Metadata.Labels)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeError(w, "provider not found", "NotFound", http.StatusNotFound)
			return
		}
		writeError(w, "status update failed", "InternalError", http.StatusInternalServerError)
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
