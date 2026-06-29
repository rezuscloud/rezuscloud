//go:build integration

package projection

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/provider/metal"
	"github.com/rezuscloud/rezuscloud/internal/provider/oci"
	"github.com/rezuscloud/rezuscloud/internal/tfbackend"

	_ "modernc.org/sqlite"
)

// Integration tests prove the projection consumes REAL `tofu state pull` blobs
// (the exact shape tofu produces), not just hand-built JSON. We apply a small
// config via the #84 backend, then project the resulting state and assert the
// resources appear with correct spec fields.
// `//go:build integration` + `TestIntegration_*` so the CI job runs them.
// Run locally: go test -tags=integration -run '^TestIntegration' ./internal/projection/

func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

// backendSource reads state straight from the #84 tfbackend store. Production
// would wrap tfexec.StatePull (decrypts), but for an unencrypted integration
// test the raw store blob is the plaintext state.
type backendSource struct {
	store *tfbackend.Store
}

func (b backendSource) State(ctx context.Context, tenant string) ([]byte, error) {
	blob, found, err := b.store.GetState(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return blob, nil
}

// TestIntegration_ProjectsRealTofuState is the headline proof: apply real TF
// config via the #84 backend, then project the resulting state and assert the
// resources appear with the right Kind + spec fields. This exercises the FULL
// pipeline (tofu apply → state blob → parseState → extractor → index).
func TestIntegration_ProjectsRealTofuState(t *testing.T) {
	skipWithoutTofu(t)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := tfbackend.New(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tfbackend.NewHandler(store))
	t.Cleanup(srv.Close)

	// Use the real OCI provider so oci_core_instance maps to Machine.
	registry := provider.NewRegistry()
	registry.Register(oci.New())
	registry.Register(metal.New())
	idx := New(backendSource{store: store}, registry)
	idx.RegisterExtractor("Machine", extractMachine)

	// Write a tiny TF config + apply it. We use null_resource (always works,
	// no provider downloads) but ALSO register it as a Machine mapping ad hoc
	// so the projection picks it up — proving the index drives off real state.
	//
	// (We can't apply real oci/openstack resources without cloud creds; this
	// proves the state-parsing + projection pipeline against real tofu output.
	// The extractor shapes for the real resource types are unit-tested in
	// index_test.go against real attribute shapes.)
	tenant := "personal"
	dir := t.TempDir()
	cfg := `terraform {
  required_providers { null = { source = "registry.opentofu.org/hashicorp/null" } }
}
resource "null_resource" "node" {
  triggers = {
    hostname = "real-node-1"
    ip       = "10.0.0.42"
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tofu", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "Failed to install provider") {
			t.Skipf("null provider unavailable; skipping:\n%s", out)
		}
		t.Fatalf("init: %v\n%s", err, out)
	}
	cmd = exec.Command("tofu", "apply", "-auto-approve", "-input=false")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// Push the resulting state into the backend.
	cmd = exec.Command("tofu", "state", "pull")
	cmd.Dir = dir
	stateBlob, err := cmd.Output()
	if err != nil {
		t.Fatalf("state pull: %v", err)
	}
	if err := store.PutState(context.Background(), tenant, stateBlob); err != nil {
		t.Fatal(err)
	}

	// null_resource isn't mapped by oci/metal, so the index skips it. Register
	// an ad-hoc mapping + extractor so we prove the pipeline end-to-end.
	registry.Register(nullResourceProvider{}) // maps null_resource → Machine
	idx.RegisterExtractor("Machine", extractNullResource)

	n, err := idx.Rebuild(context.Background(), tenant)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if n != 1 {
		t.Fatalf("projected %d resources, want 1 (the null_resource.node)", n)
	}
	got, ok := idx.Lookup(tenant, "null_resource", "node")
	if !ok {
		t.Fatal("Lookup null_resource.node failed")
	}
	if got.Kind != "Machine" {
		t.Errorf("Kind = %q, want Machine", got.Kind)
	}
	if got.Spec["hostname"] != "real-node-1" {
		t.Errorf("hostname = %v, want real-node-1", got.Spec["hostname"])
	}
	if got.StateSerial == 0 {
		t.Error("StateSerial = 0, want > 0 (proves serial was read from real state)")
	}
}

// nullResourceProvider maps null_resource → Machine for the integration test.
type nullResourceProvider struct{}

func (nullResourceProvider) Type() string { return "null" }
func (nullResourceProvider) Mappings() []provider.TFResourceMapping {
	return []provider.TFResourceMapping{{TFType: "null_resource", Kind: "Machine"}}
}
func (nullResourceProvider) Render(provider.RenderRequest) ([]byte, error) { return nil, nil }

// extractNullResource pulls hostname/ip from a null_resource's triggers map.
func extractNullResource(_ string, attrs map[string]interface{}) map[string]interface{} {
	triggers, ok := attrs["triggers"].(map[string]interface{})
	if !ok {
		return nil
	}
	return map[string]interface{}{
		"hostname": triggers["hostname"],
		"address":  triggers["ip"],
	}
}
