# RezusCloud Components

> **Type:** Explanation · **Audience:** Anyone seeking understanding

This page provides a high-level overview of the components that make up the
RezusCloud management plane. For the architecture behind these components, see
[Architecture](architecture.md). For the decisions that shaped them, see the
[ADRs](../adr/README.md).

## Overview

RezusCloud is a single binary (`rezuscloud`) that runs as the management plane —
a long-running server that declares Kubernetes clusters (tenants), realises them
on infrastructure via OpenTofu, and surfaces their state read-only. A second
binary (`rezusctl`) is the CLI client.

```
┌─────────────────────────────────────────────────────────────────────┐
│                     rezuscloud (management plane)                    │
│                                                                      │
│  ┌──────────┐  ┌──────────┐  ┌────────────┐  ┌───────────────────┐  │
│  │ HTTP API │  │  WebUI   │  │ TF Backend │  │ TF Execution Eng. │  │
│  │ (REST)   │  │ (templ)  │  │ (state)    │  │ (exec tofu)       │  │
│  └────┬─────┘  └────┬─────┘  └──────┬─────┘  └────────┬──────────┘  │
│       │              │               │                  │            │
│       └──────────────┴───────┬───────┴──────────────────┘            │
│                              │                                       │
│                     ┌────────▼────────┐    ┌──────────────┐          │
│                     │  Apply Queue    │───▶│  Providers   │          │
│                     │  (debounced)    │    │  (oci/os/metal)│         │
│                     └────────┬────────┘    └──────────────┘          │
│                              │                                       │
│              ┌───────────────┼───────────────┐                       │
│              │               │               │                       │
│     ┌────────▼───┐  ┌───────▼──────┐  ┌────▼─────────┐              │
│     │ Event Bus  │  │   Store      │  │  Projection  │              │
│     │ (NATS)     │  │   (SQLite)   │  │  (TF state)  │              │
│     └────────────┘  └──────────────┘  └──────────────┘              │
│                                                                      │
│              ┌───────────────────────────────────┐                   │
│              │     Status Plane (on-demand)      │                   │
│              │     + Rolling Upgrade Engine      │                   │
│              └───────────────────────────────────┘                   │
└─────────────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────┐
│  Cloud / Bare Metal │
│  (via tofu apply)   │
└─────────────────────┘
```

## API Layer

### HTTP API (REST)

The primary interface for creating and managing tenants, node groups, machines,
and config patches. Follows the Kubernetes API model: every resource has
`metadata`, `spec`, and `status` fields. Supports optimistic concurrency
(`resourceVersion`), label-based selection, finalizer-controlled deletion, and
watch/SSE streaming.

- **Code:** `internal/api/`
- **ADR:** [0003 — REST API, Kubernetes model](../adr/0003-rest-api-kubernetes-model.md)

### WebUI

A server-rendered web interface using templ + HTMX + Tailwind CSS. Provides the
same operations as the API (create clusters, add node groups, watch
reconciliation) plus read-only surfacing of lower-layer state (machine health,
logs via `talosctl dmesg`). Never duplicates `kubectl` — once a cluster is up,
the user downloads a kubeconfig and works with the lower layers directly.

- **Code:** `internal/web/`
- **ADR:** [0011 — WebUI: templ + HTMX](../adr/0011-webui-templ-htmx.md), [0015 — Read-only surfacing](../adr/0015-read-only-surfacing.md)

## Reconciliation Layer

### Apply Queue

A debounced, per-tenant queue that coalesces rapid spec changes into a single
`tofu apply`. When a user creates or updates a tenant or node group, the store
fires a mutation event through NATS → the EnqueueBus enqueues the tenant → the
queue debounces → the Applier runs `tofu init` + `tofu apply`. Serial within a
tenant, parallel across tenants.

- **Code:** `internal/applyqueue/`, `internal/reconcile/`
- **ADR:** [0005 — TF state single source of truth](../adr/0005-tf-state-single-source-of-truth.md)

### TF Execution Engine

Execs the `tofu` binary as a subprocess in per-tenant work directories. Handles
`tofu init`, `tofu apply`, `tofu state pull`, with the RezusCloud HTTP backend
as the remote state endpoint. State encryption is conditional on
`REZUSCLOUD_STATE_PASSPHRASE` (pbkdf2 + aes_gcm).

- **Code:** `internal/tfexec/`
- **ADR:** [0006 — Exec tofu binary](../adr/0006-exec-tofu-binary.md)

### Providers

RezusCloud-side Go modules that generate the `.tf.json` configuration that `tofu`
applies. Each provider wraps a real Terraform registry provider (OCI, OpenStack)
or operates directly against the Talos API (Metal for bare metal). Providers
declare the mapping between TF resource types and RezusCloud resources, which the
projection index uses to extract machines from TF state.

- **Code:** `internal/provider/oci/`, `internal/provider/openstack/`, `internal/provider/metal/`
- **ADR:** [0007 — Provider as TF wrapper](../adr/0007-provider-as-tf-wrapper.md)

### State Projection

Reads the TF state JSON after each apply and projects it into an in-memory index.
Each provider declares which TF resource types map to which RezusCloud kinds
(e.g., `oci_core_instance` → `Machine`). The projection extracts management
addresses, provider IDs, shapes, and hostnames from the TF state attributes.

- **Code:** `internal/projection/`

### Store Enrichment

After each successful apply, the StoreEnricher merges the projected TF-state
machines into the store. New cloud instances that don't exist in the store are
created; existing machines get their spec fields (management address, provider
ID, shape) refreshed. Management-plane status (stage, ready, connected) is never
overwritten — it comes from observation.

- **Code:** `internal/reconcile/enricher.go`

## State Layer

### Store (SQLite)

The management plane's own database. Stores tenants, node groups, machines,
config patches, providers, users, API tokens, audit logs, and the Talos secrets
bundles. SQLite is the v1 backend — the `StoreAPI` interface allows future
backends (PostgreSQL, etc.) without changing callers.

- **Code:** `internal/state/`
- **ADR:** [0004 — SQLite state store](../adr/0004-sqlite-state-store.md)

### TF HTTP Backend

RezusCloud implements the Terraform HTTP backend protocol. `tofu` reads and
writes state through RezusCloud's endpoint (`/tfstate`), so TF state is stored
in the management database alongside the management-plane data. One state blob
per tenant.

- **Code:** `internal/tfbackend/`

## Event Layer

### Event Bus (NATS)

NATS, embedded in-process in the single-replica management plane, is the single
event/streaming primitive. Both resource-change events (WebUI SSE) and
async-controller events (reconciliation triggers) flow through it. The REST
watch/SSE HTTP surface subscribes to NATS under the hood.

- **Code:** `internal/watch/`
- **ADR:** [0009 — Event bus: NATS](../adr/0009-event-bus-nats.md)

## Status Layer

### Status Gatherer

Performs on-demand tenant health probes with a short in-memory TTL (15 seconds).
No background scrapers — probes fire only when the health endpoint is hit. Probes
control-plane machines first, falls back to workers, via the real Talos API.

- **Code:** `internal/status/`
- **ADR:** [0010 — Status plane: best-effort](../adr/0010-status-plane-best-effort.md), [0016 — On-demand probe](../adr/0016-status-plane-on-demand-probe.md)

### Rolling Upgrade Engine

Detects Talos version drift between the tenant spec and observed machines. When a
version bump occurs, the engine runs a rolling upgrade (one machine at a time via
the Talos API: upgrade → health check → rollback on failure) **before** `tofu
apply` regenerates version-specific config.

- **Code:** `internal/upgrade/`, `internal/upgrade/talos/`

## Supporting Components

### Auth (JWT + API Tokens)

Local authentication with JWT for browser sessions and API tokens for programmatic
access. Role-based access control (admin, edit, view).

- **Code:** `internal/auth/`
- **ADR:** [0012 — Auth: local JWT + API tokens](../adr/0012-auth-local-jwt-and-api-tokens.md)

### Audit Log

HTTP middleware that records every mutating request (method, path, user,
timestamp) for compliance and debugging.

- **Code:** `internal/audit/`
- **ADR:** [0013 — Audit log: HTTP middleware](../adr/0013-audit-log-http-middleware.md)

### Credentials & Secrets

Auto-generates the Talos secrets bundle (cluster PKI) when a tenant is created.
Caches the bundle in memory for the upgrade engine and status probe. Secrets live
in the store, not in TF variables.

- **Code:** `internal/credentials/`
- **ADR:** [0005 — Secrets via TF resource](../adr/0005-tf-state-single-source-of-truth.md)

### Config Rendering

Generates Talos machine configuration using the `talos` Terraform provider's
`talos_machine_configuration` data source. Version-aware: the config is
generated for the specific Talos + Kubernetes version declared in the tenant spec.

- **Code:** `internal/configrender/`, `internal/talosconfig/`
- **ADR:** [0008 — Config delivery: user_data + Talos API](../adr/0008-config-delivery-user-data-and-talos-api.md)

## CLI Binary

### rezusctl

A static binary with two modes:
- **Boot** (`rezusctl boot`) — standalone, no API server needed. Boots Talos on
  Docker or QEMU platforms for bare-metal provisioning.
- **Client** (`rezusctl get/create/apply/delete/describe`) — thin client against
  the REST API. Verb-driven model like `kubectl`.

- **Code:** `cmd/rezusctl/`
- **ADR:** [0002 — Two-binary model](../adr/0002-two-binary-model.md)

## See Also

- [Architecture](architecture.md) — how the components fit together
- [API Design](api-design.md) — why the Kubernetes API model
- [ADRs](../adr/README.md) — the decisions behind each component
