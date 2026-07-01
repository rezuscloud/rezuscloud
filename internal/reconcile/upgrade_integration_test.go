//go:build integration

package reconcile

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/tfbackend"
	"github.com/rezuscloud/rezuscloud/internal/tfexec"

	_ "modernc.org/sqlite"
)

// versionedProvider is a null_resource-based provider whose rendered config
// embeds a "version" trigger. It lets the upgrade integration test prove that
// a spec version change triggers the pre-apply UpgradeRunner BEFORE tofu apply.
type versionedProvider struct {
	mu  sync.Mutex
	ver string // the version the provider currently renders
}

func (p *versionedProvider) Type() string { return "null" }
func (p *versionedProvider) Mappings() []provider.TFResourceMapping {
	return nil // null_resource is unmapped; no projection needed
}
func (p *versionedProvider) Render(req provider.RenderRequest) ([]byte, error) {
	p.mu.Lock()
	ver := p.ver
	p.mu.Unlock()
	cfg := provider.NewTFConfig()
	cfg.AddRequiredProviders(provider.ReqProvider{Name: "null", Source: "hashicorp/null"})
	for _, ng := range req.NodeGroups {
		cfg.AddResource("null_resource", ng.Name, provider.Obj{
			"count": ng.Count,
			"triggers": provider.Obj{
				"tenant":  req.Tenant.Metadata.Name,
				"version": ver,
			},
		})
	}
	return json.Marshal(cfg)
}

// recordingUpgrader is an UpgradeRunner that records calls + can block until
// the test signals the upgrade is "done" (simulating a rolling upgrade that
// completes before the apply proceeds).
type recordingUpgrader struct {
	mu    sync.Mutex
	calls []upgradeCall
	done  chan struct{}
}

func newRecordingUpgrader() *recordingUpgrader {
	return &recordingUpgrader{done: make(chan struct{})}
}

func (r *recordingUpgrader) RunUpgrade(_ context.Context, tenant, component, currentVersion, targetVersion string) error {
	r.mu.Lock()
	r.calls = append(r.calls, upgradeCall{tenant, component, currentVersion, targetVersion})
	r.mu.Unlock()
	<-r.done // block until the test signals the upgrade completed
	return nil
}

func (r *recordingUpgrader) calls_() []upgradeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]upgradeCall(nil), r.calls...)
}

// TestIntegration_UpgradeHookFiresBeforeApply proves the core ADR 0006 /
// CONTEXT.md "Upgrades" invariant: when a tenant spec declares a version that
// differs from what machines report, the pre-apply upgrade hook runs BEFORE
// tofu apply. The apply must not proceed until the upgrade completes.
//
// This uses the real reconcile.Applier (the production apply path), a real
// tofu subprocess (null_resource), and a recording UpgradeRunner that blocks
// the apply until the test verifies the hook fired.
func TestIntegration_UpgradeHookFiresBeforeApply(t *testing.T) {
	skipWithoutTofu(t)

	// Pin pool to 1 conn (modernc.org/sqlite ":memory:" DB-per-conn issue).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store, err := state.NewFromDB(db)
	if err != nil {
		t.Fatal(err)
	}
	tfStore, err := tfbackend.New(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tfbackend.NewHandler(tfStore))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	execE, err := tfexec.New(root,
		tfexec.WithBinary("tofu"),
		tfexec.WithBackendURL(srv.URL+"/tfstate"),
		tfexec.WithTimeout(90*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	p := &versionedProvider{ver: "1.12.6"}
	registry := provider.NewRegistry()
	registry.Register(p)

	upgrader := newRecordingUpgrader()
	applier := NewApplier(execE, registry, store, WithUpgradeRunner(upgrader))

	// Seed a tenant + node group + a machine reporting the OLD version.
	_, err = store.CreateTenant("prod", state.TenantSpec{
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.13.0", // NEW version (spec drift)
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(ngSpecJSON{Count: 1, Role: "controlplane", ProviderClass: "null:test"})
	_, err = store.CreateResource("nodegroup", "cp", json.RawMessage(spec), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "prod", "rezuscloud.io/role": "controlplane"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Machine reports the OLD version → drift detected.
	machineSpec := state.MachineSpec{ManagementAddress: "127.0.0.1:50000", Connected: true}
	machineStatus := state.MachineStatus{Role: "controlplane", Stage: state.StageReady, Ready: true, TalosVersion: "1.12.6", K8sVersion: "1.35.0"}
	_, err = store.CreateResource("machine", "node-1", machineSpec, machineStatus,
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-init so the apply finds the null provider cached.
	dir, err := execE.Workdir("prod")
	if err != nil {
		t.Fatal(err)
	}
	p.ver = "1.12.6"
	if err := writeMainTF(dir, p, "prod"); err != nil {
		t.Fatal(err)
	}
	if res, err := execE.Run(context.Background(), "prod", "init"); err != nil {
		if strings.Contains(res.Stderr, "dial") || strings.Contains(res.Stderr, "timeout") ||
			strings.Contains(res.Stderr, "Failed to install provider") {
			t.Skipf("null provider unavailable; skipping:\n%s", res.Stderr)
		}
		t.Fatalf("init: %v\n%s", err, res.Stderr)
	}

	// Run Apply in a goroutine — it should BLOCK on the upgrade hook.
	applyErr := make(chan error, 1)
	go func() {
		applyErr <- applier.Apply(context.Background(), "prod")
	}()

	// Wait for the upgrade hook to fire (proves the apply reached the
	// pre-upgrade stage and is now blocked on the rolling upgrade).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(upgrader.calls_()) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	calls := upgrader.calls_()
	if len(calls) == 0 {
		close(upgrader.done) // unblock to avoid a hung goroutine
		t.Fatal("pre-apply upgrade hook did not fire before apply")
	}

	// Verify the Talos upgrade was requested (1.12.6 → 1.13.0).
	var talosCall *upgradeCall
	for i := range calls {
		if calls[i].component == "talos" {
			talosCall = &calls[i]
		}
	}
	if talosCall == nil {
		close(upgrader.done)
		t.Fatalf("no talos upgrade call; got %+v", calls)
	}
	if talosCall.currentVersion != "1.12.6" || talosCall.targetVersion != "1.13.0" {
		close(upgrader.done)
		t.Errorf("talos upgrade = %s→%s, want 1.12.6→1.13.0", talosCall.currentVersion, talosCall.targetVersion)
	}

	// The apply must still be blocked (upgrade hasn't completed).
	select {
	case err := <-applyErr:
		close(upgrader.done)
		t.Fatalf("apply completed before upgrade finished: %v", err)
	case <-time.After(500 * time.Millisecond):
		// expected: apply is blocked on the upgrade
	}

	// Signal the upgrade completed → the apply proceeds.
	p.ver = "1.13.0" // provider now renders the new version
	close(upgrader.done)

	select {
	case err := <-applyErr:
		if err != nil {
			t.Fatalf("apply after upgrade: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("apply never completed after upgrade signal")
	}

	// Verify state was stored (the apply ran).
	got, found, err := tfStore.GetState(context.Background(), "prod")
	if err != nil || !found {
		t.Fatalf("state not stored: found=%v err=%v", found, err)
	}
	if !strings.Contains(string(got), "null_resource") {
		t.Fatalf("state missing null_resource: %s", got)
	}
}

// writeMainTF writes a main.tf for pre-init. The applier rewrites it during
// Apply, but init needs a provider-config-bearing file present.
func writeMainTF(dir string, p *versionedProvider, tenant string) error {
	p.mu.Lock()
	ver := p.ver
	p.mu.Unlock()
	// Minimal null_resource config mirroring the provider's Render output.
	cfg := map[string]any{
		"terraform": map[string]any{
			"required_providers": map[string]any{
				"null": map[string]any{"source": "hashicorp/null"},
			},
		},
		"resource": map[string]any{
			"null_resource": map[string]any{
				"cp": map[string]any{
					"count": 1,
					"triggers": map[string]any{
						"tenant":  tenant,
						"version": ver,
					},
				},
			},
		},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "main.tf.json"), raw)
}

// writeFile writes data to path (thin wrapper over os.WriteFile).
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
