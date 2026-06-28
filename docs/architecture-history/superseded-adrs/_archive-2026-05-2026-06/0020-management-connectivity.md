# ADR 20: Management Connectivity — IPv6 Direct (v1), WireGuard Mesh (v2)

## Status: Accepted

## Context

RezusCloud must reach managed nodes to perform management operations:
- **Collector** scrapes the Kubernetes API, Talos API, and node metrics
- **Bare-metal config push** via `talos_machine_configuration_apply` (node API port 50000)
- **Upgrades** via `talosctl upgrade`
- **Health checks** during reconciliation

This is **connectivity** — reachability between RezusCloud and a node. It is a distinct concern from **config delivery** (how a node first receives its Talos config, addressed in [ADR 13](0013-siderolink-config-pull.md) — SideroLink rejected).

The product constraint that drives this decision: RezusCloud is self-contained — the only component needed to run the personal cloud. A common deployment is a Home Assistant container app running in a home network. RezusCloud holds bootstrap credentials and exec's `tofu` directly ([ADR 12](0012-provider-based-machine-provisioning.md), credential isolation reversed). It is not deployed alongside a Sidero server or an external mesh controller.

### The reachability scenarios

| Scenario | Direct reachability? |
|----------|----------------------|
| RezusCloud ↔ cloud APIs (OCI, OpenStack) | Yes — public endpoints |
| RezusCloud ↔ cloud VMs (managed nodes) | Yes — public cloud IP / endpoint via kubeconfig/talosconfig |
| RezusCloud ↔ bare metal on same LAN (home) | Yes — LAN IPv6 / IPv4 direct |
| RezusCloud ↔ bare metal on remote network behind NAT | **No** — both may be behind NAT |

Only the last scenario needs a mesh.

### What was rejected

**SideroLink** (Talos's WireGuard-over-gRPC config-pull feature) was considered and rejected in [ADR 13](0013-siderolink-config-pull.md). SideroLink solves *config delivery*, not reachability — it does not expose a general management channel. Rejecting SideroLink does not preclude a separate connectivity mesh.

A confusion to explicitly dismiss: "Kubespan" in casual use often connotes "WireGuard mesh with STUN." Talos ships Kubespan natively, but it is a *node-to-node* mesh designed for pod networking when Talos nodes sit on different networks. RezusCloud is not a Talos node and cannot join a Kubespan mesh natively. A future mesh is therefore *Kubespan-inspired*, not literally Kubespan.

## Decision

Two phases.

### v1: IPv6 direct

Every managed node is reachable from RezusCloud via IPv6 (public IPv6 or same-LAN IPv6). This matches the production `talos-iac` deployment today: OCI control plane, OpenStack worker, edge bare metal all on IPv6, Cilium providing native IPv6 pod networking. RezusCloud reaches each node's Kubernetes API (kubeconfig) and Talos API (talosconfig) directly.

- Cloud VMs: reachable via their public/endpoint IP regardless of IP family.
- Bare metal: reachable via IPv6 on the management LAN.

No mesh, no STUN, no relay. Simplest thing that works for the v1 deployment shape.

### v2: WireGuard hub-and-spoke (Kubespan-inspired)

When nodes sit behind NAT on a remote IPv4-only network, RezusCloud runs a WireGuard hub:
- RezusCloud is the hub; each managed node dials out (outbound — NAT-friendly).
- RezusCloud receives a virtual IP per node.
- Bare-metal Talos configs include a WireGuard peer block pointing at RezusCloud's endpoint.
- **STUN** discovers RezusCloud's public endpoint when RezusCloud itself runs behind home NAT.
- **Relay fallback** (TURN-style) for symmetric NAT topologies where direct peer-to-peer fails.

The mesh provides reachability only. Config still arrives via `user_data` (cloud VMs) or Talos API push (bare metal). The mesh is not a config channel.

The mesh reuses Talos's native WireGuard config documents — RezusCloud generates the peer block, the node applies it as part of its normal config.

### Cold-boot caveat

A node behind NAT that has *never* been configured presents a chicken-and-egg: RezusCloud cannot push to it (can't reach), and it cannot pull config (SideroLink rejected). The node must obtain its *first* config via some reachable path:
- IPv6 (if available),
- temporary local access on the node's LAN, or
- a manual step (USB boot into maintenance mode on the RezusCloud LAN).

Once first-config enables the mesh peer, ongoing management flows over the mesh. The mesh solves ongoing reachability, not cold boot. This caveat is documented for v2 operators.

## Consequences

- **v1 is IPv6-only for bare metal** — operators must have IPv6 to the management LAN, or attach new nodes to RezusCloud's LAN temporarily. Matches `talos-iac`.
- **v2 adds a WireGuard hub in RezusCloud** — the binary gains a STUN client and (optionally) a relay. Self-contained; no external Headscale/Tailscale dependency.
- **RezusCloud is not a Talos node** — it does not join a real Kubespan mesh. The v2 mesh is its own WireGuard surface driven by generated Talos peer blocks.
- **Config delivery is unchanged** — `user_data` + Talos API push remain the only config mechanisms regardless of connectivity phase.
- **No SideroLink** — confirmed across both phases.

## See Also

- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md) — Credential isolation reversed; RezusCloud holds bootstrap creds
- [ADR 13: SideroLink-Based Config Pull](0013-siderolink-config-pull.md) — SideroLink rejected; config delivery via user_data + Talos API
