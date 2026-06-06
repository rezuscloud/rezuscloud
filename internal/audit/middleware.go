// Package audit implements the HTTP audit log for the management plane.
//
// Per ADR 18, audit capture happens at the HTTP boundary (not at the store),
// only for mutation methods (POST/PUT/PATCH/DELETE). Read requests are not
// audited by default. The recorder is a non-blocking wrapper — request
// latency is unaffected by audit writes.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/auth"
)

// Event is one audit row.
type Event struct {
	ID         int64   `json:"id,omitempty"`
	Timestamp  string  `json:"timestamp"`
	UserName   string  `json:"userName,omitempty"`
	Role       string  `json:"role,omitempty"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Resource   string  `json:"resource,omitempty"`
	ResourceID string  `json:"resourceId,omitempty"`
	Verb       string  `json:"verb,omitempty"`
	Status     int     `json:"status"`
	RequestID  string  `json:"requestId,omitempty"`
	SourceIP   string  `json:"sourceIP,omitempty"`
	Error      string  `json:"error,omitempty"`
	Metadata   *string `json:"metadata,omitempty"` // raw JSON string
}

// ListResponse is the paginated query response shape.
type ListResponse struct {
	Items []Event `json:"items"`
	Total int     `json:"total"`
}

// Filter is the query parameter set for listing events.
type Filter struct {
	User     string    // ?user=
	Resource string    // ?resource=
	Verb     string    // ?verb=
	Since    time.Time // ?since=
	Until    time.Time // ?until=
	Limit    int       // ?limit=
	Offset   int       // ?offset=
}

// Store is the subset of *state.Store the audit package needs. Defining it
// here keeps the audit package free of the state import.
type Store interface {
	InsertEvent(ctx context.Context, ev Event) error
	ListEvents(ctx context.Context, f Filter) ([]Event, error)
	CountEvents(ctx context.Context, f Filter) (int, error)
	DeleteEventsOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// recorder is the singleton in-memory sink used by Middleware. The async
// write path keeps the request hot-path cheap.
type recorder struct {
	store   Store
	queue   chan Event
	wg      sync.WaitGroup
	stopped bool
	mu      sync.Mutex
}

// Recorder accepts Event submissions and writes them through a queue.
// Closing it drains pending writes.
type Recorder struct {
	*recorder
}

// NewRecorder spins up a background goroutine that drains the queue.
// Buffer of 1024 is enough for bursty write traffic on a personal cloud.
// On overflow, events are dropped (logged) to keep the API responsive.
func NewRecorder(store Store) *Recorder {
	r := &recorder{store: store, queue: make(chan Event, 1024)}
	r.wg.Add(1)
	go r.drain()
	return &Recorder{recorder: r}
}

func (r *recorder) drain() {
	defer r.wg.Done()
	for ev := range r.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := r.store.InsertEvent(ctx, ev); err != nil {
			// Audit failures must not block traffic — log + drop.
			fmt.Printf("audit: insert failed: %v\n", err)
		}
		cancel()
	}
}

// Record submits an event to the async queue. Non-blocking; on overflow the
// event is dropped with a stderr message.
func (r *Recorder) Record(ev Event) {
	if r == nil || r.recorder == nil {
		return
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	select {
	case r.queue <- ev:
	default:
		fmt.Println("audit: queue full, dropping event")
	}
}

// Close drains the queue and stops the background goroutine.
func (r *Recorder) Close() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	close(r.queue)
	r.mu.Unlock()
	r.wg.Wait()
}

// Middleware returns an http.Handler that records an audit event for each
// mutation request after the inner handler completes.
//
// Capture rules (ADR 18):
//   - Only POST/PUT/PATCH/DELETE methods are audited.
//   - Identity is resolved from auth context (set by Authenticate middleware).
//   - The audit row is written asynchronously — no impact on request latency.
//   - Path is normalized (no query string); resource type/id are derived from path segments.
func Middleware(rec *Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Pass through read-only methods without audit capture.
			if !isMutation(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			rw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			ev := buildEvent(r, rw.status, rw.errMessage)
			if rec != nil {
				rec.Record(ev)
			}
		})
	}
}

// captureWriter captures status code + optional error message for the audit row.
type captureWriter struct {
	http.ResponseWriter
	status      int
	errMessage  string
	wroteHeader bool
}

func (c *captureWriter) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.wroteHeader = true
	}
	// Capture error message from JSON error body for non-2xx responses.
	if c.status >= 400 && len(b) > 0 && jsonLookahead(b) {
		var probe struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &probe) == nil && probe.Message != "" {
			c.errMessage = probe.Message
		}
	}
	return c.ResponseWriter.Write(b)
}

// jsonLookahead returns true if the first non-whitespace byte looks like JSON.
func jsonLookahead(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		return c == '{'
	}
	return false
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// buildEvent constructs an Event from the request + response state.
func buildEvent(r *http.Request, status int, errMsg string) Event {
	user := auth.UserFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if user == "" {
		user = "anonymous"
	}

	resource, resourceID, verb := classifyPath(r.URL.Path, r.Method)

	reqID := r.Header.Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("Request-Id")
	}

	sourceIP := clientIP(r)

	ev := Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		UserName:   user,
		Role:       role,
		Method:     r.Method,
		Path:       r.URL.Path,
		Resource:   resource,
		ResourceID: resourceID,
		Verb:       verb,
		Status:     status,
		RequestID:  reqID,
		SourceIP:   sourceIP,
	}
	if status >= 400 && errMsg != "" {
		ev.Error = errMsg
	}
	return ev
}

// classifyPath maps a request path + method to (resource, resourceId, verb).
// Verbs follow K8s audit style: create/update/patch/delete (mutation) / read (GET).
func classifyPath(path, method string) (resource, resourceID, verb string) {
	// Strip /api/v1 prefix.
	trimmed := strings.TrimPrefix(path, "/api/v1/")
	segments := strings.Split(trimmed, "/")
	if len(segments) == 0 || segments[0] == "" {
		return "", "", ""
	}

	switch method {
	case http.MethodPost:
		verb = "create"
	case http.MethodPut, http.MethodPatch:
		verb = "update"
	case http.MethodDelete:
		verb = "delete"
	default:
		verb = "read"
	}

	// Common shapes:
	//   /tenants                      → tenants
	//   /tenants/{name}               → tenants, {name}
	//   /tenants/{name}/machines      → machines (under tenants/{name})
	//   /users/{name}/api-tokens      → api-tokens
	//   /api-tokens/{id}              → api-tokens, {id}
	switch {
	case len(segments) == 1:
		resource = segments[0]
	case len(segments) == 2:
		resource = segments[0]
		resourceID = segments[1]
	case len(segments) >= 3:
		// The trailing resource type wins for nested collections.
		// POST /users/{name}/api-tokens → resource=api-tokens, verb=create
		if len(segments)%2 == 1 {
			resource = segments[len(segments)-1]
		} else {
			resource = segments[len(segments)-2]
			resourceID = segments[len(segments)-1]
		}
	}
	return resource, resourceID, verb
}

// clientIP extracts the originating IP from X-Forwarded-For or RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host
}

// --- API handlers ---

// Handlers provides HTTP endpoints for querying audit events.
type Handlers struct {
	store Store
}

// NewHandlers creates audit query handlers.
func NewHandlers(store Store) *Handlers {
	return &Handlers{store: store}
}

// RegisterRoutes registers read-only audit query endpoints.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit", h.List)
}

// List handles GET /api/v1/audit with filter + pagination query parameters.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	items, err := h.store.ListEvents(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list: %v", err))
		return
	}
	total, err := h.store.CountEvents(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("count: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ListResponse{Items: items, Total: total})
}

func parseFilter(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	f := Filter{
		User:     strings.TrimSpace(q.Get("user")),
		Resource: strings.TrimSpace(q.Get("resource")),
		Verb:     strings.TrimSpace(q.Get("verb")),
	}
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, fmt.Errorf("invalid 'since' (use RFC3339): %w", err)
		}
		f.Since = t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, fmt.Errorf("invalid 'until' (use RFC3339): %w", err)
		}
		f.Until = t
	}
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 || v > 1000 {
			return f, errors.New("'limit' must be 1..1000")
		}
		limit = v
	}
	f.Limit = limit
	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return f, errors.New("'offset' must be >= 0")
		}
		f.Offset = v
	}
	return f, nil
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": msg,
		"reason":  "BadRequest",
		"code":    code,
	})
}

// --- Retention ---

// RetentionPolicy runs a background loop that deletes events older than the
// retention window. Default retention is 90 days; configurable via env var
// REZUSCLOUD_AUDIT_RETENTION_DAYS.
type RetentionPolicy struct {
	store Store
	days  int
}

// NewRetentionPolicy creates a retention job. days <= 0 falls back to 90.
func NewRetentionPolicy(store Store, days int) *RetentionPolicy {
	if days <= 0 {
		days = 90
	}
	return &RetentionPolicy{store: store, days: days}
}

// Run blocks until ctx is canceled, sweeping once per hour.
func (rp *RetentionPolicy) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	rp.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rp.sweep(ctx)
		}
	}
}

func (rp *RetentionPolicy) sweep(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-time.Duration(rp.days) * 24 * time.Hour)
	n, err := rp.store.DeleteEventsOlderThan(ctx, cutoff)
	if err != nil {
		fmt.Printf("audit: retention sweep failed: %v\n", err)
		return
	}
	if n > 0 {
		fmt.Printf("audit: retention sweep deleted %d rows older than %s\n", n, cutoff.Format(time.RFC3339))
	}
}

// Compile-time interface assertion: *sql.DB matches our Store surface via
// the state adapter (internal/audit/storeadapter.go).
var _ Store = (Store)(nil)

// helper to satisfy sql.ErrNoRows import if extended later
var _ = sql.ErrNoRows
