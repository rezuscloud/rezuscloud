# rezuscloud/docs

This directory holds **architecture decision records and contributor docs** only.

## What lives here

| Path | Contents |
|---|---|
| `adr/` | Live Architectural Decision Records (co-versioned with the code). Start at the [ADR index](adr/README.md). |
| `architecture-history/` | Superseded/archived ADRs (frozen history). |
| `testing/` | Internal testing docs (e.g. the QEMU E2E harness). |
| `documentation-standards.md` | The Diátaxis standards this project follows. |

## Where the user-facing docs went

Tutorials, how-to guides, reference, concepts, and operations have moved to the
**[rezuscloud wiki](https://github.com/rezuscloud/rezuscloud/wiki)**, organized by
[Diátaxis](https://diataxis.fr/). The public docs site
([docs.rezus.cloud](https://docs.rezus.cloud)) renders that wiki.

ADRs stay here, co-located with the code they govern and deeply cross-referenced
from `CONTEXT.md`, commits, and PRs.
