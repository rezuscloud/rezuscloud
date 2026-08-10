# CONTEXT.md

> **This document describes the architecture as a status-honest map.** Every
> component is tagged: **[built]** = on `main` and wired into the server;
> **[scaffolded]** = code exists but not yet wired; **[planned #NN]** = not yet
> built, tracked by issue. The *model* (concepts + ADRs) is authoritative; the
> *status* tags track how much of it is real today. Update tags when code lands.

## Glossary

| Term | Definition |
|------|-----------|
| **RezusCloud** | A tenant orchestrator that lives one layer above `talosctl` and `kubectl`. It declares Kubernetes clusters (tenants), realises them on top of lower-layer tools, and surfaces their state read-only. It never duplicates lower-layer features (ADR 0001). |
| **rezuscloud** | Server binary — the long-running management plane: HTTP API, WebUI, TF HTTP backend, TF execution engine, reconciliation. |
| **rezusctl** | CLI binary — `boot` (standalone) + thin client commands against the REST API. Static binary, no container image. |
| **Management Plane** | The running `rezuscloud` process. Owns cluster lifecycle, config delivery, state. |
| **Management Node** | The role `rezuscloud` plays in the management mesh (ADR 0018): the WireGuard hub every node maintains a continuous, NAT-traversing link to. |
| **Tenant** | A full Talos cluster under management. Has its own etcd, API server, kubelets. User-facing term: **cluster** (CLI uses `cluster`, API uses `tenant`). |
| **NodeGroup** | A set of machines within a tenant sharing the same role (controlplane/worker) and provider. |
| **Machine** | A physical or virtual machine running Talos. Identified by hardware UUID. |
| **Provider** | The RezusCloud module that wraps a real Terraform provider (oci, openstack, talos, …) to maintain the infrastructure RezusCloud manages. One Provider per platform, living in `internal/provider/<name>/`. It generates the standard `.tf.json` that `tofu` applies (ADR 0006) and declares the mapping between TF resource types and RezusCloud resources. **There is no separate "RezusCloud provider" and "TF provider" — there is one TF-based Provider that wraps a real registry provider.** gRPC is used only if a given real TF provider requires it; otherwise RezusCloud generates config and execs `tofu` directly (ADR 0007). |
| **Config Delivery** | How a Talos node receives its config: the node **pulls** its full config from rezuscloud over the SideroLink tunnel (ADR 0008/0018). rezuscloud generates the config via the TF Talos provider during `tofu apply` (version-aware) and serves it; the node pulls and converges. Only the minimal bootstrap (SideroLink kernel arg + WireGuard key) is pushed once via user_data/boot image. Tenant assignment is still determined by which TF state apply targets. **[planned #189]** — today delivery is still push (user_data + Talos API). |
| **SideroLink Tunnel** | The persistent, node-initiated WireGuard-over-gRPC tunnel (ADR 0018) connecting every node to the management node. Nodes dial outbound (NAT-friendly via STUN + relay); it carries config-pull (node←platform) and on-demand telemetry-pull (platform←node). Push nowhere. Cold-boot bootstrap still via user_data/boot image. **[planned #189]**. |
| **Embedded Discovery** | The in-process cluster-discovery service (ADR 0019) by which tenant nodes learn their peers' reachable (SideroLink tunnel) addresses across networks, so etcd gossip and kube lookups work. Coordination, not transport; not the public discovery service. |
| **ConfigPatch** | User-defined Talos config overlay applied during config generation. Single tenant-wide scope (ADR 0014). |
| **JoinToken** | **Deprecated.** SideroLink is adopted (ADR 0018), but rezuscloud needs no join token: peers authenticate by WireGuard key, and the node→tenant mapping comes from the TF-created machine record (declare-first), not a token. The JoinToken API resource, store methods, CLI subcommand, and WebUI pages are slated for removal. |
| **TF State** | The single source of truth for **declared infrastructure** (resource `metadata` + `spec`). One state per tenant, stored in RezusCloud's TF HTTP backend. K8s-style REST APIs project `spec` from TF state through provider-declared resource mappings. Does **not** hold observed/runtime state. |
| **Reconciliation** | The async loop: spec change → controller detects drift → `tofu apply` → TF state updated → status updated. Events flow through NATS at each phase (ADR 0009). |
| **Apply Queue** | Debounced, per-tenant queue. All spec writes for a tenant coalesce within a debounce window; when it drains, a single `tofu apply` reconciles the whole tenant. Serial within a tenant, parallel across tenants. A slow periodic resync re-enqueues every tenant to catch external drift. |
| **Event Bus** | NATS, embedded in-process in the single-replica management plane. The single event/streaming primitive — both resource-change events (WebUI SSE) and async-controller events flow through it (ADR 0009). |
| **State Encryption** | OpenTofu's native state encryption (`pbkdf2` + `aes_gcm`). Applied by `tofu` in the generated `.tf.json`, NOT by RezusCloud's HTTP backend. The backend stores opaque encrypted blobs. RezusCloud never reimplements crypto — every decrypt goes through `tofu state pull` with `TF_ENCRYPTION` set. |
| **Status Plane** | Observed runtime state (node health, machine stage) — **best-effort, never authoritative, never written to TF state.** The principle (ADR 0010) and the mechanism are decided: **on-demand probe with short in-memory TTL** (ADR 0016, 15 s). No background scrapers — probes fire only when the health endpoint is hit. RezusCloud does not depend on an external observability stack and does not build one. |
| **Analytics Store** | DuckDB — a single-file columnar analytical database complementing SQLite. Holds the management plane's **operational history**: reconcile lifecycle events, apply telemetry, audit-trail analytics, and (optionally) derived status samples. **Not** a replacement for SQLite (different workload: OLAP vs OLTP), **not** the status plane (status stays point-in-time/amnesiac per ADR 0016), **not** a tenant-observability backend (ADR 0010). Two single-file databases on one PVC: `state.db` (SQLite, OLTP) + `analytics.duckdb` (DuckDB, OLAP). Decision: ADR 0017. `[planned #187]` |

## Architecture

One repo, two binaries (Kubernetes kubectl model):

```
rezuscloud binary (server)                       rezusctl binary (CLI)
├── HTTP API (REST, K8s-style) [built]            ├── boot (Docker/QEMU platforms) [built]
├── WebUI (templ + HTMX) [built]                  ├── tenant create/list/delete [built]
├── TF HTTP backend (state store) [built]         └── kubeconfig extraction [built]
├── TF execution engine (exec tofu) [built]
├── Apply Queue (debounced per-tenant) [built]
├── Event Bus (NATS, embedded) [built #110]
├── Controllers (async reconciliation) [built]
│   ├── TenantReconciler          [built]
│   ├── NodeGroupReconciler       [built]
│   └── UpgradeReconciler         [built]
├── Providers (TF config generation) [built]
├── State projection (TF state → API spec) [built]
├── Store enrichment (projection → store) [built #139]
├── Tenant health (on-demand probe) [built #139]
├── Config generation (Talos) [built]
├── Rolling upgrades [built]
├── SideroLink management tunnel [planned #189, ADR 0018]
└── Embedded cluster discovery [planned #189, ADR 0019]
```

### Two data planes, never mixed (ADR 0005)

- **Spec plane (declared):** TF state is the single source of truth for declared
  infrastructure. One TF state per tenant, stored via RezusCloud's TF HTTP
  backend. K8s-style REST API `spec` fields are projections of TF state, mapped
  through provider-declared resource schemas.
- **Status plane (observed):** Runtime data (node health, machine stage) is
  best-effort and **never authoritative** — it may lag, may be stale, and may
  be absent. **Never written back to TF state.** The principle is decided
  (ADR 0010); the mechanism is decided and built: **on-demand probe with
  short in-memory TTL** (ADR 0016, 15 s, `internal/status/`). No background
  scrapers — probes fire only when the health endpoint is hit.

The `metadata` + `spec` of an infrastructure resource come from TF state;
`status` comes from observation. The two never mix. RezusCloud does not depend
on an external observability stack and does not build one now.

### OLTP vs OLAP stores (ADR 0004 + ADR 0017)

The management plane runs **two single-file embedded databases** on one PVC,
split by workload:

- **SQLite** (ADR 0004) — the **OLTP** store: current state, point
  reads/writes, latest-write-wins. Holds spec/status point-state, auth, API
  tokens, bookkeeping, audit rows. `[built]`
- **DuckDB** (ADR 0017) — the **OLAP** store: append-only operational history,
  columnar-vectorized scans/aggregations. Holds reconcile lifecycle events,
  apply telemetry, audit analytics, derived status samples. `[planned #187]`

They never overlap: SQLite holds current state, DuckDB holds history. Neither
holds declared infrastructure (that is TF state). DuckDB does not make
RezusCloud a tenant-observability platform (ADR 0010) — it introspects the
management plane's own behaviour.

### Events

**NATS, embedded in-process** in the single-replica management plane (ADR 0009).
The single event/streaming primitive: resource-change events (WebUI SSE) and
async-controller events both flow through it. The REST watch/SSE HTTP surface is
unchanged — it subscribes to NATS under the hood. **[built]** — `internal/watch/nats.go`
runs an embedded NATS server; the `watch.Bus` interface abstracts the transport
(`NATSBus` for production, `LocalBus` for tests).

**Generic `?watch=true` [built #172].** The K8s-style watch surface is exposed
on every resource list endpoint (`GET /tenants?watch=true`, `GET
/tenants/{t}/node-groups?watch=true`, `GET /machines?watch=true`, `GET
/tenants/{t}/machines?watch=true`, `GET /tenants/{t}/configpatches?watch=true`).
The handler subscribes to the bus, optionally sends an initial ADDED snapshot,
then streams live ADDED/MODIFIED/DELETED frames as SSE (default) or NDJSON until
the client disconnects. Tenant-scoped watches filter to events labelled with that
tenant. The WebUI can subscribe to live reconciliation/machine/nodegroup changes
instead of polling.

### Scheduling

**Debounced per-tenant apply queue + optimistic concurrency [built].** The
API layer uses `resourceVersion` (optimistic concurrency) so users can PUT
resources concurrently without lost updates. The apply layer runs one debounced
queue per tenant — rapid edits coalesce into a single `tofu apply` that
reconciles the whole tenant. Applies serialize within a tenant, run in parallel
across tenants. A slow periodic resync (e.g., 5 min) re-enqueues every tenant to
catch external drift.

### Teardown (deletion)

**Finalizer-driven deletion + `tofu destroy` [built #171].** `DELETE
/tenants/{name}` is asynchronous: the store stamps a `deletionTimestamp` and two
finalizers (`rezuscloud.io/machines`, `rezuscloud.io/secrets`) and returns 202
Accepted. The reconcile `Applier.Apply` detects the `deletionTimestamp`, runs
`tofu init` + `tofu destroy -auto-approve` (same workdir + state as apply), then
on success cascade-removes every child resource (`RemoveResourcesByTenant`),
drops cached secrets, and clears the finalizers — the last one triggers the
store's auto-GC of the tenant row. On failure the finalizers are left intact so
the next resync re-attempts. The `StatusTracker` translates the queue's
`PhaseApplying` → `PhaseDestroying` when the tenant is deleting, so the
reconciliation banner reads "destroying".

### Upgrades

**Upgrade first, apply after** — the only safe ordering because config
generation is version-aware (the `talos` provider's `talos_machine_configuration`
generates version-specific config). On a `talosVersion`/`kubernetesVersion` bump:
the reconcile `Applier` runs the rolling upgrade engine (`internal/upgrade`,
real Talos API adapter in `internal/upgrade/talos`) **first** via a pre-apply
hook, *then* `tofu apply` syncs declared state. `ignore_changes = [user_data]` on
instances guarantees the apply never recreates VMs. The upgrade engine is
declarative — the spec is the input (the user already set the new version); the
upgrade converges machines to match it. No write-back of the version after
upgrade (that would re-trigger the apply queue).

### Secrets & encryption

**OpenTofu owns all crypto.** State is encrypted by `tofu` via `pbkdf2`+
`aes_gcm` config embedded in the generated `.tf.json` — RezusCloud's HTTP
backend stores opaque blobs and never implements crypto itself. The reconciler
runs `tofu state pull` (with `TF_ENCRYPTION` set) after each apply to extract
`client_configuration`, which it caches in memory **[built]**. v1 uses a
single root passphrase; the design evolves to per-tenant passphrases under a
root key without an architecture change.

### Bootstrap credentials

RezusCloud holds the minimal secret set to exec `tofu` (Bitwarden token, OCI
keyfile). Individual cloud passwords are fetched by tofu's `bitwarden` provider
at apply time — RezusCloud never sees them. RezusCloud is self-contained: the
only component needed to run the personal cloud. v1: single bootstrap set;
evolution: per-tenant bootstrap sets.

### Management connectivity & discovery

**rezuscloud is the management node** (ADR 0018). Every node maintains a
continuous, node-initiated **SideroLink** tunnel to it (outbound → NAT/CGNAT
traversal via STUN + relay). The tunnel carries **pull in both directions**: the
node pulls its config (ADR 0008, reversed to pull), and the platform pulls
telemetry on demand (ADR 0010/0016). Nothing is continuously pushed. **[planned
#189]** — not yet built; today reachability is direct (cloud endpoint IP /
bare-metal IPv6 LAN, archived ADR 0020 v1) and config delivery is push. The
cold-boot bootstrap (minimal `siderolink.api=` kernel arg + WireGuard key) is
still pushed once via user_data/boot image; everything else flows over the
tunnel.

Tenant nodes find **each other** across networks via the embedded cluster
discovery service (ADR 0019): each tenant gets a cluster ID + token, nodes
register their SideroLink tunnel address and query for peers. **[planned #189]**
— not yet built.

## CLI Design

rezusctl follows the kubectl verb-driven model: `rezusctl <verb> <type> [<name>]`

- `--cluster`/`-c` scopes operations to a tenant cluster (like kubectl's `--namespace`/`-n`)
- Resource type registry maps user-facing names to API paths (`cluster` → `/api/v1/tenants`, `machine` → `/api/v1/machines`)
- Generic verbs: `get`, `delete`, `create`, `apply`, `describe`, `label`
- Specialized commands: `kubeconfig`, `talosconfig`, `logs`, `boot`
- `jointoken` subcommand is **deprecated** (see JoinToken glossary entry) and slated for removal
- `boot` is the only standalone command (no API server needed)

## Conventions

- No references to commercial products or their vendors in code, docs, or ADRs.
- Go import paths (`github.com/siderolabs/...`) are technical dependencies, not product references.
