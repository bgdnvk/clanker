package fixer

import (
	"context"
	"fmt"
	"time"
)

// SubAgent provides fix capabilities for IAM security findings
type SubAgent struct {
	client IAMClient
	debug  bool
}

// NewSubAgent creates a new IAM fixer subagent
func NewSubAgent(client IAMClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// GenerateFixPlan generates a remediation plan for a security finding
func (f *SubAgent) GenerateFixPlan(ctx context.Context, finding SecurityFinding) (*FixPlan, error) {
	if f.debug {
		fmt.Printf("[iam-fixer] Generating fix plan for finding: %s (%s)\n", finding.Type, finding.ResourceARN)
	}

	plan := &FixPlan{
		ID:        generatePlanID(),
		Finding:   finding,
		CreatedAt: time.Now(),
	}

	switch finding.Type {
	case FindingOverpermissivePolicy:
		commands, notes, warnings := f.planOverpermissivePolicyFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Restrict overly permissive IAM policy"

	case FindingAdminAccess:
		commands, notes, warnings := f.planAdminAccessFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Review and restrict administrative IAM access"

	case FindingWildcardResource:
		commands, notes, warnings := f.planWildcardResourceFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Add resource scoping to IAM policy"

	case FindingCrossAccountTrust:
		commands, notes, warnings := f.planCrossAccountTrustFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Secure cross-account trust relationship"

	case FindingMissingMFA:
		commands, notes, warnings := f.planMFAFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Enable MFA for user"

	case FindingOldAccessKeys:
		commands, notes, warnings := f.planAccessKeyRotationFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Rotate old access keys"

	case FindingInactiveKeys:
		commands, notes, warnings := f.planInactiveKeyFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Deactivate or delete unused access keys"

	case FindingUnusedRole:
		commands, notes, warnings := f.planUnusedRoleFix(ctx, finding)
		plan.Commands = commands
		plan.Notes = notes
		plan.Warnings = warnings
		plan.Summary = "Review and potentially delete unused role"

	default:
		plan.Summary = fmt.Sprintf("Manual review required for %s finding", finding.Type)
		plan.Notes = []string{
			"This finding type requires manual review",
			finding.Remediation,
		}
	}

	if len(plan.Commands) == 0 && len(plan.Notes) == 0 {
		plan.Notes = []string{
			"No automated fix available for this finding",
			"Manual remediation suggested: " + finding.Remediation,
		}
	}

	return plan, nil
}

// ValidatePlan validates a fix plan before execution
func (f *SubAgent) ValidatePlan(plan *FixPlan) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}

	if plan.ID == "" {
		return fmt.Errorf("plan has no ID")
	}

	if len(plan.Commands) == 0 && len(plan.Notes) == 0 {
		return fmt.Errorf("plan has no commands or notes")
	}

	for i, cmd := range plan.Commands {
		if cmd.Action == "" {
			return fmt.Errorf("command %d has no action", i)
		}
		if cmd.ResourceARN == "" {
			return fmt.Errorf("command %d has no resource ARN", i)
		}
	}

	return nil
}

// ApplyPlan applies a fix plan
func (f *SubAgent) ApplyPlan(ctx context.Context, plan *FixPlan, confirm bool) error {
	if err := f.ValidatePlan(plan); err != nil {
		return fmt.Errorf("invalid plan: %w", err)
	}

	if !confirm {
		return fmt.Errorf("plan execution requires confirmation")
	}

	if f.debug {
		fmt.Printf("[iam-fixer] Applying plan: %s\n", plan.Summary)
	}

	for i, cmd := range plan.Commands {
		if f.debug {
			fmt.Printf("[iam-fixer] Executing command %d/%d: %s on %s\n",
				i+1, len(plan.Commands), cmd.Action, cmd.ResourceARN)
		}

		if err := f.executeCommand(ctx, cmd); err != nil {
			return fmt.Errorf("command %d (%s) failed: %w", i+1, cmd.Action, err)
		}
	}

	if f.debug {
		fmt.Println("[iam-fixer] Plan applied successfully")
	}

	return nil
}

// executeCommand executes a single fix command
func (f *SubAgent) executeCommand(ctx context.Context, cmd FixCommand) error {
	switch cmd.Action {
	case ActionCreatePolicyVersion:
		policyARN := cmd.ResourceARN
		document, ok := cmd.Parameters["document"].(string)
		if !ok || document == "" {
			return fmt.Errorf("document parameter required for create_policy_version")
		}
		return f.client.CreatePolicyVersion(ctx, policyARN, document, true)

	case ActionUpdateTrustPolicy:
		roleName := extractRoleName(cmd.ResourceARN)
		document, ok := cmd.Parameters["document"].(string)
		if !ok || document == "" {
			return fmt.Errorf("document parameter required for update_trust_policy")
		}
		return f.client.UpdateAssumeRolePolicy(ctx, roleName, document)

	case ActionAttachPolicy:
		roleName := extractRoleName(cmd.ResourceARN)
		policyARN, ok := cmd.Parameters["policy_arn"].(string)
		if !ok || policyARN == "" {
			return fmt.Errorf("policy_arn parameter required for attach_policy")
		}
		return f.client.AttachRolePolicy(ctx, roleName, policyARN)

	case ActionDetachPolicy:
		roleName := extractRoleName(cmd.ResourceARN)
		policyARN, ok := cmd.Parameters["policy_arn"].(string)
		if !ok || policyARN == "" {
			return fmt.Errorf("policy_arn parameter required for detach_policy")
		}
		return f.client.DetachRolePolicy(ctx, roleName, policyARN)

	case ActionDeactivateAccessKey:
		userName := extractUserName(cmd.ResourceARN)
		accessKeyID, ok := cmd.Parameters["access_key_id"].(string)
		if !ok || accessKeyID == "" {
			return fmt.Errorf("access_key_id parameter required for deactivate_access_key")
		}
		return f.client.UpdateAccessKey(ctx, userName, accessKeyID, "Inactive")

	default:
		return fmt.Errorf("unsupported action: %s", cmd.Action)
	}
}

// Plan generation helper functions

func (f *SubAgent) planOverpermissivePolicyFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	warnings = append(warnings, "Review the suggested policy changes carefully before applying")
	notes = append(notes, "This fix will create a new policy version with restricted permissions")

	// Get current policy to suggest modifications
	policyDetail, err := f.client.GetPolicyDocument(ctx, finding.ResourceARN)
	if err != nil {
		notes = append(notes, fmt.Sprintf("Unable to retrieve current policy: %v", err))
		notes = append(notes, "Manual review required: "+finding.Remediation)
		return commands, notes, warnings
	}

	suggestedDoc := suggestLeastPrivilegePolicy(policyDetail.PolicyDocument, finding.Actions)

	commands = append(commands, FixCommand{
		ID:          generateCommandID(),
		Action:      ActionCreatePolicyVersion,
		ResourceARN: finding.ResourceARN,
		Parameters: map[string]interface{}{
			"document": suggestedDoc,
		},
		Reason: "Replace overly permissive actions with least-privilege alternatives",
	})

	return commands, notes, warnings
}

func (f *SubAgent) planAdminAccessFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	warnings = append(warnings, "Removing administrative access may break functionality")
	warnings = append(warnings, "Ensure workloads do not require admin access before applying")

	notes = append(notes, "Review which specific permissions are actually needed")
	notes = append(notes, "Consider using AWS Access Analyzer to identify required permissions")

	// Do not auto-generate commands for admin access, require manual review
	notes = append(notes, "Automated fix not recommended for admin access findings")
	notes = append(notes, finding.Remediation)

	return commands, notes, warnings
}

func (f *SubAgent) planWildcardResourceFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	notes = append(notes, "Identify specific resources that need to be accessed")
	notes = append(notes, "Replace Resource: \"*\" with specific ARNs")

	// Get current policy
	policyDetail, err := f.client.GetPolicyDocument(ctx, finding.ResourceARN)
	if err != nil {
		notes = append(notes, fmt.Sprintf("Unable to retrieve current policy: %v", err))
		return commands, notes, warnings
	}

	notes = append(notes, "Current policy document retrieved for review")
	notes = append(notes, "Manual specification of target resources required")
	notes = append(notes, fmt.Sprintf("Actions to scope: %v", finding.Actions))

	// Store current document for reference
	_ = policyDetail

	return commands, notes, warnings
}

func (f *SubAgent) planCrossAccountTrustFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	warnings = append(warnings, "Modifying trust policy may break cross-account access")

	notes = append(notes, "Add conditions to restrict cross-account trust:")
	notes = append(notes, "  - aws:SourceArn: Restrict to specific resource ARNs")
	notes = append(notes, "  - aws:SourceAccount: Restrict to specific accounts")
	notes = append(notes, "  - aws:PrincipalOrgID: Restrict to organization")

	// Get current trust policy
	roleName := extractRoleName(finding.ResourceARN)
	roleDetail, err := f.client.GetRoleDetails(ctx, roleName)
	if err != nil {
		notes = append(notes, fmt.Sprintf("Unable to retrieve role details: %v", err))
		return commands, notes, warnings
	}

	suggestedTrust := suggestSecureTrustPolicy(roleDetail.AssumeRolePolicyDocument)
	if suggestedTrust != "" {
		commands = append(commands, FixCommand{
			ID:          generateCommandID(),
			Action:      ActionUpdateTrustPolicy,
			ResourceARN: finding.ResourceARN,
			Parameters: map[string]interface{}{
				"document": suggestedTrust,
			},
			Reason: "Add conditions to trust policy for confused deputy protection",
		})
	}

	return commands, notes, warnings
}

func (f *SubAgent) planMFAFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	notes = append(notes, "MFA must be enabled by the user themselves or an administrator")
	notes = append(notes, "Steps to enable MFA:")
	notes = append(notes, "  1. Sign in to AWS Console as the user")
	notes = append(notes, "  2. Go to IAM > Users > Security credentials")
	notes = append(notes, "  3. Assign MFA device (virtual or hardware)")

	notes = append(notes, "For programmatic enforcement, consider:")
	notes = append(notes, "  - Adding MFA condition to IAM policies")
	notes = append(notes, "  - Using SCP to require MFA for sensitive actions")

	return commands, notes, warnings
}

func (f *SubAgent) planAccessKeyRotationFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	warnings = append(warnings, "Deactivating access keys may break applications using them")
	warnings = append(warnings, "Create new access keys before deactivating old ones")

	notes = append(notes, "Recommended rotation process:")
	notes = append(notes, "  1. Create a new access key")
	notes = append(notes, "  2. Update applications to use the new key")
	notes = append(notes, "  3. Test that applications work with new key")
	notes = append(notes, "  4. Deactivate the old key")
	notes = append(notes, "  5. After verification period, delete the old key")

	return commands, notes, warnings
}

func (f *SubAgent) planInactiveKeyFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	notes = append(notes, "Unused access keys should be deactivated or deleted")
	notes = append(notes, "Verify the key is truly unused before removal")

	// Extract user and key info from finding
	userName := extractUserName(finding.ResourceARN)
	if userName != "" {
		keys, err := f.client.ListAccessKeys(ctx, userName)
		if err == nil {
			for _, key := range keys {
				if key.LastUsedDate == nil && key.Status == "Active" {
					commands = append(commands, FixCommand{
						ID:          generateCommandID(),
						Action:      ActionDeactivateAccessKey,
						ResourceARN: finding.ResourceARN,
						Parameters: map[string]interface{}{
							"access_key_id": key.AccessKeyId,
						},
						Reason: "Deactivate unused access key",
					})
				}
			}
		}
	}

	return commands, notes, warnings
}

func (f *SubAgent) planUnusedRoleFix(ctx context.Context, finding SecurityFinding) ([]FixCommand, []string, []string) {
	var commands []FixCommand
	var notes []string
	var warnings []string

	warnings = append(warnings, "Deleting roles is irreversible")
	warnings = append(warnings, "Ensure the role is truly unused before deletion")

	notes = append(notes, "Steps to safely remove unused role:")
	notes = append(notes, "  1. Verify role is not referenced in any application configs")
	notes = append(notes, "  2. Check CloudTrail for recent AssumeRole events")
	notes = append(notes, "  3. Detach all policies from the role")
	notes = append(notes, "  4. Remove role from any instance profiles")
	notes = append(notes, "  5. Delete the role")

	notes = append(notes, "Automated deletion not recommended - manual review required")

	return commands, notes, warnings
}
