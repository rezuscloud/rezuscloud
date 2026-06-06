package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
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

// Manager orchestrates upgrade runs and persists their state in Store resources.
type Manager struct {
	store *state.Store

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

var (
	managersMu sync.Mutex
	managers   = map[*state.Store]*Manager{}
)

// GetManager returns a singleton upgrade manager for a store.
func GetManager(store *state.Store) *Manager {
	managersMu.Lock()
	defer managersMu.Unlock()
	if m, ok := managers[store]; ok {
		return m
	}
	m := &Manager{store: store, running: map[string]context.CancelFunc{}}
	managers[store] = m
	return m
}

// StartRun creates and starts an upgrade run asynchronously.
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

	go m.executeRun(ctx, runID, tenant, component, target)

	return &Run{Metadata: md, Spec: spec, Status: status}, nil
}

func (m *Manager) executeRun(ctx context.Context, runID, tenant, component, target string) {
	defer func() {
		m.mu.Lock()
		delete(m.running, runID)
		m.mu.Unlock()
	}()

	machines, _, err := m.store.ListMachinesByTenant(tenant)
	if err != nil {
		m.failRun(runID, "list machines: "+err.Error())
		return
	}

	for i, machine := range machines {
		select {
		case <-ctx.Done():
			m.cancelRun(runID)
			return
		default:
		}

		var rs RunStatus
		md, err := m.store.GetResource("upgraderun", runID, nil, &rs)
		if err != nil || md.Name == "" {
			return
		}
		rs.CurrentMachine = machine.Metadata.Name
		rs.Completed = i
		rs.Phase = PhaseUpgrading
		_, _ = m.store.UpdateStatus("upgraderun", runID, rs)

		// Simulate per-machine upgrade step.
		time.Sleep(10 * time.Millisecond)
	}

	// Mark complete.
	finished := time.Now().UTC()
	var rs RunStatus
	md, err := m.store.GetResource("upgraderun", runID, nil, &rs)
	if err == nil && md.Name != "" {
		rs.Phase = PhaseComplete
		rs.Completed = rs.TotalMachines
		rs.CurrentMachine = ""
		rs.EndedAt = &finished
		_, _ = m.store.UpdateStatus("upgraderun", runID, rs)
	}

	// Persist new tenant desired version.
	t, err := m.store.GetTenant(tenant)
	if err != nil || t == nil {
		return
	}
	if component == "talos" {
		t.Spec.TalosVersion = target
	} else {
		t.Spec.KubernetesVersion = target
	}
	_, _ = m.store.UpdateResource("tenant", tenant, t.Metadata.ResourceVersion, t.Spec, t.Metadata.Labels, t.Metadata.Annotations)
}

func (m *Manager) failRun(runID, msg string) {
	var rs RunStatus
	md, err := m.store.GetResource("upgraderun", runID, nil, &rs)
	if err != nil || md.Name == "" {
		return
	}
	now := time.Now().UTC()
	rs.Phase = PhaseFailed
	rs.Error = msg
	rs.EndedAt = &now
	_, _ = m.store.UpdateStatus("upgraderun", runID, rs)
}

func (m *Manager) cancelRun(runID string) {
	var rs RunStatus
	md, err := m.store.GetResource("upgraderun", runID, nil, &rs)
	if err != nil || md.Name == "" {
		return
	}
	now := time.Now().UTC()
	rs.Phase = PhaseCanceled
	rs.Error = "cancelled by operator"
	rs.EndedAt = &now
	_, _ = m.store.UpdateStatus("upgraderun", runID, rs)
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
	// reverse to newest-first because ListResources returns by created_at ASC.
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

// tiny wrapper to avoid importing encoding/json in multiple files.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
