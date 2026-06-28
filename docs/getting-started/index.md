# Getting Started

> **⚠ This guide describes the pre-pivot architecture (gRPC providers +
> SideroLink) and is stale.** The current architecture is documented in
> [`CONTEXT.md`](../../CONTEXT.md) and [`docs/adr/`](../adr/README.md) —
> RezusCloud now drives infrastructure through OpenTofu (exec'ing `tofu`),
> config delivery is via `user_data` + Talos API (no SideroLink), and there
> are no provider binaries. This guide will be rewritten; until then, treat
> the provider/SideroLink descriptions below as historical, not current.

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

This creates a single-node Talos cluster in Docker containers, installs Cilium CNI, and deploys the RezusCloud management plane. Takes about 40 seconds.

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

### 4. Get Tenant Credentials

```bash
rezusctl kubeconfig personal > personal-kubeconfig.yaml

export KUBECONFIG=personal-kubeconfig.yaml
kubectl get nodes
```

### 5. Connect a Provider

Providers provision machines for tenant clusters. They connect outbound to the management plane:

```bash
docker run -d --name provider-hetzner \
  -e REZUSCTL_ENDPOINT=https://manage.mycloud.dev \
  -e REZUSCTL_TOKEN=your-token \
  ghcr.io/rezuscloud/provider-hetzner:latest
```

Providers only create/delete machines. Machines pull config from the management plane via SideroLink.

## Next Steps

- [Architecture](../concepts/architecture.md) — How RezusCloud works internally
- [CLI Reference](../reference/cli.md) — All commands and flags
- [Multi-Cluster](multi-cluster.md) — Multi-tenant cluster management
- [Versioning](../reference/versioning.md) — Automatic semantic versioning
