# ADR 12: Provider-Based Machine Provisioning

> **Superseded by the TF-state model (see CONTEXT.md Architecture).** Providers are now RezusCloud-side orchestration modules (`internal/provider/<name>/`) that render UI, optionally discover nodes (metal only), and generate standard `.tf.json` config using off-the-shelf TF providers (oci, openstack, talos). **No custom `terraform-provider-rezuscloud-*` plugins exist.** RezusCloud exec's `tofu apply`, which calls the standard providers. Config delivery uses `user_data` (cloud VMs) and the Talos API (bare metal) — see [ADR 13](0013-siderolink-config-pull.md) (SideroLink rejected).
>
> **Credential isolation reversed.** This ADR's central property — "each provider holds own creds locally, management plane never sees cloud keys" — no longer holds. RezusCloud holds bootstrap credentials and exec's tofu directly, because RezusCloud must be the **only component** needed to run the personal cloud (e.g. packaged as a Home Assistant container). See CONTEXT.md "Bootstrap Credentials".

## Status: Accepted (Superseded by TF-state model)

## Context

RezusCloud needs machine provisioning. When a user says "I want 3 nodes on Hetzner," something must create those servers. Three approaches were evaluated:

### Option A: Cluster API (CAPI) Infrastructure Providers

CAPI provides infrastructure providers as Kubernetes controllers.

**Rejected because:**
- Requires a Kubernetes management cluster running **before** any provisioning
- Providers make **inbound** API calls — cannot reach machines behind NAT, IPv6-only residential connections, or CGNAT
- No CAPI provider exists for PXE-booting a server in a basement, Wake-on-LAN, or consumer devices
- The RezusCloud API deliberately hides CAPI — its main benefit (standardized API) is lost while its cost (heavy controllers, tight coupling) is absorbed

### Option B: Direct SDK Calls in Controller (ADR 2/8/10)

Cloud SDK calls from Go code inside the controller.

**Rejected because:**
- Controller becomes coupled to every cloud SDK
- Custom state engine reimplements IaC core (diff, CRUD, topological sort) with less capability
- Each new provider requires code changes inside the management binary
- Cannot handle non-cloud scenarios: PXE boot behind NAT, IPv6-only consumer devices, edge bare metal

### Option C: Pluggable Provider Model (Outbound gRPC)

Each provider is a standalone binary that connects **outbound** to the management cluster via gRPC. The controller dispatches work. Providers are stateless — state lives in the management plane's store.

## Decision

Use pluggable providers (Option C). Each provider is a standalone binary that:

1. Connects **outbound** to the management cluster (works behind NAT, IPv6-only, CGNAT)
2. Registers its capabilities ("I can provision hetzner:fsn1")
3. Receives provisioning requests from the controller
4. Reports machine status back

### Provider Interface

After ADR 13 amendments:

```go
type Provider interface {
    // Provision creates machines for a tenant node group.
    Provision(ctx context.Context, spec NodeGroupSpec) ([]Machine, error)
    // Destroy tears down all machines for a tenant.
    Destroy(ctx context.Context, tenant string) error
}
```

### Provider Catalog

| Provider | Type | How it provisions |
|---|---|---|
| `provider-hetzner` | Dynamic | hcloud API → creates VMs |
| `provider-aws` | Dynamic | AWS API → creates EC2 instances |
| `provider-oci` | Dynamic | OCI API → creates compute instances |
| `provider-equinix` | Dynamic | Equinix Metal API → provisions servers |
| `provider-kubevirt` | Dynamic | K8s API → creates VMs as pods |
| `provider-metal` | Static | PXE + Redfish/IPMI → boots physical servers |
| `provider-static` | Static | User provides IPs, no provisioning |

### Connectivity Model

```
Provider (anywhere)          Management Cluster
┌──────────────────────┐     ┌─────────────────────────────┐
│  provider-hetzner    │     │  RezusCloud Controller      │
│                      │────►│  ┌─────────────────────────┐ │
│  - runs as container │gRPC │  │ Watches Tenant resources│ │
│  - or systemd unit   │     │  │ Dispatches to providers │ │
│  - behind NAT: OK    │     │  │ Updates Machine state   │ │
│  - IPv6-only: OK     │     │  └─────────────────────────┘ │
│  - CGNAT: OK         │     │                               │
│  - residential: OK   │     │  REST API + WebUI            │
└──────────────────────┘     └─────────────────────────────┘
```

Providers connect outbound. The management cluster never needs to reach the provider's network.

### Why Not Crossplane

Crossplane providers live in the management cluster and make **inbound** calls to cloud APIs. RezusCloud providers live anywhere and connect **outbound** to the management cluster. This determines which deployments are physically possible:

| Scenario | Crossplane | RezusCloud |
|----------|-----------|------------|
| Public cloud VM | ✅ Works | ✅ Works |
| Server behind NAT/CGNAT | ❌ Cannot reach target | ✅ Provider runs on same LAN |
| Credential isolation | All creds in management cluster | Each provider holds own creds locally |
| Home/edge/consumer | ❌ Requires inbound access | ✅ Outbound only |

## Consequences

- **Provider binaries are separate from the management binary.** Each provider is its own repo, CI, and release cycle. The management plane never imports a cloud SDK.
- **Any infrastructure is reachable.** Providers behind NAT, IPv6-only, CGNAT, residential — all work because connections are outbound.
- **New providers don't touch the management plane.** Write a provider binary, run it, register it.
- **State is in the management plane's store.** No local state files for infrastructure.
- **Boot progress state retained.** Step tracking still used for management cluster boot.

## See Also

- [ADR 13: SideroLink-Based Config Pull](0013-siderolink-config-pull.md) — Config delivery to machines
- [ADR 14: Full Talos Cluster Lifecycle](0014-full-talos-cluster-lifecycle.md) — Cluster management model
