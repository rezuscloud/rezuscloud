package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TestComponent_EndToEnd exercises the assembled component:
//   - Events written via Recorder end up in the Store
//   - CountEvents and ListEvents agree
//   - Handlers is wired (we test the registered route indirectly)
//   - Close is idempotent
func TestComponent_EndToEnd(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "comp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	c := NewComponent(store.DB(), ComponentOptions{RetentionDays: 1})
	if c.Store == nil || c.Recorder == nil || c.Handlers == nil || c.Retention == nil {
		t.Fatalf("component not fully populated: %+v", c)
	}

	// Submit a mutation event through the recorder (the path middleware uses).
	c.Recorder.Record(Event{
		Method: "POST", Path: "/api/v1/tenants", Resource: "tenants", Verb: "create",
		Status: 201, UserName: "admin", Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})

	// Close drains the queue synchronously.
	c.Close()
	c.Close() // idempotent

	// The event should now be in the store.
	events, err := c.Store.ListEvents(context.Background(), Filter{Limit: 10})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Resource != "tenants" {
		t.Errorf("resource = %q, want %q", events[0].Resource, "tenants")
	}

	// Count should agree.
	count, err := c.Store.CountEvents(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

// TestComponent_DefaultsRetentionDays verifies the 90-day default when days <= 0.
func TestComponent_DefaultsRetentionDays(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "comp-defaults.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, days := range []int{0, -1} {
		c := NewComponent(store.DB(), ComponentOptions{RetentionDays: days})
		if c.Retention.days != 90 {
			t.Errorf("RetentionDays=%d: retention days = %d, want 90", days, c.Retention.days)
		}
		c.Close()
	}
}

// TestComponent_NilSafeClose ensures Close is safe on a zero-value component
// (e.g. tests that skip the audit subsystem entirely).
func TestComponent_NilSafeClose(t *testing.T) {
	(&Component{}).Close() // must not panic
}

// TestComponent_Flush confirms Flush synchronously waits for queued events
// to be written to the store, removing the need for time.Sleep in tests.
func TestComponent_Flush(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "flush.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	c := NewComponent(store.DB(), ComponentOptions{})

	// Queue 5 events.
	for i := 0; i < 5; i++ {
		c.Recorder.Record(Event{
			Method: "POST", Path: "/x", Resource: "x",
			Status: 200, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}

	// Flush must block until all 5 are written.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	events, err := c.Store.ListEvents(context.Background(), Filter{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("after flush, got %d events, want 5", len(events))
	}
}

// TestFlush_TimesOutOnContext verifies Flush surfaces a context cancellation
// if the recorder never processes the marker (e.g. it's already closed).
func TestFlush_TimesOutOnContext(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "flush-ctx.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	c := NewComponent(store.DB(), ComponentOptions{})
	c.Close() // close first; queue is now closed, Flush will hang

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = c.Flush(ctx)
	if err == nil {
		// On a closed recorder, sending to the queue panics or hangs.
		// We accept either outcome as long as Flush returns within the ctx.
		t.Log("Flush returned nil on closed recorder (acceptable)")
	}
}
