# RezusCloud API Design

> **Type:** Explanation · **Audience:** API designer or integrator

REST API for the RezusCloud management plane. Follows the Kubernetes API model:
metadata/spec/status on every resource, label-based selection, finalizer-controlled
deletion, optimistic concurrency, sub-resource endpoints for non-CRUD operations.
See [ADR 0003](../adr/0003-rest-api-kubernetes-model.md).

## Resource Types

| Type | Scope | Purpose |
|------|-------|---------|
| Tenant | Cluster-wide | A tenant cluster (desired K8s + Talos version, plugins, node groups) |
| NodeGroup | Tenant-scoped | A set of machines with the same role and provider |
| Machine | Cluster-wide | A physical/virtual machine managed by a provider module |
| Provider | Cluster-wide | A RezusCloud-side TF provider module (oci, openstack, metal, …) |
| ConfigPatch | Tenant-scoped | User-defined Talos config overlay (tenant-wide scope, ADR 0014) |
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

The two never mix: `spec` comes from TF state (the source of truth for declared
infrastructure, [ADR 0005](../adr/0005-tf-state-single-source-of-truth.md));
`status` is best-effort observation ([ADR 0010](../adr/0010-status-plane-best-effort.md)).

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
    "talosVersion": "1.12.6"
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
    "providerClass": "oci",
    "providerConfig": {
      "shape": "VM.Standard.A1.Flex",
      "image": "talos-1.12.6"
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

`providerClass` names the RezusCloud provider module (`oci`, `openstack`,
`metal`, …). `providerConfig` is opaque JSON — each provider module interprets
it according to its own schema and uses it to generate the `.tf.json`:

**Cloud provider (e.g., OCI):**
```json
{"shape": "VM.Standard.A1.Flex", "image": "talos-1.12.6"}
```

**Metal provider (bare metal):**
```json
{"address": "192.168.1.50:50000"}
```

### Machine

A Machine represents a physical or virtual machine managed by a provider module.
Its identity comes from the TF state produced by `tofu apply`.

```json
{
  "metadata": {
    "name": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "labels": {
      "rezuscloud.io/tenant": "production",
      "rezuscloud.io/node-group": "production-control-plane",
      "rezuscloud.io/provider": "oci"
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

Machine `spec` and identity are projections of TF state; `status` (stage,
health, hardware, network) is observed best-effort and may lag.

### Provider

A Provider resource represents a RezusCloud-side TF provider module
([ADR 0007](../adr/0007-provider-as-tf-wrapper.md)). It is **not** a registered
live process — there is no gRPC server, no heartbeat, no `endpoint` field. The
resource carries the provider's declared schema (which TF resource types it
maps to which RezusCloud resources) so the projection layer
([ADR 0005](../adr/0005-tf-state-single-source-of-truth.md)) can translate TF
state into API resources.

```json
{
  "metadata": {
    "name": "oci"
  },
  "spec": {
    "type": "oci"
  },
  "status": {
    "mappings": [
      {"tfType": "oci_core_instance", "resource": "Machine"}
    ]
  }
}
```

### ConfigPatch

A ConfigPatch is a user-defined Talos config overlay applied during config
generation. It has a single tenant-wide scope, optionally filtered by role
([ADR 0014](../adr/0014-configpatch-single-scope.md)).

```json
{
  "metadata": {
    "name": "cilium-values",
    "labels": {
      "rezuscloud.io/tenant": "production"
    }
  },
  "spec": {
    "targetRole": "controlplane",
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
GET    /api/v1/tenants/{name}/machines/{id}/logs      # streaming Talos logs (read-only, ADR 0015)
POST   /api/v1/tenants/{name}/machines/{id}/restart   # restart machine
```

Cluster-wide (includes unassigned):
```
GET    /api/v1/machines                        # all machines
GET    /api/v1/machines/{id}                   # single machine
GET    /api/v1/machines/events                 # watch all machines
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
GET    /api/v1/providers                        # list provider modules + mappings
GET    /api/v1/providers/{type}                 # single provider module
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

Under the hood, events flow through the event bus (NATS, planned in
[ADR 0009](../adr/0009-event-bus-nats.md)); the HTTP watch surface is unchanged.

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

Controller runs tofu destroy, deprovisions machines → removes "rezuscloud.io/machines"
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
   POST /api/v1/tenants/prod/node-groups {metadata: {labels: {rezuscloud.io/role: "controlplane"}}, spec: {count: 3, providerClass: "oci", ...}}
   POST /api/v1/tenants/prod/node-groups {metadata: {labels: {rezuscloud.io/role: "worker"}}, spec: {count: 5, providerClass: "oci", ...}}

3. Apply queue debounces the spec changes and runs a single tofu apply
   → provider modules generate .tf.json (oci_core_instance + talos config)
   → tofu provisions cloud VMs; config delivered via user_data
   → bare metal (provider-metal) gets config via Talos API push
   → tofu writes state to the TF HTTP backend (source of truth)

4. Nodes boot fully configured and join the tenant cluster
   → etcd bootstraps on the first control-plane node
   → worker nodes join

5. Tenant phase derived from machine states
   forming → active (all machines ready)

6. User retrieves kubeconfig
   GET /api/v1/tenants/prod/kubeconfig → admin kubeconfig derived from the tenant secrets bundle

7. User deletes tenant
   DELETE /api/v1/tenants/prod
   → deletionTimestamp set, finalizers block removal
   → TenantReconciler runs tofu destroy, deprovisions each machine, removes finalizer
   → Secrets cleaned up, finalizer removed
   → All finalizers cleared → record removed
```

## State Backend

### SQLite (single writer)

SQLite is the sole state backend for the initial release
([ADR 0004](../adr/0004-sqlite-state-store.md)). It handles tens of thousands
of operations per second — more than sufficient for a personal cloud with tens
of machines across a handful of tenants.

- **Standalone**: binary + SQLite file on disk
- **Cluster (Helm)**: same binary in a K8s Deployment, SQLite on a PVC, single replica
- Temporary management plane unavailability does not affect running tenant clusters

### Future: Pluggable Backend

The Store interface is designed for future backend swap. The backend is selected
at startup; no backend switch at runtime — pick one at deploy time.

## Secrets Management

Tenant secrets (CA certs, etcd certs, admin keys) are stored as an encrypted
blob. OpenTofu owns all crypto — state is encrypted by `tofu` via `pbkdf2`+
`aes_gcm` config embedded in the generated `.tf.json`; RezusCloud's HTTP backend
stores opaque blobs and never implements crypto itself. The reconciler runs
`tofu state pull` (with `TF_ENCRYPTION` set) after each apply to extract
`client_configuration`, which it caches in memory.

The raw secrets bundle is never exposed via the API. Only derived artifacts
(kubeconfig, talosconfig) are returned through sub-resource endpoints.
