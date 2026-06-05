package statemachine

import (
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// MachineStageTransition defines valid stage transitions.
// Not all transitions are valid — a machine can't go from "off" to "ready" directly.
var validTransitions = map[state.MachineStage][]state.MachineStage{
	state.StageInitializing: {state.StageInstalling, state.StageConfiguring, state.StageOff},
	state.StageInstalling:   {state.StageConfiguring, state.StageOff, state.StageRemoving},
	state.StageConfiguring:  {state.StageReady, state.StageRestarting, state.StageOff, state.StageRemoving},
	state.StageReady:        {state.StageRestarting, state.StageUpdating, state.StageStopping, state.StageOff, state.StageRemoving},
	state.StageRestarting:   {state.StageInitializing, state.StageConfiguring, state.StageReady, state.StageOff},
	state.StageStopping:     {state.StageOff},
	state.StageOff:          {state.StageInitializing},
	state.StageUpdating:     {state.StageConfiguring, state.StageReady, state.StageOff},
	state.StageRemoving:     {state.StageOff},
}

// CanTransitionTo checks if a transition from current to next stage is valid.
func CanTransitionTo(current, next state.MachineStage) bool {
	// Same stage is always valid (idempotent update).
	if current == next {
		return true
	}

	allowed, ok := validTransitions[current]
	if !ok {
		return false
	}

	for _, s := range allowed {
		if s == next {
			return true
		}
	}
	return false
}

// IsReady determines if a machine is ready based on stage and service health.
// A machine is ready when stage is "ready" and all services report healthy.
func IsReady(stage state.MachineStage, servicesHealthy bool) bool {
	return stage == state.StageReady && servicesHealthy
}

// DeriveMachineStatus computes the effective machine status from observed data.
// This is called when MachineLink reports a new state.
func DeriveMachineStatus(current state.MachineStatus, event MachineEvent) state.MachineStatus {
	updated := current

	// Update stage if provided.
	if event.Stage != "" {
		updated.Stage = event.Stage
	}

	// Update ready based on stage + services.
	if event.ServicesHealthy != nil {
		updated.Ready = IsReady(updated.Stage, *event.ServicesHealthy)
	}

	// Update hardware info.
	if event.Hardware != nil {
		updated.Hardware = event.Hardware
	}

	// Update network info.
	if event.Network != nil {
		updated.Network = event.Network
	}

	// Update schematic info.
	if event.Schematic != nil {
		updated.Schematic = event.Schematic
	}

	// Update config status.
	if event.ConfigCurrent != nil {
		updated.ConfigCurrent = *event.ConfigCurrent
	}

	// Clear error on successful transitions.
	if event.Stage == state.StageReady {
		updated.LastError = ""
	}

	// Set error if provided.
	if event.Error != "" {
		updated.LastError = event.Error
	}

	// Derive role from labels if not set.
	if updated.Role == "" && event.Role != "" {
		updated.Role = event.Role
	}

	// Track versions.
	if event.TalosVersion != "" {
		updated.TalosVersion = event.TalosVersion
	}
	if event.K8sVersion != "" {
		updated.K8sVersion = event.K8sVersion
	}

	return updated
}

// MachineEvent represents an observation from MachineLink.
type MachineEvent struct {
	Stage           state.MachineStage
	ServicesHealthy *bool
	Hardware        *state.HardwareInfo
	Network         *state.NetworkInfo
	Schematic       *state.SchematicInfo
	ConfigCurrent   *bool
	Error           string
	Role            string
	TalosVersion    string
	K8sVersion      string
}

// BoolPtr returns a pointer to the given bool.
func BoolPtr(b bool) *bool {
	return &b
}
