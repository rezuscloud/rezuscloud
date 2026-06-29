# ADR 0008: Config Delivery via `user_data` and Talos API (No SideroLink)

## Status

Accepted

## Context

A Talos node must receive its machine config *somehow*. The earlier proposal —
SideroLink, Talos's WireGuard-over-gRPC config-pull feature — is rejected (see
[`../architecture-history/`](../architecture-history/README.md)). It solved a
reachability problem the production deployment does not have, at the cost of an
embedded gRPC+WireGuard server, custom Talos images, and a network dependency.

The production `talos-iac` deployment — which RezusCloud must reproduce — uses
**no SideroLink at all**:

| Machine type | Config delivery | Source |
|---|---|---|
| Cloud VM (OCI, OpenStack) | `user_data` at VM creation | TF Talos provider generates config → instance metadata / config_drive |
| Bare metal (pre-booted, maintenance mode) | `talos_machine_configuration_apply` (push to node API port 50000) | TF Talos provider pushes to the node's API endpoint |

Both mechanisms are already proven in `talos-iac`.

## Decision

**Two config delivery mechanisms, matching production:**

| Machine type | Config delivery | Owner |
|---|---|---|
| Cloud VM | `user_data` at creation time | The TF Talos provider, invoked by `tofu apply` (see [ADR 0006](0006-exec-tofu-binary.md)) |
| Bare metal | `talos_machine_configuration_apply` to a maintenance-mode node | The TF Talos provider |

Tenant assignment is determined by **which TF state (tenant) the `tofu apply`
runs against** — config is generated with the correct cluster secrets baked
in. There is no separate join-token-to-tenant mapping.

### Bare-metal one-time boot

Bare-metal nodes require a one-time manual boot into Talos maintenance mode
(USB ISO or PXE). After that, RezusCloud/TF reaches the node's API and pushes
config. This is a provisioning step documented per Provider, not a RezusCloud
core feature.

## Consequences

- **No SideroLink server in the `rezuscloud` binary.** Simpler component
  topology.
- **No custom Talos image** required for SideroLink kernel args. Standard
  Image Factory images suffice.
- **No outbound management-plane dependency** for cloud VMs during boot — they
  boot fully configured via `user_data`.
- **Bare metal requires network reachability** to the node API during config
  push (IPv6 or direct). This matches production.
- **No JoinToken concept.** The earlier JoinToken resource (which mapped a
  booting machine to a tenant via SideroLink kernel args) has no role under
  this model and is deprecated.

## See Also

- [ADR 0006](0006-exec-tofu-binary.md) — the apply that generates and delivers
  config
- [ADR 0007](0007-provider-as-tf-wrapper.md) — the providers that render config
- [`../architecture-history/`](../architecture-history/README.md) — SideroLink
  rejection reasoning
