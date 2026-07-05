# How to deploy a tenant on OCI

> **Type:** How-to · **Audience:** Operator with OCI access

## Overview

Create a Talos cluster on Oracle Cloud Infrastructure using the OCI provider.

## Prerequisites

- RezusCloud management plane running
- OCI credentials configured (`OCI_*` env vars on the deployment)
- An OCI compartment, subnet, and Talos image OCID

## Steps

1. **Gather OCI values** from the OCI console:
   - Compartment OCID
   - Subnet OCID
   - Talos image OCID (or let RezusCloud use `TF_VAR_talos_image_ocid`)
   - Region (e.g., `us-phoenix-1`)

2. **Create the tenant:**
   ```bash
   curl -X POST $SERVER/api/v1/tenants \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"metadata":{"name":"oci-prod"},"spec":{"kubernetesVersion":"1.35.0","talosVersion":"1.12.6","controlPlaneEndpoint":"https://10.0.0.10:6443"}}'
   ```

3. **Create the control-plane node group:**
   ```bash
   cat > cp.yaml <<'YAML'
   apiVersion: v1
   kind: NodeGroup
   metadata:
     name: controlplane
     labels:
       rezuscloud.io/tenant: oci-prod
   spec:
     name: controlplane
     role: controlplane
     count: 1
     providerClass: "oci:VM.Standard.A1.Flex"
     providerConfig:
       region: "us-phoenix-1"
       compartmentOcid: "ocid1.compartment.oc1..YOUR_COMPARTMENT"
       subnetId: "ocid1.subnet.oc1.phx.YOUR_SUBNET"
       imageOcid: "ocid1.image.oc1.phx.YOUR_IMAGE"
       ocpus: 2
       memoryGb: 4
   YAML
   rezusctl create -f cp.yaml --server $SERVER
   ```

4. **Create worker node groups** (repeat with `role: worker`).

5. **Watch reconciliation:**
   ```bash
   rezusctl describe cluster oci-prod --server $SERVER
   ```

## Verification

```bash
rezusctl get machines --cluster oci-prod --server $SERVER
```

Each machine should show `providerType: oci` with a real management address.

## See also

- [Tutorial: First cluster](../tutorials/install-and-first-cluster.md)
- [Components](../concepts/components.md)
