# ADR 18: Audit Log — HTTP Middleware Pattern

Status: Accepted (2026-06-02)

## Context

The reference platform (`arch/04-features/audit-log.md`, `arch/03-components/backend/audit.md`) implements the audit log as a **state wrapper** — every `Create`/`Update`/`Destroy` mutation on the underlying state is intercepted, the before/after image is captured, and an `AuditLog` resource is appended to the state. Audit entries are themselves resources, queryable through the same `ResourceService.Watch`.

This pattern fits the reference platform's gRPC + resource-stream architecture: the wrapper is a single seam that catches every mutation regardless of RPC origin.

RezusCloud uses **REST over HTTP** (ADR 9) with a relational SQLite store. There is no central state wrapper to intercept. The reference platform's seam is not naturally available.

Two options were considered:

1. **Store wrapper** — wrap `state.Store` so every `CreateResource`/`UpdateResource`/`DeleteResource` call writes an audit row. Captures mutations from any caller, including background controllers.
2. **HTTP middleware** — wrap the HTTP handler chain so every mutation request (POST/PUT/PATCH/DELETE) is logged with identity, verb, path, status. This is the Kubernetes audit log pattern.

## Decision

Adopt the **HTTP middleware pattern** (Kubernetes audit standard). Audit happens at the API boundary, not at the store boundary.

### Schema

```sql
CREATE TABLE audit_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL,         -- RFC3339
    user_id     TEXT,                  -- resolved from JWT claims; "anonymous" if unauthenticated
    user_email  TEXT,                  -- for human-readable display
    role        TEXT,                  -- effective role at request time
    method      TEXT NOT NULL,         -- GET/POST/PUT/PATCH/DELETE
    path        TEXT NOT NULL,         -- request URL path (no query string)
    resource    TEXT,                  -- derived from path: "tenant", "machine", etc.
    resource_id TEXT,                  -- derived from path: name/UUID
    verb        TEXT,                  -- derived: "create"/"update"/"delete"/"read"
    status      INTEGER NOT NULL,      -- HTTP response status code
    request_id  TEXT,                  -- propagated X-Request-ID or generated
    source_ip   TEXT,                  -- X-Forwarded-For or RemoteAddr
    error       TEXT,                  -- non-empty on non-2xx responses
    metadata    TEXT                   -- JSON: extra context (e.g. labels affected)
);
```

### Capture point

`internal/api/middleware/audit.go` wraps the protected mux. For mutating methods (POST/PUT/PATCH/DELETE) it:

1. Resolves identity from `auth.WithClaims` context.
2. Wraps `http.ResponseWriter` to capture the final status code.
3. Delegates to the inner handler.
4. After the handler returns, writes an audit row asynchronously (non-blocking) to the store.

For GET requests, audit is **not captured by default** (performance). A future flag may enable per-resource read auditing for sensitive resources (jointokens, users, secrets).

### Internal actor exclusions

Controllers and background jobs (e.g. `FinalizerController`, `TenantReconciler`) mutate state without an HTTP request. These are **not audited** — they are platform-internal, and auditing them would flood the log with low-signal entries. Operators debugging controller-driven changes use the resource watch API or store-level logging instead.

This matches the reference platform's "internal actor" carve-out.

### Query surface

`GET /api/v1/audit` — paginated, filterable:
- `?user=<id>` — filter by user
- `?resource=<type>` — filter by resource type
- `?verb=<verb>` — filter by verb
- `?since=<rfc3339>` — time range filter
- `?limit=100&offset=0` — pagination
- `?watch=true&sse=true` — live stream (audit events published via `state.EventBus`)

WebUI consumes the endpoint at `/settings/audit-logs`.

### Retention

Default retention is **90 days**, matching the reference platform. A periodic background job deletes rows older than the retention window. Retention is configurable via `REZUSCLOUD_AUDIT_RETENTION_DAYS` env var.

## Consequences

### What we gain
- Single, well-defined seam: the HTTP middleware chain. Every human-initiated mutation is captured.
- Simple implementation: one middleware function, one store method.
- Captures HTTP-level context (request ID, source IP) that a store wrapper would not have.
- Audit data lives in a dedicated table — easy to query, index, partition.
- Predictable: a mutation through REST → audit row. A mutation from a controller → no audit row.

### What we lose
- Controller-driven mutations are not audited. Operators must rely on resource watches for that visibility.
- A mutation that bypasses HTTP (impossible in v1 but conceivable in future) is not audited.
- Audit entries are not "resources" — they don't have labels, annotations, or the standard K8s metadata shape. They cannot be selected by `ListOptions.LabelSelector`.

### What we explicitly do not do
- No capture of read operations (GET) by default.
- No PGP-signed audit chain (the reference platform's pattern of auditable audit log tampering is enterprise; not relevant here).
- No encryption of audit row content (the data is sensitive but not secret — passwords are already hashed, secrets already encrypted).

## Migration path

If the audit seam needs to capture controller mutations in the future, the path is:

1. Extract a `state.Store` wrapper that calls back into an audit recorder.
2. Have the HTTP middleware call the same recorder.
3. The recorder writes to the same `audit_events` table; the source is tagged (HTTP vs internal).

This preserves the existing schema and query surface.

## See Also

- [ADR 16](0016-auth-jwt-only.md) — Auth scope (JWT + API tokens); identity is resolved from JWT claims in the audit middleware.
- [ADR 9](0009-kubernetes-native-api.md) — REST API surface that the middleware wraps.
