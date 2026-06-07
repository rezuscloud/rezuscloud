# ADR 19: ConfigPatch Scope — Single Tenant-Wide Scope

Status: Accepted (2026-06-02)

## Context

The reference platform (`arch/04-features/configuration-management.md`) specifies **three scopes** for config patches:

| Scope | Applies to |
|---|---|
| `Cluster` | Every machine in the cluster |
| `MachineSet` | Every machine in a specific MachineSet |
| `ClusterMachine` | One specific (cluster, machine) binding |

Patches are merged in order: Cluster → MachineSet → ClusterMachine. Later scopes override earlier ones.

This three-scope model exists because the reference platform supports large fleets with multiple MachineSets per cluster (different node pools for different workloads), and operators may need to override per-machine behaviour (e.g. a specific node's NIC config). The merge ordering is essential to make this composable.

RezusCloud is a personal cloud. The typical deployment is one cluster with one or two NodeGroups (controlplane + worker). Per-machine overrides are rare and can be achieved through Talos machine config patches at the node level if needed.

The current implementation in `internal/api/patch/` has only a `targetRole` filter (one of `controlplane`, `worker`, or empty for "all roles"). This was implemented in PR #36 before this ADR was written; this ADR documents the decision and the deliberate non-addition of MachineSet and ClusterMachine scopes.

## Decision

ConfigPatches have **one scope: tenant-wide** (i.e. apply to all machines in the tenant cluster). The existing `targetRole` filter is the only secondary dimension.

### Schema

```go
type PatchSpec struct {
    Patch      string `json:"patch"`        // raw YAML
    Format     string `json:"format"`       // "strategic" (default) or "json6902"
    TargetRole string `json:"targetRole"`   // "", "controlplane", "worker"
    Enabled    bool   `json:"enabled"`
}
```

Metadata labels:
- `rezuscloud.io/tenant=<name>` — required, scopes patch to one tenant

### Selection logic (`patch.ResolvePatches`)

```go
func ResolvePatches(store, tenant, role string) ([]string, error) {
    // List patches with label rezuscloud.io/tenant=<tenant>
    // Skip disabled
    // Skip patches where targetRole is set and doesn't match role
    // Return remaining patch YAML strings
}
```

### What this replaces from the reference model

| Reference feature | RezusCloud equivalent |
|---|---|
| Cluster scope | Default — every patch is cluster-scoped |
| MachineSet scope (e.g. `nodepool=gpu`) | `targetRole=worker` + a label selector on the NodeGroup (not implemented — see "Future scope" below) |
| ClusterMachine scope (per-machine override) | Not supported. Operator edits Talos config directly on the node if truly needed. |
| Merge ordering across scopes | N/A — no competing scopes |

## Consequences

### What we gain
- One merge layer, no conflict resolution logic.
- Simple mental model: every patch is "things I want to apply to this cluster (filtered by role)".
- WebUI editor only needs one form (no scope selector).
- The `patch.ResolvePatches` helper is ~20 lines.

### What we lose
- Cannot apply a patch only to NodeGroup "gpu-pool" without affecting NodeGroup "default-pool" of the same role.
- Cannot override a cluster-wide patch for a single specific machine.

### Workarounds for unsupported use cases

If a user needs per-NodeGroup patches:

1. **Create separate tenants per node pool** — defeats the purpose of having one cluster.
2. **Use `targetRole` + role labels** — works for controlplane vs worker split, but not finer-grained.
3. **Use Talos machine config patches at the node level** — manual, breaks GitOps flow.
4. **Add a `nodeGroupSelector` field in future** — see "Future scope" below.

If a user needs per-machine patches:

1. **Edit the machine's Talos config directly** — outside the platform.
2. **Wait for the feature** — see "Future scope".

### What we explicitly do not do
- No merge order resolution.
- No conflict detection (last-writer-wins at YAML merge time).
- No dry-run "show me the combined patch" page.

## Future scope

If per-NodeGroup patching becomes necessary, the schema extends cleanly:

```go
type PatchSpec struct {
    // ... existing fields ...
    NodeGroupSelector string `json:"nodeGroupSelector,omitempty"` // label selector, e.g. "pool=gpu"
}
```

`ResolvePatches` then takes a `nodeGroupLabels map[string]string` parameter and includes the patch only if the selector matches. This is additive — existing tenant-wide patches without a selector continue to apply to all matching roles.

Per-machine patches would require a similar addition (`MachineSelector`) plus a per-machine merge step in the Talos config generator. Not planned for v1.

## See Also

- [ADR 14](0014-full-talos-cluster-lifecycle.md) — full Talos config generation, consumes `ResolvePatches` when rendering machine configs.
- [ADR 15](0015-frontend-templ-htmx.md) — WebUI uses simple `<textarea>` editor for patches (no Monaco, no merge preview).
