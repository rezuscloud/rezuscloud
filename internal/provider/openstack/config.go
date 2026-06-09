// Package openstack implements the RezusCloud provider interface for
// OpenStack (Nova compute + Glance images + Neutron networking).
//
// Per ADR 12/13, the provider is minimal: Provision creates VMs, Destroy
// deletes them. Config delivery uses SideroLink (pull model). The provider
// never pushes Talos config or builds images.
package openstack

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the provider configuration loaded from YAML.
type Config struct {
	// ProviderType is the provider identifier registered with the management plane.
	// Defaults to "openstack".
	ProviderType string `yaml:"providerType"`

	// AuthURL is the Keystone v3 endpoint (e.g. "http://192.168.7.123:5000/v3").
	AuthURL string `yaml:"authUrl"`

	// Username for Keystone authentication. Can be set via OS_USERNAME env var.
	Username string `yaml:"username"`

	// Password for Keystone authentication. Can be set via OS_PASSWORD env var.
	Password string `yaml:"password"`

	// ProjectName (tenant). Can be set via OS_PROJECT_NAME env var.
	ProjectName string `yaml:"projectName"`

	// UserDomainName. Can be set via OS_USER_DOMAIN_NAME env var.
	UserDomainName string `yaml:"userDomainName"`

	// ProjectDomainName. Can be set via OS_PROJECT_DOMAIN_NAME env var.
	ProjectDomainName string `yaml:"projectDomainName"`

	// Region. Can be set via OS_REGION_NAME env var.
	Region string `yaml:"region"`

	// TalosImageURL is the URL to download the Talos OpenStack QCOW2 image.
	// If empty, uses the default from the Talos Image Factory.
	TalosImageURL string `yaml:"talosImageUrl"`

	// TalosImageName is the name to use when uploading to Glance.
	TalosImageName string `yaml:"talosImageName"`

	// SideroLinkEndpoint is the management plane's SideroLink URL
	// (e.g. "https://manage.rezus.cloud:443").
	SideroLinkEndpoint string `yaml:"siderolinkEndpoint"`

	// NetworkName is the OpenStack network to attach instances to.
	// Defaults to "ext-net".
	NetworkName string `yaml:"networkName"`

	// MachineTypeFlavor maps machine type (control-plane, worker) to OpenStack flavor name.
	MachineTypeFlavor map[string]string `yaml:"machineTypeFlavor"`

	// MachineTypeDisk maps machine type to root disk size in GB.
	// If 0, boots from the image's default disk.
	MachineTypeDisk map[string]int `yaml:"machineTypeDisk"`
}

// LoadConfig reads a YAML config file and applies env var overrides.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{
		ProviderType:       "openstack",
		TalosImageName:     "talos-openstack",
		NetworkName:        "ext-net",
		SideroLinkEndpoint: "https://demo.rezus.cloud:443",
		MachineTypeFlavor: map[string]string{
			"control-plane": "SCS-2V-4-10",
			"worker":        "SCS-2V-8-20",
		},
		MachineTypeDisk: map[string]int{
			"control-plane": 20,
			"worker":        50,
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyEnvOverrides()

	if cfg.AuthURL == "" {
		return nil, fmt.Errorf("authUrl is required (or set OS_AUTH_URL)")
	}

	return cfg, nil
}

func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("OS_AUTH_URL"); v != "" {
		c.AuthURL = v
	}
	if v := os.Getenv("OS_USERNAME"); v != "" {
		c.Username = v
	}
	if v := os.Getenv("OS_PASSWORD"); v != "" {
		c.Password = v
	}
	if v := os.Getenv("OS_PROJECT_NAME"); v != "" {
		c.ProjectName = v
	}
	if v := os.Getenv("OS_USER_DOMAIN_NAME"); v != "" {
		c.UserDomainName = v
	}
	if v := os.Getenv("OS_PROJECT_DOMAIN_NAME"); v != "" {
		c.ProjectDomainName = v
	}
	if v := os.Getenv("OS_REGION_NAME"); v != "" {
		c.Region = v
	}
}

// TalosImageID returns the Glance image name to use (for lookup by name).
func (c *Config) TalosImageID() string {
	return c.TalosImageName
}

// FlavorForMachineType returns the OpenStack flavor name for a given machine type.
func (c *Config) FlavorForMachineType(mt string) string {
	if f, ok := c.MachineTypeFlavor[mt]; ok {
		return f
	}
	// Default: try SCS-2V-4-10
	return "SCS-2V-4-10"
}

// DiskForMachineType returns the root disk size for a given machine type.
func (c *Config) DiskForMachineType(mt string) int {
	if d, ok := c.MachineTypeDisk[mt]; ok {
		return d
	}
	return 20
}

// MachineTypes returns the list of supported machine types from the flavor map.
func (c *Config) MachineTypes() []string {
	types := make([]string, 0, len(c.MachineTypeFlavor))
	for k := range c.MachineTypeFlavor {
		types = append(types, k)
	}
	return types
}

// Regions returns the configured region as a slice.
func (c *Config) Regions() []string {
	if c.Region == "" {
		return []string{"RegionOne"}
	}
	return []string{c.Region}
}

// SideroLinkKernelArgs returns the kernel args string for SideroLink boot.
func (c *Config) SideroLinkKernelArgs(joinToken string) string {
	endpoint := c.SideroLinkEndpoint
	if !strings.Contains(endpoint, "jointoken") && joinToken != "" {
		endpoint += "?jointoken=" + joinToken + "&wireguard_over_grpc=true"
	}
	return "siderolink.api=" + endpoint
}
