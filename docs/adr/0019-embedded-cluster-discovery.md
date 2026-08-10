# ADR 0019: Embedded Cluster Discovery Service

## Status

Accepted

## Context

[ADR 0018](0018-continuous-management-link-wireguard.md) gives RezusCloud ↔ node
reachability (the SideroLink tunnel). But a tenant cluster's nodes must also reach
**each other**: etcd peer gossip, kube-internal lookups, kube-proxy backends.
When tenant nodes sit on different networks — the NAT-spanning personal cloud
that motivates [ADR 0018](0018-continuous-management-link-wireguard.md) — they
have no way to discover their peers' reachable addresses. Without that, a tenant
cluster cannot form etcd quorum or wire pod networking, no matter how well
RezusCloud can reach each node individually.

Kubernetes/Talos solves this with a **discovery service**: each cluster has an
ID + token; nodes register themselves and query to learn their peers' addresses
(the cluster-discovery protocol). Talos supports pointing nodes at a discovery
endpoint via a config document.

The default/public discovery service (`discovery.sidero.dev`) is external and
public. For a private cloud that is wrong: it phones home, leaks cluster
existence, and reintroduces an external dependency. [ADR 0001](0001-what-rezuscloud-is.md)
requires RezusCloud to be self-contained — the only component needed to run the
personal cloud.

## Decision

**RezusCloud runs an embedded cluster discovery service.**

- Each tenant is assigned a **cluster ID + token** at creation.
- RezusCloud injects a **config patch** pointing the tenant's nodes at the
  embedded discovery endpoint (the management node's address, reachable over the
  SideroLink tunnel — [ADR 0018](0018-continuous-management-link-wireguard.md)).
- Nodes register their **reachable address** (their SideroLink tunnel address
  from [ADR 0018](0018-continuous-management-link-wireguard.md)) and query the
  service to learn peer addresses.
- A node leaving the platform has its affiliate removed (best-effort cleanup).

### Coordination, not transport

Discovery returns **peer addresses**; the actual node ↔ node traffic uses the
cluster's own data-plane mesh (Talos Kubespan / Cilium) over those addresses.
The SideroLink management tunnel ([ADR 0018](0018-continuous-management-link-wireguard.md))
and the tenant data-plane mesh are **distinct**: RezusCloud is not a Talos node
and does not join a tenant Kubespan (per archived ADR 0020). Discovery only tells
nodes how to find each other.

### Why embedded

- Honors [ADR 0001](0001-what-rezuscloud-is.md) — self-contained, no external or
  public discovery dependency.
- Co-locates discovery with the one component that already holds continuous links
  to every node ([ADR 0018](0018-continuous-management-link-wireguard.md)) — the
  management node is the natural coordination point.

## Consequences

- New in-process service in RezusCloud: a discovery endpoint + an affiliate
  store. Tenant node configs gain a discovery config patch (see
  [ADR 0014](0014-configpatch-single-scope.md) for the patch mechanism).
- **Depends on [ADR 0018](0018-continuous-management-link-wireguard.md):** the
  address a node registers is its SideroLink address. Without the tunnel, NAT'd
  peers' addresses would be unreachable even once discovered.
- Discovery is **best-effort**, like the rest of the status plane
  ([ADR 0010](0010-status-plane-best-effort.md)): stale affiliates are tolerated,
  and the cluster degrades gracefully if the discovery service is briefly
  unavailable (nodes cache their peer list).
- Does **not** replace the tenant's own pod-networking mesh (Cilium/Kubespan); it
  only tells nodes how to find each other.

## See Also

- [ADR 0018](0018-continuous-management-link-wireguard.md) — the management mesh that provides the registered addresses
- [ADR 0001](0001-what-rezuscloud-is.md) — the self-contained principle
- [ADR 0010](0010-status-plane-best-effort.md) — best-effort status, same posture here
- [ADR 0014](0014-configpatch-single-scope.md) — the config-patch mechanism used to point nodes at discovery
