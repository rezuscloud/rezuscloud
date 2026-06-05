package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/hashicorp/go-multierror"
)

// destroyNodes removes all Docker containers for a cluster.
func (d *DockerPlatform) destroyNodes(ctx context.Context, clusterName string) error {
	filter := filters.NewArgs()
	filter.Add("label", LabelOwned+"=true")
	filter.Add("label", LabelClusterName+"="+clusterName)

	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: true, Filters: filter})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	errCh := make(chan error, len(containers))

	for _, ctr := range containers {
		go func(ctr container.Summary) {
			errCh <- d.client.ContainerRemove(ctx, ctr.ID, container.RemoveOptions{
				RemoveVolumes: true,
				Force:         true,
			})
		}(ctr)
	}

	var multiErr *multierror.Error
	for range containers {
		multiErr = multierror.Append(multiErr, <-errCh)
	}

	return multiErr.ErrorOrNil()
}
