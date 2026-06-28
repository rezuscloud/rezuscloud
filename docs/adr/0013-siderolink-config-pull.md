# ADR 13: Config Delivery via user_data and Talos API (SideroLink Rejected)

## Status: Accepted

## Supersedes

- **ADR 13 (original, 2026-05-xx)** — SideroLink-Based Config Pull for Machine Bootstrap. **Rejected.**

## Context

The original ADR 13 proposed **SideroLink** — a Sidero Labs Talos kernel feature that creates a WireGuard-over-gRPC tunnel between a Talos machine and a management server — as the universal config delivery mechanism for RezusCloud. Machines would boot a minimal Talos image with `siderolink.api=` kernel args, connect outbound to RezusCloud, and pull their full config.

### Why the original proposal existed

It solved three problems:
1. **Config delivery to bare metal** without a cloud metadata service (no user_data available)
2. **NAT/CGNAT traversal** — outbound-only connection from machines behind restrictive firewalls
3. **Uniform flow** — every machine (cloud VM or bare metal) receives config the same way

### What we learned from the production cluster

The real `talos-iac` deployment — which RezusCloud must faithfully reproduce — uses **no SideroLink at all**:

| Provider | Config delivery method | Source |
|----------|------------------------|--------|
| OCI VMs | `user_data` in instance metadata (base64 Talos config) | `modules/oci-cluster/instances.tf` |
| OpenStack VMs | `user_data` via `config_drive = true` | `modules/openstack-cluster/main.tf` |
| Edge bare metal | `talos_machine_configuration_apply` (TF pushes to node API port 50000) | `modules/edge-baremetal/main.tf` |

Two distinct mechanisms cover every machine type in production:

1. **user_data (cloud-init)** — Cloud VMs. The cloud platform delivers the config at VM creation. Zero-touch: RezusCloud generates the config during `tofu apply`, passes it as `user_data`, the VM boots Talos already configured. No management plane dependency, no outbound connection needed.

2. **Talos API push (`talosctl apply-config` / `talos_machine_configuration_apply`)** — Bare metal. The node is pre-booted into Talos maintenance mode (from USB ISO or PXE), RezusCloud/TF pushes the full config to the node's API on port 50000. Requires the node to be network-reachable.

### Why SideroLink is not needed

- **Cloud VMs (95%+ of machines)**: user_data already solves config delivery. SideroLink would add a redundant outbound dependency — the machine would have to reach RezusCloud before configuring itself, when the cloud platform already delivered the config.
- **Bare metal**: Terraform's Talos provider (`talos_machine_configuration_apply`) already manages config delivery to maintenance-mode nodes successfully. The only friction is the one-time USB boot into maintenance mode, which is a provisioning step, not a config-delivery problem.
- **NAT/CGNAT concern**: The bare metal nodes in production are reachable via IPv6 directly. Cilium provides native IPv6 pod networking. No outbound-tunnel workaround is required.

### Cost of adopting SideroLink

- Embedding a SideroLink server in the `rezuscloud` binary (gRPC server, WireGuard peer management)
- Building a custom Talos image per management cluster with `siderolink.api=` kernel args baked in
- A schematic dependency on the SideroLink configuration
- Join-token-to-tenant mapping infrastructure
- A network dependency where none exists today (cloud VMs would need to reach RezusCloud before booting)

All of this to solve a problem (config delivery to unreachable machines) that the production cluster does not have.

## Decision

**SideroLink is rejected. RezusCloud uses two config delivery mechanisms, both already proven in `talos-iac`:**

| Machine type | Config delivery | Provider responsibility |
|--------------|----------------|------------------------|
| Cloud VM (OCI, OpenStack, Hetzner, AWS) | `user_data` at VM creation | TF Talos provider generates config → passed as instance metadata / config_drive / cloud-init |
| Bare metal (pre-booted, maintenance mode) | `talos_machine_configuration_apply` (push to node API) | TF Talos provider pushes config to node's API endpoint (port 50000) |

### Provider interface

Providers (TF provider plugins) only create/delete machines and pass through the RezusCloud-generated config as `user_data`. Config generation is RezusCloud's responsibility (via the TF Talos provider during `tofu apply`).

```go
// Conceptual provider responsibility (manifests as TF provider resources):
//   - Create/delete cloud instances or bare metal registrations
//   - Accept user_data (the Talos config) from RezusCloud
//   - Report instance IPs back via TF state
```

### Bare metal one-time boot

Bare metal nodes require a one-time manual boot into Talos maintenance mode (USB ISO or PXE). After that, RezusCloud/TF reaches the node's API and pushes config. This is a provisioning step documented per provider, not a RezusCloud core feature.

## Consequences

- **No SideroLink server** in the `rezuscloud` binary. Simpler component topology.
- **No custom Talos image** required for SideroLink kernel args. Standard Image Factory images suffice.
- **No outbound management-plane dependency** for cloud VMs during boot. Cloud VMs boot fully configured.
- **Bare metal requires network reachability** to the node API during config push (IPv6 or direct IPv4). This matches the production cluster.
- **Bare metal requires one-time maintenance-mode boot** (USB/PXE). Documented as a provider prerequisite, not automated by RezusCloud.
- **No join-token-to-tenant mapping via SideroLink**. Tenant assignment is determined by which TF state (tenant) the `tofu apply` runs against — config is generated with the correct cluster secrets baked in.

## Code Cleanup Required

The following existing code references SideroLink and must be removed or rewritten (tracked separately):

- `internal/provider/metal/` (config.go, config.example.yaml, config_test.go) — metal provider SideroLink config
- `cmd/provider-metal/main.go` — metal provider binary
- `internal/web/handlers/machines/` — join token flow
- `internal/web/handlers/settings/` — SideroLink endpoint config
- `internal/web/pages/*.templ` — UI references to SideroLink and join tokens
- `docs/getting-started/`, `docs/concepts/architecture.md` — doc references

## See Also

- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md) — Provider model (now TF provider plugins)
- [ADR 14: Full Talos Cluster Lifecycle](0014-full-talos-cluster-lifecycle.md) — Talos config generation
