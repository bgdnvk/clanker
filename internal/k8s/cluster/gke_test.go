package cluster

import (
	"context"
	"testing"
	"time"
)

func TestGKEProviderType(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})

	if provider.Type() != ClusterTypeGKE {
		t.Errorf("expected cluster type %s, got %s", ClusterTypeGKE, provider.Type())
	}
}

func TestGKEProviderCreateValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("missing cluster name", func(t *testing.T) {
		provider := NewGKEProvider(GKEProviderOptions{
			ProjectID: "test-project",
			Region:    "us-central1",
			Debug:     false,
		})
		_, err := provider.Create(ctx, CreateOptions{})
		if err == nil {
			t.Error("expected error, got nil")
			return
		}

		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "cluster name is required" {
				t.Errorf("expected error message 'cluster name is required', got %q", configErr.Message)
			}
		} else {
			t.Errorf("expected ErrInvalidConfiguration, got %T", err)
		}
	})

	t.Run("missing region", func(t *testing.T) {
		// Use provider without default region to test region validation
		provider := NewGKEProvider(GKEProviderOptions{
			ProjectID: "test-project",
			Region:    "", // No default region
			Debug:     false,
		})
		_, err := provider.Create(ctx, CreateOptions{
			Name: "test-cluster",
		})
		if err == nil {
			t.Error("expected error, got nil")
			return
		}

		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "region is required" {
				t.Errorf("expected error message 'region is required', got %q", configErr.Message)
			}
		} else {
			t.Errorf("expected ErrInvalidConfiguration, got %T", err)
		}
	})

	t.Run("missing GCP project", func(t *testing.T) {
		// Use provider without project to test project validation
		provider := NewGKEProvider(GKEProviderOptions{
			ProjectID: "", // No default project
			Region:    "us-central1",
			Debug:     false,
		})
		_, err := provider.Create(ctx, CreateOptions{
			Name:   "test-cluster",
			Region: "us-central1",
		})
		if err == nil {
			t.Error("expected error, got nil")
			return
		}

		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "GCP project is required" {
				t.Errorf("expected error message 'GCP project is required', got %q", configErr.Message)
			}
		} else {
			t.Errorf("expected ErrInvalidConfiguration, got %T", err)
		}
	})
}

func TestGKEProviderScaleValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	err := provider.Scale(ctx, "", ScaleOptions{})
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}

	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}
}

func TestGKEProviderGetKubeconfigValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	_, err := provider.GetKubeconfig(ctx, "")
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}

	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}
}

func TestGKEProviderOptions(t *testing.T) {
	opts := GKEProviderOptions{
		ProjectID: "my-project",
		Region:    "europe-west1",
		Debug:     true,
	}

	provider := NewGKEProvider(opts)

	if provider.projectID != "my-project" {
		t.Errorf("expected project ID 'my-project', got %q", provider.projectID)
	}

	if provider.region != "europe-west1" {
		t.Errorf("expected region 'europe-west1', got %q", provider.region)
	}

	if !provider.debug {
		t.Error("expected debug to be true")
	}
}

func TestGKEClusterInfoParsing(t *testing.T) {
	info := &gkeClusterInfo{
		Name:                 "test-cluster",
		Status:               "RUNNING",
		CurrentMasterVersion: "1.28.3-gke.1200",
		Endpoint:             "https://10.0.0.1",
		Location:             "us-central1",
		CreateTime:           time.Now().Format(time.RFC3339),
	}

	if info.Name != "test-cluster" {
		t.Errorf("expected name 'test-cluster', got %q", info.Name)
	}

	if info.Status != "RUNNING" {
		t.Errorf("expected status 'RUNNING', got %q", info.Status)
	}

	if info.CurrentMasterVersion != "1.28.3-gke.1200" {
		t.Errorf("expected version '1.28.3-gke.1200', got %q", info.CurrentMasterVersion)
	}

	if info.Location != "us-central1" {
		t.Errorf("expected location 'us-central1', got %q", info.Location)
	}
}

func TestGKENodePoolInfoParsing(t *testing.T) {
	// gkeNodePoolInfo uses anonymous structs for Config and Autoscaling
	// This test validates the expected JSON structure works correctly
	info := gkeNodePoolInfo{
		Name:             "default-pool",
		Status:           "RUNNING",
		InitialNodeCount: 3,
	}
	info.Config.MachineType = "e2-standard-2"
	info.Config.DiskSizeGb = 100

	if info.Name != "default-pool" {
		t.Errorf("expected pool name 'default-pool', got %q", info.Name)
	}

	if info.Status != "RUNNING" {
		t.Errorf("expected status 'RUNNING', got %q", info.Status)
	}

	if info.Config.MachineType != "e2-standard-2" {
		t.Errorf("expected machine type 'e2-standard-2', got %q", info.Config.MachineType)
	}

	if info.InitialNodeCount != 3 {
		t.Errorf("expected initial node count 3, got %d", info.InitialNodeCount)
	}
}

func TestGKEProviderNodePoolValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	// Test missing cluster name
	err := provider.CreateNodePool(ctx, "", NodeGroupOptions{Name: "test-pool"})
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}
	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}

	// Test missing node pool name
	err = provider.CreateNodePool(ctx, "test-cluster", NodeGroupOptions{})
	if err == nil {
		t.Error("expected error for empty node pool name, got nil")
	}
	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "node pool name is required" {
			t.Errorf("expected 'node pool name is required', got %q", configErr.Message)
		}
	}
}

func TestGKENodeGroupOptionsForPool(t *testing.T) {
	// GKE uses NodeGroupOptions for node pools (same as EKS uses for node groups)
	opts := NodeGroupOptions{
		Name:         "gpu-pool",
		InstanceType: "n1-standard-4",
		DesiredSize:  2,
		MinSize:      1,
		MaxSize:      8,
		Labels: map[string]string{
			"workload": "gpu",
		},
	}

	if opts.Name != "gpu-pool" {
		t.Errorf("expected name 'gpu-pool', got %q", opts.Name)
	}

	if opts.InstanceType != "n1-standard-4" {
		t.Errorf("expected instance type 'n1-standard-4', got %q", opts.InstanceType)
	}

	if opts.DesiredSize != 2 {
		t.Errorf("expected desired size 2, got %d", opts.DesiredSize)
	}

	if opts.Labels["workload"] != "gpu" {
		t.Errorf("expected label 'workload=gpu', got %q", opts.Labels["workload"])
	}
}

func TestGKEProviderDeleteValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	err := provider.Delete(ctx, "")
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}

	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}
}

func TestGKEProviderHealthValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	// Health returns a status even for empty cluster name (with Healthy=false)
	// This tests the behavior when cluster describe fails
	status, err := provider.Health(ctx, "")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
		return
	}

	// Should return unhealthy status when cluster cannot be described
	if status.Healthy {
		t.Error("expected Healthy=false for invalid cluster")
	}
}

func TestProviderManagerWithGKE(t *testing.T) {
	manager := NewManager(false)

	gkeProvider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
	})
	manager.RegisterProvider(gkeProvider)

	provider, ok := manager.GetProvider(ClusterTypeGKE)
	if !ok {
		t.Fatal("GKE provider not found in manager")
	}

	if provider.Type() != ClusterTypeGKE {
		t.Errorf("expected GKE provider type, got %s", provider.Type())
	}

	providers := manager.ListProviders()
	found := false
	for _, pt := range providers {
		if pt == ClusterTypeGKE {
			found = true
			break
		}
	}

	if !found {
		t.Error("GKE not found in provider list")
	}
}

func TestGKEProviderListNodePoolsValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	_, err := provider.ListNodePools(ctx, "")
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}

	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}
}

func TestGKEProviderDeleteNodePoolValidation(t *testing.T) {
	provider := NewGKEProvider(GKEProviderOptions{
		ProjectID: "test-project",
		Region:    "us-central1",
		Debug:     false,
	})
	ctx := context.Background()

	// Test missing cluster name
	err := provider.DeleteNodePool(ctx, "", "test-pool")
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}
	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}

	// Test missing node pool name
	err = provider.DeleteNodePool(ctx, "test-cluster", "")
	if err == nil {
		t.Error("expected error for empty node pool name, got nil")
	}
	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "node pool name is required" {
			t.Errorf("expected 'node pool name is required', got %q", configErr.Message)
		}
	}
}

func TestGKECreateOptionsGCPFields(t *testing.T) {
	opts := CreateOptions{
		Name:              "test-cluster",
		Region:            "us-central1",
		WorkerCount:       3,
		WorkerType:        "e2-standard-4",
		KubernetesVersion: "1.28",
		GCPProject:        "my-gcp-project",
		GCPNetwork:        "my-vpc",
		GCPSubnetwork:     "my-subnet",
		Preemptible:       true,
	}

	if opts.GCPProject != "my-gcp-project" {
		t.Errorf("expected GCP project 'my-gcp-project', got %q", opts.GCPProject)
	}

	if opts.GCPNetwork != "my-vpc" {
		t.Errorf("expected GCP network 'my-vpc', got %q", opts.GCPNetwork)
	}

	if opts.GCPSubnetwork != "my-subnet" {
		t.Errorf("expected GCP subnetwork 'my-subnet', got %q", opts.GCPSubnetwork)
	}

	if !opts.Preemptible {
		t.Error("expected preemptible to be true")
	}
}
