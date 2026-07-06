package watch

import (
	"encoding/json"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// WatchOptions configures a ServeWatch call.
type WatchOptions struct {
	// InitialState lists existing resources to send as ADDED events before
	// streaming live changes. nil skips the initial snapshot.
	InitialState func() ([]any, error)
	// Filter drops events that do not match. nil keeps every event. Used for
	// tenant-scoped watches (e.g. GET /tenants/{t}/machines?watch=true filters
	// to machines labelled with that tenant).
	Filter func(Event) bool
}

// ServeWatch subscribes to bus for resourceType, optionally sends an initial
// ADDED snapshot of existing resources, then streams live change events until
// the client disconnects. It is the generic K8s-style ?watch=true handler:
// GET /api/v1/{type}?watch=true upgrades to a text/event-stream of
// {"type":"ADDED|MODIFIED|DELETED","object":{...}} frames.
//
// SSE is the default; ?sse=false selects NDJSON (one JSON object per line),
// matching the existing low-level WatchHandler convention.
func ServeWatch(w http.ResponseWriter, r *http.Request, bus Bus, resourceType string, opts WatchOptions) {
	ch, cancel := bus.Subscribe(resourceType)
	defer cancel()

	sse := r.URL.Query().Get("sse") != "false" // SSE is the default
	if sse {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/x-ndjson")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	writeEvent := func(ev Event) {
		if opts.Filter != nil && !opts.Filter(ev) {
			return
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		if sse {
			w.Write([]byte("data: "))
			w.Write(data)
			w.Write([]byte("\n\n"))
		} else {
			w.Write(data)
			w.Write([]byte("\n"))
		}
		if canFlush {
			flusher.Flush()
		}
	}

	// Initial snapshot: one ADDED per existing resource. Sent before the live
	// stream so a freshly-connecting client sees current state then deltas.
	if opts.InitialState != nil {
		existing, err := opts.InitialState()
		if err == nil {
			for _, r := range existing {
				writeEvent(Event{Type: EventAdded, Object: r})
			}
		}
		// On error we still stream live events — the snapshot is best-effort.
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeEvent(ev)
		}
	}
}

// TenantFilter returns a Filter that keeps only events whose object carries the
// rezuscloud.io/tenant=<tenant> label. The watch.Adapter publishes events whose
// Object is a map[string]any with a "metadata" key holding a state.Metadata —
// the filter reads the label off that struct. Used for tenant-scoped watch
// endpoints (node groups, machines, config patches under a tenant).
func TenantFilter(tenant string) func(Event) bool {
	want := tenant
	return func(ev Event) bool {
		obj, ok := ev.Object.(map[string]any)
		if !ok {
			return false
		}
		md, ok := obj["metadata"].(state.Metadata)
		if !ok {
			return false
		}
		return md.Labels["rezuscloud.io/tenant"] == want
	}
}
