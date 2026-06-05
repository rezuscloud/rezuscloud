package statemachine

import (
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func TestComputeTenantPhase_NoMachines(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0"},
	}

	phase := ComputeTenantPhase(tenant, nil, nil)
	if phase != state.TenantForming {
		t.Errorf("phase = %q, want %q", phase, state.TenantForming)
	}
}

func TestComputeTenantPhase_Removing(t *testing.T) {
	ts := time.Now().UTC()
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test", DeletionTimestamp: &ts},
	}

	phase := ComputeTenantPhase(tenant, nil, nil)
	if phase != state.TenantRemoving {
		t.Errorf("phase = %q, want %q", phase, state.TenantRemoving)
	}
}

func TestComputeTenantPhase_Active(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
	}

	machines := []*state.Machine{
		{Metadata: state.Metadata{Name: "m1", Labels: map[string]string{"rezuscloud.io/role": "controlplane"}}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
		{Metadata: state.Metadata{Name: "m2", Labels: map[string]string{"rezuscloud.io/role": "worker"}}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
	}

	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 1}}

	phase := ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantActive {
		t.Errorf("phase = %q, want %q", phase, state.TenantActive)
	}
}

func TestComputeTenantPhase_Forming_NotEnoughMachines(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
	}

	machines := []*state.Machine{
		{Metadata: state.Metadata{Name: "m1"}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
	}

	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 3}}

	phase := ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantForming {
		t.Errorf("phase = %q, want %q", phase, state.TenantForming)
	}
}

func TestComputeTenantPhase_Forming_NotAllReady(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
	}

	machines := []*state.Machine{
		{Metadata: state.Metadata{Name: "m1"}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
		{Metadata: state.Metadata{Name: "m2"}, Status: state.MachineStatus{Ready: false, Stage: state.StageInstalling}},
	}

	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 1}}

	phase := ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantForming {
		t.Errorf("phase = %q, want %q", phase, state.TenantForming)
	}
}

func TestComputeTenantPhase_Shrinking(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
	}

	machines := []*state.Machine{
		{Metadata: state.Metadata{Name: "m1"}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
		{Metadata: state.Metadata{Name: "m2"}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
		{Metadata: state.Metadata{Name: "m3"}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
	}

	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 1}}

	phase := ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantShrinking {
		t.Errorf("phase = %q, want %q", phase, state.TenantShrinking)
	}
}

func TestComputeTenantStatus_FullStatus(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
		Spec:     state.TenantSpec{KubernetesVersion: "1.35.0", TalosVersion: "1.12.6"},
	}

	machines := []*state.Machine{
		{Metadata: state.Metadata{Name: "m1", Labels: map[string]string{"rezuscloud.io/role": "controlplane"}}, Spec: state.MachineSpec{Connected: true}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
		{Metadata: state.Metadata{Name: "m2", Labels: map[string]string{"rezuscloud.io/role": "worker"}}, Spec: state.MachineSpec{Connected: true}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
	}

	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 1}}

	status := ComputeTenantStatus(tenant, machines, nodeGroups)

	if status.Phase != state.TenantActive {
		t.Errorf("phase = %q, want %q", status.Phase, state.TenantActive)
	}
	if !status.Available {
		t.Error("available should be true")
	}
	if !status.Ready {
		t.Error("ready should be true")
	}
	if !status.APIReady {
		t.Error("apiReady should be true")
	}
	if !status.ControlPlaneReady {
		t.Error("controlPlaneReady should be true")
	}
	if status.Machines.Total != 2 {
		t.Errorf("total = %d, want 2", status.Machines.Total)
	}
	if status.Machines.Healthy != 2 {
		t.Errorf("healthy = %d, want 2", status.Machines.Healthy)
	}
	if status.Machines.Connected != 2 {
		t.Errorf("connected = %d, want 2", status.Machines.Connected)
	}
	if status.KubernetesVersion != "1.35.0" {
		t.Errorf("k8s version = %q, want %q", status.KubernetesVersion, "1.35.0")
	}
}

func TestComputeTenantStatus_Forming_WithControlPlane(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
	}

	// Control plane ready but workers still forming.
	machines := []*state.Machine{
		{Metadata: state.Metadata{Name: "m1", Labels: map[string]string{"rezuscloud.io/role": "controlplane"}}, Status: state.MachineStatus{Ready: true, Stage: state.StageReady}},
		{Metadata: state.Metadata{Name: "m2", Labels: map[string]string{"rezuscloud.io/role": "worker"}}, Status: state.MachineStatus{Ready: false, Stage: state.StageInstalling}},
	}

	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 1}}

	status := ComputeTenantStatus(tenant, machines, nodeGroups)

	// Forming because not all machines ready.
	if status.Phase != state.TenantForming {
		t.Errorf("phase = %q, want %q", status.Phase, state.TenantForming)
	}
	// Available because control plane is up.
	if !status.Available {
		t.Error("available should be true (control plane is up)")
	}
	if status.Ready {
		t.Error("ready should be false (not all machines ready)")
	}
}

func TestComputeTenantPhase_Transitions(t *testing.T) {
	tenant := &state.Tenant{
		Metadata: state.Metadata{Name: "test"},
	}
	nodeGroups := []NodeGroupSummary{{Name: "cp", Count: 1}}

	// Start with no machines → forming.
	phase := ComputeTenantPhase(tenant, nil, nodeGroups)
	if phase != state.TenantForming {
		t.Errorf("no machines: phase = %q, want forming", phase)
	}

	// Add machine, not ready → forming.
	machines := []*state.Machine{
		{Status: state.MachineStatus{Ready: false, Stage: state.StageInstalling}},
	}
	phase = ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantForming {
		t.Errorf("not ready: phase = %q, want forming", phase)
	}

	// Machine becomes ready → active.
	machines[0].Status = state.MachineStatus{Ready: true, Stage: state.StageReady}
	phase = ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantActive {
		t.Errorf("ready: phase = %q, want active", phase)
	}

	// Scale up → forming.
	nodeGroups = []NodeGroupSummary{{Name: "cp", Count: 1}, {Name: "workers", Count: 2}}
	phase = ComputeTenantPhase(tenant, machines, nodeGroups)
	if phase != state.TenantForming {
		t.Errorf("scaled up: phase = %q, want forming", phase)
	}
}
