package openstack

import (
	"context"
	"fmt"
)

// Provider implements the RezusCloud provider interface for OpenStack.
type Provider struct {
	cfg    *Config
	client *OpenStackClient
	api    *ManagementAPI
}

// NewProvider creates a new OpenStack provider.
func NewProvider(cfg *Config) (*Provider, error) {
	client, err := NewOpenStackClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("openstack client: %w", err)
	}
	return &Provider{
		cfg:    cfg,
		client: client,
	}, nil
}

// Register registers the provider with the RezusCloud management plane.
func (p *Provider) Register(ctx context.Context, apiURL, apiToken string) error {
	p.api = NewManagementAPI(apiURL, apiToken)

	// Verify OpenStack connectivity.
	if err := p.client.Ping(); err != nil {
		return fmt.Errorf("openstack connectivity check: %w", err)
	}

	// Ensure the Talos image exists in Glance.
	if _, err := p.client.EnsureImage(); err != nil {
		return fmt.Errorf("ensure talos image: %w", err)
	}

	// Register with management plane.
	return p.api.RegisterProvider(p.cfg.ProviderType, p.cfg.MachineTypes(), p.cfg.Regions())
}

// Heartbeat sends a heartbeat to the management plane.
func (p *Provider) Heartbeat(ctx context.Context) error {
	return p.api.UpdateProviderStatus(p.cfg.ProviderType, true, "")
}

// Run starts the provider's main loop, polling for provisioning requests.
func (p *Provider) Run(ctx context.Context) {
	// For v1, the provider registers and heartbeats.
	// Actual provisioning requests will be handled when the gRPC endpoint
	// is available in the management plane. For now, we just keep the
	// heartbeat alive.
	<-ctx.Done()
}
