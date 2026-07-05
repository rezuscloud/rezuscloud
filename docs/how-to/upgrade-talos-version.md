# How to upgrade the Talos version

> **Type:** How-to · **Audience:** Cluster operator

## Overview

Bump the Talos version in a tenant spec. RezusCloud detects the drift and runs a
rolling upgrade (one machine at a time via the Talos API) before regenerating
version-specific config.

## Prerequisites

- A running tenant with at least one machine
- All machines reachable via the Talos API

## Steps

1. **Update the tenant spec** with the new Talos version:

   ```bash
   curl -X PUT $SERVER/api/v1/tenants/prod \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "metadata": {"name": "prod", "resourceVersion": 5},
       "spec": {
         "kubernetesVersion": "1.35.0",
         "talosVersion": "1.12.7",
         "controlPlaneEndpoint": "https://10.0.0.1:6443"
       }
     }'
   ```

2. **The pre-apply upgrade hook fires automatically:**
   - RezusCloud detects the version drift (spec vs observed machine versions)
   - The rolling upgrade engine upgrades one machine at a time
   - Each upgrade: Talos API `upgrade` → health check → rollback on failure
   - Control-plane machines are upgraded first, then workers

3. **After all machines are upgraded**, `tofu apply` runs to regenerate
   version-specific Talos config.

## Verification

```bash
rezusctl describe cluster prod --server $SERVER
```

The reconciliation phase should show `applied` and machines should report the
new `talosVersion`.

## See also

- [Components: Rolling Upgrade Engine](../concepts/components.md#rolling-upgrade-engine)
