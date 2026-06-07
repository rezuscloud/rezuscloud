# CLI Reference

## Global Options

| Flag | Default | Description |
|---|---|---|
| `--cluster`, `-c` | — | Tenant cluster name (scopes resource operations) |
| `--output`, `-o` | `table` | Output format: table, wide, yaml, json |
| `--selector`, `-l` | — | Label selector (key=value) |
| `--rezusconfig` | `~/.rezuscloud/config` | Path to config file |

## Commands

### `rezusctl boot`

Bootstrap the management cluster. This is the only standalone command — all others require a running management plane.

```bash
rezusctl boot --platform <docker|qemu> [--name <name>]
```

| Flag | Default | Description |
|---|---|---|
| `--platform` | `docker` | Target platform: `docker` or `qemu` |
| `--name` | `rezuscloud` | Cluster name |
| `--control-planes` | `1` | Number of control plane nodes |
| `--workers` | `0` | Number of worker nodes |
| `--talos-version` | `latest` | Talos version |
| `--cilium-version` | `1.19.3` | Cilium version |
| `--state-dir` | `.rezusctl` | State directory |

Boot is **idempotent** — re-run skips completed steps. Drift detection re-provisions if needed.

### Generic Resource Commands

#### `rezusctl get <type> [<name>]`

List or describe resources.

```bash
rezusctl get clusters                  # List all tenants
rezusctl get cluster prod              # Get single tenant
rezusctl get machines                  # List all machines
rezusctl get machines -c prod          # List machines in cluster prod
rezusctl get ng -c prod                # List node groups
rezusctl get providers                 # List providers
```

#### `rezusctl create <type> [<name>]`

Create a resource from stdin or flags.

```bash
rezusctl create cluster prod
```

#### `rezusctl apply -f <file>`

Create or update a resource from a YAML/JSON file.

#### `rezusctl delete <type> <name>`

Delete a resource.

```bash
rezusctl delete cluster prod
rezusctl delete jointoken abc123 -c prod
```

#### `rezusctl describe <type> <name>`

Show detailed resource information.

### Specialized Commands

#### `rezusctl cluster`

```bash
rezusctl cluster status <name>     # Formatted cluster status
rezusctl cluster delete <name>     # Delete cluster
```

#### `rezusctl machine`

```bash
rezusctl machine list              # Cluster-wide
rezusctl machine list -c prod      # Tenant-scoped
rezusctl machine get <id>          # Machine details
```

#### `rezusctl logs <machine-id> -c <cluster>`

Stream machine logs via SSE.

```bash
rezusctl logs hw-001 -c prod
rezusctl logs hw-001 -c prod -f    # Follow
rezusctl logs hw-001 -c prod --tail 50  # Last 50 lines
```

#### `rezusctl kubeconfig <cluster>`

Fetch kubeconfig for a tenant cluster.

```bash
rezusctl kubeconfig prod
rezusctl kubeconfig prod -o ~/kubeconfigs/prod.yaml
```

#### `rezusctl talosconfig <cluster>`

Fetch talosconfig for a tenant cluster.

#### `rezusctl jointoken`

```bash
rezusctl jointoken create -c prod --node-group workers
rezusctl jointoken list -c prod
rezusctl jointoken delete <name> -c prod
```

#### `rezusctl user`

```bash
rezusctl user create <name> --role admin --password <pass>
rezusctl user list
rezusctl user delete <name>
```

### `rezusctl config`

Manage CLI configuration.

```bash
rezusctl config url <url>          # Set management plane URL
rezusctl config token <token>      # Set auth token
rezusctl config context <name>     # Switch context
rezusctl config contexts           # List contexts
rezusctl config info               # Show current config
```

### `rezusctl api-resources`

List all known resource types and their API paths.

### `rezusctl version`

Print version information.

## Resource Type Short Names

| Short | Full | Scoped |
|-------|------|--------|
| `cluster` | Cluster | No |
| `machines` | Machine | No |
| `ng` | NodeGroup | Yes (requires -c) |
| `jt` | JoinToken | Yes (requires -c) |
| `patch` | ConfigPatch | Yes (requires -c) |
| `provider` | Provider | No |
| `user` | User | No |

## Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | General error |
| 2 | Command misuse (invalid flags, missing arguments) |

## Configuration

| File | Location | Purpose |
|---|---|---|
| `~/.rezuscloud/config` | Home dir | Contexts, URL, token (YAML) |
| `.rezusctl/` | Working dir | Boot state (step markers, kubeconfig) |
