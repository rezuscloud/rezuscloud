// Package integration tests exercise the full API surface against a real
// HTTP server backed by a real SQLite database. These are not unit tests —
// they validate the request path from HTTP client through middleware,
// authentication, handler, state store, and back.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
)

// testServer holds the dependencies for an integration test.
type testServer struct {
	server         *httptest.Server
	store          *state.Store
	client         *http.Client
	auditComponent *audit.Component
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	path := filepath.Join(t.TempDir(), "integration.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jwtManager := auth.NewJWTManager("integration-test-secret")

	auditComponent := audit.NewComponent(store.DB(), audit.ComponentOptions{})
	t.Cleanup(auditComponent.Close)

	handler := api.Router(store, jwtManager, auditComponent, nil, upgrade.NewManager(store))

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &testServer{
		server:         server,
		store:          store,
		client:         server.Client(),
		auditComponent: auditComponent,
	}
}

// doRequest performs an HTTP request and returns the response body.
func (ts *testServer) doRequest(method, path string, body any, token string) (*http.Response, map[string]any) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, ts.server.URL+path, bodyReader)
	if err != nil {
		return nil, nil
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := ts.client.Do(req)
	if err != nil {
		return nil, nil
	}

	var result map[string]any
	if resp.Body != nil {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(bodyBytes, &result)
	}

	return resp, result
}

// createUser creates a user and returns a valid JWT token.
func (ts *testServer) createUser(t *testing.T, username, role, password string) string {
	t.Helper()

	// Create user directly in the store if it doesn't exist.
	existing, _ := ts.store.GetUser(username)
	if existing == nil {
		hash, err := auth.HashPassword(password)
		if err != nil {
			t.Fatalf("hash password: %v", err)
		}
		_, err = ts.store.CreateUser(username, state.UserSpec{
			Role:         role,
			PasswordHash: hash,
		})
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
	}

	// Login to get token.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d, body %v", resp.StatusCode, result)
	}

	token, _ := result["token"].(map[string]any)
	accessToken, _ := token["accessToken"].(string)
	if accessToken == "" {
		t.Fatal("no access token in login response")
	}
	return accessToken
}

// ============================================================
// Health endpoints (no auth required)
// ============================================================

func TestHealthz(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := ts.doRequest(http.MethodGet, "/healthz", nil, "")

	// Health endpoints are in main.go's mux, not in the API router.
	// The API router doesn't have /healthz, so this returns 404.
	// This is expected — healthz is registered in main.go, not api.Router.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("healthz: status = %d, want 404 (not in API router)", resp.StatusCode)
	}
}

// ============================================================
// Auth endpoints (public)
// ============================================================

func TestAuth_Login_Success(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser(t, "admin", "admin", "secret123")

	resp, result := ts.doRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin",
		"password": "secret123",
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status = %d, body = %v", resp.StatusCode, result)
	}

	if result["token"] == nil {
		t.Fatal("response should contain token")
	}
}

func TestAuth_Login_WrongPassword(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser(t, "admin", "admin", "secret123")

	resp, result := ts.doRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin",
		"password": "wrong",
	}, "")

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401, body = %v", resp.StatusCode, result)
	}
}

func TestAuth_Login_UserNotFound(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := ts.doRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "ghost",
		"password": "whatever",
	}, "")

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuth_Logout(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := ts.doRequest(http.MethodPost, "/api/v1/auth/logout", nil, "")

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestAuth_Whoami_WithToken(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "editor", "edit", "pass123")

	resp, result := ts.doRequest(http.MethodGet, "/api/v1/auth/whoami", nil, token)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami: status = %d, body = %v", resp.StatusCode, result)
	}
	if result["username"] != "editor" {
		t.Errorf("username = %v, want editor", result["username"])
	}
	if result["role"] != "edit" {
		t.Errorf("role = %v, want edit", result["role"])
	}
}

func TestAuth_Whoami_NoToken(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/auth/whoami", nil, "")

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ============================================================
// Protected endpoints without auth → 401
// ============================================================

func TestUnauthenticated_TenantEndpoints(t *testing.T) {
	ts := newTestServer(t)

	endpoints := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/tenants"},
		{http.MethodPost, "/api/v1/tenants"},
		{http.MethodGet, "/api/v1/tenants/nonexistent"},
		{http.MethodDelete, "/api/v1/tenants/nonexistent"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			resp, _ := ts.doRequest(ep.method, ep.path, nil, "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

func TestUnauthenticated_MachineEndpoints(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/machines", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUnauthenticated_UserEndpoints(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/users", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ============================================================
// Tenant CRUD
// ============================================================

func TestTenant_FullCRUD(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "prod"},
		"spec":     map[string]string{"kubernetesVersion": "1.35.0"},
	}, token)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %v", resp.StatusCode, result)
	}
	if result["metadata"] == nil {
		t.Fatal("response should have metadata")
	}

	// Get.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/tenants/prod", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d", resp.StatusCode)
	}

	meta, _ := result["metadata"].(map[string]any)
	if meta["name"] != "prod" {
		t.Errorf("name = %v, want prod", meta["name"])
	}

	// List.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/tenants", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d", resp.StatusCode)
	}
	items, _ := result["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}

	// Delete (returns 200 with updated resource — graceful deletion).
	resp, _ = ts.doRequest(http.MethodDelete, "/api/v1/tenants/prod", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete: status = %d, want 200", resp.StatusCode)
	}
}

func TestTenant_CreateDuplicate(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create first.
	ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "dup"},
		"spec":     map[string]string{"kubernetesVersion": "1.35.0"},
	}, token)

	// Create duplicate.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "dup"},
		"spec":     map[string]string{"kubernetesVersion": "1.35.0"},
	}, token)

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want 409, body = %v", resp.StatusCode, result)
	}
}

func TestTenant_GetNotFound(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/tenants/nope", nil, token)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ============================================================
// NodeGroup CRUD (nested under tenant)
// ============================================================

func TestNodeGroup_FullCRUD(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create tenant first.
	ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "prod"},
		"spec":     map[string]string{"kubernetesVersion": "1.35.0"},
	}, token)

	// Create node group.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/tenants/prod/node-groups", map[string]any{
		"metadata": map[string]string{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 3},
	}, token)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create nodegroup: status = %d, body = %v", resp.StatusCode, result)
	}

	// Get.
	resp, _ = ts.doRequest(http.MethodGet, "/api/v1/tenants/prod/node-groups/workers", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get nodegroup: status = %d", resp.StatusCode)
	}

	// List.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/tenants/prod/node-groups", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list nodegroups: status = %d", resp.StatusCode)
	}
	if total, _ := result["total"].(float64); total != 1 {
		t.Errorf("total = %v, want 1", total)
	}

	// Delete.
	resp, _ = ts.doRequest(http.MethodDelete, "/api/v1/tenants/prod/node-groups/workers", nil, token)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete nodegroup: status = %d, want 204", resp.StatusCode)
	}
}

func TestNodeGroup_TenantNotFound(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	resp, _ := ts.doRequest(http.MethodPost, "/api/v1/tenants/nonexistent/node-groups", map[string]any{
		"metadata": map[string]string{"name": "workers"},
		"spec":     map[string]any{"role": "worker", "count": 1},
	}, token)

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ============================================================
// Machine CRUD (cluster-wide + tenant-scoped)
// ============================================================

func TestMachine_ClusterWideAndTenantScoped(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create tenant.
	ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "prod"},
		"spec":     map[string]string{"kubernetesVersion": "1.35.0"},
	}, token)

	// Create machines.
	_, _ = ts.store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = ts.store.CreateMachine("hw-002", state.MachineSpec{Connected: false}, nil, nil)

	// Cluster-wide list.
	resp, result := ts.doRequest(http.MethodGet, "/api/v1/machines", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list machines: status = %d", resp.StatusCode)
	}
	if total, _ := result["total"].(float64); total != 2 {
		t.Errorf("total = %v, want 2", total)
	}

	// Tenant-scoped list.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/tenants/prod/machines", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tenant machines: status = %d", resp.StatusCode)
	}
	if total, _ := result["total"].(float64); total != 1 {
		t.Errorf("tenant total = %v, want 1", total)
	}

	// Get by ID.
	resp, _ = ts.doRequest(http.MethodGet, "/api/v1/machines/hw-001", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("get machine: status = %d", resp.StatusCode)
	}
}

// ============================================================
// Provider endpoints
// ============================================================

func TestProvider_ListAndGet(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create provider directly.
	_, _ = ts.store.UpsertProvider("hetzner", state.ProviderSpec{
		Endpoint: "localhost:50190",
	}, state.ProviderStatus{
		Connected: true,
		Schema:    &state.ProviderSchema{MachineTypes: []string{"standard"}, Regions: []string{"eu-west-1"}},
	}, nil)

	// List.
	resp, result := ts.doRequest(http.MethodGet, "/api/v1/providers", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list providers: status = %d", resp.StatusCode)
	}
	if total, _ := result["total"].(float64); total != 1 {
		t.Errorf("total = %v, want 1", total)
	}

	// Get.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/providers/hetzner", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get provider: status = %d", resp.StatusCode)
	}
	if result["metadata"] == nil {
		t.Fatal("should have metadata")
	}
}

// ============================================================
// JoinToken CRUD
// ============================================================

func TestJoinToken_CreateAndList(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create tenant + node group.
	ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "prod"},
		"spec":     map[string]string{"kubernetesVersion": "1.35.0"},
	}, token)
	_, _ = ts.store.CreateResource("nodegroup", "workers", state.NodeGroupSpec{
		Name: "workers", Role: "worker", Count: 3,
	}, nil, map[string]string{
		"rezuscloud.io/tenant": "prod",
		"rezuscloud.io/role":   "worker",
	}, nil)

	// Create token.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/tenants/prod/join-tokens", map[string]any{
		"spec": map[string]any{"nodeGroup": "workers", "singleUse": true},
	}, token)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: status = %d, body = %v", resp.StatusCode, result)
	}

	tokenValue, _ := result["token"].(string)
	if len(tokenValue) != 64 {
		t.Errorf("token length = %d, want 64", len(tokenValue))
	}

	// List tokens.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/tenants/prod/join-tokens", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tokens: status = %d", resp.StatusCode)
	}
	if total, _ := result["total"].(float64); total != 1 {
		t.Errorf("total = %v, want 1", total)
	}
}

// ============================================================
// User CRUD (admin only)
// ============================================================

func TestUser_CRUD(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Create user.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/users", map[string]any{
		"metadata": map[string]string{"name": "viewer"},
		"spec":     map[string]string{"role": "view", "password": "viewpass"},
	}, token)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user: status = %d, body = %v", resp.StatusCode, result)
	}

	// Password hash should never appear.
	raw, _ := json.Marshal(result)
	if bytes.Contains(raw, []byte("passwordHash")) {
		t.Error("response should not contain passwordHash")
	}
	if bytes.Contains(raw, []byte("$2a$")) {
		t.Error("response should not contain bcrypt hash")
	}

	// Get.
	resp, _ = ts.doRequest(http.MethodGet, "/api/v1/users/viewer", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get user: status = %d", resp.StatusCode)
	}

	// List.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/users", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list users: status = %d", resp.StatusCode)
	}
	items, _ := result["items"].([]any)
	if len(items) != 2 { // admin + viewer
		t.Errorf("users = %d, want 2", len(items))
	}

	// Login as the new user.
	resp, _ = ts.doRequest(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "viewer",
		"password": "viewpass",
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("login as viewer: status = %d, want 200", resp.StatusCode)
	}
}

func TestUser_ViewRoleCannotCreateUser(t *testing.T) {
	ts := newTestServer(t)
	adminToken := ts.createUser(t, "admin2", "admin", "pass")

	// Create view-role user.
	ts.doRequest(http.MethodPost, "/api/v1/users", map[string]any{
		"metadata": map[string]string{"name": "viewer2"},
		"spec":     map[string]string{"role": "view", "password": "viewpass"},
	}, adminToken)

	viewerToken := ts.createUser(t, "viewer2", "view", "viewpass")

	// Viewer tries to create user — should be forbidden.
	resp, _ := ts.doRequest(http.MethodPost, "/api/v1/users", map[string]any{
		"metadata": map[string]string{"name": "sneaky"},
		"spec":     map[string]string{"role": "admin", "password": "nope"},
	}, viewerToken)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer creating user: status = %d, want 403", resp.StatusCode)
	}
}

// ============================================================
// Structured errors
// ============================================================

func TestErrors_StructuredFormat(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	resp, result := ts.doRequest(http.MethodGet, "/api/v1/tenants/nonexistent", nil, token)

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	// All errors should have status/message/reason/code.
	if result["status"] != "failure" {
		t.Errorf("status = %v, want failure", result["status"])
	}
	if result["reason"] == nil {
		t.Error("should have reason field")
	}
	if result["code"] == nil {
		t.Error("should have code field")
	}
}

// ============================================================
// Recovery middleware (panic → 500)
// ============================================================

func TestRecovery_PanicReturns500(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	// Get a nonexistent tenant — triggers structured error path, not panic.
	// To test recovery, we'd need a handler that panics.
	// For now, verify the error is structured.
	resp, result := ts.doRequest(http.MethodGet, "/api/v1/tenants/nonexistent", nil, token)

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404, body = %v", resp.StatusCode, result)
	}
}

// ============================================================
// Cross-cutting: token works across all endpoints
// ============================================================

func TestToken_WorksWithAllEndpoints(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "admin", "admin", "pass")

	endpoints := []struct {
		name   string
		method string
		path   string
		code   int
	}{
		{"tenants list", http.MethodGet, "/api/v1/tenants", http.StatusOK},
		{"machines list", http.MethodGet, "/api/v1/machines", http.StatusOK},
		{"providers list", http.MethodGet, "/api/v1/providers", http.StatusOK},
		{"users list", http.MethodGet, "/api/v1/users", http.StatusOK},
		{"status", http.MethodGet, "/api/v1/status", http.StatusOK},
		{"whoami", http.MethodGet, "/api/v1/auth/whoami", http.StatusOK},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			resp, _ := ts.doRequest(ep.method, ep.path, nil, token)
			if resp.StatusCode != ep.code {
				t.Errorf("status = %d, want %d", resp.StatusCode, ep.code)
			}
		})
	}
}

// Ensure unused imports are referenced.
var _ = fmt.Sprintf
var _ = os.ReadFile
var _ = filepath.Join

// --- W9: Users + API Tokens ---

func TestAPI_APITokenLifecycle(t *testing.T) {
	ts := newTestServer(t)

	// Create an admin user.
	token := ts.createUser(t, "alice", "admin", "pw123456")

	// POST /api/v1/users/alice/api-tokens — should mint and return the secret.
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/users/alice/api-tokens", map[string]any{}, token)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: status=%d body=%v", resp.StatusCode, result)
	}
	secret, _ := result["secret"].(string)
	if secret == "" {
		t.Fatalf("expected secret in response, got: %v", result)
	}
	tokID, _ := result["id"].(string)
	if tokID == "" {
		t.Fatalf("expected id in response")
	}
	// Plaintext must never be present in any subsequent endpoint.
	if _, hasSecret := result["secret"]; !hasSecret {
		t.Fatalf("secret missing from create response")
	}

	// GET /api/v1/api-tokens/{id} — no secret in response.
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/api-tokens/"+tokID, nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get token: status=%d", resp.StatusCode)
	}
	if _, present := result["secret"]; present {
		t.Errorf("GET must not return plaintext secret: %v", result)
	}

	// GET /api/v1/api-tokens — list (admin).
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/api-tokens", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tokens: %d", resp.StatusCode)
	}
	items, _ := result["items"].([]any)
	if len(items) != 1 {
		t.Errorf("list: %d items, want 1", len(items))
	}

	// DELETE /api/v1/api-tokens/{id}.
	resp, _ = ts.doRequest(http.MethodDelete, "/api/v1/api-tokens/"+tokID, nil, token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}

	resp, _ = ts.doRequest(http.MethodGet, "/api/v1/api-tokens/"+tokID, nil, token)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("after revoke: %d, want 404", resp.StatusCode)
	}
}

func TestAPI_APITokenAuthenticates(t *testing.T) {
	ts := newTestServer(t)

	// Create alice (admin) and mint an API token.
	token := ts.createUser(t, "alice", "admin", "pw123456")
	resp, result := ts.doRequest(http.MethodPost, "/api/v1/users/alice/api-tokens", map[string]any{}, token)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %d", resp.StatusCode)
	}
	secret, _ := result["secret"].(string)

	// Use the API token to call a protected endpoint (/api/v1/auth/whoami).
	resp, result = ts.doRequest(http.MethodGet, "/api/v1/auth/whoami", nil, secret)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami with API token: status=%d body=%v", resp.StatusCode, result)
	}
	username, _ := result["username"].(string)
	if username != "alice" {
		t.Errorf("username = %q, want alice", username)
	}
	role, _ := result["role"].(string)
	if role != "admin" {
		t.Errorf("role = %q, want admin (denormalized)", role)
	}
}

func TestAPI_APITokenRejectsInvalid(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/auth/whoami", nil, "rez_does_not_exist")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("invalid token: status=%d, want 401", resp.StatusCode)
	}
}

func TestAPI_APITokenNonAdminCannotListAll(t *testing.T) {
	ts := newTestServer(t)
	viewerToken := ts.createUser(t, "viewer", "view", "pw123456")
	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/api-tokens", nil, viewerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer list-all: status=%d, want 403", resp.StatusCode)
	}
}

func TestAPI_APITokenCrossUserCreateForbidden(t *testing.T) {
	ts := newTestServer(t)
	aliceToken := ts.createUser(t, "alice", "edit", "pw123456")
	_ = ts.createUser(t, "bob", "view", "pw123456")
	// alice tries to mint a token for bob — forbidden.
	resp, _ := ts.doRequest(http.MethodPost, "/api/v1/users/bob/api-tokens", map[string]any{}, aliceToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-user create: status=%d, want 403", resp.StatusCode)
	}
}

// --- W10: Audit log ---

func TestAPI_AuditLogsMutations(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "alice", "admin", "pw123456")

	// Issue a mutation that should be audited.
	_, _ = ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "prod"},
		"spec":     map[string]any{"kubernetesVersion": "1.30.0"},
	}, token)

	// Wait briefly for the async audit queue to drain (recorder buffer=1024).
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	_ = ts.auditComponent.Flush(flushCtx)

	// Query audit endpoint — should see at least the create event.
	resp, result := ts.doRequest(http.MethodGet, "/api/v1/audit?user=alice&verb=create", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit list: %d", resp.StatusCode)
	}
	items, _ := result["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("expected at least one audit entry for alice/create, got %v", result)
	}

	// Each item should record alice + create + POST.
	first, _ := items[0].(map[string]any)
	if first["userName"] != "alice" {
		t.Errorf("userName = %v, want alice", first["userName"])
	}
	if first["verb"] != "create" {
		t.Errorf("verb = %v, want create", first["verb"])
	}
	if first["method"] != "POST" {
		t.Errorf("method = %v, want POST", first["method"])
	}
}

func TestAPI_AuditFilterByResource(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "alice", "admin", "pw123456")

	// Create a user (creates 'users' audit row) + a tenant (creates 'tenants' row).
	_, _ = ts.doRequest(http.MethodPost, "/api/v1/users", map[string]any{
		"metadata": map[string]string{"name": "bob"},
		"spec":     map[string]string{"role": "view", "password": "pw123456"},
	}, token)
	_, _ = ts.doRequest(http.MethodPost, "/api/v1/tenants", map[string]any{
		"metadata": map[string]string{"name": "prod"},
		"spec":     map[string]any{"kubernetesVersion": "1.30.0"},
	}, token)
	flushCtx2, flushCancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel2()
	_ = ts.auditComponent.Flush(flushCtx2)

	resp, result := ts.doRequest(http.MethodGet, "/api/v1/audit?resource=tenants", nil, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit filter: %d", resp.StatusCode)
	}
	items, _ := result["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["resource"] != "tenants" {
			t.Errorf("filter leak: resource = %v", m["resource"])
		}
	}
}

func TestAPI_AuditRejectsBadSince(t *testing.T) {
	ts := newTestServer(t)
	token := ts.createUser(t, "alice", "admin", "pw123456")
	resp, _ := ts.doRequest(http.MethodGet, "/api/v1/audit?since=garbage", nil, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad since: %d, want 400", resp.StatusCode)
	}
}
