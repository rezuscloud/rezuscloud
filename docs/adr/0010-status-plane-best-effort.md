# ADR 0010: Status Plane — Best-Effort, Never Authoritative

## Status

Accepted (mechanism deferred)

## Context

[ADR 0005](0005-tf-state-single-source-of-truth.md) decides the *spec* plane:
declared infrastructure lives in TF state. The *status* plane — observed
runtime state (node health, machine stage, pod status) — needs its own
decision.

The key requirement, which this ADR makes non-negotiable: **observed state must
never be confused with declared state, and must never override it.** A scraped
"is this node ready" boolean is not the same kind of fact as "this node group
has 3 machines."

## Decision

**Status is best-effort and never authoritative. The mechanism that populates
it is deferred to a later phase.**

### What is decided now (the principle)

- **`status` is a separate plane from `spec`.** The two never mix.
- **`status` is never written back to TF state.** TF state holds declared
  infrastructure only.
- **`status` may lag, may be stale across restarts, and may be absent** if a
  tenant cluster is unreachable. The system degrades gracefully: declared
  `spec` stays intact and authoritative; only live observation goes dark.
- **RezusCloud does not become an observability platform.** It does not build a
  metrics system, a log aggregator, or a tracing backend. It surfaces enough
  status to orchestrate tenants (did my apply succeed? is this node healthy?)
  — nothing more. See [ADR 0015](0015-read-only-surfacing.md).

### What is deferred (the mechanism)

How status is populated — live scrape, on-demand probe, controller-set, or
some mix — is **not decided here**. ADR 0005 originally named a "Collector"
component as if designed; this ADR narrows that: only the principle is
decided, the component is not. The mechanism will be chosen when the phase
that needs it arrives, and will be recorded as its own ADR.

### Today's reality

No status-plane mechanism is built. Today `status` fields are written by API
handlers from ad-hoc observations. This is honest about where the project is.

## Consequences

- **No observability dependency.** RezusCloud depends on no external metrics/
  logs/tracing stack. Status primitives are built later, in-repo, scoped to
  what orchestration needs.
- **Status is read-only-by-nature from the operator's perspective** — the
  operator declares `spec`; the system observes `status`.
- **Future status-gathering components** must respect this ADR's principle:
  write only to the status plane, never to TF state.

## See Also

- [ADR 0005](0005-tf-state-single-source-of-truth.md) — the spec plane this
  complements
- [ADR 0015](0015-read-only-surfacing.md) — how much status surfaces in the
  UI/CLI
