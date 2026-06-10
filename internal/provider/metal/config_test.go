package metal

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	content := `
providerType: metal-test
siderolinkEndpoint: "https://test.rezus.cloud:443"
discovery:
  enabled: true
  subnet: "192.168.7.0/24"
  port: 50000
  intervalSeconds: 30
  timeoutSeconds: 1
  concurrency: 20
`
	tmpFile, err := os.CreateTemp("", "metal-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.ProviderType != "metal-test" {
		t.Errorf("expected providerType metal-test, got %s", cfg.ProviderType)
	}
	if cfg.Discovery.Subnet != "192.168.7.0/24" {
		t.Errorf("expected subnet 192.168.7.0/24, got %s", cfg.Discovery.Subnet)
	}
	if cfg.Discovery.Port != 50000 {
		t.Errorf("expected port 50000, got %d", cfg.Discovery.Port)
	}
	if cfg.Discovery.IntervalSeconds != 30 {
		t.Errorf("expected intervalSeconds 30, got %d", cfg.Discovery.IntervalSeconds)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	content := `
discovery:
  enabled: true
  subnet: "10.0.0.0/24"
`
	tmpFile, err := os.CreateTemp("", "metal-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.ProviderType != "metal" {
		t.Errorf("expected default providerType metal, got %s", cfg.ProviderType)
	}
	if cfg.Discovery.Port != 50000 {
		t.Errorf("expected default port 50000, got %d", cfg.Discovery.Port)
	}
	if cfg.Discovery.IntervalSeconds != 60 {
		t.Errorf("expected default intervalSeconds 60, got %d", cfg.Discovery.IntervalSeconds)
	}
	if cfg.Discovery.TimeoutSeconds != 2 {
		t.Errorf("expected default timeoutSeconds 2, got %d", cfg.Discovery.TimeoutSeconds)
	}
	if cfg.Discovery.Concurrency != 50 {
		t.Errorf("expected default concurrency 50, got %d", cfg.Discovery.Concurrency)
	}
}

func TestLoadConfigMissingSubnet(t *testing.T) {
	content := `
discovery:
  enabled: true
`
	tmpFile, err := os.CreateTemp("", "metal-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	_, err = LoadConfig(tmpFile.Name())
	if err == nil {
		t.Error("expected error for missing subnet")
	}
}

func TestMachineTypes(t *testing.T) {
	cfg := &Config{
		Machines: []MachineConfig{
			{ID: "m1", Tags: map[string]string{"role": "worker"}},
			{ID: "m2", Tags: map[string]string{"role": "control-plane"}},
		},
	}

	types := cfg.MachineTypes()
	if len(types) != 2 {
		t.Errorf("expected 2 machine types, got %d", len(types))
	}
}

func TestMachineTypesDefault(t *testing.T) {
	cfg := &Config{}
	types := cfg.MachineTypes()
	if len(types) != 2 {
		t.Errorf("expected 2 default machine types, got %d", len(types))
	}
}

func TestRegions(t *testing.T) {
	cfg := &Config{
		Machines: []MachineConfig{
			{ID: "m1", Tags: map[string]string{"region": "basement"}},
			{ID: "m2", Tags: map[string]string{"region": "garage"}},
		},
	}

	regions := cfg.Regions()
	if len(regions) != 2 {
		t.Errorf("expected 2 regions, got %d", len(regions))
	}
}

func TestRegionsDefault(t *testing.T) {
	cfg := &Config{}
	regions := cfg.Regions()
	if len(regions) != 1 || regions[0] != "default" {
		t.Errorf("expected default region, got %v", regions)
	}
}

func TestSideroLinkKernelArgs(t *testing.T) {
	cfg := &Config{
		SideroLinkEndpoint: "https://manage.rezus.cloud:443",
	}

	args := cfg.SideroLinkKernelArgs("abc123")
	expected := "siderolink.api=https://manage.rezus.cloud:443?jointoken=abc123&wireguard_over_grpc=true"
	if args != expected {
		t.Errorf("expected %q, got %q", expected, args)
	}
}

func TestSideroLinkKernelArgsEmpty(t *testing.T) {
	cfg := &Config{
		SideroLinkEndpoint: "https://manage.rezus.cloud:443",
	}

	args := cfg.SideroLinkKernelArgs("")
	expected := "siderolink.api=https://manage.rezus.cloud:443"
	if args != expected {
		t.Errorf("expected %q, got %q", expected, args)
	}
}

func TestParseCIDR(t *testing.T) {
	cfg := &Config{
		Discovery: DiscoveryConfig{
			Subnet: "192.168.7.0/24",
		},
	}

	cidr, err := cfg.ParseCIDR()
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	if cidr.String() != "192.168.7.0/24" {
		t.Errorf("expected 192.168.7.0/24, got %s", cidr.String())
	}
}

func TestParseCIDRInvalid(t *testing.T) {
	cfg := &Config{
		Discovery: DiscoveryConfig{
			Subnet: "not-a-cidr",
		},
	}

	_, err := cfg.ParseCIDR()
	if err == nil {
		t.Error("expected error for invalid CIDR")
	}
}
