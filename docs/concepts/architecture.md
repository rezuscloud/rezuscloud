# Architecture

> **Type:** Explanation · **Audience:** Anyone seeking understanding

RezusCloud is a **tenant orchestrator** that lives one layer above `talosctl`
and `kubectl`. It declares Kubernetes clusters (called *tenants*), realises
them on top of lower-layer tools, and surfaces their state read-only. It never
duplicates lower-layer features. See [ADR 0001](../adr/0001-what-rezuscloud-is.md).

## Binaries

One repository, two binaries (the `kubectl`-in-`kubernetes` model):

| Binary | Runs on | Purpose |
|---|---|---|
| `rezuscloud` | Management host / Docker / Home Assistant | REST API, WebUI, TF execution engine, TF HTTP backend, reconciliation |
| `rezusctl` | Operator's laptop / CI | `boot` (standalone) + thin client commands against the REST API |

`rezusctl boot` creates the management cluster and deploys `rezuscloud`. After
that, `rezuscloud` runs autonomously. The CLI is only needed for boot and
convenience.

**rezusctl builds clusters. kubectl manages them. rezuscloud runs them.**

## Layering

```
┌──────────────────────────────────────────────────────────────┐
│  RezusCloud — tenant orchestration                            │
│  Declares tenants, reconciles via `tofu apply`,              │
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
(`kubectl` against the tenant, via a kubeconfig obtained from
`rezusctl kubeconfig`). RezusCloud stays responsible for the cluster-as-a-unit:
existence, configuration, upgrades, teardown.

## State — two planes, never mixed

| Plane | What it holds | Source | Authority |
|---|---|---|---|
| **Spec** | Declared infrastructure (tenant, node group, machine identity) | TF state | Authoritative — see [ADR 0005](../adr/0005-tf-state-single-source-of-truth.md) |
| **Status** | Observed runtime state (node health, machine stage) | Best-effort observation (mechanism deferred — [ADR 0010](../adr/0010-status-plane-best-effort.md)) | Never authoritative, never written to TF state |

The management plane's **own** data (users, API tokens, audit, status fields)
lives in **SQLite** initially — see [ADR 0004](../adr/0004-sqlite-state-store.md).

## How RezusCloud talks to infrastructure

RezusCloud drives infrastructure through **OpenTofu providers**. A **Provider**
is the RezusCloud module (`internal/provider/<name>/`) that wraps a real
registry TF provider (oci, openstack, talos, …) — it generates the standard
`.tf.json` that `tofu apply` runs against, and declares how the resulting TF
state projects back into RezusCloud resources. See
[ADR 0007](../adr/0007-provider-as-tf-wrapper.md).

RezusCloud exec's the `tofu` binary as a subprocess; it does not embed TF
libraries and does not use custom provider-plugin binaries. See
[ADR 0006](../adr/0006-exec-tofu-binary.md).

## Events

NATS is the event/streaming primitive, embedded in-process for the
single-replica management plane. Both resource-change events (WebUI SSE) and
async-controller events flow through it. See
[ADR 0009](../adr/0009-event-bus-nats.md).

## REST API

Kubernetes resource model: per-type endpoints, `metadata`/`spec`/`status`,
labels, finalizers, structured errors, optimistic concurrency, watch/SSE. JWT
+ API-token auth. See [ADR 0003](../adr/0003-rest-api-kubernetes-model.md) and
[ADR 0012](../adr/0012-auth-local-jwt-and-api-tokens.md).

## WebUI

Server-rendered HTML (templ + HTMX + Tailwind + Alpine.js). Surfaces tenant
orchestration state read-only and triggers orchestration actions. It is not a
Kubernetes console. See [ADR 0011](../adr/0011-webui-templ-htmx.md) and
[ADR 0015](../adr/0015-read-only-surfacing.md).

## Project layout

```
cmd/
  rezusctl/          CLI entry point (boot + API client)
internal/
  api/               REST API router + handlers (per resource type)
  auth/              JWT, bcrypt, RBAC, users, API tokens
  audit/             HTTP-middleware audit log
  backup/            Backup manager
  cli/               CLI-only packages (boot, platform, helm, apiclient, …)
  controller/        Reconciliation controllers
  credentials/       Kubeconfig / talosconfig generation
  ingress/           HA-ingress compatibility middleware
  state/             SQLite state store
  talosconfig/       Talos config generation
  upgrade/           Rolling upgrade engine
  watch/             Event bus (NATS-backed)
  web/               WebUI handler + templ views
  tfbackend/         OpenTofu HTTP state backend
  tfexec/            OpenTofu subprocess driver
  tfencryption/      OpenTofu state encryption config
  applyqueue/        Debounced per-tenant apply queue
  provider/          RezusCloud TF-Provider wrappers (oci, openstack, metal, …)
  projection/        TF-state → API-resource projection (under construction)
main.go              Server entry point
```

Component build status (which of the above are wired into `main.go` vs
scaffolded vs planned) is tracked in `CONTEXT.md`.

## See Also

- [ADR Index](../adr/README.md) — the live decision records
- [Architecture History](../architecture-history/README.md) — rejected
  alternatives and why
- [CONTEXT.md](../../CONTEXT.md) — status-honest map of what is built vs
  planned
