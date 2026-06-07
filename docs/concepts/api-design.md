# RezusCloud API Design

REST API for the RezusCloud management plane. Follows the Kubernetes API model:
metadata/spec/status on every resource, label-based selection, finalizer-controlled
deletion, optimistic concurrency, sub-resource endpoints for non-CRUD operations.

## Resource Types

| Type | Scope | Purpose |
|------|-------|---------|
| Tenant | Cluster-wide | A tenant cluster (desired K8s + Talos version, plugins, node groups) |
| NodeGroup | Tenant-scoped | A set of machines with the same role and provider |
| Machine | Cluster-wide | A physical/virtual machine that has phoned home |
| Provider | Cluster-wide | A registered infrastructure provider |
| JoinToken | Tenant-scoped | Maps a booting machine to a node group |
| ConfigPatch | Tenant-scoped | User-defined Talos config overlay |
| User | Cluster-wide | Auth identity with role (view/edit/admin) |
| APIToken | User-scoped | Long-lived token for automation |

## Resource Shapes

Every resource follows the three-section pattern:

```json
{
  "metadata": {
    "name": "string",
    "uid": "string (UUID, server-generated)",
    "resourceVersion": "int64 (monotonic, SQLite rowid)",
    "createdAt": "timestamp",
    "updatedAt": "timestamp",
    "deletionTimestamp": "timestamp | null",
    "finalizers": ["string"],
    "labels": {"key": "value"},
    "annotations": {"key": "value"}
  },
  "spec": { "...user-declared desired state..." },
  "status": { "...system-observed actual state (read-only)..." }
}
```

### Tenant

```json
{
  "metadata": {
    "name": "production",
    "labels": {
      "rezuscloud.io/environment": "prod"
    },
    "annotations": {
      "rezuscloud.io/description": "Production cluster"
    }
  },
  "spec": {
    "kubernetesVersion": "1.35.0",
    "talosVersion": "1.12.6",
    "controlPlaneEndpoint": "https://prod.k8s.example.com:6443",
    "podNetwork": ["10.244.0.0/16"],
    "serviceNetwork": ["10.96.0.0/12"],
    "plugins": {
      "cni": {
        "type": "cilium",
        "version": "1.17.0",
        "values": "..."
      },
      "csi": {
        "type": "none"
      }
    },
    "configPatches": [
      {
        "name": "cilium-helm-values",
        "patch": "...yaml patch..."
      }
    ]
  },
  "status": {
    "phase": "active",
    "available": true,
    "ready": true,
    "apiReady": true,
    "controlPlaneReady": true,
    "machines": {
      "total": 5,
      "healthy": 5,
      "connected": 5
    },
    "kubernetesVersion": "1.35.0",
    "talosVersion": "1.12.6",
    "bootstrapStatus": {
      "bootstrapped": true,
      "initMachine": "uuid-abc-123"
    }
  }
}
```

Tenant phases (derived from machine states):

```
forming     → machines being added (new tenant or scaling up)
shrinking   → machines being removed (scaling down)
active      → all machines healthy
removing    → teardown in progress (deletionTimestamp set)
```

Transition rules:
- `forming → active`: all expected machines report `ready` stage
- `active → forming`: node group count increases, or machine stage drops below ready
- `active → shrinking`: node group count decreases
- `shrinking → active`: excess machines removed, remaining healthy
- `active → removing`: DELETE request received, deletionTimestamp set
- `forming → removing`: DELETE during initial provisioning
- `removing → ∅`: all finalizers cleared, record deleted

Booleans (`available`, `ready`, `apiReady`, `controlPlaneReady`) provide granular
health independent of phase. A tenant can be `forming` with `available: true` if
the control plane is up but workers are still joining.

### NodeGroup

A NodeGroup is a set of machines within a tenant sharing the same role and provider.
Role is expressed as a well-known label.

```json
{
  "metadata": {
    "name": "production-control-plane",
    "labels": {
      "rezuscloud.io/tenant": "production",
      "rezuscloud.io/role": "controlplane"
    }
  },
  "spec": {
    "count": 3,
    "providerClass": "hetzner",
    "providerConfig": {
      "machineType": "cx22",
      "region": "fsn1"
    },
    "talosVersion": "1.12.6",
    "configPatches": [
      {"name": "cp-extra", "patch": "..."}
    ]
  },
  "status": {
    "phase": "active",
    "ready": true,
    "machines": {
      "total": 3,
      "healthy": 3,
      "connected": 3
    }
  }
}
```

Well-known role labels:

| Label | Meaning |
|-------|---------|
| `rezuscloud.io/role: controlplane` | Control plane machines → Talos config type `controlplane` (or `init` for first) |
| `rezuscloud.io/role: worker` | Worker machines → Talos config type `worker` |

Other labels may be added by users or providers for selection.

NodeGroup phases:

```
forming   → provisioning or joining machines
active    → all machines ready
shrinking → removing excess machines
removing  → teardown in progress
```

`providerConfig` is opaque JSON — each provider interprets it according to its own schema:

**Cloud provider (e.g., Hetzner):**
```json
{"machineType": "cx22", "region": "fsn1"}
```

**Metal provider (PXE/IPMI):**
```json
{"pxe": true, "ipmi": {"address": "192.168.1.50", "userRef": "secret/ipmi-creds"}}
```

**Static provider (already booted):**
```json
{"alreadyBooted": true}
```

### Machine

A machine is created when it phones home via MachineLink and reports its hardware UUID.
The ID is the hardware UUID, not provider-assigned.

```json
{
  "metadata": {
    "name": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "labels": {
      "rezuscloud.io/tenant": "production",
      "rezuscloud.io/node-group": "production-control-plane",
      "rezuscloud.io/provider": "hetzner"
    }
  },
  "spec": {
    "managementAddress": "10.0.0.5",
    "connected": true
  },
  "status": {
    "stage": "ready",
    "ready": true,
    "role": "controlplane",
    "talosVersion": "1.12.6",
    "kubernetesVersion": "1.35.0",
    "configUpToDate": true,
    "maintenance": false,
    "hardware": {
      "processors": [{"coreCount": 4, "description": "AMD EPYC"}],
      "memoryModules": [{"sizeMb": 8192}],
      "blockDevices": [{"size": 107374182400, "type": "ssd", "systemDisk": true}]
    },
    "network": {
      "hostname": "node-1",
      "addresses": ["10.0.0.5", "fd00::5"],
      "defaultGateways": ["10.0.0.1"]
    },
    "schematic": {
      "id": "sha256-abc...",
      "extensions": ["example/custom-extension"]
    },
    "lastError": ""
  }
}
```

Machine stages:

```
initializing  → Talos booting, no config applied yet
installing    → Talos writing to disk
configuring   → config applied, services starting
ready         → all services healthy, node in cluster
restarting    → reboot in progress
stopping      → graceful shutdown
off           → powered off
updating      → Talos or K8s upgrade in progress
removing      → being destroyed/cleaned
```

Unassigned machines (no `rezuscloud.io/tenant` label) sit in a pending pool
waiting for manual assignment or join token matching.

### Provider

```json
{
  "metadata": {
    "name": "hetzner"
  },
  "spec": {
    "endpoint": "grpc.provider-hetzner.rezuscloud.local:50190"
  },
  "status": {
    "connected": true,
    "lastHeartbeat": "2026-05-28T10:00:00Z",
    "schema": {
      "machineTypes": ["cx22", "cx32", "cx42"],
      "regions": ["fsn1", "nbg1", "hel1"]
    },
    "error": ""
  }
}
```

### JoinToken

```json
{
  "metadata": {
    "name": "jt-a8f3b2c1",
    "labels": {
      "rezuscloud.io/tenant": "production",
      "rezuscloud.io/node-group": "production-control-plane"
    }
  },
  "spec": {
    "expiresAt": "2026-06-04T10:00:00Z",
    "singleUse": true
  },
  "status": {
    "used": false,
    "usedBy": "",
    "usedAt": null
  }
}
```

Flow:
1. Provider requests a join token for a node group: `POST /api/v1/tenants/{name}/join-tokens`
2. Token value is returned once (not stored in plaintext — only a hash)
3. Provider injects token into machine kernel args (via MachineLink schematic or PXE)
4. Machine phones home, presents token
5. Management plane validates token, maps machine to the labeled node group
6. Role is determined from the node group's `rezuscloud.io/role` label
7. Talos config is generated using `input.Config(machine.Type)`:
   - `controlplane` label → first machine gets `TypeInit`, rest get `TypeControlPlane`
   - `worker` label → `TypeWorker`
8. Config delivered over MachineLink
9. Token consumed (if single-use)

### ConfigPatch

```json
{
  "metadata": {
    "name": "cilium-values",
    "labels": {
      "rezuscloud.io/tenant": "production"
    }
  },
  "spec": {
    "patch": "...yaml string..."
  }
}
```

### User

```json
{
  "metadata": {
    "name": "admin"
  },
  "spec": {
    "role": "admin",
    "passwordHash": "bcrypt..."
  },
  "status": {
    "lastLogin": "2026-05-28T09:00:00Z",
    "activeTokens": 2
  }
}
```

Roles (following K8s default ClusterRole naming):

| Role | Permissions |
|------|------------|
| `view` | Read tenants, machines, status. No secrets (no kubeconfig/talosconfig). |
| `edit` | Create/delete tenants, provision machines, get kubeconfig/talosconfig. No user management. |
| `admin` | Everything — manage users, configure providers, system config. |

### APIToken

```json
{
  "metadata": {
    "name": "ci-pipeline",
    "labels": {
      "rezuscloud.io/user": "admin"
    }
  },
  "spec": {
    "expiresAt": "2027-01-01T00:00:00Z"
  },
  "status": {
    "lastUsed": "2026-05-28T08:00:00Z"
  }
}
```

## API Endpoints

### Auth

```
POST   /api/v1/auth/login                    # {username, password} → JWT
POST   /api/v1/auth/logout                   # revoke session
GET    /api/v1/auth/whoami                    # current user + role
```

### Users (admin only)

```
GET    /api/v1/users
POST   /api/v1/users
GET    /api/v1/users/{name}
PUT    /api/v1/users/{name}                   # update role/password
DELETE /api/v1/users/{name}

POST   /api/v1/users/{name}/tokens            # create API token → returns JWT
DELETE /api/v1/users/{name}/tokens/{id}       # revoke API token
```

### Tenants

```
GET    /api/v1/tenants                         # list (offset/limit, label selectors)
POST   /api/v1/tenants                         # create
GET    /api/v1/tenants/{name}                  # get
PUT    /api/v1/tenants/{name}                  # update spec (status ignored)
DELETE /api/v1/tenants/{name}                  # set deletionTimestamp + finalizers
PUT    /api/v1/tenants/{name}/status           # controllers update status (spec ignored)
GET    /api/v1/tenants/{name}/events           # watch (chunked ?watch=true, SSE ?watch=true&sse=true)

GET    /api/v1/tenants/{name}/kubeconfig       # generate admin kubeconfig
GET    /api/v1/tenants/{name}/talosconfig      # generate talosconfig
GET    /api/v1/tenants/{name}/join-config      # kernel args + schematic for machines
```

### NodeGroups

```
GET    /api/v1/tenants/{name}/node-groups
POST   /api/v1/tenants/{name}/node-groups
GET    /api/v1/tenants/{name}/node-groups/{ng}
PUT    /api/v1/tenants/{name}/node-groups/{ng}
DELETE /api/v1/tenants/{name}/node-groups/{ng}
PUT    /api/v1/tenants/{name}/node-groups/{ng}/status
GET    /api/v1/tenants/{name}/node-groups/{ng}/events
```

### Machines

Tenant-scoped:
```
GET    /api/v1/tenants/{name}/machines
GET    /api/v1/tenants/{name}/machines/{id}
PUT    /api/v1/tenants/{name}/machines/{id}/status
DELETE /api/v1/tenants/{name}/machines/{id}     # teardown with finalizers
GET    /api/v1/tenants/{name}/machines/{id}/config    # generated Talos config
GET    /api/v1/tenants/{name}/machines/{id}/logs      # streaming Talos logs
POST   /api/v1/tenants/{name}/machines/{id}/restart   # restart machine
```

Cluster-wide (includes unassigned):
```
GET    /api/v1/machines                        # all machines
GET    /api/v1/machines/{id}                   # single machine
GET    /api/v1/machines/events                 # watch all machines
```

### JoinTokens

```
GET    /api/v1/tenants/{name}/join-tokens
POST   /api/v1/tenants/{name}/join-tokens      # create token for a node group
DELETE /api/v1/tenants/{name}/join-tokens/{id}
```

### ConfigPatches

```
GET    /api/v1/tenants/{name}/patches
POST   /api/v1/tenants/{name}/patches
GET    /api/v1/tenants/{name}/patches/{name}
PUT    /api/v1/tenants/{name}/patches/{name}
DELETE /api/v1/tenants/{name}/patches/{name}
```

### Providers

```
GET    /api/v1/providers
GET    /api/v1/providers/{type}
PUT    /api/v1/providers/{type}/status          # provider heartbeat updates status
GET    /api/v1/providers/{type}/events
```

### System

```
GET    /api/v1/status                           # management plane health
GET    /healthz
GET    /readyz
GET    /version
```

## Common Patterns

### Pagination

List endpoints support `offset` and `limit` query parameters:

```
GET /api/v1/tenants?offset=0&limit=50

Response:
{
  "items": [...],
  "total": 3
}
```

### Label Selectors

List endpoints support filtering by labels:

```
GET /api/v1/tenants/{name}/machines?labelSelector=rezuscloud.io/role=controlplane
```

### Watch / Events

Two wire formats on the same endpoint:

```
GET /api/v1/tenants/{name}/events?watch=true             → chunked JSON
GET /api/v1/tenants/{name}/events?watch=true&sse=true    → Server-Sent Events
```

Event format (both):
```json
{"type": "ADDED", "object": {...}}
{"type": "MODIFIED", "object": {...}}
{"type": "DELETED", "object": {...}}
```

### Optimistic Concurrency

Every resource has `metadata.resourceVersion`. On PUT, client must send the current
version. Server rejects with 409 if it changed:

```json
{
  "status": "failure",
  "message": "the object has been modified; please apply your changes to the latest version",
  "reason": "Conflict",
  "code": 409
}
```

### Finalizer-Controlled Deletion

DELETE sets `metadata.deletionTimestamp` and relies on finalizers. The object is not
removed until all finalizers are cleared. Controllers watch for deletionTimestamp,
perform cleanup, and remove their finalizer:

```
DELETE /api/v1/tenants/prod
→ 200 OK: {metadata: {deletionTimestamp: "2026-05-28T10:00:00Z", finalizers: ["rezuscloud.io/machines", "rezuscloud.io/secrets"]}}

Controller deprovisions machines → removes "rezuscloud.io/machines"
Controller cleans up secrets → removes "rezuscloud.io/secrets"

All finalizers cleared → record deleted from SQLite
```

### Error Responses

All errors follow a structured shape:

```json
{
  "status": "failure",
  "message": "tenant \"prod\" not found",
  "reason": "NotFound",
  "code": 404
}
```

Standard reasons: `NotFound`, `Conflict`, `BadRequest`, `Forbidden`,
`Unauthorized`, `InternalError`, `MethodNotAllowed`.

## Tenant Lifecycle End-to-End

```
1. User creates tenant
   POST /api/v1/tenants {metadata: {name: "prod"}, spec: {...}}
   → status: {phase: "forming", available: false}

2. User creates node groups
   POST /api/v1/tenants/prod/node-groups {metadata: {labels: {rezuscloud.io/role: "controlplane"}}, spec: {count: 3, ...}}
   POST /api/v1/tenants/prod/node-groups {metadata: {labels: {rezuscloud.io/role: "worker"}}, spec: {count: 5, ...}}

3. User or provider requests join tokens
   POST /api/v1/tenants/prod/join-tokens {nodeGroup: "prod-cp"}
   → returns token value (stored as hash)

4. Provider provisions hardware, injects join token into kernel args

5. Machine boots Talos, phones home via MachineLink
   → Machine resource created with hardware UUID as ID
   → Join token validated → machine assigned to node group
   → Role label from node group determines Talos config type
   → First controlplane machine gets TypeInit config
   → Subsequent controlplane machines get TypeControlPlane config
   → Worker machines get TypeWorker config
   → Config delivered over MachineLink

6. Machine transitions through stages
   initializing → installing → configuring → ready

7. Tenant phase derived from machine states
   forming → active (all machines ready)

8. User retrieves kubeconfig
   GET /api/v1/tenants/prod/kubeconfig → admin kubeconfig derived from encrypted secrets bundle

9. User deletes tenant
   DELETE /api/v1/tenants/prod
   → deletionTimestamp set, finalizers block removal
   → Machines controller deprovisions each machine, removes finalizer
   → Secrets controller deletes secrets bundle, removes finalizer
   → All finalizers cleared → record removed
```

## State Backend

### MVP: SQLite (single writer)

SQLite is the sole state backend for the initial release. It handles tens of
thousands of operations per second — more than sufficient for a personal cloud
with tens of machines across a handful of tenants.

- **Standalone**: binary + SQLite file on disk
- **Cluster (Helm)**: same binary in a K8s Deployment, SQLite on a PVC, single replica
- Temporary management plane unavailability does not affect running tenant clusters

### Future: Pluggable Backend

The Store interface is designed for future backend swaps:

```go
type Store interface {
    // Tenant CRUD
    CreateTenant(ctx context.Context, t *Tenant) error
    GetTenant(ctx context.Context, name string) (*Tenant, error)
    ListTenants(ctx context.Context, opts ListOptions) ([]*Tenant, int, error)
    UpdateTenant(ctx context.Context, t *Tenant) error
    DeleteTenant(ctx context.Context, name string) error
    // ... same pattern for all resource types
}
```

Planned backends (in priority order):

| Backend | When | Why |
|---------|------|-----|
| **SQLite** | MVP | Zero dependencies, single-writer, fast enough |
| **PostgreSQL** | Growth | Multi-writer, streaming replication, no K8s dependency |
| **etcd** | Scale | Native K8s distribution, multi-node HA |

The backend is selected at startup via `REZUSCLOUD_STORE` env var. Default: `sqlite`.
No backend switch at runtime — pick one at deploy time.

## Secrets Management

Tenant secrets (CA certs, etcd certs, admin keys) are stored as an encrypted blob
in the state backend. The management plane holds an encryption key in memory (generated on
first boot, persisted to a separate file).

```go
type SecretStore interface {
    GetBundle(ctx context.Context, tenant string) (*secrets.Bundle, error)
    SetBundle(ctx context.Context, tenant string, bundle *secrets.Bundle) error
    DeleteBundle(ctx context.Context, tenant string) error
}
```

Default implementation: encrypted column in the state backend. Future extension:
Bitwarden, Vault, AWS Secrets Manager via plugin.

The raw secrets bundle is never exposed via the API. Only derived artifacts
(kubeconfig, talosconfig) are returned through sub-resource endpoints.
