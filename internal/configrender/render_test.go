package configrender

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/talosconfig"
	"github.com/rezuscloud/rezuscloud/internal/validation"
)

// newTestStore opens a per-test store.
func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "configrender.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedTenant creates a tenant + secrets bundle, the minimum required to
// render a machine config.
func seedTenant(t *testing.T, store *state.Store, name string) {
	t.Helper()
	_, err := store.CreateResource("tenant", name, state.TenantSpec{
		KubernetesVersion:    "1.35.0",
		TalosVersion:         "1.12.0",
		ControlPlaneEndpoint: "https://192.168.1.10:6443",
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	bundle, err := credentials.GenerateSecretsBundle("1.12.0")
	if err != nil {
		t.Fatalf("gen bundle: %v", err)
	}
	bundleJSON, _ := credentials.SecretsBundleJSON(bundle)
	if err := store.SaveTenantSecrets(name, bundleJSON); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
}

// TestGenerateMachineConfig_EndToEnd verifies the assembled pipeline produces
// non-empty YAML. This exercises the same code path both the API and WebUI
// now share.
func TestGenerateMachineConfig_EndToEnd(t *testing.T) {
	store := newTestStore(t)
	seedTenant(t, store, "prod")

	// Create a control-plane machine in the tenant.
	_, err := store.CreateMachine("m1", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	if _, err := store.UpdateMachineStatus("m1", state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true}); err != nil {
		t.Fatalf("update status: %v", err)
	}

	result, err := GenerateMachineConfig(context.Background(), store, store, nil,
		MachineConfigRequest{TenantName: "prod", MachineID: "m1"})
	if err != nil {
		t.Fatalf("GenerateMachineConfig: %v", err)
	}
	if result.YAML == "" {
		t.Error("expected non-empty YAML")
	}
	if result.Machine == nil {
		t.Error("expected Machine populated")
	}
	if result.Tenant == nil {
		t.Error("expected Tenant populated")
	}
}

// TestGenerateMachineConfig_ErrorsOnMissingMachine verifies ErrNotFound wraps
// the error when the machine doesn't exist.
func TestGenerateMachineConfig_ErrorsOnMissingMachine(t *testing.T) {
	store := newTestStore(t)
	seedTenant(t, store, "prod")

	_, err := GenerateMachineConfig(context.Background(), store, store, nil,
		MachineConfigRequest{TenantName: "prod", MachineID: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for missing machine")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestGenerateMachineConfig_ErrorsOnMissingTenant verifies ErrNotFound wraps
// the error when the tenant doesn't exist.
func TestGenerateMachineConfig_ErrorsOnMissingTenant(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.CreateMachine("m1", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "ghost"}, nil)

	_, err := GenerateMachineConfig(context.Background(), store, store, nil,
		MachineConfigRequest{TenantName: "ghost", MachineID: "m1"})
	if err == nil {
		t.Fatal("expected error for missing tenant")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestGenerateMachineConfig_ErrorsOnMissingSecrets verifies ErrNotFound wraps
// the error when the secrets bundle is missing.
func TestGenerateMachineConfig_ErrorsOnMissingSecrets(t *testing.T) {
	store := newTestStore(t)
	// Tenant but no secrets bundle.
	_, _ = store.CreateResource("tenant", "prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil, nil)
	_, _ = store.CreateMachine("m1", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)

	_, err := GenerateMachineConfig(context.Background(), store, store, nil,
		MachineConfigRequest{TenantName: "prod", MachineID: "m1"})
	if err == nil {
		t.Fatal("expected error for missing secrets")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestGenerateMachineConfig_WithExplicitPatches verifies that the caller
// can pass pre-resolved patches via MachineConfigRequest and bypass the
// PatchResolver callback.
func TestGenerateMachineConfig_WithExplicitPatches(t *testing.T) {
	store := newTestStore(t)
	seedTenant(t, store, "prod")
	_, _ = store.CreateMachine("m1", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.UpdateMachineStatus("m1", state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true})

	called := false
	resolver := func(store state.StoreAPI, tenant, role, machine string) ([]string, error) {
		called = true
		return nil, nil
	}

	// Caller-supplied patches (empty slice, not nil) should bypass resolver.
	_, err := GenerateMachineConfig(context.Background(), store, store, resolver,
		MachineConfigRequest{
			TenantName: "prod", MachineID: "m1",
			Patches: []string{"machine:\n  type: controlplane"},
		})
	if err != nil {
		t.Fatalf("GenerateMachineConfig: %v", err)
	}
	if called {
		t.Error("resolver should not be called when caller supplies Patches")
	}
}

// TestGenerateMachineConfig_InvokesResolver verifies the resolver is invoked
// when Patches is nil.
func TestGenerateMachineConfig_InvokesResolver(t *testing.T) {
	store := newTestStore(t)
	seedTenant(t, store, "prod")
	_, _ = store.CreateMachine("m1", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.UpdateMachineStatus("m1", state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true})

	called := false
	resolver := func(store state.StoreAPI, tenant, role, machine string) ([]string, error) {
		called = true
		if tenant != "prod" {
			t.Errorf("resolver tenant = %q, want prod", tenant)
		}
		if role != "controlplane" {
			t.Errorf("resolver role = %q, want controlplane", role)
		}
		return nil, nil
	}

	_, err := GenerateMachineConfig(context.Background(), store, store, resolver,
		MachineConfigRequest{TenantName: "prod", MachineID: "m1"})
	if err != nil {
		t.Fatalf("GenerateMachineConfig: %v", err)
	}
	if !called {
		t.Error("resolver should have been called")
	}
}

// fakeStoreReader is a minimal StoreReader for error-path tests that don't
// need a real SQLite.
type fakeStoreReader struct {
	machine *state.Machine
	tenant  *state.Tenant
	secrets []byte
	err     error
}

func (f *fakeStoreReader) GetMachine(id string) (*state.Machine, error) {
	return f.machine, f.err
}
func (f *fakeStoreReader) GetTenant(name string) (*state.Tenant, error) {
	return f.tenant, f.err
}
func (f *fakeStoreReader) LoadTenantSecrets(name string) ([]byte, error) {
	return f.secrets, f.err
}

// TestStoreReader_Interface ensures fakeStoreReader satisfies the interface.
func TestStoreReader_Interface(t *testing.T) {
	var _ StoreReader = (*fakeStoreReader)(nil)
	var _ StoreReader = (*state.Store)(nil)
}

// TestMachineConfigResult_JSON ensures the result struct is JSON-serializable
// (for a future REST endpoint that returns it).
func TestMachineConfigResult_JSON(t *testing.T) {
	r := MachineConfigResult{
		YAML:        "foo",
		Machine:     &state.Machine{},
		Tenant:      &state.Tenant{},
		MachineType: talosconfig.TypeControlPlane,
	}
	if _, err := json.Marshal(r); err != nil {
		t.Errorf("Marshal: %v", err)
	}
}

// TestGenerateMachineConfig_MachineScopePrecedence verifies the resolver path
// applies cluster patches first and machine patches last (last writer wins),
// and that machine-scoped patches are invisible to other machines.
func TestGenerateMachineConfig_MachineScopePrecedence(t *testing.T) {
	store := newTestStore(t)
	seedTenant(t, store, "prod")

	patchAPI := patch.NewAPI(store, nil, validation.NewRegistry())
	create := func(name, spec string) {
		t.Helper()
		body := `{"metadata":{"name":"` + name + `"},"spec":` + spec + `}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/prod/patches", strings.NewReader(body))
		req.SetPathValue("tenant", "prod")
		w := httptest.NewRecorder()
		patchAPI.Create(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d: %s", name, w.Code, w.Body.String())
		}
	}

	_, err := store.CreateMachine("node-1", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	if _, err := store.UpdateMachineStatus("node-1", state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	_, err = store.CreateMachine("node-2", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	if _, err := store.UpdateMachineStatus("node-2", state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true}); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Names ordered so alphabetical order would produce the WRONG precedence —
	// only the scope ranking (cluster → machine) must hold.
	create("a-machine-ntp", `{"patch":"machine:\n  time:\n    servers:\n      - node1-pool","scope":"machine","targetMachine":"node-1","enabled":true}`)
	create("z-cluster-ntp", `{"patch":"machine:\n  time:\n    servers:\n      - cluster-pool","scope":"cluster","enabled":true}`)

	r1, err := GenerateMachineConfig(context.Background(), store, store, patch.ResolvePatches,
		MachineConfigRequest{TenantName: "prod", MachineID: "node-1"})
	if err != nil {
		t.Fatalf("GenerateMachineConfig node-1: %v", err)
	}
	if !strings.Contains(r1.YAML, "node1-pool") {
		t.Error("node-1 config must carry the machine-scoped override (last writer wins)")
	}
	r2, err := GenerateMachineConfig(context.Background(), store, store, patch.ResolvePatches,
		MachineConfigRequest{TenantName: "prod", MachineID: "node-2"})
	if err != nil {
		t.Fatalf("GenerateMachineConfig node-2: %v", err)
	}
	if !strings.Contains(r2.YAML, "cluster-pool") {
		t.Error("node-2 config must carry the cluster patch")
	}
	if strings.Contains(r2.YAML, "node1-pool") {
		t.Error("node-2 config must not see node-1's machine-scoped patch")
	}
}
