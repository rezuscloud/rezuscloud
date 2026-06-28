// Package installer defines interfaces for boot-time platform component
// installation (CNI, DNS, TLS, Helm charts).
// CNI, DNS, TLS, and other infrastructure components implement these interfaces.
package installer

import (
	"context"
	"io"

	"k8s.io/client-go/kubernetes"
)

// TunnelSpec defines the underlay tunnel configuration.
type TunnelSpec struct {
	// Mode is the underlay mode: "ipv6-wireguard" or "kubespan".
	Mode string `json:"mode"`
	// MTU is the tunnel MTU (e.g. 1360 for Geneve over WireGuard).
	MTU int `json:"mtu"`
	// Keepalive is the WireGuard persistent keepalive interval.
	Keepalive string `json:"keepalive"`
	// WireGuardPeers maps node names to their WireGuard endpoint addresses.
	WireGuardPeers map[string]string `json:"wireguardPeers,omitempty"`
}

// IngressSpec defines the ingress configuration.
type IngressSpec struct {
	// Domain is the wildcard domain (e.g. "*.mycloud.dev").
	Domain string `json:"domain"`
	// TLSHost is the specific host for the TLS certificate.
	TLSHost string `json:"tlsHost"`
	// TLSSecretName is the name of the TLS secret.
	TLSSecretName string `json:"tlsSecretName"`
	// Namespace is the namespace for Gateway resources.
	Namespace string `json:"namespace"`
}

// CNIProvider defines the interface for CNI implementations.
type CNIProvider interface {
	// Name returns the CNI provider identifier (e.g. "cilium").
	Name() string

	// Install installs the CNI via Helm into the cluster.
	Install(ctx context.Context, client kubernetes.Interface, spec CNISpec) error

	// ConfigureUnderlay configures the tunnel underlay (WireGuard, KubeSpan, etc.).
	ConfigureUnderlay(ctx context.Context, client kubernetes.Interface, tunnel TunnelSpec) error

	// ConfigureIngress sets up Gateway API resources for ingress routing.
	ConfigureIngress(ctx context.Context, client kubernetes.Interface, ingress IngressSpec) error

	// IsHealthy checks whether the CNI is fully operational.
	IsHealthy(ctx context.Context, client kubernetes.Interface) error

	// Uninstall removes the CNI from the cluster.
	Uninstall(ctx context.Context, client kubernetes.Interface) error
}

// CNISpec defines CNI configuration.
type CNISpec struct {
	// Type is the CNI identifier (e.g. "cilium").
	Type string `json:"type"`
	// Version is the Helm chart version.
	Version string `json:"version"`
	// TunnelType is the overlay tunnel type (e.g. "geneve").
	TunnelType string `json:"tunnelType"`
	// MTU is the configured MTU.
	MTU int `json:"mtu"`
	// IPv6NativeRoutingCIDR is the CIDR for native IPv6 routing.
	IPv6NativeRoutingCIDR string `json:"ipv6NativeRoutingCIDR,omitempty"`
	// APIServerHost is the direct API server address (bypasses ClusterIP VIP).
	APIServerHost string `json:"apiServerHost,omitempty"`
	// APIServerPort is the direct API server port.
	APIServerPort int `json:"apiServerPort,omitempty"`
}

// DNSProvider defines the interface for DNS providers.
type DNSProvider interface {
	// Name returns the DNS provider identifier (e.g. "cloudflare").
	Name() string

	// Configure sets up the external-dns deployment for this provider.
	Configure(ctx context.Context, client kubernetes.Interface, config DNSConfig) error

	// IsHealthy checks whether external-dns is running and managing records.
	IsHealthy(ctx context.Context, client kubernetes.Interface) error
}

// DNSConfig defines DNS provider configuration.
type DNSConfig struct {
	// Provider is the DNS provider name.
	Provider string `json:"provider"`
	// Zone is the DNS zone name (e.g. "mycloud.dev").
	Zone string `json:"zone"`
	// SecretRef references the Kubernetes Secret with API credentials.
	SecretRef string `json:"secretRef"`
	// Namespace is the deployment namespace.
	Namespace string `json:"namespace"`
}

// TLSProvider defines the interface for TLS certificate providers.
type TLSProvider interface {
	// Name returns the TLS provider identifier (e.g. "letsencrypt").
	Name() string

	// Configure sets up cert-manager with the appropriate issuer.
	Configure(ctx context.Context, client kubernetes.Interface, config TLSConfig) error

	// IssueCertificate creates a Certificate resource and waits for issuance.
	IssueCertificate(ctx context.Context, client kubernetes.Interface, domain string) error

	// IsHealthy checks whether cert-manager is ready.
	IsHealthy(ctx context.Context, client kubernetes.Interface) error
}

// TLSConfig defines TLS provider configuration.
type TLSConfig struct {
	// Email is the registration email for the CA.
	Email string `json:"email"`
	// Server is the ACME server URL.
	Server string `json:"server"`
	// DNSChallengeProvider is the DNS01 challenge provider name.
	DNSChallengeProvider string `json:"dnsChallengeProvider"`
	// SecretRef references the Kubernetes Secret with DNS provider credentials.
	SecretRef string `json:"secretRef"`
	// Namespace is the deployment namespace.
	Namespace string `json:"namespace"`
}

// ChartInstaller installs Helm charts programmatically.
type ChartInstaller interface {
	// Install installs or upgrades a Helm chart.
	Install(ctx context.Context, config ChartConfig, out io.Writer) error

	// Rollback rolls back a stuck Helm release.
	Rollback(ctx context.Context, releaseName, namespace string) error

	// IsInstalled checks whether a Helm release exists.
	IsInstalled(ctx context.Context, releaseName, namespace string) (bool, error)
}

// ChartConfig defines a Helm chart installation.
type ChartConfig struct {
	Name         string `json:"name"`
	Repository   string `json:"repository"`
	Chart        string `json:"chart"`
	Version      string `json:"version"`
	Namespace    string `json:"namespace"`
	Values       map[string]interface{}
	Wait         bool `json:"wait"`
	Timeout      int  `json:"timeout"`
	DisableHooks bool `json:"disableHooks"` // Skip Helm hooks (--no-hooks).
}
