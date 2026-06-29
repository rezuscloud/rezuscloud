# ADR 0001: What RezusCloud Is — Tenant Orchestrator Above talosctl/kubectl

## Status

Accepted

## Context

RezusCloud needs a crisp answer to "what is this software?" before any other
architectural decision can be evaluated. Earlier ADR sets drifted because they
described *mechanisms* (gRPC providers, SideroLink, CRDs) without first
anchoring the *product*.

RezusCloud is a **tenant orchestrator**. A **tenant** is a full Kubernetes
cluster under management (the user-facing word is *cluster*; the API word is
*tenant*). The operator uses RezusCloud to declare what clusters should exist
and how they should be configured; RezusCloud realises that declaration on top
of lower-layer tools — Talos for the node OS and Kubernetes for the workload
plane.

The defining rule, from which every other decision follows:

> **RezusCloud lives one layer above `talosctl` and `kubectl`. It never
> duplicates their features. It orchestrates them.**

`talosctl` manages Talos nodes. `kubectl` manages Kubernetes objects.
RezusCloud orchestrates *clusters* (tenants) — declaring them, bringing them
into existence, upgrading them, and surfacing their state — by driving the
lower-layer tools, never by reimplementing them.

## Decision

RezusCloud is a **single product with two faces**:

1. **A CLI tool (`rezusctl`)** — for bootstrapping the management plane and for
   convenience operations against the running management plane.
2. **A runtime with a web interface (`rezuscloud`)** — the long-running
   management plane that owns tenant lifecycle.

Both drive infrastructure through **OpenTofu providers** (see
[ADR 0007](0007-provider-as-tf-wrapper.md)) and store their own data in a
**SQLite database** (see [ADR 0004](0004-sqlite-state-store.md)), at least
initially.

### The layering rule

```
┌──────────────────────────────────────────────────────────────┐
│  RezusCloud — tenant orchestration                            │
│  Declares tenants, reconciles them via `tofu apply`,          │
│  surfaces cluster state read-only.                            │
├──────────────────────────────────────────────────────────────┤
│  talosctl / kubectl — the lower layers                        │
│  Node OS management, in-cluster object management.            │
│  RezusCloud drives these; it does not reimplement them.       │
├──────────────────────────────────────────────────────────────┤
│  Talos nodes + Kubernetes clusters (the tenants)              │
└──────────────────────────────────────────────────────────────┘
```

Once a tenant is deployed, day-to-day work happens at the lower layers
(`kubectl` against the tenant). RezusCloud stays responsible for the
*cluster-as-a-unit*: its existence, its configuration, its upgrades, its
teardown.

## Consequences

- **RezusCloud is not a Kubernetes distribution, not a Talos replacement, not
  an in-cluster workload manager.** Anything `kubectl` already does inside a
  tenant is out of scope for RezusCloud.
- **Read-only surfacing is in scope; interactive duplication is not.** See
  [ADR 0015](0015-read-only-surfacing.md). RezusCloud may show that a node is
  unhealthy; it does not provide a shell into a pod.
- **Self-contained.** RezusCloud must be the only component needed to run the
  personal cloud (for example, packaged as a Home Assistant container add-on).
  This rules out architectures that distribute responsibility across external
  controllers or sidecar processes.
- **Minimal dependencies.** The fewer external components RezusCloud depends
  on, the better. Primitives that RezusCloud needs (an event bus, a state
  store) are chosen or built with this in mind. See
  [ADR 0004](0004-sqlite-state-store.md) and [ADR 0009](0009-event-bus-nats.md).

## See Also

- [ADR 0002](0002-two-binary-model.md) — the CLI + runtime split
- [ADR 0007](0007-provider-as-tf-wrapper.md) — how RezusCloud talks to
  infrastructure APIs
- [`../architecture-history/`](../architecture-history/README.md) — why CAPI,
  Crossplane, and standalone gRPC provider binaries were rejected
