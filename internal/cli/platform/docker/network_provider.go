package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// DockerNetworkProvider manages Docker networks for Talos clusters.
type DockerNetworkProvider struct {
	client *client.Client
}

// NewNetworkProvider creates a new Docker network resource provider.
func NewNetworkProvider(cli *client.Client) *DockerNetworkProvider {
	return &DockerNetworkProvider{client: cli}
}

// Type returns the resource type identifier.
func (p *DockerNetworkProvider) Type() string {
	return "docker:network"
}

// Create provisions a new Docker network.
// Inputs: cluster_name, cidr
// Outputs: id, gateway
func (p *DockerNetworkProvider) Create(ctx context.Context, inputs map[string]interface{}) (map[string]interface{}, error) {
	clusterName := strVal(inputs, "cluster_name")
	cidr := strVal(inputs, "cidr")
	if cidr == "" {
		cidr = DefaultNetworkCIDR
	}

	resp, err := p.client.NetworkCreate(ctx, clusterName, network.CreateOptions{
		Driver: "bridge",
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{Subnet: cidr},
			},
		},
		Labels: map[string]string{
			LabelOwned:       "true",
			LabelClusterName: clusterName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("network create: %w", err)
	}

	gateway := ""
	netDetail, err := p.client.NetworkInspect(ctx, resp.ID, network.InspectOptions{})
	if err == nil {
		for _, cfg := range netDetail.IPAM.Config {
			if cfg.Gateway != "" {
				gateway = cfg.Gateway
				break
			}
		}
	}

	return map[string]interface{}{
		"id":      resp.ID,
		"gateway": gateway,
		"cidr":    cidr,
		"name":    clusterName,
	}, nil
}

// Read fetches the current state of a Docker network.
func (p *DockerNetworkProvider) Read(ctx context.Context, id string) (map[string]interface{}, error) {
	netDetail, err := p.client.NetworkInspect(ctx, id, network.InspectOptions{})
	if err != nil {
		return nil, nil
	}

	gateway := ""
	for _, cfg := range netDetail.IPAM.Config {
		if cfg.Gateway != "" {
			gateway = cfg.Gateway
			break
		}
	}

	return map[string]interface{}{
		"id":      netDetail.ID,
		"gateway": gateway,
		"name":    netDetail.Name,
	}, nil
}

// Update is a no-op for Docker networks (immutable).
func (p *DockerNetworkProvider) Update(ctx context.Context, id string, inputs map[string]interface{}) (map[string]interface{}, error) {
	return p.Read(ctx, id)
}

// Delete removes a Docker network.
func (p *DockerNetworkProvider) Delete(ctx context.Context, id string) error {
	if err := p.client.NetworkRemove(ctx, id); err != nil {
		return fmt.Errorf("remove network %s: %w", id, err)
	}
	return nil
}

// FindNetworkByName finds a Docker network by cluster name label.
func FindNetworkByName(ctx context.Context, cli *client.Client, clusterName string) (string, error) {
	filter := filters.NewArgs()
	filter.Add("label", LabelOwned+"=true")
	filter.Add("label", LabelClusterName+"="+clusterName)

	networks, err := cli.NetworkList(ctx, network.ListOptions{Filters: filter})
	if err != nil {
		return "", fmt.Errorf("list networks: %w", err)
	}
	if len(networks) == 0 {
		return "", nil
	}
	return networks[0].ID, nil
}

// strVal extracts a string from an inputs map.
func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
