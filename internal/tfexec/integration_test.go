//go:build integration

package tfexec

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/tfbackend"

	_ "modernc.org/sqlite"
)

// Integration tests drive the REAL `tofu` binary through the Exec wrapper.
// They carry the `integration` build tag + `TestIntegration_*` name (see
// CONTRIBUTING.md) so the CI `integration-test` job runs them automatically.
// Run locally: go test -tags=integration -run '^TestIntegration' ./internal/tfexec/
// They skip cleanly when `tofu` is not on PATH.

func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

// backendEnv spins up a real #84 HTTP backend over an in-memory store and
// returns the store (to assert state landed) and the tenant-keyed endpoint URL
// to point tofu at.
func backendEnv(t *testing.T) (store *tfbackend.Store, endpoint string) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err = tfbackend.New(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tfbackend.NewHandler(store))
	t.Cleanup(srv.Close)
	return store, srv.URL + "/tfstate"
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIntegration_RealTofuVersion proves the wrapper shells out to a real tofu
// binary, captures its output, and surfaces a clean Result. It is the cheapest
// real-tofu proof and needs no provider download.
func TestIntegration_RealTofuVersion(t *testing.T) {
	skipWithoutTofu(t)
	e, err := New(t.TempDir(), WithBinary("tofu"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Run(context.Background(), "personal", "version")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Stdout, "OpenTofu") {
		t.Fatalf("stdout missing version banner: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
}

// TestIntegration_RealTofuInitBackendFalse satisfies the acceptance criterion
// `tfexec.Run(ctx, "test", "init", "-backend=false")` against a real config.
// It downloads the `null` provider (needs registry egress); it skips on
// isolated runners so it never false-reds.
func TestIntegration_RealTofuInitBackendFalse(t *testing.T) {
	skipWithoutTofu(t)
	e, err := New(t.TempDir(), WithBinary("tofu"), WithTimeout(60*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.Workdir("test")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, dir, "main.tf", `terraform {
  required_providers {
    null = { source = "registry.opentofu.org/hashicorp/null" }
  }
}
resource "null_resource" "demo" {}
`)

	res, err := e.Run(context.Background(), "test", "init", "-backend=false")
	if err != nil {
		// A network failure to download the null provider is an environment
		// limitation, not a wrapper bug — skip rather than false-red. The
		// backend wire protocol is already proven by tfbackend's own tests.
		if strings.Contains(res.Stderr, "dial") || strings.Contains(res.Stderr, "timeout") ||
			strings.Contains(res.Stderr, "no such host") || strings.Contains(res.Stderr, "Failed to install provider") {
			t.Skipf("null provider unavailable (no registry egress); skipping:\n%s", res.Stderr)
		}
		t.Fatalf("init: %v\n%s", err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("init exit = %d, want 0\n%s", res.ExitCode, res.Stderr)
	}
}

// TestIntegration_RealTofuApplyViaBackend is the headline end-to-end proof: the
// wrapper drives a real `tofu apply` whose state flows through RezusCloud's own
// #84 HTTP backend. Verifies the full wiring — wrapper → backend.tf → backend →
// store — and that the applied state round-trips. Acceptance criterion: "A
// trivial apply against the #84 backend stores state".
func TestIntegration_RealTofuApplyViaBackend(t *testing.T) {
	skipWithoutTofu(t)
	store, endpoint := backendEnv(t)

	root := t.TempDir()
	e, err := New(root,
		WithBinary("tofu"),
		WithBackendURL(endpoint),
		WithTimeout(90*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	tenant := "personal"
	dir, err := e.Workdir(tenant)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, dir, "main.tf", `terraform {
  required_providers {
    null = { source = "registry.opentofu.org/hashicorp/null" }
  }
}
resource "null_resource" "demo" {
  triggers = { hello = "world" }
}
`)

	run := func(args ...string) {
		t.Helper()
		res, err := e.Run(context.Background(), tenant, args...)
		if err != nil {
			if strings.Contains(res.Stderr, "dial") || strings.Contains(res.Stderr, "timeout") ||
				strings.Contains(res.Stderr, "no such host") || strings.Contains(res.Stderr, "Failed to install provider") {
				t.Skipf("null provider unavailable (no registry egress); skipping:\n%s", res.Stderr)
			}
			t.Fatalf("tofu %v: %v\n%s", args, err, res.Stderr)
		}
	}

	// init: configures the backend (handshake GET) + downloads the null provider.
	run("init")
	// apply: LOCK → plan → create null_resource → POST applied state → UNLOCK.
	run("apply", "-auto-approve", "-input=false")

	// The applied state must have landed in the #84 store under the tenant.
	got, found, err := store.GetState(context.Background(), tenant)
	if err != nil || !found {
		t.Fatalf("state not stored via backend: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(got), `"null_resource"`) {
		t.Fatalf("applied state missing null_resource: %s", got)
	}
	if !strings.Contains(string(got), `"hello"`) {
		t.Fatalf("applied state missing the trigger: %s", got)
	}
}
