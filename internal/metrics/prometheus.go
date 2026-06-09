package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PrometheusClient queries a Prometheus instance via HTTP.
type PrometheusClient struct {
	BaseURL    string       // e.g. "http://kube-prometheus-stack-prometheus.monitoring:9090"
	HTTPClient *http.Client // defaults to 10s timeout if nil
}

// promResponse is the standard Prometheus query response envelope.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// promVectorResult is a single instant vector result.
type promVectorResult struct {
	Metric map[string]string `json:"metric"`
	Value  [2]interface{}    `json:"value"` // [timestamp, valueString]
}

// promMatrixResult is a single range vector result.
// Reserved for future time-series queries.
// type promMatrixResult struct {
// 	Metric map[string]string `json:"metric"`
// 	Values [][2]interface{}  `json:"values"`
// }

func (c *PrometheusClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// queryInstant runs an instant PromQL query and returns the vector results.
func (c *PrometheusClient) queryInstant(ctx context.Context, query string) ([]promVectorResult, error) {
	u, _ := url.Parse(c.BaseURL)
	u.Path = "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("prometheus request: %w", err)
	}

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus query: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("prometheus %d: %s", resp.StatusCode, string(body))
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("prometheus decode: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus status=%s", pr.Status)
	}

	var results []promVectorResult
	if err := json.Unmarshal(pr.Data.Result, &results); err != nil {
		return nil, fmt.Errorf("prometheus unmarshal result: %w", err)
	}
	return results, nil
}

// floatValue extracts the float64 value from a Prometheus result pair.
func floatValue(v [2]interface{}) float64 {
	s, ok := v[1].(string)
	if !ok {
		return 0
	}
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}

// NodeUsage returns actual CPU and memory usage per node.
// CPU usage is in millicores, memory usage is in bytes.
func (c *PrometheusClient) NodeUsage(ctx context.Context) (map[string]ResourceQuantity, error) {
	// CPU usage: sum by (instance) of rate(node_cpu_seconds_total[5m]) * 1000 (millicores)
	// Note: node_cpu_seconds_total is per-CPU-core, so rate gives per-core utilization.
	// We want total usage, so we sum all modes except idle.
	cpuResults, err := c.queryInstant(ctx,
		`sum by (instance) (rate(node_cpu_seconds_total{mode!="idle"}[5m])) * 1000`)
	if err != nil {
		return nil, fmt.Errorf("cpu usage query: %w", err)
	}

	usage := make(map[string]ResourceQuantity)
	for _, r := range cpuResults {
		node := resolveNodeName(r.Metric)
		if node == "" {
			continue
		}
		usage[node] = ResourceQuantity{
			CPU: int64(floatValue(r.Value)),
		}
	}

	// Memory usage: total - available
	memResults, err := c.queryInstant(ctx,
		`node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes`)
	if err != nil {
		return nil, fmt.Errorf("memory usage query: %w", err)
	}
	for _, r := range memResults {
		node := resolveNodeName(r.Metric)
		if node == "" {
			continue
		}
		if existing, ok := usage[node]; ok {
			existing.Memory = int64(floatValue(r.Value))
			usage[node] = existing
		} else {
			usage[node] = ResourceQuantity{Memory: int64(floatValue(r.Value))}
		}
	}

	return usage, nil
}

// DiskUsage returns root filesystem usage per node in bytes (used, total).
func (c *PrometheusClient) DiskUsage(ctx context.Context) (map[string][2]int64, error) {
	// Used bytes
	usedResults, err := c.queryInstant(ctx,
		`node_filesystem_size_bytes{mountpoint="/",fstype!="tmpfs"} - node_filesystem_avail_bytes{mountpoint="/",fstype!="tmpfs"}`)
	if err != nil {
		return nil, fmt.Errorf("disk used query: %w", err)
	}

	totalResults, err := c.queryInstant(ctx,
		`node_filesystem_size_bytes{mountpoint="/",fstype!="tmpfs"}`)
	if err != nil {
		return nil, fmt.Errorf("disk total query: %w", err)
	}

	disk := make(map[string][2]int64)
	for _, r := range usedResults {
		node := resolveNodeName(r.Metric)
		if node == "" {
			continue
		}
		disk[node] = [2]int64{int64(floatValue(r.Value)), 0}
	}
	for _, r := range totalResults {
		node := resolveNodeName(r.Metric)
		if node == "" {
			continue
		}
		if existing, ok := disk[node]; ok {
			disk[node] = [2]int64{existing[0], int64(floatValue(r.Value))}
		}
	}
	return disk, nil
}

// NodeConditions returns the current condition status per node.
func (c *PrometheusClient) NodeConditions(ctx context.Context) (map[string]Conditions, error) {
	conditions := make(map[string]Conditions)

	conditionQueries := map[string]string{
		"Ready":          `kube_node_status_condition{condition="Ready",status="true"}`,
		"MemoryPressure": `kube_node_status_condition{condition="MemoryPressure",status="true"}`,
		"DiskPressure":   `kube_node_status_condition{condition="DiskPressure",status="true"}`,
		"PIDPressure":    `kube_node_status_condition{condition="PIDPressure",status="true"}`,
	}

	for condName, query := range conditionQueries {
		results, err := c.queryInstant(ctx, query)
		if err != nil {
			continue // graceful degradation
		}
		for _, r := range results {
			node := r.Metric["node"]
			if node == "" {
				continue
			}
			c := conditions[node] // zero-value Conditions
			val := floatValue(r.Value)
			status := ConditionFalse
			if val == 1 {
				status = ConditionTrue
			}
			switch condName {
			case "Ready":
				c.Ready = status
			case "MemoryPressure":
				c.MemoryPressure = status
			case "DiskPressure":
				c.DiskPressure = status
			case "PIDPressure":
				c.PIDPressure = status
			}
			conditions[node] = c
		}
	}

	return conditions, nil
}

// TopPodsByCPU returns the top N pods by CPU usage (millicores).
func (c *PrometheusClient) TopPodsByCPU(ctx context.Context, n int) ([]PodResourceMetrics, error) {
	results, err := c.queryInstant(ctx,
		fmt.Sprintf(`topk(%d, sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m])) * 1000)`, n))
	if err != nil {
		return nil, err
	}
	return podMetricsFromVector(results, "cpu"), nil
}

// TopPodsByMemory returns the top N pods by memory usage (bytes).
func (c *PrometheusClient) TopPodsByMemory(ctx context.Context, n int) ([]PodResourceMetrics, error) {
	results, err := c.queryInstant(ctx,
		fmt.Sprintf(`topk(%d, sum by (namespace, pod) (container_memory_working_set_bytes{container!="",container!="POD"}))`, n))
	if err != nil {
		return nil, err
	}
	return podMetricsFromVector(results, "memory"), nil
}

// PodMetricsByNode returns all pod CPU+memory usage grouped by node.
func (c *PrometheusClient) PodMetricsByNode(ctx context.Context, nodeName string) ([]PodResourceMetrics, error) {
	cpuResults, err := c.queryInstant(ctx,
		fmt.Sprintf(`sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{container!="",container!="POD",node=%q}[5m])) * 1000`, nodeName))
	if err != nil {
		return nil, err
	}

	memResults, err := c.queryInstant(ctx,
		fmt.Sprintf(`sum by (namespace, pod) (container_memory_working_set_bytes{container!="",container!="POD",node=%q})`, nodeName))
	if err != nil {
		return nil, err
	}

	pods := make(map[string]*PodResourceMetrics)
	for _, r := range cpuResults {
		key := r.Metric["namespace"] + "/" + r.Metric["pod"]
		pods[key] = &PodResourceMetrics{
			Name:      r.Metric["pod"],
			Namespace: r.Metric["namespace"],
			Node:      nodeName,
			CPU:       PodCPU{Usage: int64(floatValue(r.Value))},
		}
	}
	for _, r := range memResults {
		key := r.Metric["namespace"] + "/" + r.Metric["pod"]
		if p, ok := pods[key]; ok {
			p.Memory.Usage = int64(floatValue(r.Value))
		} else {
			pods[key] = &PodResourceMetrics{
				Name:      r.Metric["pod"],
				Namespace: r.Metric["namespace"],
				Node:      nodeName,
				Memory:    PodMemory{Usage: int64(floatValue(r.Value))},
			}
		}
	}

	var result []PodResourceMetrics
	for _, p := range pods {
		result = append(result, *p)
	}
	return result, nil
}

func podMetricsFromVector(results []promVectorResult, resource string) []PodResourceMetrics {
	var out []PodResourceMetrics
	for _, r := range results {
		p := PodResourceMetrics{
			Name:      r.Metric["pod"],
			Namespace: r.Metric["namespace"],
		}
		v := int64(floatValue(r.Value))
		switch resource {
		case "cpu":
			p.CPU.Usage = v
		case "memory":
			p.Memory.Usage = v
		}
		out = append(out, p)
	}
	return out
}

// resolveNodeName extracts the Kubernetes node name from Prometheus metric labels.
// node-exporter uses "instance", kube-state-metrics uses "node".
func resolveNodeName(metric map[string]string) string {
	if n := metric["node"]; n != "" {
		return n
	}
	// node-exporter instance is usually "host:9100"
	if inst := metric["instance"]; inst != "" {
		return strings.SplitN(inst, ":", 2)[0]
	}
	return ""
}
