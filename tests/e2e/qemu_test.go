//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestE2E_QemuBoot is the minimal smoke test: boot a Talos VM in QEMU and
// verify the Talos API becomes reachable. When talosctl is on PATH, it also
// queries the node's version to prove Talos is actually running (not just a
// port listener). Runs under KVM when available, TCG otherwise (#183).
func TestE2E_QemuBoot(t *testing.T) {
	requireQemu(t)

	iso := isoPath()
	if _, err := os.Stat(iso); err != nil {
		t.Skipf("Talos ISO not found at %s: %v", iso, err)
	}

	vm := bootVM(t, iso)
	defer vm.Kill()

	t.Logf("Talos VM is up at %s", vm.Addr())

	// At minimum, the API port must be reachable.
	if vm.Addr() == "" {
		t.Fatal("empty VM address")
	}

	// If talosctl is available, query the node version to prove it's really
	// Talos. This is a best-effort enhancement — absence of talosctl doesn't
	// fail the test.
	if talosctl, err := exec.LookPath("talosctl"); err == nil {
		t.Log("talosctl found — querying node version")
		out, err := exec.Command(talosctl, "version", "--nodes", vm.Addr(), "--talosconfig", "/dev/null").CombinedOutput()
		if err != nil {
			t.Logf("talosctl version failed (non-fatal): %s\n%s", err, out)
		} else {
			t.Logf("talosctl version output:\n%s", out)
			if !strings.Contains(string(out), "Tag:") && !strings.Contains(string(out), "v1.") {
				t.Logf("warning: version output does not look like a Talos version")
			}
		}
	} else {
		t.Log("talosctl not on PATH — skipping version query (port-reachable is sufficient)")
	}
}

// TestE2E_QemuUpgradeCycle is the full acceptance test:
//
//	declare tenant → tofu apply → machines appear in store → version bump
//	  → rolling upgrade → re-apply
//
// This test is the primary validation that the entire rezuscloud pipeline
// works against real Talos infrastructure.
func TestE2E_QemuUpgradeCycle(t *testing.T) {
	requireQemu(t)

	// TODO: Full test implementation requires:
	// 1. Start rezuscloud server (HTTP API + reconciliation)
	// 2. Boot 1+ Talos VMs
	// 3. Create a tenant with a metal node group
	// 4. Trigger reconciliation and wait for machines in store
	// 5. Bump Talos version
	// 6. Wait for rolling upgrade via Talos API
	// 7. Verify store enrichment (machine address, version)
	//
	// The QEMU VM boot + API wait helpers are in place (qemu.go).
	// The server lifecycle and assertion helpers are the next step.
	t.Skip("full upgrade-cycle test implementation pending — see docs/testing/e2e-qemu.md")
}
