// Package logs provides HTTP handlers for streaming machine logs.
// Logs are streamed from machines via MachineLink tunnel.
// The LogProvider interface abstracts the log source for testability.
package logs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LogEntry represents a single log line from a machine.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Level     string    `json:"level,omitempty"` // info, warn, error
	Source    string    `json:"source,omitempty"`
}

// LogProvider streams log entries for a given machine.
// Implementations may connect to MachineLink, read from a buffer,
// or return mock data for testing.
type LogProvider interface {
	// StreamLogs returns a channel of log entries for the given machine.
	// The channel is closed when the context is done or there are no more logs.
	// If the machine is disconnected, returns an error.
	StreamLogs(machineID string, opts LogOptions) (<-chan LogEntry, error)
}

// LogOptions controls log streaming behavior.
type LogOptions struct {
	// Since returns logs after this time (zero = all).
	Since time.Time
	// Tail returns only the last N lines (0 = all).
	Tail int
	// Follow keeps the stream open for new logs.
	Follow bool
}

// Handler provides HTTP handlers for machine log streaming.
type Handler struct {
	provider LogProvider
}

// NewHandler creates a logs handler.
func NewHandler(provider LogProvider) *Handler {
	return &Handler{provider: provider}
}

// RegisterRoutes registers log streaming routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tenants/{tenant}/machines/{id}/logs", h.Stream)
}

// Stream handles GET /api/v1/tenants/{tenant}/machines/{id}/logs.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	machineID := r.PathValue("id")
	if machineID == "" {
		writeLogError(w, "machine id is required", "BadRequest", http.StatusBadRequest)
		return
	}

	opts := parseLogOptions(r)

	ch, err := h.provider.StreamLogs(machineID, opts)
	if err != nil {
		if strings.Contains(err.Error(), "disconnected") {
			writeLogError(w, fmt.Sprintf("machine %q is disconnected", machineID), "MachineDisconnected", http.StatusServiceUnavailable)
			return
		}
		writeLogError(w, "failed to stream logs", "InternalError", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	for entry := range ch {
		data, _ := json.Marshal(entry)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if canFlush {
			flusher.Flush()
		}
	}
}

func parseLogOptions(r *http.Request) LogOptions {
	opts := LogOptions{}

	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			opts.Since = t
		}
	}

	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			opts.Tail = n
		}
	}

	if f := r.URL.Query().Get("follow"); f == "true" || f == "1" {
		opts.Follow = true
	}

	return opts
}

func writeLogError(w http.ResponseWriter, message, reason string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  reason,
		"code":    code,
	})
}
