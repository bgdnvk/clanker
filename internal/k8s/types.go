package k8s

import (
	"context"
	"time"

	"github.com/bgdnvk/clanker/internal/k8s/cluster"
)

// ClusterType is an alias for cluster.ClusterType
type ClusterType = cluster.ClusterType

// Cluster type constants
const (
	ClusterTypeEKS      = cluster.ClusterTypeEKS
	ClusterTypeKubeadm  = cluster.ClusterTypeKubeadm
	ClusterTypeKops     = cluster.ClusterTypeKops
	ClusterTypeK3s      = cluster.ClusterTypeK3s
	ClusterTypeExisting = cluster.ClusterTypeExisting
)

// ResponseType indicates the type of response from the K8s agent
type ResponseType string

const (
	ResponseTypePlan   ResponseType = "plan"
	ResponseTypeResult ResponseType = "result"
	ResponseTypeError  ResponseType = "error"
)

// QueryOptions contains options for handling K8s queries
type QueryOptions struct {
	ClusterName string
	ClusterType ClusterType
	Namespace   string
	AWSProfile  string
	Region      string
	MakerMode   bool
	Kubeconfig  string
}

// ApplyOptions contains options for applying K8s plans
type ApplyOptions struct {
	Debug   bool
	DryRun  bool
	Force   bool
	Wait    bool
	Timeout time.Duration
}

// K8sResponse represents the response from the K8s agent
type K8sResponse struct {
	Type          ResponseType
	Plan          *K8sPlan
	Result        string
	NeedsApproval bool
	Summary       string
	Error         error
}

// K8sPlan represents an execution plan for K8s operations
type K8sPlan struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Question  string    `json:"question"`
	Summary   string    `json:"summary"`

	// Cluster information
	ClusterName string      `json:"cluster_name,omitempty"`
	ClusterType ClusterType `json:"cluster_type"`

	// Infrastructure provisioning (EC2, VPC, etc.) for new clusters
	Infrastructure []InfraCommand `json:"infrastructure,omitempty"`

	// Cluster bootstrap commands (kubeadm init, eksctl create, etc.)
	Bootstrap []BootstrapCommand `json:"bootstrap,omitempty"`

	// Kubernetes operations (kubectl commands)
	KubectlCmds []KubectlCmd `json:"kubectl_cmds,omitempty"`

	// Helm operations
	HelmCmds []HelmCmd `json:"helm_cmds,omitempty"`

	// Raw manifests to apply
	Manifests []Manifest `json:"manifests,omitempty"`

	// Post install tasks
	PostInstall []PostInstallTask `json:"post_install,omitempty"`

	// Validation checks
	Validations []Validation `json:"validations,omitempty"`

	// Plan metadata
	Notes    []string          `json:"notes,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Bindings map[string]string `json:"bindings,omitempty"`
}

// InfraCommand represents AWS CLI commands for infrastructure
type InfraCommand struct {
	Service   string            `json:"service"`
	Operation string            `json:"operation"`
	Args      []string          `json:"args"`
	Reason    string            `json:"reason"`
	Produces  map[string]string `json:"produces,omitempty"`
	DependsOn []string          `json:"depends_on,omitempty"`
}

// BootstrapCommand represents cluster bootstrap operations
type BootstrapCommand struct {
	Type      string            `json:"type"`
	Operation string            `json:"operation"`
	Target    string            `json:"target"`
	Command   string            `json:"command"`
	SSHTarget string            `json:"ssh_target,omitempty"`
	Reason    string            `json:"reason"`
	Produces  map[string]string `json:"produces,omitempty"`
}

// KubectlCmd represents a kubectl command
type KubectlCmd struct {
	Args      []string          `json:"args"`
	Namespace string            `json:"namespace,omitempty"`
	Reason    string            `json:"reason"`
	Produces  map[string]string `json:"produces,omitempty"`
	WaitFor   *WaitCondition    `json:"wait_for,omitempty"`
}

// WaitCondition represents a condition to wait for
type WaitCondition struct {
	Resource  string        `json:"resource"`
	Condition string        `json:"condition"`
	Timeout   time.Duration `json:"timeout"`
}

// HelmCmd represents a Helm operation
type HelmCmd struct {
	Action     string         `json:"action"`
	Release    string         `json:"release"`
	Chart      string         `json:"chart"`
	Namespace  string         `json:"namespace"`
	Version    string         `json:"version,omitempty"`
	Values     map[string]any `json:"values,omitempty"`
	ValuesFile string         `json:"values_file,omitempty"`
	SetValues  []string       `json:"set_values,omitempty"`
	Wait       bool           `json:"wait"`
	Timeout    string         `json:"timeout,omitempty"`
	Reason     string         `json:"reason"`
}

// Manifest represents a K8s manifest to apply
type Manifest struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"api_version"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	Content    string `json:"content"`
	Reason     string `json:"reason"`
}

// PostInstallTask represents a post installation task
type PostInstallTask struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Optional    bool   `json:"optional"`
}

// Validation represents a validation check
type Validation struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	Expected   string `json:"expected"`
	FailAction string `json:"fail_action"`
}

// NodeInfo is an alias for cluster.NodeInfo
type NodeInfo = cluster.NodeInfo

// ClusterInfo is an alias for cluster.ClusterInfo
type ClusterInfo = cluster.ClusterInfo

// HealthStatus is an alias for cluster.HealthStatus
type HealthStatus = cluster.HealthStatus

// QueryAnalysis represents the analysis of a K8s query
type QueryAnalysis struct {
	IsReadOnly    bool
	Category      string
	Resources     []string
	Operations    []string
	ClusterScope  bool
	NamespaceHint string
}

// AIDecisionFunc is a function type for making AI decisions
type AIDecisionFunc func(ctx context.Context, prompt string) (string, error)

// ClusterResources contains all K8s resources for a cluster for visualization
type ClusterResources struct {
	ClusterName string                 `json:"clusterName"`
	ClusterARN  string                 `json:"clusterArn,omitempty"`
	Nodes       []ClusterNodeInfo      `json:"nodes"`
	Pods        []ClusterPodInfo       `json:"pods"`
	Services    []ClusterServiceInfo   `json:"services"`
	PVs         []ClusterPVInfo        `json:"persistentVolumes"`
	PVCs        []ClusterPVCInfo       `json:"persistentVolumeClaims"`
	ConfigMaps  []ClusterConfigMapInfo `json:"configMaps"`
	Ingresses   []ClusterIngressInfo   `json:"ingresses,omitempty"`
}

// ClusterNodeInfo contains node information for visualization
type ClusterNodeInfo struct {
	Name       string            `json:"name"`
	Role       string            `json:"role"` // "control-plane" or "worker"
	Status     string            `json:"status"`
	InternalIP string            `json:"internalIP"`
	ExternalIP string            `json:"externalIP,omitempty"`
	InstanceID string            `json:"instanceId,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// ClusterPodInfo contains pod information for visualization
type ClusterPodInfo struct {
	Name       string                 `json:"name"`
	Namespace  string                 `json:"namespace"`
	Status     string                 `json:"status"`
	Phase      string                 `json:"phase"`
	Ready      string                 `json:"ready"`
	Restarts   int                    `json:"restarts"`
	IP         string                 `json:"ip"`
	Node       string                 `json:"node"`
	Labels     map[string]string      `json:"labels"`
	Containers []ClusterContainerInfo `json:"containers"`
	Volumes    []ClusterPodVolumeInfo `json:"volumes,omitempty"`
}

// ClusterContainerInfo contains container information
type ClusterContainerInfo struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount"`
	State        string `json:"state"`
}

// ClusterPodVolumeInfo contains pod volume mount information
type ClusterPodVolumeInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "configMap", "secret", "pvc", "emptyDir", etc.
	Source    string `json:"source,omitempty"`
	MountPath string `json:"mountPath,omitempty"`
}

// ClusterServiceInfo contains service information for visualization
type ClusterServiceInfo struct {
	Name                string                   `json:"name"`
	Namespace           string                   `json:"namespace"`
	Type                string                   `json:"type"`
	ClusterIP           string                   `json:"clusterIP"`
	ExternalIP          string                   `json:"externalIP,omitempty"`
	LoadBalancerIngress []string                 `json:"loadBalancerIngress,omitempty"`
	Ports               []ClusterServicePortInfo `json:"ports"`
	Selector            map[string]string        `json:"selector"`
	Labels              map[string]string        `json:"labels"`
}

// ClusterServicePortInfo contains service port information
type ClusterServicePortInfo struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	TargetPort string `json:"targetPort"`
	NodePort   int    `json:"nodePort,omitempty"`
}

// ClusterPVInfo contains PersistentVolume information for visualization
type ClusterPVInfo struct {
	Name          string   `json:"name"`
	Capacity      string   `json:"capacity"`
	AccessModes   []string `json:"accessModes"`
	ReclaimPolicy string   `json:"reclaimPolicy"`
	Status        string   `json:"status"`
	Claim         string   `json:"claim,omitempty"`
	StorageClass  string   `json:"storageClass,omitempty"`
}

// ClusterPVCInfo contains PersistentVolumeClaim information for visualization
type ClusterPVCInfo struct {
	Name         string   `json:"name"`
	Namespace    string   `json:"namespace"`
	Status       string   `json:"status"`
	Volume       string   `json:"volume,omitempty"`
	Capacity     string   `json:"capacity,omitempty"`
	AccessModes  []string `json:"accessModes"`
	StorageClass string   `json:"storageClass,omitempty"`
}

// ClusterConfigMapInfo contains ConfigMap information for visualization
type ClusterConfigMapInfo struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	DataKeys  []string `json:"dataKeys"`
	DataCount int      `json:"dataCount"`
}

// ClusterIngressInfo contains Ingress information for visualization
type ClusterIngressInfo struct {
	Name             string                   `json:"name"`
	Namespace        string                   `json:"namespace"`
	IngressClassName string                   `json:"ingressClassName,omitempty"`
	Hosts            []string                 `json:"hosts"`
	Address          []string                 `json:"address,omitempty"`
	Rules            []ClusterIngressRuleInfo `json:"rules"`
}

// ClusterIngressRuleInfo contains ingress rule information
type ClusterIngressRuleInfo struct {
	Host        string `json:"host,omitempty"`
	Path        string `json:"path"`
	ServiceName string `json:"serviceName"`
	ServicePort string `json:"servicePort"`
}
