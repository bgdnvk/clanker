package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestK8sPlanToMakerPlan(t *testing.T) {
	now := time.Now()
	plan := &K8sPlan{
		Version:     1,
		CreatedAt:   now,
		Operation:   "create-cluster",
		ClusterType: "eks",
		ClusterName: "test-cluster",
		Region:      "us-west-2",
		Profile:     "default",
		Summary:     "Create an EKS cluster",
		Steps: []Step{
			{
				ID:          "step-1",
				Description: "Create VPC",
				Command:     "aws",
				Args:        []string{"ec2", "create-vpc", "--cidr-block", "10.0.0.0/16"},
				Reason:      "Create VPC for cluster",
				Produces: map[string]string{
					"VPC_ID": "VpcId",
				},
			},
			{
				ID:          "step-2",
				Description: "Create cluster",
				Command:     "eksctl",
				Args:        []string{"create", "cluster", "--name", "<CLUSTER_NAME>"},
			},
		},
		Notes: []string{"This is a test plan"},
	}

	question := "Create an EKS cluster"
	makerPlan := plan.ToMakerPlan(question)

	if makerPlan.Version != plan.Version {
		t.Errorf("expected version %d, got %d", plan.Version, makerPlan.Version)
	}
	if makerPlan.Question != question {
		t.Errorf("expected question %q, got %q", question, makerPlan.Question)
	}
	if makerPlan.Summary != plan.Summary {
		t.Errorf("expected summary %q, got %q", plan.Summary, makerPlan.Summary)
	}
	if len(makerPlan.Commands) != len(plan.Steps) {
		t.Errorf("expected %d commands, got %d", len(plan.Steps), len(makerPlan.Commands))
	}
	if len(makerPlan.Notes) != len(plan.Notes) {
		t.Errorf("expected %d notes, got %d", len(plan.Notes), len(makerPlan.Notes))
	}

	// Check first command
	if len(makerPlan.Commands) > 0 {
		cmd := makerPlan.Commands[0]
		expectedArgs := []string{"aws", "ec2", "create-vpc", "--cidr-block", "10.0.0.0/16"}
		if len(cmd.Args) != len(expectedArgs) {
			t.Errorf("expected %d args, got %d", len(expectedArgs), len(cmd.Args))
		}
		if cmd.Reason != "Create VPC for cluster" {
			t.Errorf("expected reason 'Create VPC for cluster', got %q", cmd.Reason)
		}
		if cmd.Produces["VPC_ID"] != "VpcId" {
			t.Errorf("expected Produces[VPC_ID]='VpcId', got %q", cmd.Produces["VPC_ID"])
		}
	}

	// Check second command uses description as reason when reason is empty
	if len(makerPlan.Commands) > 1 {
		cmd := makerPlan.Commands[1]
		if cmd.Reason != "Create cluster" {
			t.Errorf("expected reason 'Create cluster' (from description), got %q", cmd.Reason)
		}
	}
}

func TestK8sPlanToMakerPlanEmptySteps(t *testing.T) {
	plan := &K8sPlan{
		Version:     1,
		CreatedAt:   time.Now(),
		ClusterName: "test",
		Steps:       []Step{},
	}

	makerPlan := plan.ToMakerPlan("test question")

	if len(makerPlan.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(makerPlan.Commands))
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "test-cluster",
			expected: "test-cluster",
		},
		{
			name:     "name with slashes",
			input:    "my/cluster/name",
			expected: "my-cluster-name",
		},
		{
			name:     "name with backslashes",
			input:    "my\\cluster\\name",
			expected: "my-cluster-name",
		},
		{
			name:     "name with colons",
			input:    "cluster:prod:v1",
			expected: "cluster-prod-v1",
		},
		{
			name:     "name with special chars",
			input:    "test*?\"<>|cluster",
			expected: "test------cluster",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "all special chars",
			input:    "/*\\:?\"<>|",
			expected: "---------",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestK8sPlanSavePlan(t *testing.T) {
	// Create a temp directory for testing
	tempDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tempDir)

	plan := &K8sPlan{
		Version:     1,
		CreatedAt:   time.Now(),
		Operation:   "create-cluster",
		ClusterType: "eks",
		ClusterName: "test-cluster",
		Region:      "us-west-2",
		Profile:     "default",
		Summary:     "Test plan",
		Steps: []Step{
			{
				ID:          "step-1",
				Description: "Test step",
				Command:     "kubectl",
				Args:        []string{"get", "pods"},
			},
		},
		Notes: []string{"Test note"},
	}

	planPath, err := plan.SavePlan("Test question")
	if err != nil {
		t.Fatalf("SavePlan failed: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		t.Errorf("plan file was not created at %s", planPath)
	}

	// Verify file is in expected directory
	expectedDir := filepath.Join(tempDir, ".clanker", "plans")
	if !strings.HasPrefix(planPath, expectedDir) {
		t.Errorf("plan file %s is not in expected directory %s", planPath, expectedDir)
	}

	// Verify file contains valid JSON
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("failed to read plan file: %v", err)
	}

	var makerPlan MakerPlan
	if err := json.Unmarshal(data, &makerPlan); err != nil {
		t.Errorf("plan file contains invalid JSON: %v", err)
	}

	if makerPlan.Version != plan.Version {
		t.Errorf("saved plan version %d does not match %d", makerPlan.Version, plan.Version)
	}
	if makerPlan.Question != "Test question" {
		t.Errorf("saved plan question %q does not match 'Test question'", makerPlan.Question)
	}
}

func TestMakerPlanMarshal(t *testing.T) {
	mp := &MakerPlan{
		Version:   1,
		CreatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Question:  "Deploy nginx",
		Summary:   "Deploy nginx to cluster",
		Commands: []MakerCommand{
			{
				Args:   []string{"kubectl", "create", "deployment", "nginx", "--image=nginx"},
				Reason: "Create nginx deployment",
				Produces: map[string]string{
					"DEPLOYMENT_NAME": "nginx",
				},
			},
		},
		Notes: []string{"Deployment will be in default namespace"},
	}

	data, err := json.Marshal(mp)
	if err != nil {
		t.Fatalf("failed to marshal MakerPlan: %v", err)
	}

	// Verify required fields are present
	if !strings.Contains(string(data), `"version":1`) {
		t.Error("marshaled JSON missing version")
	}
	if !strings.Contains(string(data), `"question":"Deploy nginx"`) {
		t.Error("marshaled JSON missing question")
	}
	if !strings.Contains(string(data), `"commands":[`) {
		t.Error("marshaled JSON missing commands")
	}
}

func TestMakerPlanUnmarshal(t *testing.T) {
	jsonData := `{
		"version": 1,
		"createdAt": "2024-01-15T10:30:00Z",
		"question": "Scale deployment",
		"summary": "Scale nginx to 3 replicas",
		"commands": [
			{
				"args": ["kubectl", "scale", "deployment", "nginx", "--replicas=3"],
				"reason": "Scale deployment"
			}
		],
		"notes": ["Scaling complete"]
	}`

	var mp MakerPlan
	if err := json.Unmarshal([]byte(jsonData), &mp); err != nil {
		t.Fatalf("failed to unmarshal MakerPlan: %v", err)
	}

	if mp.Version != 1 {
		t.Errorf("expected version 1, got %d", mp.Version)
	}
	if mp.Question != "Scale deployment" {
		t.Errorf("expected question 'Scale deployment', got %q", mp.Question)
	}
	if len(mp.Commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(mp.Commands))
	}
	if len(mp.Notes) != 1 {
		t.Errorf("expected 1 note, got %d", len(mp.Notes))
	}
}

func TestK8sPlanMarshal(t *testing.T) {
	plan := &K8sPlan{
		Version:     1,
		CreatedAt:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Operation:   "deploy",
		ClusterType: "eks",
		ClusterName: "prod-cluster",
		Region:      "us-east-1",
		Profile:     "production",
		Summary:     "Deploy application",
		Steps: []Step{
			{
				ID:          "step-1",
				Description: "Apply manifest",
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "deployment.yaml"},
			},
		},
		Notes: []string{"Deployment manifest ready"},
		Connection: &Connection{
			Kubeconfig: "~/.kube/config",
			Endpoint:   "https://cluster.eks.amazonaws.com",
			Commands:   []string{"kubectl get pods"},
		},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("failed to marshal K8sPlan: %v", err)
	}

	// Verify required fields
	if !strings.Contains(string(data), `"operation":"deploy"`) {
		t.Error("marshaled JSON missing operation")
	}
	if !strings.Contains(string(data), `"clusterType":"eks"`) {
		t.Error("marshaled JSON missing clusterType")
	}
	if !strings.Contains(string(data), `"clusterName":"prod-cluster"`) {
		t.Error("marshaled JSON missing clusterName")
	}
	if !strings.Contains(string(data), `"connection":`) {
		t.Error("marshaled JSON missing connection")
	}
}

func TestK8sPlanUnmarshal(t *testing.T) {
	jsonData := `{
		"version": 1,
		"createdAt": "2024-01-15T10:30:00Z",
		"operation": "create-cluster",
		"clusterType": "kubeadm",
		"clusterName": "dev-cluster",
		"region": "us-west-2",
		"profile": "dev",
		"summary": "Create development cluster",
		"steps": [
			{
				"id": "step-1",
				"description": "Launch EC2 instance",
				"command": "aws",
				"args": ["ec2", "run-instances"],
				"waitFor": {
					"type": "instance-running",
					"resource": "<INSTANCE_ID>",
					"timeout": 600000000000,
					"interval": 30000000000
				}
			}
		],
		"notes": ["Development cluster"]
	}`

	var plan K8sPlan
	if err := json.Unmarshal([]byte(jsonData), &plan); err != nil {
		t.Fatalf("failed to unmarshal K8sPlan: %v", err)
	}

	if plan.Operation != "create-cluster" {
		t.Errorf("expected operation 'create-cluster', got %q", plan.Operation)
	}
	if plan.ClusterType != "kubeadm" {
		t.Errorf("expected clusterType 'kubeadm', got %q", plan.ClusterType)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].WaitFor == nil {
		t.Error("expected WaitFor to be populated")
	} else if plan.Steps[0].WaitFor.Type != "instance-running" {
		t.Errorf("expected WaitFor.Type 'instance-running', got %q", plan.Steps[0].WaitFor.Type)
	}
}

func TestStepWithSSHConfig(t *testing.T) {
	step := Step{
		ID:          "ssh-step",
		Description: "Bootstrap node",
		Command:     "ssh",
		Args:        []string{},
		SSHConfig: &SSHStepConfig{
			Host:       "<INSTANCE_IP>",
			User:       "ubuntu",
			KeyPath:    "~/.ssh/id_rsa",
			Script:     "sudo kubeadm init",
			ScriptName: "kubeadm-init",
		},
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("failed to marshal step: %v", err)
	}

	var unmarshaled Step
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal step: %v", err)
	}

	if unmarshaled.SSHConfig == nil {
		t.Fatal("expected SSHConfig to be populated")
	}
	if unmarshaled.SSHConfig.Host != "<INSTANCE_IP>" {
		t.Errorf("expected Host '<INSTANCE_IP>', got %q", unmarshaled.SSHConfig.Host)
	}
	if unmarshaled.SSHConfig.User != "ubuntu" {
		t.Errorf("expected User 'ubuntu', got %q", unmarshaled.SSHConfig.User)
	}
	if unmarshaled.SSHConfig.ScriptName != "kubeadm-init" {
		t.Errorf("expected ScriptName 'kubeadm-init', got %q", unmarshaled.SSHConfig.ScriptName)
	}
}

func TestExecOptions(t *testing.T) {
	opts := ExecOptions{
		Profile:    "test-profile",
		Region:     "eu-west-1",
		Debug:      true,
		DryRun:     false,
		SSHKeyPath: "~/.ssh/test_key",
	}

	if opts.Profile != "test-profile" {
		t.Errorf("expected Profile 'test-profile', got %q", opts.Profile)
	}
	if opts.Region != "eu-west-1" {
		t.Errorf("expected Region 'eu-west-1', got %q", opts.Region)
	}
	if !opts.Debug {
		t.Error("expected Debug to be true")
	}
	if opts.DryRun {
		t.Error("expected DryRun to be false")
	}
}

func TestExecResult(t *testing.T) {
	result := ExecResult{
		Success: true,
		Connection: &Connection{
			Kubeconfig: "~/.kube/config",
			Endpoint:   "https://cluster.example.com",
			Commands:   []string{"kubectl get nodes"},
		},
		Bindings: map[string]string{
			"CLUSTER_NAME": "test-cluster",
			"VPC_ID":       "vpc-12345",
		},
		Errors: nil,
	}

	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Connection == nil {
		t.Error("expected Connection to be populated")
	}
	if len(result.Bindings) != 2 {
		t.Errorf("expected 2 bindings, got %d", len(result.Bindings))
	}
	if result.Bindings["CLUSTER_NAME"] != "test-cluster" {
		t.Errorf("expected CLUSTER_NAME 'test-cluster', got %q", result.Bindings["CLUSTER_NAME"])
	}
}

func TestStepResult(t *testing.T) {
	result := StepResult{
		StepID:  "step-1",
		Success: true,
		Output:  "VpcId: vpc-12345\nSubnetId: subnet-67890",
		Error:   nil,
		Bindings: map[string]string{
			"VPC_ID":    "vpc-12345",
			"SUBNET_ID": "subnet-67890",
		},
	}

	if result.StepID != "step-1" {
		t.Errorf("expected StepID 'step-1', got %q", result.StepID)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if len(result.Bindings) != 2 {
		t.Errorf("expected 2 bindings, got %d", len(result.Bindings))
	}
}

func TestCurrentPlanVersion(t *testing.T) {
	if CurrentPlanVersion != 1 {
		t.Errorf("expected CurrentPlanVersion to be 1, got %d", CurrentPlanVersion)
	}
}
