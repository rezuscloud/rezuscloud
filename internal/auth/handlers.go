package auth

import (
	"encoding/json"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// AuthHandlers provides HTTP handlers for authentication.
type AuthHandlers struct {
	store      state.StoreAPI
	jwtManager *JWTManager
}

// NewAuthHandlers creates auth handlers.
func NewAuthHandlers(store state.StoreAPI, jwtManager *JWTManager) *AuthHandlers {
	return &AuthHandlers{store: store, jwtManager: jwtManager}
}

// RegisterRoutes registers auth routes on the given mux.
func (h *AuthHandlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.Login)
	mux.HandleFunc("POST /api/v1/auth/logout", h.Logout)
	// whoami is registered on the protected mux, not here.
}

// LoginRequest is the JSON body for login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse is the JSON response for successful login.
type LoginResponse struct {
	Token TokenPair `json:"token"`
	User  UserInfo  `json:"user"`
}

// UserInfo contains user information returned after login.
type UserInfo struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// Login handles POST /api/v1/auth/login.
func (h *AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSONError(w, "username and password are required", "BadRequest", http.StatusBadRequest)
		return
	}

	user, err := h.store.GetUser(req.Username)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSONError(w, "invalid credentials", "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !CheckPassword(req.Password, user.Spec.PasswordHash) {
		writeJSONError(w, "invalid credentials", "Unauthorized", http.StatusUnauthorized)
		return
	}

	token, err := h.jwtManager.GenerateToken(user, DefaultTokenExpiry)
	if err != nil {
		writeJSONError(w, "token generation failed", "InternalError", http.StatusInternalServerError)
		return
	}

	// Update last login.
	_ = h.store.UpdateUserLastLogin(req.Username)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LoginResponse{
		Token: token,
		User:  UserInfo{Name: user.Metadata.Name, Role: user.Spec.Role},
	})
}

// Logout handles POST /api/v1/auth/logout.
// For JWT-based auth, logout is client-side (discard the token).
// This endpoint exists for API compatibility.
func (h *AuthHandlers) Logout(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// WhoamiResponse is the JSON response for whoami.
type WhoamiResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Whoami handles GET /api/v1/auth/whoami.
func (h *AuthHandlers) Whoami(w http.ResponseWriter, r *http.Request) {
	username := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if username == "" {
		writeJSONError(w, "not authenticated", "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(WhoamiResponse{
		Username: username,
		Role:     role,
	})
}

// writeJSONError writes a structured JSON error response.
func writeJSONError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
