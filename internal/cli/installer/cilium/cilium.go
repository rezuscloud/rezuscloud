package cilium

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/rezuscloud/rezuscloud/internal/cli/helm"
	"github.com/rezuscloud/rezuscloud/internal/cli/installer"
)

const (
	releaseName     = "cilium"
	chartName       = "cilium"
	chartRepo       = "https://helm.cilium.io"
	defaultVersion  = "1.19.3"
	defaultMTU      = 1500
	healthNamespace = "kube-system"
)

// CiliumProvider implements installer.CNIProvider for Cilium CNI.
type CiliumProvider struct {
	installer installer.ChartInstaller
}

// New creates a new Cilium CNI provider.
func New(kubeconfigPath string) (*CiliumProvider, error) {
	installer, err := helm.NewInstaller(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("create helm installer: %w", err)
	}
	return &CiliumProvider{installer: installer}, nil
}

// NewWithInstaller creates a CiliumProvider with a custom installer (for testing).
func NewWithInstaller(installer installer.ChartInstaller) *CiliumProvider {
	return &CiliumProvider{installer: installer}
}

// Name returns the CNI provider identifier.
func (c *CiliumProvider) Name() string {
	return "cilium"
}

// Install installs Cilium via Helm.
func (c *CiliumProvider) Install(ctx context.Context, _ kubernetes.Interface, spec installer.CNISpec) error {
	version := spec.Version
	if version == "" {
		version = defaultVersion
	}

	mtu := spec.MTU
	if mtu == 0 {
		mtu = defaultMTU
	}

	values := buildValues(spec, mtu)

	// Point Cilium to the API server directly to avoid needing kube-proxy
	// for the ClusterIP service VIP during initialization.
	if spec.APIServerHost != "" {
		values["k8sServiceHost"] = spec.APIServerHost
		values["k8sServicePort"] = spec.APIServerPort
	}

	return c.installer.Install(ctx, installer.ChartConfig{
		Name:       releaseName,
		Repository: chartRepo,
		Chart:      chartName,
		Version:    version,
		Namespace:  healthNamespace,
		Values:     values,
		Wait:       true,
		Timeout:    600,
	}, nil)
}

// ConfigureUnderlay is a future extension point for WireGuard/KubeSpan configuration.
func (c *CiliumProvider) ConfigureUnderlay(_ context.Context, _ kubernetes.Interface, _ installer.TunnelSpec) error {
	return nil
}

// ConfigureIngress is a future extension point for Gateway API configuration.
func (c *CiliumProvider) ConfigureIngress(_ context.Context, _ kubernetes.Interface, _ installer.IngressSpec) error {
	return nil
}

// IsHealthy checks whether Cilium DaemonSet is running with all pods ready.
func (c *CiliumProvider) IsHealthy(ctx context.Context, client kubernetes.Interface) error {
	ds, err := client.AppsV1().DaemonSets(healthNamespace).Get(ctx, releaseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get cilium daemonset: %w", err)
	}

	if ds.Status.NumberReady == 0 {
		return fmt.Errorf("cilium has 0 ready pods")
	}

	if ds.Status.NumberReady < ds.Status.DesiredNumberScheduled {
		return fmt.Errorf("cilium ready %d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
	}

	return nil
}

// Uninstall removes Cilium from the cluster via Helm.
func (c *CiliumProvider) Uninstall(_ context.Context, _ kubernetes.Interface) error {
	return fmt.Errorf("cilium uninstall not yet implemented")
}

// WaitForHealthy polls until Cilium DaemonSet is ready or timeout.
func (c *CiliumProvider) WaitForHealthy(ctx context.Context, client kubernetes.Interface, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := c.IsHealthy(ctx, client); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cilium not healthy after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// buildValues constructs Helm values for Cilium based on the CNI spec.
func buildValues(spec installer.CNISpec, mtu int) map[string]interface{} {
	values := map[string]interface{}{
		"kubeProxyReplacement": "true",
		"routingMode":          "tunnel",
		"tunnelProtocol":       "geneve",
		"l7Proxy":              true,
		"operator":             map[string]interface{}{"replicas": 1},
		"gatewayAPI":           map[string]interface{}{"enabled": true},
		"mtu":                  map[string]interface{}{"value": mtu},
	}

	// Docker-specific: no encryption, no IPv6, no host firewall.
	// Docker containers run privileged with all capabilities, so eBPF works.
	if spec.APIServerHost != "" {
		values["encryption"] = map[string]interface{}{"enabled": false}
		values["hostFirewall"] = map[string]interface{}{"enabled": false}
		values["ipv4"] = map[string]interface{}{"enabled": true}
		values["ipv6"] = map[string]interface{}{"enabled": false}
		values["autoDirectNodeRoutes"] = false
	}

	if spec.TunnelType != "" {
		values["tunnelProtocol"] = spec.TunnelType
	}

	if spec.IPv6NativeRoutingCIDR != "" {
		values["ipv6NativeRoutingCIDR"] = spec.IPv6NativeRoutingCIDR
	}

	return values
}

// Ensure CiliumProvider satisfies installer.CNIProvider at compile time.
var _ installer.CNIProvider = (*CiliumProvider)(nil)

// Ensure appsv1 import is used (referenced via DaemonSet in IsHealthy).
var _ appsv1.DaemonSet
