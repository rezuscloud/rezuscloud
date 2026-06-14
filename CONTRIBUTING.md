# Contributing to RezusCloud

## Testing discipline

**Every feature or fix ships with tests, and CI proves they pass.** A pull
request is not done until CI is green. Two test tiers are required:

| Tier | Purpose | Build tag | Name prefix | Run locally |
|------|---------|-----------|-------------|-------------|
| **Unit** | Fast, hermetic, deterministic. No network, no external binaries. | *(none)* | *(any)* | `go test -short ./...` |
| **Integration** | Proves the code works against real binaries / real protocols. | `//go:build integration` | `TestIntegration_*` | `go test -tags=integration -run '^TestIntegration' ./...` |

### Rules

1. **Unit tests are mandatory.** Cover the package logic with hermetic tests
   that run in the `test` CI job (`-short`). Use `httptest`, in-memory SQLite,
   fakes, and table-driven cases.

2. **Integration tests are mandatory whenever a feature touches an external
   boundary** — a real binary (`tofu`, `talosctl`, a CLI), a real protocol
   (HTTP wire format, gRPC), or a real subsystem (containerd, a kube-apiserver).
   Integration tests:
   - carry the `//go:build integration` build tag **and** the `TestIntegration_*`
     name (so the CI job can select exactly them),
   - skip cleanly when their dependency is missing (`exec.LookPath` → `t.Skip`),
   - must not require network egress or cloud credentials.

3. **CI runs both tiers.** The `test` job runs unit tests; the
   `integration-test` job runs `//go:build integration` tests. Both gate the PR
   image build (`docker-pr` depends on them). Add `integration-test` to your
   repository's required status checks so it gates merge too.

### Adding a new real-binary dependency

Install it in CI with the official action and pin the version in a single file:

- **OpenTofu** → `opentofu/setup-opentofu@v2` reading `.opentofu-version`,
  with `tofu_wrapper: false` so Go's `exec.LookPath` finds the plain binary.

The `.opentofu-version` file is the single source of truth for the OpenTofu
version — it is what CI tests against *and* what the bundled tofu (#85 exec
engine) will ship.

### Verifying a feature locally before pushing

```bash
gofmt -w . && go vet ./...
go test -race -short ./...                       # unit tier
go test -race -tags=integration -run '^TestIntegration' ./...   # integration tier
```

A pull request must show both green in CI before it is merged.
