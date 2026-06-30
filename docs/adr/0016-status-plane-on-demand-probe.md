# ADR 0016: Status-Plane Gathering — On-Demand Probe with Short TTL

## Status

Accepted

## Context

[ADR 0010](0010-status-plane-best-effort.md) decided the *principle*: status is
best-effort, never authoritative, never written to TF state, and RezusCloud
takes no external observability dependency. The *mechanism* — how status is
actually gathered — was deferred to this ADR.

RezusCloud needs status from two different planes:

1. **Management cluster** (where `rezuscloud` runs) — node capacity, pod
   requests/limits, CPU/memory usage. Used by the dashboard's resource-pressure
   visualization ([#75](https://github.com/rezuscloud/rezuscloud/issues/75)).
   A partial implementation already exists: `internal/metrics.Aggregator` queries
   the in-cluster Prometheus + Kubernetes API.

2. **Tenant clusters** (the clusters RezusCloud manages) — machine stage, node
   health, control-plane readiness. Gathering these requires reaching each
   tenant's Talos API and Kubernetes API, which needs cached credentials
   ([#92](https://github.com/rezuscloud/rezuscloud/issues/92)).

The mechanism must respect [ADR 0010](0010-status-plane-best-effort.md):
- No external metrics/logs/tracing stack
- Status may lag, may be stale, may be absent
- Graceful degradation when a tenant is unreachable

## Decision

**On-demand probe with a short in-memory TTL. Do not run background scrapers.**

### Mechanism

Status is gathered **lazily** — when an API consumer requests it (e.g., the
dashboard renders, the CLI calls `describe`). The probe result is cached in
memory with a short TTL (default 15s). Subsequent requests within the TTL return
the cached value; after it expires, the next request triggers a fresh probe.

```
GET /api/v1/tenants/{name} → handler → StatusGatherer.Gather(tenant)
  → cache hit (TTL < 15s)? → return cached
  → cache miss → probe tenant API (talosctl/kubectl via cached creds)
                 → cache result + timestamp → return
```

### Why on-demand, not periodic scrape

- **No background load on tenant APIs.** A periodic scraper hitting every tenant
  every N seconds is wasteful — most tenants are idle, and the operator rarely
  needs real-time status. On-demand probes only fire when someone is looking.
- **Simpler lifecycle.** No goroutine-per-tenant management, no retry/backoff
  storms on unreachable tenants, no memory pressure from cached stale data for
  tenants nobody watches.
- **Matches "best-effort" semantics.** If nobody asks, no status is gathered —
  which is exactly right for a system whose status is never authoritative.
- **Reuses existing patterns.** The management-cluster path already uses this
  model (`metrics.Aggregator.ClusterSummary` is called on-demand from the
  dashboard handler).

### Why a TTL, not always-fresh

- **Probes are expensive** (a subprocess or HTTP call per tenant). A 15s TTL
  coalesces rapid requests (e.g., a dashboard polling every 2s) into one probe.
- **15s staleness is acceptable** for orchestration status (ADR 0010: status may
  lag). Operators don't need sub-second freshness.

### The secrets cache (#92)

Tenant probes need credentials (talosconfig, kubeconfig). These are extracted
from TF state via `tofu state pull` after each apply and cached in memory
([#92](https://github.com/rezuscloud/rezuscloud/issues/92)). The status gatherer
uses the cached credentials to reach the tenant API. If credentials are absent
(tenant never applied, or apply failed), status returns "unavailable" — spec
stays intact (ADR 0010 invariant).

### What this does NOT do

- **No background scraping goroutines.** No controller periodically polls tenant
  APIs.
- **No timeseries store.** Status is a point-in-time snapshot, not historical
  data. Historical trends (if ever needed) come from the tenant's own
  Prometheus, not from RezusCloud.
- **No metrics dependency.** The management-cluster path queries the in-cluster
  Prometheus that already exists; RezusCloud does not deploy or manage one.

## Consequences

- **Tenant status is lazy.** A tenant that nobody queries has no status gathered.
  The first request after the TTL expires pays the probe cost.
- **Unreachable tenants degrade gracefully.** A probe failure returns stale
  cached status (if available) or "unavailable" — never blocks, never crashes.
- **The secrets cache is the prerequisite.** Without it (#92), tenant probes
  have no credentials and cannot reach tenant APIs. #92 must land before tenant
  status gathering is functional.
- **The management-cluster path is unchanged.** It already uses on-demand
  probes via `metrics.Aggregator`. This ADR formalizes that pattern as the
  project-wide decision.

## Status

The management-cluster path is implemented (`internal/metrics.Aggregator`).
Tenant-cluster status gathering and the secrets cache (#92) are not yet built.

## See Also

- [ADR 0010](0010-status-plane-best-effort.md) — the principle this mechanism
  implements
- [ADR 0005](0005-tf-state-single-source-of-truth.md) — spec plane (never mixed
  with status)
- [ADR 0015](0015-read-only-surfacing.md) — how much status surfaces in the
  UI/CLI
