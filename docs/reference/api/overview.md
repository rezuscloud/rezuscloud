# API Reference

> **Type:** Reference · **Audience:** API consumer

## Base URL

```
http://<rezuscloud-host>:8080/api/v1
```

## Authentication

All endpoints except `/auth/login` require a Bearer token:

```
Authorization: Bearer <jwt-or-api-token>
```

Obtain a token via `POST /api/v1/auth/login` or create an API token via
`POST /api/v1/users/{name}/tokens`.

## Resource Model

Every resource follows the Kubernetes shape:

```json
{
  "metadata": {
    "name": "my-cluster",
    "resourceVersion": 1,
    "labels": {},
    "annotations": {},
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-01T00:00:00Z"
  },
  "spec": { ... },
  "status": { ... }
}
```

## Optimistic Concurrency

Updates (`PUT`) require the current `resourceVersion` in the request body.
If the version is stale, the server returns `409 Conflict`.

## Error Format

```json
{
  "error": "tenant already exists",
  "code": "Conflict",
  "status": 409
}
```

## Pagination

List endpoints accept `?limit=N&offset=M` query parameters. The response includes
`total` and `items`.

## Endpoints by Resource

- [Tenants](tenants.md) — cluster lifecycle
- [Node Groups](node-groups.md) — machine groups within a tenant
- [Machines](machines.md) — individual nodes
- [Config Patches](config-patches.md) — Talos config overlays
- [Projected State](projected-state.md) — TF-state projection
- [Health](health.md) — on-demand health probes
