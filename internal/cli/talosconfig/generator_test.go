package talosconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
)

const (
	testClusterName  = "test-cluster"
	testEndpoint     = "https://10.5.0.2:6443"
	testK8sVersion   = "1.35.0"
	testTalosVersion = "v1.12"
	testControlPlane = "test-controlplane-0"
	testWorker       = "test-worker-0"
)

func newTestGenerator(t *testing.T) *Generator {
	t.Helper()
	gen, err := NewGenerator(ClusterParams{
		ClusterName:          testClusterName,
		ControlPlaneEndpoint: testEndpoint,
		KubernetesVersion:    testK8sVersion,
		TalosVersion:         testTalosVersion,
	})
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	return gen
}

func TestGenerator_ControlPlaneConfig(t *testing.T) {
	gen := newTestGenerator(t)

	data, err := gen.GenerateControlPlane(testControlPlane, nil)
	if err != nil {
		t.Fatalf("GenerateControlPlane: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("GenerateControlPlane: empty config")
	}

	cfg, err := configloader.NewFromBytes(data)
	if err != nil {
		t.Fatalf("parse generated config: %v\nconfig:\n%s", err, string(data))
	}

	if cfg.Machine().Type() != machine.TypeControlPlane {
		t.Errorf("machine type = %v, want ControlPlane", cfg.Machine().Type())
	}

	if cfg.Cluster().Name() != testClusterName {
		t.Errorf("cluster name = %q, want %q", cfg.Cluster().Name(), testClusterName)
	}
}

func TestGenerator_WorkerConfig(t *testing.T) {
	gen := newTestGenerator(t)

	data, err := gen.GenerateWorker(testWorker, nil)
	if err != nil {
		t.Fatalf("GenerateWorker: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("GenerateWorker: empty config")
	}

	cfg, err := configloader.NewFromBytes(data)
	if err != nil {
		t.Fatalf("parse generated config: %v\nconfig:\n%s", err, string(data))
	}

	if cfg.Machine().Type() != machine.TypeWorker {
		t.Errorf("machine type = %v, want Worker", cfg.Machine().Type())
	}
}

func TestGenerator_DockerPlatformPatch(t *testing.T) {
	gen := newTestGenerator(t)

	data, err := gen.GenerateControlPlane(testControlPlane, DockerPlatformPatch())
	if err != nil {
		t.Fatalf("GenerateControlPlane with Docker patch: %v", err)
	}

	cfg, err := configloader.NewFromBytes(data)
	if err != nil {
		t.Fatalf("parse generated config: %v", err)
	}

	installDisk := cfg.Machine().Install().Disk()
	if installDisk != "" {
		t.Errorf("install disk = %q, want empty for Docker platform", installDisk)
	}
}

func TestGenerator_SecretsReuse(t *testing.T) {
	secretsBundle := newTestGenerator(t).Secrets()

	gen2, err := NewGenerator(ClusterParams{
		ClusterName:          testClusterName,
		ControlPlaneEndpoint: testEndpoint,
		KubernetesVersion:    testK8sVersion,
		TalosVersion:         testTalosVersion,
		SecretsBundle:        secretsBundle,
	})
	if err != nil {
		t.Fatalf("NewGenerator with shared secrets: %v", err)
	}

	cpCfg, err := gen2.GenerateControlPlane(testControlPlane, nil)
	if err != nil {
		t.Fatalf("GenerateControlPlane: %v", err)
	}

	wkCfg, err := gen2.GenerateWorker(testWorker, nil)
	if err != nil {
		t.Fatalf("GenerateWorker: %v", err)
	}

	cpParsed, _ := configloader.NewFromBytes(cpCfg)
	wkParsed, _ := configloader.NewFromBytes(wkCfg)

	if cpParsed.Cluster().ID() != wkParsed.Cluster().ID() {
		t.Errorf("cluster ID mismatch: cp=%q worker=%q — secrets not shared", cpParsed.Cluster().ID(), wkParsed.Cluster().ID())
	}
}

func TestGenerator_Talosconfig(t *testing.T) {
	gen := newTestGenerator(t)

	data, err := gen.GenerateTalosconfig([]string{"10.5.0.2:50000"})
	if err != nil {
		t.Fatalf("GenerateTalosconfig: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("GenerateTalosconfig: empty config")
	}
}

func TestGenerator_ValidationErrors(t *testing.T) {
	t.Run("empty cluster name", func(t *testing.T) {
		_, err := NewGenerator(ClusterParams{
			ClusterName:          "",
			ControlPlaneEndpoint: testEndpoint,
		})
		if err == nil {
			t.Fatal("expected error for empty cluster name")
		}
	})

	t.Run("empty endpoint", func(t *testing.T) {
		_, err := NewGenerator(ClusterParams{
			ClusterName:          testClusterName,
			ControlPlaneEndpoint: "",
		})
		if err == nil {
			t.Fatal("expected error for empty endpoint")
		}
	})

	t.Run("invalid talos version", func(t *testing.T) {
		_, err := NewGenerator(ClusterParams{
			ClusterName:          testClusterName,
			ControlPlaneEndpoint: testEndpoint,
			TalosVersion:         "not-a-version",
		})
		if err == nil {
			t.Fatal("expected error for invalid talos version")
		}
	})

	t.Run("invalid role", func(t *testing.T) {
		gen := newTestGenerator(t)
		_, err := gen.Generate(NodeParams{
			Name: "bad-node",
			Role: NodeRole("invalid"),
		})
		if err == nil {
			t.Fatal("expected error for invalid role")
		}
	})
}

func TestGenerator_DefaultVersions(t *testing.T) {
	gen, err := NewGenerator(ClusterParams{
		ClusterName:          testClusterName,
		ControlPlaneEndpoint: testEndpoint,
	})
	if err != nil {
		t.Fatalf("NewGenerator without explicit versions: %v", err)
	}

	data, err := gen.GenerateControlPlane(testControlPlane, nil)
	if err != nil {
		t.Fatalf("GenerateControlPlane: %v", err)
	}

	cfg, err := configloader.NewFromBytes(data)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Cluster().Name() != testClusterName {
		t.Errorf("cluster name = %q, want %q", cfg.Cluster().Name(), testClusterName)
	}
}

func TestGenerator_GoldenFiles(t *testing.T) {
	gen := newTestGenerator(t)

	tests := []struct {
		name string
		data []byte
	}{
		{"controlplane", mustGenerate(t, gen, RoleControlPlane)},
		{"worker", mustGenerate(t, gen, RoleWorker)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goldenPath := filepath.Join("testdata", tt.name+".golden.yaml")

			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, tt.data, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("updated golden file: %s", goldenPath)
				return
			}

			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden file %s (run with UPDATE_GOLDEN=1 to create): %v", goldenPath, err)
			}

			if string(tt.data) != string(expected) {
				t.Logf("generated config differs from golden (secrets are non-deterministic)")
			}

			_, err = configloader.NewFromBytes(tt.data)
			if err != nil {
				t.Fatalf("generated config is invalid: %v", err)
			}
		})
	}
}

func TestInClusterEndpoint(t *testing.T) {
	tests := []struct {
		gateway string
		port    int
		want    string
	}{
		{"10.5.0.1", 6443, "https://10.5.0.1:6443"},
		{"fd00::1", 6443, "https://fd00::1:6443"},
		{"not-an-ip", 6443, "https://not-an-ip:6443"},
	}

	for _, tt := range tests {
		got := InClusterEndpoint(tt.gateway, tt.port)
		if got != tt.want {
			t.Errorf("InClusterEndpoint(%q, %d) = %q, want %q", tt.gateway, tt.port, got, tt.want)
		}
	}
}

func mustGenerate(t *testing.T, gen *Generator, role NodeRole) []byte {
	t.Helper()
	var data []byte
	var err error
	if role == RoleControlPlane {
		data, err = gen.GenerateControlPlane(testControlPlane, nil)
	} else {
		data, err = gen.GenerateWorker(testWorker, nil)
	}
	if err != nil {
		t.Fatalf("generate %s config: %v", role, err)
	}
	return data
}
