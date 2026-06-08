// Package metrics provides resource usage data from Kubernetes and Prometheus.
//
// The package abstracts two data sources:
//   - K8s API: static resource model (node capacity, allocatable, pod requests/limits)
//   - Prometheus (via kube-state-metrics + node-exporter): actual usage counters
//
// All consumers receive typed structs; no raw query strings leak out.
package metrics

// ResourceQuantity represents a compute resource amount in core Kubernetes units.
// CPU is in millicores (1000 = 1 core). Memory is in bytes.
type ResourceQuantity struct {
	CPU    int64 `json:"cpu"`    // millicores
	Memory int64 `json:"memory"` // bytes
}

// NodeResourceMetrics holds resource data for a single Kubernetes node.
type NodeResourceMetrics struct {
	Name       string `json:"name"`
	Role       string `json:"role,omitempty"` // "control-plane" or "worker"
	Status     string `json:"status"`         // "healthy", "warning", "critical"
	CPU        CPU    `json:"cpu"`
	Memory     Memory `json:"memory"`
	Pods       Pods   `json:"pods"`
	Disk       Disk   `json:"disk"`
	Conditions Conditions `json:"conditions"`
}

// CPU holds CPU resource data for a node.
type CPU struct {
	Capacity    ResourceQuantity `json:"capacity"`    // total CPU on the node
	Allocatable ResourceQuantity `json:"allocatable"` // CPU available for scheduling
	Requested   ResourceQuantity `json:"requested"`   // sum of pod CPU requests
	Limited     ResourceQuantity `json:"limited"`     // sum of pod CPU limits
	Usage       ResourceQuantity `json:"usage"`       // actual CPU usage (from node-exporter / metrics-server)
}

// Memory holds memory resource data for a node.
type Memory struct {
	Capacity    ResourceQuantity `json:"capacity"`
	Allocatable ResourceQuantity `json:"allocatable"`
	Requested   ResourceQuantity `json:"requested"`
	Limited     ResourceQuantity `json:"limited"`
	Usage       ResourceQuantity `json:"usage"`
}

// Pods holds pod count data for a node.
type Pods struct {
	Capacity    int `json:"capacity"`    // max pods (status.capacity.pods)
	Allocatable int `json:"allocatable"` // schedulable pod slots
	Running     int `json:"running"`     // currently running pods
}

// Disk holds filesystem usage for a node (root filesystem).
type Disk struct {
	TotalBytes int64 `json:"totalBytes"`
	UsedBytes  int64 `json:"usedBytes"`
}

// Conditions holds Kubernetes node condition status.
type Conditions struct {
	Ready         ConditionStatus `json:"ready"`
	MemoryPressure ConditionStatus `json:"memoryPressure"`
	DiskPressure  ConditionStatus `json:"diskPressure"`
	PIDPressure   ConditionStatus `json:"pidPressure"`
}

// ConditionStatus represents a node condition's current state.
type ConditionStatus string

const (
	ConditionTrue    ConditionStatus = "true"
	ConditionFalse   ConditionStatus = "false"
	ConditionUnknown ConditionStatus = "unknown"
)

// ClusterResourceSummary aggregates resource data across all nodes.
type ClusterResourceSummary struct {
	Nodes       int              `json:"nodes"`
	CPU         ClusterCPU       `json:"cpu"`
	Memory      ClusterMemory    `json:"memory"`
	Pods        ClusterPods      `json:"pods"`
	NodeDetails []NodeResourceMetrics `json:"nodeDetails"`
}

// ClusterCPU is the cluster-wide CPU summary.
type ClusterCPU struct {
	Capacity    int64 `json:"capacity"`    // total millicores across all nodes
	Allocatable int64 `json:"allocatable"`
	Requested   int64 `json:"requested"`
	Usage       int64 `json:"usage"`
}

// ClusterMemory is the cluster-wide memory summary.
type ClusterMemory struct {
	Capacity    int64 `json:"capacity"`    // total bytes across all nodes
	Allocatable int64 `json:"allocatable"`
	Requested   int64 `json:"requested"`
	Usage       int64 `json:"usage"`
}

// ClusterPods is the cluster-wide pod count summary.
type ClusterPods struct {
	Capacity    int `json:"capacity"`
	Allocatable int `json:"allocatable"`
	Running     int `json:"running"`
}

// PodResourceMetrics holds resource data for a single pod (or top-consumer row).
type PodResourceMetrics struct {
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Node      string          `json:"node"`
	CPU       PodCPU          `json:"cpu"`
	Memory    PodMemory       `json:"memory"`
	Ready     string          `json:"ready"`   // e.g. "1/1"
	Restarts  int             `json:"restarts"`
}

// PodCPU holds CPU data for a pod.
type PodCPU struct {
	Request int64 `json:"request"` // millicores, 0 = unset
	Limit   int64 `json:"limit"`   // millicores, 0 = unset
	Usage   int64 `json:"usage"`   // millicores
}

// PodMemory holds memory data for a pod.
type PodMemory struct {
	Request int64 `json:"request"` // bytes, 0 = unset
	Limit   int64 `json:"limit"`   // bytes, 0 = unset
	Usage   int64 `json:"usage"`   // bytes
}

// Percent returns usage as a percentage of capacity (0-100).
// Returns 0 if capacity is 0.
func Percent(usage, capacity int64) int {
	if capacity <= 0 {
		return 0
	}
	p := int(usage * 100 / capacity)
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
}

// PressureLevel returns "ok", "warning", or "critical" for a given percentage.
func PressureLevel(pct int) string {
	switch {
	case pct >= 90:
		return "critical"
	case pct >= 70:
		return "warning"
	default:
		return "ok"
	}
}
