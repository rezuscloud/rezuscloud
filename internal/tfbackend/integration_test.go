//go:build integration

package tfbackend

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Integration tests exercise the real `tofu` binary against our HTTP backend.
//
// Convention (see CONTRIBUTING.md): each carries the `integration` build tag
// AND the `TestIntegration_*` name, so the CI `integration-test` job selects
// them with `-run '^TestIntegration'`. Run locally:
//
//	go test -tags=integration -run '^TestIntegration' ./internal/tfbackend/
//
// They skip cleanly when `tofu` is not on PATH.

// tofuBackendEnv spins up a real handler over an in-memory store and returns
// the server, store, and a workdir pre-seeded with a `backend "http"` block.
func tofuBackendEnv(t *testing.T) (store *Store, workdir string) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err = New(db)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHandler(store))
	t.Cleanup(srv.Close)

	addr, err := url.JoinPath(srv.URL, "/tfstate")
	if err != nil {
		t.Fatal(err)
	}
	// Minimal backend block: tofu's defaults (LOCK/UNLOCK/POST) match our handler.
	backendBlock := `terraform {
  backend "http" {
    address = "` + addr + `"
  }
}
`
	workdir = t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "backend.tf"), []byte(backendBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	return store, workdir
}

// tofuRun runs `tofu <args>` in dir, failing the test (with output) on error.
func tofuRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "tofu", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tofu %v: %v\n%s", args, err, out)
	}
}

// tofuOut runs `tofu <args>` in dir and returns combined stdout.
func tofuOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "tofu", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tofu %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// skipWithoutTofu skips the test if the `tofu` binary is unavailable.
func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

// TestIntegration_RealTofuStatePushPull is the deterministic, network-free
// proof that our handler speaks tofu's HTTP backend wire protocol: `tofu init`
// (backend handshake + GET), `tofu state push` (LOCK → POST → UNLOCK), and
// `tofu state pull` (GET) round-trip an opaque state blob. No provider plugin
// is required, so it passes on fully isolated runners.
func TestIntegration_RealTofuStatePushPull(t *testing.T) {
	skipWithoutTofu(t)
	store, workdir := tofuBackendEnv(t)

	// A minimal valid state document (a single null_resource, no provider calls).
	const stateJSON = `{
  "version": 4,
  "terraform_version": "1.6.0",
  "serial": 42,
  "lineage": "test-lineage",
  "outputs": {},
  "resources": [
    {
      "mode": "managed",
      "type": "null_resource",
      "name": "demo",
      "provider": "provider[\"registry.opentofu.org/hashicorp/null\"]",
      "instances": [{"schema_version": 0, "attributes": {}, "sensitive_attributes": []}]
    }
  ]
}`
	statePath := filepath.Join(workdir, "state.json")
	if err := os.WriteFile(statePath, []byte(stateJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. init connects to the backend and reads state (GET → 404 first run).
	tofuRun(t, workdir, "init")

	// 2. push writes the state to the backend (LOCK → POST → UNLOCK).
	tofuRun(t, workdir, "state", "push", statePath)

	// 3. The store now holds the state for the "default" workspace.
	stored, found, err := store.GetState(context.Background(), "default")
	if err != nil || !found {
		t.Fatalf("state not stored after push: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(stored), `"null_resource"`) {
		t.Fatalf("stored state missing null_resource: %s", stored)
	}

	// 4. pull round-trips the same state tofu pushed.
	pulled := tofuOut(t, workdir, "state", "pull")
	if !strings.Contains(pulled, `"null_resource"`) {
		t.Fatalf("pulled state missing the resource: %s", pulled)
	}
}

// TestIntegration_RealTofuApplyLifecycle proves the full apply path — the exact
// workflow the #85 exec engine will invoke — against a real `tofu` binary:
//
//	tofu init  → downloads the null provider + configures the backend
//	tofu apply → LOCK → read state (GET, 404) → plan → create null_resource →
//	             POST the freshly-applied state → UNLOCK
//	tofu state pull → GET returns the applied state
//
// It requires network egress to registry.opentofu.org for the null provider.
// On an isolated runner it skips (t.Skip) so it never produces a false red;
// TestIntegration_RealTofuStatePushPull remains the deterministic baseline.
func TestIntegration_RealTofuApplyLifecycle(t *testing.T) {
	skipWithoutTofu(t)
	store, workdir := tofuBackendEnv(t)

	// A real config: requires the `null` provider plugin (downloaded by init).
	const mainTF = `terraform {
  required_providers {
    null = { source = "registry.opentofu.org/hashicorp/null" }
  }
}

resource "null_resource" "demo" {
  triggers = { hello = "world" }
}
`
	if err := os.WriteFile(filepath.Join(workdir, "main.tf"), []byte(mainTF), 0o644); err != nil {
		t.Fatal(err)
	}

	// init: backend handshake (GET) + provider download. A failure here is the
	// provider registry being unreachable — skip rather than false-red. (The
	// backend handshake itself is already proven by the push/pull test.)
	cmd := exec.CommandContext(context.Background(), "tofu", "init")
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "dial") ||
			strings.Contains(strings.ToLower(string(out)), "timeout") ||
			strings.Contains(strings.ToLower(string(out)), "no such host") ||
			strings.Contains(strings.ToLower(string(out)), "failed to download") ||
			strings.Contains(strings.ToLower(string(out)), "registry") {
			t.Skipf("null provider unavailable (no network to registry); skipping apply lifecycle test:\n%s", out)
		}
		t.Fatalf("tofu init: %v\n%s", err, out)
	}

	// apply: the full LOCK → plan → POST applied state → UNLOCK lifecycle.
	tofuRun(t, workdir, "apply", "-auto-approve", "-input=false")

	// The applied state must have landed in the store under "default".
	stored, found, err := store.GetState(context.Background(), "default")
	if err != nil || !found {
		t.Fatalf("state not stored after apply: found=%v err=%v", found, err)
	}
	storedStr := string(stored)
	if !strings.Contains(storedStr, `"null_resource"`) {
		t.Fatalf("applied state missing null_resource: %s", storedStr)
	}
	if !strings.Contains(storedStr, `"hello"`) {
		t.Fatalf("applied state missing the trigger attribute: %s", storedStr)
	}

	// pull: GET returns exactly the state apply wrote.
	pulled := tofuOut(t, workdir, "state", "pull")
	if !strings.Contains(pulled, `"null_resource"`) || !strings.Contains(pulled, `"hello"`) {
		t.Fatalf("pulled state does not match applied state: %s", pulled)
	}
}
