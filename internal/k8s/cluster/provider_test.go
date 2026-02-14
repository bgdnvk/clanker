package cluster

import (
	"testing"
	"time"
)

func TestTimeoutConstants(t *testing.T) {
	// Verify timeout constants are defined with sensible values

	tests := []struct {
		name     string
		timeout  time.Duration
		minValue time.Duration
		maxValue time.Duration
	}{
		{
			name:     "DefaultClusterCreateTimeout",
			timeout:  DefaultClusterCreateTimeout,
			minValue: 10 * time.Minute,
			maxValue: 60 * time.Minute,
		},
		{
			name:     "DefaultNodeGroupCreateTimeout",
			timeout:  DefaultNodeGroupCreateTimeout,
			minValue: 5 * time.Minute,
			maxValue: 30 * time.Minute,
		},
		{
			name:     "DefaultNodeGroupDeleteTimeout",
			timeout:  DefaultNodeGroupDeleteTimeout,
			minValue: 5 * time.Minute,
			maxValue: 30 * time.Minute,
		},
		{
			name:     "DefaultSSHConnectTimeout",
			timeout:  DefaultSSHConnectTimeout,
			minValue: 1 * time.Minute,
			maxValue: 15 * time.Minute,
		},
		{
			name:     "DefaultNodeReadyTimeout",
			timeout:  DefaultNodeReadyTimeout,
			minValue: 1 * time.Minute,
			maxValue: 15 * time.Minute,
		},
		{
			name:     "DefaultPollInterval",
			timeout:  DefaultPollInterval,
			minValue: 5 * time.Second,
			maxValue: 60 * time.Second,
		},
		{
			name:     "DefaultCommandTimeout",
			timeout:  DefaultCommandTimeout,
			minValue: 30 * time.Second,
			maxValue: 10 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.timeout < tt.minValue {
				t.Errorf("%s = %v, want >= %v", tt.name, tt.timeout, tt.minValue)
			}
			if tt.timeout > tt.maxValue {
				t.Errorf("%s = %v, want <= %v", tt.name, tt.timeout, tt.maxValue)
			}
		})
	}
}

func TestTimeoutConstantsRelationships(t *testing.T) {
	// Verify logical relationships between timeouts

	// Cluster creation should take longer than node group creation
	if DefaultClusterCreateTimeout < DefaultNodeGroupCreateTimeout {
		t.Errorf("DefaultClusterCreateTimeout (%v) should be >= DefaultNodeGroupCreateTimeout (%v)",
			DefaultClusterCreateTimeout, DefaultNodeGroupCreateTimeout)
	}

	// Node group creation should take longer than deletion
	if DefaultNodeGroupCreateTimeout < DefaultNodeGroupDeleteTimeout {
		t.Errorf("DefaultNodeGroupCreateTimeout (%v) should be >= DefaultNodeGroupDeleteTimeout (%v)",
			DefaultNodeGroupCreateTimeout, DefaultNodeGroupDeleteTimeout)
	}

	// Poll interval should be much shorter than any operation timeout
	if DefaultPollInterval >= DefaultNodeGroupDeleteTimeout {
		t.Errorf("DefaultPollInterval (%v) should be < DefaultNodeGroupDeleteTimeout (%v)",
			DefaultPollInterval, DefaultNodeGroupDeleteTimeout)
	}
}

func TestClusterTypeConstants(t *testing.T) {
	// Verify cluster type constants
	tests := []struct {
		clusterType ClusterType
		expected    string
	}{
		{ClusterTypeEKS, "eks"},
		{ClusterTypeGKE, "gke"},
		{ClusterTypeKubeadm, "kubeadm"},
		{ClusterTypeKops, "kops"},
		{ClusterTypeK3s, "k3s"},
		{ClusterTypeExisting, "existing"},
	}

	for _, tt := range tests {
		t.Run(string(tt.clusterType), func(t *testing.T) {
			if string(tt.clusterType) != tt.expected {
				t.Errorf("ClusterType = %q, want %q", tt.clusterType, tt.expected)
			}
		})
	}
}

func TestManagerCreation(t *testing.T) {
	// Test manager creation and provider registration
	manager := NewManager(true)
	if manager == nil {
		t.Fatal("NewManager returned nil")
	}

	if len(manager.ListProviders()) != 0 {
		t.Error("new manager should have no providers")
	}
}

func TestErrProviderNotFound(t *testing.T) {
	err := &ErrProviderNotFound{ClusterType: ClusterTypeEKS}
	expected := "cluster provider not found: eks"
	if err.Error() != expected {
		t.Errorf("ErrProviderNotFound.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestErrClusterNotFound(t *testing.T) {
	err := &ErrClusterNotFound{ClusterName: "test-cluster"}
	expected := "cluster not found: test-cluster"
	if err.Error() != expected {
		t.Errorf("ErrClusterNotFound.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestErrClusterExists(t *testing.T) {
	err := &ErrClusterExists{ClusterName: "test-cluster"}
	expected := "cluster already exists: test-cluster"
	if err.Error() != expected {
		t.Errorf("ErrClusterExists.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestErrInvalidConfiguration(t *testing.T) {
	err := &ErrInvalidConfiguration{Message: "missing required field"}
	expected := "invalid cluster configuration: missing required field"
	if err.Error() != expected {
		t.Errorf("ErrInvalidConfiguration.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestCreateOptions(t *testing.T) {
	opts := CreateOptions{
		Name:              "test-cluster",
		Region:            "us-east-1",
		KubernetesVersion: "1.28",
		WorkerCount:       3,
		WorkerMinCount:    1,
		WorkerMaxCount:    5,
		Tags: map[string]string{
			"env": "test",
		},
	}

	if opts.Name != "test-cluster" {
		t.Errorf("expected name 'test-cluster', got %q", opts.Name)
	}

	if opts.WorkerCount != 3 {
		t.Errorf("expected worker count 3, got %d", opts.WorkerCount)
	}

	if opts.Tags["env"] != "test" {
		t.Errorf("expected tag 'env=test', got %q", opts.Tags["env"])
	}
}

func TestScaleOptions(t *testing.T) {
	opts := ScaleOptions{
		NodeGroupName: "workers",
		DesiredCount:  5,
		MinCount:      2,
		MaxCount:      10,
	}

	if opts.NodeGroupName != "workers" {
		t.Errorf("expected node group name 'workers', got %q", opts.NodeGroupName)
	}

	if opts.DesiredCount != 5 {
		t.Errorf("expected desired count 5, got %d", opts.DesiredCount)
	}
}

func TestNodeInfo(t *testing.T) {
	node := NodeInfo{
		Name:       "worker-1",
		Role:       "worker",
		Status:     "Ready",
		InternalIP: "10.0.0.1",
		ExternalIP: "1.2.3.4",
		InstanceID: "i-12345",
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "t3.medium",
		},
	}

	if node.Name != "worker-1" {
		t.Errorf("expected name 'worker-1', got %q", node.Name)
	}

	if node.Status != "Ready" {
		t.Errorf("expected status 'Ready', got %q", node.Status)
	}
}

func TestClusterInfo(t *testing.T) {
	info := ClusterInfo{
		Name:              "test-cluster",
		Type:              ClusterTypeEKS,
		Status:            "ACTIVE",
		KubernetesVersion: "1.28",
		Endpoint:          "https://example.eks.amazonaws.com",
		Region:            "us-east-1",
		VPCID:             "vpc-123",
	}

	if info.Name != "test-cluster" {
		t.Errorf("expected name 'test-cluster', got %q", info.Name)
	}

	if info.Type != ClusterTypeEKS {
		t.Errorf("expected type EKS, got %s", info.Type)
	}
}

func TestHealthStatus(t *testing.T) {
	status := HealthStatus{
		Healthy: true,
		Message: "cluster healthy",
		Components: map[string]string{
			"cluster":        "ACTIVE",
			"nodegroup-main": "ACTIVE",
		},
		NodeStatus: map[string]string{
			"worker-1": "Ready",
			"worker-2": "Ready",
		},
	}

	if !status.Healthy {
		t.Error("expected healthy to be true")
	}

	if status.Components["cluster"] != "ACTIVE" {
		t.Errorf("expected cluster component ACTIVE, got %q", status.Components["cluster"])
	}
}
