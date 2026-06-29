# ADR 0009: NATS as the Event/Streaming Primitive

## Status

Accepted

## Context

RezusCloud needs event streaming for two purposes:

1. **WebUI live updates** — the dashboard and resource pages subscribe to
   changes (a tenant's phase changed, a machine's stage updated, an apply
   finished).
2. **Asynchronous controller operations** — the reconciliation loop (see
   [ADR 0006](0006-exec-tofu-binary.md)) runs async; controllers need to
   publish progress and completion events, and subscribers (the API, the
   WebUI, future notifications) need to receive them.

An earlier ADR removed NATS because the REST watch endpoint made it redundant
for resource-change events. That reasoning held while the only event producer
was the store. It no longer holds: async controllers introduce a second
producer with different characteristics (long-running, multi-step, not tied to
a single resource mutation), and hand-rolling pub/sub for it is exactly the
kind of reimplemented-core anti-pattern [ADR 0006](0006-exec-tofu-binary.md)
rejected for infrastructure.

The minimal-dependencies principle ([ADR 0001](0001-what-rezuscloud-is.md))
makes this a deliberate trade-off: NATS is the one event-streaming dependency,
justified because it replaces bespoke coordination logic.

## Decision

**NATS is the event/streaming primitive for RezusCloud.**

- **One event system.** Both resource-change events (for the WebUI's SSE) and
  async-controller events flow through NATS. The previous in-process
  `internal/watch` bus is replaced by a NATS backend.
- **Embedded, in-process first.** For the single-replica management plane, NATS
  runs embedded in the `rezuscloud` process — no separate NATS server to
  deploy. This preserves the self-contained property.
- **The REST watch/SSE endpoints stay** as the HTTP surface; they subscribe to
  NATS under the hood and republish to HTTP clients.

### Why one bus, not two

- **Simpler mental model.** One event system to learn, operate, and test.
- **Cross-producer subscriptions.** A WebUI page can subscribe to both
  resource changes and controller progress through one stream.
- **Future-proof.** If RezusCloud ever scales to multiple replicas, NATS
  extends naturally (shared server or JetStream); an in-process bus does not.

## Consequences

- **One new direct dependency** (`github.com/nats-io/nats.go`), traded against
  removing bespoke pub/sub code.
- **The `internal/watch` package is reworked** to publish/subscribe through
  NATS rather than its own in-process channel bus.
- **Embedded NATS keeps the deployment single-container.** No external NATS
  server required for the common case.
- **The REST watch/SSE API surface is unchanged** — clients still consume via
  HTTP; the transport underneath switches to NATS.

## See Also

- [ADR 0001](0001-what-rezuscloud-is.md) — the minimal-dependencies principle
  this trades against
- [ADR 0003](0003-rest-api-kubernetes-model.md) — the watch/SSE surface NATS
  backs
- [`../architecture-history/`](../architecture-history/README.md) — the
  earlier NATS removal and why it no longer holds
