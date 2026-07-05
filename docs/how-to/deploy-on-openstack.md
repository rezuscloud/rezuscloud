# How to deploy a tenant on OpenStack

> **Type:** How-to · **Audience:** Operator with OpenStack access

## Overview

Create a Talos cluster on OpenStack using the OpenStack provider.

## Prerequisites

- RezusCloud management plane running
- OpenStack credentials configured (`OS_*` env vars on the deployment)
- A Glance image named `talos-<version>-openstack-amd64`
- An external network, flavor, and subnet

## Steps

1. **Create the tenant** with the desired versions.

2. **Create a node group** with `providerClass: "openstack:SCS-4V-8-50"` (or your
   flavor name) and provider config:
   ```json
   {
     "flavorName": "SCS-4V-8-50",
     "imageName": "talos-1.12.6-openstack-amd64",
     "extNetName": "ext-net"
   }
   ```

3. **Watch reconciliation** — same as OCI.

## Verification

```bash
rezusctl get machines --cluster <name> --server $SERVER
```

Machines should show `providerType: openstack`.
