package statemachine

import (
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func TestCanTransitionTo_SameStage(t *testing.T) {
	if !CanTransitionTo(state.StageReady, state.StageReady) {
		t.Error("same stage should be valid (idempotent)")
	}
}

func TestCanTransitionTo_ValidPaths(t *testing.T) {
	tests := []struct {
		from, to state.MachineStage
		want     bool
	}{
		{state.StageInitializing, state.StageInstalling, true},
		{state.StageInitializing, state.StageConfiguring, true},
		{state.StageInstalling, state.StageConfiguring, true},
		{state.StageConfiguring, state.StageReady, true},
		{state.StageReady, state.StageRestarting, true},
		{state.StageReady, state.StageUpdating, true},
		{state.StageReady, state.StageStopping, true},
		{state.StageRestarting, state.StageReady, true},
		{state.StageUpdating, state.StageReady, true},
		{state.StageOff, state.StageInitializing, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			got := CanTransitionTo(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("CanTransitionTo(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestCanTransitionTo_InvalidPaths(t *testing.T) {
	tests := []struct {
		from, to state.MachineStage
	}{
		{state.StageOff, state.StageReady},
		{state.StageOff, state.StageConfiguring},
		{state.StageStopping, state.StageReady},
		{state.StageRemoving, state.StageReady},
		{state.StageInitializing, state.StageReady},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			if CanTransitionTo(tt.from, tt.to) {
				t.Errorf("CanTransitionTo(%s, %s) should be invalid", tt.from, tt.to)
			}
		})
	}
}

func TestIsReady(t *testing.T) {
	tests := []struct {
		name      string
		stage     state.MachineStage
		healthy   bool
		wantReady bool
	}{
		{"ready + healthy", state.StageReady, true, true},
		{"ready + unhealthy", state.StageReady, false, false},
		{"configuring + healthy", state.StageConfiguring, true, false},
		{"installing + healthy", state.StageInstalling, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsReady(tt.stage, tt.healthy)
			if got != tt.wantReady {
				t.Errorf("IsReady(%s, %v) = %v, want %v", tt.stage, tt.healthy, got, tt.wantReady)
			}
		})
	}
}

func TestDeriveMachineStatus_StageUpdate(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageInitializing}

	updated := DeriveMachineStatus(current, MachineEvent{
		Stage: state.StageConfiguring,
	})

	if updated.Stage != state.StageConfiguring {
		t.Errorf("stage = %q, want %q", updated.Stage, state.StageConfiguring)
	}
}

func TestDeriveMachineStatus_ReadyDerivation(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageConfiguring}

	updated := DeriveMachineStatus(current, MachineEvent{
		Stage:           state.StageReady,
		ServicesHealthy: BoolPtr(true),
	})

	if !updated.Ready {
		t.Error("should be ready when stage=ready and services healthy")
	}
}

func TestDeriveMachineStatus_NotReadyWithUnhealthyServices(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageReady}

	updated := DeriveMachineStatus(current, MachineEvent{
		Stage:           state.StageReady,
		ServicesHealthy: BoolPtr(false),
	})

	if updated.Ready {
		t.Error("should not be ready with unhealthy services")
	}
}

func TestDeriveMachineStatus_HardwareInfo(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageReady}

	hw := &state.HardwareInfo{
		Processors: []state.ProcessorInfo{
			{CoreCount: 8, Frequency: 3000},
		},
	}

	updated := DeriveMachineStatus(current, MachineEvent{
		Hardware: hw,
	})

	if updated.Hardware == nil {
		t.Fatal("hardware should be set")
	}
	if len(updated.Hardware.Processors) != 1 {
		t.Errorf("processors = %d, want 1", len(updated.Hardware.Processors))
	}
}

func TestDeriveMachineStatus_ErrorClearsOnReady(t *testing.T) {
	current := state.MachineStatus{
		Stage:     state.StageConfiguring,
		LastError: "config apply failed",
	}

	updated := DeriveMachineStatus(current, MachineEvent{
		Stage: state.StageReady,
	})

	if updated.LastError != "" {
		t.Errorf("lastError = %q, want empty on stage=ready", updated.LastError)
	}
}

func TestDeriveMachineStatus_SetsError(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageConfiguring}

	updated := DeriveMachineStatus(current, MachineEvent{
		Stage: state.StageConfiguring,
		Error: "disk full",
	})

	if updated.LastError != "disk full" {
		t.Errorf("lastError = %q, want %q", updated.LastError, "disk full")
	}
}

func TestDeriveMachineStatus_VersionTracking(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageReady}

	updated := DeriveMachineStatus(current, MachineEvent{
		TalosVersion: "1.12.6",
		K8sVersion:   "1.35.0",
	})

	if updated.TalosVersion != "1.12.6" {
		t.Errorf("talosVersion = %q, want %q", updated.TalosVersion, "1.12.6")
	}
	if updated.K8sVersion != "1.35.0" {
		t.Errorf("k8sVersion = %q, want %q", updated.K8sVersion, "1.35.0")
	}
}

func TestDeriveMachineStatus_SchematicInfo(t *testing.T) {
	current := state.MachineStatus{Stage: state.StageReady}

	schematic := &state.SchematicInfo{
		ID:         "abc123",
		Extensions: []string{"amd-ucode", "gvisor"},
	}

	updated := DeriveMachineStatus(current, MachineEvent{
		Schematic: schematic,
	})

	if updated.Schematic == nil {
		t.Fatal("schematic should be set")
	}
	if updated.Schematic.ID != "abc123" {
		t.Errorf("schematic ID = %q, want %q", updated.Schematic.ID, "abc123")
	}
}
