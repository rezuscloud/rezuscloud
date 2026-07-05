# How to add a bare-metal node

> **Type:** How-to · **Audience:** Operator with physical hardware

## Overview

Boot a Talos node into maintenance mode and apply configuration via the metal
provider (Talos API push).

## Prerequisites

- A machine booted from the Talos metal ISO or PXE (maintenance mode)
- The node's IP address reachable from the RezusCloud management plane
- Network access to port 50000 on the node

## Steps

1. **Boot the node** from the Talos metal ISO. It enters maintenance mode and
   listens on port 50000.

2. **Create the machine record:**
   ```bash
   rezusctl create -f - --server $SERVER <<'YAML'
   apiVersion: v1
   kind: Machine
   metadata:
     name: node-1
     labels:
       rezuscloud.io/tenant: metal-prod
       rezuscloud.io/role: worker
   spec:
     managementAddress: "192.168.1.50:50000"
   YAML
   ```

3. **Create a metal node group** referencing the machine:
   ```bash
   cat > ng.yaml <<'YAML'
   apiVersion: v1
   kind: NodeGroup
   metadata:
     name: workers
     labels:
       rezuscloud.io/tenant: metal-prod
   spec:
     name: workers
     role: worker
     count: 1
     providerClass: "metal"
     providerConfig:
       machines:
         "192.168.1.50:50000":
           hostname: "node-1"
   YAML
   rezusctl create -f ng.yaml --server $SERVER
   ```

4. **Watch reconciliation** — the metal provider runs
   `talos_machine_configuration_apply` to push config to the node.

## Verification

```bash
rezusctl get machines --cluster metal-prod --server $SERVER
```

The machine should transition from `initializing` to `ready`.

## See also

- [Config delivery ADR](../adr/0008-config-delivery-user-data-and-talos-api.md)
