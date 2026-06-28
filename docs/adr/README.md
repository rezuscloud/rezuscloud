# Architecture Decision Records

Decisions that shape RezusCloud's architecture, recorded as the **current**
model and the direction forward. Each ADR records context, decision, and
consequences of one significant choice.

These ADRs describe the architecture **as it is today and where it is going**.
They do not carry historical contrast. The reasoning behind **rejected**
alternatives (gRPC providers, SideroLink, CAPI, Crossplane, embedded TF
library, etc.) is preserved separately in
[`../architecture-history/`](../architecture-history/README.md).

## Index

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-what-rezuscloud-is.md) | What RezusCloud Is — Tenant Orchestrator Above talosctl/kubectl | Accepted |
| [0002](0002-two-binary-model.md) | Two Binaries From One Repo (rezuscloud + rezusctl) | Accepted |
| [0003](0003-rest-api-kubernetes-model.md) | REST API Following the Kubernetes Resource Model | Accepted |
| [0004](0004-sqlite-state-store.md) | SQLite as the Initial State Store | Accepted |
| [0005](0005-tf-state-single-source-of-truth.md) | TF State as the Single Source of Truth for Declared Infrastructure | Accepted |
| [0006](0006-exec-tofu-binary.md) | Exec the `tofu` Binary for Infrastructure Reconciliation | Accepted |
| [0007](0007-provider-as-tf-wrapper.md) | Provider — The RezusCloud Module Wrapping a Real TF Provider | Accepted |
| [0008](0008-config-delivery-user-data-and-talos-api.md) | Config Delivery via `user_data` and Talos API (No SideroLink) | Accepted |
| [0009](0009-event-bus-nats.md) | NATS as the Event/Streaming Primitive | Accepted |
| [0010](0010-status-plane-best-effort.md) | Status Plane — Best-Effort, Never Authoritative | Accepted (mechanism deferred) |
| [0011](0011-webui-templ-htmx.md) | WebUI — templ + HTMX + Tailwind | Accepted |
| [0012](0012-auth-local-jwt-and-api-tokens.md) | Auth — Local JWT Users and API Tokens | Accepted |
| [0013](0013-audit-log-http-middleware.md) | Audit Log — HTTP Middleware Pattern | Accepted |
| [0014](0014-configpatch-single-scope.md) | ConfigPatch — Single Tenant-Wide Scope | Accepted |
| [0015](0015-read-only-surfacing.md) | Read-Only Surfacing of Lower-Layer State | Accepted |

## Numbering

ADRs are numbered sequentially starting at 0001. The previous ADR set (which
spanned three architecture eras and caused confusion) has been moved to
`../architecture-history/`; the numbering here starts fresh and does not
preserve the old numbers.
