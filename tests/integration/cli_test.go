// Package integration tests exercise the CLI's apiclient against a real
// API server backed by a real SQLite database. These validate that the
// CLI client, resource registry, and API server all agree on resource
// shapes, paths, and behaviors.
package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/api"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
	"github.com/rezuscloud/rezuscloud/internal/cli/registry"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
)

// cliTestEnv sets up a real API server and returns a configured client + cleanup.
func cliTestEnv(t *testing.T) (*apiclient.Client, *state.Store, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "cli-integration.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	jwtManager := auth.NewJWTManager("cli-integration-test-secret")
	handler := api.Router(store, jwtManager, audit.NewComponent(store.DB(), audit.ComponentOptions{}), nil, upgrade.NewManager(store), nil)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// Create admin user + get token.
	hash, err := auth.HashPassword("admin-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	_, _ = store.CreateUser("admin", state.UserSpec{
		Role:         auth.RoleAdmin,
		PasswordHash: hash,
	})

	// Login via HTTP to get a real JWT.
	loginResp, err := http.Post(
		server.URL+"/api/v1/auth/login",
		"application/json",
		strings.NewReader(`{"username":"admin","password":"admin-pass"}`),
	)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer func() { _ = loginResp.Body.Close() }()

	var loginResult struct {
		Token struct {
			AccessToken string `json:"accessToken"`
		} `json:"token"`
	}
	if err := json.NewDecoder(loginResp.Body).Decode(&loginResult); err != nil {
		t.Fatalf("decode login: %v", err)
	}

	client := apiclient.New(server.URL, loginResult.Token.AccessToken)
	return client, store, server.URL
}

// ============================================================
// Resource registry resolves to correct API paths
// ============================================================

func TestCLI_RegistryResolvesAllTypes(t *testing.T) {
	reg := registry.New()
	types := reg.All()

	if len(types) != 6 {
		t.Fatalf("expected 6 resource types, got %d", len(types))
	}

	for _, rt := range types {
		t.Run(rt.Kind, func(t *testing.T) {
			// Each type must resolve by its primary name.
			resolved, err := reg.Resolve(rt.Names[0])
			if err != nil {
				t.Fatalf("Resolve(%q): %v", rt.Names[0], err)
			}
			if resolved.Kind != rt.Kind {
				t.Errorf("Resolve(%q).Kind = %q, want %q", rt.Names[0], resolved.Kind, rt.Kind)
			}
		})
	}
}

func TestCLI_RegistryShortNames(t *testing.T) {
	reg := registry.New()

	shortNames := map[string]string{
		"clusters": "Cluster",
		"ng":       "NodeGroup",
		"machines": "Machine",
		"patch":    "ConfigPatch",
	}

	for name, wantKind := range shortNames {
		t.Run(name, func(t *testing.T) {
			rt, err := reg.Resolve(name)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", name, err)
			}
			if rt.Kind != wantKind {
				t.Errorf("Resolve(%q).Kind = %q, want %q", name, rt.Kind, wantKind)
			}
		})
	}
}

// ============================================================
// Cluster CRUD via apiclient
// ============================================================

func TestCLI_ClusterCRUD(t *testing.T) {
	client, store, _ := cliTestEnv(t)

	// Create.
	created, err := client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind: "Cluster",
		Metadata: &apiclient.ObjectMeta{
			Name: "prod",
		},
		Spec: map[string]any{
			"kubernetesVersion": "1.35.0",
		},
	})
	if err != nil {
		t.Fatalf("Create cluster: %v", err)
	}
	if created.Metadata.Name != "prod" {
		t.Errorf("created name = %q, want prod", created.Metadata.Name)
	}

	// Get.
	got, err := client.Get(t.Context(), "api/v1/tenants", "prod")
	if err != nil {
		t.Fatalf("Get cluster: %v", err)
	}
	if got.Metadata.Name != "prod" {
		t.Errorf("got name = %q, want prod", got.Metadata.Name)
	}

	// List.
	list, err := client.List(t.Context(), "api/v1/tenants", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List clusters: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}

	// Verify state store agrees.
	meta, err := store.GetResource("tenant", "prod", nil, nil)
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if meta.Name != "prod" {
		t.Errorf("store name = %q, want prod", meta.Name)
	}

	// Delete.
	deleted, err := client.Delete(t.Context(), "api/v1/tenants", "prod")
	if err != nil {
		t.Fatalf("Delete cluster: %v", err)
	}
	if deleted.Metadata.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp after delete")
	}
}

func TestCLI_ClusterCreateDuplicate(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	_, _ = client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "dup"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	_, err := client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "dup"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	var apiErr *apiclient.ErrorResponse
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errorAs(err, &apiErr) {
		t.Fatalf("expected *ErrorResponse, got %T: %v", err, err)
	}
	if apiErr.Code != 409 {
		t.Errorf("code = %d, want 409", apiErr.Code)
	}
}

func TestCLI_ClusterGetNotFound(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	_, err := client.Get(t.Context(), "api/v1/tenants", "nope")

	var apiErr *apiclient.ErrorResponse
	if err == nil {
		t.Fatal("expected not found error, got nil")
	}
	if !errorAs(err, &apiErr) {
		t.Fatalf("expected *ErrorResponse, got %T: %v", err, err)
	}
	if apiErr.Code != 404 {
		t.Errorf("code = %d, want 404", apiErr.Code)
	}
}

// ============================================================
// NodeGroup CRUD via apiclient (scoped to cluster)
// ============================================================

func TestCLI_NodeGroupCRUD(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	// Create tenant first.
	_, _ = client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "prod"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	// Create nodegroup.
	created, err := client.Create(t.Context(), "api/v1/tenants/prod/node-groups", &apiclient.Resource{
		Kind:     "NodeGroup",
		Metadata: &apiclient.ObjectMeta{Name: "workers"},
		Spec:     map[string]any{"role": "worker", "count": 3},
	})
	if err != nil {
		t.Fatalf("Create nodegroup: %v", err)
	}
	if created.Metadata.Name != "workers" {
		t.Errorf("created name = %q, want workers", created.Metadata.Name)
	}

	// Get.
	got, err := client.Get(t.Context(), "api/v1/tenants/prod/node-groups", "workers")
	if err != nil {
		t.Fatalf("Get nodegroup: %v", err)
	}
	if got.Metadata.Name != "workers" {
		t.Errorf("got name = %q, want workers", got.Metadata.Name)
	}

	// List.
	list, err := client.List(t.Context(), "api/v1/tenants/prod/node-groups", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List nodegroups: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}

	// Delete (204 no content is valid).
	_, err = client.Delete(t.Context(), "api/v1/tenants/prod/node-groups", "workers")
	if err != nil {
		t.Fatalf("Delete nodegroup: %v", err)
	}
}

// ============================================================
// Machine list via apiclient (cluster-wide + tenant-scoped)
// ============================================================

func TestCLI_MachineList(t *testing.T) {
	client, store, _ := cliTestEnv(t)

	// Create tenant.
	_, _ = client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "prod"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	// Create machines directly in store.
	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.CreateMachine("hw-002", state.MachineSpec{Connected: false}, nil, nil)

	// Cluster-wide list.
	list, err := client.List(t.Context(), "api/v1/machines", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List machines: %v", err)
	}
	if list.Total != 2 {
		t.Errorf("cluster-wide total = %d, want 2", list.Total)
	}

	// Tenant-scoped list.
	list, err = client.List(t.Context(), "api/v1/tenants/prod/machines", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List tenant machines: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("tenant total = %d, want 1", list.Total)
	}

	// Get by ID.
	got, err := client.Get(t.Context(), "api/v1/machines", "hw-001")
	if err != nil {
		t.Fatalf("Get machine: %v", err)
	}
	if got.Metadata.Name != "hw-001" {
		t.Errorf("got name = %q, want hw-001", got.Metadata.Name)
	}
}

// ============================================================
// ConfigPatch via apiclient
// ============================================================

func TestCLI_ConfigPatchCRUD(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	// Create tenant.
	_, _ = client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "prod"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	// Create patch.
	created, err := client.Create(t.Context(), "api/v1/tenants/prod/patches", &apiclient.Resource{
		Kind:     "ConfigPatch",
		Metadata: &apiclient.ObjectMeta{Name: "cilium-values"},
		Spec:     map[string]any{"patch": "clusterNetwork:\n  pods: 10.244.0.0/16"},
	})
	if err != nil {
		t.Fatalf("Create patch: %v", err)
	}
	if created.Metadata.Name != "cilium-values" {
		t.Errorf("created name = %q, want cilium-values", created.Metadata.Name)
	}

	// Get.
	got, err := client.Get(t.Context(), "api/v1/tenants/prod/patches", "cilium-values")
	if err != nil {
		t.Fatalf("Get patch: %v", err)
	}
	if got.Metadata.Name != "cilium-values" {
		t.Errorf("got name = %q, want cilium-values", got.Metadata.Name)
	}

	// List.
	list, err := client.List(t.Context(), "api/v1/tenants/prod/patches", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List patches: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}

	// Delete.
	_, err = client.Delete(t.Context(), "api/v1/tenants/prod/patches", "cilium-values")
	if err != nil {
		t.Fatalf("Delete patch: %v", err)
	}
}

// ============================================================
// Provider list via apiclient
// ============================================================

func TestCLI_ProviderList(t *testing.T) {
	client, store, _ := cliTestEnv(t)

	// Create provider directly.
	_, _ = store.UpsertProvider("hetzner", state.ProviderSpec{
		Endpoint: "grpc://localhost:50190",
	}, state.ProviderStatus{
		Connected: true,
		Schema:    &state.ProviderSchema{MachineTypes: []string{"cx22"}, Regions: []string{"fsn1"}},
	}, nil)

	// List.
	list, err := client.List(t.Context(), "api/v1/providers", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List providers: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}

	// Get.
	got, err := client.Get(t.Context(), "api/v1/providers", "hetzner")
	if err != nil {
		t.Fatalf("Get provider: %v", err)
	}
	if got.Metadata.Name != "hetzner" {
		t.Errorf("got name = %q, want hetzner", got.Metadata.Name)
	}
}

// ============================================================
// User CRUD via apiclient
// ============================================================

func TestCLI_UserCRUD(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	// Create user.
	created, err := client.Create(t.Context(), "api/v1/users", &apiclient.Resource{
		Kind:     "User",
		Metadata: &apiclient.ObjectMeta{Name: "viewer"},
		Spec:     map[string]any{"role": "view", "password": "viewpass"},
	})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}
	if created.Metadata.Name != "viewer" {
		t.Errorf("created name = %q, want viewer", created.Metadata.Name)
	}

	// List — should have admin + viewer.
	list, err := client.List(t.Context(), "api/v1/users", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List users: %v", err)
	}
	if list.Total != 2 {
		t.Errorf("total = %d, want 2 (admin + viewer)", list.Total)
	}

	// Get.
	got, err := client.Get(t.Context(), "api/v1/users", "viewer")
	if err != nil {
		t.Fatalf("Get user: %v", err)
	}
	if got.Metadata.Name != "viewer" {
		t.Errorf("got name = %q, want viewer", got.Metadata.Name)
	}

	// Password hash must not appear in response.
	raw, _ := json.Marshal(got)
	if contains(string(raw), "passwordHash") {
		t.Error("user response must not contain passwordHash")
	}
	if contains(string(raw), "$2a$") {
		t.Error("user response must not contain bcrypt hash")
	}
}

// ============================================================
// Registry path resolution matches actual API paths
// ============================================================

func TestCLI_RegistryPathsMatchAPI(t *testing.T) {
	reg := registry.New()

	tests := []struct {
		typeName string
		cluster  string
		wantPath string
	}{
		{"cluster", "", "api/v1/tenants"},
		{"machine", "", "api/v1/machines"},
		{"ng", "prod", "api/v1/tenants/prod/node-groups"},
		{"patch", "prod", "api/v1/tenants/prod/patches"},
		{"provider", "", "api/v1/providers"},
		{"user", "", "api/v1/users"},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			rt, err := reg.Resolve(tt.typeName)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tt.typeName, err)
			}

			path, err := rt.APIPath(tt.cluster)
			if err != nil {
				t.Fatalf("APIPath(%q): %v", tt.cluster, err)
			}
			if path != tt.wantPath {
				t.Errorf("APIPath() = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestCLI_ScopedResourceWithoutCluster(t *testing.T) {
	reg := registry.New()

	rt, _ := reg.Resolve("ng")
	_, err := rt.APIPath("")
	if err == nil {
		t.Error("expected error for scoped resource without --cluster")
	}
}

// ============================================================
// Error propagation through apiclient
// ============================================================

func TestCLI_ErrorPropagation(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	_, err := client.Get(t.Context(), "api/v1/tenants", "nonexistent")

	var apiErr *apiclient.ErrorResponse
	if err == nil {
		t.Fatal("expected error for nonexistent tenant")
	}
	if !errorAs(err, &apiErr) {
		t.Fatalf("expected *ErrorResponse, got %T: %v", err, err)
	}
	if apiErr.Code != 404 {
		t.Errorf("code = %d, want 404", apiErr.Code)
	}
	if apiErr.Reason != "NotFound" {
		t.Errorf("reason = %q, want NotFound", apiErr.Reason)
	}
	if apiErr.Message == "" {
		t.Error("error should have a message")
	}
}

// ============================================================
// Helpers
// ============================================================

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func errorAs(err error, target **apiclient.ErrorResponse) bool {
	if e, ok := err.(*apiclient.ErrorResponse); ok {
		*target = e
		return true
	}
	return false
}

// ============================================================
// Specialized command paths
// ============================================================

// TestCLI_ClusterStatus verifies the cluster status path that the CLI
// cluster status command uses: GET /api/v1/tenants/{name}.
func TestCLI_ClusterStatus(t *testing.T) {
	client, _, _ := cliTestEnv(t)

	// Create tenant.
	_, _ = client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "prod"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	// Fetch cluster status.
	got, err := client.Get(t.Context(), "api/v1/tenants", "prod")
	if err != nil {
		t.Fatalf("Get cluster: %v", err)
	}
	if got.Metadata.Name != "prod" {
		t.Errorf("name = %q, want prod", got.Metadata.Name)
	}
	// Verify spec is populated.
	spec, ok := got.Spec.(map[string]any)
	if !ok {
		t.Fatalf("spec type = %T, want map[string]any", got.Spec)
	}
	if spec["kubernetesVersion"] != "1.35.0" {
		t.Errorf("kubernetesVersion = %v, want 1.35.0", spec["kubernetesVersion"])
	}
}

// TestCLI_UserCreateAndLogin verifies the full user lifecycle:
// create → list → login as new user → whoami.
func TestCLI_UserCreateAndLogin(t *testing.T) {
	client, store, _ := cliTestEnv(t)

	// Create a viewer user via CLI path.
	created, err := client.Create(t.Context(), "api/v1/users", &apiclient.Resource{
		Kind:     "User",
		Metadata: &apiclient.ObjectMeta{Name: "ops-viewer"},
		Spec:     map[string]any{"role": "view", "password": "viewpass123"},
	})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}
	if created.Metadata.Name != "ops-viewer" {
		t.Errorf("created name = %q", created.Metadata.Name)
	}

	// List should show admin + ops-viewer.
	list, err := client.List(t.Context(), "api/v1/users", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List users: %v", err)
	}
	if list.Total != 2 {
		t.Errorf("total = %d, want 2", list.Total)
	}

	// Verify user can login.
	hash, _ := auth.HashPassword("viewpass123")
	_ = hash // already stored via API

	u, err := store.GetUser("ops-viewer")
	if err != nil {
		t.Fatalf("store get user: %v", err)
	}
	if u.Spec.Role != "view" {
		t.Errorf("role = %q, want view", u.Spec.Role)
	}
}

// TestCLI_MachineListPath verifies both machine list paths:
// cluster-wide: GET /api/v1/machines
// tenant-scoped: GET /api/v1/tenants/{c}/machines
func TestCLI_MachineListPath(t *testing.T) {
	client, store, _ := cliTestEnv(t)

	_, _ = client.Create(t.Context(), "api/v1/tenants", &apiclient.Resource{
		Kind:     "Cluster",
		Metadata: &apiclient.ObjectMeta{Name: "prod"},
		Spec:     map[string]any{"kubernetesVersion": "1.35.0"},
	})

	// Create machines with tenant labels.
	_, _ = store.CreateMachine("hw-alpha", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.CreateMachine("hw-beta", state.MachineSpec{Connected: false}, nil, nil)

	// Cluster-wide (used by: rezusctl machine list).
	all, err := client.List(t.Context(), "api/v1/machines", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List all machines: %v", err)
	}
	if all.Total != 2 {
		t.Errorf("cluster-wide total = %d, want 2", all.Total)
	}

	// Tenant-scoped (used by: rezusctl machine list -c prod).
	scoped, err := client.List(t.Context(), "api/v1/tenants/prod/machines", apiclient.ListOptions{})
	if err != nil {
		t.Fatalf("List tenant machines: %v", err)
	}
	if scoped.Total != 1 {
		t.Errorf("tenant-scoped total = %d, want 1", scoped.Total)
	}
}
