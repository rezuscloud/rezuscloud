# ADR 0013: Audit Log — HTTP Middleware Pattern

## Status

Accepted

## Context

RezusCloud needs an audit log: who did what, when, against which resource. The
rejected alternative — a state-store wrapper that intercepts every mutation —
fits a gRPC resource-stream architecture RezusCloud does not have. See
[`../architecture-history/`](../architecture-history/README.md).

## Decision

**Audit at the HTTP boundary, not the store boundary** (the Kubernetes audit
standard).

- An HTTP middleware wraps the protected mux.
- For mutating methods (POST/PUT/PATCH/DELETE) it resolves identity from the
  JWT context, captures the response status, and writes an audit row
  asynchronously (non-blocking, drop-on-overflow).
- GET requests are not audited by default.
- Audit rows live in a dedicated SQLite table with indexes; queryable via
  `GET /api/v1/audit` with filters (user, resource, verb, time range).

### Internal actors

Controllers and background jobs mutate state without an HTTP request. These are
**not audited** at this seam — they are platform-internal. Operators debugging
controller-driven changes use resource watches (via NATS — see
[ADR 0009](0009-event-bus-nats.md)) instead.

### Retention

Default 90 days, configurable via `REZUSCLOUD_AUDIT_RETENTION_DAYS`. A periodic
background job prunes old rows.

## Consequences

- **Single well-defined seam** — the HTTP middleware chain. Every
  human-initiated mutation is captured.
- **Captures HTTP context** (request ID, source IP) that a store wrapper would
  not have.
- **Controller mutations are not audited** — by design, to avoid flooding the
  log with low-signal entries.

## See Also

- [ADR 0012](0012-auth-local-jwt-and-api-tokens.md) — identity source
- [ADR 0003](0003-rest-api-kubernetes-model.md) — the API surface audited
