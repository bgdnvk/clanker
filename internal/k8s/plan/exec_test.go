package plan

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyBindings(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		bindings map[string]string
		expected []string
	}{
		{
			name:     "no placeholders",
			args:     []string{"kubectl", "get", "pods"},
			bindings: map[string]string{},
			expected: []string{"kubectl", "get", "pods"},
		},
		{
			name:     "single placeholder",
			args:     []string{"--name", "<CLUSTER_NAME>"},
			bindings: map[string]string{"CLUSTER_NAME": "test-cluster"},
			expected: []string{"--name", "test-cluster"},
		},
		{
			name:     "multiple placeholders",
			args:     []string{"--vpc-id", "<VPC_ID>", "--subnet-id", "<SUBNET_ID>"},
			bindings: map[string]string{"VPC_ID": "vpc-12345", "SUBNET_ID": "subnet-67890"},
			expected: []string{"--vpc-id", "vpc-12345", "--subnet-id", "subnet-67890"},
		},
		{
			name:     "placeholder not in bindings",
			args:     []string{"--id", "<UNKNOWN_ID>"},
			bindings: map[string]string{"OTHER_ID": "value"},
			expected: []string{"--id", "<UNKNOWN_ID>"},
		},
		{
			name:     "multiple placeholders in one arg",
			args:     []string{"<CLUSTER_NAME>-<REGION>"},
			bindings: map[string]string{"CLUSTER_NAME": "prod", "REGION": "us-west-2"},
			expected: []string{"prod-us-west-2"},
		},
		{
			name:     "empty bindings value",
			args:     []string{"--name", "<NAME>"},
			bindings: map[string]string{"NAME": ""},
			expected: []string{"--name", "<NAME>"},
		},
		{
			name:     "empty args",
			args:     []string{},
			bindings: map[string]string{"KEY": "value"},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyBindings(tt.args, tt.bindings)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d args, got %d", len(tt.expected), len(result))
				return
			}
			for i, exp := range tt.expected {
				if result[i] != exp {
					t.Errorf("arg[%d]: expected %q, got %q", i, exp, result[i])
				}
			}
		})
	}
}

func TestApplyBindingsToString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		bindings map[string]string
		expected string
	}{
		{
			name:     "no placeholder",
			input:    "simple string",
			bindings: map[string]string{},
			expected: "simple string",
		},
		{
			name:     "single placeholder",
			input:    "cluster-<CLUSTER_NAME>",
			bindings: map[string]string{"CLUSTER_NAME": "prod"},
			expected: "cluster-prod",
		},
		{
			name:     "multiple placeholders",
			input:    "<VPC_ID>/subnets/<SUBNET_ID>",
			bindings: map[string]string{"VPC_ID": "vpc-123", "SUBNET_ID": "subnet-456"},
			expected: "vpc-123/subnets/subnet-456",
		},
		{
			name:     "placeholder with underscore and number",
			input:    "instance-<INSTANCE_1_ID>",
			bindings: map[string]string{"INSTANCE_1_ID": "i-abc123"},
			expected: "instance-i-abc123",
		},
		{
			name:     "unresolved placeholder",
			input:    "<UNKNOWN>",
			bindings: map[string]string{},
			expected: "<UNKNOWN>",
		},
		{
			name:     "empty string",
			input:    "",
			bindings: map[string]string{"KEY": "value"},
			expected: "",
		},
		{
			name:     "case sensitive",
			input:    "<cluster_name>",
			bindings: map[string]string{"CLUSTER_NAME": "test"},
			expected: "<cluster_name>", // lowercase not matched
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyBindingsToString(tt.input, tt.bindings)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestLearnBindingsFromOutput(t *testing.T) {
	tests := []struct {
		name            string
		produces        map[string]string
		output          string
		expectedBindings map[string]string
	}{
		{
			name: "single binding",
			produces: map[string]string{
				"VPC_ID": "VpcId: ",
			},
			output: "Creating VPC...\nVpcId: vpc-12345\nDone",
			expectedBindings: map[string]string{
				"VPC_ID": "vpc-12345",
			},
		},
		{
			name: "multiple bindings",
			produces: map[string]string{
				"VPC_ID":    "VpcId: ",
				"SUBNET_ID": "SubnetId: ",
			},
			output: "VpcId: vpc-12345\nSubnetId: subnet-67890",
			expectedBindings: map[string]string{
				"VPC_ID":    "vpc-12345",
				"SUBNET_ID": "subnet-67890",
			},
		},
		{
			name: "pattern not found",
			produces: map[string]string{
				"VPC_ID": "VpcId: ",
			},
			output:           "No VPC created",
			expectedBindings: map[string]string{},
		},
		{
			name: "empty output",
			produces: map[string]string{
				"KEY": "pattern",
			},
			output:           "",
			expectedBindings: map[string]string{},
		},
		{
			name:             "empty produces",
			produces:        map[string]string{},
			output:           "Some output",
			expectedBindings: map[string]string{},
		},
		{
			name: "value with spaces",
			produces: map[string]string{
				"ARN": "ClusterArn: ",
			},
			output: "ClusterArn: arn:aws:eks:us-west-2:123456789:cluster/test",
			expectedBindings: map[string]string{
				"ARN": "arn:aws:eks:us-west-2:123456789:cluster/test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := make(map[string]string)
			learnBindingsFromOutput(tt.produces, tt.output, bindings)

			if len(bindings) != len(tt.expectedBindings) {
				t.Errorf("expected %d bindings, got %d", len(tt.expectedBindings), len(bindings))
			}
			for k, v := range tt.expectedBindings {
				if bindings[k] != v {
					t.Errorf("binding[%s]: expected %q, got %q", k, v, bindings[k])
				}
			}
		})
	}
}

func TestFormatCommandForLog(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		args     []string
		expected string
	}{
		{
			name:     "short command",
			cmd:      "kubectl",
			args:     []string{"get", "pods"},
			expected: "get pods",
		},
		{
			name:     "empty args",
			cmd:      "kubectl",
			args:     []string{},
			expected: "",
		},
		{
			name:     "long command truncated",
			cmd:      "aws",
			args:     []string{"ec2", "run-instances", "--image-id", "ami-12345", "--instance-type", "t3.medium", "--key-name", "my-key", "--security-group-ids", "sg-12345", "--subnet-id", "subnet-67890", "--user-data", "some very long user data that will definitely exceed the maximum length limit for command logging"},
			expected: "ec2 run-instances --image-id ami-12345 --instance-type t3.medium --key-name my-key --security-group-ids sg-12345 --subnet-id subnet-67890 --user-d...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatCommandForLog(tt.cmd, tt.args)
			if tt.name == "long command truncated" {
				if len(result) > 153 { // 150 + "..."
					t.Errorf("result should be truncated, got length %d", len(result))
				}
				if !strings.HasSuffix(result, "...") {
					t.Error("truncated result should end with ...")
				}
			} else {
				if result != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, result)
				}
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("could not get home directory: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "tilde path",
			input:    "~/.kube/config",
			expected: home + "/.kube/config",
		},
		{
			name:     "absolute path",
			input:    "/etc/kubernetes/admin.conf",
			expected: "/etc/kubernetes/admin.conf",
		},
		{
			name:     "relative path",
			input:    "./config",
			expected: "./config",
		},
		{
			name:     "tilde only",
			input:    "~/",
			expected: home + "/",
		},
		{
			name:     "no tilde",
			input:    "some/path",
			expected: "some/path",
		},
		{
			name:     "tilde in middle",
			input:    "/home/~/file",
			expected: "/home/~/file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandPath(tt.input)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestExecuteNilPlan(t *testing.T) {
	result, err := Execute(context.Background(), nil, ExecOptions{}, nil)
	if err == nil {
		t.Error("expected error for nil plan")
	}
	if result != nil {
		t.Error("expected nil result for nil plan")
	}
}

func TestExecuteEmptyPlan(t *testing.T) {
	plan := &K8sPlan{
		Version:     1,
		CreatedAt:   time.Now(),
		ClusterName: "test",
		Region:      "us-west-2",
		Profile:     "default",
		Steps:       []Step{},
	}

	var output strings.Builder
	result, err := Execute(context.Background(), plan, ExecOptions{
		Profile: "test",
		Region:  "us-west-2",
	}, &output)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success for empty plan")
	}
	if result.Connection == nil {
		t.Error("expected connection info")
	}
}

func TestExecuteDryRun(t *testing.T) {
	plan := &K8sPlan{
		Version:     1,
		CreatedAt:   time.Now(),
		ClusterName: "test-cluster",
		Region:      "us-west-2",
		Profile:     "default",
		Steps: []Step{
			{
				ID:          "step-1",
				Description: "Run test command",
				Command:     "echo",
				Args:        []string{"hello", "world"},
			},
		},
	}

	var output strings.Builder
	result, err := Execute(context.Background(), plan, ExecOptions{
		Profile: "test",
		Region:  "us-west-2",
		DryRun:  true,
	}, &output)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success for dry run")
	}
}

func TestBuildConnectionInfo(t *testing.T) {
	plan := &K8sPlan{
		ClusterName: "test-cluster",
	}
	bindings := map[string]string{
		"CLUSTER_ENDPOINT": "https://cluster.example.com",
	}

	conn := buildConnectionInfo(plan, bindings)

	if conn == nil {
		t.Fatal("expected connection info")
	}
	if conn.Endpoint != "https://cluster.example.com" {
		t.Errorf("expected endpoint 'https://cluster.example.com', got %q", conn.Endpoint)
	}
	if len(conn.Commands) != 2 {
		t.Errorf("expected 2 default commands, got %d", len(conn.Commands))
	}
}

func TestBuildConnectionInfoNoEndpoint(t *testing.T) {
	plan := &K8sPlan{
		ClusterName: "test-cluster",
	}
	bindings := map[string]string{}

	conn := buildConnectionInfo(plan, bindings)

	if conn == nil {
		t.Fatal("expected connection info")
	}
	if conn.Endpoint != "" {
		t.Errorf("expected empty endpoint, got %q", conn.Endpoint)
	}
}

func TestPlaceholderRegex(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"<CLUSTER_NAME>", []string{"<CLUSTER_NAME>"}},
		{"<VPC_ID> and <SUBNET_ID>", []string{"<VPC_ID>", "<SUBNET_ID>"}},
		{"<INSTANCE_1>", []string{"<INSTANCE_1>"}},
		{"no placeholder", nil},
		{"<lowercase>", nil}, // lowercase not matched
		{"<MIXED_123_ID>", []string{"<MIXED_123_ID>"}},
	}

	for _, tt := range tests {
		matches := placeholderRe.FindAllString(tt.input, -1)
		if len(matches) != len(tt.expected) {
			t.Errorf("input %q: expected %d matches, got %d", tt.input, len(tt.expected), len(matches))
			continue
		}
		for i, exp := range tt.expected {
			if matches[i] != exp {
				t.Errorf("input %q: match[%d] expected %q, got %q", tt.input, i, exp, matches[i])
			}
		}
	}
}
