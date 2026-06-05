package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/rezuscloud/rezuscloud/internal/cli/platform"
)

// createNetwork creates a Docker network for the cluster.
func (d *DockerPlatform) createNetwork(ctx context.Context, spec *platform.ClusterSpec) error {
	resp, err := d.client.NetworkCreate(ctx, spec.Name, network.CreateOptions{
		Driver: "bridge",
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet: d.networkCIDR,
				},
			},
		},
		Labels: map[string]string{
			LabelOwned:       "true",
			LabelClusterName: spec.Name,
		},
	})
	if err != nil {
		return fmt.Errorf("network create: %w", err)
	}

	d.networkName = resp.ID

	// Inspect the network to capture the gateway IP.
	netDetail, err := d.client.NetworkInspect(ctx, resp.ID, network.InspectOptions{})
	if err != nil {
		return fmt.Errorf("network inspect: %w", err)
	}
	for _, cfg := range netDetail.IPAM.Config {
		if cfg.Gateway != "" {
			d.gatewayIP = cfg.Gateway
			break
		}
	}

	return nil
}

// destroyNetwork removes the Docker network for a cluster.
func (d *DockerPlatform) destroyNetwork(ctx context.Context, clusterName string) error {
	filter := filters.NewArgs()
	filter.Add("label", LabelOwned+"=true")
	filter.Add("label", LabelClusterName+"="+clusterName)

	networks, err := d.client.NetworkList(ctx, network.ListOptions{Filters: filter})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}

	for _, net := range networks {
		if err := d.client.NetworkRemove(ctx, net.ID); err != nil {
			return fmt.Errorf("remove network %s: %w", net.ID, err)
		}
	}

	return nil
}
