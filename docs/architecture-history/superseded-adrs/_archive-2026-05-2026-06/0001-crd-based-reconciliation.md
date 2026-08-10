# ADR 1: REST API with Declarative State

## Status: Accepted (Updated)

## Context

RezusCloud needs a state model and API for managing tenant clusters, machines, and providers. The management plane stores desired state and reconciles observed state against it.

## Decision

Use a REST API following the Kubernetes API model (metadata/spec/status three-section pattern) backed by a pluggable state store. SQLite for standalone mode, with future support for PostgreSQL and etcd/CRDs.

Resource types:

| Type | Scope | Purpose |
|------|-------|---------|
| `Tenant` | Cluster-wide | Cluster definition with node groups |
| `NodeGroup` | Tenant-scoped | Machine specifications (role, count) |
| `Machine` | Cluster-wide | Per-machine status from providers |
| `Provider` | Cluster-wide | Machine provisioning adapter |
| `JoinToken` | Tenant-scoped | Machine-to-tenant mapping |
| `ConfigPatch` | Tenant-scoped | Talos config customization |
| `User` | Cluster-wide | Authentication and RBAC |

The controller reconciles: dispatching to providers for machine provisioning, generating Talos configs for joining, and tracking machine stages.

## Consequences

- Single API surface for CLI, WebUI, and programmatic access.
- Structured errors, labels/annotations, finalizers for graceful deletion.
- Pluggable state backend (`REZUSCLOUD_STORE` env var selects SQLite, PostgreSQL, or CRDs).
- Declarative upgrades: change `spec.talosVersion`, controller rolls out machine-by-machine.

## See Also

- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md)
- [ADR 14: Full Talos Cluster Lifecycle](0014-full-talos-cluster-lifecycle.md)
- [API Design Document](https://github.com/rezuscloud/rezuscloud/wiki/concepts/api-design.md)
