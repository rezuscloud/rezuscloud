package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
)

func setupRouter(t *testing.T) (*state.Store, http.Handler, *auth.JWTManager) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jwtManager := auth.NewJWTManager("test-secret-key-for-testing")

	// Create a test admin user for authenticated requests.
	hash, _ := auth.HashPassword("test-password")
	_, _ = store.CreateUser("test-admin", state.UserSpec{
		Role:         auth.RoleAdmin,
		PasswordHash: hash,
	})

	return store, Router(store, jwtManager, audit.NewComponent(store.DB(), audit.ComponentOptions{}), nil, upgrade.NewManager(store), nil), jwtManager
}

func stringReader(s string) io.Reader {
	return strings.NewReader(s)
}

func getAuthToken(t *testing.T, jwtManager *auth.JWTManager) string {
	t.Helper()
	user := &state.User{
		Metadata: state.Metadata{Name: "test-admin"},
		Spec:     state.UserSpec{Role: auth.RoleAdmin},
	}
	token, err := jwtManager.GenerateToken(user, auth.DefaultTokenExpiry)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return token.AccessToken
}

func TestRouter_Login(t *testing.T) {
	_, handler, _ := setupRouter(t)

	// Login.
	body := `{"username":"test-admin","password":"test-password"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", stringReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login: status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestRouter_TenantCRUD(t *testing.T) {
	_, handler, jwtMgr := setupRouter(t)
	token := getAuthToken(t, jwtMgr)

	// Create tenant.
	body := `{"metadata":{"name":"test"},"spec":{"kubernetesVersion":"1.35.0"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", stringReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want %d, body = %s", w.Code, http.StatusCreated, w.Body.String())
	}

	// Get tenant.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRouter_Unauthenticated(t *testing.T) {
	_, handler, _ := setupRouter(t)

	// Create tenant without auth — should be 401.
	body := `{"metadata":{"name":"test"},"spec":{"kubernetesVersion":"1.35.0"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", stringReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestRouter_UserCRUD(t *testing.T) {
	_, handler, jwtMgr := setupRouter(t)
	token := getAuthToken(t, jwtMgr)

	// Create user.
	body := `{"metadata":{"name":"alice"},"spec":{"role":"edit","password":"secret123"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", stringReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create user: status = %d, want %d, body = %s", w.Code, http.StatusCreated, w.Body.String())
	}

	// List users.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list users: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRouter_MachineList(t *testing.T) {
	store, handler, jwtMgr := setupRouter(t)
	token := getAuthToken(t, jwtMgr)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestRouter_Status(t *testing.T) {
	_, handler, _ := setupRouter(t)

	// Status endpoint should require auth too (it's under /api/).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Protected by auth.
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status without auth: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestRouter_HealthNotInAPIRouter(t *testing.T) {
	_, handler, _ := setupRouter(t)

	// Health endpoints are in main.go's mux, not the API router.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// API router doesn't have health handlers — returns 404.
	if w.Code != http.StatusNotFound {
		t.Errorf("healthz: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
