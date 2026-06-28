# Architecture Decision Records

Decisions that shape RezusCloud's architecture. Each ADR records the context, decision, and consequences of one significant choice. Superseded ADRs are kept for history.

## The TF-State Architecture Pivot (2026-06)

The most recent ADRs record a major architectural pivot: RezusCloud moves from bespoke gRPC providers + SideroLink to **OpenTofu as the execution engine, with TF state as the single source of truth**. This brings a first-class IaC path, reuses the proven `talos-iac` TF tooling, and keeps RezusCloud self-contained.

| ADR | Title | Status |
|-----|-------|--------|
| [0022](0022-exec-tofu-binary.md) | Exec the Tofu Binary for Reconciliation | Accepted |
| [0021](0021-tf-state-single-source-of-truth.md) | TF State as Single Source of Truth (Two Data Planes) | Accepted |
| [0020](0020-management-connectivity.md) | Management Connectivity — IPv6 Direct (v1), WireGuard Mesh (v2) | Accepted |
| [0013](0013-siderolink-config-pull.md) | Config Delivery via user_data and Talos API (SideroLink Rejected) | Accepted (supersedes original) |
| [0012](0012-provider-based-machine-provisioning.md) | Provider-Based Machine Provisioning | Superseded by TF-state model |
| [0014](0014-full-talos-cluster-lifecycle.md) | Full Talos Cluster Lifecycle | Accepted (transport corrected) |

## Foundation

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-crd-based-reconciliation.md) | CRD-Based Reconciliation | Accepted |
| [0003](0003-cilium-only-v1.md) | Cilium Only for v1 CNI | Accepted |
| [0004](0004-event-driven-boot-nats.md) | Event-Driven Boot (NATS) | Accepted |
| [0006](0006-docker-first-platform.md) | Docker-First Platform | Accepted |
| [0009](0009-kubernetes-native-api.md) | Kubernetes-Native API | Accepted |

## Product & UX

| ADR | Title | Status |
|-----|-------|--------|
| [0015](0015-frontend-templ-htmx.md) | Frontend: templ + HTMX | Accepted |
| [0016](0016-auth-jwt-only.md) | Auth Scope — Local JWT Users and API Tokens | Accepted |
| [0017](0017-dropped-enterprise-features.md) | Dropped Enterprise Features | Accepted |
| [0018](0018-audit-log-middleware.md) | Audit Log Middleware | Accepted |
| [0019](0019-configpatch-single-scope.md) | ConfigPatch Single Scope | Accepted |

## Numbering

ADRs are numbered sequentially. Gaps (0002, 0005, 0007, 0008, 0010, 0011) indicate decisions that were merged, withdrawn, or renumbered during drafting.
