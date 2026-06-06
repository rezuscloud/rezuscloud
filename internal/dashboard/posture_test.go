package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// newTestStore opens a state.Store against a per-test tempdir.
func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "posture.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// createTenant is a minimal helper to seed a tenant (no node groups yet).
func createTenant(t *testing.T, store *state.Store, name string) {
	t.Helper()
	_, err := store.CreateResource("tenant", name, state.TenantSpec{
		KubernetesVersion: "1.35.0",
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

// addNodeGroup creates a nodegroup resource for a tenant with the given count.
func addNodeGroup(t *testing.T, store *state.Store, name, tenant string, count int) {
	t.Helper()
	_, err := store.CreateResource("nodegroup", name,
		state.NodeGroupSpec{Role: "worker", Count: count},
		nil,
		map[string]string{"rezuscloud.io/tenant": tenant},
		nil)
	if err != nil {
		t.Fatalf("create nodegroup: %v", err)
	}
}

// addMachine inserts a machine attached to the given tenant with the given stage.
func addMachine(t *testing.T, store *state.Store, id, tenant, role string, stage state.MachineStage, connected bool) {
	t.Helper()
	_, err := store.CreateMachine(id, state.MachineSpec{
		Connected: connected,
	}, map[string]string{"rezuscloud.io/tenant": tenant}, nil)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	if _, err := store.UpdateMachineStatus(id, state.MachineStatus{Stage: stage, Role: role, Ready: stage == state.StageReady}); err != nil {
		t.Fatalf("update status: %v", err)
	}
}

// TestBuilder_ClusterPosture_UsesRealMachines_NotMetadata is the regression
// test for the silent bug fixed in #42. With the prior inline implementation,
// dashboardPosture called ComputeTenantStatus(tenant, nil, nil), classifying
// the tenant as "forming" even when its machine fleet was fully ready.
// The Builder must use the real machines and classify the tenant as "active".
func TestBuilder_ClusterPosture_UsesRealMachines_NotMetadata(t *testing.T) {
	store := newTestStore(t)
	createTenant(t, store, "prod")
	addNodeGroup(t, store, "default", "prod", 1)
	addMachine(t, store, "m1", "prod", "controlplane", state.StageReady, true)

	builder := NewBuilder(Deps{Store: store})
	posture := builder.Build(context.Background())

	if posture.Clusters.Active != 1 {
		t.Errorf("Active = %d, want 1 (this is the bug fix — prior code returned Forming=1)", posture.Clusters.Active)
	}
	if posture.Clusters.Forming != 0 {
		t.Errorf("Forming = %d, want 0", posture.Clusters.Forming)
	}
	if posture.Clusters.Ready != 1 {
		t.Errorf("Ready = %d, want 1", posture.Clusters.Ready)
	}
	if posture.Clusters.Expected != 1 {
		t.Errorf("Expected = %d, want 1", posture.Clusters.Expected)
	}
}

// TestBuilder_ClusterPosture_FormingWhenMachinesMissing verifies that a
// tenant with no machines is classified as forming (the correct path,
// not a regression).
func TestBuilder_ClusterPosture_FormingWhenMachinesMissing(t *testing.T) {
	store := newTestStore(t)
	createTenant(t, store, "prod")
	addNodeGroup(t, store, "default", "prod", 3) // expects 3 machines, none present

	builder := NewBuilder(Deps{Store: store})
	posture := builder.Build(context.Background())

	if posture.Clusters.Active != 0 {
		t.Errorf("Active = %d, want 0", posture.Clusters.Active)
	}
	if posture.Clusters.Forming != 1 {
		t.Errorf("Forming = %d, want 1", posture.Clusters.Forming)
	}
	if posture.Clusters.Expected != 3 {
		t.Errorf("Expected = %d, want 3", posture.Clusters.Expected)
	}
	if posture.Clusters.Ready != 0 {
		t.Errorf("Ready = %d, want 0", posture.Clusters.Ready)
	}
}

// TestBuilder_MachinePosture_Bucketing verifies pending/failed/connected counts.
func TestBuilder_MachinePosture_Bucketing(t *testing.T) {
	store := newTestStore(t)
	createTenant(t, store, "prod")
	addNodeGroup(t, store, "default", "prod", 5)
	// 2 ready + connected, 1 installing (pending, connected), 1 off (failed), 1 not connected
	addMachine(t, store, "m1", "prod", "worker", state.StageReady, true)
	addMachine(t, store, "m2", "prod", "worker", state.StageReady, true)
	addMachine(t, store, "m3", "prod", "worker", state.StageInstalling, true)
	addMachine(t, store, "m4", "prod", "worker", state.StageOff, false)
	addMachine(t, store, "m5", "prod", "worker", state.StageInitializing, false)

	builder := NewBuilder(Deps{Store: store})
	posture := builder.Build(context.Background())

	if posture.Machines.Total != 5 {
		t.Errorf("Total = %d, want 5", posture.Machines.Total)
	}
	if posture.Machines.Connected != 3 {
		t.Errorf("Connected = %d, want 3", posture.Machines.Connected)
	}
	if posture.Machines.Pending != 2 {
		t.Errorf("Pending = %d, want 2", posture.Machines.Pending)
	}
	if posture.Machines.Failed != 1 {
		t.Errorf("Failed = %d, want 1", posture.Machines.Failed)
	}
}

// TestBuilder_BackupAdapter verifies the backup adapter is consulted.
func TestBuilder_BackupAdapter(t *testing.T) {
	store := newTestStore(t)
	builder := NewBuilder(Deps{
		Store: store,
		Backup: &fakeBackupReader{
			snaps: []BackupSnapshot{
				{CreatedAt: time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339), Status: BackupSnapshotStatus{Status: "success"}},
				{CreatedAt: "2026-01-01T00:00:00Z", Status: BackupSnapshotStatus{Status: "failed"}},
			},
		},
	})
	posture := builder.Build(context.Background())
	if posture.Backups.Failures != 1 {
		t.Errorf("Failures = %d, want 1", posture.Backups.Failures)
	}
	if posture.Backups.LastSuccess == "never" {
		t.Errorf("LastSuccess = %q, want a real timestamp", posture.Backups.LastSuccess)
	}
}

// TestBuilder_UpgradeAdapter verifies the upgrade adapter is consulted.
func TestBuilder_UpgradeAdapter(t *testing.T) {
	store := newTestStore(t)
	createTenant(t, store, "prod")
	addNodeGroup(t, store, "default", "prod", 1)

	builder := NewBuilder(Deps{
		Store: store,
		Upgrades: &fakeUpgradeReader{
			runs: map[string][]UpgradeRun{
				"prod": {
					{Tenant: "prod", Target: "1.13.0", Phase: "running", Started: time.Now()},
				},
			},
		},
	})
	posture := builder.Build(context.Background())
	if posture.Upgrades.ActiveRuns != 1 {
		t.Errorf("ActiveRuns = %d, want 1", posture.Upgrades.ActiveRuns)
	}
	if posture.Upgrades.LatestTarget != "1.13.0" {
		t.Errorf("LatestTarget = %q, want 1.13.0", posture.Upgrades.LatestTarget)
	}
}

// TestBuilder_NilStoreReturnsZero ensures Build is safe with no store.
func TestBuilder_NilStoreReturnsZero(t *testing.T) {
	builder := NewBuilder(Deps{})
	posture := builder.Build(context.Background())
	if !isZeroPosture(posture) {
		t.Errorf("expected zero-value Posture, got %+v", posture)
	}
}

func isZeroPosture(p Posture) bool {
	return len(p.Clusters.Erroring) == 0 &&
		p.Clusters.Active == 0 && p.Clusters.Forming == 0 && p.Clusters.Removing == 0 &&
		p.Clusters.Ready == 0 && p.Clusters.Expected == 0 &&
		p.Machines == MachinePosture{} &&
		p.Providers == ProviderPosture{} &&
		p.Backups == BackupPosture{} &&
		p.Upgrades == UpgradePosture{}
}

// TestRPOLabel covers the human-readable label.
func TestRPOLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "unknown"},
		{"never", "unknown"},
		{"unavailable", "unknown"},
		{"garbage", "unknown"},
		{time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339), "<1m"},
		{time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339), "5m"},
		{time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339), "2h"},
	}
	for _, c := range cases {
		if got := rpoEstimate(c.in); got != c.want {
			t.Errorf("rpoEstimate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- fakes ---

type fakeBackupReader struct {
	snaps []BackupSnapshot
	err   error
}

func (f *fakeBackupReader) ListSnapshots() ([]BackupSnapshot, error) {
	return f.snaps, f.err
}

type fakeUpgradeReader struct {
	runs map[string][]UpgradeRun
}

func (f *fakeUpgradeReader) ListRuns(tenant string) ([]UpgradeRun, error) {
	return f.runs[tenant], nil
}
