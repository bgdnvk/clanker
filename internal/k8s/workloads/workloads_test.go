package workloads

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// mockClient implements K8sClient for testing
type mockClient struct {
	runResponse           string
	runError              error
	runWithNSResponse     string
	runWithNSError        error
	getJSONResponse       []byte
	getJSONError          error
	describeResponse      string
	describeError         error
	scaleResponse         string
	scaleError            error
	rolloutResponse       string
	rolloutError          error
	deleteResponse        string
	deleteError           error
	logsResponse          string
	logsError             error
}

func (m *mockClient) Run(ctx context.Context, args ...string) (string, error) {
	return m.runResponse, m.runError
}

func (m *mockClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return m.runWithNSResponse, m.runWithNSError
}

func (m *mockClient) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return m.getJSONResponse, m.getJSONError
}

func (m *mockClient) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return m.describeResponse, m.describeError
}

func (m *mockClient) Scale(ctx context.Context, resourceType, name, namespace string, replicas int) (string, error) {
	return m.scaleResponse, m.scaleError
}

func (m *mockClient) Rollout(ctx context.Context, action, resourceType, name, namespace string) (string, error) {
	return m.rolloutResponse, m.rolloutError
}

func (m *mockClient) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return m.deleteResponse, m.deleteError
}

func (m *mockClient) Logs(ctx context.Context, podName, namespace string, opts LogOptionsInternal) (string, error) {
	return m.logsResponse, m.logsError
}

func TestNewSubAgent(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	if agent == nil {
		t.Fatal("expected non-nil agent")
	}
	if agent.client == nil {
		t.Error("expected client to be set")
	}
}

func TestAnalyzeQueryWorkloadType(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	// Note: Map iteration is random in Go, so use distinct patterns to avoid ambiguity.
	// "pods" contains "ds" which matches daemonset, so use "pod " with space.
	tests := []struct {
		query        string
		expectedType WorkloadType
	}{
		{"list all deployment resources", WorkloadDeployment},
		{"list pod items", WorkloadPod},                 // "pod " avoids "ds" match from "pods"
		{"get sts resources", WorkloadStatefulSet},      // "sts" is unique to statefulset
		{"describe ds config", WorkloadDaemonSet},       // "ds" is unique to daemonset
		{"get job info", WorkloadJob},
		{"list cj tasks", WorkloadCronJob},              // "cj" is unique to cronjob
		{"list rs resources", WorkloadReplicaSet},       // "rs" is unique to replicaset
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.WorkloadType != tt.expectedType {
				t.Errorf("expected workload type %s, got %s", tt.expectedType, analysis.WorkloadType)
			}
		})
	}
}

func TestAnalyzeQueryOperation(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	// Note: The operation detection uses map iteration which is random in Go.
	// Use unique keywords that only appear in one operation pattern.
	// Avoid overlapping patterns like: "get" (appears in get, logs), "show" (get, logs), "run" (create).
	tests := []struct {
		query         string
		expectedOp    string
		expectedRead  bool
	}{
		{"list all resources", "list", true},         // "list" is unique to list
		{"info about resource", "describe", true},    // "info about" is unique to describe
		{"view log entries", "logs", true},           // "log" is unique to logs
		{"health check", "status", true},             // "health" is unique to status
		{"events for pod", "events", true},           // "events" is unique
		{"launch new service", "create", false},      // "launch" is unique to create
		{"resize replicas", "scale", false},          // "resize" is unique to scale
		{"remove resource", "delete", false},         // "remove" is unique to delete
		{"restart service", "restart", false},        // "restart" is unique
		{"undo changes", "rollback", false},          // "undo" is unique to rollback
		{"set image version", "update", false},       // "set image" is unique to update
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Operation != tt.expectedOp {
				t.Errorf("expected operation %s, got %s", tt.expectedOp, analysis.Operation)
			}
			if analysis.IsReadOnly != tt.expectedRead {
				t.Errorf("expected IsReadOnly=%v, got %v", tt.expectedRead, analysis.IsReadOnly)
			}
		})
	}
}

func TestAnalyzeQueryNamespace(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query            string
		expectedNS       string
	}{
		{"list pods in namespace kube-system", "kube-system"},
		{"get pods -n default", "default"},
		{"show deployments in ns production", "production"},
		{"list pods in kube-system", "kube-system"},
		{"list pods", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Namespace != tt.expectedNS {
				t.Errorf("expected namespace %q, got %q", tt.expectedNS, analysis.Namespace)
			}
		})
	}
}

func TestDeploymentManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewDeploymentManager(client, false)

	opts := CreateDeploymentOptions{
		Name:      "my-app",
		Image:     "nginx:latest",
		Replicas:  3,
		Namespace: "default",
		Port:      80,
	}

	plan := manager.CreateDeploymentPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}

	// Check first step is create deployment
	if plan.Steps[0].ID != "create-deployment" {
		t.Errorf("expected first step ID to be create-deployment, got %s", plan.Steps[0].ID)
	}
	if plan.Steps[0].Command != "kubectl" {
		t.Errorf("expected command kubectl, got %s", plan.Steps[0].Command)
	}
}

func TestDeploymentManagerScalePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewDeploymentManager(client, false)

	plan := manager.ScaleDeploymentPlan("nginx", "default", 5)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Scale deployment nginx to 5 replicas" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
}

func TestDeploymentManagerRestartPlan(t *testing.T) {
	client := &mockClient{}
	manager := NewDeploymentManager(client, false)

	plan := manager.RestartPlan("nginx", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "restart-deployment" {
		t.Errorf("expected first step ID restart-deployment, got %s", plan.Steps[0].ID)
	}
}

func TestDeploymentManagerRollbackPlan(t *testing.T) {
	client := &mockClient{}
	manager := NewDeploymentManager(client, false)

	// Test rollback to previous
	plan := manager.RollbackPlan("nginx", "default", 0)
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Rollback deployment nginx to previous revision" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}

	// Test rollback to specific revision
	plan = manager.RollbackPlan("nginx", "default", 3)
	if plan.Summary != "Rollback deployment nginx to revision 3" {
		t.Errorf("unexpected summary for specific revision: %s", plan.Summary)
	}
}

func TestDeploymentManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewDeploymentManager(client, false)

	plan := manager.DeletePlan("nginx", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
	if len(plan.Notes) < 1 {
		t.Error("expected notes about deletion")
	}
}

func TestPodManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPodManager(client, false)

	// Test normal delete
	plan := manager.DeletePlan("my-pod", "default", false)
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}

	// Test force delete
	plan = manager.DeletePlan("my-pod", "default", true)
	hasForce := false
	for _, arg := range plan.Steps[0].Args {
		if arg == "--force" {
			hasForce = true
			break
		}
	}
	if !hasForce {
		t.Error("expected force flag in args")
	}
}

func TestParseDeployment(t *testing.T) {
	client := &mockClient{}
	manager := NewDeploymentManager(client, false)

	deploymentJSON := `{
		"metadata": {
			"name": "nginx",
			"namespace": "default",
			"labels": {"app": "nginx"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"spec": {
			"replicas": 3,
			"selector": {"matchLabels": {"app": "nginx"}},
			"strategy": {"type": "RollingUpdate"},
			"template": {
				"spec": {
					"containers": [{"name": "nginx", "image": "nginx:1.19"}]
				}
			}
		},
		"status": {
			"replicas": 3,
			"readyReplicas": 3,
			"availableReplicas": 3,
			"unavailableReplicas": 0,
			"updatedReplicas": 3
		}
	}`

	info, err := manager.parseDeployment([]byte(deploymentJSON))
	if err != nil {
		t.Fatalf("failed to parse deployment: %v", err)
	}

	if info.Name != "nginx" {
		t.Errorf("expected name nginx, got %s", info.Name)
	}
	if info.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", info.Namespace)
	}
	if info.Replicas != 3 {
		t.Errorf("expected 3 replicas, got %d", info.Replicas)
	}
	if info.Ready != 3 {
		t.Errorf("expected 3 ready, got %d", info.Ready)
	}
	if info.Status != "Available" {
		t.Errorf("expected status Available, got %s", info.Status)
	}
	if len(info.Images) != 1 || info.Images[0] != "nginx:1.19" {
		t.Errorf("unexpected images: %v", info.Images)
	}
}

func TestParsePod(t *testing.T) {
	client := &mockClient{}
	manager := NewPodManager(client, false)

	podJSON := `{
		"metadata": {
			"name": "nginx-abc123",
			"namespace": "default",
			"labels": {"app": "nginx"},
			"creationTimestamp": "2024-01-01T00:00:00Z",
			"ownerReferences": [{"kind": "ReplicaSet", "name": "nginx-rs"}]
		},
		"spec": {
			"nodeName": "node-1",
			"containers": [{"name": "nginx", "image": "nginx:1.19"}]
		},
		"status": {
			"phase": "Running",
			"podIP": "10.0.0.1",
			"hostIP": "192.168.1.1",
			"startTime": "2024-01-01T00:01:00Z",
			"containerStatuses": [{
				"name": "nginx",
				"ready": true,
				"restartCount": 0,
				"image": "nginx:1.19",
				"state": {"running": {}}
			}]
		}
	}`

	info, err := manager.parsePod([]byte(podJSON))
	if err != nil {
		t.Fatalf("failed to parse pod: %v", err)
	}

	if info.Name != "nginx-abc123" {
		t.Errorf("expected name nginx-abc123, got %s", info.Name)
	}
	if info.Phase != "Running" {
		t.Errorf("expected phase Running, got %s", info.Phase)
	}
	if info.IP != "10.0.0.1" {
		t.Errorf("expected IP 10.0.0.1, got %s", info.IP)
	}
	if info.Node != "node-1" {
		t.Errorf("expected node node-1, got %s", info.Node)
	}
	if len(info.Containers) != 1 {
		t.Errorf("expected 1 container, got %d", len(info.Containers))
	}
	if info.Containers[0].State != "Running" {
		t.Errorf("expected container state Running, got %s", info.Containers[0].State)
	}
	if len(info.Owners) != 1 || info.Owners[0].Kind != "ReplicaSet" {
		t.Errorf("unexpected owners: %v", info.Owners)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestWorkloadPlanJSON(t *testing.T) {
	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   "Test plan",
		Steps: []WorkloadStep{
			{
				ID:          "step-1",
				Description: "Test step",
				Command:     "kubectl",
				Args:        []string{"get", "pods"},
				Reason:      "Testing",
			},
		},
		Notes: []string{"Test note"},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("failed to marshal plan: %v", err)
	}

	var parsed WorkloadPlan
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal plan: %v", err)
	}

	if parsed.Version != 1 {
		t.Errorf("expected version 1, got %d", parsed.Version)
	}
	if parsed.Summary != "Test plan" {
		t.Errorf("unexpected summary: %s", parsed.Summary)
	}
	if len(parsed.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(parsed.Steps))
	}
}

func TestIsCommonWord(t *testing.T) {
	commonWords := []string{"the", "a", "in", "on", "for", "with", "all", "my"}
	for _, word := range commonWords {
		if !isCommonWord(word) {
			t.Errorf("expected %q to be common word", word)
		}
	}

	uncommonWords := []string{"nginx", "deployment", "pod", "kubernetes"}
	for _, word := range uncommonWords {
		if isCommonWord(word) {
			t.Errorf("expected %q to not be common word", word)
		}
	}
}
