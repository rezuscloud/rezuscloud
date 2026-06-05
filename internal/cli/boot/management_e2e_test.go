//go:build docker && kamaji

// TestManagementCluster_KamajiWithCSRSigner tests the full Kamaji + CSR signer
// stack in Docker. Requires the "kamaji" build tag.
//
// NOTE: This test requires a Docker Talos cluster with working pod networking.
// Basic Docker Talos uses flannel which may not support cert-manager webhooks.
// For CI, this test runs on self-hosted runners with proper CNI.
// Run locally: go test -tags docker,kamaji -run TestManagementCluster_KamajiWithCSRSigner

package boot_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	siderx509 "github.com/siderolabs/crypto/x509"

	"github.com/rezuscloud/rezuscloud/internal/cli/helm"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform/docker"
	"github.com/rezuscloud/rezuscloud/internal/cli/provider"
	"github.com/rezuscloud/rezuscloud/internal/cli/talosconfig"
)

// TestManagementCluster_KamajiWithCSRSigner boots a management cluster,
// installs Kamaji with the talos-csr-signer sidecar, creates a tenant
// control plane, joins a Talos worker, and verifies talosctl access.
//
// Architecture:
//
//	Docker Network (10.5.0.0/24)
//	+-- mgmt-controlplane-1 (Talos, management K8s API on 6443)
//	|   +-- kamaji controller pod
//	|   +-- kamaji-etcd pod
//	|   +-- tenant-test-cp pod (Kamaji TCP)
//	|       +-- kube-apiserver (6443) -> Kubernetes PKI
//	|       +-- talos-csr-signer (50001) -> Talos Machine PKI
//	+-- tenant-worker-1 (Talos, joins tenant TCP)
//	    +-- kubelet -> tenant API server (NodePort on mgmt node)
//	    +-- apid -> tenant CSR signer (NodePort on mgmt node)
//
// No cert-manager: TLS certs for CSR signer are pre-generated.
//
// Prerequisites: Docker running, talosctl in PATH, GHCR access.
func TestManagementCluster_KamajiWithCSRSigner(t *testing.T) {
	const (
		clusterName       = "mgmt"
		tenantName        = "tenant-test"
		kubernetesVersion = "1.35.0"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// ================================================================
	// Phase 1: Boot management cluster
	// ================================================================
	t.Log("=== Phase 1: Boot management cluster ===")
	dp, err := docker.New(ctx)
	if err != nil {
		t.Fatalf("docker.New: %v", err)
	}
	defer dp.Close()

	infra, err := dp.Provision(ctx, &platform.ClusterSpec{
		Name:         clusterName,
		K8sVersion:   kubernetesVersion,
		TalosVersion: "v1.12",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Logf("Network: %s", infra.VPCID)

	if err := dp.InitGenerator(clusterName, kubernetesVersion); err != nil {
		t.Fatalf("init generator: %v", err)
	}

	cpName := clusterName + "-controlplane-1"
	machineConfig, err := dp.GenerateMachineConfig(platform.NodeSpec{
		Name: cpName, Role: "init", Hostname: cpName,
	}, infra)
	if err != nil {
		t.Fatalf("generate config: %v", err)
	}

	cpID, err := dp.CreateControlPlane(ctx, clusterName, platform.NodeSpec{
		Name: cpName, Role: "init", Hostname: cpName,
	}, machineConfig)
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}
	t.Logf("Control plane: %s", cpID)

	if err := waitForHTTP(ctx, dp.KubeconfigEndpoint(), 120*time.Second); err != nil {
		t.Fatalf("k8s API ready: %v", err)
	}
	time.Sleep(10 * time.Second) // Let API server settle.

	mgmtKubeconfig, err := dp.Kubeconfig()
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	mgmtConfig, _ := clientcmd.RESTConfigFromKubeConfig(mgmtKubeconfig)
	mgmtClient, _ := kubernetes.NewForConfig(mgmtConfig)
	mgmtDynamic, _ := dynamic.NewForConfig(mgmtConfig)

	if err := waitForAPIServerReady(ctx, mgmtClient, 5*time.Minute); err != nil {
		t.Fatalf("API server ready: %v", err)
	}
	mgmtIP := dp.GetNodeIP(cpName)
	t.Logf("Management IP: %s", mgmtIP)

	// ================================================================
	// Phase 2: Install Kamaji (etcd + controller)
	// ================================================================
	t.Log("=== Phase 2: Install cert-manager + Kamaji ===")
	installer, err := helm.NewInstallerFromBytes(mgmtKubeconfig)
	if err != nil {
		t.Fatalf("helm installer: %v", err)
	}

	// Install cert-manager with cainjector disabled (Docker networking limitation).
	// The cainjector uses webhooks that require reliable pod-to-pod routing.
	// Without it, cert-manager still issues certs via its controller.
	err = installer.Install(ctx, provider.ChartConfig{
		Name: "cert-manager", Repository: "https://charts.jetstack.io",
		Chart: "cert-manager", Version: "v1.20.2", Namespace: "cert-manager",
		Values: map[string]interface{}{
			"crds":       map[string]interface{}{"enabled": true},
			"cainjector": map[string]interface{}{"enabled": false},
			"webhook": map[string]interface{}{
				"replicaCount": 0,
			},
		},
		Wait: true, Timeout: 300,
	}, &mgmtTestWriter{t})
	if err != nil {
		t.Fatalf("cert-manager install: %v", err)
	}
	t.Log("cert-manager installed")

	// Install Kamaji controller (needs cert-manager CRDs).
	err = installer.Install(ctx, provider.ChartConfig{
		Name: "kamaji", Repository: "https://clastix.github.io/charts",
		Chart: "kamaji", Version: "1.0.0", Namespace: "kamaji-system",
		Values: map[string]interface{}{
			"etcd": map[string]interface{}{"deploy": false},
		},
		Wait: true, Timeout: 300,
	}, &mgmtTestWriter{t})
	if err != nil {
		t.Fatalf("kamaji install: %v", err)
	}
	t.Log("kamaji controller installed")

	// Then install etcd (creates DataStore CR which needs Kamaji CRDs).
	err = installer.Install(ctx, provider.ChartConfig{
		Name: "kamaji-etcd", Repository: "https://clastix.github.io/charts",
		Chart: "kamaji-etcd", Version: "0.15.0", Namespace: "kamaji-system",
		Values: map[string]interface{}{}, Wait: true, Timeout: 300,
	}, &mgmtTestWriter{t})
	if err != nil {
		t.Fatalf("kamaji-etcd install: %v", err)
	}
	t.Log("kamaji-etcd installed")

	// ================================================================
	// Phase 3: Generate Talos secrets + CSR signer TLS
	// ================================================================
	t.Log("=== Phase 3: Generate Talos secrets ===")
	secretsBundle := dp.Secrets()
	if secretsBundle == nil {
		t.Fatal("no secrets bundle")
	}

	talosCACert := secretsBundle.Certs.OS.Crt
	talosCAKey := secretsBundle.Certs.OS.Key
	machineToken := secretsBundle.TrustdInfo.Token
	clusterID := secretsBundle.Cluster.ID
	clusterSecret := secretsBundle.Cluster.Secret
	t.Logf("cluster ID: %s", clusterID)

	// ================================================================
	// Phase 4: Create K8s secrets for CSR signer
	// ================================================================
	t.Log("=== Phase 4: Create CSR signer secrets ===")
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

	// Talos CA secret (cert + key + token).
	talosCASecret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]interface{}{"name": tenantName + "-talos-ca", "namespace": "default"},
			"type":       "Opaque",
			"data": map[string]interface{}{
				"tls.crt": base64.StdEncoding.EncodeToString(talosCACert),
				"tls.key": base64.StdEncoding.EncodeToString(ed25519PEMFix(talosCAKey)),
				"token":   base64.StdEncoding.EncodeToString([]byte(machineToken)),
			},
		},
	}
	_, err = mgmtDynamic.Resource(secretGVR).Namespace("default").Create(ctx, talosCASecret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create talos-ca secret: %v", err)
	}

	// Pre-generate TLS cert for CSR signer gRPC server.
	csrTLSCert, csrTLSKey, err := generateCSRSignerTLS(talosCACert, talosCAKey, mgmtIP)
	if err != nil {
		t.Fatalf("generate CSR signer TLS: %v", err)
	}
	csrTLSSecret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]interface{}{"name": tenantName + "-talos-tls-cert", "namespace": "default"},
			"type":       "kubernetes.io/tls",
			"data": map[string]interface{}{
				"tls.crt": base64.StdEncoding.EncodeToString(csrTLSCert),
				"tls.key": base64.StdEncoding.EncodeToString(csrTLSKey),
			},
		},
	}
	_, err = mgmtDynamic.Resource(secretGVR).Namespace("default").Create(ctx, csrTLSSecret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create tls-cert secret: %v", err)
	}
	t.Log("CSR signer secrets created")

	// ================================================================
	// Phase 5: Create TenantControlPlane with CSR signer sidecar
	// ================================================================
	t.Log("=== Phase 5: Create TenantControlPlane ===")
	tcpGVR := schema.GroupVersionResource{
		Group: "kamaji.clastix.io", Version: "v1alpha1", Resource: "tenantcontrolplanes",
	}

	tcp := buildTCPSpec(tenantName, mgmtIP, kubernetesVersion)
	_, err = mgmtDynamic.Resource(tcpGVR).Namespace("default").Create(ctx, tcp, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create TCP: %v", err)
	}
	t.Log("TenantControlPlane created")

	// ================================================================
	// Phase 6: Wait for TCP to become available
	// ================================================================
	t.Log("=== Phase 6: Wait for TCP ===")
	if err := waitForTCPAvailable(ctx, mgmtDynamic, "default", tenantName, 5*time.Minute); err != nil {
		dumpTCPDebug(ctx, mgmtDynamic, mgmtClient, tcpGVR, tenantName)
		t.Fatalf("TCP available: %v", err)
	}
	t.Log("TenantControlPlane available!")

	tcpService, err := mgmtClient.CoreV1().Services("default").Get(ctx, tenantName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get TCP service: %v", err)
	}
	var apiServerNodePort int32
	for _, port := range tcpService.Spec.Ports {
		t.Logf("  svc port: name=%s port=%d nodePort=%d", port.Name, port.Port, port.NodePort)
		if port.Port == 6443 || port.Name == "kube-apiserver" {
			apiServerNodePort = port.NodePort
		}
	}
	if apiServerNodePort == 0 {
		t.Fatal("no API server NodePort found")
	}
	tenantAPIEndpoint := fmt.Sprintf("https://%s:%d", mgmtIP, apiServerNodePort)
	t.Logf("Tenant API: %s", tenantAPIEndpoint)

	// ================================================================
	// Phase 7: Get tenant kubeconfig + create bootstrap token
	// ================================================================
	t.Log("=== Phase 7: Tenant kubeconfig + worker config ===")
	tenantKubeconfig, err := getSecretData(ctx, mgmtClient, "default",
		tenantName+"-admin-kubeconfig", "admin.conf")
	if err != nil {
		t.Fatalf("tenant kubeconfig: %v", err)
	}

	tenantConfig, _ := clientcmd.RESTConfigFromKubeConfig(tenantKubeconfig)
	tenantClient, _ := kubernetes.NewForConfig(tenantConfig)

	bootstrapToken, err := createBootstrapToken(ctx, tenantClient)
	if err != nil {
		t.Fatalf("bootstrap token: %v", err)
	}
	t.Logf("Bootstrap token: %s", bootstrapToken)

	k8sCA := extractCAFromKubeconfig(tenantKubeconfig)
	if k8sCA == nil {
		t.Fatal("no K8s CA in tenant kubeconfig")
	}

	workerConfig, err := talosconfig.GenerateTenantWorkerConfig(talosconfig.TenantWorkerParams{
		Name:            clusterName + "-tenant-worker-1",
		Endpoint:        tenantAPIEndpoint,
		CACert:          k8sCA,
		BootstrapToken:  bootstrapToken,
		KubeletImage:    fmt.Sprintf("ghcr.io/siderolabs/kubelet:v%s", kubernetesVersion),
		InstallDisk:     "/dev/sda",
		DNSNameservers:  []string{"1.1.1.1", "8.8.8.8"},
		MachineToken:    machineToken,
		TalosCACert:     talosCACert,
		ClusterID:       clusterID,
		ClusterSecret:   clusterSecret,
		TrustdEndpoints: []string{mgmtIP},
	})
	if err != nil {
		t.Fatalf("worker config: %v", err)
	}
	t.Logf("Worker config: %d bytes", len(workerConfig))

	// ================================================================
	// Phase 8: Boot tenant worker
	// ================================================================
	t.Log("=== Phase 8: Boot tenant worker ===")
	workerName := clusterName + "-tenant-worker-1"
	workerID, err := dp.CreateWorker(ctx, clusterName, platform.NodeSpec{
		Name: workerName, Role: "worker", Hostname: workerName,
	}, workerConfig)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	_ = workerID

	workerIP := dp.GetNodeIP(workerName)
	t.Logf("Worker IP: %s", workerIP)

	// ================================================================
	// Phase 9: Verify Kubernetes PKI (kubelet join)
	// ================================================================
	t.Log("=== Phase 9: Verify K8s PKI (kubelet join) ===")
	if err := waitForNodeReady(ctx, tenantClient, workerName, 5*time.Minute); err != nil {
		nodes, _ := tenantClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		for _, n := range nodes.Items {
			t.Logf("  Node: %s", n.Name)
		}
		t.Fatalf("worker node ready: %v", err)
	}
	t.Log("SUCCESS: Worker joined tenant cluster!")

	nodes, _ := tenantClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	for _, n := range nodes.Items {
		ready := "NotReady"
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = "Ready"
			}
		}
		t.Logf("  Node: %s (%s)", n.Name, ready)
	}

	// ================================================================
	// Phase 10: Verify Talos Machine PKI (talosctl)
	// ================================================================
	t.Log("=== Phase 10: Verify Talos Machine PKI (talosctl) ===")
	generator := dp.Generator()
	talosConfig, err := generator.GenerateTalosconfig([]string{workerIP})
	if err != nil {
		t.Fatalf("talosconfig: %v", err)
	}

	tcPath := filepath.Join(t.TempDir(), "talosconfig")
	_ = os.WriteFile(tcPath, talosConfig, 0o600)

	output, err := runTalosctl(ctx, tcPath, workerIP, "version")
	if err != nil {
		t.Fatalf("talosctl version: %v\n%s", err, output)
	}
	t.Logf("talosctl version:\n%s", output)
	t.Log("SUCCESS: talosctl access works via CSR signer!")

	// ================================================================
	// Cleanup
	// ================================================================
	t.Log("=== Cleanup ===")
	_ = dp.Destroy(ctx, &platform.ClusterSpec{Name: clusterName})
	t.Log("Cleanup complete")
}

// buildTCPSpec creates the Kamaji TenantControlPlane with CSR signer sidecar.
func buildTCPSpec(tenantName, mgmtIP, k8sVersion string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kamaji.clastix.io/v1alpha1",
			"kind":       "TenantControlPlane",
			"metadata": map[string]interface{}{
				"name":      tenantName,
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"dataStore": "kamaji-etcd",
				"controlPlane": map[string]interface{}{
					"deployment": map[string]interface{}{
						"replicas": int64(1),
						"additionalContainers": []interface{}{
							map[string]interface{}{
								"name":  "talos-csr-signer",
								"image": "ghcr.io/clastix/talos-csr-signer:latest",
								"ports": []interface{}{
									map[string]interface{}{
										"name": "grpc", "containerPort": int64(50001), "protocol": "TCP",
									},
								},
								"env": []interface{}{
									map[string]interface{}{
										"name": "TALOS_TOKEN",
										"valueFrom": map[string]interface{}{
											"secretKeyRef": map[string]interface{}{
												"name": tenantName + "-talos-ca", "key": "token",
											},
										},
									},
								},
								"volumeMounts": []interface{}{
									map[string]interface{}{"name": "talos-ca", "mountPath": "/etc/talos-ca", "readOnly": true},
									map[string]interface{}{"name": "tls-cert", "mountPath": "/etc/talos-server-crt", "readOnly": true},
								},
							},
						},
						"additionalVolumes": []interface{}{
							map[string]interface{}{"name": "talos-ca", "secret": map[string]interface{}{"secretName": tenantName + "-talos-ca"}},
							map[string]interface{}{"name": "tls-cert", "secret": map[string]interface{}{"secretName": tenantName + "-talos-tls-cert"}},
						},
					},
					"service": map[string]interface{}{
						"serviceType": "NodePort",
						"additionalPorts": []interface{}{
							map[string]interface{}{
								"name": "talos-csr-signer", "port": int64(50001),
								"targetPort": int64(50001), "protocol": "TCP", "nodePort": int64(30501),
							},
						},
					},
				},
				"networkProfile": map[string]interface{}{
					"address": mgmtIP, "port": int64(6443),
				},
				"kubernetes": map[string]interface{}{
					"version": k8sVersion,
					"kubelet": map[string]interface{}{"cgroupfs": "systemd"},
				},
				"addons": map[string]interface{}{
					"coreDNS": map[string]interface{}{}, "kubeProxy": map[string]interface{}{},
					"konnectivity": map[string]interface{}{},
				},
			},
		},
	}
}

// generateCSRSignerTLS creates a TLS certificate for the CSR signer gRPC server.
// It signs the cert with the Talos Machine CA so workers trust it.
func generateCSRSignerTLS(caCertPEM, caKeyPEM []byte, nodeIP string) (certPEM, keyPEM []byte, err error) {
	// Parse the CA cert.
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil {
		return nil, nil, fmt.Errorf("decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA cert: %w", err)
	}

	// Parse the CA key.
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return nil, nil, fmt.Errorf("decode CA key PEM")
	}
	// Try ED25519 first (Talos uses ED25519 for OS CA).
	var caKey interface{}
	switch caKeyBlock.Type {
	case "ED25519 PRIVATE KEY", "PRIVATE KEY":
		caKey, err = x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
	default:
		caKey, err = x509.ParseECPrivateKey(caKeyBlock.Bytes)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}

	// Generate a new key pair for the server cert.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	ip := net.ParseIP(nodeIP)
	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "talos-csr-signer"},
		NotBefore:    time.Now().Add(-10 * time.Second),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), ip},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, priv.Public(), caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// dumpTCPDebug dumps debugging info when TCP fails to become available.
func dumpTCPDebug(ctx context.Context, mgmtDynamic dynamic.Interface, mgmtClient kubernetes.Interface, tcpGVR schema.GroupVersionResource, tenantName string) {
	tcpObj, _ := mgmtDynamic.Resource(tcpGVR).Namespace("default").Get(ctx, tenantName, metav1.GetOptions{})
	if tcpObj != nil {
		status, _, _ := unstructured.NestedFieldCopy(tcpObj.Object, "status")
		fmt.Printf("TCP status: %v\n", status)
	}
	pods, _ := mgmtClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
		LabelSelector: "kamaji.clastix.io/name=" + tenantName,
	})
	for _, p := range pods.Items {
		fmt.Printf("Pod %s: %s\n", p.Name, p.Status.Phase)
		for _, cs := range p.Status.ContainerStatuses {
			fmt.Printf("  %s: ready=%v image=%s\n", cs.Name, cs.Ready, cs.Image)
		}
	}
}

// --- helpers ---

func ed25519PEMFix(pemData []byte) []byte {
	return bytes.ReplaceAll(pemData,
		[]byte("BEGIN ED25519 PRIVATE KEY"),
		[]byte("BEGIN PRIVATE KEY"),
	)
}

func waitForHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := strings.TrimPrefix(url, "https://")
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func waitForAPIServerReady(ctx context.Context, client kubernetes.Interface, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
			return err == nil, nil
		},
	)
}

func waitForTCPAvailable(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string, timeout time.Duration) error {
	gvr := schema.GroupVersionResource{
		Group: "kamaji.clastix.io", Version: "v1alpha1", Resource: "tenantcontrolplanes",
	}
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			obj, err := dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			for _, c := range conditions {
				cond, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cond["type"] == "Available" && cond["status"] == "True" {
					return true, nil
				}
			}
			return false, nil
		},
	)
}

func waitForNodeReady(ctx context.Context, client kubernetes.Interface, nodeName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		},
	)
}

func getSecretData(ctx context.Context, client kubernetes.Interface, namespace, name, key string) ([]byte, error) {
	secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	data, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}
	return data, nil
}

func createBootstrapToken(ctx context.Context, client kubernetes.Interface) (string, error) {
	tokenID := "rezus01"
	tokenSecret := "0123456789abcdef"
	_, err := client.CoreV1().Secrets("kube-system").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-token-" + tokenID},
		Type:       "bootstrap.kubernetes.io/token",
		Data: map[string][]byte{
			"token-id":                       []byte(tokenID),
			"token-secret":                   []byte(tokenSecret),
			"usage-bootstrap-authentication": []byte("true"),
			"usage-bootstrap-signing":        []byte("true"),
			"auth-extra-groups":              []byte("system:bootstrappers:worker"),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create bootstrap token: %w", err)
	}
	return tokenID + "." + tokenSecret, nil
}

func extractCAFromKubeconfig(kubeconfig []byte) []byte {
	config, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil
	}
	for _, cluster := range config.Clusters {
		if len(cluster.CertificateAuthorityData) > 0 {
			return cluster.CertificateAuthorityData
		}
	}
	return nil
}

func runTalosctl(ctx context.Context, talosconfigPath, node string, args ...string) (string, error) {
	allArgs := append([]string{
		"--talosconfig=" + talosconfigPath,
		"--nodes=" + node,
	}, args...)
	cmd := exec.CommandContext(ctx, "talosctl", allArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("talosctl %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

type mgmtTestWriter struct{ t *testing.T }

func (w *mgmtTestWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// Ensure siderx509 import is used (needed by Generator internals).
var _ siderx509.PEMEncodedCertificateAndKey
