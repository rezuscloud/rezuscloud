# CONTEXT.md

## Glossary

| Term | Definition |
|------|-----------|
| **RezusCloud** | Personal cloud platform. Single binary runs the management plane. |
| **rezuscloud** | Server binary — HTTP API, WebUI, SideroLink server, provider gRPC. |
| **rezusctl** | CLI binary — boot, tenant, join. Static binary, no container image. |
| **Management Plane** | The running `rezuscloud` process. Owns cluster lifecycle, config delivery, state. |
| **Tenant** | A full Talos cluster under management. Has its own etcd, API server, kubelets. User-facing term: **cluster** (CLI uses `cluster`, API uses `tenant`). |
| **NodeGroup** | A set of machines within a tenant sharing the same role (controlplane/worker) and provider. |
| **Machine** | A physical or virtual machine running Talos. Identified by hardware UUID. |
| **Provider** | A standalone binary that connects outbound to the management plane and provisions machines. |
| **SideroLink** | Talos kernel feature for config pull. Machines boot with `siderolink.api=` kernel args, establish WireGuard-over-gRPC tunnel, receive config. |
| **Join Token** | Per-provisioning token that maps a booting machine to a tenant and node group. |
| **ConfigPatch** | User-defined Talos config overlay applied during config generation. |
| **Stage** | A machine's observed state: initializing, installing, configuring, ready, restarting, stopping, off, updating, removing. |
| **Phase** | A tenant's derived state: forming, shrinking, active, removing. Computed from machine stages. |

## Architecture

One repo, two binaries (Kubernetes kubectl model):

```
rezuscloud binary (server)          rezusctl binary (CLI)
├── HTTP API (REST, K8s-style)      ├── boot (Docker/QEMU platforms)
├── WebUI (templ + HTMX)            ├── tenant create/list/delete
├── SideroLink server               ├── join (apply config to workers)
├── Provider gRPC server            └── kubeconfig extraction
├── SQLite state store
├── Config generation (Talos)
└── Rolling upgrades
```

State: SQLite (standalone mode). REST API with JWT auth. K8s-style resource model (metadata/spec/status, labels, finalizers).

## CLI Design

rezusctl follows the kubectl verb-driven model: `rezusctl <verb> <type> [<name>]`

- `--cluster`/`-c` scopes operations to a tenant cluster (like kubectl's `--namespace`/`-n`)
- Resource type registry maps user-facing names to API paths (`cluster` → `/api/v1/tenants`, `machine` → `/api/v1/machines`)
- Generic verbs: `get`, `delete`, `create`, `apply`, `describe`, `label`
- Specialized commands: `kubeconfig`, `talosconfig`, `logs`, `jointoken`, `boot`
- `boot` is the only standalone command (no API server needed)

## Conventions

- SideroLink is a Talos kernel feature — use the term directly, like "Talos" or "WireGuard."
- No references to commercial products or their vendors in code, docs, or ADRs.
- Go import paths (`github.com/siderolabs/...`) are technical dependencies, not product references.
