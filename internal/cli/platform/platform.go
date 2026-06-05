// Package platform defines the interface for cloud provider implementations.
// Each provider provisions compute, networking, and storage infrastructure
// for a RezusCloud cluster.
package platform

import "context"

// AuthConfig holds provider-specific authentication credentials.
type AuthConfig struct {
	// Provider is the platform identifier (e.g. "oci", "aws").
	Provider string `json:"provider"`
	// Credentials contains provider-specific key-value pairs.
	Credentials map[string]string `json:"credentials"`
}

// ClusterSpec defines the desired infrastructure for a cluster.
type ClusterSpec struct {
	Name              string `json:"name"`
	Region            string `json:"region"`
	ControlPlaneShape string `json:"controlPlaneShape"`
	ControlPlaneOCPU  int    `json:"controlPlaneOCPU"`
	ControlPlaneRAM   string `json:"controlPlaneRAM"`
	ControlPlaneDisk  string `json:"controlPlaneDisk"`
	TalosVersion      string `json:"talosVersion"`
	K8sVersion        string `json:"k8sVersion"`
	PodCIDR           string `json:"podCIDR"`
	ServiceCIDR       string `json:"serviceCIDR"`
	Arch              string `json:"arch"`
}

// Infrastructure holds the IDs and endpoints of provisioned resources.
type Infrastructure struct {
	// ControlPlaneEndpoint is the load balancer IP or DNS for the K8s API.
	ControlPlaneEndpoint string `json:"controlPlaneEndpoint"`
	// NodePublicIPs maps node names to their public IP addresses.
	NodePublicIPs map[string]string `json:"nodePublicIPs"`
	// ResourceIDs maps resource logical names to provider-specific IDs.
	// Used for state tracking and idempotency.
	ResourceIDs map[string]string `json:"resourceIDs"`
	// VPCID is the virtual network identifier.
	VPCID string `json:"vpcID"`
	// SubnetID is the subnet identifier.
	SubnetID string `json:"subnetID"`
	// SecurityGroupID is the network security group identifier.
	SecurityGroupID string `json:"securityGroupID"`
	// LoadBalancerID is the load balancer identifier.
	LoadBalancerID string `json:"loadBalancerID"`
	// ImageID is the custom Talos image identifier.
	ImageID string `json:"imageID"`
}

// NodeSpec defines a single node to be provisioned or joined.
type NodeSpec struct {
	Name         string `json:"name"`
	Role         string `json:"role"` // controlplane, worker
	InstallDisk  string `json:"installDisk"`
	StorageDisk  string `json:"storageDisk,omitempty"`
	NetworkIface string `json:"networkIface,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
}

// Platform defines the interface for cloud provider implementations.
// Each provider handles compute provisioning, networking setup,
// and Talos image management for a specific cloud platform.
type Platform interface {
	// Name returns the provider identifier (e.g. "oci", "aws").
	Name() string

	// Auth validates credentials and configures the provider client.
	// Should auto-import from standard credential locations when possible.
	Auth(ctx context.Context, config AuthConfig) error

	// Provision creates cloud infrastructure for a cluster.
	// Must be idempotent — skip already-provisioned resources.
	Provision(ctx context.Context, spec *ClusterSpec) (*Infrastructure, error)

	// Destroy tears down all cloud infrastructure for a cluster.
	Destroy(ctx context.Context, spec *ClusterSpec) error

	// GenerateMachineConfig produces a Talos machine config YAML for a node.
	// The config includes all necessary patches (CNI, WireGuard, provider-id, etc.).
	GenerateMachineConfig(node NodeSpec, infra *Infrastructure) ([]byte, error)

	// UploadTalosImage uploads and imports a Talos OS image for the platform.
	// Returns the image ID for instance creation.
	UploadTalosImage(ctx context.Context, talosVersion, arch string) (string, error)
}
