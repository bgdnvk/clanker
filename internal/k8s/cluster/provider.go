package cluster

import (
	"context"
	"fmt"
	"time"
)

// Debug logging format convention:
// All debug output should follow the pattern: [provider] action: details
//
// Examples:
//   [eks] creating cluster: aws eks create-cluster --name test
//   [gke] cluster test-cluster status: RUNNING
//   [kubeadm] bootstrapping worker 1...
//   [ssh] connecting to user@host
//
// The provider tag should be lowercase and match the tool being used:
//   - [eks], [eksctl], [aws] - for EKS operations
//   - [gke], [gcloud] - for GKE operations
//   - [kubeadm] - for kubeadm operations
//   - [kubectl] - for kubectl operations
//   - [ssh] - for SSH operations

// DebugLog prints a debug message with consistent formatting
func DebugLog(debug bool, provider, format string, args ...interface{}) {
	if !debug {
		return
	}
	message := fmt.Sprintf(format, args...)
	fmt.Printf("[%s] %s\n", provider, message)
}

// ClusterType identifies the Kubernetes cluster provisioning method
type ClusterType string

const (
	ClusterTypeEKS      ClusterType = "eks"
	ClusterTypeGKE      ClusterType = "gke"
	ClusterTypeAKS      ClusterType = "aks"
	ClusterTypeKubeadm  ClusterType = "kubeadm"
	ClusterTypeKops     ClusterType = "kops"
	ClusterTypeK3s      ClusterType = "k3s"
	ClusterTypeExisting ClusterType = "existing"
)

// Standard timeout constants for cluster operations
const (
	// DefaultClusterCreateTimeout is the default timeout for cluster creation
	DefaultClusterCreateTimeout = 20 * time.Minute

	// DefaultNodeGroupCreateTimeout is the default timeout for node group creation
	DefaultNodeGroupCreateTimeout = 15 * time.Minute

	// DefaultNodeGroupDeleteTimeout is the default timeout for node group deletion
	DefaultNodeGroupDeleteTimeout = 10 * time.Minute

	// DefaultSSHConnectTimeout is the default timeout for SSH connection
	DefaultSSHConnectTimeout = 5 * time.Minute

	// DefaultNodeReadyTimeout is the default timeout for node to become ready
	DefaultNodeReadyTimeout = 5 * time.Minute

	// DefaultPollInterval is the standard polling interval for status checks
	DefaultPollInterval = 30 * time.Second

	// DefaultCommandTimeout is the default timeout for CLI command execution
	DefaultCommandTimeout = 2 * time.Minute
)

// NodeInfo contains information about a cluster node
type NodeInfo struct {
	Name       string            `json:"name"`
	Role       string            `json:"role"`
	Status     string            `json:"status"`
	InternalIP string            `json:"internal_ip"`
	ExternalIP string            `json:"external_ip,omitempty"`
	InstanceID string            `json:"instance_id,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// ClusterInfo contains cluster details
type ClusterInfo struct {
	Name              string      `json:"name"`
	Type              ClusterType `json:"type"`
	Status            string      `json:"status"`
	KubernetesVersion string      `json:"kubernetes_version"`
	Endpoint          string      `json:"endpoint"`
	ControlPlaneNodes []NodeInfo  `json:"control_plane_nodes,omitempty"`
	WorkerNodes       []NodeInfo  `json:"worker_nodes,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	Region            string      `json:"region,omitempty"`
	VPCID             string      `json:"vpc_id,omitempty"`
}

// HealthStatus represents cluster health
type HealthStatus struct {
	Healthy     bool              `json:"healthy"`
	Message     string            `json:"message"`
	Components  map[string]string `json:"components"`
	NodeStatus  map[string]string `json:"node_status"`
	LastChecked time.Time         `json:"last_checked"`
}

// Provider defines the interface for cluster provisioning
type Provider interface {
	// Type returns the cluster type
	Type() ClusterType

	// Create provisions a new cluster
	Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error)

	// Delete removes a cluster
	Delete(ctx context.Context, clusterName string) error

	// Scale adjusts cluster node count
	Scale(ctx context.Context, clusterName string, opts ScaleOptions) error

	// GetKubeconfig retrieves cluster credentials
	GetKubeconfig(ctx context.Context, clusterName string) (string, error)

	// Health checks cluster health
	Health(ctx context.Context, clusterName string) (*HealthStatus, error)

	// ListClusters returns all clusters managed by this provider
	ListClusters(ctx context.Context) ([]ClusterInfo, error)

	// GetCluster returns information about a specific cluster
	GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error)
}

// CreateOptions for cluster creation
type CreateOptions struct {
	Name              string
	Region            string
	KubernetesVersion string

	// Control plane configuration
	ControlPlaneCount int
	ControlPlaneType  string

	// Worker node configuration
	WorkerCount    int
	WorkerMinCount int
	WorkerMaxCount int
	WorkerType     string

	// Networking
	VPCCIDR     string
	PodCIDR     string
	ServiceCIDR string
	CNIPlugin   string

	// AWS specific (for EKS and EC2 based)
	AWSProfile       string
	VPCId            string
	SubnetIds        []string
	SecurityGroupIds []string
	KeyPairName      string

	// GCP specific (for GKE)
	GCPProject    string
	GCPNetwork    string
	GCPSubnetwork string
	Preemptible   bool

	// Azure specific (for AKS)
	AzureSubscription  string
	AzureResourceGroup string
	AzureVNetName      string
	AzureSubnetName    string

	// Access configuration
	EnablePrivateEndpoint bool
	EnablePublicEndpoint  bool

	// Metadata
	Tags map[string]string

	// Timeouts
	CreateTimeout time.Duration
}

// ScaleOptions for scaling operations
type ScaleOptions struct {
	NodeGroupName string
	DesiredCount  int
	MinCount      int
	MaxCount      int
}

// Manager manages multiple cluster providers
type Manager struct {
	providers map[ClusterType]Provider
	debug     bool
}

// NewManager creates a new cluster manager
func NewManager(debug bool) *Manager {
	return &Manager{
		providers: make(map[ClusterType]Provider),
		debug:     debug,
	}
}

// RegisterProvider registers a cluster provider
func (m *Manager) RegisterProvider(provider Provider) {
	m.providers[provider.Type()] = provider
}

// GetProvider returns a provider by type
func (m *Manager) GetProvider(clusterType ClusterType) (Provider, bool) {
	provider, ok := m.providers[clusterType]
	return provider, ok
}

// ListProviders returns all registered provider types
func (m *Manager) ListProviders() []ClusterType {
	types := make([]ClusterType, 0, len(m.providers))
	for t := range m.providers {
		types = append(types, t)
	}
	return types
}

// CreateCluster creates a cluster using the appropriate provider
func (m *Manager) CreateCluster(ctx context.Context, clusterType ClusterType, opts CreateOptions) (*ClusterInfo, error) {
	provider, ok := m.GetProvider(clusterType)
	if !ok {
		return nil, &ErrProviderNotFound{ClusterType: clusterType}
	}
	return provider.Create(ctx, opts)
}

// DeleteCluster deletes a cluster using the appropriate provider
func (m *Manager) DeleteCluster(ctx context.Context, clusterType ClusterType, clusterName string) error {
	provider, ok := m.GetProvider(clusterType)
	if !ok {
		return &ErrProviderNotFound{ClusterType: clusterType}
	}
	return provider.Delete(ctx, clusterName)
}

// ScaleCluster scales a cluster using the appropriate provider
func (m *Manager) ScaleCluster(ctx context.Context, clusterType ClusterType, clusterName string, opts ScaleOptions) error {
	provider, ok := m.GetProvider(clusterType)
	if !ok {
		return &ErrProviderNotFound{ClusterType: clusterType}
	}
	return provider.Scale(ctx, clusterName, opts)
}

// GetKubeconfig retrieves kubeconfig for a cluster
func (m *Manager) GetKubeconfig(ctx context.Context, clusterType ClusterType, clusterName string) (string, error) {
	provider, ok := m.GetProvider(clusterType)
	if !ok {
		return "", &ErrProviderNotFound{ClusterType: clusterType}
	}
	return provider.GetKubeconfig(ctx, clusterName)
}

// HealthCheck checks the health of a cluster
func (m *Manager) HealthCheck(ctx context.Context, clusterType ClusterType, clusterName string) (*HealthStatus, error) {
	provider, ok := m.GetProvider(clusterType)
	if !ok {
		return nil, &ErrProviderNotFound{ClusterType: clusterType}
	}
	return provider.Health(ctx, clusterName)
}

// ErrProviderNotFound is returned when a cluster provider is not registered
type ErrProviderNotFound struct {
	ClusterType ClusterType
}

func (e *ErrProviderNotFound) Error() string {
	return "cluster provider not found: " + string(e.ClusterType)
}

// ErrClusterNotFound is returned when a cluster does not exist
type ErrClusterNotFound struct {
	ClusterName string
}

func (e *ErrClusterNotFound) Error() string {
	return "cluster not found: " + e.ClusterName
}

// ErrClusterExists is returned when trying to create a cluster that already exists
type ErrClusterExists struct {
	ClusterName string
}

func (e *ErrClusterExists) Error() string {
	return "cluster already exists: " + e.ClusterName
}

// ErrInvalidConfiguration is returned when cluster configuration is invalid
type ErrInvalidConfiguration struct {
	Message string
}

func (e *ErrInvalidConfiguration) Error() string {
	return "invalid cluster configuration: " + e.Message
}

// Security Best Practices
//
// These guidelines should be followed when provisioning clusters and resources.

// SecurityWarnings contains common security warnings to include in plans
var SecurityWarnings = map[string]string{
	"public-endpoint":    "Warning: Enabling public endpoints exposes the cluster to the internet. Consider using private endpoints with VPN/bastion access for production workloads.",
	"public-service":     "Warning: Services of type LoadBalancer expose your application to the internet. Consider using internal load balancers or Ingress with authentication.",
	"no-network-policy":  "Note: No network policy specified. Consider adding network policies to restrict pod-to-pod traffic for defense in depth.",
	"secrets-plain":      "Warning: Secrets should be managed using a secrets management solution (e.g., HashiCorp Vault, AWS Secrets Manager, Azure Key Vault) rather than plain Kubernetes secrets.",
	"root-container":     "Warning: Running containers as root is a security risk. Consider using non-root users in your container images.",
	"privileged":         "Warning: Privileged containers have full access to the host. Only use when absolutely necessary.",
	"host-network":       "Warning: Using host network bypasses network policies. Consider using standard pod networking.",
	"no-resource-limits": "Note: No resource limits specified. Consider adding CPU/memory limits to prevent resource exhaustion.",
}

// SecurityRecommendations contains security recommendations for different scenarios
var SecurityRecommendations = map[string][]string{
	"new-cluster": {
		"Enable private endpoint access when possible",
		"Configure network policies to restrict pod communication",
		"Enable pod security standards (restricted or baseline)",
		"Set up audit logging for security monitoring",
		"Use managed node groups with automatic updates",
	},
	"new-deployment": {
		"Use non-root containers where possible",
		"Set resource requests and limits",
		"Configure readiness and liveness probes",
		"Use read-only root filesystem when possible",
		"Drop unnecessary Linux capabilities",
	},
	"new-service": {
		"Use ClusterIP for internal-only services",
		"Use internal load balancers for private traffic",
		"Consider Ingress with TLS termination for HTTP services",
		"Add network policies to control service access",
	},
	"secrets": {
		"Use external secrets management (Vault, AWS Secrets Manager, etc.)",
		"Enable encryption at rest for etcd",
		"Rotate secrets regularly",
		"Limit secret access using RBAC",
	},
}

// GetSecurityWarning returns a security warning for the given key
func GetSecurityWarning(key string) string {
	if warning, ok := SecurityWarnings[key]; ok {
		return warning
	}
	return ""
}

// GetSecurityRecommendations returns security recommendations for the given scenario
func GetSecurityRecommendations(scenario string) []string {
	if recs, ok := SecurityRecommendations[scenario]; ok {
		return recs
	}
	return nil
}

// IsPublicEndpoint checks if cluster configuration has public endpoint enabled
func IsPublicEndpoint(opts CreateOptions) bool {
	return opts.EnablePublicEndpoint && !opts.EnablePrivateEndpoint
}

// ShouldWarnPublicAccess checks if a public access warning should be shown
func ShouldWarnPublicAccess(opts CreateOptions) bool {
	// Warn if only public endpoint is enabled without private
	return opts.EnablePublicEndpoint && !opts.EnablePrivateEndpoint
}
