package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBus_PublishSubscribe(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe("tenant")
	defer cancel()

	bus.Publish("tenant", Event{Type: EventAdded, Object: map[string]string{"name": "prod"}})

	select {
	case event := <-ch:
		if event.Type != EventAdded {
			t.Errorf("type = %q, want %q", event.Type, EventAdded)
		}
	case <-time.After(time.Second):
		t.Error("should receive event within 1 second")
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe("tenant")

	cancel()
	time.Sleep(50 * time.Millisecond) // Allow goroutine to clean up.

	// After unsubscribe, publish should not block or panic.
	bus.Publish("tenant", Event{Type: EventAdded, Object: nil})

	// Channel should still work (buffered) but no new subscribers.
	select {
	case <-ch:
		// OK — might have received the event before cleanup.
	default:
		// Also OK — cleanup happened first.
	}
}

func TestBus_MultipleWatchers(t *testing.T) {
	bus := NewBus()
	ch1, cancel1 := bus.Subscribe("tenant")
	defer cancel1()
	ch2, cancel2 := bus.Subscribe("tenant")
	defer cancel2()

	bus.Publish("tenant", Event{Type: EventAdded, Object: map[string]string{"name": "prod"}})

	// Both should receive.
	<-ch1
	<-ch2
}

func TestBus_DifferentTypes(t *testing.T) {
	bus := NewBus()
	tenantCh, cancel1 := bus.Subscribe("tenant")
	defer cancel1()
	machineCh, cancel2 := bus.Subscribe("machine")
	defer cancel2()

	bus.Publish("tenant", Event{Type: EventAdded, Object: map[string]string{"name": "prod"}})

	// Only tenant watcher should receive.
	select {
	case <-tenantCh:
		// OK
	case <-time.After(time.Second):
		t.Error("tenant watcher should receive event")
	}

	select {
	case <-machineCh:
		t.Error("machine watcher should not receive tenant events")
	case <-time.After(100 * time.Millisecond):
		// OK — no event.
	}
}

func TestWatchHandler_ChunkedJSON(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe("tenant")

	ctx, cancelCtx := context.WithCancel(context.Background())

	handler := WatchHandler(ch, cancel)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/events?watch=true", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Send event after a short delay, then cancel.
	go func() {
		time.Sleep(50 * time.Millisecond)
		bus.Publish("tenant", Event{Type: EventAdded, Object: map[string]string{"name": "prod"}})
		cancelCtx()
	}()

	handler(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `"type":"ADDED"`) {
		t.Errorf("body should contain ADDED event, got: %s", body)
	}
	if !strings.Contains(body, `"name":"prod"`) {
		t.Errorf("body should contain object name, got: %s", body)
	}

	// Should be NDJSON (one JSON per line).
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/x-ndjson" {
		t.Errorf("content type = %q, want %q", contentType, "application/x-ndjson")
	}
}

func TestWatchHandler_SSE(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe("tenant")

	ctx, cancelCtx := context.WithCancel(context.Background())

	handler := WatchHandler(ch, cancel)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/events?watch=true&sse=true", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	go func() {
		time.Sleep(50 * time.Millisecond)
		bus.Publish("tenant", Event{Type: EventModified, Object: map[string]string{"name": "prod"}})
		cancelCtx()
	}()

	handler(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Errorf("SSE body should contain 'data: ' prefix, got: %s", body)
	}
	if !strings.Contains(body, `"type":"MODIFIED"`) {
		t.Errorf("body should contain MODIFIED event, got: %s", body)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("content type = %q, want %q", contentType, "text/event-stream")
	}
}

func TestWatchHandler_ClientDisconnect(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe("tenant")

	handler := WatchHandler(ch, cancel)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	req := httptest.NewRequest(http.MethodGet, "/events?watch=true", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Cancel context immediately.
	cancelCtx()

	// Handler should exit.
	done := make(chan bool)
	go func() {
		handler(w, req)
		done <- true
	}()

	select {
	case <-done:
		// OK — handler exited.
	case <-time.After(time.Second):
		t.Error("handler should exit on client disconnect")
	}
}

func TestSendInitialState(t *testing.T) {
	ch := make(chan Event, 10)

	resources := []any{
		map[string]any{"metadata": map[string]string{"name": "tenant-1"}},
		map[string]any{"metadata": map[string]string{"name": "tenant-2"}},
	}

	SendInitialState(ch, resources)

	if len(ch) != 2 {
		t.Fatalf("expected 2 events, got %d", len(ch))
	}

	event1 := <-ch
	if event1.Type != EventAdded {
		t.Errorf("type = %q, want %q", event1.Type, EventAdded)
	}
}

func TestWatchHandler_SSEFormat(t *testing.T) {
	bus := NewBus()
	_ = bus
	ch := make(chan Event, 1)
	cancel := func() {}

	ctx, cancelCtx := context.WithCancel(context.Background())

	handler := WatchHandler(ch, cancel)

	// Put event in channel before handler starts.
	ch <- Event{Type: EventAdded, Object: map[string]string{"name": "test"}}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancelCtx()
	}()

	req := httptest.NewRequest(http.MethodGet, "/events?sse=true", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler(w, req)

	body := w.Body.String()

	// SSE format: data: <json>\n\n
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("SSE should start with 'data: ', got: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("SSE should end with \\n\\n, got: %q", body)
	}

	// Parse the JSON part.
	jsonPart := strings.TrimPrefix(body, "data: ")
	jsonPart = strings.TrimSuffix(jsonPart, "\n\n")

	var event map[string]any
	if err := json.Unmarshal([]byte(jsonPart), &event); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if event["type"] != "ADDED" {
		t.Errorf("type = %v, want ADDED", event["type"])
	}
}

func TestExtractName(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{map[string]any{"metadata": map[string]any{"name": "prod"}}, "prod"},
		{map[string]any{"no": "metadata"}, ""},
		{"string", ""},
		{nil, ""},
	}

	for _, tt := range tests {
		got := extractName(tt.input)
		if got != tt.want {
			t.Errorf("extractName(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// Ensure bufio import is used.
var _ = bufio.NewReader(nil)
