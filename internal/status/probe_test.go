package status

import (
	"context"
	"errors"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func TestTalosMachineProbe_ProbesControlPlaneFirst(t *testing.T) {
	store := openStatusTestStore(t)
	_, _ = store.CreateTenant("t1", state.TenantSpec{}, nil, nil)
	_, _ = store.CreateMachine("worker-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil)
	_, _ = store.UpdateMachineStatus("worker-0", state.MachineStatus{Role: "worker"})
	_, _ = store.CreateMachine("cp-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil)
	_, _ = store.UpdateMachineStatus("cp-0", state.MachineStatus{Role: "controlplane"})

	var probed []string
	vf := func(_ context.Context, machineID string) (string, error) {
		probed = append(probed, machineID)
		if machineID == "cp-0" {
			return "1.12.6", nil
		}
		return "", errors.New("unreachable")
	}

	probe := NewTalosMachineProbe(store, vf)
	ver, err := probe.ProbeTenant(context.Background(), "t1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "1.12.6" {
		t.Errorf("version = %q, want 1.12.6", ver)
	}
	// Should only have probed the control-plane machine.
	if len(probed) != 1 || probed[0] != "cp-0" {
		t.Errorf("probed = %v, want [cp-0]", probed)
	}
}

func TestTalosMachineProbe_FallsBackToWorkers(t *testing.T) {
	store := openStatusTestStore(t)
	_, _ = store.CreateTenant("t1", state.TenantSpec{}, nil, nil)
	_, _ = store.CreateMachine("cp-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil)
	_, _ = store.UpdateMachineStatus("cp-0", state.MachineStatus{Role: "controlplane"})
	_, _ = store.CreateMachine("worker-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil)
	_, _ = store.UpdateMachineStatus("worker-0", state.MachineStatus{Role: "worker"})

	vf := func(_ context.Context, machineID string) (string, error) {
		if machineID == "worker-0" {
			return "1.12.6", nil
		}
		return "", errors.New("unreachable")
	}

	probe := NewTalosMachineProbe(store, vf)
	ver, err := probe.ProbeTenant(context.Background(), "t1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "1.12.6" {
		t.Errorf("version = %q, want 1.12.6", ver)
	}
}

func TestTalosMachineProbe_AllUnreachable(t *testing.T) {
	store := openStatusTestStore(t)
	_, _ = store.CreateTenant("t1", state.TenantSpec{}, nil, nil)
	_, _ = store.CreateMachine("cp-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil)
	_, _ = store.UpdateMachineStatus("cp-0", state.MachineStatus{Role: "controlplane"})

	vf := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("connection refused")
	}

	probe := NewTalosMachineProbe(store, vf)
	_, err := probe.ProbeTenant(context.Background(), "t1", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTalosMachineProbe_NoMachines(t *testing.T) {
	store := openStatusTestStore(t)
	_, _ = store.CreateTenant("t1", state.TenantSpec{}, nil, nil)

	probe := NewTalosMachineProbe(store, func(context.Context, string) (string, error) {
		t.Fatal("should not probe when no machines")
		return "", nil
	})
	_, err := probe.ProbeTenant(context.Background(), "t1", nil)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
}

// openStatusTestStore creates a temporary SQLite store for status tests.
func openStatusTestStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
