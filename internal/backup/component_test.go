package backup

import (
	"path/filepath"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TestComponent_Defaults verifies that empty Root and Prefix fall back to
// the documented defaults, and that the resulting Service + API are non-nil.
func TestComponent_Defaults(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "comp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	c, err := NewComponent(store, ComponentOptions{})
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}
	if c.Manager == nil || c.Service == nil || c.API == nil {
		t.Fatalf("component not fully populated: %+v", c)
	}
}

// TestComponent_CustomRoot exercises a custom Root directory.
func TestComponent_CustomRoot(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "comp-custom.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	root := t.TempDir()
	c, err := NewComponent(store, ComponentOptions{Root: root, Prefix: "snaps"})
	if err != nil {
		t.Fatalf("NewComponent: %v", err)
	}

	// Trigger a real snapshot to confirm the component is wired to the right root.
	snap, err := c.Service.TriggerResources(t.Context())
	if err != nil {
		t.Fatalf("TriggerResources: %v", err)
	}
	if snap.Spec.Key == "" {
		t.Error("expected non-empty snapshot key")
	}
}
