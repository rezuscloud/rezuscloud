package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserHandlers_CRUD(t *testing.T) {
	store, _ := setupAuthTest(t)
	handlers := NewUserHandlers(store)

	// Create.
	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]string{"name": "alice"},
		"spec":     map[string]string{"role": "edit", "password": "secret123"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handlers.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want %d, body = %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var created UserResponse
	_ = json.NewDecoder(w.Body).Decode(&created)
	if created.Metadata.Name != "alice" {
		t.Errorf("name = %q, want %q", created.Metadata.Name, "alice")
	}
	if created.Spec.Role != RoleEdit {
		t.Errorf("role = %q, want %q", created.Spec.Role, RoleEdit)
	}

	// Password hash should never appear in response.
	rawBody := w.Body.String()
	if contains(rawBody, "passwordHash") || contains(rawBody, "$2a$") {
		t.Error("response should not contain password hash")
	}

	// Get.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users/alice", nil)
	req.SetPathValue("name", "alice")
	w = httptest.NewRecorder()
	handlers.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want %d", w.Code, http.StatusOK)
	}

	// List.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	w = httptest.NewRecorder()
	handlers.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want %d", w.Code, http.StatusOK)
	}

	var list UserListResponse
	_ = json.NewDecoder(w.Body).Decode(&list)
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}

	// Update role.
	body, _ = json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": created.Metadata.ResourceVersion},
		"spec":     map[string]string{"role": "view", "password": "newsecret"},
	})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/users/alice", bytes.NewReader(body))
	req.SetPathValue("name", "alice")
	w = httptest.NewRecorder()
	handlers.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var updated UserResponse
	_ = json.NewDecoder(w.Body).Decode(&updated)
	if updated.Spec.Role != RoleView {
		t.Errorf("role = %q, want %q", updated.Spec.Role, RoleView)
	}

	// Verify password was changed by attempting login.
	authHandlers := NewAuthHandlers(store, NewJWTManager("test-secret"))
	loginBody, _ := json.Marshal(LoginRequest{Username: "alice", Password: "newsecret"})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()
	authHandlers.Login(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Errorf("login after password change: status = %d, want %d", loginW.Code, http.StatusOK)
	}

	// Delete.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/users/alice", nil)
	req.SetPathValue("name", "alice")
	w = httptest.NewRecorder()
	handlers.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d, want %d", w.Code, http.StatusNoContent)
	}

	// Verify deleted.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users/alice", nil)
	req.SetPathValue("name", "alice")
	w = httptest.NewRecorder()
	handlers.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("get after delete: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUserHandlers_CreateDuplicate(t *testing.T) {
	store, _ := setupAuthTest(t)
	handlers := NewUserHandlers(store)

	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]string{"name": "dup"},
		"spec":     map[string]string{"role": "view", "password": "pass"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlers.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(body))
	w = httptest.NewRecorder()
	handlers.Create(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestUserHandlers_CreateInvalidRole(t *testing.T) {
	store, _ := setupAuthTest(t)
	handlers := NewUserHandlers(store)

	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]string{"name": "bad"},
		"spec":     map[string]string{"role": "superadmin", "password": "pass"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlers.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUserHandlers_CreateMissingPassword(t *testing.T) {
	store, _ := setupAuthTest(t)
	handlers := NewUserHandlers(store)

	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]string{"name": "nopass"},
		"spec":     map[string]string{"role": "view"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlers.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMiddleware_Authenticate_Valid(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	user := createTestUser(t, store, "testuser", RoleEdit, "pass")
	token, _ := jwtMgr.GenerateToken(user, DefaultTokenExpiry)

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		if UserFromContext(r.Context()) != "testuser" {
			t.Error("user not in context")
		}
		if RoleFromContext(r.Context()) != RoleEdit {
			t.Error("role not in context")
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	w := httptest.NewRecorder()

	Authenticate(jwtMgr, next).ServeHTTP(w, req)

	if !called {
		t.Error("next handler should be called")
	}
}

func TestMiddleware_Authenticate_NoHeader(t *testing.T) {
	_, jwtMgr := setupAuthTest(t)

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	Authenticate(jwtMgr, next).ServeHTTP(w, req)

	if called {
		t.Error("next handler should not be called")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_Authenticate_InvalidToken(t *testing.T) {
	_, jwtMgr := setupAuthTest(t)

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()

	Authenticate(jwtMgr, next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_RequireRole(t *testing.T) {
	tests := []struct {
		name         string
		userRole     string
		allowedRoles []string
		expectStatus int
	}{
		{"admin can access anything", RoleAdmin, []string{RoleView}, http.StatusOK},
		{"edit accessing edit", RoleEdit, []string{RoleEdit}, http.StatusOK},
		{"view accessing edit", RoleView, []string{RoleEdit}, http.StatusForbidden},
		{"view accessing view", RoleView, []string{RoleView}, http.StatusOK},
		{"no role", "", []string{RoleView}, http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			handler := RequireRole(tt.allowedRoles...)(next)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.userRole != "" {
				ctx := context.WithValue(req.Context(), contextKey("user"), "test")
				ctx = context.WithValue(ctx, contextKey("role"), tt.userRole)
				req = req.WithContext(ctx)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.expectStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.expectStatus)
			}
		})
	}
}
