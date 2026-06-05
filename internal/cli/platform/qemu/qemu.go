// Package qemu implements the Platform interface for local QEMU VMs.
// It uses `talosctl cluster create qemu` to provision real Talos VMs
// with KVM acceleration, providing full Kubernetes functionality
// including proper CNI networking, webhooks, and multi-node support.
//
// Prerequisites:
//   - KVM enabled (/dev/kvm)
//   - qemu-system-x86_64 in PATH
//   - talosctl in PATH
//   - nft_masq kernel module (for CNI masquerade)
//   - sudo access (network setup requires root)
package qemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/cli/talosconfig"
)

const (
	// DefaultCIDR is the default network CIDR for QEMU clusters.
	DefaultCIDR = "10.6.0.0/24"

	// DefaultStateDir is the Talos cluster state directory.
	DefaultStateDir = ".talos/clusters"

	// DefaultMemory is the default VM memory in MB.
	DefaultMemory = "2048"

	// DefaultCPUs is the default number of vCPUs per VM.
	DefaultCPUs = "2.0"
)

// ClusterSpec defines the parameters for a QEMU Talos cluster.
type ClusterSpec struct {
	// Name is the cluster name (used for state directory and node naming).
	Name string

	// KubernetesVersion is the K8s version to deploy (e.g. "1.35.0").
	KubernetesVersion string

	// TalosVersion is the Talos version to deploy (e.g. "v1.12.6").
	TalosVersion string

	// ControlPlanes is the number of control plane VMs.
	ControlPlanes int

	// Workers is the number of worker VMs.
	Workers int

	// CIDR is the network CIDR for the cluster.
	CIDR string

	// StateDirectory is the base directory for cluster state.
	// Defaults to ~/.talos/clusters.
	StateDirectory string

	// CPUCPUs is the CPU allocation per control plane VM.
	CPUControlPlanes string

	// CPUWorkers is the CPU allocation per worker VM.
	CPUWorkers string

	// MemoryControlPlanes is the memory per control plane VM (e.g. "2.0GiB").
	MemoryControlPlanes string

	// MemoryWorkers is the memory per worker VM (e.g. "2.0GiB").
	MemoryWorkers string

	// ConfigPatches are machine config patches applied to all nodes.
	ConfigPatches []string

	// ConfigPatchesControlPlanes are patches applied to control plane nodes only.
	ConfigPatchesControlPlanes []string

	// ConfigPatchesWorkers are patches applied to worker nodes only.
	ConfigPatchesWorkers []string
}

// DefaultClusterSpec returns a ClusterSpec with sensible defaults.
func DefaultClusterSpec(name, k8sVersion string) ClusterSpec {
	return ClusterSpec{
		Name:                name,
		KubernetesVersion:   k8sVersion,
		TalosVersion:        "v1.12.6",
		ControlPlanes:       1,
		Workers:             0,
		CIDR:                DefaultCIDR,
		CPUControlPlanes:    DefaultCPUs,
		CPUWorkers:          DefaultCPUs,
		MemoryControlPlanes: "2.0GiB",
		MemoryWorkers:       "2.0GiB",
	}
}

// QEMUPlatform manages a Talos cluster using QEMU VMs.
type QEMUPlatform struct {
	spec     ClusterSpec
	stateDir string
	homeDir  string
}

// New creates a new QEMU platform provisioner.
func New(spec ClusterSpec) (*QEMUPlatform, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	if spec.KubernetesVersion == "" {
		return nil, fmt.Errorf("kubernetes version is required")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home directory: %w", err)
	}

	stateDir := spec.StateDirectory
	if stateDir == "" {
		stateDir = filepath.Join(homeDir, DefaultStateDir)
	}

	return &QEMUPlatform{
		spec:     spec,
		stateDir: stateDir,
		homeDir:  homeDir,
	}, nil
}

// Name returns the platform identifier.
func (q *QEMUPlatform) Name() string {
	return "qemu"
}

// CreateCluster provisions a Talos cluster using `talosctl cluster create qemu`.
func (q *QEMUPlatform) CreateCluster(ctx context.Context) error {
	// Check prerequisites.
	if err := q.checkPrerequisites(ctx); err != nil {
		return fmt.Errorf("prerequisites: %w", err)
	}

	args := []string{
		"cluster", "create", "qemu",
		"--name", q.spec.Name,
		"--kubernetes-version", q.spec.KubernetesVersion,
		"--talos-version", q.spec.TalosVersion,
		"--cidr", q.spec.CIDR,
		"--controlplanes", fmt.Sprintf("%d", q.spec.ControlPlanes),
		"--workers", fmt.Sprintf("%d", q.spec.Workers),
		"--cpus-controlplanes", q.spec.CPUControlPlanes,
		"--cpus-workers", q.spec.CPUWorkers,
		"--memory-controlplanes", q.spec.MemoryControlPlanes,
		"--memory-workers", q.spec.MemoryWorkers,
	}

	for _, patch := range q.spec.ConfigPatches {
		args = append(args, "--config-patch", patch)
	}
	for _, patch := range q.spec.ConfigPatchesControlPlanes {
		args = append(args, "--config-patch-controlplanes", patch)
	}
	for _, patch := range q.spec.ConfigPatchesWorkers {
		args = append(args, "--config-patch-workers", patch)
	}

	cmd := exec.CommandContext(ctx, "sudo", append([]string{"--preserve-env=HOME", "talosctl"}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+q.homeDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("talosctl cluster create qemu: %w", err)
	}
	return nil
}

// Destroy removes the QEMU cluster using `talosctl cluster destroy`.
func (q *QEMUPlatform) Destroy(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "sudo", "--preserve-env=HOME", "talosctl", "cluster", "destroy", "--name", q.spec.Name)
	cmd.Env = append(os.Environ(), "HOME="+q.homeDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("talosctl cluster destroy: %w", err)
	}
	return nil
}

// Kubeconfig returns the admin kubeconfig for the cluster.
// Reads from ~/.kube/config which talosctl merges into.
func (q *QEMUPlatform) Kubeconfig() ([]byte, error) {
	kubeconfigPath := filepath.Join(q.homeDir, ".kube", "config")
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig %s: %w", kubeconfigPath, err)
	}
	return data, nil
}

// Talosconfig returns the admin talosconfig for the cluster.
func (q *QEMUPlatform) Talosconfig() ([]byte, error) {
	talosconfigPath := filepath.Join(q.homeDir, ".talos", "config")
	data, err := os.ReadFile(talosconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read talosconfig %s: %w", talosconfigPath, err)
	}
	return data, nil
}

// GatewayIP returns the gateway IP for the cluster network.
func (q *QEMUPlatform) GatewayIP() string {
	// Gateway is always .1 in the CIDR.
	parts := strings.Split(q.spec.CIDR, ".")
	if len(parts) >= 3 {
		return parts[0] + "." + parts[1] + "." + parts[2] + ".1"
	}
	return "10.6.0.1"
}

// NodeIP returns the IP address for a node by index.
// Control planes start at .2, workers follow.
func (q *QEMUPlatform) NodeIP(role string, index int) string {
	base := strings.Split(q.spec.CIDR, ".")
	if len(base) < 3 {
		return ""
	}
	prefix := base[0] + "." + base[1] + "." + base[2]

	switch role {
	case "controlplane":
		return fmt.Sprintf("%s.%d", prefix, 2+index)
	case "worker":
		return fmt.Sprintf("%s.%d", prefix, 2+q.spec.ControlPlanes+index)
	default:
		return fmt.Sprintf("%s.%d", prefix, 2+index)
	}
}

// StatePath returns the path to the cluster state directory.
func (q *QEMUPlatform) StatePath() string {
	return filepath.Join(q.stateDir, q.spec.Name)
}

// ClusterExists checks if the cluster state directory exists.
func (q *QEMUPlatform) ClusterExists() bool {
	_, err := os.Stat(q.StatePath())
	return err == nil
}

// Generator returns a new Talos config generator initialized with
// the cluster's secrets. Returns nil for QEMU (use Kubeconfig/Talosconfig).
func (q *QEMUPlatform) Generator() *talosconfig.Generator {
	return nil
}

// RESTConfig returns a Kubernetes REST config from the kubeconfig.
func (q *QEMUPlatform) RESTConfig() ([]byte, error) {
	return q.Kubeconfig()
}

// checkPrerequisites verifies that the host can run QEMU clusters.
func (q *QEMUPlatform) checkPrerequisites(ctx context.Context) error {
	// Check talosctl.
	if _, err := exec.LookPath("talosctl"); err != nil {
		return fmt.Errorf("talosctl not found in PATH: %w", err)
	}

	// Check QEMU.
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		return fmt.Errorf("qemu-system-x86_64 not found in PATH: %w", err)
	}

	// Check KVM.
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("/dev/kvm not found (KVM required): %w", err)
	}

	return nil
}
