# ADR 0004: SQLite as the Initial State Store

## Status

Accepted

## Context

The management plane needs persistent state for its own data: API resources,
auth records, audit log, metadata about tenants. (Where *declared
infrastructure* lives is a separate question — see
[ADR 0005](0005-tf-state-single-source-of-truth.md).)

RezusCloud must be self-contained and runnable as a single container
(including as a Home Assistant add-on). The initial state store must work with
zero external dependencies.

## Decision

**SQLite is the state store for the management plane, initially.**

- Pure-Go driver (`modernc.org/sqlite`), so the binary builds with
  `CGO_ENABLED=0` and runs anywhere.
- PVC-backed in production; a single file on disk in development.
- All API resources live in a generic `resources` table with JSON `spec` and
  `status` columns, plus typed accessor helpers.

### Scope of this store

This store holds the management plane's **own** state:

- Non-infrastructure API resources (Users, API tokens, audit events).
- Metadata and bookkeeping (finalizers, resource versions, labels).
- **Status** fields for all resources (see
  [ADR 0010](0010-status-plane-best-effort.md) — status is best-effort and
  lives here).

It is **not** the source of truth for declared infrastructure — that is TF
state (see [ADR 0005](0005-tf-state-single-source-of-truth.md)). Nor is it
the analytical store for append-only operational history — that is DuckDB
(see [ADR 0017](0017-duckdb-analytics-store.md)).

### Evolution

The store is accessed behind a Go interface so a future backend (PostgreSQL
for HA, or CRDs for Kubernetes-native deployment) can replace SQLite without
touching callers. The initial cut keeps SQLite to minimise dependencies
([ADR 0001](0001-what-rezuscloud-is.md)).

## Consequences

- **Single-writer.** SQLite is single-writer, so the management plane runs as
  a single replica. HA (multi-replica) is deferred to whenever the store
  backend changes.
- **No external database dependency.** RezusCloud ships as one container with
  one file on disk.
- **Pluggable later.** The interface leaves room for PostgreSQL/CRDs without
  an architecture change.

### OLTP boundary (complemented by DuckDB)

SQLite is the **OLTP** store: point reads/writes of current state, small rows,
latest-write-wins. The management plane's **OLAP** workload — append-only
operational history queried analytically (reconcile events, apply telemetry,
audit aggregations) — is a different workload and lives in DuckDB
([ADR 0017](0017-duckdb-analytics-store.md)), a separate single-file database
on the same PVC. The two never overlap: SQLite holds current state, DuckDB
holds history. Neither holds declared infrastructure (ADR 0005).

## See Also

- [ADR 0001](0001-what-rezuscloud-is.md) — the self-contained / minimal-deps
  principle
- [ADR 0005](0005-tf-state-single-source-of-truth.md) — what this store does
  *not* hold (declared infrastructure)
- [ADR 0017](0017-duckdb-analytics-store.md) — the OLAP analytics store that
  complements this OLTP store
