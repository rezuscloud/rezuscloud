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

// TestIntegration_RealTofuBackendRoundTrip exercises the HTTP backend with the
// real `tofu` binary end-to-end (read + write + round-trip), WITHOUT needing
// network access or a provider plugin: it uses `tofu state push`/`pull`, which
// talk directly to the configured backend.
//
// Test name follows the convention in CONTRIBUTING.md (TestIntegration_* prefix
// + `integration` build tag) so the CI `integration-test` job selects it via
// `-run '^TestIntegration'`.
//
// Run locally with: go test -tags=integration -run '^TestIntegration' ./internal/tfbackend/
// Skipped if `tofu` is not on PATH.
func TestIntegration_RealTofuBackendRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHandler(store))
	t.Cleanup(srv.Close)

	// Encode the backend block pointing at our test server.
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

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "backend.tf"), []byte(backendBlock), 0o644); err != nil {
		t.Fatal(err)
	}

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

	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "tofu", args...)
		cmd.Dir = workdir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tofu %v: %v\n%s", args, err, out)
		}
	}

	// 1. init connects to the backend and reads state (GET → 404 first run).
	run("init")

	// 2. push writes the state to the backend (POST + LOCK/UNLOCK).
	run("state", "push", "state.json")

	// 3. The store now holds the state for the "default" workspace.
	stored, found, err := store.GetState(context.Background(), "default")
	if err != nil || !found {
		t.Fatalf("state not stored after push: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(stored), `"null_resource"`) {
		t.Fatalf("stored state missing null_resource: %s", stored)
	}

	// 4. pull round-trips the same state tofu pushed.
	pull := exec.CommandContext(context.Background(), "tofu", "state", "pull")
	pull.Dir = workdir
	pulled, err := pull.Output()
	if err != nil {
		t.Fatalf("tofu state pull: %v", err)
	}
	if !strings.Contains(string(pulled), `"null_resource"`) {
		t.Fatalf("pulled state missing the resource: %s", pulled)
	}
}
