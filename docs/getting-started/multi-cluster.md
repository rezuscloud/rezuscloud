# Multi-Cluster Management

RezusCloud manages multiple independent Kubernetes clusters (tenants). Each
tenant cluster runs its own etcd, API server, and kubelets on dedicated
machines — no shared control plane infrastructure. This guide describes the
topology, provisioning flow, and lifecycle under the TF-state model.

## Architecture

```
RezusCloud Management Plane
├── REST API (/api/v1/*)            — K8s-style resource model (ADR 0003)
├── WebUI (templ + HTMX)            — orchestration console (ADR 0011)
├── Apply Queue                     — debounced, per-tenant (ADR 0006)
├── TF HTTP backend                 — stores tofu state (ADR 0005)
├── TF execution engine (tfexec)    — exec's the `tofu` binary (ADR 0006)
└── State Store (SQLite)            — management-plane data (ADR 0004)
        │
        │ tofu apply (per tenant, debounced)
        ▼
┌──────────────────────────────────────────────┐
│  Real TF providers (registry plugins)        │
│  oci · openstack · talos · bitwarden · …     │
└──────────────────────────────────────────────┘
        │                       │
        ▼                       ▼
┌─────────────────┐   ┌─────────────────────┐
│ Cloud VMs       │   │ Bare metal          │
│ (OCI/OpenStack) │   │ (pre-booted, maint) │
│                 │   │                     │
│ config via      │   │ config via          │
│ user_data       │   │ Talos API push      │
└─────────────────┘   └─────────────────────┘
        │                       │
        └───────────┬───────────┘
                    ▼
        Talos nodes join the tenant cluster
```

RezusCloud drives infrastructure through **off-the-shelf Terraform providers**
spawned by `tofu`. A **Provider** is the RezusCloud-side Go module
(`internal/provider/<name>/`) that wraps a real registry provider — it generates
the `.tf.json` config and declares the TF-resource → RezusCloud-resource
mapping. There are no custom provider-plugin binaries and no SideroLink. See
[ADR 0007](../adr/0007-provider-as-tf-wrapper.md).

## Provisioning Flow

When a tenant is created with a node group, the apply queue debounces the spec
change and runs a single `tofu apply` for the whole tenant:

1. RezusCloud generates the `.tf.json` for the tenant's node groups (cloud VMs,
   bare-metal `talos_machine_configuration_apply` targets).
2. `tofu apply` provisions cloud VMs / pushes config to bare metal.
3. **Cloud VMs** boot fully configured via `user_data` (cluster secrets baked
   in). **Bare metal** receives config via the Talos API push.
4. Nodes join the tenant cluster (etcd bootstraps on the first control-plane
   node).
5. `tofu` writes the resulting state back to RezusCloud's TF HTTP backend —
   that state is the source of truth for the declared infrastructure.

### Provider Catalog

| Provider | Type | How it provisions |
|---|---|---|
| `provider-oci` | Dynamic (cloud) | OCI Compute instances with `user_data` |
| `provider-openstack` | Dynamic (cloud) | OpenStack Nova instances, `config_drive` user_data |
| `provider-metal` | Static (bare metal) | `talos_machine_configuration_apply` to maintenance-mode nodes |

Each is a RezusCloud-side module, not a deployed process. Adding a platform
means writing a module under `internal/provider/<name>/`, not shipping a new
binary.

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

Deletion is finalizer-controlled (ADR 0003): DELETE sets a `deletionTimestamp`
and finalizers block removal until the future `TenantReconciler` runs
`tofu destroy` and clears them.

## Node Roles

NodeGroup role labels determine Talos config type:

| Role | First machine | Additional machines |
|------|--------------|-------------------|
| `controlplane` | `init` (bootstraps etcd) | `controlplane` (joins existing etcd) |
| `worker` | `worker` | `worker` |

## Two Data Planes

| Plane | Holds | Source | Authority |
|---|---|---|---|
| **Spec** | Declared infrastructure (tenant, node group, machine identity) | TF state | Authoritative — [ADR 0005](../adr/0005-tf-state-single-source-of-truth.md) |
| **Status** | Observed runtime state (node health, machine stage) | Best-effort observation | Never authoritative, never written to TF state — [ADR 0010](../adr/0010-status-plane-best-effort.md) |

Status is read-only from the operator's perspective: you declare `spec`, the
system observes `status`. The mechanism that populates status (live scrape,
on-demand probe, etc.) is deferred to a later phase.

## Scaling

| Dimension | Approach |
|---|---|
| More tenants | Create additional tenant via CLI or API |
| Tenant HA | 3+ control-plane machines |
| Management HA | 3+ management cluster nodes |
| Workers per tenant | Scale node-group count; the apply queue reconciles |
| New infrastructure platform | Write a provider module under `internal/provider/<name>/` |

## See Also

- [ADR 0005: TF State as Source of Truth](../adr/0005-tf-state-single-source-of-truth.md)
- [ADR 0006: Exec the Tofu Binary](../adr/0006-exec-tofu-binary.md)
- [ADR 0007: Provider as TF Wrapper](../adr/0007-provider-as-tf-wrapper.md)
- [ADR 0008: Config Delivery](../adr/0008-config-delivery-user-data-and-talos-api.md)
- [Architecture](../concepts/architecture.md)
- [CLI Reference](../reference/cli.md)
