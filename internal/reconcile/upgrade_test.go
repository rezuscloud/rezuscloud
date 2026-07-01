package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// fakeUpgradeRunner records RunUpgrade calls and optionally returns an error.
type fakeUpgradeRunner struct {
	calls []upgradeCall
	err   error
}

type upgradeCall struct {
	tenant, component, currentVersion, targetVersion string
}

func (f *fakeUpgradeRunner) RunUpgrade(_ context.Context, tenant, component, currentVersion, targetVersion string) error {
	f.calls = append(f.calls, upgradeCall{tenant, component, currentVersion, targetVersion})
	return f.err
}

func newUpgradeApplierTestStore(t *testing.T) *state.Store {
	t.Helper()
	s := openTestStore(t)
	_, err := s.CreateTenant("t1", state.TenantSpec{
		KubernetesVersion: "1.36.0",
		TalosVersion:      "1.13.0",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addMachineWithVersion(t *testing.T, store *state.Store, id, role, talosVer, k8sVer string) {
	t.Helper()
	spec := state.MachineSpec{Connected: true}
	status := state.MachineStatus{Role: role, Stage: state.StageReady, Ready: true, TalosVersion: talosVer, K8sVersion: k8sVer}
	labels := map[string]string{"rezuscloud.io/tenant": "t1"}
	_, err := store.CreateResource("machine", id, spec, status, labels, nil)
	if err != nil {
		t.Fatalf("create machine %s: %v", id, err)
	}
}

func TestSharedVersion_AllAgree(t *testing.T) {
	machines := []*state.Machine{
		{Status: state.MachineStatus{TalosVersion: "1.12.6"}},
		{Status: state.MachineStatus{TalosVersion: "1.12.6"}},
	}
	if got := sharedVersion(machines, func(m *state.Machine) string { return m.Status.TalosVersion }); got != "1.12.6" {
		t.Errorf("sharedVersion = %q, want 1.12.6", got)
	}
}

func TestSharedVersion_Disagree(t *testing.T) {
	machines := []*state.Machine{
		{Status: state.MachineStatus{TalosVersion: "1.12.6"}},
		{Status: state.MachineStatus{TalosVersion: "1.13.0"}},
	}
	if got := sharedVersion(machines, func(m *state.Machine) string { return m.Status.TalosVersion }); got != "" {
		t.Errorf("sharedVersion = %q, want empty (disagreement)", got)
	}
}

func TestSharedVersion_EmptyIsUnknown(t *testing.T) {
	machines := []*state.Machine{
		{Status: state.MachineStatus{TalosVersion: "1.12.6"}},
		{Status: state.MachineStatus{TalosVersion: ""}},
	}
	if got := sharedVersion(machines, func(m *state.Machine) string { return m.Status.TalosVersion }); got != "" {
		t.Errorf("sharedVersion = %q, want empty (one machine has no version)", got)
	}
}

func TestRunUpgradesIfNeeded_DriftTriggersUpgrade(t *testing.T) {
	store := newUpgradeApplierTestStore(t)
	// Machines on old versions; spec wants new ones.
	addMachineWithVersion(t, store, "m1", "controlplane", "1.12.6", "1.35.0")

	runner := &fakeUpgradeRunner{}
	a := &Applier{store: store, upgrades: runner, logf: func(string, ...any) {}}

	tenant, _ := store.GetTenant("t1")
	if err := a.runUpgradesIfNeeded(context.Background(), "t1", tenant); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only talos drift triggers an upgrade (k8s is handled implicitly by the
	// talos upgrade for Talos-managed clusters).
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 upgrade call (talos only), got %d: %+v", len(runner.calls), runner.calls)
	}
	c := runner.calls[0]
	if c.component != "talos" || c.currentVersion != "1.12.6" || c.targetVersion != "1.13.0" {
		t.Errorf("talos upgrade = %+v, want component=talos 1.12.6→1.13.0", c)
	}
}

func TestRunUpgradesIfNeeded_NoDriftNoUpgrade(t *testing.T) {
	store := newUpgradeApplierTestStore(t)
	// Machines already at the spec versions.
	addMachineWithVersion(t, store, "m1", "controlplane", "1.13.0", "1.36.0")

	runner := &fakeUpgradeRunner{}
	a := &Applier{store: store, upgrades: runner, logf: func(string, ...any) {}}

	tenant, _ := store.GetTenant("t1")
	if err := a.runUpgradesIfNeeded(context.Background(), "t1", tenant); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected no upgrades, got %d", len(runner.calls))
	}
}

func TestRunUpgradesIfNeeded_NoMachinesSkips(t *testing.T) {
	store := newUpgradeApplierTestStore(t)
	// No machines (forming tenant, not upgrading).
	runner := &fakeUpgradeRunner{}
	a := &Applier{store: store, upgrades: runner, logf: func(string, ...any) {}}

	tenant, _ := store.GetTenant("t1")
	if err := a.runUpgradesIfNeeded(context.Background(), "t1", tenant); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected no upgrades for forming tenant, got %d", len(runner.calls))
	}
}

func TestRunUpgradesIfNeeded_UpgradeFailurePropagates(t *testing.T) {
	store := newUpgradeApplierTestStore(t)
	addMachineWithVersion(t, store, "m1", "controlplane", "1.12.6", "1.35.0")

	runner := &fakeUpgradeRunner{err: errors.New("upgrade failed")}
	a := &Applier{store: store, upgrades: runner, logf: func(string, ...any) {}}

	tenant, _ := store.GetTenant("t1")
	err := a.runUpgradesIfNeeded(context.Background(), "t1", tenant)
	if err == nil {
		t.Fatal("expected upgrade failure to propagate")
	}
}

func TestRunUpgradesIfNeeded_NilRunnerSkips(t *testing.T) {
	store := newUpgradeApplierTestStore(t)
	addMachineWithVersion(t, store, "m1", "controlplane", "1.12.6", "1.35.0")

	// No UpgradeRunner configured — the hook is skipped (backwards compatible).
	a := &Applier{store: store, logf: func(string, ...any) {}}

	tenant, _ := store.GetTenant("t1")
	if err := a.runUpgradesIfNeeded(context.Background(), "t1", tenant); err != nil {
		t.Fatalf("expected no error without runner, got: %v", err)
	}
}
