package talosconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDetermineMachineType(t *testing.T) {
	tests := []struct {
		name                string
		role                string
		isFirstControlPlane bool
		want                MachineType
	}{
		{"first controlplane", "controlplane", true, TypeInit},
		{"second controlplane", "controlplane", false, TypeControlPlane},
		{"worker", "worker", false, TypeWorker},
		{"worker ignores first", "worker", true, TypeWorker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineMachineType(tt.role, tt.isFirstControlPlane)
			if got != tt.want {
				t.Errorf("DetermineMachineType(%q, %v) = %q, want %q", tt.role, tt.isFirstControlPlane, got, tt.want)
			}
		})
	}
}

func TestGenerateConfig_Init(t *testing.T) {
	req := ConfigRequest{
		ClusterName:       "test-cluster",
		ClusterEndpoint:   "https://192.168.1.10:6443",
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.6",
		MachineType:       TypeInit,
		MachineID:         "hw-001",
	}

	result, err := GenerateConfig(req)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	if result.MachineConfig == "" {
		t.Error("machine config should not be empty")
	}
	if result.MachineType != TypeInit {
		t.Errorf("type = %q, want %q", result.MachineType, TypeInit)
	}
	if result.MachineID != "hw-001" {
		t.Errorf("machineID = %q, want %q", result.MachineID, "hw-001")
	}
}

func TestGenerateConfig_Worker(t *testing.T) {
	req := ConfigRequest{
		ClusterName:       "test-cluster",
		ClusterEndpoint:   "https://192.168.1.10:6443",
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.6",
		MachineType:       TypeWorker,
	}

	result, err := GenerateConfig(req)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	if result.MachineConfig == "" {
		t.Error("machine config should not be empty")
	}
}

func TestGenerateConfig_ControlPlane(t *testing.T) {
	req := ConfigRequest{
		ClusterName:       "test-cluster",
		ClusterEndpoint:   "https://192.168.1.10:6443",
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.6",
		MachineType:       TypeControlPlane,
	}

	result, err := GenerateConfig(req)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	if result.MachineConfig == "" {
		t.Error("machine config should not be empty")
	}
}

func TestGenerateConfig_WithCNI(t *testing.T) {
	req := ConfigRequest{
		ClusterName:       "test-cluster",
		ClusterEndpoint:   "https://192.168.1.10:6443",
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.6",
		MachineType:       TypeInit,
		CNIType:           "cilium",
	}

	result, err := GenerateConfig(req)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	if result.MachineConfig == "" {
		t.Error("machine config should not be empty")
	}
}

func TestGenerateConfig_MissingClusterName(t *testing.T) {
	_, err := GenerateConfig(ConfigRequest{
		KubernetesVersion: "1.35.0",
		MachineType:       TypeInit,
	})
	if err == nil {
		t.Error("expected error for missing cluster name")
	}
}

func TestGenerateConfig_MissingK8sVersion(t *testing.T) {
	_, err := GenerateConfig(ConfigRequest{
		ClusterName: "test",
		MachineType: TypeInit,
	})
	if err == nil {
		t.Error("expected error for missing kubernetes version")
	}
}

func TestGenerateConfig_MissingMachineType(t *testing.T) {
	_, err := GenerateConfig(ConfigRequest{
		ClusterName:       "test",
		KubernetesVersion: "1.35.0",
	})
	if err == nil {
		t.Error("expected error for missing machine type")
	}
}

func TestGenerateConfig_DefaultEndpoint(t *testing.T) {
	req := ConfigRequest{
		ClusterName:       "test-cluster",
		KubernetesVersion: "1.35.0",
		TalosVersion:      "1.12.6",
		MachineType:       TypeWorker,
	}

	result, err := GenerateConfig(req)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	if result.MachineConfig == "" {
		t.Error("machine config should not be empty")
	}
}

// --- Secrets Store Tests ---

func TestSecretsStore_EncryptDecrypt(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	dataPath := filepath.Join(dir, "secrets.enc")

	store, err := NewSecretsStore(dataPath, keyPath)
	if err != nil {
		t.Fatalf("NewSecretsStore: %v", err)
	}

	bundle := json.RawMessage(`{"ca": "test-ca-cert", "etcd": "test-etcd-cert"}`)

	// Store.
	err = store.StoreTenantBundle("prod", bundle)
	if err != nil {
		t.Fatalf("StoreTenantBundle: %v", err)
	}

	// Verify file exists and is encrypted.
	data, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Error("encrypted file should not be empty")
	}
	// Should NOT contain plaintext.
	if string(data) == "" {
		t.Error("data should be non-empty")
	}

	// Load.
	loaded, err := store.LoadTenantBundle("prod")
	if err != nil {
		t.Fatalf("LoadTenantBundle: %v", err)
	}
	if string(loaded) != `{"ca":"test-ca-cert","etcd":"test-etcd-cert"}` {
		t.Errorf("loaded = %q, want original json", string(loaded))
	}
}

func TestSecretsStore_DeleteTenantBundle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSecretsStore(
		filepath.Join(dir, "secrets.enc"),
		filepath.Join(dir, "key"),
	)
	if err != nil {
		t.Fatalf("NewSecretsStore: %v", err)
	}

	bundle := json.RawMessage(`{"ca": "test"}`)
	_ = store.StoreTenantBundle("prod", bundle)

	// Delete.
	err = store.DeleteTenantBundle("prod")
	if err != nil {
		t.Fatalf("DeleteTenantBundle: %v", err)
	}

	// Verify gone.
	loaded, err := store.LoadTenantBundle("prod")
	if err != nil {
		t.Fatalf("LoadTenantBundle: %v", err)
	}
	if loaded != nil {
		t.Error("bundle should be nil after deletion")
	}
}

func TestSecretsStore_LoadNonexistent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSecretsStore(
		filepath.Join(dir, "secrets.enc"),
		filepath.Join(dir, "key"),
	)
	if err != nil {
		t.Fatalf("NewSecretsStore: %v", err)
	}

	loaded, err := store.LoadTenantBundle("nonexistent")
	if err != nil {
		t.Fatalf("LoadTenantBundle: %v", err)
	}
	if loaded != nil {
		t.Error("should return nil for nonexistent tenant")
	}
}

func TestSecretsStore_MultipleTenants(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSecretsStore(
		filepath.Join(dir, "secrets.enc"),
		filepath.Join(dir, "key"),
	)
	if err != nil {
		t.Fatalf("NewSecretsStore: %v", err)
	}

	_ = store.StoreTenantBundle("prod", json.RawMessage(`{"ca": "prod-ca"}`))
	_ = store.StoreTenantBundle("staging", json.RawMessage(`{"ca": "staging-ca"}`))

	prod, _ := store.LoadTenantBundle("prod")
	staging, _ := store.LoadTenantBundle("staging")

	if string(prod) != `{"ca":"prod-ca"}` {
		t.Errorf("prod = %q, want prod bundle", string(prod))
	}
	if string(staging) != `{"ca":"staging-ca"}` {
		t.Errorf("staging = %q, want staging bundle", string(staging))
	}
}
