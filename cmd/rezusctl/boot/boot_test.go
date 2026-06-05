package boot

import (
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()

	if opts.ClusterName != "rezuscloud" {
		t.Errorf("ClusterName = %q, want %q", opts.ClusterName, "rezuscloud")
	}
	if opts.StateDir != ".rezusctl" {
		t.Errorf("StateDir = %q, want %q", opts.StateDir, ".rezusctl")
	}
	if opts.Platform != "docker" {
		t.Errorf("Platform = %q, want %q", opts.Platform, "docker")
	}
}

func TestComplete_Defaults(t *testing.T) {
	opts := &Options{}
	if err := opts.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if opts.ClusterName != "rezuscloud" {
		t.Errorf("ClusterName = %q, want %q", opts.ClusterName, "rezuscloud")
	}
	if opts.StateDir != ".rezusctl" {
		t.Errorf("StateDir = %q, want %q", opts.StateDir, ".rezusctl")
	}
}

func TestComplete_EnvVars(t *testing.T) {
	t.Setenv("REZUSCTL_CLUSTER_NAME", "test-cluster")
	t.Setenv("REZUSCTL_STATE_DIR", "/data/rezusctl")
	t.Setenv("REZUSCTL_TALOS_VERSION", "1.12.6")
	t.Setenv("REZUSCTL_CILIUM_VERSION", "1.18.0")

	opts := &Options{TalosVersion: "latest"}
	if err := opts.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if opts.ClusterName != "test-cluster" {
		t.Errorf("ClusterName = %q, want %q", opts.ClusterName, "test-cluster")
	}
	if opts.StateDir != "/data/rezusctl" {
		t.Errorf("StateDir = %q, want %q", opts.StateDir, "/data/rezusctl")
	}
	if opts.TalosVersion != "1.12.6" {
		t.Errorf("TalosVersion = %q, want %q", opts.TalosVersion, "1.12.6")
	}
	if opts.CiliumVersion != "1.18.0" {
		t.Errorf("CiliumVersion = %q, want %q", opts.CiliumVersion, "1.18.0")
	}
}

func TestComplete_ExplicitOverridesEnv(t *testing.T) {
	t.Setenv("REZUSCTL_CLUSTER_NAME", "env-cluster")

	// Explicit ClusterName should override env var (already set before Complete)
	opts := &Options{ClusterName: "explicit-cluster"}
	if err := opts.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if opts.ClusterName != "explicit-cluster" {
		t.Errorf("ClusterName = %q, want %q (explicit should override env)", opts.ClusterName, "explicit-cluster")
	}
}

func TestValidate_ValidPlatforms(t *testing.T) {
	for _, platform := range []string{"docker", "qemu"} {
		opts := &Options{Platform: platform, ControlPlanes: 1}
		if err := opts.Validate(); err != nil {
			t.Errorf("Validate(%s): %v", platform, err)
		}
	}
}

func TestValidate_InvalidPlatform(t *testing.T) {
	opts := &Options{Platform: "aws", ControlPlanes: 1}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for unsupported platform")
	}
}

func TestValidate_ZeroControlPlanes(t *testing.T) {
	opts := &Options{Platform: "docker", ControlPlanes: 0}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for zero control planes")
	}
}

func TestValidate_DockerMultipleControlPlanes(t *testing.T) {
	opts := &Options{Platform: "docker", ControlPlanes: 3}
	if err := opts.Validate(); err == nil {
		t.Error("expected error for multiple docker control planes")
	}
}

func TestEnvOr(t *testing.T) {
	if got := envOr("NONEXISTENT_VAR_12345", "fallback"); got != "fallback" {
		t.Errorf("envOr with missing var = %q, want %q", got, "fallback")
	}

	t.Setenv("TEST_ENVOR_VAR", "from-env")
	if got := envOr("TEST_ENVOR_VAR", "fallback"); got != "from-env" {
		t.Errorf("envOr with set var = %q, want %q", got, "from-env")
	}
}
