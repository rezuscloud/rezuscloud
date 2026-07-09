# Documentation Standards

> **Status:** Proposed. This document defines the target standard for RezusCloud
> documentation, the gap analysis, and the migration plan.

## 1. The Standards

### 1.1 Sources

RezusCloud documentation follows three authoritative references:

1. **Diátaxis** ([diataxis.fr](https://diataxis.fr/)) — A systematic framework that
   identifies four documentation types based on user needs. Adopted by Cloudflare,
   Gatsby, Django, Python, and many CNCF projects.

2. **Software Engineering at Google** (Chapter 10: Documentation) — Google's
   internal documentation practices: "documentation is like code," "optimize for
   the reader," "don't mix types," "delete dead docs."

3. **Google Developer Documentation Style Guide**
   ([developers.google.com/style](https://developers.google.com/style)) — Editorial
   conventions for clear, consistent technical writing.

4. **Kubernetes documentation** (kubernetes.io/docs) — The reference implementation
   of Diátaxis for a cloud-native infrastructure platform. Its structure is the
   benchmark: Setup → Concepts → Tasks → Tutorials → Reference.

### 1.2 The Diátaxis Framework

Documentation serves four distinct user needs. Each need maps to a documentation
type with its own purpose, style, and rules:

```
                    ┌─────────────────────────────────────────┐
                    │              WORK                       │
                    │                                         │
                    │   How-to guides           Reference     │
                    │   (task-oriented)         (information)  │
                    │   "How do I...?"          "What is...?"  │
     Practical      ├─────────────────────────────────────────┤  Theoretical
     steps          │                                         │  understanding
                    │   Tutorials              Explanation     │
                    │   (learning)             (understanding) │
                    │   "Show me how"          "Why does...?"  │
                    │                                         │
                    │              STUDY                      │
                    └─────────────────────────────────────────┘
```

| Type | Purpose | Audience | Style | Rule |
|------|---------|----------|-------|------|
| **Tutorials** | Learning by doing | Novice | Step-by-step, numbered, assumes nothing | Each step is one atomic user action |
| **How-to guides** | Solving a specific problem | User with some knowledge | Goal-oriented, direct, skip basics | Assume the reader knows the basics |
| **Reference** | Describing the machinery | Expert seeking facts | Precise, complete, structured, no opinion | List, don't narrate |
| **Explanation** | Understanding why | Anyone seeking context | Discursive, illustrates, gives context | Discuss alternatives and trade-offs |

**The golden rule:** every document belongs to exactly one type. Do not mix types.
A tutorial that explains concepts is a bad tutorial. A reference page that tells a
story is a bad reference.

### 1.3 Google's Principles

| Principle | Application |
|-----------|-------------|
| **Optimize for the reader** | Write for the audience, not yourself. Identify who reads it before writing. |
| **Documentation is like code** | Under source control, has owners, reviewed, changes with code, has issues tracked. |
| **Minimum viable documentation** | A few fresh, accurate pages beat a large pile of stale ones. Bonsai tree: alive but frequently trimmed. |
| **Update docs with code** | Change docs in the same PR as the code change. A reviewer can insist on it. |
| **Delete dead documentation** | Dead docs misinform, slow down, incite despair. Default to delete when migrating. |
| **Duplication is evil** | Link to canonical sources. Don't rewrite what already exists. |
| **Don't mix types** | Each document has one purpose. Break mixed-purpose docs into separate pages. |
| **Know your audience** | Seekers (know what they want, need consistency) vs Stumblers (vague idea, need clarity). |
| **Number steps in tutorials** | Every user action is a numbered step. Don't number system responses. |

## 2. Gap Analysis: Current vs Target

### 2.1 Current Structure

```
docs/
├── adr/                    # 16 ADRs + README index — ✅ excellent
├── architecture-history/   # Archived ADRs — ✅ correct (Google: delete dead docs)
├── concepts/               # architecture.md, components.md, api-design.md, multi-cluster.md — ✅ complete
├── how-to/                 # 9 task-oriented guides — ✅ complete library
├── operations/             # first-deployment.md, install-and-deploy.md — ✅ good
├── reference/              # cli.md, versioning.md, api/ (7 per-resource pages) — ✅ complete
├── testing/                # e2e-qemu.md — ✅ internal, not user-facing
├── tutorials/              # install-and-first-cluster.md — ✅ real step-by-step tutorial
└── documentation-standards.md  # This document
```

### 2.2 Assessment by Diátaxis Type

| Type | Current State | Gaps |
|------|---------------|------|
| **Tutorials** | `tutorials/install-and-first-cluster.md` is a real step-by-step tutorial: zero to `kubectl get nodes` in numbered atomic steps. | ✅ Complete. |
| **How-to guides** | `how-to/` contains 9 guides: deploy-on-oci, deploy-on-openstack, add-bare-metal-node, scale-node-group, upgrade-talos-version, manage-config-patches, enable-state-encryption, manage-users, integrate-home-assistant. | ✅ Complete. |
| **Reference** | `reference/cli.md` covers the CLI. `reference/versioning.md` covers versioning. `reference/api/` contains 7 per-resource pages (overview, tenants, node-groups, machines, config-patches, health, projected-state). `concepts/api-design.md` explains API design rationale. | ✅ Complete. |
| **Explanation** | `concepts/architecture.md` is a 125-line summary. `concepts/components.md` provides a k8s-style component breakdown. `concepts/multi-cluster.md` explains multi-cluster patterns. | ✅ Complete. |

### 2.3 Kubernetes Comparison

Kubernetes docs serve as the benchmark for a cloud-native infrastructure platform:

| Kubernetes section | RezusCloud equivalent | Status |
|--------------------|-----------------------|--------|
| `/docs/setup/` (production setup) | `operations/` | ✅ `first-deployment.md` + `install-and-deploy.md` |
| `/docs/concepts/overview/` (what is k8s) | `concepts/architecture.md` | ✅ Exists |
| `/docs/concepts/overview/components/` | `concepts/components.md` | ✅ Exists |
| `/docs/concepts/architecture/` (deep arch) | `concepts/architecture.md` | ⚠️ Same 125-line page |
| `/docs/tasks/` (task library) | `how-to/` | ✅ 9 guides |
| `/docs/tutorials/` (hello world) | `tutorials/install-and-first-cluster.md` | ✅ Exists |
| `/docs/reference/api/` | `reference/api/` | ✅ 7 per-resource pages |
| `/docs/reference/kubectl/` | `reference/cli.md` | ✅ Exists |
| `/docs/concepts/` (deep concepts) | `concepts/` | ✅ 4 pages (architecture, components, api-design, multi-cluster) |

## 3. Target Structure

### 3.1 Directory Taxonomy (Diátaxis)

```
docs/
├── tutorials/                    # Learning-oriented: step-by-step, assumes nothing
│   ├── install-and-first-cluster.md   # Zero to `kubectl get nodes` in 10 steps
│   └── add-a-worker-node-group.md     # Scale out with a second node group
│
├── how-to/                       # Task-oriented: solve a specific problem
│   ├── deploy-on-oci.md              # Create an OCI tenant with real credentials
│   ├── deploy-on-openstack.md        # Create an OpenStack tenant
│   ├── add-bare-metal-node.md        # Boot a node into maintenance mode, apply config
│   ├── scale-node-group.md           # Scale a node group up or down
│   ├── upgrade-talos-version.md      # Bump the Talos version, watch rolling upgrade
│   ├── manage-config-patches.md      # Create, apply, toggle config patches
│   ├── enable-state-encryption.md    # Set REZUSCLOUD_STATE_PASSPHRASE
│   ├── configure-backups.md          # S3 backup setup
│   ├── manage-users.md               # Create users, API tokens, assign roles
│   └── integrate-home-assistant.md   # (moved from integrations/)
│
├── reference/                    # Information-oriented: precise, complete, structured
│   ├── api/                           # API reference (one page per resource type)
│   │   ├── overview.md                   # Base URL, auth, pagination, error format
│   │   ├── tenants.md                    # CRUD + status subresource
│   │   ├── node-groups.md                # CRUD + scale subresource
│   │   ├── machines.md                   # List, get, actions (reboot, shutdown)
│   │   ├── config-patches.md             # CRUD + toggle
│   │   ├── providers.md                  # List
│   │   ├── projected-state.md            # TF-state projection endpoints
│   │   └── health.md                     # Health probe endpoints
│   ├── cli/                           # CLI reference (existing cli.md, expanded)
│   ├── configuration.md               # Environment variables, Helm values
│   └── metrics.md                     # Prometheus metrics exposed at /metrics
│
├── concepts/                     # Understanding-oriented: deep, discursive, "why"
│   ├── overview.md                    # What RezusCloud is (expanded architecture.md)
│   ├── components.md                  # Component breakdown (like k8s components page)
│   ├── architecture.md                # Two data planes, reconciliation lifecycle
│   ├── providers.md                   # How providers wrap TF providers
│   └── api-design.md                  # Why K8s-style REST (kept as explanation)
│
├── operations/                   # Production deployment + runbooks
│   ├── deploy-with-helm.md           # Helm chart installation
│   ├── first-deployment.md           # (existing — keep)
│   └── troubleshooting.md            # Common errors and fixes
│
├── adr/                          # Architecture Decision Records (existing — keep)
├── architecture-history/         # Archived ADRs (existing — keep)
└── testing/                      # Internal testing docs (existing — keep)
```

### 3.2 Document Template Standards

Every page must declare its type at the top (a comment or frontmatter), so
contributors know the rules:

#### Tutorial template
```markdown
# Tutorial: [outcome the reader achieves]

> **Type:** Tutorial · **Audience:** New user · **Assumes:** Nothing

In this tutorial, you will [concrete outcome]. By the end, you will have
[measurable result].

## Prerequisites

1. [Thing you must have before starting]
2. [Thing you must have before starting]

## Step 1: [First atomic user action]

[Explanation of what to do and why]

**Do this:**
```
[exact command or action]
```

[Expected result — what you should see]

## Step 2: ...

## Next steps

- [Link to related how-to guide]
- [Link to concept page for deeper understanding]
```

#### How-to template
```markdown
# How to [achieve specific goal]

> **Type:** How-to · **Audience:** User with basic knowledge

## Overview

[Brief: what this accomplishes and when you'd do it]

## Prerequisites

- [Assumed knowledge/setup]

## Steps

1. [Action]
2. [Action]

## Verification

[How to confirm it worked]
```

#### Reference template
```markdown
# [Resource name] API

> **Type:** Reference · **Audience:** API consumer

## Base URL

```
/api/v1/[resource]
```

## Endpoints

### List [resources]

```
GET /api/v1/[resource]
```

[Parameters table]

[Example request]

[Example response]

### Create [resource]

...
```

#### Concept template
```markdown
# [Concept name]

> **Type:** Explanation · **Audience:** Anyone seeking understanding

## Overview

[High-level: what this concept is and why it matters]

## How it works

[Deep explanation with diagrams]

## Trade-offs and alternatives

[Why this design over alternatives — link to ADRs]
```

## 4. Platform-Website Integration

### 4.1 Current state

The platform-website sources documentation from two repos via a `<!-- source: -->`
comment mechanism. It renders Markdown to HTML with syntax highlighting, Mermaid
diagrams, and a table of contents. The website has only 2 authored pages; the rest
are expected to come from the rezuscloud repo.

### 4.2 Target integration

The platform-website should:

1. **Mirror the rezuscloud docs taxonomy** — the website's `docs/` directory
   structure matches the rezuscloud `docs/` taxonomy exactly.

2. **Source docs from rezuscloud** — at build time, the website's docs pipeline
   reads from the rezuscloud repo (via git submodule, API, or build-time copy).
   The `<!-- source: -->` comment provides attribution and GitHub edit links.

3. **Add a navigation sidebar** organized by Diátaxis type — Tutorials, How-to,
   Reference, Concepts — so users can find content by intent.

4. **Keep website-exclusive pages separate** — high-level marketing pages
   ("What is RezusCloud?") stay in the platform-website repo. Technical
   documentation lives in the rezuscloud repo and is rendered by the website.

5. **Enforce doc freshness** — CI checks that every `docs/` page has a
   `<!-- source: -->` comment pointing to its canonical location, and that
   the rendered HTML matches the source.

## 5. Migration Plan

### Phase 1: Standards document (this doc)
- [x] Write the standards document
- [ ] Get approval

### Phase 2: Restructure existing docs
- [x] Rename `getting-started/` → `tutorials/` + `operations/`
- [x] Split `concepts/api-design.md` into reference (API endpoints) and explanation (API design rationale)
- [x] Move `integrations/home-assistant.md` → `how-to/`
- [x] Expand `concepts/architecture.md` into overview + components + architecture pages
- [x] Add Diátaxis type headers to every page

### Phase 3: Fill gaps
- [x] Write the "Install and first cluster" tutorial (the missing "Hello World")
- [x] Write the components page (the missing k8s-style component breakdown)
- [x] Write the API reference pages (one per resource type)
- [x] Write the how-to library (9 guides)
- [ ] Write the Helm deployment guide

### Phase 4: Platform-website
- [x] Build the docs sidebar navigation (by Diátaxis type)
- [x] Wire the docs pipeline to render the full rezuscloud docs set
- [ ] Add doc freshness CI check

### Phase 5: Doc-as-code practices
- [ ] Add a `docs/` check to CI (every PR that changes code must update docs)
- [ ] Assign doc owners (each section has a maintainer)
- [ ] Add stale-doc detection (last-updated date in frontmatter)
