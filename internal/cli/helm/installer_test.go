package helm

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

func TestRestConfigToKubeconfig_BearerToken(t *testing.T) {
	cfg := &rest.Config{
		Host:        "https://192.168.1.10:6443",
		BearerToken: "my-token",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("fake-ca-data"),
		},
	}

	data, err := RestConfigToKubeconfig(cfg)
	if err != nil {
		t.Fatalf("RestConfigToKubeconfig: %v", err)
	}

	kc, err := clientcmd.Load(data)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}

	if kc.CurrentContext != "default" {
		t.Errorf("CurrentContext = %q, want default", kc.CurrentContext)
	}

	ctx := kc.Contexts["default"]
	if ctx.Cluster != "rezuscloud" {
		t.Errorf("Cluster = %q, want rezuscloud", ctx.Cluster)
	}
	if ctx.AuthInfo != "rezusctl" {
		t.Errorf("AuthInfo = %q, want rezusctl", ctx.AuthInfo)
	}

	auth := kc.AuthInfos["rezusctl"]
	if auth.Token != "my-token" {
		t.Errorf("Token = %q, want my-token", auth.Token)
	}

	cluster := kc.Clusters["rezuscloud"]
	if cluster.Server != "https://192.168.1.10:6443" {
		t.Errorf("Server = %q, want https://192.168.1.10:6443", cluster.Server)
	}
	if string(cluster.CertificateAuthorityData) != "fake-ca-data" {
		t.Errorf("CAData mismatch")
	}
}

func TestRestConfigToKubeconfig_Insecure(t *testing.T) {
	cfg := &rest.Config{
		Host:            "http://localhost:8080",
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}

	data, err := RestConfigToKubeconfig(cfg)
	if err != nil {
		t.Fatalf("RestConfigToKubeconfig: %v", err)
	}

	kc, err := clientcmd.Load(data)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}

	cluster := kc.Clusters["rezuscloud"]
	if !cluster.InsecureSkipTLSVerify {
		t.Error("InsecureSkipTLSVerify should be true")
	}
}

func TestRestConfigToKubeconfig_ClientCerts(t *testing.T) {
	cfg := &rest.Config{
		Host: "https://10.0.0.1:6443",
		TLSClientConfig: rest.TLSClientConfig{
			CertData: []byte("cert-data"),
			KeyData:  []byte("key-data"),
		},
	}

	data, err := RestConfigToKubeconfig(cfg)
	if err != nil {
		t.Fatalf("RestConfigToKubeconfig: %v", err)
	}

	kc, err := clientcmd.Load(data)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}

	auth := kc.AuthInfos["rezusctl"]
	if string(auth.ClientCertificateData) != "cert-data" {
		t.Errorf("CertData mismatch")
	}
	if string(auth.ClientKeyData) != "key-data" {
		t.Errorf("KeyData mismatch")
	}
	if auth.Token != "" {
		t.Error("Token should be empty when using client certs")
	}
}

func TestWriteTempKubeconfig(t *testing.T) {
	content := []byte("apiVersion: v1\nkind: Config\n")
	path, err := WriteTempKubeconfig(content)
	if err != nil {
		t.Fatalf("WriteTempKubeconfig: %v", err)
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}

	if string(data) != string(content) {
		t.Errorf("content = %q, want %q", string(data), string(content))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestWriteTempKubeconfig_EmptyData(t *testing.T) {
	path, err := WriteTempKubeconfig([]byte{})
	if err != nil {
		t.Fatalf("WriteTempKubeconfig with empty data: %v", err)
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(data))
	}
}

func TestNewInstallerFromBytes(t *testing.T) {
	kubeconfig := api.Config{
		Clusters: map[string]*api.Cluster{
			"test": {Server: "https://localhost:6443"},
		},
		Contexts: map[string]*api.Context{
			"default": {Cluster: "test", AuthInfo: "user"},
		},
		CurrentContext: "default",
		AuthInfos:      map[string]*api.AuthInfo{"user": {}},
	}

	data, err := clientcmd.Write(kubeconfig)
	if err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	installer, err := NewInstallerFromBytes(data)
	if err != nil {
		t.Fatalf("NewInstallerFromBytes: %v", err)
	}
	if installer == nil {
		t.Fatal("installer should not be nil")
	}
	if installer.settings.KubeConfig == "" {
		t.Error("KubeConfig path should be set")
	}

	_ = os.RemoveAll(filepath.Dir(installer.settings.KubeConfig))
}

func TestNewInstaller_EmptyPath(t *testing.T) {
	installer, err := NewInstaller("")
	if err != nil {
		t.Fatalf("NewInstaller with empty path: %v", err)
	}
	if installer == nil {
		t.Error("installer should not be nil")
	}
}

func TestNewInstallerFromRestConfig_RoundTrip(t *testing.T) {
	original := &rest.Config{
		Host:        "https://10.0.0.1:6443",
		BearerToken: "test-token-123",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("ca-bytes"),
		},
	}

	installer, err := NewInstallerFromRestConfig(original)
	if err != nil {
		t.Fatalf("NewInstallerFromRestConfig: %v", err)
	}
	if installer == nil {
		t.Fatal("installer should not be nil")
	}
	_ = os.RemoveAll(filepath.Dir(installer.settings.KubeConfig))
}
