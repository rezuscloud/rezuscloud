# ADR 0006: Exec the `tofu` Binary for Infrastructure Reconciliation

## Status

Accepted

## Context

With [ADR 0005](0005-tf-state-single-source-of-truth.md) making TF state the
source of truth, RezusCloud must *realise* declared infrastructure — turn "I
want 3 nodes on OCI" into actual OCI instances with Talos config applied.

The rejected alternatives (embedding the TF execution library; keeping custom
gRPC provider binaries) are documented in
[`../architecture-history/`](../architecture-history/README.md). The first
couples RezusCloud to unstable TF internals; the second reinvents IaC with a
bespoke protocol.

## Decision

**RezusCloud exec's the `tofu` binary as a subprocess. It does not embed TF
libraries and does not use custom provider binaries.**

### Reconciliation flow

1. The REST API accepts a resource change (e.g., a node group count edit).
2. The reconciliation loop detects drift and enqueues the tenant (see "Apply
   queue" in `CONTEXT.md`).
3. Provider modules generate standard `.tf.json` into a per-tenant working
   directory (see [ADR 0007](0007-provider-as-tf-wrapper.md)).
4. RezusCloud exec's `tofu init && tofu plan && tofu apply`, injecting
   bootstrap credentials (Bitwarden token, OCI keyfile) into the process
   environment. The tofu `bitwarden` provider fetches individual cloud
   passwords at apply time — RezusCloud never sees them.
5. `tofu` stores encrypted state via its HTTP backend (RezusCloud), using
   OpenTofu native encryption.
6. RezusCloud maps the resulting TF state into API resource `spec` (the
   projection from [ADR 0005](0005-tf-state-single-source-of-truth.md)) and
   publishes events (see [ADR 0009](0009-event-bus-nats.md)).

### Why exec wins

- **Full TF lifecycle for free** — plan, apply, destroy, refresh, state,
  drift detection, locking — all provided by `tofu`, unmodified.
- **Every off-the-shelf TF provider works** with zero adapter code.
- **State compatibility guaranteed** — RezusCloud stores exactly what `tofu`
  produces.
- **Engine upgrades are decoupled** — bumping the bundled `tofu` version does
  not require RezusCloud code changes.
- **Industry-proven** — this is the tfcontroller / Atlantis / Terraform Cloud
  / Terrakube pattern.

### What RezusCloud bundles

- The `tofu` binary, statically linked into the container image.
- The provider modules' TF-generation logic (see
  [ADR 0007](0007-provider-as-tf-wrapper.md)).
- The TF HTTP backend server (`tofu`'s state endpoint).
- A per-tenant working-directory manager.

### Filling TF gaps

Operations the standard providers cannot express are handled by RezusCloud-side
Go logic alongside reconciliation — the canonical case is `talosctl upgrade`
(the `talos` provider has no in-place-upgrade resource). See "Upgrades" in
`CONTEXT.md`: upgrade-first-then-apply, because config generation is
version-aware.

## Consequences

- **Self-contained.** RezusCloud bundles `tofu` and exec's it directly. No
  external TF runners, no agent sidecars.
- **Bootstrap credentials held by RezusCloud.** This reverses the old
  "credential isolation" property of the gRPC-provider era (see
  [`../architecture-history/`](../architecture-history/README.md)) —
  RezusCloud must hold bootstrap creds to exec `tofu`, because it must be the
  only component needed to run the personal cloud.
- **Process hygiene.** RezusCloud manages working directories, env injection,
  subprocess timeouts, and stdout/stderr capture.
- **One `tofu` process per apply, serialized per tenant.** The debounced
  per-tenant apply queue ensures this.

## See Also

- [ADR 0005](0005-tf-state-single-source-of-truth.md) — what exec'ing `tofu`
  produces
- [ADR 0007](0007-provider-as-tf-wrapper.md) — the `.tf.json` generators
- [`../architecture-history/`](../architecture-history/README.md) — why
  embedding the TF library and custom gRPC providers were rejected
