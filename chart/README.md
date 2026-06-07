# rezuscloud Helm Chart

Deploys the [RezusCloud](https://github.com/rezuscloud/rezuscloud) management plane on Kubernetes. Single-replica with PVC-backed SQLite.

> **HA note**: Single-replica only until [Issue #22](https://github.com/rezuscloud/rezuscloud/issues/22) (PostgreSQL backend) ships. SQLite is single-writer.

## Quick Start

```bash
# Install from local chart
helm install rezuscloud ./chart \
  --set jwtSecret=$(openssl rand -hex 32) \
  --set rezuscloud.adminPassword=changeme

# Or install from GHCR (when published)
helm install rezuscloud oci://ghcr.io/rezuscloud/rezuscloud-chart \
  --set jwtSecret=$(openssl rand -hex 32) \
  --set rezuscloud.adminPassword=changeme
```

## Required Configuration

| Value | Description | How to set |
|-------|-------------|------------|
| `jwtSecret` | JWT signing secret. **Required**. | `--set jwtSecret=$(openssl rand -hex 32)` |
| `rezuscloud.adminPassword` | Initial admin password. Only used on first start. | `--set rezuscloud.adminPassword=...` |

Either `jwtSecret` or `existingSecret` must be set. If both are empty, the chart fails with a clear error.

## Authentication & Secrets

Two modes:

### Inline (testing)

```yaml
jwtSecret: "32-hex-bytes..."
rezuscloud:
  adminPassword: "changeme"
```

Chart creates a Secret named `<release>-rezuscloud` containing `jwt-secret`, `admin-password`, and `join-token`.

### Existing secret (production)

```bash
kubectl create secret generic rezuscloud-secrets \
  --from-literal=jwt-secret=$(openssl rand -hex 32) \
  --from-literal=admin-password=changeme
```

```yaml
existingSecret: rezuscloud-secrets
```

The chart skips Secret creation. Expected keys: `jwt-secret` (required), `admin-password` (optional), `join-token` (optional).

## Services

rezuscloud exposes one TCP port for HTTP (WebUI + REST API + healthz/readyz). The chart creates a single Service for it:

| Service | Port | Type (default) | Purpose |
|---------|------|----------------|---------|
| `<release>-rezuscloud` | 8080 | ClusterIP | HTTP (WebUI + REST API + healthz/readyz) |

Inbound traffic for machine registration will be carried by a single UDP port
(WireGuard / Siderolink, per `arch/06-deployment/overview.md`). That Service
is added to the chart alongside the real SideroLink implementation — tracked
separately.

## Persistence

SQLite lives at `/data/rezuscloud.db` inside the container. Enabled by default:

```yaml
persistence:
  enabled: true
  storageClass: ""    # cluster default
  accessMode: ReadWriteOnce
  size: 1Gi
  mountPath: /data
```

To use an existing PVC:

```yaml
persistence:
  existingClaim: my-existing-pvc
```

To disable persistence (data lost on pod restart — testing only):

```yaml
persistence:
  enabled: false
```

## Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  hosts:
    - host: rezuscloud.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: rezuscloud-tls
      hosts:
        - rezuscloud.example.com
```

## Upgrading

```bash
helm upgrade rezuscloud ./chart \
  --set jwtSecret=$JWT_SECRET \
  --set rezuscloud.adminPassword=$ADMIN_PASSWORD
```

The chart annotates the pod template with `checksum/secret` so secret rotations trigger a rollout.

## Values Reference

See [values.yaml](./values.yaml) for the full schema with inline documentation.

### Common overrides

| Path | Default | Description |
|------|---------|-------------|
| `image.repository` | `ghcr.io/rezuscloud/rezuscloud` | Container image repo |
| `image.tag` | `""` (uses appVersion) | Container image tag |
| `image.pullPolicy` | `IfNotPresent` | Container pull policy |
| `replicaCount` | `1` | **Cannot be > 1** until #22 ships |
| `rezuscloud.addr` | `:8080` | HTTP listen address |
| `rezuscloud.dataDir` | `/data` | SQLite directory (matches `persistence.mountPath`) |
| `service.type` | `ClusterIP` | HTTP service type |
| `service.port` | `8080` | HTTP service port |
| `persistence.enabled` | `true` | Enable PVC for SQLite |
| `persistence.size` | `1Gi` | PVC size |
| `ingress.enabled` | `false` | Enable Ingress |
| `resources.requests` | `100m / 128Mi` | CPU/Memory requests |
| `resources.limits` | `500m / 256Mi` | CPU/Memory limits |
| `probes.liveness.path` | `/healthz` | Liveness probe path |
| `probes.readiness.path` | `/readyz` | Readiness probe path |

## Testing

```bash
# Lint
helm lint ./chart

# Template
helm template rezuscloud ./chart \
  --set jwtSecret=test \
  --set rezuscloud.adminPassword=test

# Test (requires cluster)
helm install rezuscloud ./chart --set jwtSecret=test --set rezuscloud.adminPassword=test
helm test rezuscloud
```

## Uninstalling

```bash
helm uninstall rezuscloud

# Optional: remove PVC (data loss)
kubectl delete pvc -l app.kubernetes.io/name=rezuscloud
```
