// Package talosconfig generates Talos machine configurations.
//
// This file implements Kamaji-specific worker config generation.
// Kamaji tenant control planes run as pods — there is no Talos control plane,
// so workers need specific patches:
//   - CA from Kamaji (not talosctl-generated)
//   - Kubelet image pinned to tenant CP version
//   - Bootstrap token from the tenant cluster
//
// Two modes of operation:
//
//  1. CSR signer mode: When talos-csr-signer is deployed as a sidecar in the
//     Kamaji TCP, workers get full Talos Machine PKI (apid/talosctl access).
//     Set CSRSigner fields to enable this mode.
//
//  2. Basic mode: Without CSR signer, the Talos API (talosctl) is non-functional.
//     Workers are managed via kubectl only. KubePrism is disabled.
package talosconfig

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// TenantWorkerParams holds the parameters for generating a Talos worker
// machine config that joins a Kamaji TenantControlPlane.
type TenantWorkerParams struct {
	// Name is the node hostname.
	Name string

	// Endpoint is the tenant control plane API endpoint (https://host:port).
	Endpoint string

	// CACert is the Kamaji-generated cluster CA certificate (PEM).
	CACert []byte

	// BootstrapToken is the bootstrap token for node authentication.
	// Format: <token-id>.<token-secret> (e.g. "abcdef.0123456789abcdef").
	BootstrapToken string

	// KubeletImage is the kubelet container image to pin.
	// Must match the tenant CP Kubernetes version.
	// Example: "ghcr.io/siderolabs/kubelet:v1.35.0"
	KubeletImage string

	// InstallDisk is the disk for Talos installation (e.g. "/dev/sda").
	InstallDisk string

	// DNSNameservers is the list of DNS nameservers.
	DNSNameservers []string

	// CSR signer fields. When set, the worker config includes Talos Machine PKI
	// for full talosctl access via the talos-csr-signer sidecar.

	// MachineToken is the Talos machine token for trustd/CSR signer authentication.
	MachineToken string

	// TalosCACert is the Talos OS CA certificate (PEM), used for Machine PKI.
	TalosCACert []byte

	// ClusterID is the Talos cluster identity UUID.
	ClusterID string

	// ClusterSecret is the Talos cluster secret.
	ClusterSecret string

	// TrustdEndpoints lists the trustd/CSR signer endpoints.
	// Workers contact these on port 50001 for apid certificate signing.
	TrustdEndpoints []string
}

// DefaultTenantWorkerParams returns params with sensible defaults.
func DefaultTenantWorkerParams(name string) TenantWorkerParams {
	return TenantWorkerParams{
		Name:           name,
		InstallDisk:    "/dev/sda",
		KubeletImage:   "ghcr.io/siderolabs/kubelet:v1.35.0",
		DNSNameservers: []string{"1.1.1.1", "8.8.8.8"},
	}
}

// HasCSRSigner returns true when CSR signer parameters are set.
func (p TenantWorkerParams) HasCSRSigner() bool {
	return p.MachineToken != "" && len(p.TalosCACert) > 0
}

// GenerateTenantWorkerConfig produces a Talos worker machine config YAML
// tailored for joining a Kamaji TenantControlPlane.
//
// This is a standalone config builder — it does NOT use Talos's secrets
// machinery because the CA comes from Kamaji, not from local generation.
// The output is a complete worker.yaml that can be applied with:
//
//	talosctl apply-config --config worker.yaml --nodes <node-ip>
//
// ## Talos API Access
//
// By default, the Talos API (apid/talosctl) is non-functional on these
// workers because trustd is not available in a hosted CP setup.
// Workers are managed via kubectl against the tenant API server.
//
// To restore full talosctl access, deploy talos-csr-signer
// (https://github.com/clastix/talos-csr-signer) as a sidecar in the
// Kamaji TenantControlPlane. It implements the trustd gRPC protocol
// and signs apid certificate requests. When deployed alongside the
// tenant control plane on port 50001, workers get both:
//   - Kubernetes PKI: kubelet → API server (port 6443)
//   - Talos Machine PKI: apid → talos-csr-signer (port 50001)
//
// rezusctl will automate talos-csr-signer deployment in a future release.
func GenerateTenantWorkerConfig(params TenantWorkerParams) ([]byte, error) {
	if err := validateTenantWorkerParams(params); err != nil {
		return nil, fmt.Errorf("validate params: %w", err)
	}

	endpoint := params.Endpoint
	if !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	caBase64 := base64.StdEncoding.EncodeToString(params.CACert)
	bootstrapToken := params.BootstrapToken
	disk := params.InstallDisk
	if disk == "" {
		disk = "/dev/sda"
	}

	nameservers := params.DNSNameservers
	if len(nameservers) == 0 {
		nameservers = []string{"1.1.1.1", "8.8.8.8"}
	}

	kubeletImage := params.KubeletImage
	if kubeletImage == "" {
		kubeletImage = "ghcr.io/siderolabs/kubelet:v1.35.0"
	}

	// Build the machine section.
	machineSection := buildMachineSection(params, kubeletImage, disk, nameservers)

	// Build the cluster section.
	clusterSection := buildClusterSection(params, endpoint, bootstrapToken, caBase64)

	config := fmt.Sprintf(`version: v1alpha1
%s
%s`, machineSection, clusterSection)

	return []byte(config), nil
}

// buildMachineSection generates the machine: block.
func buildMachineSection(params TenantWorkerParams, kubeletImage, disk string, nameservers []string) string {
	var b strings.Builder
	b.WriteString("machine:\n")
	b.WriteString("  type: worker\n")

	if params.HasCSRSigner() {
		// CSR signer mode: include Talos Machine PKI.
		talosCABase64 := base64.StdEncoding.EncodeToString(params.TalosCACert)
		b.WriteString("  token: ")
		b.WriteString(params.MachineToken)
		b.WriteString("\n")
		b.WriteString("  ca:\n")
		b.WriteString("    crt: ")
		b.WriteString(talosCABase64)
		b.WriteString("\n")
		b.WriteString("    key: \"\"\n")
	}

	b.WriteString("  kubelet:\n")
	b.WriteString("    image: ")
	b.WriteString(kubeletImage)
	b.WriteString("\n")
	if params.HasCSRSigner() {
		b.WriteString("    extraArgs:\n")
		b.WriteString("      rotate-certificates: \"true\"\n")
	}
	b.WriteString("  network:\n")
	b.WriteString("    nameservers: ")
	b.WriteString(formatYAMLList(nameservers))
	b.WriteString("\n")
	b.WriteString("  install:\n")
	b.WriteString("    disk: ")
	b.WriteString(disk)
	b.WriteString("\n")
	b.WriteString("    image: ghcr.io/siderolabs/installer:latest\n")

	if params.HasCSRSigner() {
		b.WriteString("  features:\n")
		b.WriteString("    rbac: true\n")
		b.WriteString("    kubePrism:\n")
		b.WriteString("      enabled: false\n")
	}

	return b.String()
}

// buildClusterSection generates the cluster: block.
func buildClusterSection(params TenantWorkerParams, endpoint, bootstrapToken, caBase64 string) string {
	var b strings.Builder
	b.WriteString("cluster:\n")

	if params.HasCSRSigner() && params.ClusterID != "" {
		b.WriteString("  id: ")
		b.WriteString(params.ClusterID)
		b.WriteString("\n")
		b.WriteString("  secret: ")
		b.WriteString(params.ClusterSecret)
		b.WriteString("\n")
	}

	b.WriteString("  controlPlane:\n")
	b.WriteString("    endpoint: ")
	b.WriteString(endpoint)
	b.WriteString("\n")
	b.WriteString("  clusterName: ")
	b.WriteString(params.Name)
	b.WriteString("\n")
	b.WriteString("  network:\n")
	b.WriteString("    dnsDomain: cluster.local\n")
	b.WriteString("    podSubnets:\n")
	b.WriteString("      - 10.244.0.0/16\n")
	b.WriteString("    serviceSubnets:\n")
	b.WriteString("      - 10.96.0.0/12\n")
	b.WriteString("  token: ")
	b.WriteString(bootstrapToken)
	b.WriteString("\n")
	b.WriteString("  ca:\n")
	b.WriteString("    crt: ")
	b.WriteString(caBase64)
	b.WriteString("\n")

	if !params.HasCSRSigner() {
		// Basic mode: no CSR signer. KubePrism disabled, no trustd.
		b.WriteString("  # KubePrism is disabled: no trustd in hosted control plane.\n")
		b.WriteString("  # To restore Talos API (talosctl) access, deploy talos-csr-signer\n")
		b.WriteString("  # (https://github.com/clastix/talos-csr-signer) as a sidecar in the\n")
		b.WriteString("  # Kamaji TenantControlPlane. It replaces trustd for hosted CPs.\n")
		b.WriteString("  # With talos-csr-signer deployed, KubePrism can be re-enabled.\n")
		b.WriteString("  network:\n")
		b.WriteString("    kubePrism:\n")
		b.WriteString("      enabled: false\n")
	}

	if params.HasCSRSigner() {
		b.WriteString("  discovery:\n")
		b.WriteString("    enabled: true\n")
		b.WriteString("    registries:\n")
		b.WriteString("      kubernetes:\n")
		b.WriteString("        disabled: true\n")
		b.WriteString("      service:\n")
		b.WriteString("        disabled: true\n")
	}

	return b.String()
}

// validateTenantWorkerParams checks required fields.
func validateTenantWorkerParams(params TenantWorkerParams) error {
	if params.Name == "" {
		return fmt.Errorf("name is required")
	}
	if params.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	if len(params.CACert) == 0 {
		return fmt.Errorf("CA certificate is required")
	}
	if params.BootstrapToken == "" {
		return fmt.Errorf("bootstrap token is required")
	}
	return nil
}

// formatYAMLList formats a string slice as a YAML list.
func formatYAMLList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	formatted := "\n"
	for _, item := range items {
		formatted += fmt.Sprintf("      - %s\n", item)
	}
	return strings.TrimRight(formatted, "\n")
}
