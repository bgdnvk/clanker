package cluster

import (
	"context"
	"testing"
	"time"
)

func TestEKSProviderType(t *testing.T) {
	provider := NewEKSProvider(EKSProviderOptions{
		AWSProfile: "test",
		Region:     "us-east-1",
		Debug:      false,
	})

	if provider.Type() != ClusterTypeEKS {
		t.Errorf("expected cluster type %s, got %s", ClusterTypeEKS, provider.Type())
	}
}

func TestEKSProviderCreateValidation(t *testing.T) {
	provider := NewEKSProvider(EKSProviderOptions{
		Debug: false,
	})
	ctx := context.Background()

	tests := []struct {
		name    string
		opts    CreateOptions
		wantErr string
	}{
		{
			name:    "missing cluster name",
			opts:    CreateOptions{},
			wantErr: "cluster name is required",
		},
		{
			name: "missing region",
			opts: CreateOptions{
				Name: "test-cluster",
			},
			wantErr: "region is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.Create(ctx, tt.opts)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}

			if configErr, ok := err.(*ErrInvalidConfiguration); ok {
				if configErr.Message != tt.wantErr {
					t.Errorf("expected error message %q, got %q", tt.wantErr, configErr.Message)
				}
			} else {
				t.Errorf("expected ErrInvalidConfiguration, got %T", err)
			}
		})
	}
}

func TestEKSProviderScaleValidation(t *testing.T) {
	provider := NewEKSProvider(EKSProviderOptions{
		Debug: false,
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

func TestEKSProviderGetKubeconfigValidation(t *testing.T) {
	provider := NewEKSProvider(EKSProviderOptions{
		Debug: false,
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

func TestEKSProviderOptions(t *testing.T) {
	opts := EKSProviderOptions{
		AWSProfile: "myprofile",
		Region:     "eu-west-1",
		Debug:      true,
	}

	provider := NewEKSProvider(opts)

	if provider.awsProfile != "myprofile" {
		t.Errorf("expected AWS profile 'myprofile', got %q", provider.awsProfile)
	}

	if provider.region != "eu-west-1" {
		t.Errorf("expected region 'eu-west-1', got %q", provider.region)
	}

	if !provider.debug {
		t.Error("expected debug to be true")
	}
}

func TestEKSClusterInfoParsing(t *testing.T) {
	info := &eksClusterInfo{
		Name:      "test-cluster",
		Status:    "ACTIVE",
		Version:   "1.28",
		Endpoint:  "https://example.eks.amazonaws.com",
		VpcId:     "vpc-123",
		CreatedAt: time.Now(),
	}

	if info.Name != "test-cluster" {
		t.Errorf("expected name 'test-cluster', got %q", info.Name)
	}

	if info.Status != "ACTIVE" {
		t.Errorf("expected status 'ACTIVE', got %q", info.Status)
	}

	if info.Version != "1.28" {
		t.Errorf("expected version '1.28', got %q", info.Version)
	}
}

func TestEKSNodeGroupInfoParsing(t *testing.T) {
	info := &eksNodeGroupInfo{
		NodegroupName: "test-ng",
		Status:        "ACTIVE",
		DesiredSize:   3,
		MinSize:       1,
		MaxSize:       5,
	}

	if info.NodegroupName != "test-ng" {
		t.Errorf("expected nodegroup name 'test-ng', got %q", info.NodegroupName)
	}

	if info.DesiredSize != 3 {
		t.Errorf("expected desired size 3, got %d", info.DesiredSize)
	}
}

func TestEKSProviderNodeGroupValidation(t *testing.T) {
	provider := NewEKSProvider(EKSProviderOptions{
		Region: "us-east-1",
		Debug:  false,
	})
	ctx := context.Background()

	// Test missing cluster name
	err := provider.CreateNodeGroup(ctx, "", NodeGroupOptions{Name: "test-ng"})
	if err == nil {
		t.Error("expected error for empty cluster name, got nil")
	}
	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "cluster name is required" {
			t.Errorf("expected 'cluster name is required', got %q", configErr.Message)
		}
	}

	// Test missing node group name
	err = provider.CreateNodeGroup(ctx, "test-cluster", NodeGroupOptions{})
	if err == nil {
		t.Error("expected error for empty node group name, got nil")
	}
	if configErr, ok := err.(*ErrInvalidConfiguration); ok {
		if configErr.Message != "node group name is required" {
			t.Errorf("expected 'node group name is required', got %q", configErr.Message)
		}
	}
}

func TestNodeGroupOptions(t *testing.T) {
	opts := NodeGroupOptions{
		Name:         "workers",
		InstanceType: "t3.medium",
		DesiredSize:  3,
		MinSize:      1,
		MaxSize:      5,
		DiskSize:     50,
		Labels: map[string]string{
			"role": "worker",
		},
	}

	if opts.Name != "workers" {
		t.Errorf("expected name 'workers', got %q", opts.Name)
	}

	if opts.DesiredSize != 3 {
		t.Errorf("expected desired size 3, got %d", opts.DesiredSize)
	}

	if opts.Labels["role"] != "worker" {
		t.Errorf("expected label 'role=worker', got %q", opts.Labels["role"])
	}
}

func TestNodeTaint(t *testing.T) {
	taint := NodeTaint{
		Key:    "dedicated",
		Value:  "gpu",
		Effect: "NoSchedule",
	}

	if taint.Key != "dedicated" {
		t.Errorf("expected key 'dedicated', got %q", taint.Key)
	}

	if taint.Effect != "NoSchedule" {
		t.Errorf("expected effect 'NoSchedule', got %q", taint.Effect)
	}
}

func TestProviderManagerIntegration(t *testing.T) {
	manager := NewManager(false)

	eksProvider := NewEKSProvider(EKSProviderOptions{
		Region: "us-east-1",
	})
	manager.RegisterProvider(eksProvider)

	provider, ok := manager.GetProvider(ClusterTypeEKS)
	if !ok {
		t.Fatal("EKS provider not found in manager")
	}

	if provider.Type() != ClusterTypeEKS {
		t.Errorf("expected EKS provider type, got %s", provider.Type())
	}

	providers := manager.ListProviders()
	found := false
	for _, pt := range providers {
		if pt == ClusterTypeEKS {
			found = true
			break
		}
	}

	if !found {
		t.Error("EKS not found in provider list")
	}
}
