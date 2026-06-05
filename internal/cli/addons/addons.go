// Package addons installs platform add-ons: cert-manager, external-dns, and Gateway API.
package addons

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/cli/provider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Addons manages platform add-on installation.
type Addons struct {
	installer provider.ChartInstaller
	dynClient dynamic.Interface
	out       io.Writer
}

// New creates a new Addons manager.
func New(restCfg *rest.Config, installer provider.ChartInstaller, out io.Writer) (*Addons, error) {
	if restCfg == nil {
		return nil, fmt.Errorf("rest config is required")
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	return &Addons{
		installer: installer,
		dynClient: dynClient,
		out:       out,
	}, nil
}

// InstallCertManager installs cert-manager with CRDs.
func (a *Addons) InstallCertManager(ctx context.Context) error {
	fprintf(a.out, "  Installing cert-manager...\n")
	return a.installer.Install(ctx, provider.ChartConfig{
		Name:       "cert-manager",
		Chart:      "cert-manager",
		Repository: "https://charts.jetstack.io",
		Version:    "1.18.2",
		Namespace:  "cert-manager",
		Values: map[string]interface{}{
			"crds": map[string]interface{}{
				"enabled": true,
			},
			"replicaCount": 1,
			"webhook": map[string]interface{}{
				"replicaCount": 1,
			},
			"cainjector": map[string]interface{}{
				"replicaCount": 1,
			},
			"startupapicheck": map[string]interface{}{
				"enabled": false,
			},
		},
	}, a.out)
}

// InstallExternalDNS installs external-dns.
func (a *Addons) InstallExternalDNS(ctx context.Context, domain, secretRef string) error {
	fprintf(a.out, "  Installing external-dns...\n")

	values := map[string]interface{}{
		"replicaCount": 1,
		"sources":      []string{"gateway-httproute"},
		"policy":       "sync",
		"txtOwnerId":   "rezuscloud",
	}

	if secretRef != "" {
		values["env"] = []interface{}{
			map[string]interface{}{
				"name": "CF_API_TOKEN",
				"valueFrom": map[string]interface{}{
					"secretKeyRef": map[string]interface{}{
						"name": secretRef,
						"key":  "api-token",
					},
				},
			},
		}
		// Use Cloudflare provider
		values["provider"] = map[string]interface{}{
			"name": "cloudflare",
		}
	}

	return a.installer.Install(ctx, provider.ChartConfig{
		Name:       "external-dns",
		Chart:      "external-dns",
		Repository: "https://kubernetes-sigs.github.io/external-dns",
		Version:    "1.21.1",
		Namespace:  "external-dns",
		Values:     values,
	}, a.out)
}

// WaitForCertManager waits for cert-manager deployments to be ready.
func (a *Addons) WaitForCertManager(ctx context.Context, timeout time.Duration) error {
	fprintf(a.out, "  Waiting for cert-manager...\n")

	gvr := schema.GroupVersionResource{
		Group: "apps", Version: "v1", Resource: "deployments",
	}
	client := a.dynClient.Resource(gvr).Namespace("cert-manager")

	deployments := []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"}
	return a.waitForDeployments(ctx, client, deployments, timeout)
}

// CreateClusterIssuer creates a Let's Encrypt ClusterIssuer.
func (a *Addons) CreateClusterIssuer(ctx context.Context, name, email, solverType, secretRef string) error {
	fprintf(a.out, "  Creating ClusterIssuer %s (%s)...\n", name, solverType)

	solver := map[string]interface{}{}
	switch solverType {
	case "dns01":
		solver = map[string]interface{}{
			"dns01": map[string]interface{}{
				"cloudflare": map[string]interface{}{
					"apiTokenSecretRef": map[string]interface{}{
						"name": secretRef,
						"key":  "api-token",
					},
				},
			},
		}
	case "http01":
		solver = map[string]interface{}{
			"http01": map[string]interface{}{
				"ingress": map[string]interface{}{},
			},
		}
	}

	issuer := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "ClusterIssuer",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"acme": map[string]interface{}{
					"server": "https://acme-v02.api.letsencrypt.org/directory",
					"email":  email,
					"privateKeySecretRef": map[string]interface{}{
						"name": fmt.Sprintf("%s-account-key", name),
					},
					"solvers": []interface{}{solver},
				},
			},
		},
	}

	gvr := schema.GroupVersionResource{
		Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers",
	}
	client := a.dynClient.Resource(gvr)

	existing, err := client.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		issuer.SetResourceVersion(existing.GetResourceVersion())
		_, err = client.Update(ctx, issuer, metav1.UpdateOptions{})
	} else {
		_, err = client.Create(ctx, issuer, metav1.CreateOptions{})
	}
	return err
}

func (a *Addons) waitForDeployments(ctx context.Context, client dynamic.ResourceInterface, deployments []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, dep := range deployments {
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			obj, err := client.Get(ctx, dep, metav1.GetOptions{})
			if err != nil {
				time.Sleep(2 * time.Second)
				continue
			}

			ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
			replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
			if replicas == 0 || ready >= replicas {
				fprintf(a.out, "    ✓ %s\n", dep)
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	return nil
}

func fprintf(w io.Writer, format string, args ...interface{}) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}
