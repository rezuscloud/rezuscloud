# ADR 0002: Two Binaries From One Repo (rezuscloud + rezusctl)

## Status

Accepted

## Context

RezusCloud has two distinct runtime shapes (see
[ADR 0001](0001-what-rezuscloud-is.md)):

- A long-running **management plane** (REST API, WebUI, reconciliation loop).
- A **CLI tool** used for bootstrapping and convenience operations.

These share most of their domain logic (resource types, API client, Talos
config generation) but have different entry points, distribution, and
lifecycles. The question is how to factor them across binaries.

## Decision

**One repository, two binaries**, following the `kubectl`-in-`kubernetes`
model:

| Binary | Role | Distribution |
|---|---|---|
| `rezuscloud` | Management plane server — REST API, WebUI, TF execution engine, TF HTTP backend, reconciliation | Container image + static binary |
| `rezusctl` | CLI client — `boot` (standalone), all other commands talk to the running `rezuscloud` REST API | Static binary only |

Both share the same Go module, the same version, and the same release. The
release pipeline produces both binary archives plus one container image
(server only).

### Responsibility split

- **`rezuscloud`** owns everything that must be long-running: the HTTP API,
  the WebUI, the TF HTTP backend, the reconciliation loop, the state store.
- **`rezusctl`** owns the bootstrap flow (`rezusctl boot` — the one command
  that runs with no API server present) and thin client commands that call the
  REST API.

## Consequences

- **One source tree, one test suite, one release.** No cross-repo
  coordination; shared packages live under `internal/`.
- **`rezusctl boot` is the only standalone CLI command.** Everything else
  assumes a running `rezuscloud`.
- **The CLI is an API client, not a Kubernetes client.** It talks to the
  REST API; it never imports a Kubernetes client to manage tenant objects
  directly. This keeps the layering rule from
  [ADR 0001](0001-what-rezuscloud-is.md) honest.
- **Build separation is clean:** `go build -o rezuscloud .` and
  `go build -o rezusctl ./cmd/rezusctl`.

## See Also

- [ADR 0001](0001-what-rezuscloud-is.md) — what RezusCloud is
- [ADR 0003](0003-rest-api-kubernetes-model.md) — the API the CLI talks to
