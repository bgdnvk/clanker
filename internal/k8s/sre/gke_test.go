package sre

import (
	"strings"
	"testing"
	"time"
)

func TestGKERemediationSteps(t *testing.T) {
	tests := []struct {
		name         string
		issue        Issue
		resourceName string
		namespace    string
		wantSteps    bool
	}{
		{
			name: "Node pool issue",
			issue: Issue{
				Category: CategoryGKENodePool,
				Severity: SeverityCritical,
			},
			resourceName: "default-pool",
			namespace:    "default",
			wantSteps:    true,
		},
		{
			name: "Workload Identity issue",
			issue: Issue{
				Category: CategoryGKEWorkloadID,
				Severity: SeverityWarning,
			},
			resourceName: "my-sa",
			namespace:    "default",
			wantSteps:    true,
		},
		{
			name: "Preemption issue",
			issue: Issue{
				Category: CategoryGKEPreemption,
				Severity: SeverityInfo,
			},
			resourceName: "preempt-pool",
			namespace:    "default",
			wantSteps:    true,
		},
		{
			name: "Quota exceeded",
			issue: Issue{
				Category: CategoryGKEQuotaExceeded,
				Severity: SeverityWarning,
			},
			resourceName: "cluster",
			namespace:    "",
			wantSteps:    true,
		},
		{
			name: "Autopilot issue",
			issue: Issue{
				Category: CategoryGKEAutopilot,
				Severity: SeverityWarning,
			},
			resourceName: "my-deployment",
			namespace:    "default",
			wantSteps:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := GKERemediationSteps(tt.issue, tt.resourceName, tt.namespace)

			if tt.wantSteps && len(steps) == 0 {
				t.Error("expected remediation steps but got none")
			}

			for _, step := range steps {
				if step.Action == "" {
					t.Error("remediation step action should not be empty")
				}
				if step.Description == "" {
					t.Error("remediation step description should not be empty")
				}
			}
		})
	}
}

func TestGKERemediationStepsNodePool(t *testing.T) {
	issue := Issue{
		Category: CategoryGKENodePool,
		Severity: SeverityCritical,
	}

	steps := GKERemediationSteps(issue, "my-pool", "default")

	if len(steps) < 2 {
		t.Error("expected at least 2 remediation steps for node pool issues")
	}

	// Check that gcloud commands are included
	hasGcloudCheck := false
	hasGcloudRepair := false

	for _, step := range steps {
		if step.Command == "gcloud" {
			if strings.Contains(step.Action, "Check") || strings.Contains(step.Action, "status") {
				hasGcloudCheck = true
			}
			if strings.Contains(step.Action, "Repair") || strings.Contains(step.Action, "repair") {
				hasGcloudRepair = true
			}
		}
	}

	if !hasGcloudCheck {
		t.Error("expected gcloud status check step for node pool issues")
	}
	if !hasGcloudRepair {
		t.Error("expected gcloud repair step for node pool issues")
	}
}

func TestGKERemediationStepsWorkloadIdentity(t *testing.T) {
	issue := Issue{
		Category: CategoryGKEWorkloadID,
		Severity: SeverityWarning,
	}

	steps := GKERemediationSteps(issue, "my-sa", "production")

	if len(steps) < 2 {
		t.Error("expected at least 2 remediation steps for Workload Identity issues")
	}

	// Check that IAM-related steps are included
	hasIAMCheck := false
	hasIAMBinding := false

	for _, step := range steps {
		if strings.Contains(step.Description, "IAM") || strings.Contains(step.Description, "binding") {
			hasIAMCheck = true
		}
		if strings.Contains(step.Description, "Bind") || strings.Contains(step.Description, "binding") {
			hasIAMBinding = true
		}
	}

	if !hasIAMCheck {
		t.Error("expected IAM check step for Workload Identity issues")
	}
	if !hasIAMBinding {
		t.Error("expected IAM binding step for Workload Identity issues")
	}
}

func TestGKEDiagnosticChecks(t *testing.T) {
	checks := GKEDiagnosticChecks()

	if len(checks) == 0 {
		t.Error("expected at least one diagnostic check")
	}

	checksText := strings.Join(checks, " ")

	expectedTopics := []string{
		"node pool",
		"Workload Identity",
		"preemption",
		"autoscaler",
		"network policy",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(strings.ToLower(checksText), strings.ToLower(topic)) {
			t.Errorf("diagnostic checks should mention %s", topic)
		}
	}
}

func TestGKEHealthNotes(t *testing.T) {
	notes := GKEHealthNotes()

	if len(notes) == 0 {
		t.Error("expected at least one health note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"control plane",
		"gcloud",
		"Workload Identity",
		"Preemptible",
		"Spot",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("health notes should mention %s", topic)
		}
	}
}

func TestIsGKEIssueCategory(t *testing.T) {
	tests := []struct {
		category IssueCategory
		want     bool
	}{
		{CategoryGKENodePool, true},
		{CategoryGKEWorkloadID, true},
		{CategoryGKEAutoscaling, true},
		{CategoryGKENetworkPolicy, true},
		{CategoryGKEPreemption, true},
		{CategoryGKEQuotaExceeded, true},
		{CategoryGKEAutopilot, true},
		{CategoryCrash, false},
		{CategoryPending, false},
		{CategoryResourceLimit, false},
		{CategoryImagePull, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			got := IsGKEIssueCategory(tt.category)
			if got != tt.want {
				t.Errorf("IsGKEIssueCategory(%q) = %v, want %v", tt.category, got, tt.want)
			}
		})
	}
}

func TestGetGKEIssueGuidance(t *testing.T) {
	categories := []IssueCategory{
		CategoryGKENodePool,
		CategoryGKEWorkloadID,
		CategoryGKEAutoscaling,
		CategoryGKENetworkPolicy,
		CategoryGKEPreemption,
		CategoryGKEQuotaExceeded,
		CategoryGKEAutopilot,
	}

	for _, category := range categories {
		t.Run(string(category), func(t *testing.T) {
			guidance := GetGKEIssueGuidance(category)

			if guidance == "" {
				t.Errorf("expected guidance for category %s", category)
			}

			if guidance == "No specific guidance available for this issue category." {
				t.Errorf("expected specific guidance for GKE category %s", category)
			}
		})
	}

	// Test unknown category
	unknownGuidance := GetGKEIssueGuidance("unknown-category")
	if unknownGuidance != "No specific guidance available for this issue category." {
		t.Error("expected default guidance for unknown category")
	}
}

func TestGKEClusterHealth(t *testing.T) {
	health := &GKEClusterHealth{
		ControlPlaneHealthy: true,
		NodePoolsHealthy:    true,
		WorkloadIdentityOK:  true,
		AutoscalingStatus:   "active",
		NodePoolStatuses: []NodePoolStatus{
			{
				Name:        "default-pool",
				Status:      "healthy",
				NodeCount:   3,
				ReadyCount:  3,
				Preemptible: false,
				Spot:        false,
			},
			{
				Name:        "spot-pool",
				Status:      "healthy",
				NodeCount:   2,
				ReadyCount:  2,
				Preemptible: false,
				Spot:        true,
			},
		},
	}

	if !health.ControlPlaneHealthy {
		t.Error("expected control plane to be healthy")
	}

	if !health.NodePoolsHealthy {
		t.Error("expected node pools to be healthy")
	}

	if len(health.NodePoolStatuses) != 2 {
		t.Errorf("expected 2 node pool statuses, got %d", len(health.NodePoolStatuses))
	}
}

func TestNodePoolStatus(t *testing.T) {
	status := NodePoolStatus{
		Name:         "preempt-pool",
		Status:       "degraded",
		NodeCount:    5,
		ReadyCount:   3,
		Preemptible:  true,
		Spot:         false,
		AutoscaleMin: 1,
		AutoscaleMax: 10,
		MachineType:  "e2-medium",
	}

	if status.Name != "preempt-pool" {
		t.Errorf("expected name 'preempt-pool', got %s", status.Name)
	}

	if status.Status != "degraded" {
		t.Errorf("expected status 'degraded', got %s", status.Status)
	}

	if !status.Preemptible {
		t.Error("expected preemptible to be true")
	}

	if status.AutoscaleMax != 10 {
		t.Errorf("expected autoscale max 10, got %d", status.AutoscaleMax)
	}
}

func TestGKEIssueCategoriesConstants(t *testing.T) {
	// Verify GKE issue category constants
	if CategoryGKENodePool != "gke_node_pool" {
		t.Errorf("CategoryGKENodePool = %s, want gke_node_pool", CategoryGKENodePool)
	}

	if CategoryGKEWorkloadID != "gke_workload_identity" {
		t.Errorf("CategoryGKEWorkloadID = %s, want gke_workload_identity", CategoryGKEWorkloadID)
	}

	if CategoryGKEPreemption != "gke_preemption" {
		t.Errorf("CategoryGKEPreemption = %s, want gke_preemption", CategoryGKEPreemption)
	}

	if CategoryGKEQuotaExceeded != "gke_quota_exceeded" {
		t.Errorf("CategoryGKEQuotaExceeded = %s, want gke_quota_exceeded", CategoryGKEQuotaExceeded)
	}

	if CategoryGKEAutopilot != "gke_autopilot" {
		t.Errorf("CategoryGKEAutopilot = %s, want gke_autopilot", CategoryGKEAutopilot)
	}
}

func TestGKEIssueCreation(t *testing.T) {
	issue := Issue{
		ID:           "gke-test-issue",
		Severity:     SeverityWarning,
		Category:     CategoryGKENodePool,
		ResourceType: ResourceNode,
		ResourceName: "gke-cluster-default-pool-abc123",
		Message:      "Node pool has nodes in NotReady state",
		Timestamp:    time.Now(),
		Suggestions: []string{
			"Check node conditions",
			"Review node events",
		},
	}

	if issue.Category != CategoryGKENodePool {
		t.Errorf("expected category %s, got %s", CategoryGKENodePool, issue.Category)
	}

	if !IsGKEIssueCategory(issue.Category) {
		t.Error("expected issue category to be identified as GKE-specific")
	}

	if len(issue.Suggestions) != 2 {
		t.Errorf("expected 2 suggestions, got %d", len(issue.Suggestions))
	}
}

func TestWorkloadIdentityIssueDetection(t *testing.T) {
	// Create a test issue for Workload Identity misconfiguration
	issue := Issue{
		ID:           "gke-wi-misconfigured",
		Severity:     SeverityWarning,
		Category:     CategoryGKEWorkloadID,
		ResourceType: ResourcePod,
		ResourceName: "my-app-pod",
		Namespace:    "production",
		Message:      "Service account has invalid GCP SA annotation",
		Details:      "production/my-sa: invalid@example.com",
		Timestamp:    time.Now(),
		Suggestions: []string{
			"Verify GCP service account email format",
			"Ensure IAM binding exists",
		},
	}

	if issue.Category != CategoryGKEWorkloadID {
		t.Errorf("expected Workload Identity category, got %s", issue.Category)
	}

	guidance := GetGKEIssueGuidance(issue.Category)
	if !strings.Contains(guidance, "IAM") {
		t.Error("Workload Identity guidance should mention IAM")
	}
}

func TestPreemptionIssueHandling(t *testing.T) {
	issue := Issue{
		ID:           "gke-preemption-event",
		Severity:     SeverityInfo,
		Category:     CategoryGKEPreemption,
		ResourceType: ResourceNode,
		ResourceName: "gke-cluster-spot-pool-xyz789",
		Message:      "Node preempted by GCE",
		Timestamp:    time.Now(),
		Suggestions: []string{
			"This is expected behavior for spot/preemptible VMs",
			"Ensure workloads are fault-tolerant",
		},
	}

	// Preemption issues should be informational, not critical
	if issue.Severity != SeverityInfo {
		t.Errorf("preemption issues should be informational, got %s", issue.Severity)
	}

	steps := GKERemediationSteps(issue, issue.ResourceName, "default")
	if len(steps) == 0 {
		t.Error("expected remediation steps for preemption issues")
	}

	// Check that PDB recommendation is included
	hasPDBStep := false
	for _, step := range steps {
		if strings.Contains(step.Description, "PodDisruptionBudget") || strings.Contains(step.Description, "PDB") {
			hasPDBStep = true
			break
		}
	}

	if !hasPDBStep {
		t.Error("preemption remediation should include PodDisruptionBudget recommendation")
	}
}
