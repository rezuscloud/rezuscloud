# ADR 0017: DuckDB as the Analytics Store (Complement to SQLite)

## Status

Accepted

## Context

RezusCloud is a **single-binary** cloud orchestrator ([ADR 0001](0001-what-rezuscloud-is.md),
[ADR 0002](0002-two-binary-model.md)). Its databases must therefore be
**extremely compact and self-contained** — no external server, no separate
process to operate, no extra container in the Pod. Each store is a single file
on the PVC, just like the SQLite database already is ([ADR 0004](0004-sqlite-state-store.md)).

SQLite is the right tool for the management plane's **transactional** state
([ADR 0004](0004-sqlite-state-store.md)): point reads/writes of the current
resource set, auth records, bookkeeping. That workload is OLTP — last-write-wins,
small rows, low-volume. It is the wrong tool for a different workload the
management plane is starting to generate: **append-only operational history that
must be queried analytically** — "how many reconciles failed this week?",
"average apply latency by tenant", "audit activity grouped by resource over 30
days". That workload is OLAP: append-heavy, time-ordered, scanned and aggregated
in bulk. SQLite scans rows for it; a columnar engine processes vectors of values
per operation.

Today that history either does not exist at all (resource `status` is overwritten
in place — a JSON blob, latest-wins) or lives in the wrong engine (the
`audit_events` table is SQLite doing OLAP work). The result is that the
management plane is **amnesiac about its own behaviour**: there is no record of
how long applies took, how reconcile phase transitions evolved, or what a failed
reconcile two days ago looked like.

The requirement: add an analytical store that is as compact and self-contained
as SQLite, without disturbing SQLite's OLTP role.

## Decision

**DuckDB is the management plane's analytics store, complementing SQLite. It is
a single embedded library storing data in a single `.duckdb` file on the same
PVC as the SQLite database.**

### The OLTP / OLAP split (the load-bearing boundary)

| | SQLite ([ADR 0004](0004-sqlite-state-store.md)) | DuckDB (this ADR) |
|---|---|---|
| Workload | OLTP — transactional | OLAP — analytical |
| Shape | Point reads/writes, small rows | Bulk append + scan/aggregate |
| Storage | Row-store, single file | Columnar-vectorized, single file |
| Lifetime | Current state (latest wins) | Operational history (append-only) |
| Holds | Spec + status point-state, auth, API tokens, bookkeeping, audit *rows* | Reconcile lifecycle events, apply telemetry, audit *analytics*, status-transition samples |
| Driver | `modernc.org/sqlite` (pure Go) | `github.com/duckdb/duckdb-go` (CGO) |

**SQLite stays the authoritative OLTP store. DuckDB never holds spec or current
status. The two never overlap; they are joined by key, never by migrating rows
between them.**

The audit log ([ADR 0013](0013-audit-log-http-middleware.md)) is the canonical
example of a DuckDB workload: append-only, time-ordered, queried with filters
and aggregations (by user, resource, verb, time range). Audit rows are written
to SQLite today; analytical queries over them are the first DuckDB candidate
(DuckDB can read the SQLite table directly, or events are dual-written).

### What DuckDB is for

Management-plane **operational history** — the system introspecting its own
behaviour:

- **Reconcile lifecycle events** — every phase transition (queued → applying →
  applied/failed), with duration, tenant, error. Powers trend charts and
  retroactive debugging ("what happened during that failed reconcile?").
- **Apply telemetry** — `tofu apply` durations, resource counts changed,
  exit status, per tenant over time.
- **Audit-trail analytics** — the aggregations over the audit log (ADR 0013)
  that today scan SQLite rows: activity by user/resource/verb over time ranges.
- **Status-transition samples** — *optional* point-in-time snapshots of the
  status plane (ADR 0016), kept as historical samples for trend charts. This is
  **not** the status plane itself (which stays point-in-time and amnesiac); it
  is a derived analytical copy.

### What DuckDB is NOT (boundary — prevents future contradictions)

- **Not a replacement for SQLite.** Different workload. SQLite remains the OLTP
  store (ADR 0004).
- **Not the status plane.** The status plane stays best-effort, point-in-time,
  never authoritative (ADR 0010, ADR 0016). DuckDB may hold *historical samples*
  derived from it, but it is never read as the source of current status.
- **Not a tenant observability backend.** Tenants have their own Prometheus;
  RezusCloud does not become an observability platform and does not collect
  tenant resource metrics (ADR 0010, ADR 0015). DuckDB holds the management
  plane's own operational telemetry, not tenant metrics.
- **Not a metrics/timeseries engine competing with the in-cluster Prometheus.**
  The `/metrics` endpoint (Prometheus exposition) and the management-cluster
  resource-pressure path (`internal/metrics`) are unchanged.

### Why DuckDB (the single-file columnar fit)

- **Single file, embedded, zero server** — matches the "compact, self-contained"
  constraint exactly. One `.duckdb` file alongside `state.db` on the PVC. No
  extra container, no PVC-per-store, no operator to run.
- **Columnar-vectorized** — analytical scans (aggregations, filters, group-bys
  over time-ordered events) run in vectorized batches, not row-by-row. This is
  the workload SQLite is structurally weakest at.
- **Reads Parquet/CSV/JSON natively** — operational history can be exported to
  Parquet for support handoffs or long-term archival without a separate tool.
- **`database/sql` driver** — same interface as SQLite; the store is accessed
  behind a Go interface, consistent with ADR 0004's pluggability.

### The CGO trade-off

DuckDB is a C++ library; the Go driver (`github.com/duckdb/duckdb-go`)
statically links it and requires `CGO_ENABLED=1`. This has a real consequence
for the build:

- The production binary moves from **`CGO_ENABLED=0` (pure-Go static) to
  `CGO_ENABLED=1` (statically-linked DuckDB)**. The runtime image moves from
  `distroless/static-debian12` to `distroless/base-debian12` (DuckDB needs
  `libc++`/glibc), and the binary grows (~40 MB for the statically-linked
  DuckDB library).

This is accepted because:

- **SQLite's pure-Go driver (`modernc.org/sqlite`) continues to work under
  `CGO_ENABLED=1`** — it does not require `CGO=0`, it merely does not need CGO.
  The OLTP store stays pure-Go; only DuckDB brings CGO.
- **The single-binary property is preserved.** DuckDB is a statically-linked
  library, not an external server. RezusCloud remains one container, one
  binary, one process.
- **It fits the "minimal dependencies" principle** (ADR 0001): DuckDB is an
  embedded library with no runtime dependencies, MIT-licensed — the same
  category of dependency as `modernc.org/sqlite`. It is *fewer* dependencies
  than introducing a server (Postgres/Timescale/ClickHouse) would be.

If CGO proves undesirable for a specific deployment (e.g. a Home Assistant
add-on image that must stay pure-Go static), DuckDB is accessed behind an
interface and can be compiled out with a build tag — the OLTP store keeps
working without it. The analytics capability is additive, never load-bearing
for correctness.

## Consequences

- **Two single-file databases on one PVC** — `state.db` (SQLite, OLTP) and
  `analytics.duckdb` (DuckDB, OLAP). Both compact, both embedded, both backed
  up by the same PVC snapshot.
- **Build requires CGO.** The Dockerfile and GoReleaser pipeline move to
  `CGO_ENABLED=1` with the DuckDB static library; the runtime base image gains
  glibc. CI must build with a C toolchain.
- **The management plane gains a memory of its own behaviour.** Reconcile
  trends, apply latency, and audit analytics become queryable — without making
  the status plane authoritative or becoming a tenant observability platform.
- **SQLite's role is unchanged.** No migration; the OLTP store and all its
  callers are untouched.
- **Retention is per-store.** DuckDB history is partitioned/time-bucketed and
  pruned on its own schedule, independent of the audit retention
  (ADR 0013) and SQLite row lifetime.

## See Also

- [ADR 0001](0001-what-rezuscloud-is.md) — the self-contained / minimal-deps
  principle DuckDB satisfies
- [ADR 0004](0004-sqlite-state-store.md) — the OLTP store DuckDB complements
- [ADR 0005](0005-tf-state-single-source-of-truth.md) — neither store holds
  declared infrastructure (that is TF state)
- [ADR 0010](0010-status-plane-best-effort.md) — DuckDB is not the status plane
- [ADR 0013](0013-audit-log-http-middleware.md) — audit is the first DuckDB
  analytical workload
- [ADR 0016](0016-status-plane-on-demand-probe.md) — status stays amnesiac;
  DuckDB may hold derived samples, never current status
