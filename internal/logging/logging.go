// Package logging provides slog-compatible helpers for components that use a
// printf-style log callback (func(string, ...any)). This bridges the gap
// between structured logging (slog) and the existing logf callback pattern
// used by applyqueue, tfexec, and reconcile.
package logging

import (
	"fmt"
	"log/slog"
)

// Logf is a printf-style wrapper around slog.Info. Components that accept a
// `logf func(string, ...any)` callback default to this instead of log.Printf,
// so lifecycle messages flow through the structured logger.
func Logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}
