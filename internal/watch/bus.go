// Package watch provides real-time resource change events via chunked JSON
// and Server-Sent Events (SSE). Events are produced by store mutations
// and consumed by HTTP watch endpoints.
package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EventType describes what happened to a resource.
type EventType string

const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
)

// Event represents a resource change event.
type Event struct {
	Type   EventType `json:"type"`
	Object any       `json:"object"`
}

// Bus distributes events to registered watchers.
type Bus struct {
	mu       sync.RWMutex
	watchers map[string][]chan Event // key: resource type
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		watchers: make(map[string][]chan Event),
	}
}

// Publish sends an event to all watchers of a resource type.
func (b *Bus) Publish(resourceType string, event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.watchers[resourceType] {
		select {
		case ch <- event:
		default:
			// Drop event if watcher is too slow.
		}
	}
}

// Subscribe registers a watcher for a resource type.
// Returns a channel that receives events and a cancel function.
func (b *Bus) Subscribe(resourceType string) (<-chan Event, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Event, 64)

	b.mu.Lock()
	b.watchers[resourceType] = append(b.watchers[resourceType], ch)
	b.mu.Unlock()

	// Cleanup on cancel.
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		defer b.mu.Unlock()
		watchers := b.watchers[resourceType]
		for i, w := range watchers {
			if w == ch {
				b.watchers[resourceType] = append(watchers[:i], watchers[i+1:]...)
				return
			}
		}
	}()

	return ch, cancel
}

// WatchHandler creates an HTTP handler that streams events.
// If `sse=true` query param is present, uses Server-Sent Events framing.
// Otherwise, uses chunked JSON (one JSON object per line).
func WatchHandler(ch <-chan Event, cancel context.CancelFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer cancel()

		// Check if client wants SSE.
		sse := r.URL.Query().Get("sse") == "true"

		// Set appropriate headers.
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/x-ndjson")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, canFlush := w.(http.Flusher)

		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}

				data, err := json.Marshal(event)
				if err != nil {
					continue
				}

				if sse {
					fmt.Fprintf(w, "data: %s\n\n", data)
				} else {
					fmt.Fprintf(w, "%s\n", data)
				}

				if canFlush {
					flusher.Flush()
				}
			}
		}
	}
}

// SendInitialState sends ADDED events for existing resources.
func SendInitialState(ch chan<- Event, resources []any) {
	for _, r := range resources {
		ch <- Event{Type: EventAdded, Object: r}
	}
}

// StartPolling starts a goroutine that polls the store for changes.
// This is a simplified implementation — in production, use SQLite WAL hooks
// or trigger-based notifications.
func StartPolling(ctx context.Context, bus *Bus, resourceType string, interval time.Duration, listFn func() ([]any, error)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		previous := make(map[string]bool)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				resources, err := listFn()
				if err != nil {
					continue
				}

				current := make(map[string]bool)
				for _, r := range resources {
					// Extract name from resource (assumes map with "metadata.name").
					name := extractName(r)
					current[name] = true

					if !previous[name] {
						bus.Publish(resourceType, Event{Type: EventAdded, Object: r})
					} else {
						bus.Publish(resourceType, Event{Type: EventModified, Object: r})
					}
				}

				// Detect deletions.
				for name := range previous {
					if !current[name] {
						bus.Publish(resourceType, Event{Type: EventDeleted, Object: map[string]string{"name": name}})
					}
				}

				previous = current
			}
		}
	}()
}

func extractName(r any) string {
	m, ok := r.(map[string]any)
	if !ok {
		return ""
	}
	meta, ok := m["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := meta["name"].(string)
	return name
}
