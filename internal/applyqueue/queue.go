// Package applyqueue is the reconciliation scheduler for RezusCloud's TF-state
// model (CONTEXT.md "Apply Queue", ADR 21).
//
// One debounced queue PER TENANT: spec writes (a NodeGroup PUT, a tenant edit)
// coalesce within a window (default 5s), then a single `tofu apply` reconciles
// the whole tenant. Applies SERIALIZE within a tenant (a tenant's apply never
// overlaps itself — the per-tenant goroutine is the serialization point) and run
// in PARALLEL across tenants. A slow periodic resync (default 5 min) re-enqueues
// every tenant to catch external drift.
//
// This package is the SCHEDULER CORE only (#87a). It drives an Applier (in
// production, a thin wrapper over internal/tfexec's `Exec.Run`) and emits phase
// transitions to an optional Listener (queued → applying → applied/failed). It
// knows nothing about resources, status, or the watch bus — #87b wires
// reconcilers that translate resource mutations into Enqueue calls and a
// Listener that publishes watch events + updates status.
//
// # Concurrency model
//
// Each tenant gets a goroutine spawned lazily on first Enqueue. The goroutine
// owns a debounce timer: a trigger arriving during the wait resets the timer
// (trailing-edge debounce); a trigger arriving DURING an apply is buffered and
// re-triggers exactly one more apply once the current one finishes. The exit
// decision (no pending trigger) and the goroutine's removal from the active map
// are made atomically under the queue mutex, so an Enqueue can never lose a
// wakeup to an exiting goroutine.
package applyqueue

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// Phase is a tenant's position in the reconciliation cycle. Emitted to Listener.
type Phase string

const (
	PhaseQueued   Phase = "queued"   // enqueued, waiting for the debounce window to elapse
	PhaseApplying Phase = "applying" // Applier.Apply is running
	PhaseApplied  Phase = "applied"  // Apply succeeded
	PhaseFailed   Phase = "failed"   // Apply returned an error
)

// Applier reconciles a single tenant. In production this wraps
// tfexec.Exec.Run("init") + Run("apply", "-auto-approve"). It MUST be safe for
// concurrent use by different tenants (the queue runs tenants in parallel).
type Applier interface {
	Apply(ctx context.Context, tenant string) error
}

// ApplierFunc adapts a function to Applier.
type ApplierFunc func(ctx context.Context, tenant string) error

// Apply implements Applier.
func (f ApplierFunc) Apply(ctx context.Context, tenant string) error { return f(ctx, tenant) }

// TenantLister returns the set of tenants to re-enqueue during a resync tick.
// In production this is the state store's tenant list. Returning an empty slice
// disables that resync tick (no tenants → nothing to re-enqueue).
type TenantLister func() ([]string, error)

// Listener observes phase transitions. err is non-nil for PhaseFailed. It is
// optional (nil) and MUST not block — the queue calls it inline; a blocking
// listener would stall that tenant's apply loop.
type Listener func(tenant string, phase Phase, err error)

// Config tunes the scheduler. All durations have sane defaults; pass zero to
// accept them.
type Config struct {
	// DebounceInterval is the trailing-edge coalescing window. Spec writes within
	// this window collapse into one apply, fired after the window elapses with no
	// further writes. Default 5s.
	DebounceInterval time.Duration
	// ResyncInterval re-enqueues all listed tenants periodically to catch
	// external drift. Default 5m; 0 disables resync entirely.
	ResyncInterval time.Duration
	// Logf is the lifecycle logger. Default log.Printf.
	Logf func(format string, args ...any)
}

// withDefaults returns cfg with zero fields replaced by defaults.
func (c Config) withDefaults() Config {
	if c.DebounceInterval <= 0 {
		c.DebounceInterval = 5 * time.Second
	}
	if c.ResyncInterval < 0 {
		c.ResyncInterval = 0
	}
	if c.ResyncInterval == 0 {
		// keep zero (disabled); default applied below only if caller wanted one.
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return c
}

// defaultResync applied only when caller passes a TenantLister AND leaves
// ResyncInterval at the zero default sentinel. We can't distinguish "I want 5m"
// from "I want disabled" via zero alone once a lister is present, so New applies
// 5m when a lister is given and the caller didn't set a positive interval.
const defaultResync = 5 * time.Minute

// Queue is the per-tenant debounced reconciliation scheduler. Construct with
// New, then Start, then Enqueue as specs change. It is safe for concurrent use.
type Queue struct {
	applier  Applier
	cfg      Config
	lister   TenantLister // nil ⇒ no resync
	listener func(tenant string, phase Phase, err error)

	mu     sync.Mutex
	active map[string]*tenantState // tenant → its debounce loop state (present ⇒ goroutine running)
	wg     sync.WaitGroup          // all tenant goroutines + resync ticker

	lctx context.Context    // lifecycle ctx (set by Start), threads cancellation into every apply
	stop context.CancelFunc // cancels the lifecycle context
}

// tenantState is the per-tenant debounce-loop handle.
type tenantState struct {
	// trigger is a buffered (size 1) coalescing signal: N enqueues during a
	// debounce or apply collapse into one pending trigger.
	trigger chan struct{}
}

// New builds a Queue driving applier. If lister is non-nil, a periodic resync
// re-enqueues every tenant it returns. Pass an optional listener for phase
// transitions (queued/applying/applied/failed).
func New(applier Applier, lister TenantLister, listener Listener, cfg Config) *Queue {
	cfg = cfg.withDefaults()
	// A lister without an explicit interval opts into the 5m default; without a
	// lister, resync stays disabled.
	if lister != nil && cfg.ResyncInterval == 0 {
		cfg.ResyncInterval = defaultResync
	}
	return &Queue{
		applier: applier,
		cfg:     cfg,
		lister:  lister,
		listener: func(t string, p Phase, err error) {
			if listener != nil {
				listener(t, p, err)
			}
		},
		active: make(map[string]*tenantState),
	}
}

// Start launches the queue. It is idempotent; calling twice is a no-op after the
// first. The passed context governs the whole lifecycle: when it is cancelled,
// in-flight applies receive cancellation and all goroutines exit. Returns the
// queue-wide context so callers can additionally stop it via Stop.
func (q *Queue) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	q.mu.Lock()
	if q.stop != nil {
		q.mu.Unlock()
		cancel() // already started; discard the new context
		return
	}
	q.lctx = ctx
	q.stop = cancel
	q.mu.Unlock()

	if q.lister != nil && q.cfg.ResyncInterval > 0 {
		q.wg.Add(1)
		go q.resyncLoop(ctx)
	}
}

// Stop cancels the lifecycle context and waits for all goroutines to exit.
// In-flight applies are cancelled (their context derives from the lifecycle
// ctx); running tenants finish promptly if their Applier honors cancellation.
func (q *Queue) Stop() {
	q.mu.Lock()
	stop := q.stop
	q.stop = nil
	q.mu.Unlock()
	if stop != nil {
		stop()
	}
	q.wg.Wait()
}

// Enqueue schedules a tenant for reconciliation. Within a tenant, rapid calls
// coalesce: only one apply fires after the debounce window, even if many
// enqueues arrived during it (trailing-edge debounce). Across tenants, applies
// run concurrently. Safe for concurrent use.
func (q *Queue) Enqueue(tenant string) {
	q.mu.Lock()
	ts, ok := q.active[tenant]
	if !ok {
		// No loop running: spawn one. PhaseQueued marks the start of a cycle.
		ts = &tenantState{trigger: make(chan struct{}, 1)}
		q.active[tenant] = ts
		ctx := q.lctx // captured under lock; nil if Start wasn't called
		q.mu.Unlock()
		q.listener(tenant, PhaseQueued, nil)
		if ctx == nil {
			ctx = context.Background()
		}
		q.wg.Add(1)
		go q.run(ctx, tenant, ts)
		return
	}
	q.mu.Unlock()
	// Loop running: coalesce via a non-blocking send into the buffered trigger.
	// If one is already buffered, this enqueue is a no-op (already pending).
	select {
	case ts.trigger <- struct{}{}:
	default:
	}
}

// run is the per-tenant debounce + apply loop. It exits when the debounce
// window elapses with no pending trigger (idle), atomically removing itself from
// the active map so an Enqueue racing with exit never loses a wakeup.
func (q *Queue) run(ctx context.Context, tenant string, ts *tenantState) {
	defer q.wg.Done()
	for {
		timer := time.NewTimer(q.cfg.DebounceInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			q.release(tenant, ts)
			return
		case <-ts.trigger:
			// A spec write reset the window: drain and re-debounce.
			timer.Stop()
			continue
		case <-timer.C:
			// Window elapsed: run the apply.
			q.listener(tenant, PhaseApplying, nil)
			err := q.applier.Apply(ctx, tenant)
			if err != nil && !errors.Is(err, context.Canceled) {
				q.listener(tenant, PhaseFailed, err)
			} else {
				q.listener(tenant, PhaseApplied, nil)
			}
			// Decide exit atomically with map removal to avoid a lost wakeup: an
			// Enqueue that arrives between this check and removal would otherwise
			// race a returning goroutine.
			q.mu.Lock()
			select {
			case <-ts.trigger:
				// More work arrived during/just-after apply — re-debounce.
				q.mu.Unlock()
				continue
			default:
				delete(q.active, tenant)
				q.mu.Unlock()
				return
			}
		}
	}
}

// release removes a tenant from the active map only if it still points at ts
// (guards against a resync-spawned replacement).
func (q *Queue) release(tenant string, ts *tenantState) {
	q.mu.Lock()
	if cur := q.active[tenant]; cur == ts {
		delete(q.active, tenant)
	}
	q.mu.Unlock()
}

// resyncLoop periodically re-enqueues every tenant the lister returns.
func (q *Queue) resyncLoop(ctx context.Context) {
	defer q.wg.Done()
	ticker := time.NewTicker(q.cfg.ResyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tenants, err := q.lister()
			if err != nil {
				q.cfg.Logf("applyqueue: resync list failed: %v", err)
				continue
			}
			for _, t := range tenants {
				q.Enqueue(t)
			}
		}
	}
}

// Len returns the number of tenants with an active debounce/apply loop. It is
// best-effort and intended for tests/observability, not synchronization.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.active)
}
