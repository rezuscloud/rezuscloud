//go:build integration

package oci

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
// Run locally: go test -tags=integration -run '^TestIntegration' ./internal/provider/oci/

func skipWithoutTofu(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tofu"); err != nil {
		t.Skip("tofu not on PATH")
	}
}

// validateSummary is the `tofu validate -json` output shape (subset we read).
type validateSummary struct {
	Valid        bool `json:"valid"`
	ErrorCount   int  `json:"error_count"`
	WarningCount int  `json:"warning_count"`
	Diagnostics  []struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
	} `json:"diagnostics"`
}

// tofuValidate writes the config to a temp dir and runs `tofu validate -json`.
// It does NOT run `tofu init`, so providers aren't downloaded — that's
// deliberate: we assert structural validity, not provider availability. A
// structurally-valid config reports ONLY "Missing required provider" errors;
// any typo / bad nesting / unknown key surfaces as a different diagnostic
// (e.g. "Extraneous JSON object property"), which fails the test.
func tofuValidate(t *testing.T, tfjson []byte) validateSummary {
	t.Helper()
	dir := t.TempDir()
	if err := writeMain(t, dir, tfjson); err != nil {
		t.Fatalf("write main.tf.json: %v", err)
	}
	cmd := exec.Command("tofu", "validate", "-json")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput() // validate exits 0 even when "valid": false
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
			continue // expected: providers aren't downloaded without `init`
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
		Name:          "controlplane",
		Role:          "controlplane",
		Count:         3,
		ProviderClass: "oci:VM.Standard.A1.Flex",
		ProviderConfig: []byte(`{
			"compartmentOcid": "ocid1.compartment.oc1..aaaaaaaa.demo",
			"subnetId":        "ocid1.subnet.oc1.phx.aaaaaaaa.demo",
			"imageOcid":       "ocid1.image.oc1.phx..talos",
			"nsgId":           "ocid1.networksecuritygroup.oc1.phx.aaaaaaaa.demo",
			"ocpus":           4,
			"memoryGb":        24
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

func TestIntegration_FixedShapeConfigIsValidTF(t *testing.T) {
	// Cover the shape_config-omitting path too (fixed shape).
	skipWithoutTofu(t)

	p := New()
	tenant := &state.Tenant{}
	tenant.Metadata.Name = "demo"
	ng := state.NodeGroupSpec{
		Name:          "worker",
		Role:          "worker",
		Count:         2,
		ProviderClass: "oci:VM.Standard2.1",
		ProviderConfig: []byte(`{
			"compartmentOcid": "ocid1.compartment.oc1..aaaaaaaa.demo",
			"subnetId":        "ocid1.subnet.oc1.phx.aaaaaaaa.demo"
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

// writeMain writes the TF JSON config to the workdir as main.tf.json.
func writeMain(t *testing.T, dir string, tfjson []byte) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, "main.tf.json"), tfjson, 0o644)
}
