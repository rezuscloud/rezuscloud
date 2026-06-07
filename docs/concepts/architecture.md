# Architecture

## Overview

RezusCloud is a single-repo project with two binaries:

| Binary | Runs on | Purpose |
|--------|---------|----------|
| `rezuscloud` | Management cluster / Docker / Home Assistant | REST API, WebUI, SideroLink, provider gRPC, state store |
| `rezusctl` | User's laptop / CI | CLI client: `boot` (standalone), all other commands talk to `rezuscloud` |

`rezusctl boot` creates the management cluster and deploys `rezuscloud`. After that, `rezuscloud` runs autonomously. The CLI is only needed for boot and convenience commands.

**rezusctl builds clusters. kubectl manages them. rezuscloud runs them.**

## Design Principles

1. **Single binary management plane.** One container runs the entire management plane — REST API, SideroLink server, Config Provider, provider gRPC, WebUI, health endpoints. Temporary unavailability does not affect running clusters.
2. **REST API following Kubernetes model.** Per-type endpoints, metadata/spec/status, labels, finalizers, JWT auth. Not CRDs — the API abstracts the state backend (SQLite, PostgreSQL, or CRDs).
3. **Pluggable providers for machines.** Provider binaries connect outbound to the management cluster. Works behind NAT, IPv6-only, CGNAT ([ADR 12](../adr/0012-provider-based-machine-provisioning.md)).
4. **Full Talos cluster lifecycle.** RezusCloud generates complete Talos configs for init, controlplane, and worker nodes. No hosted control planes ([ADR 14](../adr/0014-full-talos-cluster-lifecycle.md)).
5. **Docker-first development.** Validate orchestration locally before cloud ([ADR 6](../adr/0006-docker-first-platform.md)).
6. **Outbound-only connectivity.** Providers and machines connect to the management cluster, never the reverse.

## Layered Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Layer 4: User API (REST)                                   │
│  /api/v1/tenants, machines, nodegroups, providers, etc.     │
│  Accessed via: rezusctl CLI, WebUI, HTTP clients             │
├─────────────────────────────────────────────────────────────┤
│  Layer 3: Orchestration                                     │
│  Controller — watches state, dispatches to providers         │
│  Config Provider — generates Talos config for connected      │
│  machines, pushes over SideroLink                            │
├─────────────────────────────────────────────────────────────┤
│  Layer 2: Node Bootstrap                                    │
│  Talos machine config generation (init, controlplane, worker)│
│  SideroLink — machines pull config via WireGuard over gRPC   │
├─────────────────────────────────────────────────────────────┤
│  Layer 1: Machine Provisioning (pluggable providers)        │
│  gRPC providers — connect outbound to management cluster    │
│  hetzner, aws, oci, metal, kubevirt, static, equinix        │
├─────────────────────────────────────────────────────────────┤
│  Layer 0: Management Cluster Bootstrap                      │
│  rezusctl boot — docker / qemu                              │
└─────────────────────────────────────────────────────────────┘
```

## Component Topology

```
Management Plane (single binary/container)
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  rezuscloud                                                         │
│  ├── REST API (/api/v1/*)                                           │
│  │   ├── Tenant CRUD (clusters)                                     │
│  │   ├── Machine listing (cluster-wide + tenant-scoped)             │
│  │   ├── NodeGroup CRUD (tenant-scoped)                             │
│  │   ├── Provider listing                                           │
│  │   ├── JoinToken CRUD (tenant-scoped)                             │
│  │   ├── ConfigPatch CRUD (tenant-scoped)                           │
│  │   ├── User CRUD (admin only)                                     │
│  │   ├── Watch/SSE (real-time events)                               │
│  │   └── Log streaming (SSE per machine)                            │
│  ├── WebUI (templ + Tailwind + HTMX + Alpine.js)                    │
│  ├── SideroLink server (machine WireGuard connections)              │
│  ├── Config Provider (pushes Talos config to machines)              │
│  ├── Provider gRPC server (accepts provider connections)            │
│  ├── State store (SQLite / PostgreSQL / CRDs)                       │
│  ├── Auth (JWT, bcrypt, RBAC)                                       │
│  └── Health endpoints (/healthz, /readyz)                           │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
        ▲                         ▲                         ▲
        │ Reverse proxy           │ SideroLink               │ gRPC
        │ (Cloudflare/ngrok)      │ (machines pull config)   │ (providers connect)
        │                         │                         │
┌───────┴──────────┐  ┌─────────┴──────────┐  ┌────────────┴──────────┐
│ Public endpoint   │  │ Tenant Nodes       │  │ Provider (anywhere)    │
│ grpc.manage.      │  │ (Talos Linux)      │  │ hetzner, aws, oci,     │
│ rezus.cloud       │  │ edge,cloud,home    │  │ metal, static...       │
└──────────────────┘  └────────────────────┘  └────────────────────────┘
```

## State Store

Pluggable backend selected by `REZUSCLOUD_STORE`:

| Backend | Mode | When |
|---------|------|------|
| SQLite | Standalone | Docker, Home Assistant, single-node |
| PostgreSQL | Cluster | Production, multi-node |
| CRDs | Cluster | Kubernetes-native deployment |

SQLite is the MVP — WAL mode, auto-migration, FK cascades. All resources stored in a generic `resources` table with JSON spec/status columns.

## REST API Resources

| Type | Scope | Endpoints |
|------|-------|-----------|
| Tenant | Cluster-wide | `/api/v1/tenants` |
| Machine | Cluster-wide + tenant-scoped | `/api/v1/machines`, `/api/v1/tenants/{c}/machines` |
| NodeGroup | Tenant-scoped | `/api/v1/tenants/{c}/node-groups` |
| Provider | Cluster-wide | `/api/v1/providers` |
| JoinToken | Tenant-scoped | `/api/v1/tenants/{c}/join-tokens` |
| ConfigPatch | Tenant-scoped | `/api/v1/tenants/{c}/patches` |
| User | Cluster-wide (admin) | `/api/v1/users` |

All endpoints support:
- Structured errors: `{status, message, reason, code}`
- Labels and annotations for filtering
- Finalizers for graceful deletion
- Watch/SSE for real-time updates

## Boot Sequence

`rezusctl boot` creates the management cluster:

```
1. Create cluster   → Docker containers / QEMU VMs
2. Install CNI      → Cilium (Docker-specific values)
3. Deploy plane     → rezuscloud container
4. Health check     → verify cluster is ready
```

On re-run, completed steps are skipped. If drift is detected (containers missing after reboot), state is reset and re-provisioned.

## WebUI

Embedded in the management plane binary. Built with templ + Tailwind CSS v4 + HTMX 2.x + Alpine.js 3.x. Calls the REST API directly. No border-radius, dual-era theme, HA ingress compatible.

Pages: Dashboard, Tenants, Tenant Detail, Login.

## Project Layout

```
cmd/
  rezuscloud/        Management plane entry point
  rezusctl/          CLI entry point (boot + API client)
internal/
  api/               REST API router + handlers
    jointoken/       JoinToken CRUD
    logs/            SSE log streaming
    machine/         Machine listing
    middleware/      Recovery, CORS, structured errors
    nodegroup/       NodeGroup CRUD
    patch/           ConfigPatch CRUD + resolve
    provider/        Provider listing
  auth/              JWT, bcrypt, RBAC, User CRUD
  backup/            S3 backup (database + resource export)
  cli/               CLI-only packages (used by boot)
    addons/          Addon lifecycle (Cilium)
    apiclient/       HTTP client for REST API
    boot/            Management cluster boot orchestrators
    helm/            Helm Go SDK wrapper
    platform/        Docker and QEMU platform providers
    provider/        Cilium installer
    registry/        Resource type registry (short name → API path)
    rezusconfig/     Config file management (~/.rezuscloud/config)
    state/           Boot step state
    talosconfig/     Talos machine config generator
    version/         Version injection
  controller/        Finalizer controller
  credentials/       Talos certificate generator
  ingress/           HA ingress compatibility middleware
  state/             Pluggable state store (SQLite)
  statemachine/      Machine stage + Tenant phase state machines
  talosconfig/       Talos config generation (init/controlplane/worker)
  upgrade/           Rolling upgrade engine (Talos + K8s version policy)
  watch/             Event bus + watch API
  web/               WebUI handler + templ views
tests/
  integration/       Integration tests (real HTTP + real SQLite)
version/             Version package
docs/                Documentation
```

## See Also

- [ADR Index](adr/) — Architecture Decision Records
- [CLI Reference](../reference/cli.md) — All commands and flags
- [Getting Started](../getting-started/index.md) — Installation and first cluster
- [API Design](api-design.md) — Full REST API specification
