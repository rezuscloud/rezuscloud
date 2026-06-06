// Package upgrade implements rolling Talos and Kubernetes upgrades.
// Upgrades are triggered by declarative spec changes (talosVersion, kubernetesVersion)
// and executed machine-by-machine with health checks between each.
package upgrade

import "context"

// Phase represents the current upgrade phase.
type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhasePreCheck  Phase = "precheck"
	PhaseUpgrading Phase = "upgrading"
	PhaseRollback  Phase = "rollback"
	PhaseComplete  Phase = "complete"
	PhaseFailed    Phase = "failed"
	PhaseCanceled  Phase = "canceled"
)

// Status tracks the progress of an upgrade.
type Status struct {
	Phase          Phase  `json:"phase"`
	Component      string `json:"component"` // "talos" or "kubernetes"
	Target         string `json:"target"`    // target version
	Current        string `json:"current"`   // current version
	TotalMachines  int    `json:"totalMachines"`
	Completed      int    `json:"completed"`
	CurrentMachine string `json:"currentMachine,omitempty"`
	Error          string `json:"error,omitempty"`
}

// MachineUpgrader upgrades a single machine.
type MachineUpgrader interface {
	// UpgradeMachine performs the upgrade on a single machine.
	// Returns nil if the machine reports ready after upgrade.
	UpgradeMachine(ctx context.Context, machineID, targetVersion string) error

	// CheckMachineHealth verifies a machine is healthy after upgrade.
	CheckMachineHealth(ctx context.Context, machineID string) error

	// RollbackMachine reverts a machine to the previous version.
	RollbackMachine(ctx context.Context, machineID, previousVersion string) error
}

// MachineLister lists machines for a tenant in upgrade order.
type MachineLister interface {
	// ListMachinesInOrder returns machines in the order they should be upgraded.
	// Control plane machines first (one at a time), then workers.
	ListMachinesInOrder(ctx context.Context, tenant string) ([]string, error)
}

// RollingUpgrader performs a rolling upgrade across machines.
type RollingUpgrader struct {
	upgrader MachineUpgrader
	lister   MachineLister
}

// NewRollingUpgrader creates a new rolling upgrader.
func NewRollingUpgrader(upgrader MachineUpgrader, lister MachineLister) *RollingUpgrader {
	return &RollingUpgrader{
		upgrader: upgrader,
		lister:   lister,
	}
}

// Upgrade performs a rolling upgrade across all machines in a tenant.
// It upgrades one machine at a time, checking health between each.
// On failure, it rolls back the current machine and stops.
func (r *RollingUpgrader) Upgrade(ctx context.Context, tenant, currentVersion, targetVersion, component string) Status {
	status := Status{
		Phase:     PhasePreCheck,
		Component: component,
		Target:    targetVersion,
		Current:   currentVersion,
	}

	if currentVersion == targetVersion {
		status.Phase = PhaseComplete
		return status
	}

	// Get machines in upgrade order.
	machines, err := r.lister.ListMachinesInOrder(ctx, tenant)
	if err != nil {
		status.Phase = PhaseFailed
		status.Error = err.Error()
		return status
	}

	if len(machines) == 0 {
		status.Phase = PhaseComplete
		return status
	}

	status.TotalMachines = len(machines)
	status.Phase = PhaseUpgrading

	for i, machineID := range machines {
		status.CurrentMachine = machineID

		// Upgrade machine.
		if err := r.upgrader.UpgradeMachine(ctx, machineID, targetVersion); err != nil {
			status.Phase = PhaseRollback
			status.Error = err.Error()

			// Rollback current machine.
			if rbErr := r.upgrader.RollbackMachine(ctx, machineID, currentVersion); rbErr != nil {
				status.Error = err.Error() + "; rollback also failed: " + rbErr.Error()
			}

			status.Phase = PhaseFailed
			return status
		}

		// Check health.
		if err := r.upgrader.CheckMachineHealth(ctx, machineID); err != nil {
			status.Phase = PhaseRollback

			// Rollback.
			_ = r.upgrader.RollbackMachine(ctx, machineID, currentVersion)

			status.Phase = PhaseFailed
			status.Error = "health check failed for " + machineID + ": " + err.Error()
			return status
		}

		status.Completed = i + 1
	}

	status.Phase = PhaseComplete
	status.CurrentMachine = ""
	return status
}
