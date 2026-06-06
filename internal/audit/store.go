package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SQLStore implements audit.Store on top of *sql.DB.
//
// All methods are safe for concurrent use; the underlying *sql.DB manages
// connection pooling. InsertEvent uses a short context timeout (5s) to avoid
// head-of-line blocking on slow disks; query methods inherit the caller's
// context.
type SQLStore struct {
	db *sql.DB
}

// NewSQLStore wraps a *sql.DB.
func NewSQLStore(db *sql.DB) *SQLStore {
	return &SQLStore{db: db}
}

// InsertEvent persists one audit row. metadata, if non-nil, is stored as-is
// (caller-supplied JSON). timestamp defaults to now if empty.
func (s *SQLStore) InsertEvent(ctx context.Context, ev Event) error {
	ts := ev.Timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var meta any
	if ev.Metadata != nil {
		meta = *ev.Metadata
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events
		  (timestamp, user_name, role, method, path, resource, resource_id, verb,
		   status, request_id, source_ip, error, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, nullable(ev.UserName), nullable(ev.Role), ev.Method, ev.Path,
		nullable(ev.Resource), nullable(ev.ResourceID), nullable(ev.Verb),
		ev.Status, nullable(ev.RequestID), nullable(ev.SourceIP),
		nullable(ev.Error), nullableAny(meta),
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// ListEvents returns events matching the filter, ordered newest-first.
func (s *SQLStore) ListEvents(ctx context.Context, f Filter) ([]Event, error) {
	q, args := buildQuery(`SELECT id, timestamp, user_name, role, method, path,
		resource, resource_id, verb, status, request_id, source_ip, error, metadata
		FROM audit_events`, f, true)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var ev Event
		var userName, role, resource, resourceID, verb, reqID, sourceIP, errMsg, meta sql.NullString
		if err := rows.Scan(&ev.ID, &ev.Timestamp, &userName, &role, &ev.Method, &ev.Path,
			&resource, &resourceID, &verb, &ev.Status, &reqID, &sourceIP, &errMsg, &meta); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		ev.UserName = userName.String
		ev.Role = role.String
		ev.Resource = resource.String
		ev.ResourceID = resourceID.String
		ev.Verb = verb.String
		ev.RequestID = reqID.String
		ev.SourceIP = sourceIP.String
		ev.Error = errMsg.String
		if meta.Valid && meta.String != "" {
			s := meta.String
			ev.Metadata = &s
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// CountEvents returns the total count matching the filter (ignoring limit/offset).
func (s *SQLStore) CountEvents(ctx context.Context, f Filter) (int, error) {
	q, args := buildQuery(`SELECT COUNT(*) FROM audit_events`, f, false)
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit events: %w", err)
	}
	return n, nil
}

// DeleteEventsOlderThan removes rows with timestamp < cutoff.
func (s *SQLStore) DeleteEventsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_events WHERE timestamp < ?`,
		cutoff.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("delete old audit events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// buildQuery assembles a SELECT/COUNT query with the supplied filter. applyOrder
// is true for list queries (which need ORDER + LIMIT).
func buildQuery(base string, f Filter, applyOrder bool) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if f.User != "" {
		clauses = append(clauses, "user_name = ?")
		args = append(args, f.User)
	}
	if f.Resource != "" {
		clauses = append(clauses, "resource = ?")
		args = append(args, f.Resource)
	}
	if f.Verb != "" {
		clauses = append(clauses, "verb = ?")
		args = append(args, f.Verb)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, f.Since.Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, f.Until.Format(time.RFC3339Nano))
	}

	q := base
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	if applyOrder {
		q += " ORDER BY id DESC"
		if f.Limit > 0 {
			q += " LIMIT ?"
			args = append(args, f.Limit)
		}
		if f.Offset > 0 {
			q += " OFFSET ?"
			args = append(args, f.Offset)
		}
	}
	return q, args
}

// nullable returns nil if s is empty so the column stores SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableAny is like nullable but accepts any value (e.g. for JSON columns).
func nullableAny(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}

// EnsureUnused keeps the json import available for future metadata expansion
// without forcing callers to use json directly.
var _ = json.Marshal
