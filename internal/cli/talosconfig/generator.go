package talosconfig

import (
	"crypto/x509"
	"fmt"
	"net/netip"
	"strings"
	"time"

	siderx509 "github.com/siderolabs/crypto/x509"
	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// NodeRole determines the Talos machine type.
type NodeRole string

const (
	RoleControlPlane NodeRole = "controlplane"
	RoleInit         NodeRole = "init"
	RoleWorker       NodeRole = "worker"
)

// ClusterParams holds cluster-wide configuration shared across all nodes.
type ClusterParams struct {
	// ClusterName is the Kubernetes cluster name.
	ClusterName string
	// ControlPlaneEndpoint is the Kubernetes API endpoint (https://host:port).
	ControlPlaneEndpoint string
	// KubernetesVersion is the K8s version to install (e.g. "1.35.0").
	KubernetesVersion string
	// TalosVersion is the Talos version contract for config generation (e.g. "v1.12").
	TalosVersion string
	// PodCIDR is the pod network CIDR.
	PodCIDR string
	// ServiceCIDR is the service network CIDR.
	ServiceCIDR string
	// SecretsBundle is the cluster-wide secrets. If nil, a new bundle is generated.
	SecretsBundle *secrets.Bundle
}

// NodeParams holds node-specific configuration for machine config generation.
type NodeParams struct {
	// Name is the node hostname.
	Name string
	// Role is the node role (controlplane or worker).
	Role NodeRole
	// PlatformPatch is an optional function that applies platform-specific
	// config modifications to the generated provider.
	PlatformPatch func(config.Provider) error
}

// Generator creates Talos machine configs from cluster and node parameters.
type Generator struct {
	input *generate.Input
}

// NewGenerator creates a config generator with shared cluster secrets.
// Call once per cluster, then call Generate for each node.
func NewGenerator(params ClusterParams) (*Generator, error) {
	if params.ClusterName == "" {
		return nil, fmt.Errorf("cluster name is required")
	}
	if params.ControlPlaneEndpoint == "" {
		return nil, fmt.Errorf("control plane endpoint is required")
	}

	k8sVersion := params.KubernetesVersion
	if k8sVersion == "" {
		k8sVersion = constants.DefaultKubernetesVersion
	}

	var versionContract *config.VersionContract
	if params.TalosVersion != "" {
		var err error
		versionContract, err = config.ParseContractFromVersion(params.TalosVersion)
		if err != nil {
			return nil, fmt.Errorf("parse talos version %q: %w", params.TalosVersion, err)
		}
	}

	var secretsBundle *secrets.Bundle
	if params.SecretsBundle != nil {
		secretsBundle = params.SecretsBundle
	} else {
		var err error
		secretsBundle, err = secrets.NewBundle(secrets.NewFixedClock(time.Now()), versionContract)
		if err != nil {
			return nil, fmt.Errorf("generate secrets: %w", err)
		}
	}

	opts := []generate.Option{
		generate.WithVersionContract(versionContract),
		generate.WithSecretsBundle(secretsBundle),
		generate.WithClusterDiscovery(false),
	}

	input, err := generate.NewInput(
		params.ClusterName,
		params.ControlPlaneEndpoint,
		k8sVersion,
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("create config input: %w", err)
	}

	return &Generator{input: input}, nil
}

// Secrets returns the cluster secrets bundle for persistence.
func (g *Generator) Secrets() *secrets.Bundle {
	return g.input.Options.SecretsBundle
}

// Generate creates a Talos machine config YAML for a single node.
func (g *Generator) Generate(params NodeParams) ([]byte, error) {
	machineType, err := roleToMachineType(params.Role)
	if err != nil {
		return nil, err
	}

	cfg, err := g.input.Config(machineType)
	if err != nil {
		return nil, fmt.Errorf("generate config for %q: %w", params.Name, err)
	}

	if params.PlatformPatch != nil {
		if err := params.PlatformPatch(cfg); err != nil {
			return nil, fmt.Errorf("apply platform patch to %q: %w", params.Name, err)
		}
	}

	data, err := cfg.Bytes()
	if err != nil {
		return nil, fmt.Errorf("marshal config for %q: %w", params.Name, err)
	}

	return data, nil
}

// GenerateControlPlane is a convenience method for generating a control plane config.
func (g *Generator) GenerateControlPlane(name string, platformPatch func(config.Provider) error) ([]byte, error) {
	return g.Generate(NodeParams{
		Name:          name,
		Role:          RoleControlPlane,
		PlatformPatch: platformPatch,
	})
}

// GenerateWorker is a convenience method for generating a worker config.
func (g *Generator) GenerateWorker(name string, platformPatch func(config.Provider) error) ([]byte, error) {
	return g.Generate(NodeParams{
		Name:          name,
		Role:          RoleWorker,
		PlatformPatch: platformPatch,
	})
}

// GenerateTalosconfig generates a client config for talosctl access.
func (g *Generator) GenerateTalosconfig(endpoints []string) ([]byte, error) {
	talosCfg, err := g.input.Talosconfig()
	if err != nil {
		return nil, fmt.Errorf("generate talosconfig: %w", err)
	}

	talosCfg.Contexts[talosCfg.Context].Endpoints = endpoints

	return talosCfg.Bytes()
}

// GenerateKubeconfig generates a kubeconfig for kubectl access.
func (g *Generator) GenerateKubeconfig(endpoints []string) ([]byte, error) {
	endpoint := "https://127.0.0.1:6443"
	if len(endpoints) > 0 {
		endpoint = endpoints[0]
	}
	if !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	k8sCA := g.input.Options.SecretsBundle.Certs.K8s

	// Parse the K8s CA to create a certificate authority.
	ca, err := siderx509.NewCertificateAuthorityFromCertificateAndKey(k8sCA)
	if err != nil {
		return nil, fmt.Errorf("parse k8s CA: %w", err)
	}

	// Generate admin client cert signed by the K8s CA.
	adminCert, err := siderx509.NewKeyPair(ca,
		siderx509.CommonName("admin"),
		siderx509.Organization("system:masters"),
		siderx509.NotBefore(time.Now().Add(-10*time.Second)),
		siderx509.NotAfter(time.Now().Add(365*24*time.Hour)),
		siderx509.KeyUsage(x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment),
		siderx509.ExtKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}),
	)
	if err != nil {
		return nil, fmt.Errorf("generate admin cert: %w", err)
	}

	adminPEM := siderx509.NewCertificateAndKeyFromKeyPair(adminCert)

	cfg := clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			g.input.ClusterName: {
				Server:                   endpoint,
				CertificateAuthorityData: k8sCA.Crt,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"admin@" + g.input.ClusterName: {
				ClientCertificateData: adminPEM.Crt,
				ClientKeyData:         adminPEM.Key,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"admin@" + g.input.ClusterName: {
				Cluster:   g.input.ClusterName,
				Namespace: "default",
				AuthInfo:  "admin@" + g.input.ClusterName,
			},
		},
		CurrentContext: "admin@" + g.input.ClusterName,
	}

	return clientcmd.Write(cfg)
}

// roleToMachineType converts a NodeRole to a Talos machine.Type.
func roleToMachineType(role NodeRole) (machine.Type, error) {
	switch role {
	case RoleControlPlane:
		return machine.TypeControlPlane, nil
	case RoleInit:
		return machine.TypeInit, nil
	case RoleWorker:
		return machine.TypeWorker, nil
	default:
		return machine.TypeUnknown, fmt.Errorf("unknown node role: %q", role)
	}
}

// InClusterEndpoint computes the in-cluster control plane endpoint
// from a Docker network gateway IP and port.
func InClusterEndpoint(gatewayIP string, port int) string {
	addr, err := netip.ParseAddr(gatewayIP)
	if err != nil {
		return fmt.Sprintf("https://%s:%d", gatewayIP, port)
	}
	return fmt.Sprintf("https://%s:%d", addr.String(), port)
}
