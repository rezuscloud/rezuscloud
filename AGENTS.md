# AGENTS.md

Guidelines for agentic coding agents operating in this workspace.

## What This Repo Is

`rezuscloud` is the management plane and CLI for RezusCloud Personal Cloud. Two binaries from one repo (like `kubectl` in `kubernetes`):

- **`rezuscloud`** — server binary (HTTP API, WebUI, MachineLink). Deployed as Docker container.
- **`rezusctl`** — CLI tool (boot, tenant, join). Static binary only, no Docker image.

## Current State

- **488 tests** across **57 tested packages** — all passing
- CLI merged from `rezuscloud/rezusctl` (238 tests + 250 server tests)
- All implementation phases complete
- CI + Release pipelines green. Single version, single release.

## Repository Layout

```
cmd/
├── rezuscloud/       # Server entry point (main.go is at root)
└── rezusctl/         # CLI entry point + boot/join/tenant subcommands
chart/                # Helm chart for Kubernetes deployment
internal/
├── api/              # REST API handlers (server)
├── auth/             # JWT + RBAC (server)
├── backup/           # S3 backup (server)
├── cli/              # CLI-only packages
│   ├── addons/       #   Addon management
│   ├── backup/       #   Etcd/CRD backup
│   ├── boot/         #   Docker/QEMU boot orchestration
│   ├── cloud/        #   Cloud type definitions
│   ├── config/       #   CRD registration
│   ├── configprovider/ # Config provider
│   ├── connectivity/ #   Connectivity modes
│   ├── controller/   #   RezusCloudConfig reconciler
│   ├── events/       #   Event bus
│   ├── helm/         #   Helm installer
│   ├── image/        #   Image Factory schematics
│   ├── ingress/      #   Reverse proxy (CLI)
│   ├── join/         #   Join token manager
│   ├── kamaji/       #   Kamaji converter
│   ├── machine/      #   Provider interface + registry
│   ├── machinecrd/   #   Machine CRD types
│   ├── platform/     #   Docker/QEMU platform providers
│   ├── provider/     #   gRPC server + Cilium/Kamaji
│   ├── siderolink/   #   MachineLink server
│   ├── state/        #   File/CRD state
│   ├── talosconfig/  #   Talos config generation (CLI)
│   ├── tenant/       #   Tenant manager
│   ├── tenantcontroller/ # Tenant reconciler
│   ├── tenantcrd/    #   Tenant CRD types
│   ├── version/      #   Build metadata
│   └── web/          #   WebUI (CLI serve)
├── controller/       # Finalizers (server)
├── credentials/      # Kubeconfig generation (server)
├── ingress/          # HA ingress middleware (server)
├── state/            # SQLite store (server)
├── statemachine/     # Phase/stage transitions (server)
├── talosconfig/      # Talos config generation (server)
├── upgrade/          # Rolling upgrades (server)
├── watch/            # Event bus (server)
├── web/              # WebUI (server)
└── version/          # Build metadata (server)
main.go               # Server main
```

## Key Architecture

- **State**: SQLite (standalone mode). Helm chart supports K8s deployment with PV-backed SQLite. Pluggable Store interface for future PostgreSQL/etcd backends.
- **REST API**: K8s-style resource model (metadata/spec/status) with JWT auth and RBAC.
- **WebUI**: templ + HTMX + Alpine.js + Tailwind CSS v4. No border-radius. Relative URLs (HA ingress compatible).
- **CLI**: Cobra-based with boot/join/tenant subcommands. Uses K8s controller-runtime for CRD reconciliation.
- **Upgrades**: Rolling engine for Talos and Kubernetes. One machine at a time, health check, auto-rollback.
- **Backup**: S3-compatible. Database snapshot + CRD resource export.
- **MachineLink**: WireGuard-over-gRPC tunnel (stub — real implementation pending).
- **Provider gRPC**: Outbound provider connections (stub — real implementation pending).

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
| `REZUSCLOUD_MACHINELINK_ADDR` | `:50180` | MachineLink gRPC listen address |
| `REZUSCLOUD_PROVIDER_ADDR` | `:50190` | Provider gRPC listen address |
| `REZUSCLOUD_JOIN_TOKEN` | — | Global join token for machine auth |
| `REZUSCLOUD_ADMIN_PASSWORD` | — | Initial admin password |
| `REZUSCLOUD_JWT_SECRET` | — | JWT signing secret |
