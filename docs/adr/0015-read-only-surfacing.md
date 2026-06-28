# ADR 0015: Read-Only Surfacing of Lower-Layer State

## Status

Accepted

## Context

[ADR 0001](0001-what-rezuscloud-is.md) establishes that RezusCloud lives one
layer above `talosctl` and `kubectl` and **never duplicates their features**.
This ADR sharpens that rule for the specific case of *surfacing* lower-layer
state in the RezusCloud UI/CLI.

There is genuine tension: a tenant orchestrator is only useful if the operator
can see whether their tenants are healthy, whether an apply succeeded, whether
a node is ready. That is *read* access to lower-layer state. But equally,
RezusCloud must not become a second `kubectl` — providing pod exec, in-cluster
resource editing, or any interactive lower-layer verb would duplicate the
lower layer and violate the layering rule.

## Decision

**Read-only surfacing for orchestration purposes is in scope. Interactive
duplication of lower-layer verbs is not.**

### In scope (read-only surfacing)

- Machine stage / health (is this node up? did it join?).
- Tenant reconciliation status (did the apply succeed? what's the drift?).
- Rolling-upgrade progress (which node is being upgraded, did its health gate
  pass?).
- Log streaming for a machine, surfaced through the management plane, **for
  orchestration visibility** (is the node bootstrapping correctly?).

These are rendered read-only in the WebUI and surfaced via the CLI/REST API.
The operator cannot mutate lower-layer objects through RezusCloud.

### Out of scope (interactive duplication)

- Pod `exec` / shell into a tenant cluster.
- Editing Kubernetes resources inside a tenant (edit a Deployment, scale a
  workload, etc.).
- Anything `kubectl` already does inside the tenant.

For those, the operator uses `rezusctl kubeconfig <tenant>` to obtain a
kubeconfig and runs `kubectl` themselves. RezusCloud hands off downward; it
does not reproduce the lower layer's interactive surface.

## Consequences

- **The WebUI stays an orchestration console, not a Kubernetes console.** No
  pod tables, no workload editors, no in-cluster resource views.
- **`rezusctl logs <machine>` stays** — it surfaces machine-bootstrapping
  logs for orchestration visibility, read-only.
- **`rezusctl kubeconfig <tenant>` stays** — it is a hand-off downward, not a
  duplicated feature.
- **The status plane ([ADR 0010](0010-status-plane-best-effort.md)) is scoped
  to what this ADR permits** — enough to orchestrate, no more.

## See Also

- [ADR 0001](0001-what-rezuscloud-is.md) — the layering rule this sharpens
- [ADR 0010](0010-status-plane-best-effort.md) — the status plane this draws
  from
- [ADR 0011](0011-webui-templ-htmx.md) — the UI that obeys this rule
