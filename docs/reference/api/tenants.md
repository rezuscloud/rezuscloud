# Tenants API

> **Type:** Reference · **Audience:** API consumer

## Endpoints

### List tenants

```
GET /api/v1/tenants
```

**Response:** `200 OK`

```json
{
  "items": [
    {
      "metadata": {"name": "prod", "resourceVersion": 3},
      "spec": {
        "kubernetesVersion": "1.35.0",
        "talosVersion": "1.12.6",
        "controlPlaneEndpoint": "https://10.0.0.1:6443"
      },
      "status": {"phase": "active", "ready": true}
    }
  ],
  "total": 1
}
```

### Create tenant

```
POST /api/v1/tenants
```

**Body:**

```json
{
  "metadata": {"name": "prod"},
  "spec": {
    "kubernetesVersion": "1.35.0",
    "talosVersion": "1.12.6",
    "controlPlaneEndpoint": "https://10.0.0.1:6443"
  }
}
```

**Response:** `201 Created`

A Talos secrets bundle is auto-generated on creation.

### Get tenant

```
GET /api/v1/tenants/{name}
```

### Update tenant

```
PUT /api/v1/tenants/{name}
```

Requires `metadata.resourceVersion` for optimistic concurrency. Bumping
`spec.talosVersion` triggers a rolling upgrade.

### Delete tenant

```
DELETE /api/v1/tenants/{name}
```

Returns **202 Accepted** — deletion is asynchronous. The store stamps a
`deletionTimestamp` and two finalizers (`rezuscloud.io/machines`,
`rezuscloud.io/secrets`). The reconcile controller then runs `tofu destroy`
to tear down the real cloud infrastructure, cascade-removes child resources
(node groups, machines, config patches), drops cached secrets, and clears the
finalizers — the last one triggers garbage-collection of the tenant row. On
destroy failure the finalizers are left intact and the next resync re-attempts.

### Watch tenants

```
GET /api/v1/tenants?watch=true
```

Upgrades to an SSE stream of `{"type":"ADDED|MODIFIED|DELETED","object":{...}}`
frames. An initial `ADDED` burst reflects current state, then live deltas flow
until the client disconnects.

### Download kubeconfig

```
GET /api/v1/tenants/{name}/kubeconfig
```

Returns a kubeconfig file for the tenant's Kubernetes API server.

### Download talosconfig

```
GET /api/v1/tenants/{name}/talosconfig
```

Returns a talosconfig file for the tenant's Talos API.

## Tenant Spec

| Field | Type | Description |
|-------|------|-------------|
| `kubernetesVersion` | string | Required. Kubernetes version (e.g., "1.35.0") |
| `talosVersion` | string | Talos version (e.g., "1.12.6") |
| `controlPlaneEndpoint` | string | API server endpoint URL |
| `podNetwork` | []string | Pod CIDRs |
| `serviceNetwork` | []string | Service CIDRs |
| `nodeGroups` | []NodeGroupSpec | Inline node group definitions |
| `configPatches` | []ConfigPatchRef | Config patch references |
