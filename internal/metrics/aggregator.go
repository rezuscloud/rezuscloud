package metrics

import "context"

// Aggregator combines data from K8s API and Prometheus into typed metrics.
type Aggregator struct {
	Prom *PrometheusClient
	K8s  *K8sMetricsClient
}

// ClusterSummary returns the full resource picture for the cluster.
// Gracefully degrades if Prometheus is unavailable (usage will be zero).
func (a *Aggregator) ClusterSummary(ctx context.Context) (*ClusterResourceSummary, error) {
	// Static data from K8s API (always available)
	nodeCapacities, err := a.K8s.NodeCapacity(ctx)
	if err != nil {
		return nil, err
	}
	requests, limits, podCounts, err := a.K8s.PodRequests(ctx)
	if err != nil {
		return nil, err
	}

	// Dynamic data from Prometheus (may be unavailable)
	nodeUsage, _ := a.Prom.NodeUsage(ctx)
	diskUsage, _ := a.Prom.DiskUsage(ctx)
	nodeConditions, _ := a.Prom.NodeConditions(ctx)

	// Merge everything
	summary := &ClusterResourceSummary{
		Nodes: len(nodeCapacities),
	}

	for name, nc := range nodeCapacities {
		// Fill in requests/limits
		if r, ok := requests[name]; ok {
			nc.CPU.Requested = r
			nc.Memory.Requested = r
		}
		if l, ok := limits[name]; ok {
			nc.CPU.Limited = ResourceQuantity{CPU: l.CPU}
			nc.Memory.Limited = ResourceQuantity{Memory: l.Memory}
		}

		// Fill in usage from Prometheus
		if u, ok := nodeUsage[name]; ok {
			nc.CPU.Usage = u
			nc.Memory.Usage = ResourceQuantity{Memory: u.Memory}
		}

		// Fill in pod counts
		if count, ok := podCounts[name]; ok {
			nc.Pods.Running = count
		}

		// Fill in disk
		if d, ok := diskUsage[name]; ok {
			nc.Disk = Disk{UsedBytes: d[0], TotalBytes: d[1]}
		}

		// Fill in conditions
		if cond, ok := nodeConditions[name]; ok {
			nc.Conditions = cond
			if cond.Ready == ConditionTrue {
				nc.Status = "healthy"
			} else {
				nc.Status = "warning"
			}
		}

		summary.NodeDetails = append(summary.NodeDetails, nc)

		// Aggregate cluster totals
		summary.CPU.Capacity += nc.CPU.Capacity.CPU
		summary.CPU.Allocatable += nc.CPU.Allocatable.CPU
		summary.CPU.Requested += nc.CPU.Requested.CPU
		summary.CPU.Usage += nc.CPU.Usage.CPU

		summary.Memory.Capacity += nc.Memory.Capacity.Memory
		summary.Memory.Allocatable += nc.Memory.Allocatable.Memory
		summary.Memory.Requested += nc.Memory.Requested.Memory
		summary.Memory.Usage += nc.Memory.Usage.Memory

		summary.Pods.Capacity += nc.Pods.Capacity
		summary.Pods.Allocatable += nc.Pods.Allocatable
		summary.Pods.Running += nc.Pods.Running
	}

	return summary, nil
}

// NodeMetrics returns detailed metrics for a single node.
func (a *Aggregator) NodeMetrics(ctx context.Context, nodeName string) (*NodeResourceMetrics, error) {
	summary, err := a.ClusterSummary(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range summary.NodeDetails {
		if n.Name == nodeName {
			return &n, nil
		}
	}
	return nil, nil
}

// TopPods returns the top N pods by CPU and memory combined.
func (a *Aggregator) TopPods(ctx context.Context, n int) ([]PodResourceMetrics, error) {
	cpuPods, _ := a.Prom.TopPodsByCPU(ctx, n)
	memPods, _ := a.Prom.TopPodsByMemory(ctx, n)

	// Merge by namespace/pod key
	merged := make(map[string]*PodResourceMetrics)
	for _, p := range cpuPods {
		key := p.Namespace + "/" + p.Name
		cp := p
		merged[key] = &cp
	}
	for _, p := range memPods {
		key := p.Namespace + "/" + p.Name
		if existing, ok := merged[key]; ok {
			existing.Memory = p.Memory
		} else {
			cp := p
			merged[key] = &cp
		}
	}

	var result []PodResourceMetrics
	for _, p := range merged {
		result = append(result, *p)
	}
	// Return up to n
	if len(result) > n {
		result = result[:n]
	}
	return result, nil
}
