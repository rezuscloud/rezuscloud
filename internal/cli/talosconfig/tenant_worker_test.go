package talosconfig

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateTenantWorkerConfig(t *testing.T) {
	params := TenantWorkerParams{
		Name:           "tenant-worker-0",
		Endpoint:       "https://10.0.0.100:6443",
		CACert:         []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----"),
		BootstrapToken: "rezus01.0123456789abcdef",
		KubeletImage:   "ghcr.io/siderolabs/kubelet:v1.35.0",
		InstallDisk:    "/dev/sda",
		DNSNameservers: []string{"1.1.1.1", "8.8.8.8"},
	}

	config, err := GenerateTenantWorkerConfig(params)
	if err != nil {
		t.Fatalf("GenerateTenantWorkerConfig: %v", err)
	}

	s := string(config)

	// Verify key fields.
	if !strings.Contains(s, "type: worker") {
		t.Error("config should be worker type")
	}
	if !strings.Contains(s, "tenant-worker-0") {
		t.Error("config should contain node name")
	}
	if !strings.Contains(s, "https://10.0.0.100:6443") {
		t.Error("config should contain endpoint")
	}
	if !strings.Contains(s, "ghcr.io/siderolabs/kubelet:v1.35.0") {
		t.Error("config should pin kubelet image")
	}
	if !strings.Contains(s, "kubePrism") {
		t.Error("config should mention KubePrism")
	}
	if !strings.Contains(s, "enabled: false") {
		t.Error("KubePrism should be disabled")
	}
	if !strings.Contains(s, "rezus01.0123456789abcdef") {
		t.Error("config should contain bootstrap token")
	}
	if !strings.Contains(s, "/dev/sda") {
		t.Error("config should contain install disk")
	}
	// Basic mode: no Talos Machine PKI.
	if strings.Contains(s, "machine:") && strings.Contains(s, "token:") {
		// machine.token should NOT appear in basic mode.
		if strings.Contains(s, "machine:\n  type: worker\n  token:") {
			t.Error("basic mode should not include machine token")
		}
	}
}

func TestGenerateTenantWorkerConfig_WithCSRSigner(t *testing.T) {
	params := TenantWorkerParams{
		Name:           "csr-worker-0",
		Endpoint:       "https://10.0.0.100:6443",
		CACert:         []byte("-----BEGIN CERTIFICATE-----\nK8SCA\n-----END CERTIFICATE-----"),
		BootstrapToken: "rezus01.0123456789abcdef",
		KubeletImage:   "ghcr.io/siderolabs/kubelet:v1.35.0",
		InstallDisk:    "/dev/sda",
		DNSNameservers: []string{"1.1.1.1", "8.8.8.8"},
		// CSR signer fields.
		MachineToken:    "my-machine-token-123",
		TalosCACert:     []byte("-----BEGIN CERTIFICATE-----\nTALOSCA\n-----END CERTIFICATE-----"),
		ClusterID:       "abc123-def456",
		ClusterSecret:   "secret789xyz",
		TrustdEndpoints: []string{"10.0.0.100"},
	}

	config, err := GenerateTenantWorkerConfig(params)
	if err != nil {
		t.Fatalf("GenerateTenantWorkerConfig: %v", err)
	}

	s := string(config)

	// CSR signer mode should include Talos Machine PKI.
	if !strings.Contains(s, "my-machine-token-123") {
		t.Error("CSR signer mode should include machine token")
	}
	// Talos CA is base64-encoded in the config.
	if !strings.Contains(s, base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nTALOSCA\n-----END CERTIFICATE-----"))) {
		t.Error("CSR signer mode should include base64-encoded Talos CA")
	}
	if !strings.Contains(s, "abc123-def456") {
		t.Error("CSR signer mode should include cluster ID")
	}
	if !strings.Contains(s, "secret789xyz") {
		t.Error("CSR signer mode should include cluster secret")
	}
	if !strings.Contains(s, "rotate-certificates") {
		t.Error("CSR signer mode should enable certificate rotation")
	}
	if !strings.Contains(s, "discovery:") {
		t.Error("CSR signer mode should include discovery section")
	}
	if !strings.Contains(s, "rbac: true") {
		t.Error("CSR signer mode should enable RBAC")
	}
	// KubePrism still disabled even with CSR signer (it proxies to kube-apiserver, not trustd).
	if !strings.Contains(s, "kubePrism:") {
		t.Error("config should mention KubePrism")
	}
}

func TestGenerateTenantWorkerConfig_Defaults(t *testing.T) {
	params := DefaultTenantWorkerParams("test-node")
	params.Endpoint = "https://192.168.1.1:6443"
	params.CACert = []byte("FAKE-CA")
	params.BootstrapToken = "abcdef.0123456789"

	config, err := GenerateTenantWorkerConfig(params)
	if err != nil {
		t.Fatalf("GenerateTenantWorkerConfig: %v", err)
	}

	s := string(config)
	if !strings.Contains(s, "ghcr.io/siderolabs/kubelet:v1.35.0") {
		t.Error("default kubelet image should be v1.35.0")
	}
	if !strings.Contains(s, "/dev/sda") {
		t.Error("default disk should be /dev/sda")
	}
	if !strings.Contains(s, "1.1.1.1") {
		t.Error("default DNS should contain 1.1.1.1")
	}
}

func TestGenerateTenantWorkerConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		params  TenantWorkerParams
		wantErr string
	}{
		{
			name: "missing name",
			params: TenantWorkerParams{
				Endpoint:       "https://10.0.0.1:6443",
				CACert:         []byte("CA"),
				BootstrapToken: "token.secret",
			},
			wantErr: "name is required",
		},
		{
			name: "missing endpoint",
			params: TenantWorkerParams{
				Name:           "test",
				CACert:         []byte("CA"),
				BootstrapToken: "token.secret",
			},
			wantErr: "endpoint is required",
		},
		{
			name: "missing CA",
			params: TenantWorkerParams{
				Name:           "test",
				Endpoint:       "https://10.0.0.1:6443",
				BootstrapToken: "token.secret",
			},
			wantErr: "CA certificate is required",
		},
		{
			name: "missing token",
			params: TenantWorkerParams{
				Name:     "test",
				Endpoint: "https://10.0.0.1:6443",
				CACert:   []byte("CA"),
			},
			wantErr: "bootstrap token is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateTenantWorkerConfig(tt.params)
			if err == nil {
				t.Fatal("should error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultTenantWorkerParams(t *testing.T) {
	params := DefaultTenantWorkerParams("my-node")
	if params.Name != "my-node" {
		t.Errorf("Name = %q", params.Name)
	}
	if params.InstallDisk != "/dev/sda" {
		t.Errorf("InstallDisk = %q", params.InstallDisk)
	}
	if !strings.Contains(params.KubeletImage, "kubelet:") {
		t.Errorf("KubeletImage = %q", params.KubeletImage)
	}
	if len(params.DNSNameservers) != 2 {
		t.Errorf("DNSNameservers = %v", params.DNSNameservers)
	}
}

func TestGenerateTenantWorkerConfig_HTTPSPrefix(t *testing.T) {
	params := TenantWorkerParams{
		Name:           "test",
		Endpoint:       "10.0.0.1:6443", // No https:// prefix.
		CACert:         []byte("CA"),
		BootstrapToken: "token.secret",
	}

	config, _ := GenerateTenantWorkerConfig(params)
	s := string(config)

	if !strings.Contains(s, "https://10.0.0.1:6443") {
		t.Error("endpoint should get https:// prefix added")
	}
}

func TestHasCSRSigner(t *testing.T) {
	tests := []struct {
		name   string
		params TenantWorkerParams
		want   bool
	}{
		{
			name: "no CSR signer fields",
			params: TenantWorkerParams{
				Name:     "test",
				Endpoint: "https://10.0.0.1:6443",
			},
			want: false,
		},
		{
			name: "only machine token",
			params: TenantWorkerParams{
				MachineToken: "token",
			},
			want: false,
		},
		{
			name: "only talos CA",
			params: TenantWorkerParams{
				TalosCACert: []byte("CA"),
			},
			want: false,
		},
		{
			name: "both CSR signer fields",
			params: TenantWorkerParams{
				MachineToken: "token",
				TalosCACert:  []byte("CA"),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.params.HasCSRSigner(); got != tt.want {
				t.Errorf("HasCSRSigner() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatYAMLList(t *testing.T) {
	tests := []struct {
		name     string
		items    []string
		contains string
	}{
		{"single", []string{"1.1.1.1"}, "1.1.1.1"},
		{"multiple", []string{"1.1.1.1", "8.8.8.8"}, "8.8.8.8"},
		{"empty", []string{}, "[]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatYAMLList(tt.items)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("formatYAMLList(%v) = %q, want to contain %q", tt.items, result, tt.contains)
			}
		})
	}
}
