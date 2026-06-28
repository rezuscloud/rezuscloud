package controller

import (
	"path/filepath"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func setupControllerTest(t *testing.T) (*state.Store, *FinalizerController) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewFinalizerController(store)
}

// --- Tenant Finalizer Tests ---

func TestFinalizer_TenantDelete_Reconciles(t *testing.T) {
	store, ctrl := setupControllerTest(t)

	// Create tenant with finalizers.
	tenant, err := store.CreateTenant("prod", state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_ = tenant

	for _, f := range DefaultTenantFinalizers() {
		_ = store.AddFinalizer("tenant", "prod", f)
	}

	// Delete tenant (sets deletionTimestamp).
	_, err = store.DeleteResource("tenant", "prod")
	if err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}

	// Verify deletionTimestamp is set.
	tenant, _ = store.GetTenant("prod")
	if tenant == nil {
		t.Fatal("tenant should still exist (has finalizers)")
	}
	if tenant.Metadata.DeletionTimestamp == nil {
		t.Error("deletionTimestamp should be set")
	}

	// Reconcile.
	if err := ctrl.ReconcileTenant("prod"); err != nil {
		t.Fatalf("ReconcileTenant: %v", err)
	}

	// Verify tenant is now permanently deleted.
	tenant, _ = store.GetTenant("prod")
	if tenant != nil {
		t.Error("tenant should be permanently deleted after all finalizers clear")
	}
}

func TestFinalizer_TenantNoDeletionTimestamp_Skips(t *testing.T) {
	store, ctrl := setupControllerTest(t)

	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// Reconcile without deletion — should be a no-op.
	if err := ctrl.ReconcileTenant("prod"); err != nil {
		t.Fatalf("ReconcileTenant: %v", err)
	}

	// Tenant still exists.
	tenant, _ := store.GetTenant("prod")
	if tenant == nil {
		t.Error("tenant should still exist")
	}
}

func TestFinalizer_TenantCleansUpMachines(t *testing.T) {
	store, ctrl := setupControllerTest(t)

	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	_ = store.AddFinalizer("tenant", "prod", "rezuscloud.io/machines")

	// Create machines.
	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = store.CreateMachine("hw-002", state.MachineSpec{Connected: true},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)

	// Delete and reconcile.
	_, _ = store.DeleteResource("tenant", "prod")
	_ = ctrl.ReconcileTenant("prod")

	// Machines should be deleted.
	m, _ := store.GetMachine("hw-001")
	if m != nil {
		t.Error("machine hw-001 should be deleted")
	}
	m, _ = store.GetMachine("hw-002")
	if m != nil {
		t.Error("machine hw-002 should be deleted")
	}
}

// --- Machine Finalizer Tests ---

func TestFinalizer_MachineDelete_Reconciles(t *testing.T) {
	store, ctrl := setupControllerTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true}, nil, nil)
	for _, f := range DefaultMachineFinalizers() {
		_ = store.AddFinalizer("machine", "hw-001", f)
	}

	// Delete machine.
	_, err := store.DeleteResource("machine", "hw-001")
	if err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}

	// Verify still exists.
	m, _ := store.GetMachine("hw-001")
	if m == nil {
		t.Fatal("machine should still exist (has finalizers)")
	}

	// Reconcile.
	if err := ctrl.ReconcileMachine("hw-001"); err != nil {
		t.Fatalf("ReconcileMachine: %v", err)
	}

	// Verify permanently deleted.
	m, _ = store.GetMachine("hw-001")
	if m != nil {
		t.Error("machine should be permanently deleted")
	}
}

func TestFinalizer_MachineNoDeletionTimestamp_Skips(t *testing.T) {
	store, ctrl := setupControllerTest(t)

	_, _ = store.CreateMachine("hw-001", state.MachineSpec{Connected: true}, nil, nil)
	_ = store.AddFinalizer("machine", "hw-001", "rezuscloud.io/config")

	// No deletion — reconcile is no-op.
	_ = ctrl.ReconcileMachine("hw-001")

	m, _ := store.GetMachine("hw-001")
	if m == nil {
		t.Error("machine should still exist")
	}
}

// --- NodeGroup Finalizer Tests ---

func TestFinalizer_NodeGroupDelete_Reconciles(t *testing.T) {
	store, ctrl := setupControllerTest(t)

	// Create tenant.
	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// Create node group with finalizers.
	_, _ = store.CreateResource("nodegroup", "workers", state.NodeGroupSpec{
		Name: "workers", Role: "worker", Count: 2,
	}, nil, map[string]string{
		"rezuscloud.io/tenant": "prod",
		"rezuscloud.io/role":   "worker",
	}, nil)
	_ = store.AddFinalizer("nodegroup", "workers", "rezuscloud.io/machines")

	// Create machines in the group.
	_, _ = store.CreateMachine("hw-001", state.MachineSpec{}, map[string]string{
		"rezuscloud.io/tenant":     "prod",
		"rezuscloud.io/node-group": "workers",
	}, nil)
	_, _ = store.CreateMachine("hw-002", state.MachineSpec{}, map[string]string{
		"rezuscloud.io/tenant":     "prod",
		"rezuscloud.io/node-group": "workers",
	}, nil)

	// Delete node group.
	_, _ = store.DeleteResource("nodegroup", "workers")

	// Reconcile.
	_ = ctrl.ReconcileNodeGroup("prod", "workers")

	// Node group should be gone.
	var spec state.NodeGroupSpec
	_, err := store.GetResource("nodegroup", "workers", &spec, nil)
	if err == nil {
		t.Error("node group should be deleted")
	}
}

// --- Finalizer Helpers ---

func TestFinalizer_AddAndRemove(t *testing.T) {
	store, _ := setupControllerTest(t)

	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// Add finalizer.
	err := store.AddFinalizer("tenant", "prod", "rezuscloud.io/test")
	if err != nil {
		t.Fatalf("AddFinalizer: %v", err)
	}

	md, _ := store.GetResource("tenant", "prod", nil, nil)
	if len(md.Finalizers) != 1 || md.Finalizers[0] != "rezuscloud.io/test" {
		t.Errorf("finalizers = %v, want [rezuscloud.io/test]", md.Finalizers)
	}

	// Add duplicate — idempotent.
	_ = store.AddFinalizer("tenant", "prod", "rezuscloud.io/test")
	md, _ = store.GetResource("tenant", "prod", nil, nil)
	if len(md.Finalizers) != 1 {
		t.Errorf("finalizers = %v, want 1 entry", md.Finalizers)
	}

	// Remove finalizer.
	removed, err := store.RemoveFinalizer("tenant", "prod", "rezuscloud.io/test")
	if err != nil {
		t.Fatalf("RemoveFinalizer: %v", err)
	}
	if !removed {
		t.Error("should report removed=true")
	}

	md, _ = store.GetResource("tenant", "prod", nil, nil)
	if len(md.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want empty", md.Finalizers)
	}
}

func TestFinalizer_RemoveLastAutoDeletes(t *testing.T) {
	store, _ := setupControllerTest(t)

	_, _ = store.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	_ = store.AddFinalizer("tenant", "prod", "rezuscloud.io/test")

	// Delete (sets deletionTimestamp).
	_, _ = store.DeleteResource("tenant", "prod")

	// Remove last finalizer — should auto-delete.
	removed, err := store.RemoveFinalizer("tenant", "prod", "rezuscloud.io/test")
	if err != nil {
		t.Fatalf("RemoveFinalizer: %v", err)
	}
	if !removed {
		t.Error("should report removed=true")
	}

	// Tenant should be gone.
	tenant, _ := store.GetTenant("prod")
	if tenant != nil {
		t.Error("tenant should be auto-deleted when last finalizer clears")
	}
}
