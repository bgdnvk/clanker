package fixer

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

var planCounter int
var commandCounter int

// generatePlanID generates a unique plan ID
func generatePlanID() string {
	planCounter++
	return fmt.Sprintf("FIX-%d-%d", time.Now().Unix(), planCounter)
}

// generateCommandID generates a unique command ID
func generateCommandID() string {
	commandCounter++
	return fmt.Sprintf("CMD-%d-%d", time.Now().Unix(), commandCounter)
}

// extractRoleName extracts the role name from a role ARN
func extractRoleName(arn string) string {
	// arn:aws:iam::123456789012:role/role-name
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	// If not an ARN, assume it is the role name
	return arn
}

// extractUserName extracts the user name from a user ARN
func extractUserName(arn string) string {
	// arn:aws:iam::123456789012:user/user-name
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return arn
}

// suggestLeastPrivilegePolicy suggests a more restrictive policy document
func suggestLeastPrivilegePolicy(currentDoc string, problematicActions []string) string {
	var policy PolicyDocument
	if err := json.Unmarshal([]byte(currentDoc), &policy); err != nil {
		return currentDoc
	}

	// Build a map of problematic actions for quick lookup
	problematicMap := make(map[string]bool)
	for _, a := range problematicActions {
		problematicMap[strings.ToLower(a)] = true
	}

	// Process each statement
	for i := range policy.Statement {
		stmt := &policy.Statement[i]
		if stmt.Effect != "Allow" {
			continue
		}

		actions := toStringSlice(stmt.Action)
		var newActions []string

		for _, action := range actions {
			actionLower := strings.ToLower(action)

			// Replace full wildcard with comment
			if action == "*" {
				newActions = append(newActions, "# REVIEW: Replace * with specific actions")
				continue
			}

			// Replace service wildcard with specific actions
			if strings.HasSuffix(action, ":*") {
				service := strings.TrimSuffix(action, ":*")
				suggestion := suggestServiceActions(service)
				newActions = append(newActions, suggestion...)
				continue
			}

			// Keep non-problematic actions
			if !problematicMap[actionLower] {
				newActions = append(newActions, action)
			}
		}

		// Update statement actions
		if len(newActions) == 1 {
			stmt.Action = newActions[0]
		} else if len(newActions) > 1 {
			stmt.Action = newActions
		}
	}

	result, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return currentDoc
	}
	return string(result)
}

// suggestServiceActions suggests common read-only actions for a service
func suggestServiceActions(service string) []string {
	service = strings.ToLower(service)

	readOnlyActions := map[string][]string{
		"s3": {
			"s3:GetObject",
			"s3:GetObjectVersion",
			"s3:GetBucketLocation",
			"s3:ListBucket",
		},
		"ec2": {
			"ec2:Describe*",
		},
		"iam": {
			"iam:Get*",
			"iam:List*",
		},
		"lambda": {
			"lambda:GetFunction",
			"lambda:ListFunctions",
			"lambda:GetFunctionConfiguration",
		},
		"dynamodb": {
			"dynamodb:GetItem",
			"dynamodb:Query",
			"dynamodb:Scan",
			"dynamodb:DescribeTable",
		},
		"sqs": {
			"sqs:GetQueueAttributes",
			"sqs:GetQueueUrl",
			"sqs:ReceiveMessage",
		},
		"sns": {
			"sns:GetTopicAttributes",
			"sns:ListTopics",
		},
		"logs": {
			"logs:Describe*",
			"logs:Get*",
			"logs:FilterLogEvents",
		},
		"cloudwatch": {
			"cloudwatch:Describe*",
			"cloudwatch:Get*",
			"cloudwatch:List*",
		},
	}

	if actions, ok := readOnlyActions[service]; ok {
		return actions
	}

	// Default suggestion for unknown services
	return []string{
		fmt.Sprintf("%s:Get*", service),
		fmt.Sprintf("%s:List*", service),
		fmt.Sprintf("%s:Describe*", service),
	}
}

// suggestSecureTrustPolicy adds conditions to a trust policy for confused deputy protection
func suggestSecureTrustPolicy(currentTrust string) string {
	var policy TrustPolicy
	if err := json.Unmarshal([]byte(currentTrust), &policy); err != nil {
		return ""
	}

	modified := false

	for i := range policy.Statement {
		stmt := &policy.Statement[i]
		if stmt.Effect != "Allow" {
			continue
		}

		// Check if statement already has conditions
		if stmt.Condition != nil {
			continue
		}

		principals := extractTrustPrincipals(stmt.Principal)
		for _, principal := range principals {
			// Add conditions for service principals
			if strings.HasSuffix(principal, ".amazonaws.com") {
				// Add basic condition structure
				stmt.Condition = map[string]interface{}{
					"StringEquals": map[string]interface{}{
						"aws:SourceAccount": "${AWS_ACCOUNT_ID}",
					},
					"ArnLike": map[string]interface{}{
						"aws:SourceArn": "arn:aws:*:*:${AWS_ACCOUNT_ID}:*",
					},
				}
				modified = true
				break
			}
		}
	}

	if !modified {
		return ""
	}

	result, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return ""
	}
	return string(result)
}

// extractTrustPrincipals extracts principal strings from a trust policy principal field
func extractTrustPrincipals(principal interface{}) []string {
	var principals []string

	switch p := principal.(type) {
	case string:
		principals = append(principals, p)
	case map[string]interface{}:
		for _, v := range p {
			switch val := v.(type) {
			case string:
				principals = append(principals, val)
			case []interface{}:
				for _, item := range val {
					if s, ok := item.(string); ok {
						principals = append(principals, s)
					}
				}
			}
		}
	}

	return principals
}

// toStringSlice converts an interface to a string slice
func toStringSlice(value interface{}) []string {
	switch v := value.(type) {
	case nil:
		return []string{}
	case string:
		return []string{v}
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return v
	default:
		return []string{}
	}
}

// FormatPlan formats a fix plan for display
func FormatPlan(plan *FixPlan) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Fix Plan: %s\n", plan.ID))
	sb.WriteString(fmt.Sprintf("Summary: %s\n", plan.Summary))
	sb.WriteString(fmt.Sprintf("Created: %s\n\n", plan.CreatedAt.Format("2006-01-02 15:04:05")))

	sb.WriteString("Finding:\n")
	sb.WriteString(fmt.Sprintf("  Type: %s\n", plan.Finding.Type))
	sb.WriteString(fmt.Sprintf("  Severity: %s\n", plan.Finding.Severity))
	sb.WriteString(fmt.Sprintf("  Resource: %s\n", plan.Finding.ResourceARN))
	sb.WriteString(fmt.Sprintf("  Description: %s\n\n", plan.Finding.Description))

	if len(plan.Commands) > 0 {
		sb.WriteString("Commands:\n")
		for i, cmd := range plan.Commands {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, cmd.Action))
			sb.WriteString(fmt.Sprintf("     Resource: %s\n", cmd.ResourceARN))
			sb.WriteString(fmt.Sprintf("     Reason: %s\n", cmd.Reason))
			if len(cmd.Parameters) > 0 {
				sb.WriteString("     Parameters:\n")
				for k, v := range cmd.Parameters {
					// Truncate long values
					vStr := fmt.Sprintf("%v", v)
					if len(vStr) > 100 {
						vStr = vStr[:100] + "..."
					}
					sb.WriteString(fmt.Sprintf("       %s: %s\n", k, vStr))
				}
			}
		}
		sb.WriteString("\n")
	}

	if len(plan.Notes) > 0 {
		sb.WriteString("Notes:\n")
		for _, note := range plan.Notes {
			sb.WriteString(fmt.Sprintf("  - %s\n", note))
		}
		sb.WriteString("\n")
	}

	if len(plan.Warnings) > 0 {
		sb.WriteString("Warnings:\n")
		for _, warning := range plan.Warnings {
			sb.WriteString(fmt.Sprintf("  ! %s\n", warning))
		}
	}

	return sb.String()
}

// ValidatePlanJSON validates a plan JSON and returns the parsed plan
func ValidatePlanJSON(planJSON string) (*FixPlan, error) {
	var plan FixPlan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return nil, fmt.Errorf("invalid plan JSON: %w", err)
	}

	if plan.ID == "" {
		return nil, fmt.Errorf("plan missing ID")
	}

	if plan.Summary == "" {
		return nil, fmt.Errorf("plan missing summary")
	}

	return &plan, nil
}
