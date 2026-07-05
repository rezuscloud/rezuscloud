# Versioning and Release Pipeline

> **Type:** Reference · **Audience:** Release engineer

## Overview

Versions are **automatically computed** by [GitVersion](https://gitversion.net/) from git history. No manual tagging needed — every merge to `main` produces a versioned release.

## How It Works

```
PR merged to main
       │
       ▼
┌─────────────────┐
│  CI Pipeline    │  lint → build (versioned) → test
└────────┬────────┘
         │ (also triggers)
         ▼
┌─────────────────┐
│  GitVersion     │  Reads git history → computes semVer
│                 │  e.g. 0.1.0, 0.1.1, 0.2.0, 1.0.0
└────────┬────────┘
         │ creates tag v{semVer}
         ▼
┌─────────────────┐
│  GoReleaser     │  Builds binaries + Docker image
│                 │  Publishes to GitHub Release + GHCR
└─────────────────┘
```

## Version Computation

GitVersion uses the `GitHubFlow/v1` workflow (`.github/GitVersion.yml`):

| Scenario | Result | Example |
|----------|--------|---------|
| Stable base `v0.0.1`, `fix:` commit | `0.0.2-1` | Patch bump |
| Stable base `v0.0.1`, `feat:` commit | `0.1.0-1` | Minor bump |
| Stable base `v0.0.1`, `feat!:` commit | `1.0.0-1` | Major bump |
| Stable base `v0.0.1`, `chore:` / `docs:` commit | `0.0.2-1` | Branch default (Patch) |
| Pre-release base `v0.0.1-35`, any commit | `0.0.1-36` | Counter increments |
| On a PR branch | `0.0.2-PullRequest42.1` | Pre-release |

### Stable vs pre-release cycles

GitVersion's default `ContinuousDelivery` mode treats pre-release tags (`v0.0.1-1` through `v0.0.1-35`) as "still working toward 0.0.1". Commit-message bumps are absorbed into the pre-release counter until the cycle is closed by a **stable tag**.

Once a stable tag exists (e.g., `v0.0.1`), the next commit on `main` triggers the configured bump direction and starts a new pre-release cycle:

```
v0.0.1   (stable)      ← closes the 0.0.1 cycle
v0.0.2-1 (pre-release) ← fix: commit starts the 0.0.2 cycle
v0.0.2-2 (pre-release) ← another fix: continues the cycle
v0.1.0-1 (pre-release) ← feat: commit starts the 0.1.0 cycle
v0.1.0   (stable)      ← closes the 0.1.0 cycle
v0.1.1-1 (pre-release) ← fix: starts the 0.1.1 cycle
```

The historical `v0.0.1-N` tags remain as record but no longer influence future calculations.

### Commit Message Bumping

GitVersion reads [conventional commit](https://www.conventionalcommits.org/) prefixes:

| Prefix | Bump | Example |
|--------|------|---------|
| `fix:` | Patch | `fix: correct helm chart path` |
| `perf:` | Patch | `perf: reduce memory in controller` |
| `feat:` | Minor | `feat: add edge node join` |
| `feat!:` or `BREAKING CHANGE:` | Major | `feat!: new resource schema` |
| `chore:`, `docs:`, `ci:`, `test:` | Patch (default) | `chore: update dependencies` |

### Alternative: `+semver` Directives

You can also use explicit directives in commit messages:

```
+semver: major    # force major bump
+semver: minor    # force minor bump
+semver: patch    # force patch bump
+semver: none     # skip bump entirely
```

## Version Injection

The `version/` package holds three variables injected at build time:

```go
package version

var (
    Version   = "dev"     // GitVersion semVer
    GitCommit = "unknown" // short SHA
    BuildTime = "unknown" // commit date
)
```

Injected via `-ldflags` in both CI and release builds:

```bash
go build -ldflags="
  -X github.com/rezuscloud/rezuscloud/version.Version=0.2.0
  -X github.com/rezuscloud/rezuscloud/version.GitCommit=abc1234
  -X github.com/rezuscloud/rezuscloud/version.BuildTime=2026-05-23
"
```

Displayed by `rezusctl version`:

```
$ rezusctl version
rezusctl 0.2.0
commit: abc1234
built: 2026-05-23
```

## Release Pipeline

### Automatic Release (push to main)

Triggered on every push to `main`. The release workflow:

1. **GitVersion** computes the version from git history
2. **Creates tag** `v{semVer}` via GitHub API (skips if tag already exists — prevents duplicates on re-runs)
3. **GoReleaser** checks out the tag, builds:
   - 4 binary archives: linux/darwin × amd64/arm64
   - Docker image pushed to `ghcr.io/rezuscloud/rezuscloud`
   - cosign-signed checksums + Syft SBOMs

### Snapshot Build (manual)

For testing the pipeline without publishing:

```bash
gh workflow run release.yml --repo rezuscloud/rezuscloud -f snapshot=true
```

Builds everything locally using GitVersion's computed version, but skips GitHub Release and GHCR push.

## Container Image

Each release pushes to `ghcr.io/rezuscloud/rezuscloud` with tags:

- `v0.2.0` (exact version)
- `v0.2` (minor)
- `v0` (major)
- `latest`

Image: distroless/static-debian12, nonroot user.

## CI Pipeline

`.github/workflows/ci.yml` runs on every push and PR to `main`:

| Job | Runner | What |
|-----|--------|------|
| lint | self-hosted ARM64 | gofmt, go vet, golangci-lint v2.12 |
| build | self-hosted ARM64 + X64 | GitVersion → versioned binary build |
| test | self-hosted ARM64 + X64 | Race detector enabled |

The build job uses GitVersion to stamp the version into the binary, so CI builds are also versioned.

## File Layout

```
.github/
├── Dockerfile.release      # Distroless container image
├── GitVersion.yml          # GitVersion config (GitHubFlow + conventional commits)
├── goreleaser.yaml         # GoReleaser build + Docker + signing config
└── workflows/
    ├── ci.yml              # CI: lint → build (versioned) → test
    └── release.yml         # Release: GitVersion → tag → GoReleaser → GHCR
```

## Typical Workflow

```bash
# 1. Create a feature branch
git checkout -b feat/edge-join

# 2. Make changes, commit with conventional prefix
git commit -m "feat: add edge node join command"

# 3. Push and create PR
git push -u origin feat/edge-join
gh pr create --repo rezuscloud/rezuscloud

# 4. CI runs on the PR (lint + build + test)

# 5. Merge the PR
gh pr merge --squash

# 6. Release pipeline triggers automatically:
#    GitVersion computes 0.2.0 (minor bump from feat:)
#    Creates tag v0.2.0
#    GoReleaser builds and publishes
#    ghcr.io/rezuscloud/rezuscloud:v0.2.0 is available

# 7. Verify
gh release view v0.2.0 --repo rezuscloud/rezuscloud
docker pull ghcr.io/rezuscloud/rezuscloud:v0.2.0
```

No manual tagging. No npm. No semantic-release. Git history is the source of truth.
