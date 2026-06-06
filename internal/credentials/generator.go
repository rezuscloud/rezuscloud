// Package credentials generates kubeconfig and talosconfig from tenant secrets.
package credentials

import (
	"encoding/json"
	"fmt"
	"time"

	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/role"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigRequest contains the parameters for generating a kubeconfig.
type KubeconfigRequest struct {
	ClusterName     string
	ClusterEndpoint string // e.g. "https://192.168.1.10:6443"
	Bundle          *secrets.Bundle
}

// TalosconfigRequest contains the parameters for generating a talosconfig.
type TalosconfigRequest struct {
	ClusterName     string
	MachineLinkAddr string // e.g. "192.168.1.5:50180"
	Bundle          *secrets.Bundle
}

// GenerateKubeconfig generates an admin kubeconfig from the secrets bundle.
func GenerateKubeconfig(req KubeconfigRequest) ([]byte, error) {
	if req.Bundle == nil {
		return nil, fmt.Errorf("secrets bundle is required")
	}

	// Generate admin client certificate.
	adminCert, err := req.Bundle.GenerateTalosAPIClientCertificate(role.MakeSet(role.Admin))
	if err != nil {
		return nil, fmt.Errorf("generate admin cert: %w", err)
	}

	endpoint := req.ClusterEndpoint
	if endpoint == "" {
		endpoint = "https://127.0.0.1:6443"
	}

	// Build kubeconfig using client-go API.
	kubeConfig := clientcmdapi.NewConfig()
	kubeConfig.Clusters[req.ClusterName] = &clientcmdapi.Cluster{
		Server:                   endpoint,
		CertificateAuthorityData: req.Bundle.Certs.K8s.Crt,
	}
	kubeConfig.AuthInfos["admin"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: adminCert.Crt,
		ClientKeyData:         adminCert.Key,
	}
	kubeConfig.Contexts[req.ClusterName] = &clientcmdapi.Context{
		Cluster:  req.ClusterName,
		AuthInfo: "admin",
	}
	kubeConfig.CurrentContext = req.ClusterName

	return clientcmd.Write(*kubeConfig)
}

// GenerateTalosconfig generates an admin talosconfig from the secrets bundle.
func GenerateTalosconfig(req TalosconfigRequest) ([]byte, error) {
	if req.Bundle == nil {
		return nil, fmt.Errorf("secrets bundle is required")
	}

	// Generate client certificate.
	clientCert, err := req.Bundle.GenerateTalosAPIClientCertificate(role.MakeSet(role.Admin))
	if err != nil {
		return nil, fmt.Errorf("generate client cert: %w", err)
	}

	// Determine endpoint.
	endpoint := req.MachineLinkAddr
	if endpoint == "" {
		endpoint = "127.0.0.1:50000"
	}

	// Build talosconfig.
	talosConfig := clientconfig.NewConfig(
		req.ClusterName,
		[]string{endpoint},
		req.Bundle.Certs.OS.Crt,
		clientCert,
	)

	return talosConfig.Bytes()
}

// GenerateSecretsBundle creates a new secrets bundle for a tenant.
func GenerateSecretsBundle(talosVersion string) (*secrets.Bundle, error) {
	vc, err := config.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("parse version: %w", err)
	}

	bundle, err := secrets.NewBundle(secrets.NewFixedClock(time.Now().UTC()), vc)
	if err != nil {
		return nil, fmt.Errorf("generate bundle: %w", err)
	}

	return bundle, nil
}

// SecretsBundleJSON serializes a secrets bundle to JSON for storage.
func SecretsBundleJSON(bundle *secrets.Bundle) (json.RawMessage, error) {
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("marshal secrets: %w", err)
	}
	return data, nil
}

// UnmarshalSecretsBundle deserializes a secrets bundle from JSON.
// Returns nil only if the input is empty or nil; an error is returned for
// malformed JSON.
// The Clock field is json:"-" in the bundle type, so we restore it with
// NewFixedClock(time.Now()) so callers can immediately generate client certs.
func UnmarshalSecretsBundle(data []byte) (*secrets.Bundle, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var bundle secrets.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("unmarshal secrets: %w", err)
	}
	// The Clock field doesn't survive JSON serialization. Restore it so the
	// bundle can be used to generate client certs.
	bundle.Clock = secrets.NewFixedClock(time.Now().UTC())
	return &bundle, nil
}
