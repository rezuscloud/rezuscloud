package applyqueue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// small is a debounce window small enough for fast tests but large enough that
// a coalescing test can fire several enqueues inside it deterministically.
const small = 25 * time.Millisecond

// recorderApplier counts applies per tenant and optionally blocks until released.
type recorderApplier struct {
	mu      sync.Mutex
	counts  map[string]int
	events  []string // ordered tenant apply events, for sequence assertions
	release chan struct{}
	delay   time.Duration
	err     error
}

func newRecorder() *recorderApplier {
	return &recorderApplier{counts: map[string]int{}, release: make(chan struct{})}
}

func (r *recorderApplier) Apply(_ context.Context, tenant string) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.counts[tenant]++
	r.events = append(r.events, tenant)
	r.mu.Unlock()
	return r.err
}

func (r *recorderApplier) count(tenant string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[tenant]
}

func (r *recorderApplier) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.counts {
		n += c
	}
	return n
}

// waitApply polls until the applier has recorded at least want applies for tenant.
func waitApply(t *testing.T, r *recorderApplier, tenant string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.count(tenant) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("tenant %s: wanted %d applies, got %d", tenant, want, r.count(tenant))
}

func TestNew_AppliesDefaults(t *testing.T) {
	q := New(newRecorder(), nil, nil, Config{})
	if q.cfg.DebounceInterval != 5*time.Second {
		t.Errorf("default debounce = %v, want 5s", q.cfg.DebounceInterval)
	}
	// No lister ⇒ resync disabled (interval 0).
	if q.cfg.ResyncInterval != 0 {
		t.Errorf("no-lister resync = %v, want 0", q.cfg.ResyncInterval)
	}
	if q.cfg.Logf == nil {
		t.Error("default logger not set")
	}
}

func TestNew_ListerOptsIntoResyncDefault(t *testing.T) {
	q := New(newRecorder(), func() ([]string, error) { return nil, nil }, nil, Config{})
	if q.cfg.ResyncInterval != 5*time.Minute {
		t.Errorf("lister resync = %v, want 5m", q.cfg.ResyncInterval)
	}
}

func TestEnqueue_CoalescesRapidWritesToOneApply(t *testing.T) {
	// The headline acceptance criterion: N rapid spec writes → exactly 1 apply.
	r := newRecorder()
	q := New(r, nil, nil, Config{DebounceInterval: small})
	q.Start(context.Background())
	defer q.Stop()

	tenant := "personal"
	for i := 0; i < 5; i++ {
		q.Enqueue(tenant)
		time.Sleep(2 * time.Millisecond) // all within the debounce window
	}
	waitApply(t, r, tenant, 1)

	// After the apply, wait for the loop to go idle, then assert NO further apply.
	q.Stop() // drains; no re-trigger should have fired
	if got := r.count(tenant); got != 1 {
		t.Fatalf("coalesced into %d applies, want 1", got)
	}
}

func TestEnqueue_SerializesWithinTenant(t *testing.T) {
	// Two enqueues that each trigger an apply must NEVER overlap for the same
	// tenant. We detect overlap with an in-use flag; the second apply fires only
	// AFTER the debounce following the first completes.
	var (
		inUse  atomic.Bool
		overl  atomic.Bool
		applyN atomic.Int64
	)
	applier := ApplierFunc(func(_ context.Context, tenant string) error {
		if !inUse.CompareAndSwap(false, true) {
			overl.Store(true) // detected concurrent apply on same tenant
		}
		defer inUse.Store(false)
		applyN.Add(1)
		time.Sleep(30 * time.Millisecond) // hold long enough that a second enqueue lands mid-apply
		return nil
	})
	q := New(applier, nil, nil, Config{DebounceInterval: small})
	q.Start(context.Background())
	defer q.Stop()

	q.Enqueue("personal")
	// Wait for apply #1 to be in-flight, then enqueue again mid-apply. The
	// re-apply must wait for #1 to finish (no overlap) then run once more.
	deadline1 := time.Now().Add(time.Second)
	for time.Now().Before(deadline1) && applyN.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	q.Enqueue("personal")

	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) && applyN.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	q.Stop()
	if applyN.Load() != 2 {
		t.Fatalf("apply count = %d, want 2", applyN.Load())
	}
	if overl.Load() {
		t.Fatal("two applies for the same tenant overlapped (serialization broken)")
	}
}

func TestEnqueue_RunsTenantsInParallel(t *testing.T) {
	// Two tenants enqueued concurrently must apply at the same time: a blocking
	// applier that waits on a release channel proves the second tenant's apply
	// starts while the first is still in-flight (serial-within/parallel-across).
	var started sync.Map // tenant -> struct{}
	hold := make(chan struct{})
	applier := ApplierFunc(func(_ context.Context, tenant string) error {
		started.Store(tenant, struct{}{})
		select {
		case <-hold:
		case <-time.After(2 * time.Second):
			return errors.New("hold timeout")
		}
		return nil
	})
	q := New(applier, nil, nil, Config{DebounceInterval: small})
	q.Start(context.Background())
	defer q.Stop()

	q.Enqueue("a")
	q.Enqueue("b")

	// Both tenants' applies must have STARTED (parallel) while held.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, aOk := started.Load("a")
		_, bOk := started.Load("b")
		if aOk && bOk {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, aOk := started.Load("a")
	_, bOk := started.Load("b")
	close(hold) // release both
	if !aOk || !bOk {
		t.Fatalf("tenants did not run in parallel: a=%v b=%v", aOk, bOk)
	}
}

func TestEnqueue_TriggersDuringApplyCauseExactlyOneReapply(t *testing.T) {
	// While apply #1 is running, two enqueues arrive. They must coalesce into
	// exactly ONE re-apply, not two.
	var applies atomic.Int64
	hold := make(chan struct{})
	applier := ApplierFunc(func(ctx context.Context, tenant string) error {
		n := applies.Add(1)
		if n == 1 {
			close(hold) // release the test once the first apply is in-flight
		}
		time.Sleep(15 * time.Millisecond)
		_ = ctx
		return nil
	})
	q := New(applier, nil, nil, Config{DebounceInterval: small})
	q.Start(context.Background())
	defer q.Stop()

	q.Enqueue("personal")
	<-hold // apply #1 running now
	q.Enqueue("personal")
	q.Enqueue("personal") // two during-apply triggers → coalesce to one re-apply

	// Wait for the count to stabilize at 2 (apply #1 + one coalesced re-apply).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && applies.Load() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	q.Stop()
	if got := applies.Load(); got != 2 {
		t.Fatalf("applies = %d, want 2 (one initial + one coalesced re-apply)", got)
	}
}

func TestResync_ReenqueuesAllTenants(t *testing.T) {
	r := newRecorder()
	tenants := []string{"alpha", "beta"}
	lister := TenantLister(func() ([]string, error) { return tenants, nil })
	q := New(r, lister, nil, Config{
		DebounceInterval: small,
		ResyncInterval:   30 * time.Millisecond, // tight for a fast test
	})
	q.Start(context.Background())
	defer q.Stop()

	// Both tenants get enqueued on the first resync tick, then coalesce to one
	// apply each.
	waitApply(t, r, "alpha", 1)
	waitApply(t, r, "beta", 1)
}

func TestResync_DisabledWhenNoLister(t *testing.T) {
	r := newRecorder()
	q := New(r, nil, nil, Config{DebounceInterval: small, ResyncInterval: 20 * time.Millisecond})
	q.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	q.Stop()
	if r.total() != 0 {
		t.Fatalf("resync fired with no lister: %d applies", r.total())
	}
}

func TestListener_ReceivesAllPhases(t *testing.T) {
	wantErr := errors.New("boom")
	applier := ApplierFunc(func(_ context.Context, tenant string) error { return wantErr })
	var (
		mu     sync.Mutex
		phases []Phase
	)
	listener := func(_ string, p Phase, _ error) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	}
	q := New(applier, nil, listener, Config{DebounceInterval: small})
	q.Start(context.Background())
	defer q.Stop()

	q.Enqueue("personal")
	// Expect: queued → applying → failed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(phases)
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	q.Stop()
	mu.Lock()
	defer mu.Unlock()
	if len(phases) < 3 {
		t.Fatalf("phases = %v, want at least 3", phases)
	}
	if phases[0] != PhaseQueued || phases[1] != PhaseApplying || phases[len(phases)-1] != PhaseFailed {
		t.Fatalf("phase order wrong: %v", phases)
	}
}

func TestStop_CancelsInFlightApply(t *testing.T) {
	// An Applier honoring ctx must exit promptly on Stop.
	started := make(chan struct{})
	done := make(chan error, 1)
	applier := ApplierFunc(func(ctx context.Context, tenant string) error {
		close(started)
		<-ctx.Done()
		done <- ctx.Err()
		return ctx.Err()
	})
	q := New(applier, nil, nil, Config{DebounceInterval: small})
	q.Start(context.Background())

	q.Enqueue("personal")
	<-started
	stopStart := time.Now()
	q.Stop()
	elapsed := time.Since(stopStart)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("apply err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel in-flight apply within 2s")
	}
	if elapsed > time.Second {
		t.Fatalf("Stop took %v, want prompt cancellation", elapsed)
	}
}

func TestStart_IsIdempotent(t *testing.T) {
	q := New(newRecorder(), nil, nil, Config{DebounceInterval: small})
	q.Start(context.Background())
	q.Start(context.Background()) // must not panic or double-spawn resync
	q.Stop()
}

func TestApplierFunc_ComposesWithExec(t *testing.T) {
	// A smoke check that the ApplierFunc adapter shape matches how production will
	// wrap tfexec.Exec — documented contract, not a real exec call.
	called := false
	f := ApplierFunc(func(_ context.Context, tenant string) error {
		called = true
		return nil
	})
	if err := f.Apply(context.Background(), "x"); err != nil || !called {
		t.Fatalf("ApplierFunc misbehaved: called=%v err=%v", called, err)
	}
}
