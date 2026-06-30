// Adapter bridges state mutations to the watch event bus.
// It implements state.EventBus and translates state.ResourceEvent into Event.
package watch

import (
	"encoding/json"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Adapter is a state.EventBus implementation that republishes events on a Bus.
type Adapter struct {
	bus Bus
}

// NewAdapter creates a new adapter wrapping the given watch bus.
func NewAdapter(bus Bus) *Adapter {
	return &Adapter{bus: bus}
}

// Publish is called by the store on every mutation. It re-loads the resource
// snapshot and emits an Event suitable for HTTP streaming.
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

	a.bus.Publish(resourceType, Event{
		Type:   EventType(ev.Type),
		Object: obj,
	})
}
