package upgrade

import (
	"context"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

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

// MachineUpgrader upgrades a single machine. This is the seam where real
// `talosctl upgrade` / `kubectl` calls live; the rolling loop in Manager drives
// it machine-by-machine. Production adapters (internal/upgrade/talos) are
// injected; tests use a fake.
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

// StoreMachineLister is a real MachineLister backed by the state store. It
// orders control-plane machines before workers (the only safe upgrade order for
// a Talos+K8s cluster).
type StoreMachineLister struct {
	store state.StoreAPI
}

// NewStoreMachineLister returns a MachineLister that reads machines from the
// store and orders them control-plane-first.
func NewStoreMachineLister(store state.StoreAPI) *StoreMachineLister {
	return &StoreMachineLister{store: store}
}

// ListMachinesInOrder implements MachineLister. Control planes come first
// (sequential upgrades), then workers.
func (s *StoreMachineLister) ListMachinesInOrder(ctx context.Context, tenant string) ([]string, error) {
	_ = ctx
	machines, _, err := s.store.ListMachinesByTenant(tenant)
	if err != nil {
		return nil, err
	}
	var controlPlane, workers []string
	for _, m := range machines {
		if m.Status.Role == "controlplane" {
			controlPlane = append(controlPlane, m.Metadata.Name)
		} else {
			workers = append(workers, m.Metadata.Name)
		}
	}
	return append(controlPlane, workers...), nil
}

// NoOpMachineUpgrader is a placeholder MachineUpgrader whose per-machine calls
// are no-ops. It exists so the upgrade engine (the rolling loop, health gates,
// rollback, status tracking) is exercised end-to-end before a real
// `talosctl`-backed adapter lands. Replace it in main.go once the Talos adapter
// (#93 follow-up) is built.
type NoOpMachineUpgrader struct{}

func (NoOpMachineUpgrader) UpgradeMachine(context.Context, string, string) error  { return nil }
func (NoOpMachineUpgrader) CheckMachineHealth(context.Context, string) error      { return nil }
func (NoOpMachineUpgrader) RollbackMachine(context.Context, string, string) error { return nil }
