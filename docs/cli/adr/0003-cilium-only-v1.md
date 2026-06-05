# ADR 3: Cilium as the Only CNI for v1

## Status: Accepted

## Context

The platform needs a CNI provider. Multiple CNIs were considered: Cilium, Calico, Flannel, and others. The interface is designed for pluggability, but v1 must ship with one implementation.

## Decision

Ship Cilium as the only CNI implementation. Cilium is mandatory on all tenant clusters. On the management cluster, Cilium is used where feasible (Docker with privileged containers = feasible).

## Consequences

- Cilium provides everything needed: eBPF data plane, Gateway API for ingress, WireGuard for encryption, Geneve/VXLAN overlay.
- Reduced testing surface: one CNI × platforms × edge scenarios.
- The installer is extensible. Adding Calico or Flannel later requires implementing the install interface.
- Management cluster CNI and tenant cluster CNI are completely independent.
