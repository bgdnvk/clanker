package analyzer

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnalyzePermissions analyzes an IAM policy document for security issues
func AnalyzePermissions(resourceARN, document string) []SecurityFinding {
	var findings []SecurityFinding

	doc, err := ParsePolicyDocument(document)
	if err != nil {
		return findings
	}

	for _, stmt := range doc.Statement {
		if stmt.Effect != "Allow" {
			continue
		}

		actions := toStringSlice(stmt.Action)
		resources := toStringSlice(stmt.Resource)

		// Check for wildcard actions
		wildcardFindings := findWildcardActions(resourceARN, actions, resources)
		findings = append(findings, wildcardFindings...)

		// Check for admin privileges
		adminFindings := findAdminPrivileges(resourceARN, actions)
		findings = append(findings, adminFindings...)

		// Check for overly permissive resources
		resourceFindings := findOverlyPermissiveResources(resourceARN, actions, resources)
		findings = append(findings, resourceFindings...)

		// Check for dangerous action combinations
		dangerousFindings := findDangerousActionCombinations(resourceARN, actions, resources)
		findings = append(findings, dangerousFindings...)
	}

	return findings
}

// findWildcardActions finds statements with wildcard actions
func findWildcardActions(resourceARN string, actions, resources []string) []SecurityFinding {
	var findings []SecurityFinding

	for _, action := range actions {
		if action == "*" {
			findings = append(findings, SecurityFinding{
				ID:          GenerateFindingID(),
				Severity:    SeverityCritical,
				Type:        FindingOverpermissivePolicy,
				ResourceARN: resourceARN,
				Description: "Policy grants all actions (*) which provides full administrative access",
				Remediation: "Replace wildcard action with specific actions required for the workload",
				Actions:     []string{action},
				Resources:   resources,
			})
		} else if strings.HasSuffix(action, ":*") {
			service := strings.TrimSuffix(action, ":*")
			severity := SeverityHigh
			if isHighRiskService(service) {
				severity = SeverityCritical
			}

			findings = append(findings, SecurityFinding{
				ID:          GenerateFindingID(),
				Severity:    severity,
				Type:        FindingOverpermissivePolicy,
				ResourceARN: resourceARN,
				Description: fmt.Sprintf("Policy grants all actions for service %s", service),
				Remediation: fmt.Sprintf("Replace %s with specific %s actions required", action, service),
				Actions:     []string{action},
				Resources:   resources,
			})
		}
	}

	return findings
}

// findAdminPrivileges finds statements that grant administrative access
func findAdminPrivileges(resourceARN string, actions []string) []SecurityFinding {
	var findings []SecurityFinding

	adminActions := []string{
		"iam:*",
		"iam:CreateUser",
		"iam:CreateRole",
		"iam:AttachRolePolicy",
		"iam:AttachUserPolicy",
		"iam:PutRolePolicy",
		"iam:PutUserPolicy",
		"iam:CreatePolicyVersion",
		"iam:SetDefaultPolicyVersion",
		"iam:PassRole",
	}

	var foundAdminActions []string
	for _, action := range actions {
		actionLower := strings.ToLower(action)
		for _, adminAction := range adminActions {
			if actionLower == strings.ToLower(adminAction) || action == "*" {
				foundAdminActions = append(foundAdminActions, action)
				break
			}
		}
	}

	if len(foundAdminActions) > 0 {
		severity := SeverityHigh
		description := "Policy grants IAM administrative actions that could allow privilege escalation"

		// Check for especially dangerous combinations
		hasPassRole := containsAction(foundAdminActions, "iam:PassRole")
		hasCreateRole := containsAction(foundAdminActions, "iam:CreateRole")
		hasAttachPolicy := containsAction(foundAdminActions, "iam:AttachRolePolicy") ||
			containsAction(foundAdminActions, "iam:AttachUserPolicy")

		if hasPassRole && (hasCreateRole || hasAttachPolicy) {
			severity = SeverityCritical
			description = "Policy grants dangerous IAM action combination that enables privilege escalation"
		}

		findings = append(findings, SecurityFinding{
			ID:          GenerateFindingID(),
			Severity:    severity,
			Type:        FindingAdminAccess,
			ResourceARN: resourceARN,
			Description: description,
			Remediation: "Review and restrict IAM permissions to the minimum required",
			Actions:     foundAdminActions,
		})
	}

	return findings
}

// findOverlyPermissiveResources finds statements with wildcard resources
func findOverlyPermissiveResources(resourceARN string, actions, resources []string) []SecurityFinding {
	var findings []SecurityFinding

	hasWildcardResource := false
	for _, r := range resources {
		if r == "*" {
			hasWildcardResource = true
			break
		}
	}

	if !hasWildcardResource {
		return findings
	}

	// Check if actions are sensitive
	sensitiveActions := filterSensitiveActions(actions)
	if len(sensitiveActions) == 0 {
		return findings
	}

	findings = append(findings, SecurityFinding{
		ID:          GenerateFindingID(),
		Severity:    SeverityMedium,
		Type:        FindingMissingResourceScoping,
		ResourceARN: resourceARN,
		Description: "Policy grants sensitive actions on all resources (*)",
		Remediation: "Scope Resource to specific ARNs or use resource-based conditions",
		Actions:     sensitiveActions,
		Resources:   resources,
	})

	return findings
}

// findDangerousActionCombinations finds dangerous action combinations
func findDangerousActionCombinations(resourceARN string, actions, resources []string) []SecurityFinding {
	var findings []SecurityFinding

	// iam:PassRole + lambda:CreateFunction = privilege escalation
	hasPassRole := containsAction(actions, "iam:PassRole")
	hasLambdaCreate := containsAction(actions, "lambda:CreateFunction") ||
		containsAction(actions, "lambda:UpdateFunctionCode")

	if hasPassRole && hasLambdaCreate {
		findings = append(findings, SecurityFinding{
			ID:          GenerateFindingID(),
			Severity:    SeverityCritical,
			Type:        FindingExcessivePermissions,
			ResourceARN: resourceARN,
			Description: "Policy allows iam:PassRole with Lambda create/update which enables privilege escalation",
			Remediation: "Add conditions to iam:PassRole to restrict which roles can be passed",
			Actions:     []string{"iam:PassRole", "lambda:CreateFunction"},
			Resources:   resources,
		})
	}

	// iam:PassRole + ecs:RunTask or ecs:CreateService
	hasECSRun := containsAction(actions, "ecs:RunTask") ||
		containsAction(actions, "ecs:CreateService")

	if hasPassRole && hasECSRun {
		findings = append(findings, SecurityFinding{
			ID:          GenerateFindingID(),
			Severity:    SeverityHigh,
			Type:        FindingExcessivePermissions,
			ResourceARN: resourceARN,
			Description: "Policy allows iam:PassRole with ECS task execution which may enable privilege escalation",
			Remediation: "Add conditions to iam:PassRole to restrict which roles can be passed to ECS",
			Actions:     []string{"iam:PassRole", "ecs:RunTask"},
			Resources:   resources,
		})
	}

	// S3 data exfiltration risk
	hasS3GetObject := containsAction(actions, "s3:GetObject") ||
		containsAction(actions, "s3:*")
	hasS3WildcardResource := false
	for _, r := range resources {
		if r == "*" || strings.HasPrefix(r, "arn:aws:s3:::*") {
			hasS3WildcardResource = true
			break
		}
	}

	if hasS3GetObject && hasS3WildcardResource {
		findings = append(findings, SecurityFinding{
			ID:          GenerateFindingID(),
			Severity:    SeverityHigh,
			Type:        FindingPublicS3Access,
			ResourceARN: resourceARN,
			Description: "Policy grants S3 read access to all buckets which poses a data exfiltration risk",
			Remediation: "Restrict S3 access to specific buckets required for the workload",
			Actions:     filterS3Actions(actions),
			Resources:   resources,
		})
	}

	return findings
}

// AnalyzeTrustPolicy analyzes an IAM role trust policy
func AnalyzeTrustPolicy(roleName, trustPolicy string) []SecurityFinding {
	var findings []SecurityFinding

	var policy TrustPolicy
	if err := json.Unmarshal([]byte(trustPolicy), &policy); err != nil {
		return findings
	}

	for _, stmt := range policy.Statement {
		if stmt.Effect != "Allow" {
			continue
		}

		principals := extractPrincipals(stmt.Principal)
		hasConditions := stmt.Condition != nil

		for _, principal := range principals {
			// Check for wildcard principal
			if principal == "*" {
				findings = append(findings, SecurityFinding{
					ID:          GenerateFindingID(),
					Severity:    SeverityCritical,
					Type:        FindingCrossAccountTrust,
					ResourceARN: roleName,
					Description: "Role trust policy allows anyone (*) to assume the role",
					Remediation: "Restrict Principal to specific AWS accounts or services",
				})
				continue
			}

			// Check for cross-account trust without conditions
			if strings.Contains(principal, "arn:aws:iam::") {
				accountID := extractAccountID(principal)
				if accountID != "" {
					severity := SeverityMedium
					if !hasConditions {
						severity = SeverityHigh
					}

					findings = append(findings, SecurityFinding{
						ID:          GenerateFindingID(),
						Severity:    severity,
						Type:        FindingCrossAccountTrust,
						ResourceARN: roleName,
						Description: fmt.Sprintf("Role can be assumed by account %s", accountID),
						Remediation: "Ensure cross-account trust is intended and add conditions like aws:SourceArn",
					})
				}
			}

			// Check for service principals without condition
			if strings.HasSuffix(principal, ".amazonaws.com") && !hasConditions {
				service := strings.TrimSuffix(principal, ".amazonaws.com")
				if isConfusedDeputyRiskService(service) {
					findings = append(findings, SecurityFinding{
						ID:          GenerateFindingID(),
						Severity:    SeverityMedium,
						Type:        FindingCrossAccountTrust,
						ResourceARN: roleName,
						Description: fmt.Sprintf("Role trust policy for %s service lacks confused deputy protection", service),
						Remediation: "Add aws:SourceArn or aws:SourceAccount conditions to prevent confused deputy attacks",
					})
				}
			}
		}
	}

	return findings
}

// Helper functions

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

func containsAction(actions []string, target string) bool {
	targetLower := strings.ToLower(target)
	for _, a := range actions {
		if strings.ToLower(a) == targetLower || a == "*" {
			return true
		}
		// Check for service wildcard
		if strings.HasSuffix(a, ":*") {
			service := strings.Split(targetLower, ":")[0]
			if strings.HasPrefix(a, service) {
				return true
			}
		}
	}
	return false
}

func isHighRiskService(service string) bool {
	highRiskServices := []string{
		"iam", "sts", "organizations", "kms", "secretsmanager",
		"cloudtrail", "config", "guardduty", "securityhub",
	}
	serviceLower := strings.ToLower(service)
	for _, s := range highRiskServices {
		if serviceLower == s {
			return true
		}
	}
	return false
}

func filterSensitiveActions(actions []string) []string {
	sensitivePatterns := []string{
		"Create", "Delete", "Put", "Update", "Attach", "Detach",
		"Get", "List", "Describe", // Read actions on sensitive data
	}

	var sensitive []string
	for _, action := range actions {
		if action == "*" {
			sensitive = append(sensitive, action)
			continue
		}
		for _, pattern := range sensitivePatterns {
			if strings.Contains(action, pattern) {
				sensitive = append(sensitive, action)
				break
			}
		}
	}
	return sensitive
}

func filterS3Actions(actions []string) []string {
	var s3Actions []string
	for _, action := range actions {
		if strings.HasPrefix(strings.ToLower(action), "s3:") || action == "*" {
			s3Actions = append(s3Actions, action)
		}
	}
	return s3Actions
}

func extractPrincipals(principal interface{}) []string {
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

func extractAccountID(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

func isConfusedDeputyRiskService(service string) bool {
	// Services that commonly need confused deputy protection
	riskServices := []string{
		"lambda", "s3", "cloudwatch", "events", "sns", "sqs",
		"logs", "apigateway", "cloudformation", "codebuild",
		"codepipeline", "ecs", "states", "firehose",
	}

	serviceLower := strings.ToLower(service)
	for _, s := range riskServices {
		if serviceLower == s {
			return true
		}
	}
	return false
}
