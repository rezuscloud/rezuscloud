# ADR 14: Full Talos Cluster Lifecycle

## Status: Accepted

**Amends**: [ADR 12](0012-provider-based-machine-provisioning.md) — Controller generates full control plane configs.
**Amends**: [ADR 13](0013-siderolink-config-pull.md) — Config generation covers control plane nodes too.

## Context

RezusCloud needs multi-cluster management. The management plane generates complete Talos configs for all node types — init, controlplane, and worker. Each tenant cluster has dedicated machines running their own etcd and API server.

```
RezusCloud Management Plane
└── Provisions full Talos clusters on machines:
    ├── Machine 1 → control plane (etcd + apiserver + scheduler + controller-manager)
    ├── Machine 2 → control plane (HA)
    ├── Machine 3 → worker
    └── Machine 4 → worker
```

### Why Full Lifecycle

1. **Full cluster ownership** — RezusCloud owns the entire lifecycle from boot to destroy, including control plane.
2. **Simpler mental model** — "machines run Talos, Talos runs K8s." No hosted control plane abstraction.
3. **No external controller dependency** — No complex dependency with its own etcd management, certificate rotation, and upgrade path.
4. **No split-PKI** — Machines run standard Talos with native `trustd`. No sidecar CSR signers.
5. **Resilient** — Tenant control planes survive management plane outages (they run on their own machines).
6. **True multi-cloud** — Control planes run on the user's machines, not concentrated in the management cluster.

## Decision

RezusCloud generates **full Talos machine configs** for:
- **Control plane nodes**: etcd + kube-apiserver + kube-controller-manager + kube-scheduler
- **Worker nodes**: kubelet joining the cluster's API server endpoint

The management plane is responsible for:
1. Generating cluster CAs and certificates
2. Generating Talos configs for control plane and worker nodes
3. Pushing configs over SideroLink
4. Monitoring cluster health
5. Orchestrating upgrades (Talos + Kubernetes)

### Cluster Bootstrap Flow

```
1. User creates tenant: POST /api/v1/tenants {name: "personal", kubernetesVersion: "1.35.0"}
2. RezusCloud generates cluster CA + certificates + bootstrap token
3. User allocates machine to tenant as control plane
4. RezusCloud generates full Talos control plane config (init type)
5. Config pushed over SideroLink → machine applies → etcd bootstraps → API server starts
6. Additional control plane machines get join config (controlplane type)
7. Worker machines get worker config (worker type) → join via bootstrap token
8. Cluster is running — RezusCloud monitors via SideroLink + Kubernetes API
```

### Control Plane Scaling

| Scale | Config |
|-------|--------|
| 1 node | `init` type — single-node control plane, etcd embedded |
| 3 nodes | 1 `init` + 2 `controlplane` — HA etcd, HA API server |
| 5+ nodes | Same pattern, user controls how many are control plane vs worker |

### Certificate Management

RezusCloud generates and stores:
- Cluster CA (root CA for the tenant cluster)
- API server serving certificate
- Etcd server/client certificates
- Front-proxy CA and certificate
- Service account key pair
- Bootstrap token for worker joins

Certificates are stored encrypted in the management plane's state and pushed to machines via SideroLink.

## Consequences

### Positive

- **Full ownership** — No external control plane dependency.
- **Simpler** — One fewer component to deploy and manage.
- **Resilient** — Tenant clusters survive management plane outages.
- **Standard Talos** — No special patches or integrations needed.

### Negative

- **More config generation** — Must generate full control plane configs (etcd, certs, API server).
- **Minimum 1 machine per cluster** — Even a single-node cluster needs a real machine.
- **Certificate lifecycle** — RezusCloud must manage certificate rotation for tenant clusters.
- **etcd management** — RezusCloud must handle etcd cluster formation, scaling, and disaster recovery.

## See Also

- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md)
- [ADR 13: SideroLink-Based Config Pull](0013-siderolink-config-pull.md)
