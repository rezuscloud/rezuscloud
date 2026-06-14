//go:build integration

package openstack

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Integration tests prove the generated `.tf.json` is a structurally valid
// OpenTofu configuration, using the REAL `tofu` binary. `//go:build integration`
// + `TestIntegration_*` so the CI job runs them.
// Run locally: go test -tags=integration -run '^TestIntegration' ./internal/provider/openstack/

func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

type validateSummary struct {
	Valid       bool `json:"valid"`
	ErrorCount  int  `json:"error_count"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
	} `json:"diagnostics"`
}

// tofuValidate writes the config to a temp dir and runs `tofu validate -json`.
// It does NOT run `tofu init`, so providers aren't downloaded — that's
// deliberate: we assert structural validity, not provider availability.
func tofuValidate(t *testing.T, tfjson []byte) validateSummary {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf.json"), tfjson, 0o644); err != nil {
		t.Fatalf("write main.tf.json: %v", err)
	}
	cmd := exec.Command("tofu", "validate", "-json")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	var s validateSummary
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatalf("could not parse `tofu validate -json` output:\n%s\nerr=%v", out, err)
	}
	return s
}

// structuralErrors filters out the expected "provider not installed" diagnostics
// and returns only diagnostics that indicate the CONFIG itself is malformed.
func structuralErrors(s validateSummary) []string {
	const providerMissing = "Missing required provider"
	var errs []string
	for _, d := range s.Diagnostics {
		if d.Severity != "error" {
			continue
		}
		if strings.HasPrefix(d.Summary, providerMissing) {
			continue
		}
		errs = append(errs, d.Summary)
	}
	return errs
}

func TestIntegration_RenderedConfigIsValidTF(t *testing.T) {
	skipWithoutTofu(t)

	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	ng := state.NodeGroupSpec{
		Name:          "workers",
		Role:          "worker",
		Count:         2,
		TalosVersion:  "v1.13.4",
		ProviderClass: "openstack:SCS-16V-32-100",
		ProviderConfig: []byte(`{
			"bootVolumeSizeGb": 50,
			"dataVolumeSizeGb": 250
		}`),
	}
	out, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: []state.NodeGroupSpec{ng}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := tofuValidate(t, out)
	if errs := structuralErrors(s); len(errs) > 0 {
		t.Fatalf("generated config has structural TF errors (providers-missing is OK):\n%v\n--- config ---\n%s", errs, out)
	}
}

func TestIntegration_NoDataVolumeConfigIsValidTF(t *testing.T) {
	// Cover the single-volume (no data volume) path.
	skipWithoutTofu(t)

	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	ng := state.NodeGroupSpec{
		Name:          "controlplane",
		Role:          "controlplane",
		Count:         1,
		TalosVersion:  "v1.13.4",
		ProviderClass: "openstack:SCS-4V-8-50",
		ProviderConfig: []byte(`{
			"bootVolumeSizeGb": 50,
			"dataVolumeSizeGb": 0
		}`),
	}
	out, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: []state.NodeGroupSpec{ng}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := tofuValidate(t, out)
	if errs := structuralErrors(s); len(errs) > 0 {
		t.Fatalf("generated config has structural TF errors:\n%v\n--- config ---\n%s", errs, out)
	}
}
