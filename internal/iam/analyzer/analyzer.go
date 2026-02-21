package analyzer

import (
	"context"
	"fmt"
)

// IAMClient interface defines the methods needed from the IAM client
type IAMClient interface {
	GetRoleDetails(ctx interface{}, roleName string) (*RoleDetail, error)
	GetPolicyDocument(ctx interface{}, policyARN string) (*PolicyDetail, error)
	ListRoles(ctx interface{}) ([]RoleInfo, error)
	ListPolicies(ctx interface{}) ([]PolicyInfo, error)
	GetCredentialReport(ctx interface{}) (*CredentialReport, error)
}

// SubAgent provides security analysis for IAM resources
type SubAgent struct {
	client IAMClient
	debug  bool
}

// NewSubAgent creates a new IAM analyzer subagent
func NewSubAgent(client IAMClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// AnalyzeAccount performs a comprehensive security analysis of the entire IAM account
func (a *SubAgent) AnalyzeAccount(ctx context.Context) ([]SecurityFinding, error) {
	if a.debug {
		fmt.Println("[iam-analyzer] Starting account-wide security analysis...")
	}

	var findings []SecurityFinding

	// Analyze all roles
	roleFindings, err := a.analyzeAllRoles(ctx)
	if err != nil && a.debug {
		fmt.Printf("[iam-analyzer] Warning: error analyzing roles: %v\n", err)
	}
	findings = append(findings, roleFindings...)

	// Analyze all policies
	policyFindings, err := a.analyzeAllPolicies(ctx)
	if err != nil && a.debug {
		fmt.Printf("[iam-analyzer] Warning: error analyzing policies: %v\n", err)
	}
	findings = append(findings, policyFindings...)

	// Analyze credential report
	credentialFindings, err := a.analyzeCredentials(ctx)
	if err != nil && a.debug {
		fmt.Printf("[iam-analyzer] Warning: error analyzing credentials: %v\n", err)
	}
	findings = append(findings, credentialFindings...)

	if a.debug {
		fmt.Printf("[iam-analyzer] Analysis complete. Found %d security issues.\n", len(findings))
	}

	return findings, nil
}

// AnalyzeRole performs security analysis on a specific role
func (a *SubAgent) AnalyzeRole(ctx context.Context, roleName string) ([]SecurityFinding, error) {
	if a.debug {
		fmt.Printf("[iam-analyzer] Analyzing role: %s\n", roleName)
	}

	var findings []SecurityFinding

	detail, err := a.client.GetRoleDetails(ctx, roleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get role details: %w", err)
	}

	// Analyze trust policy
	trustFindings := AnalyzeTrustPolicy(detail.RoleName, detail.AssumeRolePolicyDocument)
	findings = append(findings, trustFindings...)

	// Analyze attached policies
	for _, policy := range detail.AttachedPolicies {
		policyDetail, err := a.client.GetPolicyDocument(ctx, policy.PolicyARN)
		if err != nil {
			continue
		}
		policyFindings := AnalyzePermissions(policy.PolicyARN, policyDetail.PolicyDocument)
		findings = append(findings, policyFindings...)
	}

	// Analyze inline policies
	for _, policy := range detail.InlinePolicies {
		policyFindings := AnalyzePermissions(
			fmt.Sprintf("%s (inline: %s)", detail.RoleARN, policy.PolicyName),
			policy.PolicyDocument,
		)
		findings = append(findings, policyFindings...)
	}

	// Check if role is unused
	if detail.LastUsed == nil {
		findings = append(findings, SecurityFinding{
			ID:          GenerateFindingID(),
			Severity:    SeverityLow,
			Type:        FindingUnusedRole,
			ResourceARN: detail.RoleARN,
			Description: fmt.Sprintf("Role %s has never been used", detail.RoleName),
			Remediation: "Consider deleting unused roles to reduce attack surface",
		})
	}

	return findings, nil
}

// AnalyzePolicy performs security analysis on a specific policy
func (a *SubAgent) AnalyzePolicy(ctx context.Context, policyARN string) ([]SecurityFinding, error) {
	if a.debug {
		fmt.Printf("[iam-analyzer] Analyzing policy: %s\n", policyARN)
	}

	detail, err := a.client.GetPolicyDocument(ctx, policyARN)
	if err != nil {
		return nil, fmt.Errorf("failed to get policy document: %w", err)
	}

	findings := AnalyzePermissions(policyARN, detail.PolicyDocument)
	return findings, nil
}

// analyzeAllRoles analyzes all roles in the account
func (a *SubAgent) analyzeAllRoles(ctx context.Context) ([]SecurityFinding, error) {
	roles, err := a.client.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list roles: %w", err)
	}

	var findings []SecurityFinding

	for _, role := range roles {
		roleFindings, err := a.AnalyzeRole(ctx, role.RoleName)
		if err != nil {
			if a.debug {
				fmt.Printf("[iam-analyzer] Warning: error analyzing role %s: %v\n", role.RoleName, err)
			}
			continue
		}
		findings = append(findings, roleFindings...)
	}

	return findings, nil
}

// analyzeAllPolicies analyzes all customer-managed policies
func (a *SubAgent) analyzeAllPolicies(ctx context.Context) ([]SecurityFinding, error) {
	policies, err := a.client.ListPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list policies: %w", err)
	}

	var findings []SecurityFinding

	for _, policy := range policies {
		policyFindings, err := a.AnalyzePolicy(ctx, policy.PolicyARN)
		if err != nil {
			if a.debug {
				fmt.Printf("[iam-analyzer] Warning: error analyzing policy %s: %v\n", policy.PolicyName, err)
			}
			continue
		}
		findings = append(findings, policyFindings...)
	}

	return findings, nil
}

// analyzeCredentials analyzes the credential report for security issues
func (a *SubAgent) analyzeCredentials(ctx context.Context) ([]SecurityFinding, error) {
	report, err := a.client.GetCredentialReport(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get credential report: %w", err)
	}

	return AnalyzeCredentialReport(report), nil
}
