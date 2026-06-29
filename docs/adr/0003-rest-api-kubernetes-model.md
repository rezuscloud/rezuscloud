# ADR 0003: REST API Following the Kubernetes Resource Model

## Status

Accepted

## Context

RezusCloud needs an API surface shared by the CLI, the WebUI, and programmatic
automation. The API must support resource CRUD, real-time streaming of changes,
authentication, and structured errors.

The Kubernetes API shape — `metadata` / `spec` / `status` three-section
resources, labels, annotations, finalizers, structured errors, optimistic
concurrency via `resourceVersion` — is well-understood, tool-friendly, and
maps cleanly onto declarative reconciliation.

## Decision

Implement a **REST API following the Kubernetes resource model**:

- Per-type endpoints: `/api/v1/tenants`, `/api/v1/machines`, etc.
- Three-section resource shape: `metadata` / `spec` / `status`
- Labels and annotations for filtering and ownership
- Structured errors: `{status, message, reason, code}`
- Optimistic concurrency via `metadata.resourceVersion`
- Finalizers for graceful deletion
- Watch/SSE endpoints for real-time updates (events delivered via NATS — see
  [ADR 0009](0009-event-bus-nats.md))

### Why REST, not gRPC

REST is simpler for a personal-cloud API surface. The WebUI needs HTTP anyway
(HTMX + SSE). gRPC would add protobuf generation, a grpc-web bridge for the
browser, and streaming complexity for no gain at this scale. (The earlier gRPC
provider-binary model is rejected — see
[`../architecture-history/`](../architecture-history/README.md).)

### Why not direct Kubernetes CRDs as the primary API

The management plane runs standalone (SQLite) in Docker or Home Assistant — no
Kubernetes is required to run RezusCloud. CRD support may arrive later as a
state-backend option (see [ADR 0004](0004-sqlite-state-store.md)), not as the
primary API shape.

## Consequences

- **Single API surface** for CLI, WebUI, and automation.
- **JWT + API-token auth** for all clients (see
  [ADR 0012](0012-auth-local-jwt-and-api-tokens.md)).
- **Watch/SSE** enables real-time WebUI updates without polling.
- **The `spec`/`status` split aligns with the two data planes** (see
  [ADR 0005](0005-tf-state-single-source-of-truth.md) and
  [ADR 0010](0010-status-plane-best-effort.md)): `spec` is the declared
  intent (sourced from TF state), `status` is best-effort observation.

## See Also

- [ADR 0004](0004-sqlite-state-store.md) — where API resources are stored
- [ADR 0005](0005-tf-state-single-source-of-truth.md) — how `spec` is sourced
- [ADR 0012](0012-auth-local-jwt-and-api-tokens.md) — authentication
