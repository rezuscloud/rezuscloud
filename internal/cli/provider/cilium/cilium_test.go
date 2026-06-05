package cilium

import (
	"context"
	"io"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/cli/provider"
)

func TestCiliumProvider_Name(t *testing.T) {
	c := NewWithInstaller(&mockInstaller{})
	if c.Name() != "cilium" {
		t.Errorf("Name() = %q, want %q", c.Name(), "cilium")
	}
}

func TestBuildValues_Defaults(t *testing.T) {
	spec := provider.CNISpec{
		Type:    "cilium",
		Version: "1.19.3",
	}

	values := buildValues(spec, defaultMTU)

	if values["kubeProxyReplacement"] != "true" {
		t.Error("kubeProxyReplacement should be true")
	}

	if values["routingMode"] != "tunnel" {
		t.Error("routingMode should be tunnel")
	}

	if values["tunnelProtocol"] != "geneve" {
		t.Error("tunnelProtocol should be geneve")
	}

	if values["l7Proxy"] != true {
		t.Error("l7Proxy should be true (required by Gateway API)")
	}

	mtuVal, ok := values["mtu"].(map[string]interface{})
	if !ok {
		t.Fatal("mtu should be a map")
	}
	if mtuVal["value"] != defaultMTU {
		t.Errorf("mtu value = %v, want %d", mtuVal["value"], defaultMTU)
	}
}

func TestBuildValues_CustomMTU(t *testing.T) {
	spec := provider.CNISpec{
		Type: "cilium",
		MTU:  1360,
	}

	values := buildValues(spec, 1360)

	mtuVal := values["mtu"].(map[string]interface{})
	if mtuVal["value"] != 1360 {
		t.Errorf("mtu value = %v, want 1360", mtuVal["value"])
	}
}

func TestBuildValues_CustomTunnel(t *testing.T) {
	spec := provider.CNISpec{
		Type:       "cilium",
		TunnelType: "vxlan",
	}

	values := buildValues(spec, defaultMTU)

	if values["tunnelProtocol"] != "vxlan" {
		t.Errorf("tunnelProtocol = %v, want vxlan", values["tunnelProtocol"])
	}
}

func TestBuildValues_IPv6CIDR(t *testing.T) {
	spec := provider.CNISpec{
		Type:                  "cilium",
		IPv6NativeRoutingCIDR: "fd00:10:244::/48",
	}

	values := buildValues(spec, defaultMTU)

	if values["ipv6NativeRoutingCIDR"] != "fd00:10:244::/48" {
		t.Errorf("ipv6NativeRoutingCIDR = %v, want fd00:10:244::/48", values["ipv6NativeRoutingCIDR"])
	}
}

func TestBuildValues_GatewayAPI(t *testing.T) {
	spec := provider.CNISpec{Type: "cilium"}
	values := buildValues(spec, defaultMTU)

	gw, ok := values["gatewayAPI"].(map[string]interface{})
	if !ok {
		t.Fatal("gatewayAPI should be a map")
	}
	if gw["enabled"] != true {
		t.Error("gatewayAPI should be enabled")
	}
}

func TestBuildValues_Operator(t *testing.T) {
	spec := provider.CNISpec{Type: "cilium"}
	values := buildValues(spec, defaultMTU)

	op, ok := values["operator"].(map[string]interface{})
	if !ok {
		t.Fatal("operator should be a map")
	}
	if op["replicas"] != 1 {
		t.Errorf("operator replicas = %v, want 1", op["replicas"])
	}
}

func TestBuildValues_DockerSpecific(t *testing.T) {
	spec := provider.CNISpec{
		Type:          "cilium",
		APIServerHost: "10.5.0.1",
		APIServerPort: 6443,
	}

	values := buildValues(spec, defaultMTU)

	// Docker: encryption disabled.
	enc, ok := values["encryption"].(map[string]interface{})
	if !ok {
		t.Fatal("encryption should be a map")
	}
	if enc["enabled"] != false {
		t.Error("encryption should be disabled for Docker")
	}

	// Docker: host firewall disabled.
	hf, ok := values["hostFirewall"].(map[string]interface{})
	if !ok {
		t.Fatal("hostFirewall should be a map")
	}
	if hf["enabled"] != false {
		t.Error("hostFirewall should be disabled for Docker")
	}

	// Docker: IPv6 disabled.
	ipv6, ok := values["ipv6"].(map[string]interface{})
	if !ok {
		t.Fatal("ipv6 should be a map")
	}
	if ipv6["enabled"] != false {
		t.Error("ipv6 should be disabled for Docker")
	}

	// Docker: IPv4 enabled.
	ipv4, ok := values["ipv4"].(map[string]interface{})
	if !ok {
		t.Fatal("ipv4 should be a map")
	}
	if ipv4["enabled"] != true {
		t.Error("ipv4 should be enabled for Docker")
	}

	// Docker: autoDirectNodeRoutes disabled.
	if values["autoDirectNodeRoutes"] != false {
		t.Error("autoDirectNodeRoutes should be false for Docker")
	}
}

func TestBuildValues_NoDockerDefaults(t *testing.T) {
	spec := provider.CNISpec{Type: "cilium"}

	values := buildValues(spec, defaultMTU)

	// Non-Docker: no encryption/hostFirewall/ipv4/ipv6 keys.
	if _, ok := values["encryption"]; ok {
		t.Error("encryption should not be set for non-Docker")
	}
	if _, ok := values["hostFirewall"]; ok {
		t.Error("hostFirewall should not be set for non-Docker")
	}
	if _, ok := values["ipv4"]; ok {
		t.Error("ipv4 should not be set for non-Docker")
	}
	if _, ok := values["ipv6"]; ok {
		t.Error("ipv6 should not be set for non-Docker")
	}
}

type mockInstaller struct {
	installed  bool
	installErr error
}

func (m *mockInstaller) Install(_ context.Context, _ provider.ChartConfig, _ io.Writer) error {
	return m.installErr
}

func (m *mockInstaller) Rollback(_ context.Context, _, _ string) error { return nil }

func (m *mockInstaller) IsInstalled(_ context.Context, _, _ string) (bool, error) {
	return m.installed, nil
}
