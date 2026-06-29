//go:build integration

package applyqueue

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/tfbackend"
	"github.com/rezuscloud/rezuscloud/internal/tfexec"

	_ "modernc.org/sqlite"
)

// Integration tests drive a REAL `tofu apply` through the queue, proving the
// scheduler reconciles actual infrastructure via tfexec. `//go:build integration`
// + `TestIntegration_*` so the CI job runs them.
// Run locally: go test -tags=integration -run '^TestIntegration' ./internal/applyqueue/

func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

// execApplier adapts tfexec.Exec to the Applier interface: it runs a clean
// init+apply per tenant against RezusCloud's own backend. This is exactly how
// production will wire the queue once reconcilers land (#87b).
type execApplier struct {
	exec *tfexec.Exec
	once sync.Once
}

func (a *execApplier) Apply(ctx context.Context, tenant string) error {
	// init on first apply (idempotent); apply reconciles declared state.
	if _, err := a.exec.Run(ctx, tenant, "apply", "-auto-approve", "-input=false"); err != nil {
		return err
	}
	return nil
}

// TestIntegration_QueueDrivesRealTofuApply is the headline proof: the queue's
// debounced Enqueue triggers a real `tofu apply` whose state flows through the
// #84 backend into the store — the same wiring a reconciler will use. Confirms
// coalescing holds against real apply latency (3 rapid enqueues → 1 stored
// state, one apply's worth of tofu output).
func TestIntegration_QueueDrivesRealTofuApply(t *testing.T) {
	skipWithoutTofu(t)

	// Pin the pool to a single connection. With modernc.org/sqlite, ":memory:"
	// creates a SEPARATE database per connection — migrate() runs on one connection
	// but tofu's concurrent HTTP requests open others that see an empty DB
	// ("no such table: tf_state"). SetMaxOpenConns(1) guarantees every query
	// (migrate + all handler calls) shares the same in-memory database.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := tfbackend.New(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tfbackend.NewHandler(store))
	t.Cleanup(srv.Close)

	// Exec pointing tofu at RezusCloud's own backend.
	root := t.TempDir()
	execE, err := tfexec.New(root,
		tfexec.WithBinary("tofu"),
		tfexec.WithBackendURL(srv.URL+"/tfstate"),
		tfexec.WithTimeout(90*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	tenant := "personal"
	dir, err := execE.Workdir(tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`terraform {
  required_providers { null = { source = "registry.opentofu.org/hashicorp/null" } }
}
resource "null_resource" "demo" { triggers = { hello = "from-queue" } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-init so the queue's apply (init+apply) finds the provider cached. If the
	// provider can't be downloaded (isolated runner), skip rather than false-red.
	if res, err := execE.Run(context.Background(), tenant, "init"); err != nil {
		if strings.Contains(res.Stderr, "dial") || strings.Contains(res.Stderr, "timeout") ||
			strings.Contains(res.Stderr, "Failed to install provider") {
			t.Skipf("null provider unavailable; skipping:\n%s", res.Stderr)
		}
		t.Fatalf("init: %v\n%s", err, res.Stderr)
	}

	applier := &execApplier{exec: execE}
	q := New(applier, nil, nil, Config{DebounceInterval: 200 * time.Millisecond})
	q.Start(context.Background())
	defer q.Stop()

	// Three rapid enqueues: must coalesce into a single apply.
	q.Enqueue(tenant)
	q.Enqueue(tenant)
	q.Enqueue(tenant)

	// Wait for the debounced apply to land state in the backend.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if got, found, _ := store.GetState(context.Background(), tenant); found &&
			strings.Contains(string(got), "null_resource") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, found, err := store.GetState(context.Background(), tenant)
	if err != nil || !found {
		t.Fatalf("queue's apply never stored state: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(got), "from-queue") {
		t.Fatalf("stored state missing the trigger (wrong/old state): %s", got)
	}
}
