package logs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockProvider is a test LogProvider.
type mockProvider struct {
	entries []LogEntry
	err     error
}

func (m *mockProvider) StreamLogs(_ string, _ LogOptions) (<-chan LogEntry, error) {
	if m.err != nil {
		return nil, m.err
	}

	ch := make(chan LogEntry, len(m.entries))
	go func() {
		for _, e := range m.entries {
			ch <- e
		}
		close(ch)
	}()
	return ch, nil
}

func TestStream_Success(t *testing.T) {
	provider := &mockProvider{
		entries: []LogEntry{
			{Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Message: "machine started", Level: "info"},
			{Timestamp: time.Date(2025, 1, 1, 0, 0, 1, 0, time.UTC), Message: "kubelet ready", Level: "info"},
		},
	}

	h := NewHandler(provider)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/machines/abc123/logs", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "machine started") {
		t.Error("should contain log entry")
	}
	if !strings.Contains(body, "kubelet ready") {
		t.Error("should contain second log entry")
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Error("should set SSE content type")
	}
}

func TestStream_Disconnected(t *testing.T) {
	provider := &mockProvider{
		err: fmt.Errorf("machine disconnected"),
	}

	h := NewHandler(provider)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/machines/abc123/logs", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestStream_NoMachineID(t *testing.T) {
	provider := &mockProvider{}
	h := NewHandler(provider)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/machines//logs", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()

	h.Stream(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestStream_EmptyLogs(t *testing.T) {
	provider := &mockProvider{entries: nil}
	h := NewHandler(provider)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/machines/abc123/logs", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "data:") {
		t.Error("empty logs should produce no SSE data lines")
	}
}

func TestParseLogOptions_Since(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/logs?since=2025-01-01T00:00:00Z&tail=10&follow=true", nil)
	opts := parseLogOptions(req)

	if !opts.Since.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v, want 2025-01-01", opts.Since)
	}
	if opts.Tail != 10 {
		t.Errorf("tail = %d, want 10", opts.Tail)
	}
	if !opts.Follow {
		t.Error("follow should be true")
	}
}

func TestParseLogOptions_Defaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	opts := parseLogOptions(req)

	if !opts.Since.IsZero() {
		t.Error("since should be zero by default")
	}
	if opts.Tail != 0 {
		t.Error("tail should be 0 by default")
	}
	if opts.Follow {
		t.Error("follow should be false by default")
	}
}

func TestParseLogOptions_InvalidSince(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/logs?since=not-a-date", nil)
	opts := parseLogOptions(req)

	if !opts.Since.IsZero() {
		t.Error("invalid since should default to zero")
	}
}

func TestParseLogOptions_InvalidTail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/logs?tail=abc", nil)
	opts := parseLogOptions(req)

	if opts.Tail != 0 {
		t.Error("invalid tail should default to 0")
	}
}

func TestStream_SSEFormat(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	provider := &mockProvider{
		entries: []LogEntry{
			{Timestamp: ts, Message: "test log", Level: "info", Source: "kubelet"},
		},
	}

	h := NewHandler(provider)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/prod/machines/abc/logs", nil)
	req.SetPathValue("tenant", "prod")
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Error("SSE should start with 'data: '")
	}

	// Verify it's valid JSON after "data: ".
	line := strings.TrimPrefix(body, "data: ")
	line = strings.TrimSuffix(line, "\n\n")
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("SSE data should be valid JSON: %v", err)
	}
	if entry["message"] != "test log" {
		t.Errorf("message = %v, want 'test log'", entry["message"])
	}
}
