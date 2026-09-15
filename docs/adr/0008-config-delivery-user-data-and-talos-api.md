# ADR 0008: Config Delivery via SideroLink Config-Pull

## Status

Accepted — **reverses the prior push decision.** Implementation is
[planned #194](https://github.com/rezuscloud/rezuscloud/issues/194); today config
delivery is still push (`user_data` + Talos API). This ADR records the adopted
direction.

## Context

[ADR 0018](0018-continuous-management-link-wireguard.md) decides that rezuscloud
is the management node and every node maintains a continuous, node-initiated
SideroLink tunnel to it. Once that tunnel exists, config delivery has a choice:
the platform can **push** config to the node, or the node can **pull** its config
from the platform.

The prior decision delivered config by **push** — `user_data` at VM creation for
cloud VMs, and `talos_machine_configuration_apply` (Talos API push) for bare
metal. That was optimal when nodes were directly reachable and the platform was
the sole driver. SideroLink itself was rejected earlier (archived
[ADR 0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md))
on the grounds that the deployment did not need a tunnel and push sufficed.

Two premises changed:

1. **The fleet is NAT-spanning** ([ADR 0018](0018-continuous-management-link-wireguard.md))
   — the node-initiated tunnel now exists for reachability, and config can ride it
   in either direction.
2. **Pull is the more efficient, more idiomatic model.** The lower layers
   rezuscloud orchestrates are themselves pull/reconcile systems: Kubernetes nodes
   pull desired state from the API server and converge; Talos reconciles toward
   config. A platform that *imperatively pushes* config to each node fights that
   grain and must schedule per-node pushes. A node that *pulls* its declared config
   and converges is simpler, NAT-robust, and matches the reconcile pattern.
   Telemetry is the mirror image ([ADR 0010](0010-status-plane-best-effort.md) /
   [0016](0016-status-plane-on-demand-probe.md)): on-demand *pull*, never
   continuous-push. **Both directions are pull; nothing is continuously pushed.**

## Decision

**Config delivery is pull.** rezuscloud generates each node's Talos config (via the
TF Talos provider during `tofu apply`, version-aware — unchanged) and **serves**
it; the node **pulls** its config over the SideroLink tunnel
([ADR 0018](0018-continuous-management-link-wireguard.md)) when it connects.

- **Generation unchanged.** The TF Talos provider still renders the full,
  tenant-specific config (with cluster secrets), as today.
- **Delivery reversed: push → pull.** Instead of `tofu apply` pushing config to the
  node, rezuscloud holds the generated config keyed by machine identity and the node
  pulls it over SideroLink.
- **Tenant assignment unchanged.** Still determined by which TF state the apply
  targets. When a node dials in, its WireGuard key (from its bootstrap config) maps
  to the TF-created machine record → that record's tenant → the served config.

### Bootstrap (the one unavoidable push)

A node cannot pull the config that enables the pull channel — chicken-and-egg. The
*bootstrap* is a one-time push of a **minimal** config containing only the
SideroLink endpoint (`siderolink.api=` kernel arg) and the node's WireGuard key
(for tunnel auth + identity mapping):

| Node type | Bootstrap delivery | Then |
|---|---|---|
| Cloud VM | `user_data` at VM creation (tiny) | pulls full config after boot |
| Bare metal | boot image / maintenance-mode apply | pulls full config after connecting |

Only the bootstrap is pushed; everything else is pull.

### JoinToken — stays deprecated

rezuscloud needs no join token. SideroLink authenticates the peer by **WireGuard
key**; rezuscloud maps that key to the TF-created machine record (which already
lives in a specific tenant's state). TF pre-registration establishes the
node → tenant mapping, so there is nothing for a join token to do. The deprecated
`JoinToken` resource remains slated for removal. (The canonical Omni/SideroLink
model uses join tokens to map ad-hoc booting nodes to clusters; rezuscloud's
declare-first / TF model does not need them.)

## Implementation (amendment, #194)

Two facts about the **stock** node reshape the mechanism (verified against the
Talos fork source):

1. **The node's SideroLink WG key is runtime-generated** (`siderolink`
   controller, `wgtypes.GeneratePrivateKey()`) — it is not in the machine
   config, secrets bundle, or TF state. Nothing declarative can know it
   before first boot, so "keyed by the machine's WireGuard key" above is not
   implementable with a stock node. The identity a stock node presents at
   provision is: `node_uuid` (hardware UUID), the kernel-arg join token, and
   `node_unique_token`.
2. A stock node cannot fetch config itself once running — the config
   AcquireController consults its sources once at boot, before any tunnel
   exists. What a stock node **does** do: an incomplete config drops it into
   **maintenance mode**, where it waits for config while the SideroLink
   controller runs on kernel args.

The implemented mechanism therefore:

- **Machine mapping via a per-machine binding token** — TF generates one per
  machine resource, the bootstrap carries it in `siderolink.api=…?jointoken=`
  kernel args, and the node presents it verbatim in the Provision request.
  rezuscloud maps binding token → TF-created machine record → tenant
  (declare-first, unchanged). This reuses the wire field; the deprecated
  SideroLink *JoinToken resource* (ad-hoc enrollment) is unrelated and stays
  deprecated. (This is the same mapping scheme Omni uses.)
- **Delivery over the tunnel, converged by the platform engine**
  (`internal/converge`): a node's connect triggers rendering via the
  `configrender` pipeline (generation unchanged) and an `ApplyConfiguration`
  (NO_REBOOT) to the node's Talos API (`apid` :1024) through the management
  tunnel — the **maintenance API** (self-signed) for a bootstrapping node,
  the authenticated API (an `os:admin` client certificate minted from the
  tenant secrets bundle) for a configured node. Config changes re-deliver to
  connected machines. The wire is platform→node, but nothing is scheduled:
  the node's own connect is the trigger and re-apply is idempotent — the
  reconcile model this ADR adopts. Delivery never reboots; version-requiring
  transitions belong to the upgrade engine.

## Consequences

- rezuscloud gains a SideroLink config-pull endpoint (part of the SideroLink server
  from [ADR 0018](0018-continuous-management-link-wireguard.md)) that serves
  generated config keyed by machine key.
- The TF Talos provider's push (`talos_machine_configuration_apply`) is **retired**
  for managed nodes — it generates config only. Bare-metal maintenance-mode push
  survives solely for the one-time bootstrap.
- Cloud VMs no longer carry their full config in `user_data` — only the tiny
  bootstrap. Smaller metadata payloads.
- Nodes converge by pulling — operational simplicity, no per-node push
  orchestration, NAT-robust.
- Reverses archived
  [ADR 0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md)
  (SideroLink rejection), justified by the reconcile/efficiency argument and the
  NAT-spanning fleet ([ADR 0018](0018-continuous-management-link-wireguard.md)).

## Historical record (prior push decision, superseded)

Before this ADR, config delivery was push: `user_data` (cloud VMs) and
`talos_machine_configuration_apply` to a maintenance-mode node's API on port 50000
(bare metal), with no SideroLink. That matched a directly-reachable, single-driver
deployment. The full reasoning is preserved in archived
[ADR 0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md).

## See Also

- [ADR 0018](0018-continuous-management-link-wireguard.md) — the SideroLink tunnel
  config delivery rides on
- [ADR 0005](0005-tf-state-single-source-of-truth.md) — TF state, the config source
- [ADR 0010](0010-status-plane-best-effort.md) / [0016](0016-status-plane-on-demand-probe.md) — telemetry is pull too
- archived [0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md) — prior SideroLink rejection, reversed here
- archived [0020](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0020-management-connectivity.md) — connectivity history
