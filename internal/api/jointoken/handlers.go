// Package jointoken provides HTTP handlers for JoinToken CRUD.
// JoinTokens are tenant-scoped resources that map booting machines to node groups.
package jointoken

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// DefaultTokenExpiry is the default token validity duration.
const DefaultTokenExpiry = 24 * time.Hour

// API provides HTTP handlers for JoinToken operations.
type API struct {
	store *state.Store
}

// NewAPI creates a JoinToken API handler.
func NewAPI(store *state.Store) *API {
	return &API{store: store}
}

// RegisterRoutes registers join token routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/join-tokens", a.List)
	mux.HandleFunc("POST /api/v1/tenants/{tenant}/join-tokens", a.Create)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant}/join-tokens/{id}", a.Delete)
}

type createRequest struct {
	Spec joinTokenSpecRequest `json:"spec"`
}

type joinTokenSpecRequest struct {
	NodeGroup string `json:"nodeGroup"`
	SingleUse bool   `json:"singleUse"`
	ExpiresIn int    `json:"expiresInSeconds,omitempty"` // 0 = default
}

type createResponse struct {
	Token     string              `json:"token"`
	ExpiresAt time.Time           `json:"expiresAt"`
	Spec      state.JoinTokenSpec `json:"spec"`
}

type listResponse struct {
	Items []*state.JoinToken `json:"items"`
	Total int                `json:"total"`
}

// generateToken creates a cryptographically random token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create handles POST /api/v1/tenants/{tenant}/join-tokens.
func (a *API) Create(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Spec.NodeGroup == "" {
		writeError(w, "spec.nodeGroup is required", "BadRequest", http.StatusBadRequest)
		return
	}

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

	// Generate token.
	token, err := generateToken()
	if err != nil {
		writeError(w, "token generation failed", "InternalError", http.StatusInternalServerError)
		return
	}

	// Calculate expiry.
	expiry := DefaultTokenExpiry
	if req.Spec.ExpiresIn > 0 {
		expiry = time.Duration(req.Spec.ExpiresIn) * time.Second
	}

	spec := state.JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(expiry),
		SingleUse: req.Spec.SingleUse,
		NodeGroup: req.Spec.NodeGroup,
	}

	jt, err := a.store.CreateJoinToken(token, spec, tenant, req.Spec.NodeGroup)
	if err != nil {
		writeError(w, "create failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(createResponse{
		Token:     token,
		ExpiresAt: spec.ExpiresAt,
		Spec:      jt.Spec,
	})
}

// List handles GET /api/v1/tenants/{tenant}/join-tokens.
func (a *API) List(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")

	items, total, err := state.ListTypedByTenant(a.store, "jointoken", tenant,
		func(meta state.Metadata, specRaw, statusRaw json.RawMessage) (*state.JoinToken, error) {
			var spec state.JoinTokenSpec
			var status state.JoinTokenStatus
			_ = json.Unmarshal(specRaw, &spec)
			_ = json.Unmarshal(statusRaw, &status)
			return &state.JoinToken{Metadata: meta, Spec: spec, Status: status}, nil
		})
	if err != nil {
		writeError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{Items: items, Total: total})
}

// Delete handles DELETE /api/v1/tenants/{tenant}/join-tokens/{id}.
func (a *API) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check if token exists first.
	var spec state.JoinTokenSpec
	md, err := a.store.GetResource("jointoken", id, &spec, nil)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeError(w, "join token not found", "NotFound", http.StatusNotFound)
			return
		}
		writeError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}

	// Verify tenant ownership.
	tenant := r.PathValue("tenant")
	if md.Labels["rezuscloud.io/tenant"] != tenant {
		writeError(w, "join token not found", "NotFound", http.StatusNotFound)
		return
	}

	if err := a.store.RemoveResource("jointoken", id); err != nil {
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
