package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

func newTestStore(t *testing.T) *SQLStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewSQLStore(store.DB())
}

// memStore is an in-memory Store impl for testing the recorder / handlers
// without going through SQLite.
type memStore struct {
	mu     sync.Mutex
	events []Event
}

func (m *memStore) InsertEvent(_ context.Context, ev Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return nil
}
func (m *memStore) ListEvents(_ context.Context, f Filter) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, ev := range m.events {
		if f.User != "" && ev.UserName != f.User {
			continue
		}
		if f.Resource != "" && ev.Resource != f.Resource {
			continue
		}
		if f.Verb != "" && ev.Verb != f.Verb {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}
func (m *memStore) CountEvents(_ context.Context, _ Filter) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events), nil
}
func (m *memStore) DeleteEventsOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []Event
	var deleted int64
	for _, ev := range m.events {
		ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if err == nil && ts.Before(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, ev)
	}
	m.events = kept
	return deleted, nil
}

// recorder dropping test helper: ensures Record is non-blocking and items
// reach the store eventually.
func TestRecorder_RecordsEvent(t *testing.T) {
	store := &memStore{}
	rec := NewRecorder(store)
	t.Cleanup(rec.Close)

	rec.Record(Event{Method: "POST", Path: "/api/v1/tenants", UserName: "alice", Status: 201})

	// Drain: with a queue of 1024 this is near-instant.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.events)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(store.events); got != 1 {
		t.Fatalf("expected 1 event, got %d", got)
	}
}

func TestRecorder_NilSafe(t *testing.T) {
	var rec *Recorder
	rec.Record(Event{}) // must not panic
}

// TestMiddleware_MutationOnly verifies GETs are not audited; mutations are.
func TestMiddleware_MutationOnly(t *testing.T) {
	store := &memStore{}
	rec := NewRecorder(store)
	t.Cleanup(rec.Close)
	mw := Middleware(rec)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// GET → not audited.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	// POST → audited.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tenants", nil)
	req = req.WithContext(auth.WithClaims(req.Context(), "alice", auth.RoleEdit))
	mw.ServeHTTP(httptest.NewRecorder(), req)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.events)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(store.events); got != 1 {
		t.Fatalf("expected 1 event (POST only), got %d", got)
	}
	if store.events[0].Method != http.MethodPost {
		t.Errorf("captured method = %q, want POST", store.events[0].Method)
	}
	if store.events[0].UserName != "alice" {
		t.Errorf("user = %q, want alice", store.events[0].UserName)
	}
}

func TestMiddleware_CapturesStatus(t *testing.T) {
	store := &memStore{}
	rec := NewRecorder(store)
	t.Cleanup(rec.Close)
	mw := Middleware(rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/prod", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.events)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.events[0].Status; got != http.StatusConflict {
		t.Errorf("status = %d, want 409", got)
	}
}

func TestMiddleware_CapturesErrorMessage(t *testing.T) {
	store := &memStore{}
	rec := NewRecorder(store)
	t.Cleanup(rec.Close)
	mw := Middleware(rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"failure","message":"bad input","reason":"BadRequest","code":400}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.events)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.events[0].Error; got != "bad input" {
		t.Errorf("error = %q, want 'bad input'", got)
	}
}

// TestClassifyPath covers the path → (resource, resourceID, verb) extractor.
func TestClassifyPath(t *testing.T) {
	cases := []struct {
		method, path, wantRes, wantID, wantVerb string
	}{
		{"POST", "/api/v1/tenants", "tenants", "", "create"},
		{"GET", "/api/v1/tenants/prod", "tenants", "prod", "read"},
		{"DELETE", "/api/v1/tenants/prod", "tenants", "prod", "delete"},
		{"POST", "/api/v1/users/alice/api-tokens", "api-tokens", "", "create"},
		{"DELETE", "/api/v1/api-tokens/tok_123", "api-tokens", "tok_123", "delete"},
		{"PUT", "/api/v1/users/alice", "users", "alice", "update"},
		{"PATCH", "/api/v1/tenants/prod/machines/m1/config", "config", "", "update"},
	}
	for _, tc := range cases {
		gotRes, gotID, gotVerb := classifyPath(tc.path, tc.method)
		if gotRes != tc.wantRes || gotID != tc.wantID || gotVerb != tc.wantVerb {
			t.Errorf("classifyPath(%q, %q) = (%q,%q,%q); want (%q,%q,%q)",
				tc.method, tc.path, gotRes, gotID, gotVerb, tc.wantRes, tc.wantID, tc.wantVerb)
		}
	}
}

func TestClientIP(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"1.2.3.4, 5.6.7.8": "1.2.3.4",
		"192.168.1.1":      "192.168.1.1",
		"  spaced  ":       "spaced",
	}
	for input, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if input != "" {
			req.Header.Set("X-Forwarded-For", input)
		}
		got := clientIP(req)
		if got != want && want != "" {
			// Allow trimmed results.
			if strings.TrimSpace(got) != want {
				t.Errorf("clientIP(%q) = %q, want %q", input, got, want)
			}
		}
	}
}

// TestSQLStore_InsertAndList exercises the SQLite-backed store end-to-end.
func TestSQLStore_InsertAndList(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ev := Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		UserName:  "alice",
		Role:      "edit",
		Method:    "POST",
		Path:      "/api/v1/tenants",
		Resource:  "tenants",
		Verb:      "create",
		Status:    201,
		SourceIP:  "10.0.0.1",
	}
	if err := store.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := store.ListEvents(ctx, Filter{User: "alice"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("list: %d events, want 1", len(got))
	}
	if got[0].UserName != "alice" {
		t.Errorf("user = %q", got[0].UserName)
	}
	if got[0].ID == 0 {
		t.Errorf("id should be set by DB")
	}

	// Filter miss.
	got, _ = store.ListEvents(ctx, Filter{User: "bob"})
	if len(got) != 0 {
		t.Errorf("filter miss: got %d events, want 0", len(got))
	}

	// Count.
	n, err := store.CountEvents(ctx, Filter{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

func TestSQLStore_DeleteOlderThan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	old := Event{
		Timestamp: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano),
		Method:    "POST", Path: "/api/v1/tenants",
		UserName: "alice", Status: 201,
	}
	fresh := Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Method:    "POST", Path: "/api/v1/tenants",
		UserName: "alice", Status: 201,
	}
	_ = store.InsertEvent(ctx, old)
	_ = store.InsertEvent(ctx, fresh)

	n, err := store.DeleteEventsOlderThan(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	got, _ := store.ListEvents(ctx, Filter{})
	if len(got) != 1 {
		t.Errorf("after delete: %d events, want 1", len(got))
	}
}

// TestHandlers_List verifies the API response shape + filters.
func TestHandlers_List(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Seed multiple events.
	for i, u := range []string{"alice", "bob", "alice"} {
		_ = store.InsertEvent(ctx, Event{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano),
			UserName:  u, Method: "POST", Path: "/api/v1/tenants", Status: 201,
			Resource: "tenants", Verb: "create",
		})
	}

	h := NewHandlers(store)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?user=alice&limit=2", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp ListResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2 (alice only)", resp.Total)
	}
	if len(resp.Items) != 2 {
		t.Errorf("items = %d, want 2 (limit)", len(resp.Items))
	}
	for _, it := range resp.Items {
		if it.UserName != "alice" {
			t.Errorf("filter leak: %v", it.UserName)
		}
	}
}

func TestHandlers_List_BadSince(t *testing.T) {
	store := newTestStore(t)
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?since=not-a-date", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandlers_List_LimitBounds(t *testing.T) {
	store := newTestStore(t)
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?limit=0", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("limit=0 should fail, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/audit?limit=9999", nil)
	w = httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("limit=9999 should fail, got %d", w.Code)
	}
}

// TestRetentionPolicy_DeleteOld exercises the sweep loop using a memStore
// and a very short ticker.
func TestRetentionPolicy_DeleteOld(t *testing.T) {
	store := &memStore{}
	old := Event{Timestamp: time.Now().UTC().Add(-200 * 24 * time.Hour).Format(time.RFC3339Nano)}
	_ = store.InsertEvent(context.Background(), old)
	rp := NewRetentionPolicy(store, 7) // 7 days

	// Call sweep directly via Run's first invocation by running once and canceling.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rp.Run(ctx)

	got, _ := store.CountEvents(context.Background(), Filter{})
	if got != 0 {
		t.Errorf("after sweep: %d events, want 0", got)
	}
}
