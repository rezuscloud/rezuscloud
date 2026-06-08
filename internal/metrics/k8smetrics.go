package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// K8sMetricsClient queries the Kubernetes API for resource capacity and pod
// requests/limits. These are "static" values derived from node status and pod
// specs (no metrics-server or Prometheus required).
type K8sMetricsClient struct {
	// BaseURL is the Kubernetes API server URL (e.g. "https://kubernetes.default.svc")
	BaseURL string
	// BearerToken for authenticating to the K8s API.
	BearerToken string
	// HTTPClient defaults to 10s timeout if nil.
	HTTPClient *http.Client
}

func (c *K8sMetricsClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *K8sMetricsClient) do(ctx context.Context, path string) (json.RawMessage, error) {
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.BearerToken)

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("k8s api %s: %d", path, resp.StatusCode)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// k8sNodeList is a minimal unmarshal target for /api/v1/nodes.
type k8sNodeList struct {
	Items []k8sNode `json:"items"`
}

type k8sNode struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Allocatable map[string]string `json:"allocatable"`
		Capacity    map[string]string `json:"capacity"`
	} `json:"status"`
}

// k8sPodList is a minimal unmarshal target for /api/v1/pods.
type k8sPodList struct {
	Items []k8sPod `json:"items"`
}

type k8sPod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string         `json:"nodeName"`
		Containers []k8sContainer `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name  string `json:"name"`
			Ready bool   `json:"ready"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type k8sContainer struct {
	Name      string       `json:"name"`
	Resources k8sResources `json:"resources"`
}

type k8sResources struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

// NodeCapacity returns per-node CPU and memory capacity/allocatable.
func (c *K8sMetricsClient) NodeCapacity(ctx context.Context) (map[string]NodeResourceMetrics, error) {
	raw, err := c.do(ctx, "/api/v1/nodes")
	if err != nil {
		return nil, err
	}
	var list k8sNodeList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}

	nodes := make(map[string]NodeResourceMetrics, len(list.Items))
	for _, n := range list.Items {
		role := "worker"
		if _, ok := n.Metadata.Labels["node-role.kubernetes.io/control-plane"]; ok {
			role = "control-plane"
		} else if _, ok := n.Metadata.Labels["node-role.kubernetes.io/control-plane"]; ok {
			role = "control-plane"
		}

		cpuCap := parseK8sQuantity(n.Status.Capacity["cpu"])
		memCap := parseK8sQuantity(n.Status.Capacity["memory"])
		cpuAlloc := parseK8sQuantity(n.Status.Allocatable["cpu"])
		memAlloc := parseK8sQuantity(n.Status.Allocatable["memory"])
		podCap := parseK8sQuantity(n.Status.Capacity["pods"])
		podAlloc := parseK8sQuantity(n.Status.Allocatable["pods"])

		nodes[n.Metadata.Name] = NodeResourceMetrics{
			Name:   n.Metadata.Name,
			Role:   role,
			Status: "healthy", // will be updated by conditions later
			CPU: CPU{
				Capacity:    ResourceQuantity{CPU: cpuCap},
				Allocatable: ResourceQuantity{CPU: cpuAlloc},
			},
			Memory: Memory{
				Capacity:    ResourceQuantity{Memory: memCap},
				Allocatable: ResourceQuantity{Memory: memAlloc},
			},
			Pods: Pods{
				Capacity:    int(podCap),
				Allocatable: int(podAlloc),
			},
		}
	}
	return nodes, nil
}

// PodRequests returns aggregated CPU and memory requests/limits per node.
func (c *K8sMetricsClient) PodRequests(ctx context.Context) (map[string]ResourceQuantity, map[string]ResourceQuantity, map[string]int, error) {
	raw, err := c.do(ctx, "/api/v1/pods")
	if err != nil {
		return nil, nil, nil, err
	}
	var list k8sPodList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, nil, nil, err
	}

	requests := make(map[string]ResourceQuantity)
	limits := make(map[string]ResourceQuantity)
	podCounts := make(map[string]int)

	for _, p := range list.Items {
		if p.Spec.NodeName == "" || p.Status.Phase != "Running" {
			continue
		}
		podCounts[p.Spec.NodeName]++
		for _, c := range p.Spec.Containers {
			reqCPU := parseK8sQuantity(c.Resources.Requests["cpu"])
			reqMem := parseK8sQuantity(c.Resources.Requests["memory"])
			limCPU := parseK8sQuantity(c.Resources.Limits["cpu"])
			limMem := parseK8sQuantity(c.Resources.Limits["memory"])

			r := requests[p.Spec.NodeName]
			r.CPU += reqCPU
			r.Memory += reqMem
			requests[p.Spec.NodeName] = r

			l := limits[p.Spec.NodeName]
			l.CPU += limCPU
			l.Memory += limMem
			limits[p.Spec.NodeName] = l
		}
	}
	return requests, limits, podCounts, nil
}

// parseK8sQuantity parses a Kubernetes resource quantity string.
// For CPU: returns millicores (e.g. "500m" → 500, "2" → 2000).
// For memory: returns bytes (e.g. "32Gi" → 34359738368).
// For pods: returns count as int64.
func parseK8sQuantity(s string) int64 {
	if s == "" {
		return 0
	}

	// Memory suffixes (binary)
	memSuffixes := map[string]int64{
		"Ki": 1024,
		"Mi": 1024 * 1024,
		"Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024,
		"K":  1000,
		"M":  1000 * 1000,
		"G":  1000 * 1000 * 1000,
		"T":  1000 * 1000 * 1000 * 1000,
	}

	for suffix, mult := range memSuffixes {
		if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
			var v float64
			_, _ = fmt.Sscanf(s[:len(s)-len(suffix)], "%f", &v)
			return int64(v * float64(mult))
		}
	}

	// CPU millicores
	if len(s) > 1 && s[len(s)-1] == 'm' {
		var v int64
		_, _ = fmt.Sscanf(s[:len(s)-1], "%d", &v)
		return v
	}

	// Plain number (cores, pods count, etc.)
	var v float64
	_, _ = fmt.Sscanf(s, "%f", &v)
	// If it looks like a CPU value (small number < 1000), treat as cores → millicores
	if v < 1000 && v > 0 {
		// Could be cores. But could also be pod count. Heuristic:
		// If the string contains a decimal point, it's CPU.
		if containsDot(s) {
			return int64(v * 1000)
		}
	}
	return int64(v)
}

func containsDot(s string) bool {
	for _, c := range s {
		if c == '.' {
			return true
		}
	}
	return false
}
