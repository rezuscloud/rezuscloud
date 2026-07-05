# Config Patches API

> **Type:** Reference · **Audience:** API consumer

Config patches are Talos configuration overlays applied during config generation.

## Endpoints

### List

```
GET /api/v1/tenants/{tenant}/configpatches
```

### Create

```
POST /api/v1/tenants/{tenant}/configpatches
```

### Get

```
GET /api/v1/tenants/{tenant}/configpatches/{name}
```

### Update

```
PUT /api/v1/tenants/{tenant}/configpatches/{name}
```

### Delete

```
DELETE /api/v1/tenants/{tenant}/configpatches/{name}
```

## Config Patch Spec

| Field | Type | Description |
|-------|------|-------------|
| `format` | string | "strategic" or "jsonpatch" |
| `target` | string | Role target ("controlplane", "worker", "all") |
| `enabled` | bool | Whether the patch is active |
| `patches` | []object | The patch content |
