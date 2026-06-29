# ADR 22: Exec the Tofu Binary for Reconciliation

## Status: Accepted

## Context

RezusCloud needs to *realize* declared infrastructure — turn "I want 3 nodes on OCI" into actual OCI instances, with Talos config applied. With [ADR 21](0021-tf-state-single-source-of-truth.md) making TF state the source of truth, the question is: how does RezusCloud execute infrastructure changes?

This was previously answered by [ADR 12](0012-provider-based-machine-provisioning.md): standalone gRPC provider binaries connect outbound to the management plane and push Talos config. That model is superseded (see ADR 12's supersede note and CONTEXT.md). RezusCloud now drives standard OpenTofu workflows.

Three approaches were considered for *how* RezusCloud runs OpenTofu:

### Option A: Embed the TF execution library

Use OpenTofu/Terraform as a Go library, calling its internals directly from the `rezuscloud` process.

**Rejected because:**

- **No stable library API.** OpenTofu's internals are not designed as a reusable library; the surface is unstable and version-coupled.
- **Plugin protocol mismatch.** TF providers are separate processes speaking gRPC over a specific plugin protocol. Embedding the engine still requires the full plugin machinery; you gain little.
- **Coupling to engine internals.** Engine upgrades become RezusCloud upgrades. State format changes lock the two together.

### Option B: Keep custom gRPC provider binaries (the old ADR 12 model)

Each provider is a standalone binary that RezusCloud dispatches to over gRPC.

**Rejected because:**

- Reinvents infrastructure-as-code with a bespoke protocol instead of reusing the proven TF provider ecosystem.
- No first-class IaC path; operators who already use OpenTofu have no native integration.
- Requires every provider to reimplement cloud SDKs, state, drift detection that TF already provides.

### Option C: Exec the `tofu` binary

RezusCloud generates standard `.tf.json` configuration (using off-the-shelf providers: oci, openstack, talos), writes it to a working directory, and exec's the `tofu` binary (`tofu init`, `tofu plan`, `tofu apply`, `tofu state pull`) as a subprocess. The tofu binary points its HTTP backend at RezusCloud (per [ADR 21](0021-tf-state-single-source-of-truth.md)) so state is stored and encrypted there.

This is the **tfcontroller / Atlantis / Terraform Cloud / Terrakube** pattern: orchestrate the standard TF CLI rather than reimplement it.

**Accepted.**

## Decision

**RezusCloud exec's the `tofu` binary as a subprocess. It does not embed TF libraries and does not use custom provider binaries.**

### Reconciliation flow

1. The REST API writes a resource `spec` (e.g., NodeGroup count changes).
2. A controller detects the drift (see "Apply Queue" in CONTEXT.md).
3. The controller's provider modules generate standard `.tf.json` into a per-tenant working directory.
4. RezusCloud exec's `tofu init && tofu plan && tofu apply`, injecting bootstrap credentials (Bitwarden token, OCI keyfile) into the process environment. The tofu `bitwarden` provider fetches individual cloud passwords at apply time — RezusCloud never sees them.
5. Tofu stores encrypted state via its HTTP backend (RezusCloud), using OpenTofu native encryption.
6. After apply, RezusCloud exec's `tofu state pull` (with `TF_ENCRYPTION` set) to extract `client_configuration`, caching it in memory for the Collector.
7. The controller maps the resulting TF state into K8s-style resource `status` (spec plane per ADR 21) and publishes events.

### Why exec wins

- **Full TF lifecycle for free.** Plan, apply, destroy, refresh, state, drift detection, locking — all provided by tofu, unmodified.
- **Every provider works.** Any TF provider on the registry (oci, openstack, talos, bitwarden, gitlab, …) is usable with zero adapter code. RezusCloud's provider modules just generate the right `.tf.json`.
- **State compatibility guaranteed.** RezusCloud stores exactly what tofu produces; there is no translation layer to drift.
- **Engine upgrades are decoupled.** Bumping the bundled tofu version does not require RezusCloud code changes.
- **Industry-proven.** tfcontroller (Weaveworks), Atlantis (HashiCorp-adjacent), Terraform Cloud, and Terrakube all exec the TF CLI. This is the standard controller pattern, not a novelty.

### Standard TF provider plugins, not custom ones

The providers tofu calls are **off-the-shelf registry plugins** (`oci`, `openstack`, `talos`, `bitwarden`, `gitlab`). RezusCloud writes **no custom `terraform-provider-rezuscloud-*` plugins.** The RezusCloud "provider" concept (see CONTEXT.md "Provider") is a RezusCloud-side Go module that *generates* standard TF config, optionally discovers nodes, and fills TF gaps with Go logic — it is not a TF plugin binary.

### What RezusCloud must bundle

- The `tofu` binary (statically linked into the container image).
- The provider modules' TF-generation logic.
- The TF HTTP backend server (tofu's state endpoint).
- A working-directory manager (one per tenant).

### Filling TF gaps

Some operations the standard providers cannot express are handled by RezusCloud-side Go logic alongside reconciliation, not via TF — the canonical case is `talosctl upgrade` (the `talos` provider has no in-place-upgrade resource). See CONTEXT.md "Upgrades" for the upgrade-first-then-apply ordering.

## Consequences

- **Self-contained.** RezusCloud bundles tofu and exec's it directly. It is the only component needed to run the personal cloud (e.g., as a Home Assistant container). No external TF runners, no agent sidecars.
- **Bootstrap credentials held by RezusCloud.** Exec'ing tofu requires RezusCloud to inject cloud credentials into the subprocess environment. This reverses ADR 12's "credential isolation" — RezusCloud holds bootstrap creds (Bitwarden token, OCI keyfile); individual cloud passwords are still fetched by tofu's bitwarden provider. See CONTEXT.md "Bootstrap Credentials".
- **Process hygiene required.** RezusCloud must manage working directories, env injection, subprocess timeouts, and stdout/stderr capture. This is straightforward but real.
- **One tofu process per apply, serialized per tenant.** The debounced per-tenant apply queue (CONTEXT.md) ensures only one tofu process runs per tenant at a time; applies run in parallel across tenants.
- **State format owned by tofu.** RezusCloud reads TF state JSON as an opaque-to-semi-structured blob via `tofu state pull` — it never hand-parses the internal binary state format.

## See Also

- [ADR 21: TF State as Single Source of Truth](0021-tf-state-single-source-of-truth.md) — What exec'ing tofu produces and where state lives
- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md) — Superseded gRPC model
- [ADR 13: SideroLink-Based Config Pull](0013-siderolink-config-pull.md) — SideroLink rejected; config via user_data + Talos API
