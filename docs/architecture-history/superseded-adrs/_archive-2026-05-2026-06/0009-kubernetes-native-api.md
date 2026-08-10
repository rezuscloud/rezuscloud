# ADR 9: REST API Design

## Status: Accepted

## Context

RezusCloud needs an API for the CLI, WebUI, and programmatic access. The API must support CRUD operations on resources, real-time streaming, authentication, and structured errors.

## Decision

Implement a REST API following the Kubernetes API model:

- Per-type endpoints (`/api/v1/tenants`, `/api/v1/machines`, etc.)
- Three-section resource shape: `metadata` / `spec` / `status`
- Labels and annotations for filtering and ownership
- Structured errors with `status`, `message`, `reason`, `code`
- JWT authentication with view/edit/admin roles
- Watch endpoints with chunked JSON (CLI) and SSE (WebUI)
- Finalizers for graceful deletion

The API is served by the `rezuscloud` binary. The CLI (`rezusctl`) is an API client, not a Kubernetes client. The WebUI calls the REST API, not the Kubernetes API server.

## Why Not gRPC

REST is simpler for a personal cloud with ~7 resource types. The WebUI needs HTTP anyway (HTMX/SSE). gRPC adds protobuf generation, grpc-web proxies, and streaming complexity for marginal performance gains at this scale.

## Why Not Direct Kubernetes CRDs

The management plane runs standalone (SQLite) in Docker or Home Assistant — no Kubernetes required for single-node deployments. The REST API abstracts the state backend. Future CRD support is a state backend option, not the primary API surface.

## Consequences

- Single API surface for CLI, WebUI, and automation.
- JWT tokens authenticate all clients. No K8s service account dependency in standalone mode.
- Structured errors are consistent and machine-parseable.
- Watch/SSE enables real-time UI updates without polling.

## See Also

- [API Design Document](https://github.com/rezuscloud/rezuscloud/wiki/concepts/api-design.md) — Full endpoint specification
- [ADR 13: SideroLink-Based Config Pull](0013-siderolink-config-pull.md)
