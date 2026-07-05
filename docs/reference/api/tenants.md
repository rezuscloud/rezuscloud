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

Sets a deletion timestamp; finalizers clean up resources before removal.

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
