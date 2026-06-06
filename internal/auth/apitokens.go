package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// APITokenHandlers provides HTTP handlers for managing long-lived API tokens.
//
// Tokens are SHA-256 hashed at rest (never stored in plaintext). The plaintext
// secret is returned to the caller exactly once, at creation time, in the
// response body and (for browser flows) via a flash query parameter on the
// redirect target.
type APITokenHandlers struct {
	store *state.Store
}

// NewAPITokenHandlers creates API token handlers.
func NewAPITokenHandlers(store *state.Store) *APITokenHandlers {
	return &APITokenHandlers{store: store}
}

// RegisterRoutes registers API token routes on the given mux.
//
// Routes:
//
//	GET    /api/v1/users/{name}/api-tokens            — list tokens for a user (admin or owner)
//	GET    /api/v1/api-tokens                          — list all tokens (admin only)
//	POST   /api/v1/users/{name}/api-tokens             — create a token for {name}
//	DELETE /api/v1/api-tokens/{id}                      — revoke a token by id
//	GET    /api/v1/api-tokens/{id}                      — get token metadata (no secret)
func (h *APITokenHandlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/api-tokens", h.List)
	mux.HandleFunc("GET /api/v1/api-tokens/{id}", h.Get)
	mux.HandleFunc("DELETE /api/v1/api-tokens/{id}", h.Delete)
	mux.HandleFunc("GET /api/v1/users/{name}/api-tokens", h.ListForUser)
	mux.HandleFunc("POST /api/v1/users/{name}/api-tokens", h.CreateForUser)
}

// --- types ---

// APITokenResponse is the JSON shape returned for a token. The plaintext
// secret is included ONLY in the create response.
type APITokenResponse struct {
	ID        string  `json:"id"`
	UserName  string  `json:"userName"`
	Role      string  `json:"role"`
	ExpiresAt *int64  `json:"expiresAt,omitempty"`
	CreatedAt int64   `json:"createdAt"`
	LastUsed  *int64  `json:"lastUsed,omitempty"`
	Secret    *string `json:"secret,omitempty"` // populated only by Create
}

// APITokenListResponse is the list endpoint shape.
type APITokenListResponse struct {
	Items []APITokenResponse `json:"items"`
	Total int                `json:"total"`
}

// CreateAPITokenRequest is the JSON body for creating a token.
type CreateAPITokenRequest struct {
	ExpiresInDays int `json:"expiresInDays,omitempty"` // optional; 0 = no expiry
}

// CreateAPITokenResponse is the create endpoint shape. `secret` is the
// plaintext token shown exactly once.
type CreateAPITokenResponse struct {
	APITokenResponse
	Secret string `json:"secret"` // returned exactly once
}

// --- helpers ---

// GenerateAPIToken returns a freshly minted plaintext token plus its id and
// SHA-256 hash. The plaintext is `rez_<random>`; the hash is what we persist.
func GenerateAPIToken() (plaintext, id, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate token entropy: %w", err)
	}
	plaintext = "rez_" + hex.EncodeToString(buf)
	id = "tok_" + hex.EncodeToString(buf[:8])
	sum := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(sum[:])
	return plaintext, id, hash, nil
}

// HashAPIToken computes the SHA-256 hex digest of a plaintext API token. Used
// by the auth middleware to look up incoming Bearer credentials.
func HashAPIToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func tokenToResponse(t *state.APIToken, role string) APITokenResponse {
	resp := APITokenResponse{
		ID:        t.ID,
		UserName:  t.UserName,
		Role:      role,
		CreatedAt: t.CreatedAt.Unix(),
	}
	if t.ExpiresAt != nil {
		v := t.ExpiresAt.Unix()
		resp.ExpiresAt = &v
	}
	if t.LastUsed != nil {
		v := t.LastUsed.Unix()
		resp.LastUsed = &v
	}
	return resp
}

// --- handlers ---

// List handles GET /api/v1/api-tokens (admin only).
func (h *APITokenHandlers) List(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if role != RoleAdmin {
		writeAuthError(w, "admin role required", http.StatusForbidden)
		return
	}
	h.listTokens(w, "")
}

// ListForUser handles GET /api/v1/users/{name}/api-tokens.
// Owners can list their own tokens; admins can list any user's tokens.
func (h *APITokenHandlers) ListForUser(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("name")
	caller := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if role != RoleAdmin && caller != target {
		writeAuthError(w, "cannot list another user's tokens", http.StatusForbidden)
		return
	}
	h.listTokens(w, target)
}

func (h *APITokenHandlers) listTokens(w http.ResponseWriter, userName string) {
	tokens, err := h.store.ListAPITokens(userName)
	if err != nil {
		writeJSONError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	items := make([]APITokenResponse, 0, len(tokens))
	for _, t := range tokens {
		// Resolve current role from user record (denormalized at response time).
		role := ""
		if u, _ := h.store.GetUser(t.UserName); u != nil {
			role = u.Spec.Role
		}
		items = append(items, tokenToResponse(t, role))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(APITokenListResponse{Items: items, Total: len(items)})
}

// Get handles GET /api/v1/api-tokens/{id}. Returns metadata only (no secret).
func (h *APITokenHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tok, err := h.store.GetAPIToken(id)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if tok == nil {
		writeJSONError(w, "token not found", "NotFound", http.StatusNotFound)
		return
	}

	caller := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if role != RoleAdmin && caller != tok.UserName {
		writeAuthError(w, "forbidden", http.StatusForbidden)
		return
	}

	respRole := ""
	if u, _ := h.store.GetUser(tok.UserName); u != nil {
		respRole = u.Spec.Role
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tokenToResponse(tok, respRole))
}

// CreateForUser handles POST /api/v1/users/{name}/api-tokens. The plaintext
// secret is returned in the response body exactly once. Owners can mint their
// own tokens; admins can mint any user's tokens.
func (h *APITokenHandlers) CreateForUser(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("name")
	caller := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if role != RoleAdmin && caller != target {
		writeAuthError(w, "cannot create tokens for another user", http.StatusForbidden)
		return
	}

	// Verify target user exists and is active.
	user, err := h.store.GetUser(target)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSONError(w, "user not found", "NotFound", http.StatusNotFound)
		return
	}

	var req CreateAPITokenRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, "invalid json", "BadRequest", http.StatusBadRequest)
			return
		}
	}

	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().UTC().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	plaintext, id, hash, err := GenerateAPIToken()
	if err != nil {
		writeJSONError(w, "token generation failed", "InternalError", http.StatusInternalServerError)
		return
	}

	tok, err := h.store.CreateAPIToken(id, target, hash, expiresAt)
	if err != nil {
		writeJSONError(w, "create failed", "InternalError", http.StatusInternalServerError)
		return
	}

	resp := CreateAPITokenResponse{
		APITokenResponse: tokenToResponse(tok, user.Spec.Role),
		Secret:           plaintext,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// Delete handles DELETE /api/v1/api-tokens/{id}.
func (h *APITokenHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tok, err := h.store.GetAPIToken(id)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if tok == nil {
		writeJSONError(w, "token not found", "NotFound", http.StatusNotFound)
		return
	}

	caller := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if role != RoleAdmin && caller != tok.UserName {
		writeAuthError(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.store.DeleteAPIToken(id); err != nil {
		writeJSONError(w, "delete failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- middleware support ---

// VerifyAPIToken looks up a plaintext API token and returns the associated user
// (resolved fresh from the user store) plus a boolean indicating whether the
// token is currently valid (not expired). On any failure it returns (nil, false).
func VerifyAPIToken(store *state.Store, plaintext string) (*state.User, *state.APIToken, bool) {
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return nil, nil, false
	}
	hash := HashAPIToken(plaintext)
	tok, err := store.LookupAPITokenByHash(hash)
	if err != nil || tok == nil {
		return nil, nil, false
	}
	if tok.ExpiresAt != nil && time.Now().UTC().After(*tok.ExpiresAt) {
		return nil, nil, false
	}
	user, err := store.GetUser(tok.UserName)
	if err != nil || user == nil {
		return nil, nil, false
	}
	_ = store.TouchAPIToken(tok.ID) // best-effort last_used update
	return user, tok, true
}
