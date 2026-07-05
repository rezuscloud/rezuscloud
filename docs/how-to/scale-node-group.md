# How to scale a node group

> **Type:** How-to · **Audience:** Cluster operator

## Overview

Change the number of machines in a node group.

## Steps

1. **Update the count** via the API, CLI, or WebUI:

   **Via CLI:**
   ```bash
   cat > ng.yaml <<'YAML'
   apiVersion: v1
   kind: NodeGroup
   metadata:
     name: workers
     labels:
       rezuscloud.io/tenant: prod
     resourceVersion: 3
   spec:
     name: workers
     role: worker
     count: 5
     providerClass: "oci:VM.Standard.A1.Flex"
   YAML
   rezusctl apply -f ng.yaml --server $SERVER
   ```

   **Via WebUI:** Click "Scale" on the node group row, enter the new count.

2. **Reconciliation fires automatically** — the apply queue detects the spec
   change and runs `tofu apply` to create or destroy machines.

## Verification

```bash
rezusctl get machines --cluster prod --server $SERVER
```

The machine count should match the new `spec.count`.
