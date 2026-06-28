# CONTEXT.md

## Glossary

| Term | Definition |
|------|-----------|
| **RezusCloud** | Personal cloud platform. Single binary runs the management plane. |
| **rezuscloud** | Server binary — HTTP API, WebUI, TF execution engine, TF HTTP backend, controllers. |
| **rezusctl** | CLI binary — boot, tenant, join. Static binary, no container image. |
| **Management Plane** | The running `rezuscloud` process. Owns cluster lifecycle, config delivery, state. |
| **Tenant** | A full Talos cluster under management. Has its own etcd, API server, kubelets. User-facing term: **cluster** (CLI uses `cluster`, API uses `tenant`). |
| **NodeGroup** | A set of machines within a tenant sharing the same role (controlplane/worker) and provider. |
| **Machine** | A physical or virtual machine running Talos. Identified by hardware UUID. |
| **Provider** | A RezusCloud-side orchestration module (`internal/provider/<name>/`) that is the **complete vertical slice** for a platform. Four responsibilities:  
&nbsp;&nbsp;1. **Render a UI panel** (templ) + status indicator.  
&nbsp;&nbsp;2. **Generate standard `.tf.json`** using off-the-shelf TF providers (oci, openstack, talos) — **no custom `terraform-provider-rezuscloud-*` plugins exist.**  
&nbsp;&nbsp;3. **Optionally discover** nodes (metal only; cloud has none).  
&nbsp;&nbsp;4. **Fill TF gaps with RezusCloud-side Go logic** — operations the standard providers can't express, e.g. `talosctl upgrade` (rolling, health-gated in-place upgrade; the `talos` provider has no upgrade resource). These run *alongside* reconciliation, driven by the same spec change.  
Replaces the earlier gRPC provider model (ADR 12). Two shapes:  
&nbsp;&nbsp;• **Cloud providers** (oci, openstack, …) — no discovery; resources generated *from* the provider's TF config; UI shows a **status indicator** = cloud-access health, checked by `ProviderHealthReconciler`.  
&nbsp;&nbsp;• **Metal provider** — optional discovery (subnet scan for Talos maintenance nodes on port 50000) + manual entry (always available); resources are physical machines targeted by `talos_machine_configuration_apply`. |
| **Discovery** | An **optional, per-provider UI capability** built into RezusCloud. When an operator selects a provider in the UI, that provider's panel renders (and may include a "scan now" discovery action). Discovery finds nodes (e.g. metal scans a subnet for Talos maintenance-mode nodes on port 50000); the operator confirms to add them. **Manual entry is always available** — discovery is a convenience, never mandatory. Discovered or manually-entered nodes become Machine resources via the API. Discovery results are a UI session, not persisted state; once added, an unassigned Machine sits in the pending pool (no tenant label) per api-design.md. Discovery is RezusCloud-side Go logic on top of TF provisioning, not a TF resource. |
| **Config Delivery** | How a Talos node receives its config. Two methods: **user_data** (cloud VMs — OCI metadata, OpenStack config_drive, cloud-init) at VM creation time, and **Talos API push** (`talosctl apply-config` / `talos_machine_configuration_apply`) for pre-booted bare metal in maintenance mode. No SideroLink. |
| **Bootstrap Credentials** | The minimal secret set RezusCloud holds to drive `tofu apply` — Bitwarden token, OCI keyfile/tenancy, etc. RezusCloud does **not** hold individual cloud passwords; tofu's provider chain (e.g. the `bitwarden` provider) fetches them at apply time, exactly as `talos-iac` does today. RezusCloud must be self-contained — the only component needed to run the personal cloud (e.g. a Home Assistant container app). **Reverses ADR 12's credential isolation** (providers no longer hold creds locally). v1: single bootstrap set; evolution: per-tenant bootstrap sets. |
| **ConfigPatch** | User-defined Talos config overlay applied during config generation. |
| **TF State** | The single source of truth for **desired/declared infrastructure** (resource `metadata` + `spec`). One state per tenant, stored in RezusCloud's TF HTTP backend. K8s-style REST APIs project `spec` from TF state through provider-declared resource mappings. Does **not** hold observed/runtime state — see Observed State. |
| **Observed State** | Runtime data scraped live from each tenant cluster (node health, pod status, etcd quorum, disk/CPU/mem). Fills in resource `status` fields. Never persisted as truth — it is ephemeral and may lag. Lives in the SQLite timeseries table and an in-memory ring buffer. |
| **Reconciliation** | The async loop: spec change → controller detects drift → `tofu apply` → TF state updated → status updated. Events emitted at each phase. |
| **Apply Queue** | Debounced, per-tenant queue. All spec writes for a tenant coalesce within a debounce window (e.g., 5s); when it drains, a single `tofu apply` reconciles the whole tenant. Serial within a tenant, parallel across tenants. A slow periodic resync (e.g., 5 min) re-enqueues every tenant to catch external drift. |
| **State Encryption** | OpenTofu's native state encryption (`pbkdf2` + `aes_gcm`). Applied by `tofu` in the generated `.tf.json`, NOT by RezusCloud's HTTP backend. The backend stores opaque encrypted blobs. RezusCloud never reimplements crypto — every decrypt goes through `tofu state pull` with `TF_ENCRYPTION` set. |
| **Encryption Passphrase** | The `pbkdf2` passphrase that unlocks a tenant's state. v1: single root passphrase (env/mount) shared by all tenants. Evolution: root key encrypts a per-tenant passphrase table, enabling per-tenant rotation/recovery. |
| **Secrets Cache** | In-memory cache of `client_configuration` (talosconfig/kubeconfig) extracted via `tofu state pull` after each apply. The Collector reads plaintext creds from this cache — it never decrypts directly and never spawns `tofu`. Invalidated on next successful apply. `client_configuration` only changes on bootstrap/re-seal, so effectively static between applies. |
| **Management Connectivity** | How RezusCloud reaches managed nodes for management operations (Collector scraping, bare-metal config push, upgrades, health). **Distinct from config delivery.** v1: IPv6 direct (each node routable). v2: WireGuard hub-and-spoke (Kubespan-inspired) with STUN + relay for nodes behind NAT. Mesh is reachability-only; config still arrives via user_data / Talos API push. See [ADR 20](docs/adr/0020-management-connectivity.md). |

## Architecture

One repo, two binaries (Kubernetes kubectl model):

```
rezuscloud binary (server)                    rezusctl binary (CLI)
├── HTTP API (REST, K8s-style)                ├── boot (Docker/QEMU platforms)
├── WebUI (templ + HTMX)                      ├── tenant create/list/delete
├── TF HTTP backend (state store)              └── kubeconfig extraction
├── TF execution engine (exec tofu binary)
├── Controllers (async reconciliation)
│   ├── TenantReconciler
│   ├── NodeGroupReconciler
│   ├── UpgradeReconciler
│   └── ProviderHealthReconciler
├── Collector (scrapes tenant clusters live)
│   ├── Kubernetes API (node/pod health)
│   ├── Talos API (machine stage, version)
│   └── Node metrics (CPU/mem/disk/net)
├── Metrics store (SQLite timeseries + in-memory ring buffer)
├── Event bus (NATS embedded, in-process first)
├── Providers (UI panels + TF config generation + optional discovery)
├── Config generation (Talos)
└── Rolling upgrades
```

State: **Two data planes, never mixed.**
- **Spec plane (desired):** TF state is the single source of truth for declared infrastructure. One TF state per tenant, stored via RezusCloud's TF HTTP backend. K8s-style REST API `spec` fields are projections of TF state, mapped through provider-declared resource schemas.
- **Status plane (observed):** Runtime data is scraped live by the Collector from each tenant's Kubernetes + Talos APIs. It fills in resource `status` fields and feeds the metrics timeseries. Observed state is never written back to TF state and is never treated as source of truth — it is ephemeral, may lag, and may be stale across restarts.

The `metadata` + `spec` of a resource come from TF state; `status` comes from the Collector. The two never mix.

Controllers: Asynchronous reconciliation loop (Kubernetes controller pattern). REST API writes spec, controller detects drift, exec's `tofu plan && tofu apply`, reads resulting state, updates status. Events published via embedded NATS.

Scheduling: **Debounced per-tenant apply queue + optimistic concurrency.** The API layer uses `resourceVersion` (optimistic concurrency) so users can PUT resources concurrently without lost updates. The apply layer runs one debounced queue per tenant — rapid edits coalesce into a single `tofu apply` that reconciles the whole tenant. Applies serialize within a tenant, run in parallel across tenants. A slow periodic resync (e.g., 5 min) re-enqueues every tenant to catch external drift (manual/cloud-side changes).

Upgrades: **upgrade first, apply after** — the only safe ordering because config generation is version-aware (the `talos` provider's `talos_machine_configuration` generates version-specific config). On a `talosVersion`/`kubernetesVersion` bump: the UpgradeReconciler runs `talosctl upgrade` machine-by-machine with health gates (existing `internal/upgrade/rolling.go`, K8s skew policy in `internal/upgrade/k8s/policy.go`), *then* the Reconciler runs `tofu apply` to sync declared state. `ignore_changes = [user_data]` on instances guarantees the apply never recreates VMs. The upgrade engine is model-agnostic — its only change under the TF-state model is reading creds from the Secrets Cache (Q20). This upgrade path is itself a provider-module "TF-gap" operation: the `talos` provider has no in-place-upgrade resource, so RezusCloud fills it with Go logic.

Secrets & encryption: **OpenTofu owns all crypto.** State is encrypted by `tofu` via `pbkdf2`+`aes_gcm` config embedded in the generated `.tf.json` — RezusCloud's HTTP backend stores opaque blobs and never implements crypto itself. The Reconciler runs `tofu state pull` (with `TF_ENCRYPTION` set) after each apply to extract `client_configuration`, which it caches in memory. The Collector reads plaintext creds from that cache. v1 uses a single root passphrase; the design evolves to per-tenant passphrases under a root key without an architecture change.

Bootstrap credentials: RezusCloud holds the minimal secret set to exec `tofu` (Bitwarden token, OCI keyfile). Individual cloud passwords are fetched by tofu's `bitwarden` provider at apply time — RezusCloud never sees them. RezusCloud is self-contained: the only component needed to run the personal cloud.

Management connectivity: **v1 = IPv6 direct** (matches `talos-iac` today — all nodes reachable via IPv6 or same LAN). **v2 = WireGuard hub-and-spoke** (Kubespan-inspired, with STUN + relay) for nodes behind NAT on remote IPv4-only networks. The mesh is reachability-only — config still arrives via user_data / Talos API push, never over the mesh. SideroLink remains rejected across both phases. See [ADR 20](docs/adr/0020-management-connectivity.md).

## CLI Design

rezusctl follows the kubectl verb-driven model: `rezusctl <verb> <type> [<name>]`

- `--cluster`/`-c` scopes operations to a tenant cluster (like kubectl's `--namespace`/`-n`)
- Resource type registry maps user-facing names to API paths (`cluster` → `/api/v1/tenants`, `machine` → `/api/v1/machines`)
- Generic verbs: `get`, `delete`, `create`, `apply`, `describe`, `label`
- Specialized commands: `kubeconfig`, `talosconfig`, `logs`, `jointoken`, `boot`
- `boot` is the only standalone command (no API server needed)

## Conventions

- No references to commercial products or their vendors in code, docs, or ADRs.
- Go import paths (`github.com/siderolabs/...`) are technical dependencies, not product references.
