# ADR 17: Dropped Enterprise Features

Status: Accepted (2026-06-05)

## Context

The reference platform (`arch/04-features/`) specifies 20 features. RezusCloud is a personal cloud — the operator owns the hardware, runs the clusters, and uses external tools for advanced workflows. Eight of the twenty features are enterprise concerns that add significant complexity for zero personal-cloud value.

## Decision

The following features are **explicitly dropped** from RezusCloud v1. They will not be implemented unless scope changes (e.g. multi-tenant SaaS mode).

### Dropped features

| # | Feature | Reason | Replacement |
|---|---|---|---|
| 12 | **Workload proxy** (expose in-cluster services) | Requires per-cluster reverse proxy + DNS + TLS automation | User runs their own ingress (Cilium Gateway, Traefik, nginx) |
| 13 | **k8s API proxy** through backend | Concentrates credentials, adds latency, requires TLS pass-through | `rezusctl kubeconfig <cluster>` downloads a kubeconfig; user runs `kubectl` directly |
| 14 | **Embedded DNS** | Operational burden, conflict with cluster DNS (CoreDNS) | Cluster-native DNS |
| 15 | **Embedded cluster discovery** | Talos handles natively via KubeSpan / discovery service | Talos built-in |
| 17 | **Support bundle** | `talosctl support` + `kubectl cluster-info dump` are sufficient | Talos and kubectl native commands |
| 18 | **Metrics & monitoring page** | SigNoz already deployed, Grafana already available | External observability stack |
| 19 | **Settings & feature flags page** (full) | No feature-flag system; no runtime-configurable flags beyond env vars | Env vars + Helm values |
| 20 | **Billing** (Stripe SaaS mode) | Not a SaaS | — |

### Deferred enterprise-only sub-features

Within the in-scope features, the following sub-features are dropped:

- **Machine Classes** (`/machine-classes/*` routes) — static provider config is sufficient
- **Installation media wizard** (`/machines/installation-media/*` 7-step wizard) — replaced with a "download schematic URL" link to the external Image Factory
- **Machine devices/disks/extensions** detail pages — visible in Talos config but not editable from the WebUI
- **Pods view** per cluster — user runs `kubectl get pods`
- **Manifests sync status** per cluster — Flux handles GitOps; not our concern
- **OIDC/SAML/PGP** authentication — see [ADR 16](0016-auth-jwt-only.md)
- **Machine pending updates** detail — surfaced in upgrade wizard, not standalone page
- **Config diffs** page — visible in upgrade wizard, not standalone page

### What stays in scope

| # | Feature | Scope |
|---|---|---|
| 1 | Machine registration & join | Full |
| 2 | Cluster lifecycle | Full |
| 3 | Cluster upgrades | Full |
| 4 | Configuration management | Tenant-wide scope only, see [ADR 19](0019-configpatch-single-scope.md); no schematics wizard |
| 5 | Installation media | Link only (Image Factory URL per cluster) |
| 6 | Backups & restore | Full |
| 7 | Authentication | JWT + API tokens only (ADR 16) |
| 8 | Authorization | admin/edit/view roles only |
| 9 | Users & service accounts | Local users + API tokens (no PGP) |
| 10 | Audit log | Full — HTTP middleware pattern, see [ADR 18](0018-audit-log-middleware.md) |
| 11 | Infra providers | List + status only (providers self-register; no UI config) |
| 16 | Live logs & events | Full (SSE) |
| 19 | Settings & flags | Minimal — JWT secret rotation, backup config |

## Consequences

### What the user does differently
- **No kubectl-in-browser** — user runs kubectl locally with downloaded kubeconfig
- **No in-cluster service exposure through rezuscloud** — user configures their own ingress
- **No metrics dashboard in rezuscloud** — user points browser at SigNoz/Grafana
- **No multi-step installation media wizard** — user downloads schematic URL, builds image via Image Factory web UI
- **No OIDC login** — user creates local account, logs in with username/password

### What the codebase does NOT contain
- No SAML XML parsing
- No PGP key generation/verification
- No Stripe SDK integration
- No embedded DNS server
- No reverse-proxy logic for workload exposure
- No k8s API pass-through handler
- No per-cluster pods/nodes caching layer

### Operator experience trade-off
The operator carries the load that the platform would otherwise absorb:
- Manages their own observability (SigNoz/Grafana)
- Manages their own ingress (Cilium Gateway)
- Runs kubectl directly
- Handles their own IdP if they want SSO (not via the platform)

This is the correct trade-off for a personal cloud. The platform focuses on what only it can do: bootstrap, lifecycle, upgrades, configuration management of Talos nodes.

## See Also

- [ADR 15](0015-frontend-templ-htmx.md) — Frontend stack
- [ADR 16](0016-auth-jwt-only.md) — Auth scope
