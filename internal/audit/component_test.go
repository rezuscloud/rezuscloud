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
