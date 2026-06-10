// Package metal implements the RezusCloud bare metal provider.
//
// Per ADR 12/13, the provider only creates/deletes machines. Config delivery
// uses SideroLink (pull model). The provider never pushes Talos config.
//
// provider-metal adds auto-discovery: it scans a configured subnet for
// Talos nodes in maintenance mode (gRPC API on port 50000) and registers
// them with the management plane as machines awaiting configuration.
package metal

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the provider configuration loaded from YAML.
type Config struct {
	// ProviderType is the provider identifier registered with the management plane.
	// Defaults to "metal".
	ProviderType string `yaml:"providerType"`

	// SideroLinkEndpoint is the management plane's SideroLink URL.
	SideroLinkEndpoint string `yaml:"siderolinkEndpoint"`

	// Discovery configures network auto-discovery of Talos maintenance-mode nodes.
	Discovery DiscoveryConfig `yaml:"discovery"`

	// Machines is the static inventory of known machines with BMC info.
	// For v1, auto-discovery replaces the need for manual inventory.
	Machines []MachineConfig `yaml:"machines,omitempty"`
}

// DiscoveryConfig controls network scanning for Talos nodes.
type DiscoveryConfig struct {
	// Enabled enables auto-discovery scanning.
	Enabled bool `yaml:"enabled"`

	// Subnet is the CIDR to scan (e.g. "192.168.7.0/24").
	Subnet string `yaml:"subnet"`

	// Port is the Talos API port to probe. Default: 50000.
	Port int `yaml:"port"`

	// IntervalSeconds is how often to scan. Default: 60.
	IntervalSeconds int `yaml:"intervalSeconds"`

	// TimeoutSeconds is per-probe timeout. Default: 2.
	TimeoutSeconds int `yaml:"timeoutSeconds"`

	// Concurrency is how many IPs to probe in parallel. Default: 50.
	Concurrency int `yaml:"concurrency"`
}

// MachineConfig describes a known bare metal machine (static inventory).
type MachineConfig struct {
	// ID is the unique machine identifier.
	ID string `yaml:"id"`

	// BMC address for power control (future: IPMI/Redfish).
	BMC BMCConfig `yaml:"bmc,omitempty"`

	// PXE MAC address (future: PXE boot).
	// PXE struct {
	// 	MAC  string `yaml:"mac"`
	// 	Mode string `yaml:"mode"` // "uefi" or "bios"
	// } `yaml:"pxe,omitempty"`

	// Tags for matching provision requests.
	Tags map[string]string `yaml:"tags,omitempty"`
}

// BMCConfig describes the BMC access for a machine.
type BMCConfig struct {
	Address  string `yaml:"address"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// LoadConfig reads a YAML config file and applies env var overrides.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{
		ProviderType:       "metal",
		SideroLinkEndpoint: "https://demo.rezus.cloud:443",
		Discovery: DiscoveryConfig{
			Enabled:         true,
			Port:            50000,
			IntervalSeconds: 60,
			TimeoutSeconds:  2,
			Concurrency:     50,
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()

	if cfg.Discovery.Subnet == "" && cfg.Discovery.Enabled {
		return nil, fmt.Errorf("discovery.subnet is required when discovery is enabled")
	}

	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Discovery.Port == 0 {
		c.Discovery.Port = 50000
	}
	if c.Discovery.IntervalSeconds == 0 {
		c.Discovery.IntervalSeconds = 60
	}
	if c.Discovery.TimeoutSeconds == 0 {
		c.Discovery.TimeoutSeconds = 2
	}
	if c.Discovery.Concurrency == 0 {
		c.Discovery.Concurrency = 50
	}
}

// MachineTypes returns the supported machine types from static inventory.
func (c *Config) MachineTypes() []string {
	seen := map[string]bool{}
	for _, m := range c.Machines {
		if role, ok := m.Tags["role"]; ok && !seen[role] {
			seen[role] = true
		}
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	if len(types) == 0 {
		types = []string{"control-plane", "worker"}
	}
	return types
}

// Regions returns the configured regions from static inventory.
func (c *Config) Regions() []string {
	seen := map[string]bool{}
	for _, m := range c.Machines {
		if region, ok := m.Tags["region"]; ok && !seen[region] {
			seen[region] = true
		}
	}
	regions := make([]string, 0, len(seen))
	for r := range seen {
		regions = append(regions, r)
	}
	if len(regions) == 0 {
		regions = []string{"default"}
	}
	return regions
}

// SideroLinkKernelArgs returns the kernel args string for SideroLink boot.
func (c *Config) SideroLinkKernelArgs(joinToken string) string {
	endpoint := c.SideroLinkEndpoint
	if !strings.Contains(endpoint, "jointoken") && joinToken != "" {
		endpoint += "?jointoken=" + joinToken + "&wireguard_over_grpc=true"
	}
	return "siderolink.api=" + endpoint
}

// ParseCIDR returns the parsed subnet for discovery.
func (c *Config) ParseCIDR() (*net.IPNet, error) {
	_, cidr, err := net.ParseCIDR(c.Discovery.Subnet)
	if err != nil {
		return nil, fmt.Errorf("parse subnet %q: %w", c.Discovery.Subnet, err)
	}
	return cidr, nil
}
