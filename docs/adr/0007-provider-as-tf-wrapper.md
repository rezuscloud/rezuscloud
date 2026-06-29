# ADR 0007: Provider — The RezusCloud Module Wrapping a Real TF Provider

## Status

Accepted

## Context

RezusCloud drives infrastructure through OpenTofu (see
[ADR 0006](0006-exec-tofu-binary.md)). `tofu` calls **off-the-shelf registry
TF provider plugins** — `oracle/oci`, `terraform-provider-openstack`,
`siderolabs/talos`, the `bitwarden` provider, etc. These are spawned by `tofu`
as separate processes and die when it exits.

For each platform RezusCloud manages, something must:

1. Generate the standard `.tf.json` configuration that drives the right TF
   provider.
2. Declare the mapping between TF resource types and RezusCloud API resources
   (e.g. `oci_core_instance` → `Machine`), consumed by the state projection
   (see [ADR 0005](0005-tf-state-single-source-of-truth.md)).
3. Fill TF gaps with RezusCloud-side Go logic where a standard provider cannot
   express an operation (e.g. `talosctl upgrade`).
4. Optionally discover nodes (bare-metal subnet scan; cloud has none).

The word "provider" is overloaded in the TF world (a "Terraform provider" is
the registry plugin). This ADR pins a single meaning for RezusCloud.

## Decision

**A Provider is the RezusCloud module that wraps a real Terraform provider to
maintain the infrastructure RezusCloud manages.** One Provider per platform,
living in `internal/provider/<name>/` (e.g. `internal/provider/oci/`,
`internal/provider/openstack/`, `internal/provider/metal/`).

**There is no separate "RezusCloud provider" and "TF provider" — there is one
TF-based Provider that wraps a real registry provider.** The Provider's job is
to generate the `.tf.json` that `tofu` applies against the wrapped registry
provider, and to declare how the resulting TF state projects back into
RezusCloud resources.

gRPC is used only if a given real TF provider requires it for some operation;
otherwise RezusCloud generates config and exec's `tofu` directly. There are no
custom `terraform-provider-rezuscloud-*` plugin binaries.

### The Provider interface (narrow)

```go
type Provider interface {
    Type() string
    Render(...) ([]byte, error)      // generate standard .tf.json
    Mappings() []ResourceMapping     // TF resource type → RezusCloud resource
}
```

Health checks, UI panels, and node discovery land in follow-up work; the
interface grows as those land.

### What dies with this decision

The gRPC provider-binary model (standalone binaries connecting outbound,
self-registering, heartbeating) is gone. There is no `/api/v1/providers`
registry of live processes, no heartbeat endpoint, no `endpoint` field on the
Provider resource. The earlier API surface for that model is drift and is
removed.

## Consequences

- **The Provider concept is RezusCloud-side Go code, not a deployed process.**
  Adding a platform means writing a module under `internal/provider/<name>/`,
  not shipping a new binary.
- **TF state is the only thing the wrapped provider produces that RezusCloud
  trusts.** RezusCloud never trusts a provider's ad-hoc status reports.
- **Discovery (metal only) is RezusCloud-side Go logic on top of TF
  provisioning**, not a TF resource. Discovered or manually-entered nodes
  become inputs to the metal Provider's `.tf.json` generation.

## See Also

- [ADR 0005](0005-tf-state-single-source-of-truth.md) — the mappings feed the
  projection
- [ADR 0006](0006-exec-tofu-binary.md) — what consumes the generated `.tf.json`
- [`../architecture-history/`](../architecture-history/README.md) — why the
  gRPC provider-binary model was rejected
