# ADR 21: TF State as Single Source of Truth (Two Data Planes)

## Status: Accepted

## Context

RezusCloud must represent two fundamentally different kinds of state for a managed tenant cluster:

1. **Desired/declared infrastructure** — what the operator asked for and what was created: a tenant's Kubernetes/Talos version, a NodeGroup's machine count and provider, a Machine's identity. This is structural, changes on user action, and is the *intent* of the system.

2. **Observed/runtime state** — what the cluster is *actually doing* right now: node health, pod crash loops, etcd quorum, disk/CPU/memory. This is ephemeral, changes constantly, and is the *reality* of the system.

### The previous model

The original design stored both in a single generic `resources` SQLite table with JSON `spec` and `status` columns (see [ADR 9](0009-kubernetes-native-api.md)). Every resource type — Tenant, Machine, NodeGroup, Provider — had its own Go struct. `spec` was the declared intent; `status` was system-observed. The management plane was the sole authority for both columns, and all infrastructure was realized by gRPC provider binaries pushing config (now superseded — see [ADR 12](0012-provider-based-machine-provisioning.md)).

This worked for a small prototype but had three problems as the design matured:

- **No IaC path.** Operators who already manage infrastructure with OpenTofu (as the production `talos-iac` does) had no first-class way in. RezusCloud reinvented infrastructure-as-code with a bespoke provider protocol instead of reusing the proven TF ecosystem.
- **Two sources of truth drift.** Declared infrastructure lived in RezusCloud's SQLite; the same infrastructure, if also managed by tofu elsewhere, would drift. There was no canonical "what should exist."
- **Status and spec conflated.** Mixing observed runtime data into the same store as declared intent made it unclear what was authoritative. A scraped "node is ready" boolean is not the same kind of fact as "NodeGroup count = 3."

### The proven alternative

The production `talos-iac` deployment already manages the entire cluster lifecycle with OpenTofu: OCI instances, OpenStack workers, edge bare metal, Cilium values, the cluster's `talos_machine_secrets` (which holds the cluster CA, etcd certs, API server certs, and `client_configuration`). One `tofu apply` realizes declared infrastructure; one TF state blob captures what was created. This is battle-tested.

The question became: should RezusCloud adopt TF state as the source of truth for declared infrastructure, and if so, where does observed runtime state live?

## Decision

**TF state is the single source of truth for declared infrastructure. Observed runtime state is a separate, ephemeral data plane. The two never mix.**

### Spec plane — TF state is authoritative for desired/declared infrastructure

- **One TF state blob per tenant**, stored in RezusCloud's TF HTTP backend (the protocol tofu speaks natively: GET/POST/DELETE on `/state?ID=<name>`).
- The K8s-style REST API `metadata` + `spec` fields are **projections** of the TF state, mapped through provider-declared resource schemas. The API is a read model over TF state, not a separate store of intent.
- A thin derived index table maps `(tenant, resource_type, resource_name) → (state_version, json_path)` so spec queries don't re-parse the full blob on every request. The index is a cache rebuilt from state — never a second source of truth. (Option C from the design discussion: one authoritative blob plus a derived index.)
- RezusCloud exec's `tofu apply` to reconcile declared state (see [ADR 22](0022-exec-tofu-binary.md)).

### Status plane — observed runtime state is ephemeral, never authoritative

- Runtime data (node health, pod status, etcd quorum, metrics) is **scraped live** by a Collector from each tenant's Kubernetes API + Talos API + node metrics.
- It fills in resource `status` fields and feeds a metrics timeseries. It is **never written back to TF state** and is never treated as source of truth — it may lag, may be stale across restarts, and may be absent if a cluster is unreachable.
- History lives in a SQLite timeseries table + an in-memory ring buffer for hot queries (see CONTEXT.md).

### The rule

For any RezusCloud resource: `metadata` + `spec` come from TF state; `status` comes from the Collector. The two data planes are separated by construction. A scraped observation can never silently override a declared intent, and a declared intent can never be confused for a live measurement.

### What this replaces

- The generic `resources` table's `spec` column is no longer the authority for infrastructure intent — TF state is. (The table may persist for non-infrastructure resources like Users/API tokens, but not for Tenant/NodeGroup/Machine infrastructure state.)
- The separate encrypted `tenant_secrets` bundle is no longer needed for cluster PKI — the `talos_machine_secrets` TF resource already holds it in TF state (encrypted — see "State Encryption" in CONTEXT.md).

## Consequences

- **First-class IaC.** Operators can `tofu apply` with a backend pointing at RezusCloud and it works natively. RezusCloud orchestrates the same proven TF tooling `talos-iac` uses.
- **Single source of truth for spec.** No drift between "what RezusCloud thinks" and "what TF created" — they are the same blob.
- **Status is best-effort.** If the Collector can't reach a cluster, `status` is stale or absent, but declared `spec` remains intact and authoritative. The system degrades gracefully.
- **TF state is sensitive.** It contains private keys (cluster CA, etcd/API certs). State-at-rest encryption becomes a core concern — resolved by OpenTofu's native encryption (see CONTEXT.md "State Encryption"), not by RezusCloud reimplementing crypto.
- **Provider mappings required.** Each provider module must declare which TF resource types map to which K8s-style API resources and how to extract Machine-level fields (IP, hostname, status) from them.
- **No separate secret store for cluster PKI.** Credentials come from the tenant's TF state via `tofu state pull` into an in-memory cache (see CONTEXT.md "Secrets Cache").

## See Also

- [ADR 9: Kubernetes-Native API](0009-kubernetes-native-api.md) — The REST API shape this builds on
- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md) — Superseded; providers are now RezusCloud-side modules generating standard TF
- [ADR 22: Exec Tofu Binary for Reconciliation](0022-exec-tofu-binary.md) — How RezusCloud applies infrastructure
- [ADR 20: Management Connectivity](0020-management-connectivity.md) — How the Collector reaches clusters
