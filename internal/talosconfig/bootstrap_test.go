package talosconfig

import (
	"strings"
	"testing"
)

func TestBootstrapConfig_Minimal(t *testing.T) {
	out, err := BootstrapConfig(BootstrapParams{InstallDisk: "/dev/nvme0n1"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"machine:", "install:", "disk: /dev/nvme0n1"} {
		if !strings.Contains(out, want) {
			t.Errorf("bootstrap config missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secrets") || strings.Contains(out, "cluster") {
		t.Errorf("bootstrap must carry no cluster secrets:\n%s", out)
	}
}

func TestBootstrapConfig_KernelArgs(t *testing.T) {
	arg := SiderolinkKernelArg("grpc.rezus.cloud:51800", "bind-tok-1")
	out, err := BootstrapConfig(BootstrapParams{
		InstallDisk:     "/dev/sda",
		ExtraKernelArgs: []string{arg},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "siderolink.api=grpc://grpc.rezus.cloud:51800?jointoken=bind-tok-1") {
		t.Errorf("management-link kernel arg missing:\n%s", out)
	}
}

func TestBootstrapConfig_RejectsBadArg(t *testing.T) {
	if _, err := BootstrapConfig(BootstrapParams{ExtraKernelArgs: []string{"bad\narg"}}); err == nil {
		t.Error("want error for kernel arg with newline")
	}
}
