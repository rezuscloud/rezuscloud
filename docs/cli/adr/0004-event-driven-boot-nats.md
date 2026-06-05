# ADR 4: Event Model — REST Watch and SSE

## Status: Accepted

## Context

RezusCloud needs real-time event delivery for boot progress, state changes, and dashboard updates. The original design used NATS as an event bus, but the REST API already provides a watch endpoint for state changes. NATS became redundant.

## Decision

Remove NATS entirely. Events use REST-based mechanisms:

### Boot Progress (CLI → stdout)
Boot progress is printed to stdout during `rezusctl boot`. No event bus needed — the CLI is attached to the terminal for the duration of boot.

### API Events (Watch + SSE)
The REST API provides watch endpoints for real-time state updates:
- `GET /api/v1/tenants?watch=true` — chunked JSON streaming for CLI
- `GET /api/v1/tenants?watch=true&sse=true` — Server-Sent Events for WebUI
- `GET /api/v1/tenants/{c}/machines/{id}/logs` — SSE machine log streaming

The event bus (`internal/watch/`) publishes resource change events. Subscribers receive them via the watch API.

### Real-Time WebUI Updates
The WebUI subscribes to SSE streams from the REST API. HTMX + Alpine.js process events and update the UI.

## Consequences

- One fewer infrastructure dependency. No NATS server, no NATS client library.
- Events are accessible to all API clients (CLI, WebUI, programmatic).
- The `internal/watch/` package provides the event bus with `WatchableStore` wrapper.
- Boot progress remains stdout-only.
