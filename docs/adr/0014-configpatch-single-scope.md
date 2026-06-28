# ADR 0014: ConfigPatch — Single Tenant-Wide Scope

## Status

Accepted

## Context

A ConfigPatch is a user-defined Talos config overlay applied during config
generation. The rejected alternative — three scopes (Cluster, MachineSet,
ClusterMachine) with merge ordering — exists for large fleets with multiple
machine sets per cluster. RezusCloud is a personal cloud; the typical tenant
has one or two node groups. See
[`../architecture-history/`](../architecture-history/README.md) for the
original three-scope proposal.

## Decision

**ConfigPatches have one scope: tenant-wide.** A `targetRole` filter
(`controlplane`, `worker`, or empty for all roles) is the only secondary
dimension.

- Patches are labelled `rezuscloud.io/tenant=<name>`.
- `ResolvePatches(tenant, role)` lists the tenant's enabled patches whose
  `targetRole` matches (or is empty).
- No MachineSet scope, no ClusterMachine scope, no cross-scope merge ordering.

### What this means for the operator

- Every patch is "something I want to apply to this cluster, optionally
  filtered by role."
- Per-node-group overrides within the same role are not supported. If truly
  needed, the operator edits Talos config on the node directly (outside the
  platform).
- The schema extends cleanly to a future `nodeGroupSelector` field if demand
  appears — additive, no architecture change.

## Consequences

- **One merge layer, no conflict resolution logic.**
- **Simple WebUI editor** — one form, no scope selector.
- **`ResolvePatches` is small** (~20 lines).

## See Also

- [ADR 0006](0006-exec-tofu-binary.md) — config generation happens during
  `tofu apply`
- [ADR 0011](0011-webui-templ-htmx.md) — the patch editor UI
