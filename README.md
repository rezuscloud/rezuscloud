# RezusCloud

Management plane and CLI for RezusCloud Personal Cloud. Single repository, two binaries — following the `kubectl`-in-`kubernetes` model.

## Binaries

| Binary | Purpose | Distribution |
|--------|---------|-------------|
| `rezuscloud` | Management plane server (HTTP API, WebUI, MachineLink) | Docker image (`ghcr.io/rezuscloud/rezuscloud`) + static binary |
| `rezusctl` | CLI tool (boot, tenant, join) | Static binary only (no Docker image) |

Both share the same version number and are released together.

## Quick Start

```bash
# Build both
go build -o rezuscloud .
go build -o rezusctl ./cmd/rezusctl

# Run management plane
REZUSCLOUD_ADMIN_PASSWORD=changeme ./rezuscloud

# Use CLI
./rezusctl --help
```

## Repository Layout

```
cmd/
├── rezuscloud/       # Management plane entry point
└── rezusctl/         # CLI entry point + subcommands
chart/                # Helm chart for Kubernetes deployment
internal/
├── api/              # REST API handlers (server)
├── auth/             # JWT + RBAC (server)
├── backup/           # S3 backup (server)
├── cli/              # CLI-only packages (boot, platform, helm, etc.)
├── controller/       # Finalizers (server)
├── credentials/      # Kubeconfig generation (server)
├── ingress/          # HA ingress middleware (server)
├── state/            # SQLite store (server)
├── statemachine/     # Phase/stage transitions (server)
├── talosconfig/      # Talos config generation (server)
├── upgrade/          # Rolling upgrades (server)
├── watch/            # Event bus (server)
├── web/              # WebUI (server)
├── version/          # Build metadata (server)
tests/
├── integration/      # Server integration tests
main.go               # Server main
```

## API Endpoints

```
# Tenants
POST/GET/DELETE /api/v1/tenants/{name}
POST/GET/DELETE /api/v1/tenants/{name}/nodegroups/{name}
POST/GET/DELETE /api/v1/tenants/{name}/machines/{id}
GET             /api/v1/tenants/{name}/machines/{id}/logs
POST/GET/DELETE /api/v1/tenants/{name}/patches/{name}
POST/GET/DELETE /api/v1/tenants/{name}/join-tokens/{id}
POST/GET        /api/v1/tenants/{name}/prechecks
GET             /api/v1/tenants/{name}/upgrade-status
# Cluster-wide
POST/GET/DELETE /api/v1/machines
POST/GET        /api/v1/providers/{name}
# Auth
POST            /api/v1/auth/login
POST            /api/v1/auth/logout
GET             /api/v1/auth/whoami
# Admin
POST/GET/DELETE /api/v1/users
# Backup
POST            /api/v1/backups/database
POST            /api/v1/backups/resources
GET             /api/v1/backups
# System
GET             /healthz  /readyz  /version
```

## Deployment

Two modes are supported:

### Standalone (Docker)

```bash
docker run -d \
  -p 8080:8080 -p 50180:50180 -p 50190:50190 \
  -v /data/rezuscloud:/data \
  -e REZUSCLOUD_ADMIN_PASSWORD=changeme \
  ghcr.io/rezuscloud/rezuscloud:latest
```

### Kubernetes (Helm chart)

```bash
# Local chart (from repo clone)
helm install rezuscloud ./chart \
  --set jwtSecret=$(openssl rand -hex 32) \
  --set rezuscloud.adminPassword=changeme

# Or from OCI registry (when published)
helm install rezuscloud oci://ghcr.io/rezuscloud/rezuscloud-chart \
  --set jwtSecret=$(openssl rand -hex 32) \
  --set rezuscloud.adminPassword=changeme
```

See [`chart/README.md`](./chart/README.md) for the full deployment guide, including ingress, persistence, secrets, and per-service configuration.

## Development

```bash
# Test everything
go test ./... -count=1 -race

# Lint everything
golangci-lint run ./...

# Build both binaries
go build -o rezuscloud .
go build -o rezusctl ./cmd/rezusctl
```

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `REZUSCLOUD_ADDR` | `:8080` | HTTP listen address |
| `REZUSCLOUD_DATA_DIR` | `/data` | SQLite database directory |
| `REZUSCLOUD_MODE` | `standalone` | `standalone` or `cluster` |
| `REZUSCLOUD_MACHINELINK_ADDR` | `:50180` | MachineLink gRPC address |
| `REZUSCLOUD_PROVIDER_ADDR` | `:50190` | Provider gRPC address |
| `REZUSCLOUD_ADMIN_PASSWORD` | — | Initial admin password |
| `REZUSCLOUD_JWT_SECRET` | — | JWT signing secret |
