# First Real Deployment Runbook

> This runbook walks through creating a real tenant cluster against real cloud
> infrastructure (OCI or OpenStack), verifying the full reconciliation pipeline.
> It is the validation that everything works end-to-end: spec → apply →
> convergence → health.

## Prerequisites

### RezusCloud running

The management plane must be deployed and reachable:

```bash
# Verify the server is up
curl https://rezuscloud.example.com/healthz
# → ok

curl https://rezuscloud.example.com/readyz
# → ready

curl https://rezuscloud.example.com/version
# → {"version":"...","gitCommit":"...","buildTime":"..."}
```

### OpenTofu installed

RezusCloud exec's `tofu` as a subprocess. The binary must be on PATH inside the
container:

```bash
# Inside the rezuscloud pod
kubectl exec -n rezuscloud deploy/rezuscloud -- tofu version
```

If `tofu` is not found, add it to the container image or install it as a sidecar.

### Cloud credentials

RezusCloud needs the cloud provider's credentials as environment variables so
`tofu` can authenticate. These are set on the rezuscloud deployment (Helm values
or env vars), NOT on the tenant.

#### OCI

```bash
# Set on the rezuscloud deployment
OCI_CLI_USER=ocid1.user.oc1...
OCI_CLI_TENANCY=ocid1.tenancy.oc1...
OCI_CLI_FINGERPRINT=aa:bb:cc:...
OCI_CLI_KEY_CONTENT=-----BEGIN RSA PRIVATE KEY-----...
OCI_CLI_REGION=us-phoenix-1
TF_VAR_region=us-phoenix-1
```

#### OpenStack

```bash
OS_AUTH_URL=https://identity.example.com/v3
OS_USERNAME=...
OS_PASSWORD=...
OS_PROJECT_NAME=...
OS_USER_DOMAIN_NAME=...
OS_PROJECT_DOMAIN_NAME=...
```

## Step 1: Create a tenant

Create the cluster identity. This generates the Talos secrets bundle and
enqueues reconciliation (but with no node groups, the apply is a no-op).

### Via CLI

```bash
cat > tenant.yaml <<'YAML'
apiVersion: v1
kind: Tenant
metadata:
  name: my-first-cluster
spec:
  kubernetesVersion: "1.35.0"
  talosVersion: "1.12.6"
  controlPlaneEndpoint: "https://10.0.0.10:6443"
YAML

rezusctl create -f tenant.yaml --server https://rezuscloud.example.com
```

### Via API

```bash
curl -X POST https://rezuscloud.example.com/api/v1/tenants \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "metadata": {"name": "my-first-cluster"},
    "spec": {
      "kubernetesVersion": "1.35.0",
      "talosVersion": "1.12.6",
      "controlPlaneEndpoint": "https://10.0.0.10:6443"
    }
  }'
```

### Via WebUI

Navigate to **Clusters → Create Cluster**, enter the name and versions, submit.

## Step 2: Add node groups

Node groups define the machines. Each node group specifies a provider class
(which RezusCloud provider renders the TF config) and provider-specific config.

### OCI example

```bash
cat > cp.yaml <<'YAML'
apiVersion: v1
kind: NodeGroup
metadata:
  name: controlplane
  labels:
    rezuscloud.io/tenant: my-first-cluster
spec:
  name: controlplane
  role: controlplane
  count: 1
  providerClass: "oci:VM.Standard.A1.Flex"
  providerConfig:
    region: "us-phoenix-1"
    compartmentOcid: "ocid1.compartment.oc1..aaa..."
    subnetId: "ocid1.subnet.oc1.phx.aaa..."
    imageOcid: "ocid1.image.oc1.phx.aaa..."
    ocpus: 2
    memoryGb: 4
YAML

rezusctl create -f cp.yaml --server https://rezuscloud.example.com
```

Repeat for worker node groups:

```bash
cat > workers.yaml <<'YAML'
apiVersion: v1
kind: NodeGroup
metadata:
  name: workers
  labels:
    rezuscloud.io/tenant: my-first-cluster
spec:
  name: workers
  role: worker
  count: 2
  providerClass: "oci:VM.Standard.A1.Flex"
  providerConfig:
    region: "us-phoenix-1"
    compartmentOcid: "ocid1.compartment.oc1..aaa..."
    subnetId: "ocid1.subnet.oc1.phx.aaa..."
    imageOcid: "ocid1.image.oc1.phx.aaa..."
    ocpus: 2
    memoryGb: 8
YAML

rezusctl create -f workers.yaml --server https://rezuscloud.example.com
```

### Via WebUI

On the cluster detail page, use the **Add Node Group** form (Overview tab).
Enter name, role, count, provider class, and paste the provider config JSON.

## Step 3: Watch reconciliation

Creating node groups fires store mutation events → NATS → EnqueueBus → apply
queue. The reconciler debounces and runs `tofu init` + `tofu apply`.

### Check reconciliation status

```bash
# API
curl https://rezuscloud.example.com/api/v1/tenants/my-first-cluster \
  -H "Authorization: Bearer $TOKEN" | jq .status.reconciliation

# → { "phase": "applying", "updatedAt": "2026-07-04T20:00:00Z" }
```

### WebUI

The cluster detail page shows a reconciliation banner with the current phase
(queued → applying → applied / failed) and any last error.

### Server logs

```bash
kubectl logs -n rezuscloud deploy/rezuscloud | grep reconcile
```

Expected phases: `queued` → `applying` → `applied`.

If the phase is `failed`, check `lastError` for the tofu output.

## Step 4: Verify convergence

After a successful apply, the projected TF-state machines are enriched into
the store. The cluster detail page should show machines with their real IPs.

```bash
# List machines
rezusctl get machines --cluster my-first-cluster --server https://rezuscloud.example.com

# Via API
curl https://rezuscloud.example.com/api/v1/tenants/my-first-cluster/machines \
  -H "Authorization: Bearer $TOKEN" | jq '.items[].spec'
```

Each machine should have:
- `managementAddress` — the instance's IP
- `providerId` — the cloud provider's resource ID (OCID/UUID)
- `shape` — the instance shape/flavor
- `providerType` — "oci" / "openstack" / "metal"

## Step 5: Verify health

```bash
# Per-tenant health
curl https://rezuscloud.example.com/api/v1/tenants/my-first-cluster/health \
  -H "Authorization: Bearer $TOKEN" | jq

# → {
#   "tenant": "my-first-cluster",
#   "reachable": true,
#   "talosVersion": "1.12.6",
#   "machineCount": 3
# }
```

If `reachable` is `true`, the management plane can reach the cluster's Talos
API. If `false`, check network connectivity (firewall, security lists).

## Step 6: Download credentials

```bash
# Kubeconfig (use with kubectl)
rezusctl kubeconfig my-first-cluster --server https://rezuscloud.example.com > kubeconfig
kubectl --kubeconfig kubeconfig get nodes

# Talosconfig (use with talosctl)
rezusctl talosconfig my-first-cluster --server https://rezuscloud.example.com > talosconfig
talosctl --talosconfig talosconfig health -n 10.0.0.10
```

## Step 7: Version upgrade (optional)

To test the rolling upgrade engine:

```bash
# Bump the Talos version in the tenant spec
curl -X PUT https://rezuscloud.example.com/api/v1/tenants/my-first-cluster \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "If-Match: <resourceVersion>" \
  -d '{
    "metadata": {"name": "my-first-cluster", "resourceVersion": "..."},
    "spec": {
      "kubernetesVersion": "1.35.0",
      "talosVersion": "1.12.7",
      "controlPlaneEndpoint": "https://10.0.0.10:6443"
    }
  }'
```

The reconciler detects the version drift → runs the rolling upgrade engine
(one machine at a time via the Talos API) → then `tofu apply` regenerates
version-specific config.

Watch on the cluster detail page → Upgrade tab.

## Troubleshooting

### Phase stuck on `queued`

The apply queue debounces for a few seconds. If it stays queued for more than
a minute, check server logs for the queue worker.

### Phase: `failed` — `tofu` errors

Common causes:
- **Missing cloud credentials**: `OCI_*` / `OS_*` env vars not set on the
  rezuscloud deployment
- **Missing `TF_VAR_region`**: OCI requires the region variable
- **Invalid compartment/subnet OCIDs**: verify in the cloud console
- **Image not found**: the Talos image OCID must exist in your compartment

### No machines after apply

- Check the projected state: `GET /api/v1/tenants/my-first-cluster/projected`
- If projected has machines but the store doesn't, the StoreEnricher may have
  failed — check logs for `enrich:` messages
- The provider may not have a resource mapping for the TF resource type

### Health shows `reachable: false`

- The Talos API (port 50000) must be reachable from the rezuscloud pod
- Check security lists / network security groups
- Verify the management address in the machine spec matches the instance IP
