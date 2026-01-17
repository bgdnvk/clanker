package telemetry

import (
	"context"
	"time"
)

// MetricsScope indicates the scope level for metrics queries
type MetricsScope string

const (
	ScopeCluster   MetricsScope = "cluster"
	ScopeNode      MetricsScope = "node"
	ScopeNamespace MetricsScope = "namespace"
	ScopePod       MetricsScope = "pod"
	ScopeContainer MetricsScope = "container"
)

// MetricsSource indicates where metrics data came from
type MetricsSource string

const (
	SourceMetricsServer MetricsSource = "metrics-server"
	SourceResourceSpecs MetricsSource = "resource-specs"
)

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypeError  ResponseType = "error"
)

// K8sClient defines the interface for kubectl operations needed by telemetry
type K8sClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
	RunJSON(ctx context.Context, args ...string) ([]byte, error)
	GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error)
}

// QueryOptions contains options for telemetry queries
type QueryOptions struct {
	Namespace     string
	AllNamespaces bool
	PodName       string
	ContainerName string
	NodeName      string
	Scope         MetricsScope
	SortBy        string // cpu, memory
}

// Response from telemetry sub-agent
type Response struct {
	Type    ResponseType
	Data    interface{}
	Message string
	Error   error
}

// ResourceUsage represents CPU and memory values
type ResourceUsage struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// NodeMetrics represents metrics for a single node
type NodeMetrics struct {
	Name        string        `json:"name"`
	CPUUsage    string        `json:"cpuUsage"`      // e.g., "250m"
	CPUPercent  float64       `json:"cpuPercent"`    // percentage of allocatable
	MemUsage    string        `json:"memoryUsage"`   // e.g., "1.2Gi"
	MemPercent  float64       `json:"memoryPercent"` // percentage of allocatable
	Allocatable ResourceUsage `json:"allocatable,omitempty"`
	Capacity    ResourceUsage `json:"capacity,omitempty"`
}

// ContainerMetrics represents metrics for a single container
type ContainerMetrics struct {
	Name       string  `json:"name"`
	CPUUsage   string  `json:"cpuUsage"`
	MemUsage   string  `json:"memoryUsage"`
	CPURequest string  `json:"cpuRequest,omitempty"`
	CPULimit   string  `json:"cpuLimit,omitempty"`
	MemRequest string  `json:"memoryRequest,omitempty"`
	MemLimit   string  `json:"memoryLimit,omitempty"`
	CPUPercent float64 `json:"cpuPercent,omitempty"` // percentage of limit if set
	MemPercent float64 `json:"memoryPercent,omitempty"`
}

// PodMetrics represents metrics for a single pod
type PodMetrics struct {
	Name       string             `json:"name"`
	Namespace  string             `json:"namespace"`
	Containers []ContainerMetrics `json:"containers,omitempty"`
	CPUUsage   string             `json:"cpuUsage"`
	MemUsage   string             `json:"memoryUsage"`
	CPURequest string             `json:"cpuRequest,omitempty"`
	CPULimit   string             `json:"cpuLimit,omitempty"`
	MemRequest string             `json:"memoryRequest,omitempty"`
	MemLimit   string             `json:"memoryLimit,omitempty"`
}

// ClusterMetrics represents aggregated cluster-wide metrics
type ClusterMetrics struct {
	Timestamp     time.Time     `json:"timestamp"`
	Source        MetricsSource `json:"source"`
	Nodes         []NodeMetrics `json:"nodes"`
	TotalCPU      string        `json:"totalCPU"`
	TotalMemory   string        `json:"totalMemory"`
	UsedCPU       string        `json:"usedCPU"`
	UsedMemory    string        `json:"usedMemory"`
	CPUPercent    float64       `json:"cpuPercent"`
	MemoryPercent float64       `json:"memoryPercent"`
	NodeCount     int           `json:"nodeCount"`
	ReadyNodes    int           `json:"readyNodes"`
}

// NamespaceMetrics represents metrics aggregated by namespace
type NamespaceMetrics struct {
	Namespace   string        `json:"namespace"`
	Source      MetricsSource `json:"source"`
	Pods        []PodMetrics  `json:"pods"`
	TotalCPU    string        `json:"totalCPU"`
	TotalMemory string        `json:"totalMemory"`
	PodCount    int           `json:"podCount"`
}

// MetricsResult contains metrics data with metadata
type MetricsResult struct {
	Source    MetricsSource `json:"source"`
	Available bool          `json:"available"`
	Timestamp time.Time     `json:"timestamp"`
	Error     string        `json:"error,omitempty"`
	Scope     MetricsScope  `json:"scope"`

	// One of these will be populated based on scope
	Cluster   *ClusterMetrics   `json:"cluster,omitempty"`
	Nodes     []NodeMetrics     `json:"nodes,omitempty"`
	Namespace *NamespaceMetrics `json:"namespace,omitempty"`
	Pods      []PodMetrics      `json:"pods,omitempty"`
	Pod       *PodMetrics       `json:"pod,omitempty"`
}

// TopNodeOutput represents raw output from kubectl top nodes
type TopNodeOutput struct {
	Name     string
	CPUCores string // e.g., "250m" or "1"
	CPUPct   string // e.g., "6%"
	MemBytes string // e.g., "2147Mi"
	MemPct   string // e.g., "27%"
}

// TopPodOutput represents raw output from kubectl top pods
type TopPodOutput struct {
	Namespace string
	Name      string
	CPUCores  string
	MemBytes  string
}

// TopContainerOutput represents raw output from kubectl top pods --containers
type TopContainerOutput struct {
	Namespace string
	Pod       string
	Container string
	CPUCores  string
	MemBytes  string
}
