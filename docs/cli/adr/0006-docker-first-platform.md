# ADR 6: Docker-First Local Development Platform

## Status: Accepted

## Context

The boot orchestrator, health checks, Helm installs, and REST API handlers need testing without cloud credentials. Developers also need a fast local development experience. Three options:

1. **Cloud-first**: Build OCI platform, test with real credentials.
2. **Mock-first**: Build against interfaces with mock implementations.
3. **Docker-first**: Build a Docker platform that runs Talos in containers locally.

## Decision

Implement the Docker platform first. `rezusctl boot --platform docker` creates a local Talos cluster in Docker containers, installs Cilium, and deploys the management plane. This validates the entire orchestration pipeline without cloud credentials.

## Consequences

- Full end-to-end testing on any machine with Docker. No cloud account needed.
- Talos in Docker has limitations: no `upgrade`, no `reset`, no VIP support on macOS.
- Docker containers are not real VMs — some Talos features won't work (disk management, hardware detection).
- The Docker platform validates orchestration logic. Cloud-specific logic still needs cloud credentials to test.
- Docker containers use `USERDATA` env var for config, not SideroLink (same-network shortcut).
