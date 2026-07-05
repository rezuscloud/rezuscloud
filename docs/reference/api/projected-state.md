# Projected State API

> **Type:** Reference · **Audience:** API consumer

The projected state endpoints expose the TF-state projection index — a read-only
view of what `tofu apply` created, before store enrichment.

## Endpoints

### List all projected resources

```
GET /api/v1/projected
```

### List projected resources for a tenant

```
GET /api/v1/tenants/{tenant}/projected
```

### List projected resources by kind

```
GET /api/v1/tenants/{tenant}/projected/{kind}
```

Common kinds: `Machine`, `DataSource`.

**Response:** `200 OK`

```json
{
  "items": [
    {
      "kind": "Machine",
      "tfType": "oci_core_instance",
      "name": "controlplane-0",
      "spec": {
        "address": "10.0.0.1",
        "providerId": "ocid1.instance.abc",
        "shape": "VM.Standard.A1.Flex"
      }
    }
  ]
}
```

Returns `503 Service Unavailable` if no apply has run yet (projection index is nil).
