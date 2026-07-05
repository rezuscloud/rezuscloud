# How to manage config patches

> **Type:** How-to · **Audience:** Cluster operator

## Overview

Config patches are Talos configuration overlays applied during config generation.
They allow customizing kernel args, storage, network, and more.

## Steps

1. **Create a patch:**
   ```bash
   cat > patch.yaml <<'YAML'
   apiVersion: v1
   kind: ConfigPatch
   metadata:
     name: extra-kernel-args
     labels:
       rezuscloud.io/tenant: prod
   spec:
     format: strategic
     target: all
     enabled: true
     patches:
       - op: add
         path: /machine/install/extraKernelArgs
         value: ["nouveau.debug=1"]
   YAML
   rezusctl create -f patch.yaml --server $SERVER
   ```

2. **Toggle a patch on/off:**
   ```bash
   curl -X PUT $SERVER/api/v1/tenants/prod/configpatches/extra-kernel-args \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"metadata":{"name":"extra-kernel-args","resourceVersion":1},"spec":{"enabled":false}}'
   ```

3. **Reconciliation fires** — config generation includes the enabled patches on
   the next `tofu apply`.

## See also

- [Config delivery ADR](../adr/0008-config-delivery-user-data-and-talos-api.md)
- [ConfigPatch ADR](../adr/0014-configpatch-single-scope.md)
