# Multi-Cluster with Full Talos Lifecycle

> **⚠ This guide describes the pre-pivot architecture (gRPC providers +
> SideroLink) and is stale.** The current architecture is documented in
> [`CONTEXT.md`](../../CONTEXT.md) and [`docs/adr/`](../adr/README.md) —
> RezusCloud now drives infrastructure through OpenTofu (exec'ing `tofu`),
> config delivery is via `user_data` + Talos API (no SideroLink), and there
> are no provider binaries or SideroLink server. This guide will be
> rewritten; until then, treat the topology and bootstrap-flow diagrams
> below as historical, not current.

RezusCloud manages multiple independent Kubernetes clusters. Each tenant cluster runs its own etcd, API server, and kubelet on dedicated machines. No shared control plane infrastructure.

## Architecture

```
RezusCloud Management Plane
├── REST API (/api/v1/*)
├── WebUI (dashboard)
├── SideroLink Server (machine config delivery)
├── Provider gRPC (machine provisioning)
├── State Store (SQLite / PostgreSQL)
│
├── Tenant "personal"
│   ├── Machine 1 → control plane (etcd + apiserver + scheduler + cm)
│   ├── Machine 2 → control plane (HA)
│   └── Machine 3 → worker
│
└── Tenant "work"
    ├── Machine 1 → control plane
    └── Machine 2 → worker

        ▲ gRPC (outbound)        ▲ SideroLink (outbound)
        │                        │
┌───────┴──────────┐   ┌────────┴───────────┐
│ Provider (anywhere)│   │ Worker Nodes        │
│ hetzner,aws,oci,  │   │ provisioned by      │
│ metal,static...   │   │ providers, running  │
└───────────────────┘   │ Talos Linux         │
                        └─────────────────────┘
```

## Provisioning Flow

When a tenant is created with a node group:

1. RezusCloud dispatches to the connected provider to provision machines
2. Provider creates VMs/servers with Talos images (SideroLink kernel args baked in)
3. Machines boot, connect to SideroLink server in management plane
4. Config Provider generates Talos config (init, controlplane, or worker type)
5. Config pushed over WireGuard tunnel
6. Machine applies config, joins the tenant cluster

### Provider Catalog

| Provider | Type | How it provisions |
|---|---|---|
| `provider-hetzner` | Dynamic | hcloud API → creates VMs |
| `provider-aws` | Dynamic | AWS API → EC2 instances |
| `provider-oci` | Dynamic | OCI API → compute instances |
| `provider-metal` | Static | PXE + Redfish/IPMI → boots servers |
| `provider-static` | Static | User provides IPs |

Providers connect outbound to the management cluster. They work behind NAT, IPv6-only, and CGNAT.

## Tenant Lifecycle

### Create a Tenant

```bash
rezusctl create cluster personal
```

### List Tenants

```bash
rezusctl get clusters
```

### Get Tenant Credentials

```bash
rezusctl kubeconfig personal > personal.yaml
export KUBECONFIG=personal.yaml
kubectl get nodes
```

### Delete a Tenant

```bash
rezusctl cluster delete personal
```

The controller dispatches `Destroy` to the provider, which tears down VMs.

## Node Roles

NodeGroup role labels determine Talos config type:

| Role | First machine | Additional machines |
|------|--------------|-------------------|
| `controlplane` | `init` (bootstraps etcd) | `controlplane` (joins existing etcd) |
| `worker` | `worker` | `worker` |

## Certificate Management

RezusCloud generates and stores:
- Cluster CA, API server certs, etcd certs, service account keys
- Certificates stored encrypted in the state store
- Pushed to machines via SideroLink

## Connectivity Modes

| Mode | When | How |
|---|---|---|
| WireGuard/IPv6 (direct) | IPv6 available | Direct connection to tenant API server |
| Konnectivity (reverse tunnel) | NAT/IPv4-only | Workers connect outbound to proxy |

Selection is automatic at join time.

## Scaling

| Dimension | Approach |
|---|---|
| More tenants | Create additional tenant via CLI or API |
| Tenant HA | 3+ control plane machines |
| Management HA | 3+ management cluster nodes |
| Workers per tenant | No limit — workers join via connectivity layer |
| New infrastructure | Start a new provider binary |

## See Also

- [ADR 12: Provider-Based Machine Provisioning](../adr/0012-provider-based-machine-provisioning.md)
- [ADR 13: SideroLink-Based Config Pull](../adr/0013-siderolink-config-pull.md)
- [ADR 14: Full Talos Cluster Lifecycle](../adr/0014-full-talos-cluster-lifecycle.md)
- [Architecture](../concepts/architecture.md)
- [CLI Reference](../reference/cli.md)
