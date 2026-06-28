# AGENTS.md

Guidelines for agentic coding agents operating in this repository.

## What This Repo Is

`rezuscloud` is a **tenant orchestrator** that lives one layer above `talosctl`
and `kubectl`. It declares Kubernetes clusters (called *tenants*), realises
them on top of lower-layer tools (OpenTofu), and surfaces their state
read-only. It never duplicates lower-layer features. See
[CONTEXT.md](CONTEXT.md) and [docs/adr/](docs/adr/) for the authoritative
architecture.

Two binaries from one repo (the `kubectl`-in-`kubernetes` model):

- **`rezuscloud`** — server binary (long-running management plane: REST API,
  WebUI, TF HTTP backend, TF execution engine, reconciliation). Deployed as a
  Docker container.
- **`rezusctl`** — CLI binary (`boot` + thin client commands against the REST
  API). Static binary, no container image.

**rezusctl builds clusters. kubectl manages them. rezuscloud runs them.**

## Current State

The TF-state architecture pivot (epic #83) is in progress — Phases 1–3
landed (TF backend, exec engine, encryption, apply queue, three provider
modules), Phase 4 (projection + reconciliation) in flight. Dead code from the
superseded gRPC-provider/SideroLink model is being removed under the Cleanup
milestone (#94, #104–#109).

- **47 tested packages, 703 test functions** — all passing
- CI + release pipelines green. Single version, single release.

## Repository Layout

```
cmd/
├── rezusctl/         # CLI entry point (boot + API client subcommands)
main.go               # Server entry point
chart/                # Helm chart for Kubernetes deployment
internal/
├── api/              # REST API handlers (server)
│   ├── logs/         #   Machine log viewer endpoints
│   └── ...
├── applyqueue/       # Debounced per-tenant reconciliation queue
├── audit/            # HTTP-middleware audit log
├── auth/             # JWT + RBAC + API tokens (server)
├── backup/           # S3 backup (server)
├── cli/              # CLI-only packages
│   ├── addons/       #   Addon management (boot-time)
│   ├── boot/         #   Docker/QEMU boot orchestration
│   ├── cloud/        #   Cloud type definitions
│   ├── config/       #   CRD registration
│   ├── configprovider/ # Config provider
│   ├── connectivity/ #   Connectivity modes
│   ├── controller/   #   RezusCloudConfig reconciler
│   ├── events/       #   Event bus (CLI)
│   ├── helm/         #   Helm installer (boot-time)
│   ├── image/        #   Image Factory schematics
│   ├── ingress/      #   Reverse proxy (CLI)
│   ├── installer/    #   Boot-time CNI/DNS/TLS/chart installer abstractions
│   │   └── cilium/   #     Cilium CNI installer
│   ├── kamaji/       #   Kamaji converter
│   ├── machine/      #   Provider interface + registry
│   ├── machinecrd/   #   Machine CRD types
│   ├── platform/     #   Docker/QEMU platform providers
│   ├── state/        #   File/CRD state
│   ├── talosconfig/  #   Talos config generation (CLI)
│   ├── tenant/       #   Tenant manager
│   ├── tenantcontroller/ # Tenant reconciler
│   ├── tenantcrd/    #   Tenant CRD types
│   ├── version/      #   Build metadata
│   └── web/          #   WebUI (CLI serve)
├── configrender/     # Config rendering (server)
├── credentials/      # Kubeconfig generation (server)
├── dashboard/        # Dashboard posture/status aggregation (server)
├── ingress/          # HA ingress middleware (server)
├── metrics/          # Resource metrics (server)
├── provider/         # RezusCloud TF Provider modules (oci, openstack, metal)
├── state/            # SQLite store + status derivation (server)
├── talosconfig/      # Talos config generation (server)
├── tfbackend/        # OpenTofu HTTP state backend
├── tfencryption/     # OpenTofu state encryption (pbkdf2 + aes_gcm)
├── tfexec/           # OpenTofu subprocess driver
├── upgrade/          # Rolling upgrades (server)
├── watch/            # Event bus + SSE/streaming (server)
└── web/              # WebUI (server)
    ├── handlers/     #   Section-specific handlers (clusters, machines, settings, dashboard)
    ├── layout/       #   Shared layout + design-system components
    └── pages/        #   templ views
```

## Key Architecture

- **State**: SQLite (ADR 0004). Two data planes never mixed (ADR 0005): TF
  state for declared infrastructure (spec), best-effort observation for
  status (ADR 0010). Pluggable Store interface for future backends.
- **REST API**: K8s-style resource model (metadata/spec/status) with JWT auth,
  RBAC, optimistic concurrency, watch/SSE (ADR 0003).
- **Reconciliation**: Debounced per-tenant apply queue → `tofu apply` via the
  exec engine (ADRs 0005/0006, epic #87/#99).
- **Providers**: RezusCloud-side Go modules wrapping real TF providers
  (`internal/provider/<name>/` — oci, openstack, metal). No gRPC provider
  binaries, no custom TF plugins (ADR 0007).
- **Config delivery**: `user_data` (cloud VMs) + Talos API push (bare metal).
  No SideroLink (ADR 0008).
- **WebUI**: templ + HTMX + Alpine.js + Tailwind CSS v4. Read-only surfacing
  of lower-layer state (ADR 0015); no interactive kubectl duplication.
- **Upgrades**: Rolling engine for Talos and Kubernetes. One machine at a
  time, health check, auto-rollback (ADR lifecycle, #93).
- **Backup**: S3-compatible. Database snapshot + resource export.
- **Events**: In-process event bus today (`internal/watch/`); NATS planned
  (ADR 0009, #110).

## Build Separation (Kubernetes Model)

- **Server binary**: `go build -o rezuscloud .` — deployed as Docker image
- **CLI binary**: `go build -o rezusctl ./cmd/rezusctl` — static binary, no Docker image
- **Unified CI**: `go test ./...` tests everything. `golangci-lint run ./...` lints everything.
- **Unified release**: GoReleaser produces both binary archives + one Docker image (server only)
- **Shared version**: Both binaries get the same version from GitVersion

## Commands

```bash
# Build both binaries
go build -o rezuscloud .
go build -o rezusctl ./cmd/rezusctl

# Test everything (server + CLI)
go test ./... -count=1 -race

# Lint everything
golangci-lint run ./...

# Templ generate (for WebUI)
templ generate ./...

# Helm chart (Kubernetes deployment)
helm lint chart/
helm template rezuscloud chart/ --set jwtSecret=test --set rezuscloud.adminPassword=test
```

## Code Style

- Imports: stdlib → external → internal
- `log.Fatalf` for startup errors, return errors from handlers
- Lowercase error strings (ST1005)
- `_, _ =` for discarded returns (errcheck `check-blank: false`)
- Generated `*_templ.go` files: never edit directly, run `templ generate`
- CLI packages under `internal/cli/`, server packages under `internal/`
- Tests: `httptest.NewServer` for integration, direct handler calls for unit

## Environment Variables

| Key | Default | Purpose |
|-----|---------|---------|
| `REZUSCLOUD_ADDR` | `:8080` | HTTP listen address |
| `REZUSCLOUD_DATA_DIR` | `/data` | Persistent state directory |
| `REZUSCLOUD_MODE` | `standalone` | `standalone` or `cluster` |
| `REZUSCLOUD_ADMIN_PASSWORD` | — | Initial admin password |
| `REZUSCLOUD_JWT_SECRET` | — | JWT signing secret |
| `REZUSCLOUD_PROMETHEUS_URL` | — | Prometheus query endpoint (resource pressure viz) |
| `REZUSCLOUD_K8S_API_URL` | — | Kubernetes API server URL |
| `REZUSCLOUD_BACKUP_DIR` | — | Backup output directory |
| `REZUSCLOUD_AUDIT_RETENTION_DAYS` | `90` | Audit log retention |
