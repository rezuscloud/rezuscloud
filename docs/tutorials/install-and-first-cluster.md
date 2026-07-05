# Tutorial: Install RezusCloud and Create Your First Cluster

> **Type:** Tutorial · **Audience:** New user · **Assumes:** Nothing

In this tutorial, you will install RezusCloud, create your first tenant cluster,
add a node group, and verify that machines appear. By the end, you will have a
running RezusCloud management plane with one cluster under management.

## Prerequisites

Before you start, you need:

1. **A Linux machine** (bare metal, VM, or cloud instance) to run the management
   plane. 2 vCPU and 4 GB RAM is sufficient for development.
2. **Docker** installed — [install instructions](https://docs.docker.com/engine/install/).
3. **A Kubernetes cluster** (any type — even a single-node k3s) to deploy
   RezusCloud into. If you don't have one, install k3s:
   ```bash
   curl -sfL https://get.k3s.io | sh -
   ```
4. **kubectl** installed and configured to talk to your cluster.
5. **A cloud account** (OCI, OpenStack) OR **a bare-metal machine** to create
   tenant clusters on. For this tutorial, you can use the demo environment at
   [demo.rezus.cloud](https://demo.rezus.cloud) if you don't have cloud access yet.

## Step 1: Deploy the RezusCloud management plane

RezusCloud ships as a Helm chart. Add the Helm repository and install:

**Do this:**

```bash
# Clone the repository
git clone https://github.com/rezuscloud/rezuscloud.git
cd rezuscloud

# Generate a JWT secret
JWT_SECRET=$(openssl rand -hex 32)

# Install with Helm
helm install rezuscloud ./chart \
  --set jwtSecret="$JWT_SECRET" \
  --set rezuscloud.adminPassword="admin123" \
  --namespace rezuscloud \
  --create-namespace
```

**You should see:**

```
NAME: rezuscloud
NAMESPACE: rezuscloud
STATUS: deployed
```

Wait for the pod to be ready:

```bash
kubectl -n rezuscloud wait --for=condition=ready pod -l app.kubernetes.io/name=rezuscloud --timeout=120s
```

## Step 2: Verify the management plane is running

**Do this:**

```bash
# Port-forward to access the management plane
kubectl -n rezuscloud port-forward svc/rezuscloud 8080:8080 &

# Check health
curl http://localhost:8080/healthz
```

**You should see:**

```
ok
```

**Do this:**

```bash
# Check readiness
curl http://localhost:8080/readyz
```

**You should see:**

```
ready
```

## Step 3: Install the CLI

**Do this:**

```bash
# Download the latest release
curl -sL https://github.com/rezuscloud/rezuscloud/releases/latest/download/rezusctl_linux_amd64.tar.gz | tar xz

# Move to PATH
sudo mv rezusctl /usr/local/bin/

# Verify
rezusctl version
```

**You should see:**

```
rezusctl version v0.x.x (commit: abc1234)
```

## Step 4: Authenticate

**Do this:**

```bash
# Log in and save the API token
export REZUSCLOUD_SERVER=http://localhost:8080
rezusctl user login --server $REZUSCLOUD_SERVER
# Username: admin
# Password: admin123 (the password you set in Step 1)
```

The CLI stores the token in `~/.rezuscloud/token`. All subsequent commands use
it automatically.

**You should see:**

```
Login successful. Token saved.
```

## Step 5: Create a tenant

A tenant is a Talos cluster under management. Create one with a Kubernetes and
Talos version:

**Do this:**

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

rezusctl create -f tenant.yaml --server $REZUSCLOUD_SERVER
```

**You should see:**

```
Created tenant "my-first-cluster"
```

Verify the tenant exists:

```bash
rezusctl get clusters --server $REZUSCLOUD_SERVER
```

**You should see:**

```
NAME               KUBERNETES   TALOS    PHASE
my-first-cluster   1.35.0       1.12.6   forming
```

## Step 6: Add a node group

A tenant needs at least one node group to define the machines. Create a control
plane node group:

**Do this:**

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
    compartmentOcid: "ocid1.compartment.oc1..YOUR_COMPARTMENT"
    subnetId: "ocid1.subnet.oc1.phx.YOUR_SUBNET"
    imageOcid: "ocid1.image.oc1.phx.YOUR_IMAGE"
    ocpus: 2
    memoryGb: 4
YAML

rezusctl create -f cp.yaml --server $REZUSCLOUD_SERVER
```

> **Note:** Replace the OCID placeholders with real values from your OCI console.
> If you don't have OCI access, you can use the WebUI to experiment — navigate to
> `http://localhost:8080`, log in, and create a cluster from the UI.

**You should see:**

```
Created node group "controlplane"
```

## Step 7: Watch reconciliation

Creating a node group fires a store mutation → the apply queue enqueues the
tenant → `tofu init` + `tofu apply` runs. This takes 1–3 minutes on first run
(downloading providers, creating cloud resources).

**Do this:**

```bash
# Check the reconciliation status
rezusctl describe cluster my-first-cluster --server $REZUSCLOUD_SERVER
```

**You should see something like:**

```
Name:           my-first-cluster
Phase:          forming
Kubernetes:     1.35.0
Talos:          1.12.6
Reconciliation: applying
```

When reconciliation completes, the phase changes to `applied`.

## Step 8: Verify convergence

After a successful apply, the projected machines are enriched into the store.

**Do this:**

```bash
rezusctl get machines --cluster my-first-cluster --server $REZUSCLOUD_SERVER
```

**You should see:**

```
ID                     STAGE      ROLE           PROVIDER
controlplane-0         ready      controlplane   oci
```

## Step 9: Download credentials

**Do this:**

```bash
# Download the kubeconfig
rezusctl kubeconfig my-first-cluster --server $REZUSCLOUD_SERVER > kubeconfig

# Use it with kubectl
kubectl --kubeconfig kubeconfig get nodes
```

**You should see:**

```
NAME               STATUS   ROLES           AGE   VERSION
controlplane-0     Ready    control-plane   2m    v1.35.0
```

## Step 10: Explore the WebUI

**Do this:**

Open `http://localhost:8080` in your browser. Log in with `admin` / `admin123`.

You should see:
- **Dashboard** — overview of tenants, machines, and resource counts
- **Clusters** — your `my-first-cluster` with node groups, machines, and
  reconciliation status
- **Machines** — the fleet view with machine stages and health

## What you learned

- RezusCloud runs as a management plane (H chart on Kubernetes)
- Tenants are cluster identities; node groups define the machines
- Creating a node group triggers reconciliation (`tofu apply`)
- After convergence, machines appear in the store with real IPs
- Credentials (kubeconfig, talosconfig) are available immediately

## Next steps

- [How to deploy on OCI](../how-to/deploy-on-oci.md) — detailed OCI setup
- [How to add a bare-metal node](../how-to/add-bare-metal-node.md) — metal provider
- [Components](../concepts/components.md) — understand the architecture
- [Architecture](../concepts/architecture.md) — the two data planes model
