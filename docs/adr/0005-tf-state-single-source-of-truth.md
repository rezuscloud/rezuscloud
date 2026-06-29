# ADR 0005: TF State as the Single Source of Truth for Declared Infrastructure

## Status

Accepted

## Context

RezusCloud must represent two fundamentally different kinds of state for a
managed tenant:

1. **Declared infrastructure** — what the operator asked for and what was
   created: the tenant's Kubernetes/Talos version, a node group's machine
   count and provider, a machine's identity. Structural, changes on user
   action, the *intent* of the system.
2. **Observed runtime state** — what the cluster is *actually doing*: node
   health, pod status, machine stage. Ephemeral, changes constantly, the
   *reality* of the system.

Mixing these in one store (the original design's generic `resources` table
with `spec` and `status` columns) made it unclear what was authoritative and
gave operators who already use OpenTofu no first-class way in.

## Decision

**Terraform state is the single source of truth for declared infrastructure.
Observed runtime state is a separate, best-effort plane that never mixes with
it.**

### Spec plane — TF state is authoritative for declared infrastructure

- **One TF state blob per tenant**, stored in RezusCloud's TF HTTP backend
  (the protocol `tofu` speaks natively).
- The REST API's `metadata` + `spec` fields for infrastructure resources
  (Tenant, NodeGroup, Machine) are **projections** of the TF state, mapped
  through provider-declared resource schemas. The API is a read model over TF
  state, not a separate store of intent.
- A thin derived index maps `(tenant, resource_type, resource_name)` to
  `(state_version, json_path)` so reads do not re-parse the full blob. The
  index is a cache rebuilt from state — never a second source of truth.
- RezusCloud reconciles declared state by exec'ing `tofu apply` (see
  [ADR 0006](0006-exec-tofu-binary.md)).

### Status plane — see ADR 0010

`status` is best-effort observation, never authoritative, never written back
to TF state. Its mechanism is deferred — see
[ADR 0010](0010-status-plane-best-effort.md).

### The rule

For any infrastructure resource: `metadata` + `spec` come from TF state;
`status` comes from observation. The two never mix. An observation can never
silently override a declaration, and a declaration can never be confused for a
live measurement.

### What this replaces

- The generic `resources` table's `spec` column is no longer the authority for
  infrastructure intent — TF state is. (The table persists for the management
  plane's own data — Users, API tokens, audit, status — see
  [ADR 0004](0004-sqlite-state-store.md).)
- A separate encrypted `tenant_secrets` bundle is not needed for cluster PKI:
  the `talos_machine_secrets` TF resource holds it in TF state, encrypted with
  OpenTofu's native encryption.

## Consequences

- **First-class IaC.** Operators can point `tofu` at RezusCloud's backend and
  it works natively. RezusCloud orchestrates the same TF tooling the
  production `talos-iac` deployment uses.
- **No drift** between "what RezusCloud thinks" and "what TF created" — they
  are the same blob.
- **TF state is sensitive** (private keys). Encryption at rest is handled by
  OpenTofu's native `pbkdf2`+`aes_gcm` encryption, configured in the generated
  `.tf.json`. RezusCloud never reimplements crypto.
- **Provider mappings required.** Each provider module declares which TF
  resource types map to which API resources, and how to extract fields (IP,
  hostname) from them. See [ADR 0007](0007-provider-as-tf-wrapper.md).

## Status

This ADR records the *principle*. The projection layer that reads `spec` from
TF state is under construction; until it lands, the management plane's
`resources` table still holds infrastructure `spec` directly. The target model
is what is written above.

## See Also

- [ADR 0003](0003-rest-api-kubernetes-model.md) — the API that projects from
  TF state
- [ADR 0006](0006-exec-tofu-binary.md) — how declared state is realised
- [ADR 0007](0007-provider-as-tf-wrapper.md) — the mappings
- [ADR 0010](0010-status-plane-best-effort.md) — the status plane
