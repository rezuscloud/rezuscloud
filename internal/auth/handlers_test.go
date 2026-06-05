package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthHandlers_Login(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	createTestUser(t, store, "admin", RoleAdmin, "password123")

	handlers := NewAuthHandlers(store, jwtMgr)

	// Successful login.
	body, _ := json.Marshal(LoginRequest{Username: "admin", Password: "password123"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handlers.Login(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp LoginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Token.AccessToken == "" {
		t.Error("token should not be empty")
	}
	if resp.User.Name != "admin" {
		t.Errorf("user name = %q, want %q", resp.User.Name, "admin")
	}
	if resp.User.Role != RoleAdmin {
		t.Errorf("user role = %q, want %q", resp.User.Role, RoleAdmin)
	}
}

func TestAuthHandlers_Login_WrongPassword(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	createTestUser(t, store, "admin", RoleAdmin, "password123")

	handlers := NewAuthHandlers(store, jwtMgr)

	body, _ := json.Marshal(LoginRequest{Username: "admin", Password: "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handlers.Login(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthHandlers_Login_UserNotFound(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	handlers := NewAuthHandlers(store, jwtMgr)

	body, _ := json.Marshal(LoginRequest{Username: "nonexistent", Password: "pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handlers.Login(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthHandlers_Login_MissingFields(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	handlers := NewAuthHandlers(store, jwtMgr)

	body, _ := json.Marshal(LoginRequest{Username: "admin"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handlers.Login(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAuthHandlers_Whoami(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	handlers := NewAuthHandlers(store, jwtMgr)

	user := createTestUser(t, store, "editor", RoleEdit, "pass123")
	token, _ := jwtMgr.GenerateToken(user, DefaultTokenExpiry)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	w := httptest.NewRecorder()

	// Wrap with auth middleware to set context.
	authed := Authenticate(jwtMgr, http.HandlerFunc(handlers.Whoami))
	authed.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp WhoamiResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Username != "editor" {
		t.Errorf("username = %q, want %q", resp.Username, "editor")
	}
	if resp.Role != RoleEdit {
		t.Errorf("role = %q, want %q", resp.Role, RoleEdit)
	}
}

func TestAuthHandlers_Logout(t *testing.T) {
	store, jwtMgr := setupAuthTest(t)
	handlers := NewAuthHandlers(store, jwtMgr)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	w := httptest.NewRecorder()

	handlers.Logout(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}
