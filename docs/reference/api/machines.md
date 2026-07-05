# Machines API

> **Type:** Reference · **Audience:** API consumer

Machines are physical or virtual nodes running Talos. They are created by the
reconciliation pipeline (not directly by users) and enriched from TF state.

## Endpoints

### List all machines

```
GET /api/v1/machines
```

### List machines by tenant

```
GET /api/v1/tenants/{tenant}/machines
```

### Get machine

```
GET /api/v1/tenants/{tenant}/machines/{id}
```

### Get machine config

```
GET /api/v1/tenants/{tenant}/machines/{id}/config
```

Returns the rendered Talos machine configuration.

### Update machine status

```
PUT /api/v1/tenants/{tenant}/machines/{id}/status
```

Controller-only endpoint for updating observed state.

### Delete machine

```
DELETE /api/v1/tenants/{tenant}/machines/{id}
```

## Machine Spec

| Field | Type | Description |
|-------|------|-------------|
| `managementAddress` | string | Talos API endpoint (IP:port) |
| `connected` | bool | Whether the machine is reachable |
| `providerId` | string | Cloud provider resource ID (OCID/UUID) |
| `providerType` | string | "oci", "openstack", "metal" |
| `shape` | string | Instance shape/flavor |

## Machine Status

| Field | Type | Description |
|-------|------|-------------|
| `stage` | string | Lifecycle stage (initializing, ready, off, ...) |
| `ready` | bool | Machine is healthy |
| `role` | string | "controlplane" or "worker" |
| `talosVersion` | string | Observed Talos version |
| `kubernetesVersion` | string | Observed Kubernetes version |
