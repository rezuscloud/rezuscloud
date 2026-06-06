package state

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_CreatesDatabase(t *testing.T) {
	s := openTestStore(t)
	if s == nil {
		t.Fatal("store should not be nil")
	}
}

// --- Tenant Tests ---

func TestTenant_CRUD(t *testing.T) {
	s := openTestStore(t)

	// Create.
	tenant, err := s.CreateTenant("personal", TenantSpec{
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.6",
		NodeGroups: []NodeGroupSpec{
			{Name: "control-plane", Role: "controlplane", Count: 1, ProviderClass: "static"},
			{Name: "workers", Role: "worker", Count: 3, ProviderClass: "hetzner", ProviderConfig: []byte(`{"machineType":"cx41"}`)},
		},
	}, map[string]string{"env": "personal"}, nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Verify metadata.
	if tenant.Metadata.Name != "personal" {
		t.Errorf("name = %q, want %q", tenant.Metadata.Name, "personal")
	}
	if tenant.Metadata.UID == "" {
		t.Error("uid should be set")
	}
	if tenant.Metadata.ResourceVersion == 0 {
		t.Error("resourceVersion should be > 0")
	}
	if tenant.Metadata.Labels["env"] != "personal" {
		t.Errorf("label env = %q, want %q", tenant.Metadata.Labels["env"], "personal")
	}

	// Verify spec.
	if tenant.Spec.KubernetesVersion != "1.35.0" {
		t.Errorf("kubernetesVersion = %q, want %q", tenant.Spec.KubernetesVersion, "1.35.0")
	}
	if len(tenant.Spec.NodeGroups) != 2 {
		t.Fatalf("node groups = %d, want 2", len(tenant.Spec.NodeGroups))
	}
	if tenant.Spec.NodeGroups[1].ProviderClass != "hetzner" {
		t.Errorf("provider class = %q, want %q", tenant.Spec.NodeGroups[1].ProviderClass, "hetzner")
	}

	// Verify default status.
	if tenant.Status.Phase != TenantForming {
		t.Errorf("phase = %q, want %q", tenant.Status.Phase, TenantForming)
	}

	// Get.
	got, err := s.GetTenant("personal")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got == nil {
		t.Fatal("tenant should exist")
	}
	if got.Metadata.Name != "personal" {
		t.Errorf("name = %q, want %q", got.Metadata.Name, "personal")
	}

	// List.
	tenants, total, err := s.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(tenants) != 1 {
		t.Errorf("tenants count = %d, want 1", len(tenants))
	}

	// Update spec.
	updated, err := s.UpdateTenantSpec("personal", got.Metadata.ResourceVersion, TenantSpec{
		KubernetesVersion: "1.36.0",
		TalosVersion:      "1.12.6",
	}, got.Metadata.Labels, nil)
	if err != nil {
		t.Fatalf("UpdateTenantSpec: %v", err)
	}
	if updated.Spec.KubernetesVersion != "1.36.0" {
		t.Errorf("updated version = %q, want %q", updated.Spec.KubernetesVersion, "1.36.0")
	}

	// Update status.
	activeTenant, err := s.UpdateTenantStatus("personal", TenantStatus{
		Phase:     TenantActive,
		Available: true,
		Ready:     true,
		Machines:  MachineCounts{Total: 4, Healthy: 4, Connected: 4},
	})
	if err != nil {
		t.Fatalf("UpdateTenantStatus: %v", err)
	}
	if activeTenant.Status.Phase != TenantActive {
		t.Errorf("status phase = %q, want %q", activeTenant.Status.Phase, TenantActive)
	}

	// Delete (sets deletionTimestamp + finalizers).
	if err := s.DeleteTenant("personal"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	deleted, _ := s.GetTenant("personal")
	if deleted == nil {
		t.Fatal("tenant should still exist (finalizers block removal)")
	}
	if deleted.Metadata.DeletionTimestamp == nil {
		t.Error("deletionTimestamp should be set")
	}
	if len(deleted.Metadata.Finalizers) != 3 {
		t.Errorf("finalizers = %d, want 3", len(deleted.Metadata.Finalizers))
	}
}

func TestTenant_NotFound(t *testing.T) {
	s := openTestStore(t)

	tenant, err := s.GetTenant("nonexistent")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if tenant != nil {
		t.Error("tenant should be nil")
	}
}

func TestTenant_OptimisticConcurrency(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateTenant("concurrency-test", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// Read.
	first, _ := s.GetTenant("concurrency-test")

	// Update with correct version.
	_, err := s.UpdateTenantSpec("concurrency-test", first.Metadata.ResourceVersion, TenantSpec{KubernetesVersion: "1.36.0"}, nil, nil)
	if err != nil {
		t.Fatalf("first update should succeed: %v", err)
	}

	// Update with stale version — should fail.
	_, err = s.UpdateTenantSpec("concurrency-test", first.Metadata.ResourceVersion, TenantSpec{KubernetesVersion: "1.37.0"}, nil, nil)
	if err != ErrConflict {
		t.Errorf("second update with stale version: err = %v, want ErrConflict", err)
	}
}

func TestTenant_Pagination(t *testing.T) {
	s := openTestStore(t)

	for i := 0; i < 5; i++ {
		_, _ = s.CreateTenant(fmt.Sprintf("tenant-%d", i), TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	}

	// Page 1.
	page1, total, err := s.ListTenants(WithLimit(2), WithOffset(0))
	if err != nil {
		t.Fatalf("ListTenants page 1: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(page1) != 2 {
		t.Errorf("page 1 count = %d, want 2", len(page1))
	}

	// Page 2.
	page2, _, err := s.ListTenants(WithLimit(2), WithOffset(2))
	if err != nil {
		t.Fatalf("ListTenants page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 2 count = %d, want 2", len(page2))
	}

	// Page 3 (partial).
	page3, _, err := s.ListTenants(WithLimit(2), WithOffset(4))
	if err != nil {
		t.Fatalf("ListTenants page 3: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page 3 count = %d, want 1", len(page3))
	}
}

// --- Machine Tests ---

func TestMachine_CRUD(t *testing.T) {
	s := openTestStore(t)

	// Create machine.
	machine, err := s.CreateMachine("hw-uuid-001", MachineSpec{
		ManagementAddress: "10.0.0.1",
		Connected:         true,
	}, map[string]string{
		"rezuscloud.io/tenant":   "test",
		"rezuscloud.io/role":     "worker",
		"rezuscloud.io/provider": "hetzner",
	}, nil)
	if err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}

	// Verify default status.
	if machine.Status.Stage != StageInitializing {
		t.Errorf("initial stage = %q, want %q", machine.Status.Stage, StageInitializing)
	}

	// Get.
	m, err := s.GetMachine("hw-uuid-001")
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if m == nil {
		t.Fatal("machine should exist")
	}
	if m.Spec.ManagementAddress != "10.0.0.1" {
		t.Errorf("address = %q, want %q", m.Spec.ManagementAddress, "10.0.0.1")
	}
	if m.Metadata.Labels["rezuscloud.io/tenant"] != "test" {
		t.Errorf("tenant label = %q, want %q", m.Metadata.Labels["rezuscloud.io/tenant"], "test")
	}

	// Update status.
	updated, err := s.UpdateMachineStatus("hw-uuid-001", MachineStatus{
		Stage:        StageReady,
		Ready:        true,
		Role:         "worker",
		TalosVersion: "1.12.6",
		K8sVersion:   "1.35.0",
		Hardware: &HardwareInfo{
			Processors:    []ProcessorInfo{{CoreCount: 4, Description: "AMD EPYC"}},
			MemoryModules: []MemoryInfo{{SizeMB: 8192}},
			BlockDevices:  []BlockDeviceInfo{{Size: 107374182400, Type: "ssd", SystemDisk: true}},
		},
	})
	if err != nil {
		t.Fatalf("UpdateMachineStatus: %v", err)
	}
	if updated.Status.Stage != StageReady {
		t.Errorf("stage = %q, want %q", updated.Status.Stage, StageReady)
	}
	if updated.Status.Hardware.Processors[0].CoreCount != 4 {
		t.Errorf("CPU cores = %d, want 4", updated.Status.Hardware.Processors[0].CoreCount)
	}

	// List all.
	machines, total, err := s.ListMachines()
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(machines) != 1 {
		t.Errorf("machines count = %d, want 1", len(machines))
	}

	// Delete.
	if err := s.DeleteMachine("hw-uuid-001"); err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}

	deleted, _ := s.GetMachine("hw-uuid-001")
	if deleted.Metadata.DeletionTimestamp == nil {
		t.Error("deletionTimestamp should be set")
	}
}

func TestMachine_ListByTenant(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateMachine("m1", MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)
	_, _ = s.CreateMachine("m2", MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)
	_, _ = s.CreateMachine("m3", MachineSpec{}, map[string]string{"rezuscloud.io/tenant": "beta"}, nil)

	alphaMachines, _, err := s.ListMachinesByTenant("alpha")
	if err != nil {
		t.Fatalf("ListMachinesByTenant: %v", err)
	}
	if len(alphaMachines) != 2 {
		t.Errorf("alpha machines = %d, want 2", len(alphaMachines))
	}

	betaMachines, _, err := s.ListMachinesByTenant("beta")
	if err != nil {
		t.Fatalf("ListMachinesByTenant beta: %v", err)
	}
	if len(betaMachines) != 1 {
		t.Errorf("beta machines = %d, want 1", len(betaMachines))
	}
}

// --- Provider Tests ---

func TestProvider_Upsert(t *testing.T) {
	s := openTestStore(t)

	// Create.
	p, err := s.UpsertProvider("hetzner", ProviderSpec{
		Endpoint: "grpc://provider-hetzner:50190",
	}, ProviderStatus{
		Connected:     true,
		LastHeartbeat: time.Now().UTC(),
		Schema: &ProviderSchema{
			MachineTypes: []string{"cx22", "cx32"},
			Regions:      []string{"fsn1", "nbg1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if p.Metadata.Name != "hetzner" {
		t.Errorf("name = %q, want %q", p.Metadata.Name, "hetzner")
	}
	if p.Status.Schema.MachineTypes[0] != "cx22" {
		t.Errorf("machine type = %q, want %q", p.Status.Schema.MachineTypes[0], "cx22")
	}

	// Upsert (update status).
	p2, err := s.UpsertProvider("hetzner", ProviderSpec{}, ProviderStatus{
		Connected:     false,
		LastHeartbeat: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatalf("UpsertProvider update: %v", err)
	}
	if p2.Status.Connected {
		t.Error("should be disconnected after upsert")
	}

	// List.
	providers, err := s.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 1 {
		t.Errorf("providers = %d, want 1", len(providers))
	}
}

// --- JoinToken Tests ---

func TestJoinToken_IssueConsumeExpire(t *testing.T) {
	s := openTestStore(t)

	// Issue token.
	jt, err := s.CreateJoinToken("abc123", JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		SingleUse: true,
		NodeGroup: "workers",
	}, "test", "workers")
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	if jt.Metadata.Labels["rezuscloud.io/tenant"] != "test" {
		t.Errorf("tenant label = %q, want %q", jt.Metadata.Labels["rezuscloud.io/tenant"], "test")
	}

	// Lookup.
	found, err := s.LookupJoinToken("abc123")
	if err != nil {
		t.Fatalf("LookupJoinToken: %v", err)
	}
	if found == nil {
		t.Fatal("token should exist")
	}

	// Consume.
	consumed, err := s.ConsumeJoinToken("abc123")
	if err != nil {
		t.Fatalf("ConsumeJoinToken: %v", err)
	}
	if consumed == nil {
		t.Fatal("token should be consumed")
	}

	// Second consume should return nil.
	again, _ := s.ConsumeJoinToken("abc123")
	if again != nil {
		t.Error("token should be nil on second consume")
	}
}

func TestJoinToken_Expired(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateJoinToken("expired", JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
		SingleUse: true,
	}, "test", "workers")

	jt, err := s.LookupJoinToken("expired")
	if err != nil {
		t.Fatalf("LookupJoinToken: %v", err)
	}
	if jt != nil {
		t.Error("expired token should return nil")
	}
}

func TestJoinToken_Cleanup(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateJoinToken("old", JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
		SingleUse: true,
	}, "test", "workers")
	_, _ = s.CreateJoinToken("fresh", JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
		SingleUse: true,
	}, "test", "workers")

	removed, err := s.CleanupExpiredTokens()
	if err != nil {
		t.Fatalf("CleanupExpiredTokens: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	fresh, _ := s.LookupJoinToken("fresh")
	if fresh == nil {
		t.Error("fresh token should still exist")
	}
}

func TestListJoinTokens_Empty(t *testing.T) {
	s := openTestStore(t)

	items, total, err := s.ListJoinTokens()
	if err != nil {
		t.Fatalf("ListJoinTokens: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Errorf("expected empty list, got %d items (total=%d)", len(items), total)
	}
}

func TestListJoinTokens_WithTokens(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateJoinToken("tok-a", JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
		NodeGroup: "workers",
	}, "tenant-a", "workers")
	_, _ = s.CreateJoinToken("tok-b", JoinTokenSpec{
		ExpiresAt: time.Now().UTC().Add(2 * time.Hour),
		NodeGroup: "control",
	}, "tenant-b", "control")

	items, total, err := s.ListJoinTokens()
	if err != nil {
		t.Fatalf("ListJoinTokens: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 tokens, got %d items (total=%d)", len(items), total)
	}

	// Verify labels are preserved.
	haveTenantA := false
	haveTenantB := false
	for _, jt := range items {
		if jt.Metadata.Labels["rezuscloud.io/tenant"] == "tenant-a" {
			haveTenantA = true
		}
		if jt.Metadata.Labels["rezuscloud.io/tenant"] == "tenant-b" {
			haveTenantB = true
		}
	}
	if !haveTenantA || !haveTenantB {
		t.Errorf("labels not preserved; tenant-a=%v tenant-b=%v", haveTenantA, haveTenantB)
	}
}

func TestListJoinTokensByTenant(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateJoinToken("tok-a", JoinTokenSpec{NodeGroup: "workers"}, "tenant-a", "workers")
	_, _ = s.CreateJoinToken("tok-b", JoinTokenSpec{NodeGroup: "control"}, "tenant-b", "control")

	items, total, err := s.ListJoinTokensByTenant("tenant-a")
	if err != nil {
		t.Fatalf("ListJoinTokensByTenant: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 token for tenant-a, got %d items (total=%d)", len(items), total)
	}
	if items[0].Spec.NodeGroup != "workers" {
		t.Errorf("NodeGroup = %q, want workers", items[0].Spec.NodeGroup)
	}
}

func TestGetJoinToken(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateJoinToken("mytoken", JoinTokenSpec{NodeGroup: "workers"}, "tenant-a", "workers")

	// Existing.
	jt, err := s.GetJoinToken("mytoken")
	if err != nil {
		t.Fatalf("GetJoinToken: %v", err)
	}
	if jt == nil {
		t.Fatal("expected non-nil token")
	}
	if jt.Spec.NodeGroup != "workers" {
		t.Errorf("NodeGroup = %q, want workers", jt.Spec.NodeGroup)
	}

	// Non-existent returns nil (not error).
	missing, err := s.GetJoinToken("does-not-exist")
	if err != nil {
		t.Fatalf("GetJoinToken(missing): %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing token")
	}
}

func TestDeleteJoinToken(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateJoinToken("removable", JoinTokenSpec{NodeGroup: "workers"}, "tenant-a", "workers")

	if err := s.DeleteJoinToken("removable"); err != nil {
		t.Fatalf("DeleteJoinToken: %v", err)
	}

	jt, _ := s.GetJoinToken("removable")
	if jt != nil {
		t.Error("expected token to be removed")
	}

	// Deleting non-existent is also OK (idempotent).
	if err := s.DeleteJoinToken("removable"); err != nil {
		t.Errorf("second delete should be idempotent: %v", err)
	}
}

// --- User Tests ---

func TestUser_CRUD(t *testing.T) {
	s := openTestStore(t)

	// Create.
	user, err := s.CreateUser("admin", UserSpec{
		Role:         "admin",
		PasswordHash: "$2a$10$hash",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Metadata.Name != "admin" {
		t.Errorf("name = %q, want %q", user.Metadata.Name, "admin")
	}
	if user.Spec.Role != "admin" {
		t.Errorf("role = %q, want %q", user.Spec.Role, "admin")
	}

	// Get.
	got, err := s.GetUser("admin")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got == nil {
		t.Fatal("user should exist")
	}

	// List.
	users, err := s.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("users = %d, want 1", len(users))
	}

	// Update.
	updated, err := s.UpdateUser("admin", got.Metadata.ResourceVersion, UserSpec{
		Role:         "edit",
		PasswordHash: "$2a$10$newhash",
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.Spec.Role != "edit" {
		t.Errorf("role = %q, want %q", updated.Spec.Role, "edit")
	}

	// Delete.
	if err := s.DeleteUser("admin"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	deleted, _ := s.GetUser("admin")
	if deleted != nil {
		t.Error("user should be nil after delete")
	}
}

// --- Finalizer Tests ---

func TestFinalizers_Teardown(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateTenant("test", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// Delete triggers finalizers.
	_ = s.DeleteTenant("test")

	tenant, _ := s.GetTenant("test")
	if tenant.Metadata.DeletionTimestamp == nil {
		t.Fatal("deletionTimestamp should be set")
	}
	if len(tenant.Metadata.Finalizers) != 3 {
		t.Fatalf("finalizers = %d, want 3", len(tenant.Metadata.Finalizers))
	}

	// Remove first finalizer.
	removed, err := s.RemoveFinalizer("tenant", "test", "rezuscloud.io/machines")
	if err != nil {
		t.Fatalf("RemoveFinalizer: %v", err)
	}
	if !removed {
		t.Error("finalizer should be removed")
	}

	tenant, _ = s.GetTenant("test")
	if tenant == nil {
		t.Fatal("tenant should still exist (2 finalizers left)")
	}
	if len(tenant.Metadata.Finalizers) != 2 {
		t.Errorf("finalizers = %d, want 2", len(tenant.Metadata.Finalizers))
	}

	// Remove remaining finalizers — last one should trigger permanent deletion.
	_, _ = s.RemoveFinalizer("tenant", "test", "rezuscloud.io/secrets")
	_, _ = s.RemoveFinalizer("tenant", "test", "rezuscloud.io/tokens")

	gone, _ := s.GetTenant("test")
	if gone != nil {
		t.Error("tenant should be permanently deleted after all finalizers cleared")
	}
}

func TestFinalizer_Idempotent(t *testing.T) {
	s := openTestStore(t)

	_, _ = s.CreateTenant("test", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)

	// Add same finalizer twice.
	_ = s.AddFinalizer("tenant", "test", "rezuscloud.io/test")
	_ = s.AddFinalizer("tenant", "test", "rezuscloud.io/test")

	tenant, _ := s.GetTenant("test")
	count := 0
	for _, f := range tenant.Metadata.Finalizers {
		if f == "rezuscloud.io/test" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate finalizer count = %d, want 1", count)
	}
}

// recordingBus is a state.EventBus that records every event for assertions.
type recordingBus struct {
	events []ResourceEvent
}

func (r *recordingBus) Publish(resourceType string, event ResourceEvent) {
	r.events = append(r.events, event)
}

func TestStore_PublishesBusEvents(t *testing.T) {
	s := openTestStore(t)
	bus := &recordingBus{}
	s.SetBus(bus)

	t.Run("create publishes ADDED", func(t *testing.T) {
		bus.events = nil
		_, err := s.CreateTenant("alpha", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(bus.events) != 1 {
			t.Fatalf("events = %d, want 1", len(bus.events))
		}
		if bus.events[0].Type != "ADDED" {
			t.Errorf("type = %q, want ADDED", bus.events[0].Type)
		}
		if bus.events[0].ResourceType != "tenant" {
			t.Errorf("resource type = %q, want tenant", bus.events[0].ResourceType)
		}
		if bus.events[0].Metadata.Name != "alpha" {
			t.Errorf("name = %q, want alpha", bus.events[0].Metadata.Name)
		}
	})

	t.Run("update status publishes MODIFIED", func(t *testing.T) {
		bus.events = nil
		_, err := s.UpdateTenantStatus("alpha", TenantStatus{Phase: TenantActive})
		if err != nil {
			t.Fatalf("update status: %v", err)
		}
		if len(bus.events) != 1 {
			t.Fatalf("events = %d, want 1", len(bus.events))
		}
		if bus.events[0].Type != "MODIFIED" {
			t.Errorf("type = %q, want MODIFIED", bus.events[0].Type)
		}
	})

	t.Run("delete publishes DELETED", func(t *testing.T) {
		bus.events = nil
		err := s.DeleteTenant("alpha")
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if len(bus.events) != 1 {
			t.Fatalf("events = %d, want 1", len(bus.events))
		}
		if bus.events[0].Type != "DELETED" {
			t.Errorf("type = %q, want DELETED", bus.events[0].Type)
		}
	})
}

func TestStore_NoBusIsSafe(t *testing.T) {
	// No SetBus call — store must still work without panicking.
	s := openTestStore(t)

	_, err := s.CreateTenant("beta", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = s.UpdateTenantStatus("beta", TenantStatus{Phase: TenantActive})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.DeleteTenant("beta"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// --- Tenant Secrets ---

func TestStore_TenantSecrets_RoundTrip(t *testing.T) {
	s := openTestStore(t)

	// Create a tenant first (FK constraint).
	_, err := s.CreateTenant("with-secrets", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// Initially no secrets.
	bundle, err := s.LoadTenantSecrets("with-secrets")
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	if bundle != nil {
		t.Errorf("expected nil bundle initially, got %d bytes", len(bundle))
	}

	// Save a bundle.
	payload := []byte(`{"version":"v1","data":"topsecret"}`)
	if err := s.SaveTenantSecrets("with-secrets", payload); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load it back.
	loaded, err := s.LoadTenantSecrets("with-secrets")
	if err != nil {
		t.Fatalf("load after save: %v", err)
	}
	if string(loaded) != string(payload) {
		t.Errorf("bundle mismatch: got %q, want %q", string(loaded), string(payload))
	}
}

func TestStore_TenantSecrets_Overwrite(t *testing.T) {
	s := openTestStore(t)
	_, err := s.CreateTenant("overwrite", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = s.SaveTenantSecrets("overwrite", []byte("first"))
	_ = s.SaveTenantSecrets("overwrite", []byte("second"))

	loaded, err := s.LoadTenantSecrets("overwrite")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(loaded) != "second" {
		t.Errorf("expected overwrite to 'second', got %q", string(loaded))
	}
}

func TestStore_TenantSecrets_Remove(t *testing.T) {
	s := openTestStore(t)
	_, err := s.CreateTenant("removable", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = s.SaveTenantSecrets("removable", []byte("data"))
	if err := s.RemoveTenantSecrets("removable"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	loaded, err := s.LoadTenantSecrets("removable")
	if err != nil {
		t.Fatalf("load after remove: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil after remove, got %d bytes", len(loaded))
	}
}

func TestStore_TenantSecrets_RemoveNonExistent(t *testing.T) {
	s := openTestStore(t)
	// Should not error when removing secrets for a non-existent tenant.
	if err := s.RemoveTenantSecrets("never-existed"); err != nil {
		t.Errorf("remove non-existent should be no-op, got: %v", err)
	}
}

func TestStore_TenantSecrets_LoadNonExistent(t *testing.T) {
	s := openTestStore(t)
	loaded, err := s.LoadTenantSecrets("never-existed")
	if err != nil {
		t.Errorf("load non-existent should not error, got: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for non-existent, got %d bytes", len(loaded))
	}
}
