package cluster

import (
	"context"
	"time"
)

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
