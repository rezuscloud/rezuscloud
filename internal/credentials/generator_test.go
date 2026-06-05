package credentials

import (
	"strings"
	"testing"
)

func TestGenerateSecretsBundle(t *testing.T) {
	bundle, err := GenerateSecretsBundle("1.12.6")
	if err != nil {
		t.Fatalf("GenerateSecretsBundle: %v", err)
	}

	if len(bundle.Certs.K8s.Crt) == 0 {
		t.Error("K8s CA cert should not be empty")
	}
	if len(bundle.Certs.OS.Crt) == 0 {
		t.Error("OS CA cert should not be empty")
	}
}

func TestSecretsBundleJSON_Roundtrip(t *testing.T) {
	bundle, err := GenerateSecretsBundle("1.12.6")
	if err != nil {
		t.Fatalf("GenerateSecretsBundle: %v", err)
	}

	raw, err := SecretsBundleJSON(bundle)
	if err != nil {
		t.Fatalf("SecretsBundleJSON: %v", err)
	}

	if len(raw) == 0 {
		t.Error("JSON should not be empty")
	}

	// Verify we can generate kubeconfig from the fresh bundle (not roundtripped).
	kubeconfig, err := GenerateKubeconfig(KubeconfigRequest{
		ClusterName:     "test-cluster",
		ClusterEndpoint: "https://192.168.1.10:6443",
		Bundle:          bundle,
	})
	if err != nil {
		t.Fatalf("GenerateKubeconfig from fresh bundle: %v", err)
	}
	if !strings.Contains(string(kubeconfig), "test-cluster") {
		t.Error("kubeconfig should contain cluster name")
	}
}

func TestGenerateKubeconfig(t *testing.T) {
	bundle, err := GenerateSecretsBundle("1.12.6")
	if err != nil {
		t.Fatalf("GenerateSecretsBundle: %v", err)
	}

	kubeconfig, err := GenerateKubeconfig(KubeconfigRequest{
		ClusterName:     "test-cluster",
		ClusterEndpoint: "https://192.168.1.10:6443",
		Bundle:          bundle,
	})
	if err != nil {
		t.Fatalf("GenerateKubeconfig: %v", err)
	}

	kubeconfigStr := string(kubeconfig)

	if !strings.Contains(kubeconfigStr, "apiVersion:") {
		t.Error("kubeconfig should contain apiVersion")
	}
	if !strings.Contains(kubeconfigStr, "kind: Config") {
		t.Error("kubeconfig should be kind: Config")
	}
	if !strings.Contains(kubeconfigStr, "test-cluster") {
		t.Error("kubeconfig should contain cluster name")
	}
	if !strings.Contains(kubeconfigStr, "https://192.168.1.10:6443") {
		t.Error("kubeconfig should contain cluster endpoint")
	}
	if !strings.Contains(kubeconfigStr, "certificate-authority-data:") {
		t.Error("kubeconfig should contain CA data")
	}
}

func TestGenerateKubeconfig_DefaultEndpoint(t *testing.T) {
	bundle, _ := GenerateSecretsBundle("1.12.6")

	kubeconfig, err := GenerateKubeconfig(KubeconfigRequest{
		ClusterName: "test",
		Bundle:      bundle,
	})
	if err != nil {
		t.Fatalf("GenerateKubeconfig: %v", err)
	}

	if !strings.Contains(string(kubeconfig), "https://127.0.0.1:6443") {
		t.Error("should use default endpoint")
	}
}

func TestGenerateKubeconfig_NoBundle(t *testing.T) {
	_, err := GenerateKubeconfig(KubeconfigRequest{
		ClusterName: "test",
	})
	if err == nil {
		t.Error("should error without secrets bundle")
	}
}

func TestGenerateTalosconfig(t *testing.T) {
	bundle, err := GenerateSecretsBundle("1.12.6")
	if err != nil {
		t.Fatalf("GenerateSecretsBundle: %v", err)
	}

	talosconfig, err := GenerateTalosconfig(TalosconfigRequest{
		ClusterName:     "test-cluster",
		MachineLinkAddr: "192.168.1.5:50180",
		Bundle:          bundle,
	})
	if err != nil {
		t.Fatalf("GenerateTalosconfig: %v", err)
	}

	talosconfigStr := string(talosconfig)

	if !strings.Contains(talosconfigStr, "test-cluster") {
		t.Error("talosconfig should contain cluster name")
	}
	if !strings.Contains(talosconfigStr, "192.168.1.5:50180") {
		t.Error("talosconfig should contain endpoint")
	}
}

func TestGenerateTalosconfig_DefaultEndpoint(t *testing.T) {
	bundle, _ := GenerateSecretsBundle("1.12.6")

	talosconfig, err := GenerateTalosconfig(TalosconfigRequest{
		ClusterName: "test",
		Bundle:      bundle,
	})
	if err != nil {
		t.Fatalf("GenerateTalosconfig: %v", err)
	}

	if !strings.Contains(string(talosconfig), "127.0.0.1:50000") {
		t.Error("should use default endpoint")
	}
}

func TestGenerateTalosconfig_NoBundle(t *testing.T) {
	_, err := GenerateTalosconfig(TalosconfigRequest{
		ClusterName: "test",
	})
	if err == nil {
		t.Error("should error without secrets bundle")
	}
}
