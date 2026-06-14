// Package tfexec orchestrates per-tenant `tofu` subprocess invocations.
//
// RezusCloud reconciles infrastructure by exec'ing the OpenTofu binary as a
// subprocess (ADR 22 — the tfcontroller / Atlantis / Terraform Cloud pattern):
// it gets the full Terraform/OpenTofu lifecycle for free, every provider works
// unmodified, and state is guaranteed compatible with the rest of the ecosystem.
//
// This package is the scaffold (issue #85). It provides three things:
//
//   - Working-directory management: one directory per tenant under a root
//     (typically $DATA_DIR/tfwork/<tenant>/), holding generated `.tf.json` plus
//     the `.terraform/` plugin cache. The RezusCloud-owned `backend.tf`
//     pointing at RezusCloud's own HTTP backend (#84) is written automatically.
//
//   - Controlled execution: Run(ctx, tenant, args...) execs `tofu <args>` in the
//     tenant workdir with a clean + augmented environment (bootstrap credentials,
//     TF_ENCRYPTION in Phase 2), captures stdout/stderr into a Result while
//     streaming it line-by-line to the logger tagged by tenant, and enforces a
//     deadline — killing the process on overrun.
//
//   - Bootstrap credential injection: a pluggable CredentialProvider feeds
//     per-tenant bootstrap secrets (Bitwarden token, OCI keyfile, …) into the
//     subprocess environment. v1 reads them from the process environment.
//
// # Concurrency contract (important)
//
// Run does NOT serialize calls for the same tenant. Two concurrent Run calls on
// the same tenant will race on the shared workdir and the backend lock. The
// debounced per-tenant apply queue (#87) is the only correct place to serialize;
// until it lands, callers MUST ensure at most one in-flight Run per tenant.
//
// This package is deliberately free of business logic: it knows nothing about
// clusters, nodegroups, or providers. Phase 2 (#86/#87) adds encryption + the
// reconcilers that drive it; Phase 3 (#88–#90) generates the `.tf.json` it runs.
package tfexec
