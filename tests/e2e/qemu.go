//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// requireQemu skips the test if QEMU is not available or the test is not
// explicitly enabled. KVM is NOT required — when absent, the VM boots under
// TCG software emulation (slower but functional) (#183).
func requireQemu(t *testing.T) {
	t.Helper()

	if os.Getenv("REZUSCLOUD_E2E_QEMU") != "1" {
		t.Skip("set REZUSCLOUD_E2E_QEMU=1 to run QEMU E2E tests")
	}

	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skipf("qemu-system-x86_64 not found on PATH: %v", err)
	}
}

// kvmAvailable reports whether hardware virtualization (/dev/kvm) is present.
// When false, bootVM falls back to TCG software emulation (#183).
func kvmAvailable() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// bootTimeout returns the max time to wait for the Talos API based on the
// accelerator: KVM boots in ~30s (5 min budget), TCG in minutes (15 min budget).
func bootTimeout() time.Duration {
	if kvmAvailable() {
		return 5 * time.Minute
	}
	return 15 * time.Minute
}
func isoPath() string {
	if p := os.Getenv("REZUSCLOUD_E2E_TALOS_ISO"); p != "" {
		return p
	}
	return "/tmp/talos.iso"
}

// QEMUVM represents a running QEMU Talos VM.
type QEMUVM struct {
	cmd  *exec.Cmd
	addr string //Talos API address (host:port)
}

// bootVM boots a Talos VM in QEMU maintenance mode from the given ISO.
// The VM's Talos API is forwarded to a random localhost port. Uses KVM when
// available, TCG software emulation otherwise (#183).
func bootVM(t *testing.T, iso string) *QEMUVM {
	t.Helper()

	// Pick a free port for the Talos API (50000 inside VM → random host port).
	apiPort, err := freePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}

	// Pick accelerator: KVM when available, TCG (software emulation) otherwise.
	accel := "kvm"
	cpu := "host"
	if !kvmAvailable() {
		accel = "tcg"
		cpu = "qemu64" // TCG cannot use host passthrough
		t.Log("KVM not available — booting under TCG software emulation (slower)")
	}

	// QEMU working directory (for serial log, etc.).
	workDir := t.TempDir()
	serialLog := filepath.Join(workDir, "serial.log")

	args := []string{
		"-machine", fmt.Sprintf("type=q35,accel=%s", accel),
		"-cpu", cpu,
		"-smp", "2",
		"-m", "2048",
		"-nic", fmt.Sprintf("user,model=virtio-net-pci,hostfwd=tcp::%d-:50000", apiPort),
		"-drive", fmt.Sprintf("file=%s,media=cdrom", iso),
		"-drive", fmt.Sprintf("file=fat:rw:%s,format=raw", workDir),
		"-serial", fmt.Sprintf("file=%s", serialLog),
		"-display", "none",
		"-no-reboot",
	}

	cmd := exec.Command("qemu-system-x86_64", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start QEMU: %v", err)
	}

	vm := &QEMUVM{
		cmd:  cmd,
		addr: fmt.Sprintf("127.0.0.1:%d", apiPort),
	}

	// Wait for the Talos API to become reachable. TCG is much slower.
	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout())
	defer cancel()
	if err := waitForPort(ctx, apiPort); err != nil {
		vm.Kill()
		t.Fatalf("Talos VM did not come up within 5 minutes: %v", err)
	}

	return vm
}

// Kill stops the QEMU process.
func (vm *QEMUVM) Kill() {
	if vm.cmd != nil && vm.cmd.Process != nil {
		_ = vm.cmd.Process.Kill()
		_, _ = vm.cmd.Process.Wait()
	}
}

// Addr returns the Talos API address.
func (vm *QEMUVM) Addr() string { return vm.addr }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(ctx context.Context, port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}
