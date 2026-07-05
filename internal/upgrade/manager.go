package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// RunSpec is the desired state of an upgrade run.
type RunSpec struct {
	Tenant      string    `json:"tenant"`
	Component   string    `json:"component"` // talos|kubernetes
	Target      string    `json:"target"`
	RequestedBy string    `json:"requestedBy,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
}

// RunStatus is the observed state of an upgrade run.
type RunStatus struct {
	Status
	RunID     string     `json:"runId"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// Run is a persisted upgrade run record.
type Run struct {
	Metadata state.Metadata `json:"metadata"`
	Spec     RunSpec        `json:"spec"`
	Status   RunStatus      `json:"status"`
}

// Manager orchestrates upgrade runs and persists their state in Store
// resources. It is the single deep module for the upgrade lifecycle: it owns
// the goroutine, status persistence, cancellation, AND the real rolling loop
// (upgrade → health check → rollback, machine-by-machine). The per-machine
// action lives behind the MachineUpgrader seam; the ordering behind
// MachineLister.
//
// The rolling loop was previously split across RollingUpgrader (real loop, dead
// code) and Manager.executeRun (no-op simulation). They are unified here so the
// health-gated rollback logic has one home and is actually exercised.
type Manager struct {
	store    state.StoreAPI
	upgrader MachineUpgrader
	lister   MachineLister

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewManager creates an upgrade Manager bound to the store and the injected
// per-machine upgrader + machine lister. Construct one per process in main.go.
func NewManager(store state.StoreAPI, upgrader MachineUpgrader, lister MachineLister) *Manager {
	return &Manager{store: store, upgrader: upgrader, lister: lister, running: map[string]context.CancelFunc{}}
}

// StartRun creates and starts an upgrade run asynchronously. The returned Run
// reflects the initial persisted state; the run completes in the background.
func (m *Manager) StartRun(tenant, component, target, requestedBy string) (*Run, error) {
	if component != "talos" && component != "kubernetes" {
		return nil, fmt.Errorf("component must be 'talos' or 'kubernetes'")
	}
	if target == "" {
		return nil, fmt.Errorf("target version is required")
	}

	t, err := m.store.GetTenant(tenant)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("tenant not found")
	}

	machines, _, err := m.store.ListMachinesByTenant(tenant)
	if err != nil {
		return nil, err
	}
	if len(machines) == 0 {
		return nil, fmt.Errorf("no machines in tenant")
	}

	runID := fmt.Sprintf("%s-%d", tenant, time.Now().UTC().UnixNano())
	now := time.Now().UTC()
	spec := RunSpec{
		Tenant:      tenant,
		Component:   component,
		Target:      target,
		RequestedBy: requestedBy,
		StartedAt:   now,
	}
	status := RunStatus{
		RunID: runID,
		Status: Status{
			Phase:         PhaseUpgrading,
			Component:     component,
			Target:        target,
			Current:       currentVersion(t.Spec, component),
			TotalMachines: len(machines),
		},
		StartedAt: now,
	}

	labels := map[string]string{"rezuscloud.io/tenant": tenant}
	md, err := m.store.CreateResource("upgraderun", runID, spec, status, labels, nil)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.running[runID] = cancel
	m.mu.Unlock()

	go m.executeRun(ctx, runID, tenant, component, target, status.Current)

	return &Run{Metadata: md, Spec: spec, Status: status}, nil
}

// executeRun runs the real rolling upgrade loop in the background and persists
// the final status. It does NOT write the tenant spec version back — the spec
// is the declarative input (the user already set it); the upgrade converges
// machines to match it. Writing it back would only re-trigger the apply queue.
func (m *Manager) executeRun(ctx context.Context, runID, tenant, component, target, currentVersion string) {
	defer func() {
		m.mu.Lock()
		delete(m.running, runID)
		m.mu.Unlock()
		if r := recover(); r != nil {
			slog.Error("upgrade: run panicked", "run", runID, "panic", r)
			m.failRun(runID, fmt.Sprintf("panic: %v", r))
		}
	}()

	status := Status{
		Component: component,
		Target:    target,
		Current:   currentVersion,
		Phase:     PhasePreCheck,
	}
	m.persistStatus(runID, status)

	final := m.rollUpgrade(ctx, runID, tenant, status.Current, target, component)

	finished := time.Now().UTC()
	final.Current = currentVersion
	final.Component = component
	final.Target = target
	m.persistFinal(runID, final, &finished)
}

// rollUpgrade is the real rolling loop: upgrade one machine → health check →
// (rollback on failure). Pure orchestration; the per-machine action is the
// injected MachineUpgrader. Status is persisted between each machine so the UI
// reflects live progress. Returns the final Status.
func (m *Manager) rollUpgrade(ctx context.Context, runID, tenant, currentVersion, targetVersion, component string) Status {
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

	machines, err := m.lister.ListMachinesInOrder(ctx, tenant)
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
		select {
		case <-ctx.Done():
			status.Phase = PhaseCanceled
			status.Error = "cancelled by operator"
			return status
		default:
		}

		status.CurrentMachine = machineID
		status.Completed = i
		m.persistStatus(runID, status)

		// Upgrade machine.
		if err := m.upgrader.UpgradeMachine(ctx, machineID, targetVersion); err != nil {
			// Rollback current machine, then fail.
			if rbErr := m.upgrader.RollbackMachine(ctx, machineID, currentVersion); rbErr != nil {
				status.Error = err.Error() + "; rollback also failed: " + rbErr.Error()
			} else {
				status.Error = err.Error()
			}
			status.Phase = PhaseFailed
			return status
		}

		// Health check.
		if err := m.upgrader.CheckMachineHealth(ctx, machineID); err != nil {
			_ = m.upgrader.RollbackMachine(ctx, machineID, currentVersion)
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

// persistStatus writes an intermediate Status to the run record.
func (m *Manager) persistStatus(runID string, status Status) {
	var rs RunStatus
	md, err := m.store.GetResource("upgraderun", runID, nil, &rs)
	if err != nil || md.Name == "" {
		return
	}
	rs.Status = status
	_, _ = m.store.UpdateStatus("upgraderun", runID, rs)
}

// persistFinal writes the terminal Status + endedAt to the run record.
func (m *Manager) persistFinal(runID string, status Status, endedAt *time.Time) {
	var rs RunStatus
	md, err := m.store.GetResource("upgraderun", runID, nil, &rs)
	if err != nil || md.Name == "" {
		return
	}
	rs.Status = status
	rs.EndedAt = endedAt
	_, _ = m.store.UpdateStatus("upgraderun", runID, rs)
}

func (m *Manager) failRun(runID, msg string) {
	now := time.Now().UTC()
	m.persistFinal(runID, Status{Phase: PhaseFailed, Error: msg}, &now)
}

// RunUpgrade runs the rolling upgrade loop SYNCHRONOUSLY (no goroutine) and
// returns an error if the upgrade failed or was canceled. It does not persist
// a run record — callers that need run persistence use StartRun. This is the
// pre-apply hook entry point: the reconcile Applier calls it before
// `tofu apply` when it detects a version change, blocking the apply until
// machines converge.
func (m *Manager) RunUpgrade(ctx context.Context, tenant, component, currentVersion, targetVersion string) error {
	status := m.rollUpgrade(ctx, "", tenant, currentVersion, targetVersion, component)
	if status.Phase == PhaseFailed {
		return fmt.Errorf("upgrade %s %s→%s failed: %s", component, currentVersion, targetVersion, status.Error)
	}
	if status.Phase == PhaseCanceled {
		return fmt.Errorf("upgrade %s %s→%s canceled", component, currentVersion, targetVersion)
	}
	return nil
}

// CancelRun cancels a currently-running upgrade run.
func (m *Manager) CancelRun(runID string) error {
	m.mu.Lock()
	cancel, ok := m.running[runID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("run is not active")
	}
	cancel()
	return nil
}

// GetRun returns an upgrade run by ID (tenant-scoped).
func (m *Manager) GetRun(tenant, runID string) (*Run, error) {
	var spec RunSpec
	var status RunStatus
	md, err := m.store.GetResource("upgraderun", runID, &spec, &status)
	if err == state.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if md.Labels["rezuscloud.io/tenant"] != tenant {
		return nil, nil
	}
	return &Run{Metadata: md, Spec: spec, Status: status}, nil
}

// ListRuns lists upgrade runs for a tenant, newest first.
func (m *Manager) ListRuns(tenant string) ([]*Run, error) {
	opts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + tenant}
	mds, specs, statuses, _, err := m.store.ListResources("upgraderun", opts)
	if err != nil {
		return nil, err
	}
	out := make([]*Run, 0, len(mds))
	for i := range mds {
		var spec RunSpec
		var status RunStatus
		_ = jsonUnmarshal(specs[i], &spec)
		_ = jsonUnmarshal(statuses[i], &status)
		out = append(out, &Run{Metadata: mds[i], Spec: spec, Status: status})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func currentVersion(spec state.TenantSpec, component string) string {
	if component == "kubernetes" {
		return spec.KubernetesVersion
	}
	return spec.TalosVersion
}

func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
