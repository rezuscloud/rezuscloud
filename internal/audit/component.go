package audit

import (
	"context"
	"database/sql"
)

// Component bundles the audit subsystem's runtime pieces so main.go can
// construct them once and pass them wherever needed.
//
// The component owns:
//   - Store     — the query/insert surface used by middleware, retention,
//     and the /api/v1/audit endpoint
//   - Recorder  — the async queue that middleware writes through
//   - Handlers  — the read-only HTTP query handlers
//   - Retention — the background sweep loop
//
// Lifecycle: call StartRetention(ctx) once at startup; call Close() during
// shutdown to drain the recorder queue. Constructed exactly once in main.go.
type Component struct {
	Store     Store
	Recorder  *Recorder
	Handlers  *Handlers
	Retention *RetentionPolicy
}

// ComponentOptions configures the audit component.
type ComponentOptions struct {
	// RetentionDays is the retention window (default 90).
	// Events older than this are deleted by the background sweep.
	RetentionDays int
}

// NewComponent constructs a complete audit subsystem backed by the given DB.
// Returns one Component holding Store + Recorder + Handlers + Retention,
// so callers don't have to assemble these pieces themselves.
func NewComponent(db *sql.DB, opts ComponentOptions) *Component {
	store := NewSQLStore(db)
	return &Component{
		Store:     store,
		Recorder:  NewRecorder(store),
		Handlers:  NewHandlers(store),
		Retention: NewRetentionPolicy(store, opts.RetentionDays),
	}
}

// StartRetention launches the retention sweep goroutine. It blocks until
// ctx is canceled. Safe to call as `go c.StartRetention(ctx)`.
func (c *Component) StartRetention(ctx context.Context) {
	if c.Retention == nil {
		return
	}
	c.Retention.Run(ctx)
}

// Close drains the async recorder queue. Call during shutdown.
func (c *Component) Close() {
	if c.Recorder == nil {
		return
	}
	c.Recorder.Close()
}

// Flush blocks until every audit event submitted so far has been written
// to the store, or until ctx is canceled. Useful for tests.
func (c *Component) Flush(ctx context.Context) error {
	if c.Recorder == nil {
		return nil
	}
	return c.Recorder.Flush(ctx)
}
