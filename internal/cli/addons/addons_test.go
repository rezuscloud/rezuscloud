package addons

import (
	"context"
	"io"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/cli/installer"
)

func TestNewAddons_NilConfig(t *testing.T) {
	// Should fail with nil rest.Config
	_, err := New(nil, nil, io.Discard)
	if err == nil {
		t.Error("expected error with nil config")
	}
}

func TestInstallCertManager_Config(t *testing.T) {
	var received installer.ChartConfig
	mock := &mockInstaller{
		installFn: func(_ context.Context, cfg installer.ChartConfig, _ io.Writer) error {
			received = cfg
			return nil
		},
	}

	// Can't test with real dynamic client without a cluster, but can test the config
	// by calling InstallCertManager directly on a struct with mock installer
	a := &Addons{
		installer: mock,
		out:       io.Discard,
	}

	err := a.InstallCertManager(context.Background())
	if err != nil {
		t.Fatalf("InstallCertManager: %v", err)
	}

	if received.Name != "cert-manager" {
		t.Errorf("Name = %q, want %q", received.Name, "cert-manager")
	}
	if received.Namespace != "cert-manager" {
		t.Errorf("Namespace = %q, want %q", received.Namespace, "cert-manager")
	}
	if received.Repository != "https://charts.jetstack.io" {
		t.Errorf("Repository = %q, want %q", received.Repository, "https://charts.jetstack.io")
	}
	if received.Version != "1.18.2" {
		t.Errorf("Version = %q, want %q", received.Version, "1.18.2")
	}

	// Check CRDs enabled
	crds, ok := received.Values["crds"].(map[string]interface{})
	if !ok {
		t.Fatal("crds not a map")
	}
	if crds["enabled"] != true {
		t.Error("crds.enabled should be true")
	}
}

func TestInstallExternalDNS_Config(t *testing.T) {
	var received installer.ChartConfig
	mock := &mockInstaller{
		installFn: func(_ context.Context, cfg installer.ChartConfig, _ io.Writer) error {
			received = cfg
			return nil
		},
	}

	a := &Addons{
		installer: mock,
		out:       io.Discard,
	}

	err := a.InstallExternalDNS(context.Background(), "mycloud.dev", "cf-secret")
	if err != nil {
		t.Fatalf("InstallExternalDNS: %v", err)
	}

	if received.Name != "external-dns" {
		t.Errorf("Name = %q, want %q", received.Name, "external-dns")
	}
	if received.Namespace != "external-dns" {
		t.Errorf("Namespace = %q, want %q", received.Namespace, "external-dns")
	}

	// Check Cloudflare provider configured when secretRef is set
	if received.Values["env"] == nil {
		t.Error("env should be set when secretRef is provided")
	}
}

func TestInstallExternalDNS_NoSecret(t *testing.T) {
	var received installer.ChartConfig
	mock := &mockInstaller{
		installFn: func(_ context.Context, cfg installer.ChartConfig, _ io.Writer) error {
			received = cfg
			return nil
		},
	}

	a := &Addons{
		installer: mock,
		out:       io.Discard,
	}

	err := a.InstallExternalDNS(context.Background(), "test.local", "")
	if err != nil {
		t.Fatalf("InstallExternalDNS: %v", err)
	}

	if received.Values["env"] != nil {
		t.Error("env should be nil when no secretRef")
	}
}

type mockInstaller struct {
	installFn func(context.Context, installer.ChartConfig, io.Writer) error
}

func (m *mockInstaller) Install(ctx context.Context, cfg installer.ChartConfig, out io.Writer) error {
	if m.installFn != nil {
		return m.installFn(ctx, cfg, out)
	}
	return nil
}

func (m *mockInstaller) Rollback(_ context.Context, _, _ string) error { return nil }

func (m *mockInstaller) IsInstalled(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
