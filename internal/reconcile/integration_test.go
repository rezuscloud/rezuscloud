//go:build integration

package reconcile

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/tfbackend"
	"github.com/rezuscloud/rezuscloud/internal/tfexec"

	_ "modernc.org/sqlite"
)

func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

// testProvider renders a simple null_resource config. It lets the integration
// test exercise the full reconcile pipeline (store → render → init+apply →
// state) without cloud credentials.
type testProvider struct{}

func (p *testProvider) Type() string { return "null" }
func (p *testProvider) Mappings() []provider.TFResourceMapping {
	return []provider.TFResourceMapping{{TFType: "null_resource", Kind: "Machine"}}
}
func (p *testProvider) Render(req provider.RenderRequest) ([]byte, error) {
	cfg := provider.NewTFConfig()
	cfg.AddRequiredProviders(provider.ReqProvider{Name: "null", Source: "hashicorp/null"})
	for _, ng := range req.NodeGroups {
		cfg.AddResource("null_resource", ng.Name, provider.Obj{
			"count": ng.Count,
			"triggers": provider.Obj{
				"tenant": req.Tenant.Metadata.Name,
				"role":   ng.Role,
			},
		})
	}
	return json.Marshal(cfg)
}

// TestIntegration_ApplierDrivesRealTofu proves the production reconcile wiring:
// a tenant mutation → store → Applier.Apply → provider.Render → tofu init+apply
// → state stored in the backend. This is the same path main.go uses.
func TestIntegration_ApplierDrivesRealTofu(t *testing.T) {
	skipWithoutTofu(t)

	// Pin pool to 1 conn (modernc.org/sqlite ":memory:" creates a DB per conn).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store, err := state.NewFromDB(db)
	if err != nil {
		t.Fatal(err)
	}
	tfStore, err := tfbackend.New(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tfbackend.NewHandler(tfStore))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	execE, err := tfexec.New(root,
		tfexec.WithBinary("tofu"),
		tfexec.WithBackendURL(srv.URL+"/tfstate"),
		tfexec.WithTimeout(90*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	registry := provider.NewRegistry()
	registry.Register(&testProvider{})

	applier := NewApplier(execE, registry, store)

	// Seed a tenant + a node group that routes to the "null" provider.
	_, err = store.CreateTenant("personal", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(ngSpecJSON{Count: 2, Role: "worker", ProviderClass: "null:test"})
	_, err = store.CreateResource("nodegroup", "workers", json.RawMessage(spec), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "personal", "rezuscloud.io/role": "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Run the Applier — this is exactly what the apply queue calls.
	err = applier.Apply(context.Background(), "personal")
	if err != nil {
		// Skip if provider download fails (isolated runner).
		if strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "timeout") ||
			strings.Contains(err.Error(), "Failed to install provider") {
			t.Skipf("null provider unavailable; skipping:\n%v", err)
		}
		t.Fatalf("Apply: %v", err)
	}

	// Verify state was stored through the backend.
	got, found, err := tfStore.GetState(context.Background(), "personal")
	if err != nil || !found {
		t.Fatalf("state not stored: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(got), "null_resource") {
		t.Fatalf("state missing null_resource: %s", got)
	}
	if !strings.Contains(string(got), `"count"`) && !strings.Contains(string(got), "workers") {
		t.Fatalf("state missing nodegroup config: %s", got)
	}
}

// TestIntegration_ApplierDestroysTenant proves the teardown half of #171: after
// a tenant is soft-deleted (deletionTimestamp + finalizers set), Apply runs
// `tofu destroy`, cascade-removes child resources, and clears the finalizers so
// the tenant is garbage-collected. This is the same destroy path the apply
// queue drives when the EnqueueBus sees a tenant delete event.
func TestIntegration_ApplierDestroysTenant(t *testing.T) {
	skipWithoutTofu(t)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store, err := state.NewFromDB(db)
	if err != nil {
		t.Fatal(err)
	}
	tfStore, err := tfbackend.New(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tfbackend.NewHandler(tfStore))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	execE, err := tfexec.New(root,
		tfexec.WithBinary("tofu"),
		tfexec.WithBackendURL(srv.URL+"/tfstate"),
		tfexec.WithTimeout(90*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	registry := provider.NewRegistry()
	registry.Register(&testProvider{})

	applier := NewApplier(execE, registry, store)

	// Seed a tenant + a node group, then apply to create real state.
	_, err = store.CreateTenant("doomed", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(ngSpecJSON{Count: 2, Role: "worker", ProviderClass: "null:test"})
	_, err = store.CreateResource("nodegroup", "workers", json.RawMessage(spec), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "doomed", "rezuscloud.io/role": "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := applier.Apply(context.Background(), "doomed"); err != nil {
		if strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "timeout") ||
			strings.Contains(err.Error(), "Failed to install provider") {
			t.Skipf("null provider unavailable; skipping:\n%v", err)
		}
		t.Fatalf("initial Apply: %v", err)
	}

	// State must now contain the null_resource.
	got, found, _ := tfStore.GetState(context.Background(), "doomed")
	if !found || !strings.Contains(string(got), "null_resource") {
		t.Fatalf("state missing null_resource after apply: %s", got)
	}

	// Soft-delete: sets deletionTimestamp + finalizers.
	if err := store.DeleteTenant("doomed"); err != nil {
		t.Fatal(err)
	}

	// The destroy Apply — runs tofu destroy, cascade-removes children, clears
	// finalizers, GCs the tenant.
	if err := applier.Apply(context.Background(), "doomed"); err != nil {
		t.Fatalf("destroy Apply: %v", err)
	}

	// Tenant is gone (last finalizer cleared → auto-GC).
	if t1, _ := store.GetTenant("doomed"); t1 != nil {
		t.Fatal("tenant still exists after destroy — finalizers should have been cleared")
	}

	// Child resources are cascade-removed.
	metas, _, _, total, _ := store.ListResources("nodegroup", state.ListOptions{
		LabelSelector: "rezuscloud.io/tenant=doomed",
	})
	if total != 0 || len(metas) != 0 {
		t.Errorf("nodegroups survived destroy: %d remain", total)
	}

	// State in the backend is now empty (tofu destroy removed all resources).
	got, found, _ = tfStore.GetState(context.Background(), "doomed")
	if !found {
		t.Fatal("state object missing after destroy — backend should still hold an empty state")
	}
	if strings.Contains(string(got), "null_resource") {
		t.Errorf("state still contains null_resource after destroy: %s", got)
	}
}
