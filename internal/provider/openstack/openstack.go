package openstack

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

// OpenStackClient wraps Gophercloud clients for Nova, Glance, and Neutron.
type OpenStackClient struct {
	compute *gophercloud.ServiceClient
	image   *gophercloud.ServiceClient
	network *gophercloud.ServiceClient
	cfg     *Config
}

// NewOpenStackClient authenticates to OpenStack and creates service clients.
func NewOpenStackClient(cfg *Config) (*OpenStackClient, error) {
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: cfg.AuthURL,
		Username:         cfg.Username,
		Password:         cfg.Password,
		TenantName:       cfg.ProjectName,
		DomainName:       cfg.UserDomainName,
	}

	provider, err := openstack.AuthenticatedClient(context.Background(), opts)
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}

	// Use HTTP (no TLS) for LAN-based kolla deployments.
	if strings.HasPrefix(cfg.AuthURL, "http://") {
		provider.HTTPClient = http.Client{
			Timeout: 60 * time.Second,
		}
	}

	region := cfg.Region
	if region == "" {
		region = "RegionOne"
	}

	compute, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{Region: region})
	if err != nil {
		return nil, fmt.Errorf("create compute client: %w", err)
	}

	image, err := openstack.NewImageV2(provider, gophercloud.EndpointOpts{Region: region})
	if err != nil {
		return nil, fmt.Errorf("create image client: %w", err)
	}

	network, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{Region: region})
	if err != nil {
		return nil, fmt.Errorf("create network client: %w", err)
	}

	return &OpenStackClient{
		compute: compute,
		image:   image,
		network: network,
		cfg:     cfg,
	}, nil
}

// Ping verifies OpenStack connectivity by listing servers (limit 1).
func (c *OpenStackClient) Ping() error {
	ctx := context.Background()
	opts := servers.ListOpts{Limit: 1}
	pager := servers.List(c.compute, opts)
	_, err := pager.AllPages(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	return nil
}

// EnsureImage checks if the Talos image exists in Glance.
// Returns the image ID, or empty string if not found.
// For v1, we don't auto-upload — the image must be pre-loaded via openstack-iac or manual upload.
func (c *OpenStackClient) EnsureImage() (string, error) {
	id, err := c.findImageByName(c.cfg.TalosImageName)
	if err != nil {
		return "", fmt.Errorf("find image: %w", err)
	}
	if id != "" {
		log.Printf("talos image %q exists in Glance: %s", c.cfg.TalosImageName, id)
	} else {
		log.Printf("WARNING: talos image %q not found in Glance — upload it manually or via openstack-iac", c.cfg.TalosImageName)
	}
	return id, nil
}

// ProvisionVM creates a Nova instance for a tenant's node group.
func (c *OpenStackClient) ProvisionVM(ctx context.Context, name, machineType, joinToken string) (*VMInfo, error) {
	flavor := c.cfg.FlavorForMachineType(machineType)
	imageName := c.cfg.TalosImageName

	// Resolve flavor and image to IDs.
	flavorID, err := c.resolveFlavorID(ctx, flavor)
	if err != nil {
		return nil, fmt.Errorf("resolve flavor %q: %w", flavor, err)
	}
	imageID, err := c.findImageByName(imageName)
	if err != nil {
		return nil, fmt.Errorf("resolve image %q: %w", imageName, err)
	}
	if imageID == "" {
		return nil, fmt.Errorf("image %q not found in Glance", imageName)
	}

	// Get network ID.
	netID, err := c.resolveNetworkID(ctx, c.cfg.NetworkName)
	if err != nil {
		return nil, fmt.Errorf("get network %s: %w", c.cfg.NetworkName, err)
	}

	// Build SideroLink kernel args as user-data.
	userData := c.cfg.SideroLinkKernelArgs(joinToken)

	createOpts := servers.CreateOpts{
		Name:      name,
		FlavorRef: flavorID,
		ImageRef:  imageID,
		Networks: []servers.Network{
			{UUID: netID},
		},
		UserData: []byte(userData),
		Metadata: map[string]string{
			"rezuscloud-tenant": strings.Split(name, "-")[0],
			"rezuscloud-type":   machineType,
		},
	}

	server, err := servers.Create(ctx, c.compute, &createOpts, nil).Extract()
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}

	log.Printf("waiting for VM %s (%s) to become ACTIVE...", server.Name, server.ID)

	// Wait for active state (up to 5 minutes).
	err = servers.WaitForStatus(ctx, c.compute, server.ID, "ACTIVE")
	if err != nil {
		return nil, fmt.Errorf("wait for active: %w", err)
	}

	// Get the server again for IP addresses.
	server, err = servers.Get(ctx, c.compute, server.ID).Extract()
	if err != nil {
		return nil, fmt.Errorf("get server after boot: %w", err)
	}

	info := &VMInfo{
		ID:     server.ID,
		Name:   server.Name,
		Status: server.Status,
	}

	// Extract IPs from addresses.
	for networkName, addrList := range server.Addresses {
		for _, addr := range addrList.([]interface{}) {
			addrMap := addr.(map[string]interface{})
			version, ok := addrMap["version"].(float64)
			if !ok {
				continue
			}
			ipAddr, ok := addrMap["addr"].(string)
			if !ok {
				continue
			}
			if version == 4 {
				info.IPv4 = ipAddr
				info.Network = networkName
			}
		}
	}

	log.Printf("provisioned VM %s (%s) at %s", server.Name, server.ID, info.IPv4)
	return info, nil
}

// DestroyVMs deletes all Nova instances with the rezuscloud-tenant metadata label.
func (c *OpenStackClient) DestroyVMs(ctx context.Context, tenant string) error {
	opts := servers.ListOpts{}
	pager := servers.List(c.compute, opts)
	pages, err := pager.AllPages(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}

	srvList, err := servers.ExtractServers(pages)
	if err != nil {
		return fmt.Errorf("extract servers: %w", err)
	}

	// Filter by metadata client-side.
	var filtered []servers.Server
	for _, srv := range srvList {
		if srv.Metadata["rezuscloud-tenant"] == tenant {
			filtered = append(filtered, srv)
		}
	}

	if len(filtered) == 0 {
		log.Printf("no VMs found for tenant %q", tenant)
		return nil
	}

	for _, srv := range filtered {
		log.Printf("destroying VM %s (%s)...", srv.Name, srv.ID)
		if err := servers.Delete(ctx, c.compute, srv.ID).ExtractErr(); err != nil {
			log.Printf("failed to delete %s: %v", srv.ID, err)
		}
	}

	return nil
}

func (c *OpenStackClient) findImageByName(name string) (string, error) {
	ctx := context.Background()
	listOpts := images.ListOpts{
		Name:   name,
		Status: imageStatusActive,
	}
	pager := images.List(c.image, listOpts)
	pages, err := pager.AllPages(ctx)
	if err != nil {
		return "", err
	}

	imgList, err := images.ExtractImages(pages)
	if err != nil {
		return "", err
	}

	if len(imgList) == 0 {
		return "", nil
	}
	return imgList[0].ID, nil
}

// VMInfo holds information about a provisioned VM.
type VMInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	IPv4    string `json:"ipv4"`
	Network string `json:"network"`
}

const imageStatusActive = "active"

// resolveFlavorID resolves a flavor name to its UUID by listing flavors.
func (c *OpenStackClient) resolveFlavorID(ctx context.Context, name string) (string, error) {
	pager := flavors.ListDetail(c.compute, flavors.ListOpts{})
	pages, err := pager.AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("list flavors: %w", err)
	}
	flavorList, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return "", fmt.Errorf("extract flavors: %w", err)
	}
	for _, f := range flavorList {
		if f.Name == name {
			return f.ID, nil
		}
	}
	return "", fmt.Errorf("flavor %q not found", name)
}

// resolveNetworkID resolves a network name to its UUID by listing networks.
func (c *OpenStackClient) resolveNetworkID(ctx context.Context, name string) (string, error) {
	pager := networks.List(c.network, networks.ListOpts{Name: name})
	pages, err := pager.AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("list networks: %w", err)
	}
	netList, err := networks.ExtractNetworks(pages)
	if err != nil {
		return "", fmt.Errorf("extract networks: %w", err)
	}
	for _, n := range netList {
		if n.Name == name {
			return n.ID, nil
		}
	}
	return "", fmt.Errorf("network %q not found", name)
}
