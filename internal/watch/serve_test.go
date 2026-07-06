package watch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TestServeWatch_StreamsLiveEvents proves the generic ?watch=true handler:
// a client connects, the bus publishes a MODIFIED event, and the SSE stream
// delivers it as a {"type":"MODIFIED","object":{...}} frame (#172).
func TestServeWatch_StreamsLiveEvents(t *testing.T) {
	bus := NewBus()

	// Subscribe lazily inside ServeWatch; publish after the handler is running.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWatch(w, r, bus, "tenant", WatchOptions{})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type frame struct {
		Type   string          `json:"type"`
		Object json.RawMessage `json:"object"`
	}
	frames := make(chan frame, 8)

	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"?watch=true", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 512)
		for {
			n, err := resp.Body.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				// SSE frames are separated by "\n\n".
				for {
					idx := strings.Index(string(buf), "\n\n")
					if idx < 0 {
						break
					}
					raw := string(buf[:idx])
					buf = buf[idx+2:]
					raw = strings.TrimPrefix(raw, "data: ")
					var f frame
					if json.Unmarshal([]byte(raw), &f) == nil {
						frames <- f
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Give the watcher a moment to subscribe.
	time.Sleep(100 * time.Millisecond)

	bus.Publish("tenant", Event{
		Type: EventModified,
		Object: map[string]any{
			"metadata": state.Metadata{Name: "personal"},
		},
	})

	select {
	case f := <-frames:
		if f.Type != "MODIFIED" {
			t.Errorf("event type = %q, want MODIFIED", f.Type)
		}
		var obj map[string]any
		if err := json.Unmarshal(f.Object, &obj); err != nil {
			t.Fatal(err)
		}
		md := obj["metadata"].(map[string]any)
		if md["name"] != "personal" {
			t.Errorf("object name = %v, want personal", md["name"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the streamed event")
	}
}

// TestServeWatch_TenantFilter confirms the tenant-scoped filter drops events
// for other tenants — the mechanism behind GET /tenants/{t}/nodegroups?watch=true.
func TestServeWatch_TenantFilter(t *testing.T) {
	filter := TenantFilter("a")

	keep := filter(Event{Object: map[string]any{
		"metadata": state.Metadata{Labels: map[string]string{"rezuscloud.io/tenant": "a"}},
	}})
	if !keep {
		t.Error("filter dropped an event for tenant 'a'")
	}

	drop := filter(Event{Object: map[string]any{
		"metadata": state.Metadata{Labels: map[string]string{"rezuscloud.io/tenant": "b"}},
	}})
	if drop {
		t.Error("filter kept an event for tenant 'b' when watching 'a'")
	}
}

// TestServeWatch_InitialState confirms the snapshot is sent as ADDED frames
// before live events.
func TestServeWatch_InitialState(t *testing.T) {
	bus := NewBus()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWatch(w, r, bus, "tenant", WatchOptions{
			InitialState: func() ([]any, error) {
				return []any{
					map[string]any{"metadata": map[string]any{"name": "t1"}},
					map[string]any{"metadata": map[string]any{"name": "t2"}},
				}, nil
			},
		})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"?sse=false", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// NDJSON: read the first two lines (the initial ADDED snapshot).
	type frame struct {
		Type   string          `json:"type"`
		Object json.RawMessage `json:"object"`
	}
	dec := json.NewDecoder(resp.Body)
	var f frame
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode first frame: %v", err)
	}
	if f.Type != "ADDED" {
		t.Errorf("first frame type = %q, want ADDED", f.Type)
	}
	var obj map[string]any
	_ = json.Unmarshal(f.Object, &obj)
	if obj["metadata"].(map[string]any)["name"] != "t1" {
		t.Errorf("first snapshot name = %v, want t1", obj["metadata"])
	}
}
