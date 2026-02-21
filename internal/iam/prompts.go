package iam

import "fmt"

// GetLLMAnalysisPrompt returns the prompt for LLM to analyze what IAM operations are needed
func GetLLMAnalysisPrompt(question string, iamContext string) string {
	return fmt.Sprintf(`Analyze this user query about AWS IAM and determine what operations would be needed to answer it accurately.

User Query: "%s"

Current IAM Context:
%s

Available IAM READ-ONLY operations (all are safe and never modify anything):

ACCOUNT INFORMATION:
- get_account_summary: Get IAM account statistics (role count, user count, policy count, etc.)
- get_caller_identity: Get current AWS account and identity information

ROLES:
- list_roles: List all IAM roles with basic information
- get_role_details: Get detailed information about a specific role (requires role_name parameter)
- get_role_policies: List attached and inline policies for a role (requires role_name parameter)
- analyze_role_trust: Analyze the trust policy of a role (requires role_name parameter)

POLICIES:
- list_policies: List all customer-managed IAM policies
- get_policy_document: Get the full policy document (requires policy_arn parameter)
- get_role_policy_document: Get inline policy document (requires role_name and policy_name parameters)

USERS:
- list_users: List all IAM users
- get_user_details: Get detailed user information (requires user_name parameter)
- list_user_policies: List attached and inline policies for a user (requires user_name parameter)

ACCESS KEYS:
- list_access_keys: List access keys for a user (requires user_name parameter)
- check_access_key_rotation: Check if access keys need rotation based on age

CREDENTIAL REPORT:
- get_credential_report: Generate and get the full credential report for the account

GROUPS:
- list_groups: List all IAM groups
- get_group_details: Get group membership and policies (requires group_name parameter)

SECURITY ANALYSIS:
- find_overpermissive_policies: Scan for policies with wildcards or admin access
- find_admin_access: Find roles or users with AdministratorAccess or equivalent
- check_mfa_status: Check MFA status for all users
- find_unused_roles: Find roles that have not been used recently
- find_cross_account_trusts: Find roles with cross-account trust relationships
- analyze_permissions: Analyze permissions granted by a specific policy or role

Respond with ONLY a JSON object in this format:
{
  "operations": [
    {
      "operation": "operation_name",
      "reason": "why this operation is needed to answer the question",
      "parameters": {
        "role_name": "optional role name",
        "policy_arn": "optional policy ARN",
        "user_name": "optional user name",
        "group_name": "optional group name",
        "policy_name": "optional inline policy name"
      }
    }
  ],
  "analysis": "brief explanation of what the user wants to know"
}

Important guidelines:
- Only include operations that are necessary to answer the question
- For security analysis queries, include relevant security scanning operations
- For specific role or policy queries, include the get_role_details or get_policy_document operations
- If no IAM operations are needed, return: {"operations": [], "analysis": "explanation"}`, question, iamContext)
}

// GetFinalResponsePrompt returns the prompt for generating the final user-facing response
func GetFinalResponsePrompt(question, iamData, conversationContext string) string {
	prompt := `Based on the AWS IAM data below, please answer the user's question comprehensively.

`
	if conversationContext != "" {
		prompt += fmt.Sprintf(`Previous conversation context (for follow-up questions):
%s

`, conversationContext)
	}

	prompt += fmt.Sprintf(`Current Question: "%s"

AWS IAM Data:
%s

Instructions:
- Provide a clear, well-formatted markdown response
- Include specific details like role names, policy ARNs, and permission details
- Use tables for listing multiple resources when appropriate
- Highlight any security concerns, warnings, or issues found
- If analyzing security, prioritize findings by severity (Critical > High > Medium > Low)
- Suggest remediation steps for any security issues found
- Keep the response concise but complete
- Do not include raw JSON unless specifically asked`, question, iamData)

	return prompt
}

// GetSecurityAnalysisPrompt returns the prompt for analyzing a policy document for security issues
func GetSecurityAnalysisPrompt(resourceType, resourceName, policyDocument string) string {
	return fmt.Sprintf(`Analyze this AWS IAM policy document for security issues and best practices violations.

Resource Type: %s
Resource Name: %s

Policy Document:
%s

Analyze and identify:
1. Overly permissive actions (wildcards like "*" or service:*)
2. Missing resource scoping (Resource: "*" when specific ARNs could be used)
3. Administrative access patterns (iam:*, *:*, AdministratorAccess)
4. Dangerous action combinations (iam:PassRole + lambda:CreateFunction)
5. Missing conditions that should be present (e.g., MFA requirements)
6. Cross-account access risks in trust policies
7. Data exfiltration risks (s3:GetObject with broad access)
8. Privilege escalation risks

Respond with ONLY a JSON object in this format:
{
  "findings": [
    {
      "severity": "critical|high|medium|low|info",
      "type": "finding_type",
      "description": "what the issue is",
      "actions": ["list", "of", "problematic", "actions"],
      "resources": ["list", "of", "affected", "resources"],
      "remediation": "how to fix this issue"
    }
  ],
  "overall_risk": "critical|high|medium|low",
  "summary": "brief summary of the policy's security posture"
}

Severity guide:
- critical: Immediate security risk, unrestricted admin access, data exposure
- high: Significant security concern, broad permissions, missing constraints
- medium: Best practice violation, overly permissive but limited scope
- low: Minor issues, recommendations for improvement
- info: Informational findings, not security concerns`, resourceType, resourceName, policyDocument)
}

// GetFixPlanPrompt returns the prompt for generating a fix plan for a security finding
func GetFixPlanPrompt(finding SecurityFinding, currentPolicy string) string {
	return fmt.Sprintf(`Generate a remediation plan to fix this IAM security finding.

Finding:
- Type: %s
- Severity: %s
- Description: %s
- Resource: %s
- Problematic Actions: %v
- Affected Resources: %v
- Suggested Remediation: %s

Current Policy Document:
%s

Generate a fix plan that:
1. Addresses the specific security finding
2. Follows the principle of least privilege
3. Maintains necessary functionality where possible
4. Includes specific AWS CLI commands or policy changes needed

Respond with ONLY a JSON object in this format:
{
  "summary": "brief description of what the fix does",
  "commands": [
    {
      "action": "update_policy|create_policy_version|attach_policy|detach_policy",
      "resource_arn": "ARN of the resource being modified",
      "parameters": {
        "document": "new policy document if applicable",
        "other_params": "as needed"
      },
      "reason": "why this command is needed"
    }
  ],
  "notes": ["important notes about the fix"],
  "warnings": ["any warnings about potential impact"]
}

Important:
- Only include commands that are necessary to fix the specific finding
- Ensure the fix does not break existing functionality unnecessarily
- Include appropriate IAM conditions where applicable
- If the fix requires multiple steps, order them correctly`,
		finding.Type,
		finding.Severity,
		finding.Description,
		finding.ResourceARN,
		finding.Actions,
		finding.Resources,
		finding.Remediation,
		currentPolicy)
}

// GetTrustPolicyAnalysisPrompt returns the prompt for analyzing a trust policy
func GetTrustPolicyAnalysisPrompt(roleName, trustPolicy string) string {
	return fmt.Sprintf(`Analyze this IAM role trust policy for security concerns.

Role Name: %s

Trust Policy:
%s

Analyze and identify:
1. Cross-account trust relationships (principals from other AWS accounts)
2. Service principals and their appropriateness
3. Overly broad trust (Principal: "*" or Principal: {"AWS": "*"})
4. Missing conditions (aws:SourceArn, aws:SourceAccount for confused deputy prevention)
5. External identity provider trusts (OIDC, SAML)
6. Trust to potentially compromised services

Respond with ONLY a JSON object in this format:
{
  "findings": [
    {
      "severity": "critical|high|medium|low|info",
      "type": "trust_policy_finding_type",
      "description": "what the concern is",
      "principals": ["affected", "principals"],
      "remediation": "how to secure this"
    }
  ],
  "trusted_entities": [
    {
      "type": "aws_account|aws_service|oidc|saml|federated",
      "identifier": "the principal identifier",
      "risk_level": "high|medium|low",
      "notes": "context about this trust"
    }
  ],
  "summary": "brief summary of the trust policy security posture"
}`, roleName, trustPolicy)
}

// GetAccountSummaryContext returns a formatted string of IAM account summary
func GetAccountSummaryContext(summary *AccountSummary) string {
	if summary == nil {
		return "IAM Account Summary: Not yet gathered"
	}

	return fmt.Sprintf(`IAM Account Summary:
- Total Roles: %d
- Total Policies (Customer Managed): %d
- Total Users: %d
- Total Groups: %d
- Instance Profiles: %d
- MFA Devices: %d`,
		summary.RoleCount,
		summary.PolicyCount,
		summary.UserCount,
		summary.GroupCount,
		summary.InstanceProfiles,
		summary.MFADevices)
}

// GetPermissionAnalysisPrompt returns the prompt for analyzing specific permissions
func GetPermissionAnalysisPrompt(question string, permissions []string, context string) string {
	return fmt.Sprintf(`Analyze these IAM permissions in the context of the user's question.

User Question: "%s"

Permissions Found:
%v

Additional Context:
%s

Provide analysis of:
1. What these permissions allow the principal to do
2. Any security implications of these permissions
3. Whether these permissions seem appropriate for common use cases
4. Any recommendations for restriction or improvement

Format your response as clear, actionable markdown.`, question, permissions, context)
}
