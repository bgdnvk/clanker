package cluster

import (
	"context"
	"testing"
)

func TestAKSProviderType(t *testing.T) {
	provider := NewAKSProvider(AKSProviderOptions{
		SubscriptionID: "test-subscription",
		ResourceGroup:  "test-rg",
		Region:         "eastus",
		Debug:          false,
	})

	if provider.Type() != ClusterTypeAKS {
		t.Errorf("expected cluster type %s, got %s", ClusterTypeAKS, provider.Type())
	}
}

func TestAKSProviderCreateValidation(t *testing.T) {
	tests := []struct {
		name        string
		opts        AKSProviderOptions
		createOpts  CreateOptions
		expectError string
	}{
		{
			name: "missing cluster name",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				ResourceGroup:  "test-rg",
				Region:         "eastus",
			},
			createOpts:  CreateOptions{},
			expectError: "cluster name is required",
		},
		{
			name: "missing region",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				ResourceGroup:  "test-rg",
			},
			createOpts: CreateOptions{
				Name: "test-cluster",
			},
			expectError: "region is required",
		},
		{
			name: "missing resource group",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				Region:         "eastus",
			},
			createOpts: CreateOptions{
				Name:   "test-cluster",
				Region: "eastus",
			},
			expectError: "Azure resource group is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewAKSProvider(tt.opts)
			_, err := provider.Create(context.Background(), tt.createOpts)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if configErr, ok := err.(*ErrInvalidConfiguration); ok {
				if configErr.Message != tt.expectError {
					t.Errorf("expected error message %q, got %q", tt.expectError, configErr.Message)
				}
			} else {
				t.Errorf("expected ErrInvalidConfiguration, got %T", err)
			}
		})
	}
}

func TestAKSProviderDeleteValidation(t *testing.T) {
	tests := []struct {
		name        string
		opts        AKSProviderOptions
		clusterName string
		expectError string
	}{
		{
			name: "missing cluster name",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				ResourceGroup:  "test-rg",
				Region:         "eastus",
			},
			clusterName: "",
			expectError: "cluster name is required",
		},
		{
			name: "missing resource group",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				Region:         "eastus",
			},
			clusterName: "test-cluster",
			expectError: "resource group is required for delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewAKSProvider(tt.opts)
			err := provider.Delete(context.Background(), tt.clusterName)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if configErr, ok := err.(*ErrInvalidConfiguration); ok {
				if configErr.Message != tt.expectError {
					t.Errorf("expected error message %q, got %q", tt.expectError, configErr.Message)
				}
			} else {
				t.Errorf("expected ErrInvalidConfiguration, got %T", err)
			}
		})
	}
}

func TestAKSProviderScaleValidation(t *testing.T) {
	tests := []struct {
		name        string
		opts        AKSProviderOptions
		clusterName string
		scaleOpts   ScaleOptions
		expectError string
	}{
		{
			name: "missing cluster name",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				ResourceGroup:  "test-rg",
				Region:         "eastus",
			},
			clusterName: "",
			scaleOpts:   ScaleOptions{DesiredCount: 3},
			expectError: "cluster name is required",
		},
		{
			name: "missing resource group",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				Region:         "eastus",
			},
			clusterName: "test-cluster",
			scaleOpts:   ScaleOptions{DesiredCount: 3},
			expectError: "resource group is required for scale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewAKSProvider(tt.opts)
			err := provider.Scale(context.Background(), tt.clusterName, tt.scaleOpts)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if configErr, ok := err.(*ErrInvalidConfiguration); ok {
				if configErr.Message != tt.expectError {
					t.Errorf("expected error message %q, got %q", tt.expectError, configErr.Message)
				}
			} else {
				t.Errorf("expected ErrInvalidConfiguration, got %T", err)
			}
		})
	}
}

func TestAKSProviderGetKubeconfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		opts        AKSProviderOptions
		clusterName string
		expectError string
	}{
		{
			name: "missing cluster name",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				ResourceGroup:  "test-rg",
				Region:         "eastus",
			},
			clusterName: "",
			expectError: "cluster name is required",
		},
		{
			name: "missing resource group",
			opts: AKSProviderOptions{
				SubscriptionID: "test-sub",
				Region:         "eastus",
			},
			clusterName: "test-cluster",
			expectError: "resource group is required for kubeconfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewAKSProvider(tt.opts)
			_, err := provider.GetKubeconfig(context.Background(), tt.clusterName)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if configErr, ok := err.(*ErrInvalidConfiguration); ok {
				if configErr.Message != tt.expectError {
					t.Errorf("expected error message %q, got %q", tt.expectError, configErr.Message)
				}
			} else {
				t.Errorf("expected ErrInvalidConfiguration, got %T", err)
			}
		})
	}
}

func TestAKSProviderNodePoolValidation(t *testing.T) {
	provider := NewAKSProvider(AKSProviderOptions{
		SubscriptionID: "test-sub",
		ResourceGroup:  "test-rg",
		Region:         "eastus",
	})

	t.Run("create node pool missing cluster name", func(t *testing.T) {
		err := provider.CreateNodePool(context.Background(), "", NodeGroupOptions{Name: "pool1"})
		if err == nil {
			t.Error("expected error, got nil")
			return
		}
		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "cluster name is required" {
				t.Errorf("expected error 'cluster name is required', got %q", configErr.Message)
			}
		}
	})

	t.Run("create node pool missing pool name", func(t *testing.T) {
		err := provider.CreateNodePool(context.Background(), "test-cluster", NodeGroupOptions{})
		if err == nil {
			t.Error("expected error, got nil")
			return
		}
		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "node pool name is required" {
				t.Errorf("expected error 'node pool name is required', got %q", configErr.Message)
			}
		}
	})

	t.Run("delete node pool missing cluster name", func(t *testing.T) {
		err := provider.DeleteNodePool(context.Background(), "", "pool1")
		if err == nil {
			t.Error("expected error, got nil")
			return
		}
		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "cluster name is required" {
				t.Errorf("expected error 'cluster name is required', got %q", configErr.Message)
			}
		}
	})

	t.Run("delete node pool missing pool name", func(t *testing.T) {
		err := provider.DeleteNodePool(context.Background(), "test-cluster", "")
		if err == nil {
			t.Error("expected error, got nil")
			return
		}
		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "node pool name is required" {
				t.Errorf("expected error 'node pool name is required', got %q", configErr.Message)
			}
		}
	})
}

func TestAKSProviderNodePoolMissingResourceGroup(t *testing.T) {
	provider := NewAKSProvider(AKSProviderOptions{
		SubscriptionID: "test-sub",
		Region:         "eastus",
		// No resource group
	})

	t.Run("create node pool missing resource group", func(t *testing.T) {
		err := provider.CreateNodePool(context.Background(), "test-cluster", NodeGroupOptions{Name: "pool1"})
		if err == nil {
			t.Error("expected error, got nil")
			return
		}
		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "resource group is required" {
				t.Errorf("expected error 'resource group is required', got %q", configErr.Message)
			}
		}
	})

	t.Run("delete node pool missing resource group", func(t *testing.T) {
		err := provider.DeleteNodePool(context.Background(), "test-cluster", "pool1")
		if err == nil {
			t.Error("expected error, got nil")
			return
		}
		if configErr, ok := err.(*ErrInvalidConfiguration); ok {
			if configErr.Message != "resource group is required" {
				t.Errorf("expected error 'resource group is required', got %q", configErr.Message)
			}
		}
	})
}

func TestAKSProviderErrorHints(t *testing.T) {
	provider := NewAKSProvider(AKSProviderOptions{
		SubscriptionID: "test-sub",
		ResourceGroup:  "test-rg",
		Region:         "eastus",
	})

	tests := []struct {
		name         string
		stderr       string
		expectedHint string
	}{
		{
			name:         "authorization failed",
			stderr:       "AuthorizationFailed: The client does not have authorization",
			expectedHint: "(hint: check Azure RBAC permissions or subscription access)",
		},
		{
			name:         "permission denied",
			stderr:       "permission denied to perform action",
			expectedHint: "(hint: check Azure RBAC permissions or subscription access)",
		},
		{
			name:         "resource not found",
			stderr:       "ResourceNotFound: The resource was not found",
			expectedHint: "(hint: resource may not exist or check resource group/subscription)",
		},
		{
			name:         "invalid subscription",
			stderr:       "InvalidSubscriptionId: The subscription is not valid",
			expectedHint: "(hint: run 'az account list' to verify subscription ID)",
		},
		{
			name:         "login required",
			stderr:       "Please run 'az login' to setup account",
			expectedHint: "(hint: run 'az login' to authenticate with Azure)",
		},
		{
			name:         "quota exceeded",
			stderr:       "QuotaExceeded: Operation could not be completed due to quota",
			expectedHint: "(hint: Azure quota exceeded, request increase or use different region)",
		},
		{
			name:         "resource group not found",
			stderr:       "ResourceGroupNotFound: Resource group 'test-rg' could not be found",
			expectedHint: "(hint: resource group does not exist, create it first with 'az group create')",
		},
		{
			name:         "conflicting operation",
			stderr:       "ConflictingServerOperation: Another operation is in progress",
			expectedHint: "(hint: another operation is in progress, wait and retry)",
		},
		{
			name:         "unknown error",
			stderr:       "Some unknown error occurred",
			expectedHint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := provider.errorHint(tt.stderr)
			if tt.expectedHint != "" && hint == "" {
				t.Errorf("expected hint containing %q, got empty string", tt.expectedHint)
			}
			if tt.expectedHint != "" && hint != "" && hint[1:] != tt.expectedHint[0:] {
				// Compare without leading space
				if hint != " "+tt.expectedHint {
					t.Errorf("expected hint %q, got %q", " "+tt.expectedHint, hint)
				}
			}
		})
	}
}

func TestAKSProviderRetryableErrors(t *testing.T) {
	provider := NewAKSProvider(AKSProviderOptions{
		SubscriptionID: "test-sub",
		ResourceGroup:  "test-rg",
		Region:         "eastus",
	})

	tests := []struct {
		name        string
		stderr      string
		isRetryable bool
	}{
		{
			name:        "rate limit",
			stderr:      "Rate limit exceeded, please retry",
			isRetryable: true,
		},
		{
			name:        "throttling",
			stderr:      "Request was throttled due to high load",
			isRetryable: true,
		},
		{
			name:        "timeout",
			stderr:      "The operation timed out",
			isRetryable: true,
		},
		{
			name:        "service unavailable",
			stderr:      "Service unavailable, please try again",
			isRetryable: true,
		},
		{
			name:        "temporarily unavailable",
			stderr:      "The service is temporarily unavailable",
			isRetryable: true,
		},
		{
			name:        "internal error",
			stderr:      "Internal error occurred",
			isRetryable: true,
		},
		{
			name:        "not found",
			stderr:      "ResourceNotFound: cluster not found",
			isRetryable: false,
		},
		{
			name:        "invalid configuration",
			stderr:      "InvalidConfiguration: invalid cluster name",
			isRetryable: false,
		},
		{
			name:        "authorization failed",
			stderr:      "AuthorizationFailed: not authorized",
			isRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.isRetryableError(tt.stderr)
			if result != tt.isRetryable {
				t.Errorf("expected isRetryable=%v for stderr %q, got %v", tt.isRetryable, tt.stderr, result)
			}
		})
	}
}

func TestAKSProviderOptions(t *testing.T) {
	opts := AKSProviderOptions{
		SubscriptionID: "sub-123",
		ResourceGroup:  "rg-test",
		Region:         "westus2",
		Debug:          true,
	}

	provider := NewAKSProvider(opts)

	if provider.subscriptionID != opts.SubscriptionID {
		t.Errorf("expected subscriptionID %q, got %q", opts.SubscriptionID, provider.subscriptionID)
	}
	if provider.resourceGroup != opts.ResourceGroup {
		t.Errorf("expected resourceGroup %q, got %q", opts.ResourceGroup, provider.resourceGroup)
	}
	if provider.region != opts.Region {
		t.Errorf("expected region %q, got %q", opts.Region, provider.region)
	}
	if provider.debug != opts.Debug {
		t.Errorf("expected debug %v, got %v", opts.Debug, provider.debug)
	}
}

func TestAKSNodeGroupOptionsUsage(t *testing.T) {
	// NodeGroupOptions is defined in eks.go and shared across providers
	// This test verifies AKS can use the shared type
	opts := NodeGroupOptions{
		Name:         "testpool",
		DesiredSize:  3,
		MinSize:      1,
		MaxSize:      5,
		InstanceType: "Standard_D2s_v3",
		DiskSize:     100,
		Labels: map[string]string{
			"env":  "test",
			"team": "platform",
		},
	}

	if opts.Name != "testpool" {
		t.Errorf("expected Name 'testpool', got %q", opts.Name)
	}
	if opts.DesiredSize != 3 {
		t.Errorf("expected DesiredSize 3, got %d", opts.DesiredSize)
	}
}
