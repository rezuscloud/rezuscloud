# Health API

> **Type:** Reference · **Audience:** API consumer

On-demand health probes with a 15-second in-memory TTL (ADR 0016). No background
scrapers.

## Endpoints

### Tenant health

```
GET /api/v1/tenants/{name}/health
```

Probes the tenant's machines via the Talos API. Control-plane machines are
checked first, then workers.

**Response:** `200 OK`

```json
{
  "tenant": "prod",
  "reachable": true,
  "talosVersion": "1.12.6",
  "machineCount": 3,
  "probedAt": "2026-07-05T20:00:00Z"
}
```

If no credentials are cached or the probe fails:

```json
{
  "tenant": "prod",
  "reachable": false,
  "error": "no reachable machines",
  "machineCount": 3
}
```

### Fleet health

```
GET /api/v1/health
```

Returns health for all tenants.

**Response:** `200 OK`

```json
{
  "tenants": [
    {"tenant": "prod", "reachable": true, "talosVersion": "1.12.6"},
    {"tenant": "staging", "reachable": false, "error": "no credentials"}
  ]
}
```
