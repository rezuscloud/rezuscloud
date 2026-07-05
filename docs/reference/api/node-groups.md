# Node Groups API

> **Type:** Reference · **Audience:** API consumer

Node groups are tenant-scoped resources defining sets of machines.

## Endpoints

### List node groups

```
GET /api/v1/tenants/{tenant}/node-groups
```

### Create node group

```
POST /api/v1/tenants/{tenant}/node-groups
```

**Body:**

```json
{
  "metadata": {"name": "controlplane"},
  "spec": {
    "name": "controlplane",
    "role": "controlplane",
    "count": 1,
    "providerClass": "oci:VM.Standard.A1.Flex",
    "providerConfig": {
      "region": "us-phoenix-1",
      "compartmentOcid": "ocid1.compartment.oc1..aaa",
      "subnetId": "ocid1.subnet.oc1.phx.aaa",
      "ocpus": 2,
      "memoryGb": 4
    }
  }
}
```

**Response:** `201 Created`

### Get node group

```
GET /api/v1/tenants/{tenant}/node-groups/{name}
```

### Update node group

```
PUT /api/v1/tenants/{tenant}/node-groups/{name}
```

Requires `metadata.resourceVersion`. Changing `spec.count` scales the group.

### Delete node group

```
DELETE /api/v1/tenants/{tenant}/node-groups/{name}
```

## Node Group Spec

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Node group name |
| `role` | string | "controlplane" or "worker" |
| `count` | int | Number of machines (min 1) |
| `providerClass` | string | Provider routing (e.g., "oci:VM.Standard.A1.Flex") |
| `providerConfig` | object | Provider-specific config (JSON) |
| `talosVersion` | string | Override tenant Talos version |
| `configPatches` | []ConfigPatchRef | Per-group config patches |
