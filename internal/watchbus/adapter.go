// Package watchbus bridges state mutations to the watch event bus.
// It implements state.EventBus and translates state.ResourceEvent into watch.Event.
package watchbus

import (
	"encoding/json"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
)

// Adapter is a state.EventBus implementation that republishes events on a watch.Bus.
type Adapter struct {
	bus *watch.Bus
}

// New creates a new adapter wrapping the given watch bus.
func New(bus *watch.Bus) *Adapter {
	return &Adapter{bus: bus}
}

// Publish is called by the store on every mutation. It re-loads the resource
// snapshot and emits a watch.Event suitable for HTTP streaming.
func (a *Adapter) Publish(resourceType string, ev state.ResourceEvent) {
	obj := map[string]any{
		"metadata": ev.Metadata,
	}

	if len(ev.Spec) > 0 {
		// Best-effort unmarshal; if it fails, keep raw JSON.
		obj["spec"] = (json.RawMessage)(ev.Spec)
	}
	if len(ev.Status) > 0 {
		obj["status"] = (json.RawMessage)(ev.Status)
	}

	a.bus.Publish(resourceType, watch.Event{
		Type:   watch.EventType(ev.Type),
		Object: obj,
	})
}
