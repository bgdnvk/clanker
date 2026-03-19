package deploy

import "testing"

func TestClassifyIssue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Noise patterns
		{"empty string", "", "noise"},
		{"whitespace only", "   ", "noise"},
		{"disregard", "Please disregard the above note", "noise"},
		{"actually correct", "The configuration is actually correct", "noise"},
		{"re-check", "Let me re-check the parameters", "noise"},
		{"cloudfront websocket noise", "CloudFront does not support WebSocket connections", "noise"},
		{"iam malformed noise", "IAM policy ARN is malformed: arn:aws:iam::aws:policy/ReadOnlyAccess", "noise"},

		// Context patterns
		{"if conditional", "if the region supports it, this will work", "context"},
		{"depends", "This depends on the VPC being ready", "context"},
		{"worth verifying", "It is worth verifying the subnet CIDR", "context"},
		{"may be", "The instance type may be unavailable in us-west-1", "context"},
		{"might", "The security group might need additional rules", "context"},

		// Hard patterns (default)
		{"missing resource", "The VPC is missing a subnet", "hard"},
		{"plain statement", "The security group allows all inbound traffic", "hard"},
		{"critical failure", "The deployment will fail without proper IAM roles", "hard"},
		{"error in config", "There is an error in the launch template configuration", "hard"},
		{"invalid parameter", "The instance type t2.nano is invalid for this region", "hard"},
		{"not found", "Subnet not found in the specified AZ", "hard"},
		{"access denied", "Access denied for the IAM role", "hard"},
		{"wrong type", "Wrong instance type specified", "hard"},
		{"mismatch", "CIDR mismatch between VPC and subnet", "hard"},
		{"required field", "The required field InstanceType is empty", "hard"},

		// Edge case: issue has BOTH hard and context patterns
		// Hard patterns are now checked before context patterns, so this
		// is correctly classified as hard (contains "fail").
		{"hard with if prefix", "if the security group doesn't exist, the deploy will fail", "hard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyIssue(tt.input)
			if got != tt.want {
				t.Errorf("classifyIssue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClassifyFix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Noise patterns
		{"empty string", "", "noise"},
		{"whitespace only", "   ", "noise"},
		{"disregard", "disregard the previous fix suggestion", "noise"},

		// Context patterns
		{"consider", "Consider adding a health check endpoint", "context"},
		{"verify", "Verify the subnet is in the correct AZ", "context"},

		// Hard patterns (default)
		{"add security group", "Add a security group rule for port 443", "hard"},
		{"change instance type", "Change the instance type to t3.medium", "hard"},
		{"remove duplicate", "Remove the duplicate IAM policy attachment", "hard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFix(tt.input)
			if got != tt.want {
				t.Errorf("classifyFix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTriageValidationForRepair(t *testing.T) {
	t.Run("nil validation", func(t *testing.T) {
		result := TriageValidationForRepair(nil)
		if !result.Hard.IsValid {
			t.Error("expected Hard.IsValid to be true for nil input")
		}
	})

	t.Run("mixed issues", func(t *testing.T) {
		v := &PlanValidation{
			IsValid: false,
			Issues: []string{
				"The VPC is missing a subnet",
				"Please disregard this note",
				"if the region supports it",
			},
			Fixes: []string{
				"Add a subnet to the VPC",
				"Consider verifying the region",
			},
			Warnings: []string{
				"CloudFront does not support WebSocket connections",
				"Security group allows all inbound traffic",
			},
		}

		result := TriageValidationForRepair(v)

		if result.Hard.IsValid {
			t.Error("expected Hard.IsValid to be false when there are hard issues")
		}
		if len(result.Hard.Issues) != 1 {
			t.Errorf("expected 1 hard issue, got %d", len(result.Hard.Issues))
		}
		if len(result.Hard.Fixes) != 1 {
			t.Errorf("expected 1 hard fix, got %d", len(result.Hard.Fixes))
		}
		if len(result.LikelyNoise) != 2 {
			t.Errorf("expected 2 noise items, got %d", len(result.LikelyNoise))
		}
		if len(result.ContextNeeded) != 2 {
			t.Errorf("expected 2 context items, got %d", len(result.ContextNeeded))
		}
	})

	t.Run("all hard issues", func(t *testing.T) {
		v := &PlanValidation{
			IsValid: false,
			Issues:  []string{"Missing IAM role", "Invalid security group"},
		}

		result := TriageValidationForRepair(v)
		if result.Hard.IsValid {
			t.Error("expected Hard.IsValid to be false")
		}
		if len(result.Hard.Issues) != 2 {
			t.Errorf("expected 2 hard issues, got %d", len(result.Hard.Issues))
		}
	})

	t.Run("all noise", func(t *testing.T) {
		v := &PlanValidation{
			IsValid: false,
			Issues:  []string{"disregard this", "actually correct"},
		}

		result := TriageValidationForRepair(v)
		if !result.Hard.IsValid {
			t.Error("expected Hard.IsValid to be true when all issues are noise")
		}
		if len(result.LikelyNoise) != 2 {
			t.Errorf("expected 2 noise items, got %d", len(result.LikelyNoise))
		}
	})
}
