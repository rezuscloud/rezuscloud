# Install and Deploy RezusCloud

> **Type:** How-to · **Audience:** Operator deploying the management plane

This guide walks through installing RezusCloud and bootstrapping your first
tenant cluster. For the architecture behind these steps, see
[Architecture](../concepts/architecture.md) and the
[ADRs](../adr/README.md).

## What RezusCloud is

RezusCloud is a **tenant orchestrator** that lives one layer above `talosctl`
and `kubectl`. You declare Kubernetes clusters (called *tenants*); RezusCloud
realises them by driving OpenTofu (exec'ing `tofu`) against real Terraform
providers, delivers Talos config via `user_data` (cloud) or the Talos API
(bare metal), and surfaces cluster state read-only. It never duplicates
`kubectl` features — once a tenant is up, you get a kubeconfig and work with
the lower layers directly. See [ADR 0001](../adr/0001-what-rezuscloud-is.md).

## Prerequisites

| Requirement | Version | Check |
|---|---|---|
| Go | 1.26+ | `go version` |
| Docker | 20+ | `docker version` |

## Install

Download the latest release from [GitHub](https://github.com/rezuscloud/rezuscloud/releases):

```bash
# Linux (amd64)
curl -sL https://github.com/rezuscloud/rezuscloud/releases/latest/download/rezuscloud_linux_amd64.tar.gz | tar xz
sudo mv rezusctl /usr/local/bin/
sudo mv rezuscloud /usr/local/bin/

# Or use Docker
docker pull ghcr.io/rezuscloud/rezuscloud:latest
```

Verify:

```bash
rezusctl version
```

## Quick Start

### 1. Bootstrap a Local Management Cluster

Create a management cluster locally without cloud credentials:

```bash
rezusctl boot --platform docker
```

This creates a single-node Talos cluster in Docker containers, installs Cilium
CNI, and deploys the RezusCloud management plane. Takes about 40 seconds.

### 2. Configure the CLI

```bash
# Point CLI to management plane
rezusctl config url http://localhost:3000

# Create admin user (first time)
rezusctl user create admin --role admin --password secret
```

### 3. Create a Tenant

```bash
rezusctl create cluster personal
```

This declares a tenant. RezusCloud's reconciliation loop (a debounced per-tenant
apply queue) drives `tofu apply` to provision the infrastructure declared by the
tenant's [provider](../adr/0007-provider-as-tf-wrapper.md) modules and node
groups. See [ADR 0006](../adr/0006-exec-tofu-binary.md).

### 4. Get Tenant Credentials

```bash
rezusctl kubeconfig personal > personal-kubeconfig.yaml

export KUBECONFIG=personal-kubeconfig.yaml
kubectl get nodes
```

From here, day-to-day work happens with `kubectl` against the tenant cluster —
RezusCloud stays responsible for the cluster-as-a-unit (existence,
configuration, upgrades, teardown), not the in-cluster objects.

## How tenants get realised

```
rezuscloud (management plane)
  │
  │  spec change (create tenant, scale node group, …)
  ▼
  debounced per-tenant apply queue  ──►  tofu apply  ──►  real TF providers
                                                                    │
                                         ┌──────────────────────────┘
                                         ▼
                          VMs (user_data) / bare metal (Talos API push)
                                         │
                                         ▼
                                 Talos node joins the tenant cluster
```

- **Config delivery** is via `user_data` at VM creation (cloud) or
  `talos_machine_configuration_apply` to a maintenance-mode node (bare metal) —
  no SideroLink, no outbound management-plane dependency during boot. See
  [ADR 0008](../adr/0008-config-delivery-user-data-and-talos-api.md).
- **TF state** is the single source of truth for declared infrastructure
  ([ADR 0005](../adr/0005-tf-state-single-source-of-truth.md)); observed status
  (node health, stage) is a separate best-effort plane that is never written
  back to TF state ([ADR 0010](../adr/0010-status-plane-best-effort.md)).

## Next Steps

- [Architecture](../concepts/architecture.md) — How RezusCloud works internally
- [Multi-Cluster](multi-cluster.md) — Multi-tenant cluster management
- [API Design](../concepts/api-design.md) — REST API resource model
- [CLI Reference](../reference/cli.md) — All commands and flags
- [Versioning](../reference/versioning.md) — Automatic semantic versioning
