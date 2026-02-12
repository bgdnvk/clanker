package workloads

import (
	"context"
	"time"
)

// K8sClient defines the interface for kubectl operations
// This interface is satisfied by k8s.Client
type K8sClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
	GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error)
	Describe(ctx context.Context, resourceType, name, namespace string) (string, error)
	Scale(ctx context.Context, resourceType, name, namespace string, replicas int) (string, error)
	Rollout(ctx context.Context, action, resourceType, name, namespace string) (string, error)
	Delete(ctx context.Context, resourceType, name, namespace string) (string, error)
	Logs(ctx context.Context, podName, namespace string, opts LogOptionsInternal) (string, error)
}

// LogOptionsInternal is used to avoid import cycles with k8s.LogOptions
type LogOptionsInternal struct {
	Container string
	Follow    bool
	Previous  bool
	TailLines int
	Since     string
}

// WorkloadType identifies the type of Kubernetes workload
type WorkloadType string

const (
	WorkloadDeployment  WorkloadType = "deployment"
	WorkloadPod         WorkloadType = "pod"
	WorkloadStatefulSet WorkloadType = "statefulset"
	WorkloadDaemonSet   WorkloadType = "daemonset"
	WorkloadReplicaSet  WorkloadType = "replicaset"
	WorkloadJob         WorkloadType = "job"
	WorkloadCronJob     WorkloadType = "cronjob"
)

// ResponseType indicates the type of response from the sub-agent
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
)

// QueryOptions contains options for workload queries
type QueryOptions struct {
	Namespace     string
	LabelSelector string
	FieldSelector string
	AllNamespaces bool
}

// Response represents the response from the workloads sub-agent
type Response struct {
	Type    ResponseType
	Data    interface{}
	Plan    *WorkloadPlan
	Message string
}

// WorkloadPlan represents a plan for workload modifications
type WorkloadPlan struct {
	Version   int            `json:"version"`
	CreatedAt time.Time      `json:"createdAt"`
	Summary   string         `json:"summary"`
	Steps     []WorkloadStep `json:"steps"`
	Notes     []string       `json:"notes,omitempty"`
}

// WorkloadStep represents a single step in a workload plan
type WorkloadStep struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Reason      string            `json:"reason,omitempty"`
	Produces    map[string]string `json:"produces,omitempty"`
	WaitFor     *WaitCondition    `json:"waitFor,omitempty"`
}

// WaitCondition specifies a condition to wait for
type WaitCondition struct {
	Resource  string        `json:"resource"`
	Condition string        `json:"condition"`
	Timeout   time.Duration `json:"timeout"`
}

// WorkloadInfo contains common workload information
type WorkloadInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Type      WorkloadType      `json:"type"`
	Replicas  int               `json:"replicas"`
	Ready     int               `json:"ready"`
	Available int               `json:"available"`
	Status    string            `json:"status"`
	Age       string            `json:"age"`
	Images    []string          `json:"images"`
	Labels    map[string]string `json:"labels"`
	Selector  map[string]string `json:"selector"`
	CreatedAt time.Time         `json:"createdAt"`
}

// PodInfo contains pod-specific information
type PodInfo struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Status     string            `json:"status"`
	Phase      string            `json:"phase"`
	Ready      string            `json:"ready"`
	Restarts   int               `json:"restarts"`
	Age        string            `json:"age"`
	IP         string            `json:"ip"`
	Node       string            `json:"node"`
	Containers []ContainerInfo   `json:"containers"`
	Labels     map[string]string `json:"labels"`
	Owners     []OwnerRef        `json:"owners,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	StartedAt  *time.Time        `json:"startedAt,omitempty"`
}

// ContainerInfo contains container details
type ContainerInfo struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
}

// OwnerRef identifies the owner of a resource
type OwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// DeploymentInfo contains deployment-specific information
type DeploymentInfo struct {
	WorkloadInfo
	Strategy            string `json:"strategy"`
	MaxSurge            string `json:"maxSurge"`
	MaxUnavailable      string `json:"maxUnavailable"`
	UpdatedReplicas     int    `json:"updatedReplicas"`
	ReadyReplicas       int    `json:"readyReplicas"`
	AvailableReplicas   int    `json:"availableReplicas"`
	UnavailableReplicas int    `json:"unavailableReplicas"`
}

// StatefulSetInfo contains statefulset-specific information
type StatefulSetInfo struct {
	WorkloadInfo
	ServiceName     string `json:"serviceName"`
	PodManagement   string `json:"podManagement"`
	UpdateStrategy  string `json:"updateStrategy"`
	CurrentReplicas int    `json:"currentReplicas"`
	UpdatedReplicas int    `json:"updatedReplicas"`
}

// DaemonSetInfo contains daemonset-specific information
type DaemonSetInfo struct {
	WorkloadInfo
	DesiredNumber   int    `json:"desiredNumber"`
	CurrentNumber   int    `json:"currentNumber"`
	ReadyNumber     int    `json:"readyNumber"`
	UpdatedNumber   int    `json:"updatedNumber"`
	AvailableNumber int    `json:"availableNumber"`
	UpdateStrategy  string `json:"updateStrategy"`
}

// JobInfo contains job-specific information
type JobInfo struct {
	WorkloadInfo
	Completions    *int       `json:"completions,omitempty"`
	Parallelism    *int       `json:"parallelism,omitempty"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	Active         int        `json:"active"`
	StartTime      *time.Time `json:"startTime,omitempty"`
	CompletionTime *time.Time `json:"completionTime,omitempty"`
	Duration       string     `json:"duration,omitempty"`
}

// CronJobInfo contains cronjob-specific information
type CronJobInfo struct {
	WorkloadInfo
	Schedule           string     `json:"schedule"`
	Suspend            bool       `json:"suspend"`
	LastScheduleTime   *time.Time `json:"lastScheduleTime,omitempty"`
	LastSuccessfulTime *time.Time `json:"lastSuccessfulTime,omitempty"`
	ActiveJobs         int        `json:"activeJobs"`
}

// CreateDeploymentOptions contains options for creating a deployment
type CreateDeploymentOptions struct {
	Name      string
	Namespace string
	Image     string
	Replicas  int
	Port      int
	Labels    map[string]string
	Env       map[string]string
	Command   []string
	Args      []string
	Resources *ResourceRequirements
}

// ResourceRequirements specifies compute resources
type ResourceRequirements struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

// ScaleOptions contains options for scaling workloads
type ScaleOptions struct {
	Name      string
	Namespace string
	Replicas  int
	Type      WorkloadType
}

// RolloutOptions contains options for rollout operations
type RolloutOptions struct {
	Name      string
	Namespace string
	Type      WorkloadType
	Action    string // restart, undo, pause, resume
	Revision  int    // for undo to specific revision
}

// UpdateImageOptions contains options for updating container images
type UpdateImageOptions struct {
	Name      string
	Namespace string
	Type      WorkloadType
	Container string
	Image     string
}

// LogOptions contains options for retrieving pod logs
type LogOptions struct {
	Container  string
	Follow     bool
	Previous   bool
	TailLines  int
	Since      string
	SinceTime  *time.Time
	Timestamps bool
}

// ExecOptions contains options for executing commands in pods
type ExecOptions struct {
	Container string
	Command   []string
	Stdin     bool
	TTY       bool
}

// GKE node labels
const (
	// GKELabelNodePool identifies the GKE node pool
	GKELabelNodePool = "cloud.google.com/gke-nodepool"
	// GKELabelPreemptible marks preemptible VM nodes
	GKELabelPreemptible = "cloud.google.com/gke-preemptible"
	// GKELabelSpot marks Spot VM nodes
	GKELabelSpot = "cloud.google.com/gke-spot"
	// GKELabelMachineType identifies the GCE machine type
	GKELabelMachineType = "node.kubernetes.io/instance-type"
	// GKELabelAcceleratorType identifies GPU accelerator type
	GKELabelAcceleratorType = "cloud.google.com/gke-accelerator"
	// GKELabelAcceleratorCount identifies GPU accelerator count
	GKELabelAcceleratorCount = "cloud.google.com/gke-accelerator-count"
	// GKELabelBootDiskType identifies the boot disk type
	GKELabelBootDiskType = "cloud.google.com/gke-boot-disk"
	// GKELabelOS identifies the operating system
	GKELabelOS = "kubernetes.io/os"
	// GKELabelArch identifies the architecture
	GKELabelArch = "kubernetes.io/arch"
)

// GKE taint keys
const (
	// GKETaintPreemptible is the taint key for preemptible nodes
	GKETaintPreemptible = "cloud.google.com/gke-preemptible"
	// GKETaintSpot is the taint key for Spot VM nodes
	GKETaintSpot = "cloud.google.com/gke-spot"
	// GKETaintGPU is the taint key for GPU nodes
	GKETaintGPU = "nvidia.com/gpu"
)

// GKE Autopilot resource class
const (
	// GKEAutopilotResourceClassGeneral is the general-purpose resource class
	GKEAutopilotResourceClassGeneral = "general-purpose"
	// GKEAutopilotResourceClassBalanced is the balanced resource class
	GKEAutopilotResourceClassBalanced = "balanced"
	// GKEAutopilotResourceClassScaleOut is the scale-out resource class
	GKEAutopilotResourceClassScaleOut = "scale-out"
)

// EKS node labels for comparison
const (
	// EKSLabelNodeGroup identifies the EKS node group
	EKSLabelNodeGroup = "eks.amazonaws.com/nodegroup"
	// EKSLabelCapacityType identifies capacity type (ON_DEMAND, SPOT)
	EKSLabelCapacityType = "eks.amazonaws.com/capacityType"
	// EKSLabelInstanceType identifies the EC2 instance type
	EKSLabelInstanceType = "node.kubernetes.io/instance-type"
)

// EKS taint keys for comparison
const (
	// EKSTaintSpot is the taint key for Spot instances
	EKSTaintSpot = "eks.amazonaws.com/capacityType"
)
