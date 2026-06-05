package auth

import (
	"encoding/json"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// UserHandlers provides HTTP handlers for user management.
type UserHandlers struct {
	store *state.Store
}

// NewUserHandlers creates user management handlers.
func NewUserHandlers(store *state.Store) *UserHandlers {
	return &UserHandlers{store: store}
}

// RegisterRoutes registers user management routes on the given mux.
func (h *UserHandlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/users", h.List)
	mux.HandleFunc("POST /api/v1/users", h.Create)
	mux.HandleFunc("GET /api/v1/users/{name}", h.Get)
	mux.HandleFunc("PUT /api/v1/users/{name}", h.Update)
	mux.HandleFunc("DELETE /api/v1/users/{name}", h.Delete)
}

// UserResponse is the JSON response for a user.
type UserResponse struct {
	Metadata state.Metadata   `json:"metadata"`
	Spec     UserSpecPublic   `json:"spec"`
	Status   state.UserStatus `json:"status"`
}

// UserSpecPublic is the user spec without the password hash.
type UserSpecPublic struct {
	Role string `json:"role"`
}

// UserListResponse is the JSON response for listing users.
type UserListResponse struct {
	Items []UserResponse `json:"items"`
	Total int            `json:"total"`
}

// CreateUserRequest is the JSON body for creating a user.
type CreateUserRequest struct {
	Metadata state.Metadata `json:"metadata"`
	Spec     struct {
		Role     string `json:"role"`
		Password string `json:"password"`
	} `json:"spec"`
}

// UpdateUserRequest is the JSON body for updating a user.
type UpdateUserRequest struct {
	Metadata state.Metadata `json:"metadata"`
	Spec     struct {
		Role     string `json:"role"`
		Password string `json:"password,omitempty"`
	} `json:"spec"`
}

func userToResponse(u *state.User) UserResponse {
	return UserResponse{
		Metadata: u.Metadata,
		Spec:     UserSpecPublic{Role: u.Spec.Role},
		Status:   u.Status,
	}
}

// Create handles POST /api/v1/users.
func (h *UserHandlers) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if req.Metadata.Name == "" {
		writeJSONError(w, "metadata.name is required", "BadRequest", http.StatusBadRequest)
		return
	}
	if req.Spec.Password == "" {
		writeJSONError(w, "spec.password is required", "BadRequest", http.StatusBadRequest)
		return
	}
	if !ValidRoles[req.Spec.Role] {
		writeJSONError(w, "spec.role must be one of: view, edit, admin", "BadRequest", http.StatusBadRequest)
		return
	}

	// Check if user already exists.
	existing, err := h.store.GetUser(req.Metadata.Name)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		writeJSONError(w, "user already exists", "Conflict", http.StatusConflict)
		return
	}

	passwordHash, err := HashPassword(req.Spec.Password)
	if err != nil {
		writeJSONError(w, "password hashing failed", "InternalError", http.StatusInternalServerError)
		return
	}

	user, err := h.store.CreateUser(req.Metadata.Name, state.UserSpec{
		Role:         req.Spec.Role,
		PasswordHash: passwordHash,
	})
	if err != nil {
		writeJSONError(w, "create failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(userToResponse(user))
}

// List handles GET /api/v1/users.
func (h *UserHandlers) List(w http.ResponseWriter, _ *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil {
		writeJSONError(w, "list failed", "InternalError", http.StatusInternalServerError)
		return
	}

	items := make([]UserResponse, 0, len(users))
	for _, u := range users {
		items = append(items, userToResponse(u))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(UserListResponse{Items: items, Total: len(items)})
}

// Get handles GET /api/v1/users/{name}.
func (h *UserHandlers) Get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	user, err := h.store.GetUser(name)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSONError(w, "user not found", "NotFound", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(userToResponse(user))
}

// Update handles PUT /api/v1/users/{name}.
func (h *UserHandlers) Update(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid json", "BadRequest", http.StatusBadRequest)
		return
	}

	if !ValidRoles[req.Spec.Role] {
		writeJSONError(w, "spec.role must be one of: view, edit, admin", "BadRequest", http.StatusBadRequest)
		return
	}

	existing, err := h.store.GetUser(name)
	if err != nil {
		writeJSONError(w, "internal error", "InternalError", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		writeJSONError(w, "user not found", "NotFound", http.StatusNotFound)
		return
	}

	// Keep existing password hash unless a new password is provided.
	passwordHash := existing.Spec.PasswordHash
	if req.Spec.Password != "" {
		passwordHash, err = HashPassword(req.Spec.Password)
		if err != nil {
			writeJSONError(w, "password hashing failed", "InternalError", http.StatusInternalServerError)
			return
		}
	}

	updated, err := h.store.UpdateUser(name, existing.Metadata.ResourceVersion, state.UserSpec{
		Role:         req.Spec.Role,
		PasswordHash: passwordHash,
	})
	if err != nil {
		writeJSONError(w, "update failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(userToResponse(updated))
}

// Delete handles DELETE /api/v1/users/{name}.
func (h *UserHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := h.store.DeleteUser(name)
	if err != nil {
		writeJSONError(w, "delete failed", "InternalError", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
