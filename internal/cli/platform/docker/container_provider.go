package docker

import (
	"context"
	"fmt"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// DockerContainerProvider manages Docker containers for Talos nodes.
type DockerContainerProvider struct {
	client *client.Client
}

// NewContainerProvider creates a new Docker container resource provider.
func NewContainerProvider(cli *client.Client) *DockerContainerProvider {
	return &DockerContainerProvider{client: cli}
}

// Type returns the resource type identifier.
func (p *DockerContainerProvider) Type() string {
	return "docker:container"
}

// Create provisions a new Docker container.
// Inputs: name, cluster_name, role (init/controlplane/worker), machine_config (base64),
//
//	network_id, k8s_port, talos_port
//
// Outputs: id, name
func (p *DockerContainerProvider) Create(ctx context.Context, inputs map[string]interface{}) (map[string]interface{}, error) {
	name := strVal(inputs, "name")
	clusterName := strVal(inputs, "cluster_name")
	role := strVal(inputs, "role")
	machineConfigB64 := strVal(inputs, "machine_config")
	networkID := strVal(inputs, "network_id")

	cfg := &container.Config{
		Hostname: name,
		Image:    TalosImage,
		Env: []string{
			"PLATFORM=container",
			"USERDATA=" + machineConfigB64,
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

	// Control plane and init nodes expose ports
	if role != "worker" {
		k8sPort := intVal(inputs, "k8s_port")
		talosPort := intVal(inputs, "talos_port")

		cfg.ExposedPorts = nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", DefaultKubernetesPort)): {},
			nat.Port(fmt.Sprintf("%d/tcp", DefaultTalosPort)):      {},
		}

		hostCfg.PortBindings = nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", DefaultTalosPort)): []nat.PortBinding{
				{HostIP: "127.0.0.1", HostPort: strconv.Itoa(talosPort)},
			},
			nat.Port(fmt.Sprintf("%d/tcp", DefaultKubernetesPort)): []nat.PortBinding{
				{HostIP: "127.0.0.1", HostPort: strconv.Itoa(k8sPort)},
			},
		}
	}

	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkID: {NetworkID: networkID},
		},
	}

	resp, err := p.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	return map[string]interface{}{
		"id":   resp.ID,
		"name": name,
	}, nil
}

// Read fetches the current state of a Docker container.
func (p *DockerContainerProvider) Read(ctx context.Context, id string) (map[string]interface{}, error) {
	ctr, err := p.client.ContainerInspect(ctx, id)
	if err != nil {
		return nil, nil
	}

	return map[string]interface{}{
		"id":    ctr.ID,
		"name":  ctr.Name,
		"state": ctr.State.Status,
	}, nil
}

// Update recreates a Docker container (containers are not updatable in-place).
func (p *DockerContainerProvider) Update(ctx context.Context, id string, inputs map[string]interface{}) (map[string]interface{}, error) {
	// Delete old, create new
	if err := p.Delete(ctx, id); err != nil {
		return nil, err
	}
	return p.Create(ctx, inputs)
}

// Delete removes a Docker container.
func (p *DockerContainerProvider) Delete(ctx context.Context, id string) error {
	_ = p.client.ContainerStop(ctx, id, container.StopOptions{})
	if err := p.client.ContainerRemove(ctx, id, container.RemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}); err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// FindContainersByCluster finds all containers for a cluster.
func FindContainersByCluster(ctx context.Context, cli *client.Client, clusterName string) ([]string, error) {
	filter := filters.NewArgs()
	filter.Add("label", LabelOwned+"=true")
	filter.Add("label", LabelClusterName+"="+clusterName)

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	ids := make([]string, 0, len(containers))
	for _, ctr := range containers {
		ids = append(ids, ctr.ID)
	}
	return ids, nil
}

// intVal extracts an int from an inputs map.
func intVal(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}
