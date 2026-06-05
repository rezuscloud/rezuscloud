package watchbus

import (
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
)

func TestAdapter_TranslatesAddedEvent(t *testing.T) {
	bus := watch.NewBus()
	adapter := New(bus)

	ch, cancel := bus.Subscribe("tenant")
	defer cancel()

	adapter.Publish("tenant", state.ResourceEvent{
		Type:         "ADDED",
		ResourceType: "tenant",
		Metadata:     state.Metadata{Name: "prod"},
		Spec:         []byte(`{"kubernetesVersion":"1.35.0"}`),
		Status:       []byte(`{"phase":"active"}`),
	})

	select {
	case ev := <-ch:
		if ev.Type != watch.EventAdded {
			t.Errorf("type = %s, want ADDED", ev.Type)
		}
		obj, ok := ev.Object.(map[string]any)
		if !ok {
			t.Fatalf("object is not a map")
		}
		md, ok := obj["metadata"].(state.Metadata)
		if !ok {
			t.Fatalf("metadata is wrong type: %T", obj["metadata"])
		}
		if md.Name != "prod" {
			t.Errorf("metadata.name = %q, want prod", md.Name)
		}
		if _, ok := obj["spec"]; !ok {
			t.Error("spec missing from event")
		}
		if _, ok := obj["status"]; !ok {
			t.Error("status missing from event")
		}
	default:
		t.Fatal("did not receive event")
	}
}

func TestAdapter_TranslatesModifiedEvent(t *testing.T) {
	bus := watch.NewBus()
	adapter := New(bus)

	ch, cancel := bus.Subscribe("machine")
	defer cancel()

	adapter.Publish("machine", state.ResourceEvent{
		Type:         "MODIFIED",
		ResourceType: "machine",
		Metadata:     state.Metadata{Name: "abc-123"},
	})

	select {
	case ev := <-ch:
		if ev.Type != watch.EventModified {
			t.Errorf("type = %s, want MODIFIED", ev.Type)
		}
	default:
		t.Fatal("did not receive event")
	}
}

func TestAdapter_TranslatesDeletedEvent(t *testing.T) {
	bus := watch.NewBus()
	adapter := New(bus)

	ch, cancel := bus.Subscribe("provider")
	defer cancel()

	adapter.Publish("provider", state.ResourceEvent{
		Type:         "DELETED",
		ResourceType: "provider",
		Metadata:     state.Metadata{Name: "hetzner"},
	})

	select {
	case ev := <-ch:
		if ev.Type != watch.EventDeleted {
			t.Errorf("type = %s, want DELETED", ev.Type)
		}
	default:
		t.Fatal("did not receive event")
	}
}

func TestAdapter_DroppedEvents(t *testing.T) {
	// Verify that the adapter itself doesn't block when the bus is full
	// (watch.Bus already drops on slow watcher).
	bus := watch.NewBus()
	adapter := New(bus)

	// Publish without subscribing — must not panic.
	for i := 0; i < 100; i++ {
		adapter.Publish("tenant", state.ResourceEvent{
			Type:         "ADDED",
			ResourceType: "tenant",
			Metadata:     state.Metadata{Name: "prod"},
		})
	}
}
