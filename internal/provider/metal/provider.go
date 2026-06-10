package metal

import (
	"context"
	"fmt"
	"log"
	"time"

	"golang.org/x/sync/errgroup"
)

// Provider implements the RezusCloud bare metal provider.
type Provider struct {
	cfg     *Config
	scanner *DiscoveryScanner
	api     *ManagementAPI
}

// NewProvider creates a new bare metal provider.
func NewProvider(cfg *Config) (*Provider, error) {
	scanner := NewDiscoveryScanner(cfg)
	return &Provider{
		cfg:     cfg,
		scanner: scanner,
	}, nil
}

// Register registers the provider with the RezusCloud management plane.
func (p *Provider) Register(ctx context.Context, apiURL, apiToken string) error {
	p.api = NewManagementAPI(apiURL, apiToken)

	// Register with management plane.
	if err := p.api.RegisterProvider(p.cfg.ProviderType, p.cfg.MachineTypes(), p.cfg.Regions()); err != nil {
		return fmt.Errorf("register provider: %w", err)
	}

	// Wire up discovery callbacks.
	p.scanner.OnDiscover(func(m *DiscoveredMachine) {
		log.Printf("registering discovered machine %s with management plane", m.IP)
		if err := p.api.RegisterDiscoveredMachine(m.IP, p.cfg.ProviderType); err != nil {
			log.Printf("failed to register machine %s: %v", m.IP, err)
		}
	})

	p.scanner.OnLost(func(ip string) {
		log.Printf("marking machine %s as lost", ip)
		if err := p.api.MarkMachineLost(ip); err != nil {
			log.Printf("failed to mark machine %s lost: %v", ip, err)
		}
	})

	log.Printf("provider-metal registered with %s", apiURL)
	return nil
}

// Heartbeat sends a heartbeat to the management plane.
func (p *Provider) Heartbeat(ctx context.Context) error {
	if p.api == nil {
		return fmt.Errorf("provider not registered")
	}
	return p.api.UpdateProviderStatus(p.cfg.ProviderType, true, "")
}

// Run starts the provider's main loop: discovery scanning + heartbeat.
// Blocks until context is cancelled.
func (p *Provider) Run(ctx context.Context) {
	g, gCtx := errgroup.WithContext(ctx)

	// Discovery scanner goroutine.
	if p.cfg.Discovery.Enabled {
		g.Go(func() error {
			log.Printf("starting network discovery on %s (port %d, interval %ds)",
				p.cfg.Discovery.Subnet,
				p.cfg.Discovery.Port,
				p.cfg.Discovery.IntervalSeconds,
			)
			p.scanner.Run(gCtx)
			return nil
		})
	} else {
		log.Printf("network discovery disabled")
	}

	// Heartbeat goroutine (every 30s).
	g.Go(func() error {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-gCtx.Done():
				return nil
			case <-ticker.C:
				if err := p.Heartbeat(gCtx); err != nil {
					log.Printf("heartbeat failed: %v", err)
				}
			}
		}
	})

	_ = g.Wait()
}
