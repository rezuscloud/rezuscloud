package watch

import (
	"testing"
	"time"
)

// TestNATSBus_PubSub verifies the core pub/sub contract: a subscriber
// receives events published after it subscribed, on the correct subject.
func TestNATSBus_PubSub(t *testing.T) {
	bus, err := NewNATSBus()
	if err != nil {
		t.Fatalf("NewNATSBus: %v", err)
	}
	defer bus.Close()

	ch, cancel := bus.Subscribe("machine")
	defer cancel()

	// Publish an event after subscribing.
	bus.Publish("machine", Event{
		Type:   EventAdded,
		Object: map[string]any{"name": "node-1"},
	})

	select {
	case ev := <-ch:
		if ev.Type != EventAdded {
			t.Errorf("type = %q, want %q", ev.Type, EventAdded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

// TestNATSBus_SubjectIsolation verifies events on one subject don't leak
// to subscribers of a different subject.
func TestNATSBus_SubjectIsolation(t *testing.T) {
	bus, err := NewNATSBus()
	if err != nil {
		t.Fatalf("NewNATSBus: %v", err)
	}
	defer bus.Close()

	machineCh, _ := bus.Subscribe("machine")
	tenantCh, _ := bus.Subscribe("tenant")

	bus.Publish("tenant", Event{Type: EventAdded, Object: map[string]any{"name": "prod"}})

	select {
	case <-tenantCh:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("tenant subscriber did not receive tenant event")
	}

	select {
	case ev := <-machineCh:
		t.Fatalf("machine subscriber received tenant event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
		// expected — no machine event published
	}
}

// TestNATSBus_MultipleSubscribers verifies fan-out: all subscribers of a
// subject receive the same event.
func TestNATSBus_MultipleSubscribers(t *testing.T) {
	bus, err := NewNATSBus()
	if err != nil {
		t.Fatalf("NewNATSBus: %v", err)
	}
	defer bus.Close()

	ch1, _ := bus.Subscribe("machine")
	ch2, _ := bus.Subscribe("machine")

	bus.Publish("machine", Event{Type: EventModified, Object: map[string]any{"name": "node-1"}})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case <-ch:
			// expected
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d did not receive event", i)
		}
	}
}

// TestNATSBus_CancelUnsubscribe verifies that after cancel(), the subscriber
// no longer receives events.
func TestNATSBus_CancelUnsubscribe(t *testing.T) {
	bus, err := NewNATSBus()
	if err != nil {
		t.Fatalf("NewNATSBus: %v", err)
	}
	defer bus.Close()

	ch, cancel := bus.Subscribe("machine")
	cancel()

	// Give the unsub a moment to process.
	time.Sleep(100 * time.Millisecond)

	bus.Publish("machine", Event{Type: EventAdded, Object: map[string]any{"name": "node-2"}})

	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("received event after cancel: %+v", ev)
		}
		// channel closed — fine
	case <-time.After(200 * time.Millisecond):
		// no event — expected
	}
}

// TestNATSBus_InterfaceConformance ensures NATSBus satisfies the Bus interface.
func TestNATSBus_InterfaceConformance(t *testing.T) {
	var _ Bus = (*NATSBus)(nil)
	var _ Bus = (*LocalBus)(nil)
}
