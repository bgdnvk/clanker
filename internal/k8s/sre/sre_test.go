package sre

import (
	"context"
	"testing"
)

// mockK8sClient implements K8sClient for testing
type mockK8sClient struct {
	runOutput     string
	runErr        error
	runJSONOutput []byte
	runCalls      [][]string
}

func (m *mockK8sClient) Run(ctx context.Context, args ...string) (string, error) {
	m.runCalls = append(m.runCalls, args)
	return m.runOutput, m.runErr
}

func (m *mockK8sClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	m.runCalls = append(m.runCalls, args)
	return m.runOutput, m.runErr
}

func (m *mockK8sClient) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	m.runCalls = append(m.runCalls, args)
	return m.runJSONOutput, m.runErr
}

func TestNewSubAgent(t *testing.T) {
	client := &mockK8sClient{}
	agent := NewSubAgent(client, false)

	if agent == nil {
		t.Fatal("expected non-nil agent")
	}
	if agent.client != client {
		t.Error("client not set correctly")
	}
	if agent.diagnostics == nil {
		t.Error("diagnostics manager not initialized")
	}
	if agent.health == nil {
		t.Error("health checker not initialized")
	}
}

func TestDetectResourceType(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		query    string
		expected ResourceType
	}{
		{"check pod nginx", ResourcePod},
		{"diagnose deployment web-app", ResourceDeployment},
		{"analyze node worker-1", ResourceNode},
		{"service health", ResourceService},
		{"pvc issues", ResourcePVC},
		{"what events occurred", ResourceEvent},
		{"general cluster status", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.detectResourceType(tt.query)
			if result != tt.expected {
				t.Errorf("detectResourceType(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestDetectOperation(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"check cluster health", "health"},
		{"is my app healthy", "health"},
		{"cluster status", "health"},
		{"diagnose the issue", "diagnose"},
		{"analyze pod nginx", "diagnose"},
		{"troubleshoot deployment", "diagnose"},
		{"get logs for pod web", "logs"},
		{"show pod output", "logs"},
		{"what events happened", "events"},
		{"list issues", "issues"},
		{"what problems exist", "issues"},
		{"why is pod not ready", "why"},
		{"what is wrong with deployment", "why"},
		{"fix the issue", "fix"},
		{"restart the pod", "fix"},
		{"some random query", "health"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.detectOperation(tt.query)
			if result != tt.expected {
				t.Errorf("detectOperation(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractResourceName(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"check pod nginx-app", "nginx-app"},
		{"deployment web-server status", "web-server"},
		{"diagnose for web-server", "web-server"},
		{"for my-app", "my-app"},
		{"list all pods", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractResourceName(tt.query)
			if result != tt.expected {
				t.Errorf("extractResourceName(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractNamespace(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		query    string
		expected string
	}{
		{"check pods in namespace production", "production"},
		{"pods -n kube-system", "kube-system"},
		{"in ns monitoring", "monitoring"},
		{"all pods", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractNamespace(tt.query)
			if result != tt.expected {
				t.Errorf("extractNamespace(%q) = %v, want %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestIsCommonWord(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	commonWords := []string{"the", "all", "my", "cluster", "namespace"}
	for _, word := range commonWords {
		if !agent.isCommonWord(word) {
			t.Errorf("expected %q to be common word", word)
		}
	}

	notCommon := []string{"nginx", "web-app", "production"}
	for _, word := range notCommon {
		if agent.isCommonWord(word) {
			t.Errorf("expected %q to not be common word", word)
		}
	}
}

func TestAnalyzeQuery(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		query            string
		expectedType     ResourceType
		expectedOp       string
		expectedReadOnly bool
	}{
		{"check cluster health", "", "health", true},
		{"diagnose pod nginx in namespace production", ResourcePod, "diagnose", true},
		{"fix pod crash-loop", ResourcePod, "fix", false},
		{"why is deployment not ready", ResourceDeployment, "why", true},
		{"get logs for pod web-app", ResourcePod, "logs", true},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.ResourceType != tt.expectedType {
				t.Errorf("ResourceType = %v, want %v", analysis.ResourceType, tt.expectedType)
			}
			if analysis.Operation != tt.expectedOp {
				t.Errorf("Operation = %v, want %v", analysis.Operation, tt.expectedOp)
			}
			if analysis.IsReadOnly != tt.expectedReadOnly {
				t.Errorf("IsReadOnly = %v, want %v", analysis.IsReadOnly, tt.expectedReadOnly)
			}
		})
	}
}

func TestGetRemediationStepsForIssue(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		category      IssueCategory
		expectSteps   bool
		expectCommand string
	}{
		{CategoryCrash, true, "kubectl"},
		{CategoryImagePull, true, "kubectl"},
		{CategoryResourceLimit, true, "kubectl"},
		{CategoryPending, true, "kubectl"},
		{CategoryProbe, true, "kubectl"},
		{CategoryNodePressure, true, "kubectl"},
		{CategoryStorage, true, "kubectl"},
	}

	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			issue := Issue{
				Category:     tt.category,
				ResourceName: "test-resource",
				Namespace:    "default",
			}
			steps := agent.getRemediationStepsForIssue(issue, "test-resource", "default")
			if tt.expectSteps && len(steps) == 0 {
				t.Error("expected remediation steps")
			}
			if len(steps) > 0 && tt.expectCommand != "" {
				found := false
				for _, step := range steps {
					if step.Command == tt.expectCommand {
						found = true
						break
					}
				}
				if !found && steps[0].Command != "" {
					// Some steps don't have commands (manual steps)
					if steps[0].Command != tt.expectCommand {
						t.Errorf("expected command %q in steps", tt.expectCommand)
					}
				}
			}
		})
	}
}

func TestDetectPodIssues(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	tests := []struct {
		name     string
		status   *PodStatus
		expected int // number of issues
	}{
		{
			name: "healthy pod",
			status: &PodStatus{
				Name:      "healthy-pod",
				Namespace: "default",
				Phase:     "Running",
				Ready:     true,
			},
			expected: 0,
		},
		{
			name: "failed pod",
			status: &PodStatus{
				Name:      "failed-pod",
				Namespace: "default",
				Phase:     "Failed",
			},
			expected: 1,
		},
		{
			name: "pending pod",
			status: &PodStatus{
				Name:      "pending-pod",
				Namespace: "default",
				Phase:     "Pending",
			},
			expected: 1,
		},
		{
			name: "crash loop",
			status: &PodStatus{
				Name:      "crash-pod",
				Namespace: "default",
				Phase:     "Running",
				ContainerStates: []ContainerState{
					{
						Name:   "main",
						State:  "waiting",
						Reason: "CrashLoopBackOff",
					},
				},
			},
			expected: 1,
		},
		{
			name: "image pull error",
			status: &PodStatus{
				Name:      "image-pod",
				Namespace: "default",
				Phase:     "Pending",
				ContainerStates: []ContainerState{
					{
						Name:   "main",
						State:  "waiting",
						Reason: "ImagePullBackOff",
					},
				},
			},
			expected: 2, // Pending + ImagePull
		},
		{
			name: "high restart count",
			status: &PodStatus{
				Name:      "restart-pod",
				Namespace: "default",
				Phase:     "Running",
				ContainerStates: []ContainerState{
					{
						Name:         "main",
						State:        "running",
						RestartCount: 10,
					},
				},
			},
			expected: 1,
		},
		{
			name: "OOM killed",
			status: &PodStatus{
				Name:      "oom-pod",
				Namespace: "default",
				Phase:     "Running",
				ContainerStates: []ContainerState{
					{
						Name:   "main",
						State:  "terminated",
						Reason: "OOMKilled",
					},
				},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := dm.detectPodIssues(tt.status)
			if len(issues) != tt.expected {
				t.Errorf("detectPodIssues() returned %d issues, want %d", len(issues), tt.expected)
				for _, issue := range issues {
					t.Logf("  Issue: %s - %s", issue.Category, issue.Message)
				}
			}
		})
	}
}

func TestDetectDeploymentIssues(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	tests := []struct {
		name     string
		status   *DeploymentStatus
		expected int
	}{
		{
			name: "healthy deployment",
			status: &DeploymentStatus{
				Name:          "healthy-deploy",
				Namespace:     "default",
				Replicas:      3,
				ReadyReplicas: 3,
			},
			expected: 0,
		},
		{
			name: "unavailable replicas",
			status: &DeploymentStatus{
				Name:                "partial-deploy",
				Namespace:           "default",
				Replicas:            3,
				ReadyReplicas:       2,
				UnavailableReplicas: 1,
			},
			expected: 1,
		},
		{
			name: "no ready replicas",
			status: &DeploymentStatus{
				Name:          "failed-deploy",
				Namespace:     "default",
				Replicas:      3,
				ReadyReplicas: 0,
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := dm.detectDeploymentIssues(tt.status)
			if len(issues) != tt.expected {
				t.Errorf("detectDeploymentIssues() returned %d issues, want %d", len(issues), tt.expected)
			}
		})
	}
}

func TestDetectNodeIssues(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	tests := []struct {
		name     string
		status   *NodeStatus
		expected int
	}{
		{
			name: "healthy node",
			status: &NodeStatus{
				Name:             "healthy-node",
				Ready:            true,
				NetworkAvailable: true,
			},
			expected: 0,
		},
		{
			name: "not ready",
			status: &NodeStatus{
				Name:             "not-ready-node",
				Ready:            false,
				NetworkAvailable: true,
			},
			expected: 1,
		},
		{
			name: "memory pressure",
			status: &NodeStatus{
				Name:             "pressure-node",
				Ready:            true,
				MemoryPressure:   true,
				NetworkAvailable: true,
			},
			expected: 1,
		},
		{
			name: "multiple issues",
			status: &NodeStatus{
				Name:             "bad-node",
				Ready:            false,
				MemoryPressure:   true,
				DiskPressure:     true,
				NetworkAvailable: false,
			},
			expected: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := dm.detectNodeIssues(tt.status)
			if len(issues) != tt.expected {
				t.Errorf("detectNodeIssues() returned %d issues, want %d", len(issues), tt.expected)
			}
		})
	}
}

func TestParseAndAnalyzeLogs(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	logs := `2024-01-01 10:00:00 INFO Starting application
2024-01-01 10:00:01 INFO Connected to database
2024-01-01 10:00:02 WARN High memory usage detected
2024-01-01 10:00:03 ERROR Failed to process request
2024-01-01 10:00:04 FATAL Application crashed`

	entries := dm.parseAndAnalyzeLogs(logs, "test-pod")

	if len(entries) != 5 {
		t.Errorf("expected 5 log entries, got %d", len(entries))
	}

	// Count errors
	errorCount := 0
	for _, entry := range entries {
		if entry.IsError {
			errorCount++
		}
	}

	if errorCount != 2 {
		t.Errorf("expected 2 error entries, got %d", errorCount)
	}
}

func TestHealthCheckerCalculateOverallScore(t *testing.T) {
	hc := NewHealthChecker(&mockK8sClient{}, false)

	tests := []struct {
		name     string
		summary  *ClusterHealthSummary
		minScore int
		maxScore int
	}{
		{
			name: "healthy cluster",
			summary: &ClusterHealthSummary{
				NodeHealth:     ComponentHealth{Score: 100},
				WorkloadHealth: ComponentHealth{Score: 100},
				StorageHealth:  ComponentHealth{Score: 100},
				NetworkHealth:  ComponentHealth{Score: 100},
			},
			minScore: 95,
			maxScore: 100,
		},
		{
			name: "degraded cluster",
			summary: &ClusterHealthSummary{
				NodeHealth:     ComponentHealth{Score: 80},
				WorkloadHealth: ComponentHealth{Score: 70},
				StorageHealth:  ComponentHealth{Score: 90},
				NetworkHealth:  ComponentHealth{Score: 85},
				WarningIssues:  5,
			},
			minScore: 70,
			maxScore: 85,
		},
		{
			name: "critical cluster",
			summary: &ClusterHealthSummary{
				NodeHealth:     ComponentHealth{Score: 50},
				WorkloadHealth: ComponentHealth{Score: 30},
				StorageHealth:  ComponentHealth{Score: 60},
				NetworkHealth:  ComponentHealth{Score: 40},
				CriticalIssues: 5,
			},
			minScore: 15,
			maxScore: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := hc.calculateOverallScore(tt.summary)
			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("calculateOverallScore() = %d, want between %d and %d", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestGenerateSuggestionsFromIssues(t *testing.T) {
	hc := NewHealthChecker(&mockK8sClient{}, false)

	issues := []Issue{
		{Category: CategoryCrash, Suggestions: []string{"Check logs"}},
		{Category: CategoryImagePull},
		{Category: CategoryResourceLimit},
	}

	suggestions := hc.generateSuggestionsFromIssues(issues)

	if len(suggestions) == 0 {
		t.Error("expected suggestions to be generated")
	}

	// Should include category-specific suggestions
	foundCrashSuggestion := false
	foundImageSuggestion := false
	for _, s := range suggestions {
		if s == "Check logs" || s == "Review pod logs for crash details" {
			foundCrashSuggestion = true
		}
		if s == "Verify image name and tag are correct" || s == "Check image pull secrets" {
			foundImageSuggestion = true
		}
	}

	if !foundCrashSuggestion {
		t.Error("expected crash-related suggestion")
	}
	if !foundImageSuggestion {
		t.Error("expected image pull suggestion")
	}
}

func TestParsePodJSON(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	podJSON := `{
		"metadata": {
			"name": "test-pod",
			"namespace": "default",
			"labels": {"app": "test"}
		},
		"spec": {
			"nodeName": "worker-1"
		},
		"status": {
			"phase": "Running",
			"conditions": [
				{"type": "Ready", "status": "True"}
			],
			"containerStatuses": [
				{
					"name": "main",
					"ready": true,
					"restartCount": 0,
					"state": {"running": {"startedAt": "2024-01-01T00:00:00Z"}}
				}
			]
		}
	}`

	status, err := dm.parsePodJSON([]byte(podJSON))
	if err != nil {
		t.Fatalf("parsePodJSON failed: %v", err)
	}

	if status.Name != "test-pod" {
		t.Errorf("Name = %v, want test-pod", status.Name)
	}
	if status.Namespace != "default" {
		t.Errorf("Namespace = %v, want default", status.Namespace)
	}
	if status.Phase != "Running" {
		t.Errorf("Phase = %v, want Running", status.Phase)
	}
	if !status.Ready {
		t.Error("expected pod to be ready")
	}
	if status.NodeName != "worker-1" {
		t.Errorf("NodeName = %v, want worker-1", status.NodeName)
	}
}

func TestParseDeploymentJSON(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	deployJSON := `{
		"metadata": {
			"name": "test-deploy",
			"namespace": "production"
		},
		"spec": {
			"replicas": 3
		},
		"status": {
			"replicas": 3,
			"readyReplicas": 3,
			"availableReplicas": 3,
			"updatedReplicas": 3,
			"conditions": [
				{"type": "Available", "status": "True"}
			]
		}
	}`

	status, err := dm.parseDeploymentJSON([]byte(deployJSON))
	if err != nil {
		t.Fatalf("parseDeploymentJSON failed: %v", err)
	}

	if status.Name != "test-deploy" {
		t.Errorf("Name = %v, want test-deploy", status.Name)
	}
	if status.Replicas != 3 {
		t.Errorf("Replicas = %v, want 3", status.Replicas)
	}
	if status.ReadyReplicas != 3 {
		t.Errorf("ReadyReplicas = %v, want 3", status.ReadyReplicas)
	}
}

func TestParseNodeJSON(t *testing.T) {
	dm := NewDiagnosticsManager(&mockK8sClient{}, false)

	nodeJSON := `{
		"metadata": {
			"name": "worker-1"
		},
		"status": {
			"conditions": [
				{"type": "Ready", "status": "True"},
				{"type": "MemoryPressure", "status": "False"},
				{"type": "DiskPressure", "status": "False"}
			],
			"allocatable": {
				"cpu": "4",
				"memory": "8Gi",
				"pods": "110"
			},
			"capacity": {
				"cpu": "4",
				"memory": "8Gi",
				"pods": "110"
			}
		}
	}`

	status, err := dm.parseNodeJSON([]byte(nodeJSON))
	if err != nil {
		t.Fatalf("parseNodeJSON failed: %v", err)
	}

	if status.Name != "worker-1" {
		t.Errorf("Name = %v, want worker-1", status.Name)
	}
	if !status.Ready {
		t.Error("expected node to be ready")
	}
	if status.MemoryPressure {
		t.Error("expected no memory pressure")
	}
	if status.Allocatable.CPU != "4" {
		t.Errorf("Allocatable.CPU = %v, want 4", status.Allocatable.CPU)
	}
}

func TestGenerateRemediationPlan(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	report := &DiagnosticReport{
		ResourceType: "pod",
		ResourceName: "crashing-pod",
		Namespace:    "default",
		Issues: []Issue{
			{
				Category:     CategoryCrash,
				Severity:     SeverityCritical,
				ResourceName: "crashing-pod",
				Message:      "Pod is crash looping",
			},
		},
	}

	plan := agent.generateRemediationPlan(report)

	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) == 0 {
		t.Error("expected remediation steps")
	}

	// Check that steps have order numbers
	for _, step := range plan.Steps {
		if step.Order == 0 {
			t.Error("expected step to have order > 0")
		}
	}
}

func TestGenerateRemediationPlanNoIssues(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	report := &DiagnosticReport{
		ResourceType: "pod",
		ResourceName: "healthy-pod",
		Namespace:    "default",
		Issues:       []Issue{},
	}

	plan := agent.generateRemediationPlan(report)

	if !containsString(plan.Summary, "no") && !containsString(plan.Summary, "No") {
		t.Errorf("expected summary to mention no issues, got: %s", plan.Summary)
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsSubstringHelper(s, substr))
}

func containsSubstringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
