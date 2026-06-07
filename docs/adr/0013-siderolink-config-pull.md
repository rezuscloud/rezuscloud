# ADR 13: SideroLink-Based Config Pull for Machine Bootstrap

## Status: Accepted

## Amends

- **ADR 12** (Provider-Based Machine Provisioning) — Simplifies the provider interface. Providers no longer apply Talos config or build images. They only create/delete machines. Config delivery moves from push (provider applies) to pull (machine connects via SideroLink and receives config).

## Context

ADR 12 established the pluggable provider model: standalone binaries connect outbound to the management cluster and provision machines. The original design had providers doing three things:

1. **Provision** — Create VMs/servers via cloud API, IPMI, PXE, etc.
2. **Apply config** — Push Talos machine config to the provisioned machine
3. **Report status** — Report machine state back to the controller

This creates problems:

- **Provider complexity**: Each provider must understand Talos config application across different platforms
- **Config timing**: The controller must generate the Talos config before the provider can apply it — synchronous handoff
- **Network dependency**: The provider must be able to reach the machine. For machines behind NAT, the provider must be on the same LAN
- **Image management**: Each provider must handle Talos image building/uploading for its platform

The alternative is a **pull model**: machines boot a minimal Talos image that knows how to reach the management cluster. The management cluster delivers the full config over an encrypted tunnel via **SideroLink**.

## Decision

Use SideroLink for machine config delivery. Machines boot a Talos image with the management cluster's SideroLink endpoint baked into the kernel arguments. After boot, the machine establishes a WireGuard tunnel to the management cluster and receives its full Talos config.

The provider interface simplifies to two operations:

```go
type Provider interface {
    Info() ProviderInfo
    Provision(ctx context.Context, spec NodeGroupSpec) ([]Machine, error)
    Destroy(ctx context.Context, tenant string) error
}
```

No `ApplyConfig`. No `Status`. No `BuildImage`. Providers only create and delete machines.

### How SideroLink Works

SideroLink is a Talos kernel feature for secure config delivery. The flow:

```
Step 1: Machine boots Talos image with kernel args:
        siderolink.api=https://manage.rezus.cloud:443?jointoken=<token>&wireguard_over_grpc=true

Step 2: Talos gRPC call to SideroLink server:
        Provision(nodeUUID, nodePublicKey, joinToken, wireguardOverGRPC=true)

Step 3: Server responds with WireGuard config:
        serverEndpoint, serverPublicKey, nodeAddress, serverAddress

Step 4: WireGuard tunnel established over gRPC (TCP only, no UDP needed).
        Machine now has a secure link to the management cluster.

Step 5: Management cluster pushes full Talos config over the WireGuard tunnel.
        Config includes: cluster CA, bootstrap token, CNI settings,
        kubelet image, install disk, etc.

Step 6: Machine applies config, joins tenant cluster.
```

The `wireguard_over_grpc=true` option tunnels WireGuard traffic inside the gRPC connection:
- **No UDP required** — works behind any corporate firewall, CGNAT, or restrictive NAT
- **No port forwarding** — the machine connects outbound
- **TCP-only** — the only requirement is outbound HTTPS to the management cluster endpoint

### Architecture

```
Provider (anywhere)            Management Cluster                    Machine (anywhere)
┌───────────────────┐         ┌─────────────────────────────┐      ┌──────────────────────┐
│ provider-hetzner  │         │ SideroLink Server (gRPC)    │      │ Talos Image           │
│                   │         │ - Provision RPC             │      │ kernel args:          │
│ 1. Create VM with │         │ - WireGuard peer mgmt       │      │   siderolink.api=     │
│    RezusCloud     │         │ - Join token validation     │      │   manage.rezus.cloud  │
│    Talos image    │         │                             │      │                       │
│    (has SideroLink│         │ Config Provider (gRPC/WG)   │      │ 4. Boots → connects   │
│     kernel args)  │         │ - Waits for machine connect │◄─────│    to SideroLink      │
│                   │         │ - Generates Talos config    │      │                       │
│ 2. Reports IPs    │         │ - Pushes config over WG     │──────│ 5. Receives config    │
│    back           │         │                             │      │    over WireGuard      │
└───────────────────┘         │ RezusCloud Controller       │      │                       │
                              │ - Watches Tenant resources  │      │ 6. Applies config     │
                              │ - Dispatches to providers   │      │    joins tenant       │
                              │ - Triggers config generation│      │    cluster             │
                              └─────────────────────────────┘      └──────────────────────┘
                                        ▲
                                        │ Reverse proxy
                                        │ (Cloudflare/ngrok/nginx)
                                        │
                              ┌─────────┴──────────┐
                              │ Public endpoint     │
                              │ grpc.manage.        │
                              │ rezus.cloud:443     │
                              └────────────────────┘
```

### What We Import

| Component | Import Path | Purpose |
|-----------|------------|---------|
| Protobuf definitions | `siderolink/api/` | `ProvisionService` RPC |
| WireGuard peer management | `siderolink/pkg/wireguard/` | Peer event handling |
| gRPC-over-WireGuard tunnel | `siderolink/pkg/wgtunnel/` | Tunnel WG inside gRPC |
| Talos client-side | Built into Talos | No changes needed |

We import SideroLink Go packages as a library, not a fork.

### Join Token Matching

When a machine connects via SideroLink, the management cluster maps it to a tenant:

1. **Join token encoding**: The join token in the SideroLink kernel args is unique per provisioning request. When the machine connects with that token, the management cluster maps it to the correct tenant and node group.
2. **Machine UUID mapping**: The provider reports the machine's UUID. When a machine connects with that UUID, the mapping is confirmed.

### Docker Platform Exception

Docker containers receive config via the `USERDATA` environment variable. SideroLink is not used for Docker containers because they are on the same network as the controller.

### Image Building

Machines need a Talos image with the management cluster's SideroLink endpoint baked into kernel arguments. The schematic is:

```yaml
customization:
  extraKernelArgs:
    - siderolink.api=https://manage.rezus.cloud:443?jointoken=<token>&wireguard_over_grpc=true
  systemExtensions:
    officialExtensions:
      - siderolabs/qemu-guest-agent
```

Image sources by platform:

| Platform | Image source |
|----------|-------------|
| **Hetzner** | Image Factory ISO → hcloud upload |
| **OCI** | Image Factory QCOW2 → OCI import |
| **AWS** | Image Factory AMI → AWS import |
| **Metal (PXE)** | Image Factory PXE URL |
| **Static** | User downloads from Image Factory |
| **Docker** | Standard Talos container (no SideroLink) |

## Consequences

- **Providers are minimal**: Each provider only implements `Provision` and `Destroy`. No Talos config knowledge, no image building, no `talosctl` dependency.
- **Config delivery is network-agnostic**: Machines behind NAT, CGNAT, restrictive firewalls all receive config via SideroLink's gRPC-over-TCP tunnel.
- **Talos images require a schematic per management cluster**: Changing the SideroLink endpoint requires rebuilding images.
- **Join tokens are the machine-to-tenant mapping**: Each provisioning request gets a unique join token.
- **Config generation is deferred**: Happens when the machine connects, which may be seconds or minutes after VM creation.

## See Also

- [ADR 12: Provider-Based Machine Provisioning](0012-provider-based-machine-provisioning.md)
- [ADR 14: Full Talos Cluster Lifecycle](0014-full-talos-cluster-lifecycle.md)
