# Home Assistant Integration

> **Type:** How-to · **Audience:** User integrating Home Assistant

> **⚠ Partially stale.** The deployment topology and HA addon blueprint below
> describe the pre-pivot architecture (MachineLink port 50180, WireGuard
> capabilities, Provider gRPC port 50190, join tokens). Under the current
> architecture (TF-state model), the management plane needs only the HTTP port
> (8080) — there is no MachineLink, no Provider gRPC, and no join token.
> Authoritative current architecture:
> [`CONTEXT.md`](../../CONTEXT.md) + [`docs/adr/`](../adr/README.md).
> A full rewrite of this guide is tracked as a follow-up; until then, disregard
> the MachineLink/Provider-gRPC/join-token specifics below and expose only
> `REZUSCLOUD_ADDR` (HTTP :8080) plus the standard env vars from
> [`AGENTS.md`](../../AGENTS.md).

Running the RezusCloud management plane as a **Home Assistant add-on** — turning an always-on HA device into a personal cloud control plane with a sidebar panel.

> **This document defines the contract between two repos.**
> - **`rezuscloud/rezuscloud`** (this repo) — provides the management plane binary + container image
> - **`rezuscloud/homeassistant-rezuscloud`** (add-on repo) — wraps it into an HA add-on with `config.yaml`, `run.sh`, and HAOS lifecycle integration

## Architecture

```
Home Assistant OS                                Cloud / Edge
┌──────────────────────────────────────────┐     ┌──────────────┐
│ HA Sidebar                               │     │ Worker nodes │
│ ┌──────┬──────┬──────────┬─────────────┐ │     │ (Talos)      │
│ │ Over │ Conf │ RezusCl… │ Energy      │ │     │              │
│ │ view │ ig   │ ☁️       │             │ │     │              │
│ └──────┴──────┴──────────┴─────────────┘ │     │              │
│                                          │     │              │
│ ┌──────────────────────────────────────┐ │     │              │
│ │ RezusCloud Ingress Panel             │ │     │              │
│ │ (iframe → localhost:3000)            │ │     │              │
│ │                                      │ │     │              │
│ │  Dashboard │ Tenants │ Machines      │ │     │              │
│ │                                      │ │     │              │
│ └──────────────────────────────────────┘ │     │              │
│                                          │     │              │
│ ┌──────────────────────────────────────┐ │     │              │
│ │ HA Supervisor                        │ │     │              │
│ │ ┌──────────────────────────────────┐ │ │     │              │
│ │ │ rezuscloud container             │ │ │     │              │
│ │ │                                  │ │ │     │              │
│ │ │ /usr/local/bin/rezuscloud        │ │ │     │  ───> │ hetzner     │
│ │ │ ├── HTTP/WebUI    :3000          │ │ │     │  ───> │ oci         │
│ │ │ ├── MachineLink   :50180         │ │ │     │  ───> │ edge metal  │
│ │ │ ├── Provider gRPC :50190         │ │ │     │  ───> │ home RPi    │
│ │ │ └── SQLite        /data/         │ │ │     │       └──────────────┘
│ │ └──────────────────────────────────┘ │ │
│ └──────────────────────────────────────┘ │
└──────────────────────────────────────────┘
```

## What the Add-On Repo Must Build

The add-on repo (`rezuscloud/homeassistant-rezuscloud`) is a standard HA add-on structure:

```
homeassistant-rezuscloud/
├── config.yaml          # HA add-on configuration (ingress, panel, options)
├── DOCS.md              # Documentation shown in HA add-on store
├── Dockerfile           # Multi-stage: pull rezuscloud image + HA base
├── run.sh               # Entry point: configure + exec rezuscloud
├── translations/
│   └── en.yaml          # Option labels and descriptions
├── icon.png             # Add-on icon (shown in HA add-on store)
└── README.md            # Repository README
```

### config.yaml

This is the critical file that makes RezusCloud appear in the HA sidebar:

```yaml
name: "RezusCloud"
description: "Personal cloud management — run Kubernetes clusters from your home"
version: "0.1.0"
slug: "rezuscloud"
url: "https://github.com/rezuscloud/homeassistant-rezuscloud"

# -- Sidebar panel (this is what makes it appear in HA sidebar) --
ingress: true                    # Enable ingress → sidebar entry
ingress_port: 3000               # Internal port rezuscloud listens on
ingress_stream: true             # Stream requests (for SSE/WebSocket)
panel_icon: "mdi:cloud"         # MDI icon in sidebar
panel_title: "RezusCloud"       # Sidebar text (defaults to name)
panel_admin: true                # Only visible to admin users

# -- Lifecycle --
init: false                      # We use our own entrypoint (run.sh)
startup: "services"              # Start after core HA services
boot: "manual"                   # Don't auto-start on first install

# -- Architecture --
arch:
  - aarch64                      # RPi 4/5, HA Green/Yellow
  - amd64                        # Intel NUC, mini PC

# -- Privileges (needed for Docker-in-Docker: Talos containers) --
host_network: true               # Needed for MachineLink + WireGuard
docker_api: true                 # Read-only Docker API access
privileged:
  - NET_ADMIN                    # WireGuard/MachineLink tunnel
  - SYS_ADMIN                    # Docker container management
  - NET_RAW                      # Raw socket access for CNI
full_access: true                # Full hardware access for Talos containers
apparmor: false                  # Disable AppArmor (conflicts with privileged ops)

# -- Storage --
map:
  - data:rw                      # /data for persistent SQLite state

# -- Watchdog --
watchdog: "http://[HOST]:[PORT:3000]/healthz"

# -- User-configurable options --
options:
  auto_start: false
  join_token: ""
  ingress_type: "direct"
  backup_enabled: false
  backup_s3_endpoint: ""
  backup_s3_bucket: ""

schema:
  auto_start: bool
  join_token: password?
  ingress_type: list(direct|cloudflare|ngrok)
  backup_enabled: bool
  backup_s3_endpoint: str?
  backup_s3_bucket: str?
```

### Key config.yaml fields explained

| Field | Value | Why |
|-------|-------|-----|
| `ingress: true` | **Required** | This is what creates the sidebar entry. Without it, the add-on is invisible in the HA UI. |
| `ingress_port: 3000` | Internal port | HA supervisor proxies `http://addon-rezuscloud:3000` into an iframe in the HA frontend. |
| `ingress_stream: true` | Stream mode | Needed for SSE (live events), HTMX partial responses, and WebSocket connections from the WebUI. |
| `panel_icon: mdi:cloud` | Cloud icon | [Material Design Icons](https://materialdesignicons.com/) — appears next to "RezusCloud" in the sidebar. |
| `panel_admin: true` | Admin-only | Only admin HA users see the panel. Non-admin users can't manage clusters. |
| `host_network: true` | Host network | MachineLink needs to accept incoming TCP connections on port 50180. Ingress won't route gRPC. |
| `docker_api: true` | Docker access | The management plane creates Talos containers via the Docker socket. |
| `privileged: [NET_ADMIN, SYS_ADMIN, NET_RAW]` | Capabilities | Required for WireGuard tunnel (MachineLink) and Docker-in-Docker. |
| `full_access: true` | Full access | Talos containers need device access, cgroup manipulation, and mount operations. |
| `apparmor: false` | No AppArmor | AppArmor blocks the privileged operations needed for Talos containers. |
| `watchdog` | healthz | HA supervisor monitors `/healthz` and restarts the add-on if unhealthy. |

### Dockerfile

```dockerfile
# Multi-stage: pull the rezuscloud binary from GHCR
FROM ghcr.io/rezuscloud/rezuscloud:latest AS rezuscloud

# Use HA base image
FROM ghcr.io/home-assistant/base:latest

# Copy binary from release image
COPY --from=rezuscloud /usr/local/bin/rezuscloud /usr/local/bin/rezuscloud
RUN chmod +x /usr/local/bin/rezuscloud

# Copy entrypoint
COPY run.sh /run.sh
RUN chmod a+x /run.sh

CMD ["/run.sh"]
```

### run.sh

```bash
#!/usr/bin/with-contenv bashio
set -e

# -- Configure from HA add-on options --
export REZUSCLOUD_DATA_DIR="/data/rezuscloud"
export REZUSCLOUD_ADDR=":3000"
export REZUSCLOUD_MACHINELINK_ADDR=":50180"
export REZUSCLOUD_PROVIDER_ADDR=":50190"
export REZUSCLOUD_MODE="standalone"

# Join token (from HA options UI)
if bashio::config.has_value 'join_token'; then
    export REZUSCLOUD_JOIN_TOKEN="$(bashio::config 'join_token')"
fi

# Backup configuration
if bashio::config.true 'backup_enabled'; then
    export REZUSCLOUD_BACKUP_ENABLED="true"
    export REZUSCLOUD_BACKUP_S3_ENDPOINT="$(bashio::config 'backup_s3_endpoint')"
    export REZUSCLOUD_BACKUP_S3_BUCKET="$(bashio::config 'backup_s3_bucket')"
fi

# Ensure data directory exists
mkdir -p "${REZUSCLOUD_DATA_DIR}"

# -- Health check announcement --
bashio::log.info "RezusCloud management plane starting"
bashio::log.info "  WebUI:     http://localhost:3000"
bashio::log.info "  MachineLink: :50180"
bashio::log.info "  Provider:   :50190"

# -- Run the management plane --
exec rezuscloud
```

### Ingress Panel Behavior

When `ingress: true` is set:

1. **HA Supervisor** starts the add-on container
2. Supervisor creates a **reverse proxy** from the HA frontend to `http://addon-rezuscloud:3000`
3. A **sidebar entry** appears with `panel_icon` (mdi:cloud) and `panel_title` ("RezusCloud")
4. Clicking the sidebar entry loads the RezusCloud WebUI inside an **iframe** in the HA frontend
5. All requests go through HA's auth proxy — the user's HA session is used

**Important constraints for the WebUI** (in `rezuscloud` repo):

- **No authentication redirect** — HA ingress handles auth. The WebUI should not prompt for login when accessed via ingress. Detect the `X-Hass-Source` header.
- **Relative URLs only** — the WebUI is served under `/api/hassio_ingress/...`. All internal links must be relative (no absolute paths like `/api/v1/tenants` — use `api/v1/tenants`).
- **CSP headers** — HA sets strict Content-Security-Policy. The WebUI must work within it (no inline scripts except Alpine.js, HTMX is fine).
- **No iframe breakout** — don't set `X-Frame-Options: DENY` on the HTTP server.

## What This Repo (`rezuscloud`) Must Provide

The `rezuscloud` binary must:

| Requirement | Implementation |
|-------------|---------------|
| Listen on port 3000 | `REZUSCLOUD_ADDR=:3000` |
| Serve WebUI at `/` | templ + Alpine.js + HTMX |
| Serve health at `/healthz` | Returns 200 OK |
| Serve API at `/api/v1/...` | REST endpoints |
| Detect ingress mode | Check `X-Hass-Source` header, skip auth UI |
| Use relative URLs | All WebUI links are relative |
| Work without TLS | HA ingress terminates TLS |
| Store state in `/data/` | `REZUSCLOUD_DATA_DIR=/data/rezuscloud` |
| Accept Docker socket | `/var/run/docker.sock` mounted by HA supervisor |
| Run as PID 1 | `exec rezuscloud` in run.sh — no s6, no init system |
| Work without K8s | Standalone mode (SQLite) — no K8s dependency |
| Multi-arch | `linux/amd64` + `linux/arm64` via GHCR |

## What the Add-On Repo Must Build

The add-on repo (`rezuscloud/homeassistant-rezuscloud`) is responsible for:

| Responsibility | Details |
|----------------|---------|
| `config.yaml` | HAOS add-on config (ingress, panel, privileges, options schema) |
| `Dockerfile` | Pull `rezuscloud` from GHCR + HA base image |
| `run.sh` | Configure env vars from HA options, exec binary |
| `DOCS.md` | User-facing docs shown in HA add-on store |
| `translations/en.yaml` | Option labels for the HA configuration UI |
| CI pipeline | Build add-on image, publish to GHCR |
| Version tracking | `version` in config.yaml matches `rezuscloud` image tag |
| Testing | Validate add-on installs on HAOS, sidebar appears, WebUI loads |

## Requirements

- **Home Assistant OS or Supervised** — add-ons don't work on HA Container
- **Docker access** — the add-on needs Docker socket for Talos containers
- **Minimum 4 GB RAM** — management cluster components need headroom
- **Persistent storage** — SQLite state stored in `/data/rezuscloud` (HA volume, survives restarts)
- **ARM64 or AMD64** — multi-arch image supports both

## User Experience

1. User adds the add-on repository URL to HA
2. Installs "RezusCloud" from the HA add-on store
3. ☁️ **RezusCloud appears in the HA sidebar**
4. Click → WebUI dashboard opens inside HA
5. User creates tenants, issues join tokens, downloads Talos images
6. Machines boot, connect via MachineLink, receive config
7. All visible from the HA sidebar — no separate port, no separate login
