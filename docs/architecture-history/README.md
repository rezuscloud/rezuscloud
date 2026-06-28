# Architecture History — Superseded Decisions

This folder preserves **decisions that have been superseded or rejected**. It is
deliberately kept **separate** from the live ADRs in `docs/adr/`.

The live ADRs (`docs/adr/`) record the architecture **as it is today and the
direction forward** — one consistent model, no historical contrast.

This folder records the **alternative architectures that were considered and
rejected** along the way. The reasoning here is valuable: it documents *why* a
given path was not taken, so the same option is not re-proposed in the future.

## How to read this folder

- Every document here describes a model that is **no longer the architecture**.
- If a live ADR depends on the rejection of one of these options, it links to
  the relevant history entry in its *Context* section.
- These documents are **frozen** — they are not updated when the live
  architecture changes. They capture the state of reasoning at the time the
  option was rejected.

## The three architecture eras (and why they were superseded)

RezusCloud's infrastructure-management model went through three iterations. The
confusion the live ADRs used to carry came from ADRs spanning all three. The
live ADR set (in `docs/adr/`) now reflects **only the current, third era**;
this folder holds the first two.

### Era 1 — Direct cloud-SDK calls in the controller

**Rejected** because it coupled the management binary to every cloud SDK and
reimplemented infrastructure-as-code core (diff, CRUD, topological ordering)
with less capability than Terraform. There is no clean way to add a new
infrastructure target without code changes inside the management binary.

Original ADR: `superseded-adrs/_archive-2026-05-2026-06/0001-crd-based-reconciliation.md`
(original context section).

### Era 2 — Standalone gRPC provider binaries (Cluster-API-inspired)

Each provider was a standalone binary that connected **outbound** to the
management cluster over gRPC and provisioned machines. The management plane
never imported a cloud SDK; providers held their own credentials.

**Rejected** because:

- It reinvented infrastructure-as-code with a bespoke gRPC protocol instead of
  reusing the proven Terraform provider ecosystem.
- There was no first-class IaC path — operators who already manage
  infrastructure with OpenTofu (as the production `talos-iac` deployment does)
  had no native way in.
- Every provider reimplemented cloud SDKs, state management, drift detection,
  and ordering that Terraform already provides.
- RezusCloud must be **self-contained** (the only component needed to run the
  personal cloud, e.g. packaged as a Home Assistant container). Distributing
  provisioning across separate provider binaries broke that property.

Original ADRs:
- `superseded-adrs/_archive-2026-05-2026-06/0012-provider-based-machine-provisioning.md`
- `superseded-adrs/_archive-2026-05-2026-06/0014-full-talos-cluster-lifecycle.md`

### Era 2b — SideroLink for config delivery

SideroLink (Talos's WireGuard-over-gRPC config-pull kernel feature) was
proposed as the universal config delivery mechanism: machines would boot a
minimal image with `siderolink.api=` kernel args, connect outbound, and pull
their config.

**Rejected** because the production `talos-iac` deployment — which RezusCloud
must faithfully reproduce — uses **no SideroLink at all**:

- Cloud VMs receive config via `user_data` (cloud-init) at VM creation time.
- Bare metal receives config via `talos_machine_configuration_apply` (Talos
  node API push) after a one-time maintenance-mode boot.

SideroLink solved a reachability problem that the production deployment does
not have, at the cost of an embedded gRPC+WireGuard server, custom Talos
images, and a network dependency where none is needed.

Original ADR: `superseded-adrs/_archive-2026-05-2026-06/0013-siderolink-config-pull.md`

### Era 2c — Embedding the Terraform execution library

Using OpenTofu/Terraform as a Go library, calling its internals directly.

**Rejected** because OpenTofu's internals are not a stable library API, the
plugin protocol still requires the full provider-plugin machinery, and engine
upgrades would become RezusCloud upgrades.

Original ADR: `superseded-adrs/_archive-2026-05-2026-06/0022-exec-tofu-binary.md`
(context section, Option A).

## Other rejected options (one-off)

- **Cluster API (CAPI) infrastructure providers** — require a Kubernetes
  management cluster running before any provisioning; make inbound calls that
  cannot reach machines behind NAT/CGNAT; no CAPI provider exists for
  PXE-booting consumer hardware. See original ADR 0012 (Option A).
- **Crossplane** — providers live in the management cluster and make inbound
  calls to cloud APIs; cannot reach targets behind NAT. See original ADR 0012
  ("Why Not Crossplane").
- **Hosted control planes (Kamaji-style)** — rejected in favour of full Talos
  clusters owned end-to-end by RezusCloud. See original ADR 0014.
- **NATS as the event bus (first proposal)** — removed in the REST-watch era
  because the watch endpoint made it redundant. **Reintroduced** in the current
  era as the event/streaming primitive for async controller operations; see
  the live `docs/adr/0006-event-bus-nats.md`. The original removal reasoning is
  preserved in `superseded-adrs/_archive-2026-05-2026-06/0004-event-driven-boot-nats.md`.
- **SPA frontend (Vue/React), gRPC-Web transport, Monaco, JSONForms** —
  rejected as enterprise-console complexity inappropriate for a personal cloud.
  See original ADR 0015.
- **OIDC/SAML/PGP authentication** — rejected; local JWT users + API tokens
  suffice. See original ADR 0016.

## Full archive

The complete, unmodified ADR set from the pre-pivot eras lives in
[`superseded-adrs/_archive-2026-05-2026-06/`](superseded-adrs/_archive-2026-05-2026-06/).
