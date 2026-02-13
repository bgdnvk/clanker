package sre

import (
	"strings"
	"testing"
)

func TestAKSRemediationSteps(t *testing.T) {
	tests := []struct {
		name         string
		issue        Issue
		resourceName string
		namespace    string
		wantSteps    bool
		wantCommand  string
	}{
		{
			name: "Node pool issue",
			issue: Issue{
				Category: CategoryAKSNodePool,
			},
			resourceName: "mypool",
			wantSteps:    true,
			wantCommand:  "az",
		},
		{
			name: "Managed Identity issue",
			issue: Issue{
				Category: CategoryAKSManagedIdentity,
			},
			resourceName: "myidentity",
			wantSteps:    true,
			wantCommand:  "az",
		},
		{
			name: "Spot eviction issue",
			issue: Issue{
				Category: CategoryAKSSpotEviction,
			},
			resourceName: "mynode",
			wantSteps:    true,
			wantCommand:  "kubectl",
		},
		{
			name: "Quota exceeded issue",
			issue: Issue{
				Category: CategoryAKSQuotaExceeded,
			},
			resourceName: "",
			wantSteps:    true,
			wantCommand:  "az",
		},
		{
			name: "Virtual node issue",
			issue: Issue{
				Category: CategoryAKSVirtualNode,
			},
			resourceName: "virtual-node",
			wantSteps:    true,
			wantCommand:  "kubectl",
		},
		{
			name: "Autoscaling issue",
			issue: Issue{
				Category: CategoryAKSAutoscaling,
			},
			resourceName: "nodepool1",
			wantSteps:    true,
			wantCommand:  "kubectl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := AKSRemediationSteps(tt.issue, tt.resourceName, tt.namespace)

			if tt.wantSteps && len(steps) == 0 {
				t.Error("expected remediation steps")
			}

			if len(steps) > 0 {
				foundCommand := false
				for _, step := range steps {
					if step.Command == tt.wantCommand {
						foundCommand = true
						break
					}
				}
				if !foundCommand {
					t.Errorf("expected command %s in remediation steps", tt.wantCommand)
				}
			}
		})
	}
}

func TestAKSDiagnosticChecks(t *testing.T) {
	checks := AKSDiagnosticChecks()

	if len(checks) == 0 {
		t.Error("expected at least one diagnostic check")
	}

	checksText := strings.Join(checks, " ")

	expectedTopics := []string{
		"node pool",
		"Workload Identity",
		"Spot",
		"autoscaler",
		"Virtual Nodes",
		"Azure Monitor",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(checksText, topic) {
			t.Errorf("diagnostic checks should mention %s", topic)
		}
	}
}

func TestAKSHealthNotes(t *testing.T) {
	notes := AKSHealthNotes()

	if len(notes) == 0 {
		t.Error("expected at least one health note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"control plane",
		"az aks",
		"Workload Identity",
		"Spot",
		"Azure Monitor",
		"AKS Diagnostics",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("health notes should mention %s", topic)
		}
	}
}

func TestIsAKSIssueCategory(t *testing.T) {
	tests := []struct {
		category IssueCategory
		want     bool
	}{
		{CategoryAKSNodePool, true},
		{CategoryAKSManagedIdentity, true},
		{CategoryAKSSpotEviction, true},
		{CategoryAKSQuotaExceeded, true},
		{CategoryAKSNetworkPolicy, true},
		{CategoryAKSVirtualNode, true},
		{CategoryAKSAutoscaling, true},
		{CategoryGKENodePool, false},
		{CategoryGKEWorkloadID, false},
		{CategoryCrash, false},
		{CategoryPending, false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			got := IsAKSIssueCategory(tt.category)
			if got != tt.want {
				t.Errorf("IsAKSIssueCategory(%q) = %v, want %v", tt.category, got, tt.want)
			}
		})
	}
}

func TestGetAKSIssueGuidance(t *testing.T) {
	categories := []IssueCategory{
		CategoryAKSNodePool,
		CategoryAKSManagedIdentity,
		CategoryAKSSpotEviction,
		CategoryAKSQuotaExceeded,
		CategoryAKSNetworkPolicy,
		CategoryAKSVirtualNode,
		CategoryAKSAutoscaling,
	}

	for _, category := range categories {
		guidance := GetAKSIssueGuidance(category)
		if guidance == "" {
			t.Errorf("expected guidance for category %s", category)
		}
		if guidance == "No specific guidance available for this issue category." {
			t.Errorf("expected specific guidance for AKS category %s", category)
		}
	}

	// Test unknown category
	unknownGuidance := GetAKSIssueGuidance("unknown_category")
	if unknownGuidance != "No specific guidance available for this issue category." {
		t.Errorf("expected default guidance for unknown category")
	}
}

func TestAKSManagedIdentityChecks(t *testing.T) {
	checks := AKSManagedIdentityChecks()

	if len(checks) == 0 {
		t.Error("expected at least one managed identity check")
	}

	checksText := strings.Join(checks, " ")

	expectedTopics := []string{
		"client-id",
		"federated",
		"OIDC",
		"RBAC",
		"managed identity",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(checksText, topic) {
			t.Errorf("managed identity checks should mention %s", topic)
		}
	}
}

func TestGKESREComparison(t *testing.T) {
	comparison := GKESREComparison()

	if len(comparison) == 0 {
		t.Error("expected SRE comparison entries")
	}

	// Verify AKS entries
	aksKeys := []string{"aks_identity", "aks_node_pool_check", "aks_spot_eviction", "aks_diagnostics"}
	for _, key := range aksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_identity", "gke_node_pool_check", "gke_preemption", "gke_diagnostics"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_identity", "eks_node_pool_check", "eks_spot_eviction", "eks_diagnostics"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestAKSIssueCategoryConstants(t *testing.T) {
	if CategoryAKSNodePool != "aks_node_pool" {
		t.Errorf("CategoryAKSNodePool = %s, want aks_node_pool", CategoryAKSNodePool)
	}

	if CategoryAKSManagedIdentity != "aks_managed_identity" {
		t.Errorf("CategoryAKSManagedIdentity = %s, want aks_managed_identity", CategoryAKSManagedIdentity)
	}

	if CategoryAKSSpotEviction != "aks_spot_eviction" {
		t.Errorf("CategoryAKSSpotEviction = %s, want aks_spot_eviction", CategoryAKSSpotEviction)
	}

	if CategoryAKSQuotaExceeded != "aks_quota_exceeded" {
		t.Errorf("CategoryAKSQuotaExceeded = %s, want aks_quota_exceeded", CategoryAKSQuotaExceeded)
	}

	if CategoryAKSVirtualNode != "aks_virtual_node" {
		t.Errorf("CategoryAKSVirtualNode = %s, want aks_virtual_node", CategoryAKSVirtualNode)
	}

	if CategoryAKSAutoscaling != "aks_autoscaling" {
		t.Errorf("CategoryAKSAutoscaling = %s, want aks_autoscaling", CategoryAKSAutoscaling)
	}
}

func TestAKSNodePoolStatusStruct(t *testing.T) {
	status := AKSNodePoolStatus{
		Name:              "nodepool1",
		Status:            "healthy",
		NodeCount:         3,
		ReadyCount:        3,
		Mode:              "User",
		VMSize:            "Standard_DS2_v2",
		Spot:              false,
		PowerState:        "Running",
		ProvisioningState: "Succeeded",
	}

	if status.Name != "nodepool1" {
		t.Errorf("expected name 'nodepool1', got %s", status.Name)
	}

	if status.Mode != "User" {
		t.Errorf("expected mode 'User', got %s", status.Mode)
	}

	if status.VMSize != "Standard_DS2_v2" {
		t.Errorf("expected VMSize 'Standard_DS2_v2', got %s", status.VMSize)
	}
}

func TestAKSClusterHealthStruct(t *testing.T) {
	health := AKSClusterHealth{
		ControlPlaneHealthy: true,
		NodePoolsHealthy:    true,
		ManagedIdentityOK:   true,
		NetworkPolicyStatus: "enabled",
		VirtualNodeStatus:   "ready",
	}

	if !health.ControlPlaneHealthy {
		t.Error("expected ControlPlaneHealthy to be true")
	}

	if !health.NodePoolsHealthy {
		t.Error("expected NodePoolsHealthy to be true")
	}

	if !health.ManagedIdentityOK {
		t.Error("expected ManagedIdentityOK to be true")
	}
}

func TestNewAKSHealthCheck(t *testing.T) {
	// Test that constructor doesn't panic
	checker := NewAKSHealthCheck(nil, false)
	if checker == nil {
		t.Error("expected non-nil AKSHealthCheck")
	}

	checkerDebug := NewAKSHealthCheck(nil, true)
	if checkerDebug == nil {
		t.Error("expected non-nil AKSHealthCheck with debug")
	}
}
