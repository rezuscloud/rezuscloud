package reconcile

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/applyqueue"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// StatusTracker persists apply-queue phase into tenant/nodegroup status without
// feeding back into the queue. It runs in a background worker so the queue's
// listener path stays non-blocking.
type StatusTracker struct {
	store state.StoreAPI
	logf  func(format string, args ...any)

	ch chan phaseEvent
	wg sync.WaitGroup
}

type phaseEvent struct {
	tenant string
	phase  applyqueue.Phase
	err    string
}

// nodeGroupStatusJSON mirrors the stored nodegroup status JSON shape so we can
// preserve the existing lifecycle fields while updating only reconciliation.
type nodeGroupStatusJSON struct {
	Phase          string                      `json:"phase"`
	ReadyMachines  int                         `json:"readyMachines"`
	TotalMachines  int                         `json:"totalMachines"`
	Reconciliation *state.ReconciliationStatus `json:"reconciliation,omitempty"`
}

func NewStatusTracker(store state.StoreAPI) *StatusTracker {
	return &StatusTracker{
		store: store,
		logf:  log.Printf,
		ch:    make(chan phaseEvent, 256),
	}
}

func (t *StatusTracker) Start(ctx context.Context) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-t.ch:
				t.apply(ev)
			}
		}
	}()
}

func (t *StatusTracker) Stop() {
	t.wg.Wait()
}

// Listener returns the applyqueue.Listener that feeds the background worker.
func (t *StatusTracker) Listener() applyqueue.Listener {
	return func(tenant string, phase applyqueue.Phase, err error) {
		ev := phaseEvent{tenant: tenant, phase: phase}
		if err != nil {
			ev.err = err.Error()
		}
		select {
		case t.ch <- ev:
		default:
			t.logf("reconcile: status tracker queue full; dropping phase=%s tenant=%s", phase, tenant)
		}
	}
}

func (t *StatusTracker) apply(ev phaseEvent) {
	now := time.Now().UTC()

	tenant, err := t.store.GetTenant(ev.tenant)
	if err == nil && tenant != nil {
		status := tenant.Status
		status.Reconciliation = nextReconciliation(status.Reconciliation, ev.phase, ev.err, now)
		if _, err := t.store.UpdateTenantStatus(ev.tenant, status); err != nil {
			t.logf("reconcile: update tenant status for %q failed: %v", ev.tenant, err)
		}
	}

	metas, _, statuses, _, err := t.store.ListResources("nodegroup", state.ListOptions{
		LabelSelector: "rezuscloud.io/tenant=" + ev.tenant,
	})
	if err != nil {
		t.logf("reconcile: list nodegroups for %q failed: %v", ev.tenant, err)
		return
	}
	for i, md := range metas {
		var st nodeGroupStatusJSON
		_ = json.Unmarshal(statuses[i], &st)
		st.Reconciliation = nextReconciliation(st.Reconciliation, ev.phase, ev.err, now)
		if _, err := t.store.UpdateStatus("nodegroup", md.Name, st); err != nil {
			t.logf("reconcile: update nodegroup status for %q/%q failed: %v", ev.tenant, md.Name, err)
		}
	}
}

func nextReconciliation(cur *state.ReconciliationStatus, phase applyqueue.Phase, errMsg string, now time.Time) *state.ReconciliationStatus {
	next := &state.ReconciliationStatus{
		Phase:     string(phase),
		UpdatedAt: &now,
	}
	if cur != nil {
		next.LastError = cur.LastError
	}
	switch phase {
	case applyqueue.PhaseFailed:
		next.LastError = errMsg
	case applyqueue.PhaseApplied:
		next.LastError = ""
	}
	return next
}
