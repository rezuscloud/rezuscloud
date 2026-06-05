// Package docker implements the Platform interface for local Docker clusters.
// Talos Linux containers simulate real nodes, providing a fast local
// development and testing environment without cloud credentials.
package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/rezuscloud/rezuscloud/internal/cli/platform"
	"github.com/rezuscloud/rezuscloud/internal/cli/talosconfig"

	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
)

const (
	// TalosImage is the Docker image for Talos Linux.
	TalosImage = "ghcr.io/siderolabs/talos:latest"

	// LabelClusterName is the Docker label for cluster identification.
	LabelClusterName = "rezus.cloud/cluster"

	// LabelOwned marks containers managed by rezusctl.
	LabelOwned = "rezus.cloud/owned"

	// DefaultNetworkCIDR is the default Docker network CIDR.
	DefaultNetworkCIDR = "10.5.0.0/24"

	// DefaultKubernetesPort is the default Kubernetes API port.
	DefaultKubernetesPort = 6443

	// DefaultTalosPort is the default Talos API port.
	DefaultTalosPort = 50000
)

// DockerPlatform implements platform.Platform for local Docker clusters.
type DockerPlatform struct {
	client               *client.Client
	mappedKubernetesPort int
	mappedTalosPort      int
	networkCIDR          string
	networkName          string
	gatewayIP            string
	generator            *talosconfig.Generator
}

// New creates a new Docker platform provisioner.
func New(ctx context.Context) (*DockerPlatform, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	k8sPort, err := getAvailablePort(ctx)
	if err != nil {
		return nil, fmt.Errorf("get k8s port: %w", err)
	}

	talosPort, err := getAvailablePort(ctx)
	if err != nil {
		return nil, fmt.Errorf("get talos port: %w", err)
	}

	return &DockerPlatform{
		client:               cli,
		mappedKubernetesPort: k8sPort,
		mappedTalosPort:      talosPort,
		networkCIDR:          DefaultNetworkCIDR,
	}, nil
}

// Name returns the platform identifier.
func (d *DockerPlatform) Name() string {
	return "docker"
}

// Auth validates that Docker is available and running.
// No credentials needed — uses the local Docker socket.
func (d *DockerPlatform) Auth(ctx context.Context, _ platform.AuthConfig) error {
	_, err := d.client.Ping(ctx)
	if err != nil {
		return fmt.Errorf("docker not available: %w", err)
	}
	return nil
}

// Provision creates a Docker network for the cluster.
// Compute resources (containers) are created in CreateControlPlane/CreateWorker.
func (d *DockerPlatform) Provision(ctx context.Context, spec *platform.ClusterSpec) (*platform.Infrastructure, error) {
	d.networkName = spec.Name

	if err := d.createNetwork(ctx, spec); err != nil {
		return nil, fmt.Errorf("create network: %w", err)
	}

	return &platform.Infrastructure{
		ControlPlaneEndpoint: fmt.Sprintf("127.0.0.1:%d", d.mappedKubernetesPort),
		NodePublicIPs:        map[string]string{},
		ResourceIDs: map[string]string{
			"network": d.networkName,
		},
		VPCID: d.networkName,
	}, nil
}

// Destroy removes all Docker containers and the network for a cluster.
func (d *DockerPlatform) Destroy(ctx context.Context, spec *platform.ClusterSpec) error {
	if err := d.destroyNodes(ctx, spec.Name); err != nil {
		return fmt.Errorf("destroy nodes: %w", err)
	}

	if err := d.destroyNetwork(ctx, spec.Name); err != nil {
		return fmt.Errorf("destroy network: %w", err)
	}

	return nil
}

// GenerateMachineConfig generates a Talos machine config for a Docker node.
func (d *DockerPlatform) GenerateMachineConfig(node platform.NodeSpec, infra *platform.Infrastructure) ([]byte, error) {
	if d.generator == nil {
		return nil, fmt.Errorf("config generator not initialized - call InitGenerator first")
	}
	var role talosconfig.NodeRole
	switch node.Role {
	case "init":
		role = talosconfig.RoleInit
	case "worker":
		role = talosconfig.RoleWorker
	default:
		role = talosconfig.RoleControlPlane
	}
	return d.generator.Generate(talosconfig.NodeParams{
		Name:          node.Name,
		Role:          role,
		PlatformPatch: talosconfig.DockerPlatformPatch(),
	})
}

// UploadTalosImage pulls the Talos Docker image locally.
// No upload needed — just pull from GHCR.
func (d *DockerPlatform) UploadTalosImage(ctx context.Context, talosVersion, arch string) (string, error) {
	image := fmt.Sprintf("ghcr.io/siderolabs/talos:%s", talosVersion)
	// TODO: Pull image if not present
	return image, nil
}

// CreateControlPlane creates a single control plane container.
func (d *DockerPlatform) CreateControlPlane(ctx context.Context, clusterName string, node platform.NodeSpec, machineConfig []byte) (string, error) {
	cfg := &container.Config{
		Hostname: node.Name,
		Image:    TalosImage,
		Env: []string{
			"PLATFORM=container",
			"USERDATA=" + base64.StdEncoding.EncodeToString(machineConfig),
		},
		Labels: map[string]string{
			LabelOwned:       "true",
			LabelClusterName: clusterName,
		},
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", DefaultTalosPort)):      {},
			nat.Port(fmt.Sprintf("%d/tcp", DefaultKubernetesPort)): {},
		},
	}

	hostCfg := &container.HostConfig{
		Privileged:     true,
		CapAdd:         []string{"ALL"},
		SecurityOpt:    []string{"seccomp:unconfined"},
		ReadonlyRootfs: true,
		Mounts: []mount.Mount{
			{Type: mount.TypeTmpfs, Target: "/run"},
			{Type: mount.TypeTmpfs, Target: "/system"},
			{Type: mount.TypeTmpfs, Target: "/tmp"},
			{Type: mount.TypeVolume, Target: "/var"},
			{Type: mount.TypeVolume, Target: "/system/state"},
			{Type: mount.TypeVolume, Target: "/etc/cni"},
			{Type: mount.TypeVolume, Target: "/etc/kubernetes"},
			{Type: mount.TypeVolume, Target: "/usr/libexec/kubernetes"},
			{Type: mount.TypeVolume, Target: "/opt"},
		},
		PortBindings: nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", DefaultTalosPort)): []nat.PortBinding{
				{HostIP: "127.0.0.1", HostPort: strconv.Itoa(d.mappedTalosPort)},
			},
			nat.Port(fmt.Sprintf("%d/tcp", DefaultKubernetesPort)): []nat.PortBinding{
				{HostIP: "127.0.0.1", HostPort: strconv.Itoa(d.mappedKubernetesPort)},
			},
		},
	}

	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			d.networkName: {
				NetworkID: d.networkName,
			},
		},
	}

	resp, err := d.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, node.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}

	return resp.ID, nil
}

// CreateWorker creates a worker node container.
func (d *DockerPlatform) CreateWorker(ctx context.Context, clusterName string, node platform.NodeSpec, machineConfig []byte) (string, error) {
	cfg := &container.Config{
		Hostname: node.Name,
		Image:    TalosImage,
		Env: []string{
			"PLATFORM=container",
			"USERDATA=" + base64.StdEncoding.EncodeToString(machineConfig),
		},
		Labels: map[string]string{
			LabelOwned:       "true",
			LabelClusterName: clusterName,
		},
	}

	hostCfg := &container.HostConfig{
		Privileged:     true,
		CapAdd:         []string{"ALL"},
		SecurityOpt:    []string{"seccomp:unconfined"},
		ReadonlyRootfs: true,
		Mounts: []mount.Mount{
			{Type: mount.TypeTmpfs, Target: "/run"},
			{Type: mount.TypeTmpfs, Target: "/system"},
			{Type: mount.TypeTmpfs, Target: "/tmp"},
			{Type: mount.TypeVolume, Target: "/var"},
			{Type: mount.TypeVolume, Target: "/system/state"},
			{Type: mount.TypeVolume, Target: "/etc/cni"},
			{Type: mount.TypeVolume, Target: "/etc/kubernetes"},
			{Type: mount.TypeVolume, Target: "/usr/libexec/kubernetes"},
			{Type: mount.TypeVolume, Target: "/opt"},
		},
	}

	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			d.networkName: {
				NetworkID: d.networkName,
			},
		},
	}

	resp, err := d.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, node.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}

	return resp.ID, nil
}

// KubeconfigEndpoint returns the local Kubernetes API endpoint (host-side).
func (d *DockerPlatform) KubeconfigEndpoint() string {
	return fmt.Sprintf("https://127.0.0.1:%d", d.mappedKubernetesPort)
}

// GatewayIP returns the Docker network gateway IP.
func (d *DockerPlatform) GatewayIP() string {
	return d.gatewayIP
}

// GetNodeIP returns the IP address of a container in the Docker network.
func (d *DockerPlatform) GetNodeIP(containerName string) string {
	ctx := context.Background()
	container, err := d.client.ContainerInspect(ctx, containerName)
	if err != nil {
		return d.gatewayIP
	}
	for _, netSettings := range container.NetworkSettings.Networks {
		if netSettings.IPAddress != "" {
			return netSettings.IPAddress
		}
	}
	return d.gatewayIP
}

// Kubeconfig returns the admin kubeconfig bytes for this cluster.
func (d *DockerPlatform) Kubeconfig() ([]byte, error) {
	if d.generator == nil {
		return nil, fmt.Errorf("generator not initialized")
	}
	return d.generator.GenerateKubeconfig([]string{d.KubeconfigEndpoint()})
}

// TalosEndpoint returns the local Talos API endpoint.
func (d *DockerPlatform) TalosEndpoint() string {
	return fmt.Sprintf("127.0.0.1:%d", d.mappedTalosPort)
}

// InitGenerator creates the Talos config generator for this cluster.
func (d *DockerPlatform) InitGenerator(clusterName, kubernetesVersion string) error {
	// Use the Docker gateway IP for the in-cluster endpoint so
	// kubelets can reach the API server from inside the container network.
	endpoint := talosconfig.InClusterEndpoint(d.gatewayIP, DefaultKubernetesPort)
	gen, err := talosconfig.NewGenerator(talosconfig.ClusterParams{
		ClusterName:          clusterName,
		ControlPlaneEndpoint: endpoint,
		KubernetesVersion:    kubernetesVersion,
	})
	if err != nil {
		return fmt.Errorf("create config generator: %w", err)
	}
	d.generator = gen
	return nil
}

// SetGenerator sets the Talos config generator for this cluster.
func (d *DockerPlatform) SetGenerator(gen *talosconfig.Generator) {
	d.generator = gen
}

// Close releases the Docker client.
func (d *DockerPlatform) Close() error {
	if d.client != nil {
		return d.client.Close()
	}
	return nil
}

// Client returns the underlying Docker client for advanced operations.
func (d *DockerPlatform) Client() *client.Client {
	return d.client
}

// DestroyNetwork removes the Docker network for a cluster.
// Safe to call even if the network does not exist.
func (d *DockerPlatform) DestroyNetwork(ctx context.Context, clusterName string) error {
	return d.destroyNetwork(ctx, clusterName)
}

// Generator returns the Talos config generator for this cluster.
func (d *DockerPlatform) Generator() *talosconfig.Generator {
	return d.generator
}

// Secrets returns the Talos secrets bundle for this cluster.
func (d *DockerPlatform) Secrets() *secrets.Bundle {
	if d.generator == nil {
		return nil
	}
	return d.generator.Secrets()
}

func getAvailablePort(ctx context.Context) (int, error) {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()

	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0, err
	}

	return strconv.Atoi(portStr)
}
