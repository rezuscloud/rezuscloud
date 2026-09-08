# ADR 0018: Continuous Management Link — SideroLink (Tunnel + Config-Pull)

## Status

Accepted (implementation [planned #193](https://github.com/rezuscloud/rezuscloud/issues/193))

## Context

rezuscloud must reach every managed node for config delivery
([ADR 0008](0008-config-delivery-user-data-and-talos-api.md)), upgrades, status
probes ([ADR 0010](0010-status-plane-best-effort.md) /
[0016](0016-status-plane-on-demand-probe.md)), and logs. The prior model assumed
**direct reachability** — cloud VMs via their public/endpoint IP, bare metal via
IPv6 or the same LAN (the "IPv6 direct" model, archived
[ADR 0020](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0020-management-connectivity.md)
v1). A personal cloud spans NAT'd networks — home, multi-site, edge. A node behind
NAT that rezuscloud cannot dial inbound is **unmanageable**: it cannot be
configured, probed, upgraded, or even declared unhealthy.

Once reachability must be **node-initiated** (to traverse NAT), the efficient
design is for that single node-initiated channel to carry **everything**: the node
pulls its config from the platform over it, and the platform pulls telemetry from
the node on demand over it. Both directions are pull; nothing is continuously
pushed. This matches the reconcile pattern of the lower layers (Kubernetes nodes
pull; Talos reconciles) and avoids the platform becoming a continuous pusher.

**SideroLink** is Talos's native realization of exactly this: a node-initiated
WireGuard-over-gRPC tunnel that doubles as the config-pull channel. It was rejected
earlier (archived
[ADR 0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md))
on the grounds that the deployment did not need a tunnel and config-push sufficed.
Both premises no longer hold: the fleet is NAT-spanning, and **pull** is now the
chosen delivery model ([ADR 0008](0008-config-delivery-user-data-and-talos-api.md),
reversed). This ADR adopts SideroLink.

## Decision

**rezuscloud is the management node. It runs an embedded SideroLink server, and
every node maintains a continuous, node-initiated SideroLink tunnel to it.**

- Each node dials **outbound** to rezuscloud's SideroLink endpoint (node-initiated
  → traverses NAT/CGNAT).
- **STUN** discovers rezuscloud's public endpoint when rezuscloud itself sits behind
  home NAT; an in-binary **relay (TURN-style)** is the fallback for symmetric NAT.
- The tunnel is **persistent** (WireGuard keepalives) — a continuous link.
  rezuscloud always knows peer liveness (up/down) without probing.
- The tunnel carries, bidirectionally:
  - **node → platform (pull by platform, on demand)** — status, logs, dmesg, events,
    fetched via the Talos API over the tunnel when requested
    ([ADR 0010](0010-status-plane-best-effort.md) / [0016](0016-status-plane-on-demand-probe.md)).
  - **platform → node (pull by node)** — the node's Talos config, pulled over
    SideroLink ([ADR 0008](0008-config-delivery-user-data-and-talos-api.md)).

## Implementation (amendment, #193)

**The server is implemented from scratch in `internal/mgmtlink`, speaking the
stock node contract. The node side is untouched stock Talos.**

Rationale (the decomposition that made this tractable): SideroLink is three
things — a one-RPC provisioning contract (`sidero.link.ProvisionService`), a
plain WireGuard data plane, and what rides it. Only the server is ours to
build; the contract is ~40 lines of proto.

- **Wire contract**: the generated `siderolink/api` protobuf package is used
  as a dependency (not vendored) — it is the node's API stub; duplicating it
  would risk descriptor drift for zero benefit.
- **Transport**: hand-written — a `wireguard-go` `conn.Bind` routing kernel-
  mode nodes over UDP and tunnel-mode nodes over per-peer queues served by a
  from-scratch `WireGuardOverGRPCService` (metadata header
  `x-siderolink-ipv6-addr`, single-use peer slots, replacement semantics).
  Interop with the stock node stack is proven by tests that drive the
  upstream client bind + relay against this server (real WG handshake, real
  TCP through the tunnel).
- **Data plane termination**: `wireguard-go` over an in-process gVisor
  netstack — no kernel TUN, no host privileges, CGO-free; management code
  dials node ULAs through `Server.DialContext`.
- **Addressing**: one /64 per node carved from a configured ULA prefix
  (`REZUSCLOUD_MGMTLINK_PREFIX`, default /48 → 65536 nodes); allocations are
  keyed by node identity (machine UUID, unique-token fallback) and persist
  in SQLite, as does the server WG key (node-visible identity survives
  restarts).
- **Enrollment**: single join token (`REZUSCLOUD_MGMTLINK_JOIN_TOKEN`);
  token-checked at provision, stream addresses validated against the
  allocation.
- **Deferred deliberately**: STUN endpoint discovery (an optimization — the
  node-initiated gRPC tunnel already traverses NAT, incl. symmetric; UDP is
  the fast path when reachable) and the log/event sink services (status
  stays on-demand pull per ADR 0010/0016). File separately when needed.

### What this is, precisely

- **Config delivery is pull** over this tunnel
  ([ADR 0008](0008-config-delivery-user-data-and-talos-api.md)) — **not** push.
- **Telemetry is on-demand pull** over this tunnel
  ([ADR 0010](0010-status-plane-best-effort.md) / [0016](0016-status-plane-on-demand-probe.md))
  — **not** continuous-push. rezuscloud deliberately does **not** adopt Omni's
  always-on log/event streaming; for a personal cloud with mostly-idle nodes,
  continuous-push telemetry is wasteful.
- **One node-initiated tunnel, pull in both directions, push nowhere.**

### Self-contained

Honors [ADR 0001](0001-what-rezuscloud-is.md): the SideroLink server, STUN client,
and relay live in the `rezuscloud` binary — no external mesh/Headscale/Tailscale.
**No custom Talos images** — SideroLink is a Talos kernel feature activated by a
kernel arg in the bootstrap config.

### Cold-boot

A never-configured NAT'd node cannot pull (no tunnel yet). Its **bootstrap** — the
minimal `siderolink.api=` kernel arg + WireGuard key — is delivered once via
`user_data` (cloud) or boot image (bare metal). After that, everything flows over
the tunnel. The tunnel solves ongoing reachability and delivery; only the bootstrap
is pushed.

## Consequences

- rezuscloud gains an embedded SideroLink server (WireGuard + gRPC config-pull) +
  STUN client + optional relay. This is the cost the prior ADRs avoided; it is now
  accepted because the NAT-spanning pull model requires it.
- **NAT'd nodes become fully manageable** — reachable, config-pulled, probeable.
- Direct-dial code is replaced by tunnel-dial for managed nodes.
- Reverses archived
  [ADR 0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md)
  (SideroLink rejection) — justified by the NAT-spanning fleet and the pull decision.
- Tenant nodes still need to reach **each other** (etcd peers, kube lookups) →
  embedded discovery ([ADR 0019](0019-embedded-cluster-discovery.md)).
- Operators must expose rezuscloud's SideroLink endpoint (port-forward, public IP,
  or reliance on STUN/relay).

## See Also

- [ADR 0008](0008-config-delivery-user-data-and-talos-api.md) — config delivery is pull over this tunnel
- [ADR 0010](0010-status-plane-best-effort.md) / [0016](0016-status-plane-on-demand-probe.md) — telemetry is on-demand pull over this tunnel
- [ADR 0019](0019-embedded-cluster-discovery.md) — embedded discovery, depends on this
- [ADR 0001](0001-what-rezuscloud-is.md) — self-contained principle
- archived [0013](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md) — prior SideroLink rejection, reversed here
- archived [0020](../architecture-history/superseded-adrs/_archive-2026-05-2026-06/0020-management-connectivity.md) — connectivity history
