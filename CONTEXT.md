# CONTEXT.md

> **This document describes the architecture as a status-honest map.** Every
> component is tagged: **[built]** = on `main` and wired into the server;
> **[scaffolded]** = code exists but not yet wired; **[planned #NN]** = not yet
> built, tracked by issue. The *model* (concepts + ADRs) is authoritative; the
> *status* tags track how much of it is real today. Update tags when code lands.

## Glossary

| Term | Definition |
|------|-----------|
| **RezusCloud** | Personal cloud platform. Single binary runs the management plane. |
| **rezuscloud** | Server binary — HTTP API, WebUI, TF HTTP backend, TF execution engine. |
| **rezusctl** | CLI binary — boot, tenant, join. Static binary, no container image. |
| **Management Plane** | The running `rezuscloud` process. Owns cluster lifecycle, config delivery, state. |
| **Tenant** | A full Talos cluster under management. Has its own etcd, API server, kubelets. User-facing term: **cluster** (CLI uses `cluster`, API uses `tenant`). |
| **NodeGroup** | A set of machines within a tenant sharing the same role (controlplane/worker) and provider. |
| **Machine** | A physical or virtual machine running Talos. Identified by hardware UUID. |
| **Provider** | The RezusCloud module that interacts with a real Terraform provider (oci, openstack, talos, …) to maintain the infrastructure RezusCloud manages. One Provider per platform, living in `internal/provider/<name>/`. It generates the standard `.tf.json` that drives `tofu` (ADR 22) and declares the mapping between TF resource types and RezusCloud resources. **There is no separate "RezusCloud provider" and "TF provider" — there is one TF-based Provider that wraps a real registry provider.** gRPC is used only if a given TF provider requires it; otherwise RezusCloud generates config and execs `tofu` directly. |
| **Config Delivery** | How a Talos node receives its config. Two methods: **user_data** (cloud VMs — OCI metadata, OpenStack config_drive, cloud-init) at VM creation time, and **Talos API push** (`talosctl apply-config` / `talos_machine_configuration_apply`) for pre-booted bare metal in maintenance mode. No SideroLink (ADR 13). |
| **ConfigPatch** | User-defined Talos config overlay applied during config generation. |
| **TF State** | The single source of truth for **declared infrastructure** (resource `metadata` + `spec`). One state per tenant, stored in RezusCloud's TF HTTP backend. K8s-style REST APIs project `spec` from TF state through provider-declared resource mappings. Does **not** hold observed/runtime state. |
| **Reconciliation** | The async loop: spec change → controller detects drift → `tofu apply` → TF state updated → status updated. Events emitted at each phase. |
| **Apply Queue** | Debounced, per-tenant queue. All spec writes for a tenant coalesce within a debounce window; when it drains, a single `tofu apply` reconciles the whole tenant. Serial within a tenant, parallel across tenants. A slow periodic resync re-enqueues every tenant to catch external drift. |
| **State Encryption** | OpenTofu's native state encryption (`pbkdf2` + `aes_gcm`). Applied by `tofu` in the generated `.tf.json`, NOT by RezusCloud's HTTP backend. The backend stores opaque encrypted blobs. RezusCloud never reimplements crypto — every decrypt goes through `tofu state pull` with `TF_ENCRYPTION` set. |
| **Management Connectivity** | How RezusCloud reaches managed nodes for management operations (health checks, bare-metal config push, upgrades). **Distinct from config delivery.** v1: IPv6 direct (each node routable). v2: WireGuard hub-and-spoke (Kubespan-inspired) with STUN + relay for nodes behind NAT. Mesh is reachability-only; config still arrives via user_data / Talos API push. See ADR 20. |

## Architecture

One repo, two binaries (Kubernetes kubectl model):

```
rezuscloud binary (server)                    rezusctl binary (CLI)
├── HTTP API (REST, K8s-style) [built]        ├── boot (Docker/QEMU platforms) [built]
├── WebUI (templ + HTMX) [built]              ├── tenant create/list/delete [built]
├── TF HTTP backend (state store) [built]     └── kubeconfig extraction [built]
├── TF execution engine (exec tofu) [scaffolded]
├── Apply Queue (debounced per-tenant) [scaffolded]
├── Controllers (async reconciliation) [planned #87b/#99]
│   ├── TenantReconciler          [planned]
│   ├── NodeGroupReconciler       [planned]
│   └── UpgradeReconciler         [planned]
├── Providers (TF config generation) [scaffolded]
├── State projection (TF state → API spec) [planned #91/#103]
├── Config generation (Talos) [built]
└── Rolling upgrades [built]
```

### Two data planes, never mixed (ADR 21)

- **Spec plane (declared):** TF state is the single source of truth for declared
  infrastructure. One TF state per tenant, stored via RezusCloud's TF HTTP
  backend. K8s-style REST API `spec` fields are projections of TF state, mapped
  through provider-declared resource schemas.
- **Status plane (observed):** Runtime data (node health, pod status, machine
  stage, version) is read live from each tenant's Kubernetes + Talos APIs. It
  fills in resource `status` fields. Observed state is **never written back to
  TF state** and is never treated as source of truth — it is ephemeral and may
  lag. *[planned #92]* — the live-scrape pipeline and metrics store are not yet
  built; today `status` is written by API handlers from ad-hoc observations.

The `metadata` + `spec` of an infrastructure resource come from TF state;
`status` comes from live observation. The two never mix.

### Scheduling

**Debounced per-tenant apply queue + optimistic concurrency [scaffolded].** The
API layer uses `resourceVersion` (optimistic concurrency) so users can PUT
resources concurrently without lost updates. The apply layer runs one debounced
queue per tenant — rapid edits coalesce into a single `tofu apply` that
reconciles the whole tenant. Applies serialize within a tenant, run in parallel
across tenants. A slow periodic resync (e.g., 5 min) re-enqueues every tenant to
catch external drift.

### Upgrades

**Upgrade first, apply after** — the only safe ordering because config
generation is version-aware (the `talos` provider's `talos_machine_configuration`
generates version-specific config). On a `talosVersion`/`kubernetesVersion` bump:
the UpgradeReconciler [planned] runs `talosctl upgrade` machine-by-machine with
health gates (existing `internal/upgrade/rolling.go` [built], K8s skew policy in
`internal/upgrade/k8s/policy.go` [built]), *then* the reconciler runs
`tofu apply` to sync declared state. `ignore_changes = [user_data]` on instances
guarantees the apply never recreates VMs. The upgrade engine is model-agnostic
— its only change under the TF-state model is reading creds from the secrets
cache [planned #92].

### Secrets & encryption

**OpenTofu owns all crypto.** State is encrypted by `tofu` via `pbkdf2`+
`aes_gcm` config embedded in the generated `.tf.json` — RezusCloud's HTTP
backend stores opaque blobs and never implements crypto itself. The reconciler
runs `tofu state pull` (with `TF_ENCRYPTION` set) after each apply to extract
`client_configuration`, which it caches in memory [planned #92]. v1 uses a
single root passphrase; the design evolves to per-tenant passphrases under a
root key without an architecture change.

### Bootstrap credentials

RezusCloud holds the minimal secret set to exec `tofu` (Bitwarden token, OCI
keyfile). Individual cloud passwords are fetched by tofu's `bitwarden` provider
at apply time — RezusCloud never sees them. RezusCloud is self-contained: the
only component needed to run the personal cloud. v1: single bootstrap set;
evolution: per-tenant bootstrap sets.

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
