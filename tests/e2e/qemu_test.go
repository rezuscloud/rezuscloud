//go:build e2e

package e2e

import (
	"os"
	"testing"
)

// TestE2E_QemuBoot is the minimal smoke test: boot a Talos VM in QEMU and
// verify the Talos API becomes reachable. This validates the QEMU setup
// before running the full upgrade-cycle test.
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
