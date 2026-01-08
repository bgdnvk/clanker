package sre

import (
	"context"
	"time"
)

// K8sClient interface for kubectl operations to avoid import cycles
type K8sClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
	RunJSON(ctx context.Context, args ...string) ([]byte, error)
}

// ResourceType represents the type of K8s resource being analyzed
type ResourceType string

const (
	ResourcePod         ResourceType = "pod"
	ResourceDeployment  ResourceType = "deployment"
	ResourceStatefulSet ResourceType = "statefulset"
	ResourceDaemonSet   ResourceType = "daemonset"
	ResourceNode        ResourceType = "node"
	ResourceService     ResourceType = "service"
	ResourcePVC         ResourceType = "pvc"
	ResourceEvent       ResourceType = "event"
)

// IssueSeverity represents the severity level of an issue
type IssueSeverity string

const (
	SeverityCritical IssueSeverity = "critical"
	SeverityWarning  IssueSeverity = "warning"
	SeverityInfo     IssueSeverity = "info"
)

// IssueCategory represents the category of an issue
type IssueCategory string

const (
	CategoryCrash           IssueCategory = "crash"
	CategoryPending         IssueCategory = "pending"
	CategoryResourceLimit   IssueCategory = "resource_limit"
	CategoryImagePull       IssueCategory = "image_pull"
	CategoryProbe           IssueCategory = "probe"
	CategoryScheduling      IssueCategory = "scheduling"
	CategoryNetwork         IssueCategory = "network"
	CategoryStorage         IssueCategory = "storage"
	CategoryConfiguration   IssueCategory = "configuration"
	CategoryNodePressure    IssueCategory = "node_pressure"
	CategoryNodeUnreachable IssueCategory = "node_unreachable"
)

// Issue represents a detected problem in the cluster
type Issue struct {
	ID           string        `json:"id"`
	Severity     IssueSeverity `json:"severity"`
	Category     IssueCategory `json:"category"`
	ResourceType ResourceType  `json:"resource_type"`
	ResourceName string        `json:"resource_name"`
	Namespace    string        `json:"namespace,omitempty"`
	Message      string        `json:"message"`
	Details      string        `json:"details,omitempty"`
	Timestamp    time.Time     `json:"timestamp"`
	Suggestions  []string      `json:"suggestions,omitempty"`
}

// PodStatus represents the status of a pod
type PodStatus struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Phase           string            `json:"phase"`
	Ready           bool              `json:"ready"`
	RestartCount    int               `json:"restart_count"`
	ContainerStates []ContainerState  `json:"container_states,omitempty"`
	Conditions      []PodCondition    `json:"conditions,omitempty"`
	NodeName        string            `json:"node_name,omitempty"`
	StartTime       *time.Time        `json:"start_time,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

// ContainerState represents the state of a container
type ContainerState struct {
	Name         string     `json:"name"`
	Ready        bool       `json:"ready"`
	RestartCount int        `json:"restart_count"`
	State        string     `json:"state"`
	Reason       string     `json:"reason,omitempty"`
	Message      string     `json:"message,omitempty"`
	ExitCode     int        `json:"exit_code,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

// PodCondition represents a condition of a pod
type PodCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// DeploymentStatus represents the status of a deployment
type DeploymentStatus struct {
	Name                string                `json:"name"`
	Namespace           string                `json:"namespace"`
	Replicas            int                   `json:"replicas"`
	ReadyReplicas       int                   `json:"ready_replicas"`
	AvailableReplicas   int                   `json:"available_replicas"`
	UnavailableReplicas int                   `json:"unavailable_replicas"`
	UpdatedReplicas     int                   `json:"updated_replicas"`
	Conditions          []DeploymentCondition `json:"conditions,omitempty"`
}

// DeploymentCondition represents a condition of a deployment
type DeploymentCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// NodeStatus represents the status of a node
type NodeStatus struct {
	Name             string          `json:"name"`
	Ready            bool            `json:"ready"`
	Conditions       []NodeCondition `json:"conditions,omitempty"`
	Allocatable      ResourceList    `json:"allocatable,omitempty"`
	Capacity         ResourceList    `json:"capacity,omitempty"`
	MemoryPressure   bool            `json:"memory_pressure"`
	DiskPressure     bool            `json:"disk_pressure"`
	PIDPressure      bool            `json:"pid_pressure"`
	NetworkAvailable bool            `json:"network_available"`
}

// NodeCondition represents a condition of a node
type NodeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// ResourceList represents resource quantities
type ResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Pods   string `json:"pods,omitempty"`
}

// EventInfo represents a Kubernetes event
type EventInfo struct {
	Type           string    `json:"type"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	Count          int       `json:"count"`
	FirstTimestamp time.Time `json:"first_timestamp"`
	LastTimestamp  time.Time `json:"last_timestamp"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"involved_object"`
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Healthy     bool      `json:"healthy"`
	Score       int       `json:"score"` // 0-100
	CheckedAt   time.Time `json:"checked_at"`
	Summary     string    `json:"summary"`
	Issues      []Issue   `json:"issues,omitempty"`
	Suggestions []string  `json:"suggestions,omitempty"`
}

// ClusterHealthSummary represents overall cluster health
type ClusterHealthSummary struct {
	OverallHealth  string          `json:"overall_health"` // healthy, degraded, critical
	Score          int             `json:"score"`          // 0-100
	NodeHealth     ComponentHealth `json:"node_health"`
	WorkloadHealth ComponentHealth `json:"workload_health"`
	StorageHealth  ComponentHealth `json:"storage_health"`
	NetworkHealth  ComponentHealth `json:"network_health"`
	CriticalIssues int             `json:"critical_issues"`
	WarningIssues  int             `json:"warning_issues"`
	TotalPods      int             `json:"total_pods"`
	RunningPods    int             `json:"running_pods"`
	PendingPods    int             `json:"pending_pods"`
	FailedPods     int             `json:"failed_pods"`
}

// ComponentHealth represents health of a cluster component
type ComponentHealth struct {
	Status  string `json:"status"` // healthy, degraded, critical
	Score   int    `json:"score"`  // 0-100
	Details string `json:"details,omitempty"`
}

// DiagnosticReport represents a diagnostic analysis
type DiagnosticReport struct {
	GeneratedAt  time.Time         `json:"generated_at"`
	Scope        string            `json:"scope"` // cluster, namespace, resource
	ResourceType string            `json:"resource_type,omitempty"`
	ResourceName string            `json:"resource_name,omitempty"`
	Namespace    string            `json:"namespace,omitempty"`
	Summary      string            `json:"summary"`
	Issues       []Issue           `json:"issues"`
	Events       []EventInfo       `json:"events,omitempty"`
	Logs         []LogEntry        `json:"logs,omitempty"`
	Remediation  []RemediationStep `json:"remediation,omitempty"`
}

// LogEntry represents a log line with metadata
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Container string    `json:"container"`
	Message   string    `json:"message"`
	Level     string    `json:"level,omitempty"` // error, warn, info
	IsError   bool      `json:"is_error"`
}

// RemediationStep represents a suggested fix
type RemediationStep struct {
	Order       int      `json:"order"`
	Action      string   `json:"action"`
	Description string   `json:"description"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Risk        string   `json:"risk,omitempty"` // low, medium, high
	Automated   bool     `json:"automated"`
}

// QueryOptions contains options for SRE queries
type QueryOptions struct {
	Namespace     string
	AllNamespaces bool
	ResourceName  string
	Since         string // Duration like "1h", "30m"
	TailLines     int
}

// ResponseType indicates the type of response
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypeReport ResponseType = "report"
	ResponseTypePlan   ResponseType = "plan"
)

// Response represents a response from the SRE sub-agent
type Response struct {
	Type    ResponseType      `json:"type"`
	Message string            `json:"message"`
	Data    interface{}       `json:"data,omitempty"`
	Report  *DiagnosticReport `json:"report,omitempty"`
	Plan    *SREPlan          `json:"plan,omitempty"`
}

// SREPlan represents a remediation plan
type SREPlan struct {
	Version int               `json:"version"`
	Summary string            `json:"summary"`
	Steps   []RemediationStep `json:"steps"`
	Notes   []string          `json:"notes,omitempty"`
}

// QueryAnalysis represents the analysis of a query
type QueryAnalysis struct {
	ResourceType ResourceType
	Operation    string
	ResourceName string
	Namespace    string
	IsReadOnly   bool
}
