package iam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// IAMAnalysis represents the LLM's analysis of what IAM operations are needed
type IAMAnalysis struct {
	Operations []IAMOperation `json:"operations"`
	Analysis   string         `json:"analysis"`
}

// IAMOperationResult represents the result of an IAM operation
type IAMOperationResult struct {
	Operation string
	Result    string
	Error     error
	Index     int
}

// ExecuteOperations executes IAM operations in parallel and returns combined results
func (c *Client) ExecuteOperations(ctx context.Context, operations []IAMOperation) (string, error) {
	if len(operations) == 0 {
		return "", nil
	}

	verbose := viper.GetBool("debug")
	localMode := viper.GetBool("local_mode")
	delayMs := viper.GetInt("local_delay_ms")

	// Default to local mode with rate limiting
	if !viper.IsSet("local_mode") {
		localMode = true
	}
	if localMode && delayMs == 0 {
		delayMs = 100
	}

	resultChan := make(chan IAMOperationResult, len(operations))
	var wg sync.WaitGroup

	for i, op := range operations {
		wg.Add(1)

		// Rate limiting for local mode
		if localMode && i > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		go func(index int, operation IAMOperation) {
			defer wg.Done()

			if verbose {
				fmt.Printf("[iam] Starting operation %d: %s\n", index+1, operation.Operation)
			}

			start := time.Now()
			result, err := c.executeIAMOperation(ctx, operation)
			duration := time.Since(start)

			if verbose {
				if err != nil {
					fmt.Printf("[iam] Operation %d failed (%v): %s - %v\n", index+1, duration, operation.Operation, err)
				} else {
					fmt.Printf("[iam] Operation %d completed (%v): %s\n", index+1, duration, operation.Operation)
				}
			}

			resultChan <- IAMOperationResult{
				Operation: operation.Operation,
				Result:    result,
				Error:     err,
				Index:     index,
			}
		}(i, op)
	}

	// Wait for all operations and close channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results in order
	results := make([]IAMOperationResult, len(operations))
	for result := range resultChan {
		results[result.Index] = result
	}

	// Build combined results string
	var iamResults strings.Builder
	for _, result := range results {
		if result.Error != nil {
			iamResults.WriteString(fmt.Sprintf("[%s] Error: %v\n\n", result.Operation, result.Error))
		} else if result.Result != "" {
			iamResults.WriteString(fmt.Sprintf("[%s]:\n%s\n\n", result.Operation, result.Result))
		}
	}

	return iamResults.String(), nil
}

// executeIAMOperation executes a single IAM operation based on its type
func (c *Client) executeIAMOperation(ctx context.Context, op IAMOperation) (string, error) {
	// Extract common parameters
	roleName := c.getStringParam(op.Parameters, "role_name", "")
	policyARN := c.getStringParam(op.Parameters, "policy_arn", "")
	userName := c.getStringParam(op.Parameters, "user_name", "")
	policyName := c.getStringParam(op.Parameters, "policy_name", "")
	groupName := c.getStringParam(op.Parameters, "group_name", "")

	switch op.Operation {
	// ACCOUNT INFORMATION
	case "get_account_summary":
		summary, err := c.GetAccountSummary(ctx)
		if err != nil {
			return "", err
		}
		return formatAccountSummary(summary), nil

	case "get_caller_identity":
		return fmt.Sprintf("Account ID: %s", c.GetAccountID()), nil

	// ROLES
	case "list_roles":
		roles, err := c.ListRoles(ctx)
		if err != nil {
			return "", err
		}
		return formatRoleList(roles), nil

	case "get_role_details":
		if roleName == "" {
			return "", fmt.Errorf("role_name required")
		}
		detail, err := c.GetRoleDetails(ctx, roleName)
		if err != nil {
			return "", err
		}
		return formatRoleDetail(detail), nil

	case "get_role_policies":
		if roleName == "" {
			return "", fmt.Errorf("role_name required")
		}
		detail, err := c.GetRoleDetails(ctx, roleName)
		if err != nil {
			return "", err
		}
		return formatRolePolicies(detail), nil

	case "analyze_role_trust":
		if roleName == "" {
			return "", fmt.Errorf("role_name required")
		}
		detail, err := c.GetRoleDetails(ctx, roleName)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Trust Policy for role %s:\n%s", roleName, detail.AssumeRolePolicyDocument), nil

	// POLICIES
	case "list_policies":
		policies, err := c.ListPolicies(ctx)
		if err != nil {
			return "", err
		}
		return formatPolicyList(policies), nil

	case "get_policy_document":
		if policyARN == "" {
			return "", fmt.Errorf("policy_arn required")
		}
		detail, err := c.GetPolicyDocument(ctx, policyARN)
		if err != nil {
			return "", err
		}
		return formatPolicyDetail(detail), nil

	case "get_role_policy_document":
		if roleName == "" || policyName == "" {
			return "", fmt.Errorf("role_name and policy_name required")
		}
		doc, err := c.GetRolePolicyDocument(ctx, roleName, policyName)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Inline Policy %s for role %s:\n%s", policyName, roleName, doc), nil

	// USERS
	case "list_users":
		users, err := c.ListUsers(ctx)
		if err != nil {
			return "", err
		}
		return formatUserList(users), nil

	case "get_user_details":
		if userName == "" {
			return "", fmt.Errorf("user_name required")
		}
		users, err := c.ListUsers(ctx)
		if err != nil {
			return "", err
		}
		for _, u := range users {
			if u.UserName == userName {
				return formatUserDetail(&u), nil
			}
		}
		return "", fmt.Errorf("user %s not found", userName)

	case "list_user_policies":
		if userName == "" {
			return "", fmt.Errorf("user_name required")
		}
		// This would need additional implementation
		return fmt.Sprintf("User policies for %s (implementation pending)", userName), nil

	// ACCESS KEYS
	case "list_access_keys":
		if userName == "" {
			return "", fmt.Errorf("user_name required")
		}
		keys, err := c.ListAccessKeys(ctx, userName)
		if err != nil {
			return "", err
		}
		return formatAccessKeys(keys), nil

	case "check_access_key_rotation":
		report, err := c.GetCredentialReport(ctx)
		if err != nil {
			return "", err
		}
		return formatAccessKeyRotationStatus(report), nil

	// CREDENTIAL REPORT
	case "get_credential_report":
		report, err := c.GetCredentialReport(ctx)
		if err != nil {
			return "", err
		}
		return formatCredentialReport(report), nil

	// GROUPS
	case "list_groups":
		groups, err := c.ListGroups(ctx)
		if err != nil {
			return "", err
		}
		return formatGroupList(groups), nil

	case "get_group_details":
		if groupName == "" {
			return "", fmt.Errorf("group_name required")
		}
		groups, err := c.ListGroups(ctx)
		if err != nil {
			return "", err
		}
		for _, g := range groups {
			if g.GroupName == groupName {
				return formatGroupDetail(&g), nil
			}
		}
		return "", fmt.Errorf("group %s not found", groupName)

	// SECURITY ANALYSIS
	case "find_overpermissive_policies":
		return c.findOverpermissivePolicies(ctx)

	case "find_admin_access":
		return c.findAdminAccess(ctx)

	case "check_mfa_status":
		report, err := c.GetCredentialReport(ctx)
		if err != nil {
			return "", err
		}
		return formatMFAStatus(report), nil

	case "find_unused_roles":
		return c.findUnusedRoles(ctx)

	case "find_cross_account_trusts":
		return c.findCrossAccountTrusts(ctx)

	case "analyze_permissions":
		if roleName != "" {
			detail, err := c.GetRoleDetails(ctx, roleName)
			if err != nil {
				return "", err
			}
			return formatPermissionAnalysis(detail), nil
		}
		if policyARN != "" {
			detail, err := c.GetPolicyDocument(ctx, policyARN)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Policy: %s\nDocument:\n%s", policyARN, detail.PolicyDocument), nil
		}
		return "", fmt.Errorf("role_name or policy_arn required")

	default:
		return "", fmt.Errorf("unknown operation: %s", op.Operation)
	}
}

// Helper functions for formatting

func formatAccountSummary(summary *AccountSummary) string {
	return fmt.Sprintf(`IAM Account Summary:
- Roles: %d
- Policies (Customer Managed): %d
- Users: %d
- Groups: %d
- Instance Profiles: %d
- MFA Devices: %d`,
		summary.RoleCount,
		summary.PolicyCount,
		summary.UserCount,
		summary.GroupCount,
		summary.InstanceProfiles,
		summary.MFADevices)
}

func formatRoleList(roles []RoleInfo) string {
	if len(roles) == 0 {
		return "No IAM roles found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d IAM roles:\n\n", len(roles)))
	sb.WriteString("NAME\tPATH\tCREATED\n")
	for _, r := range roles {
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\n",
			r.RoleName, r.Path, r.CreateDate.Format("2006-01-02")))
	}
	return sb.String()
}

func formatRoleDetail(detail *RoleDetail) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Role: %s\n", detail.RoleName))
	sb.WriteString(fmt.Sprintf("ARN: %s\n", detail.RoleARN))
	sb.WriteString(fmt.Sprintf("Path: %s\n", detail.Path))
	sb.WriteString(fmt.Sprintf("Created: %s\n", detail.CreateDate.Format("2006-01-02 15:04:05")))
	if detail.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", detail.Description))
	}
	if detail.LastUsed != nil {
		sb.WriteString(fmt.Sprintf("Last Used: %s\n", detail.LastUsed.Format("2006-01-02 15:04:05")))
	}

	sb.WriteString("\nAttached Policies:\n")
	if len(detail.AttachedPolicies) == 0 {
		sb.WriteString("  (none)\n")
	} else {
		for _, p := range detail.AttachedPolicies {
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", p.PolicyName, p.PolicyARN))
		}
	}

	sb.WriteString("\nInline Policies:\n")
	if len(detail.InlinePolicies) == 0 {
		sb.WriteString("  (none)\n")
	} else {
		for _, p := range detail.InlinePolicies {
			sb.WriteString(fmt.Sprintf("  - %s\n", p.PolicyName))
		}
	}

	sb.WriteString("\nTrust Policy:\n")
	sb.WriteString(detail.AssumeRolePolicyDocument)

	return sb.String()
}

func formatRolePolicies(detail *RoleDetail) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Policies for role %s:\n\n", detail.RoleName))

	sb.WriteString("Attached Managed Policies:\n")
	if len(detail.AttachedPolicies) == 0 {
		sb.WriteString("  (none)\n")
	} else {
		for _, p := range detail.AttachedPolicies {
			sb.WriteString(fmt.Sprintf("  - %s\n    ARN: %s\n", p.PolicyName, p.PolicyARN))
		}
	}

	sb.WriteString("\nInline Policies:\n")
	if len(detail.InlinePolicies) == 0 {
		sb.WriteString("  (none)\n")
	} else {
		for _, p := range detail.InlinePolicies {
			sb.WriteString(fmt.Sprintf("  - %s:\n%s\n", p.PolicyName, p.PolicyDocument))
		}
	}

	return sb.String()
}

func formatPolicyList(policies []PolicyInfo) string {
	if len(policies) == 0 {
		return "No customer-managed IAM policies found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d customer-managed IAM policies:\n\n", len(policies)))
	sb.WriteString("NAME\tATTACHMENTS\tCREATED\n")
	for _, p := range policies {
		sb.WriteString(fmt.Sprintf("%s\t%d\t%s\n",
			p.PolicyName, p.AttachmentCount, p.CreateDate.Format("2006-01-02")))
	}
	return sb.String()
}

func formatPolicyDetail(detail *PolicyDetail) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Policy: %s\n", detail.PolicyName))
	sb.WriteString(fmt.Sprintf("ARN: %s\n", detail.PolicyARN))
	sb.WriteString(fmt.Sprintf("Path: %s\n", detail.Path))
	sb.WriteString(fmt.Sprintf("Attachment Count: %d\n", detail.AttachmentCount))
	sb.WriteString(fmt.Sprintf("Created: %s\n", detail.CreateDate.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("Updated: %s\n", detail.UpdateDate.Format("2006-01-02 15:04:05")))
	if detail.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", detail.Description))
	}

	sb.WriteString("\nPolicy Document:\n")
	// Pretty print the JSON
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(detail.PolicyDocument), "", "  "); err == nil {
		sb.WriteString(prettyJSON.String())
	} else {
		sb.WriteString(detail.PolicyDocument)
	}

	return sb.String()
}

func formatUserList(users []UserInfo) string {
	if len(users) == 0 {
		return "No IAM users found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d IAM users:\n\n", len(users)))
	sb.WriteString("NAME\tPATH\tCREATED\tPASSWORD_LAST_USED\n")
	for _, u := range users {
		lastUsed := "Never"
		if u.PasswordLastUsed != nil {
			lastUsed = u.PasswordLastUsed.Format("2006-01-02")
		}
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n",
			u.UserName, u.Path, u.CreateDate.Format("2006-01-02"), lastUsed))
	}
	return sb.String()
}

func formatUserDetail(user *UserInfo) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("User: %s\n", user.UserName))
	sb.WriteString(fmt.Sprintf("ARN: %s\n", user.UserARN))
	sb.WriteString(fmt.Sprintf("Path: %s\n", user.Path))
	sb.WriteString(fmt.Sprintf("Created: %s\n", user.CreateDate.Format("2006-01-02 15:04:05")))
	if user.PasswordLastUsed != nil {
		sb.WriteString(fmt.Sprintf("Password Last Used: %s\n", user.PasswordLastUsed.Format("2006-01-02 15:04:05")))
	}
	return sb.String()
}

func formatAccessKeys(keys []AccessKeyInfo) string {
	if len(keys) == 0 {
		return "No access keys found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d access keys:\n\n", len(keys)))
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("Key ID: %s\n", k.AccessKeyId))
		sb.WriteString(fmt.Sprintf("  Status: %s\n", k.Status))
		sb.WriteString(fmt.Sprintf("  Created: %s\n", k.CreateDate.Format("2006-01-02")))
		if k.LastUsedDate != nil {
			sb.WriteString(fmt.Sprintf("  Last Used: %s\n", k.LastUsedDate.Format("2006-01-02")))
			if k.LastUsedService != "" {
				sb.WriteString(fmt.Sprintf("  Last Service: %s\n", k.LastUsedService))
			}
			if k.LastUsedRegion != "" {
				sb.WriteString(fmt.Sprintf("  Last Region: %s\n", k.LastUsedRegion))
			}
		} else {
			sb.WriteString("  Last Used: Never\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func formatAccessKeyRotationStatus(report *CredentialReport) string {
	var sb strings.Builder
	sb.WriteString("Access Key Rotation Status:\n\n")

	now := time.Now()
	rotationThreshold := 90 * 24 * time.Hour // 90 days

	var needsRotation []string
	for _, u := range report.Users {
		if u.AccessKey1Active && u.AccessKey1LastRotated != nil {
			age := now.Sub(*u.AccessKey1LastRotated)
			if age > rotationThreshold {
				needsRotation = append(needsRotation, fmt.Sprintf("%s (Key 1: %d days old)", u.User, int(age.Hours()/24)))
			}
		}
		if u.AccessKey2Active && u.AccessKey2LastRotated != nil {
			age := now.Sub(*u.AccessKey2LastRotated)
			if age > rotationThreshold {
				needsRotation = append(needsRotation, fmt.Sprintf("%s (Key 2: %d days old)", u.User, int(age.Hours()/24)))
			}
		}
	}

	if len(needsRotation) == 0 {
		sb.WriteString("All active access keys are within rotation threshold (90 days)")
	} else {
		sb.WriteString(fmt.Sprintf("%d access keys need rotation:\n", len(needsRotation)))
		for _, k := range needsRotation {
			sb.WriteString(fmt.Sprintf("  - %s\n", k))
		}
	}

	return sb.String()
}

func formatCredentialReport(report *CredentialReport) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Credential Report (Generated: %s)\n\n", report.GeneratedTime.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("Total Users: %d\n\n", len(report.Users)))

	sb.WriteString("USER\tMFA\tPASSWORD\tKEY1\tKEY2\n")
	for _, u := range report.Users {
		mfa := "No"
		if u.MFAActive {
			mfa = "Yes"
		}
		pwd := "No"
		if u.PasswordEnabled {
			pwd = "Yes"
		}
		key1 := "No"
		if u.AccessKey1Active {
			key1 = "Yes"
		}
		key2 := "No"
		if u.AccessKey2Active {
			key2 = "Yes"
		}
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n", u.User, mfa, pwd, key1, key2))
	}

	return sb.String()
}

func formatGroupList(groups []GroupInfo) string {
	if len(groups) == 0 {
		return "No IAM groups found"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d IAM groups:\n\n", len(groups)))
	sb.WriteString("NAME\tPATH\tCREATED\n")
	for _, g := range groups {
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\n",
			g.GroupName, g.Path, g.CreateDate.Format("2006-01-02")))
	}
	return sb.String()
}

func formatGroupDetail(group *GroupInfo) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Group: %s\n", group.GroupName))
	sb.WriteString(fmt.Sprintf("ARN: %s\n", group.GroupARN))
	sb.WriteString(fmt.Sprintf("Path: %s\n", group.Path))
	sb.WriteString(fmt.Sprintf("Created: %s\n", group.CreateDate.Format("2006-01-02 15:04:05")))
	return sb.String()
}

func formatMFAStatus(report *CredentialReport) string {
	var sb strings.Builder
	sb.WriteString("MFA Status Report:\n\n")

	var withoutMFA []string
	var withMFA []string

	for _, u := range report.Users {
		if u.User == "<root_account>" {
			if !u.MFAActive {
				withoutMFA = append([]string{"ROOT ACCOUNT (CRITICAL)"}, withoutMFA...)
			}
			continue
		}
		if u.PasswordEnabled && !u.MFAActive {
			withoutMFA = append(withoutMFA, u.User)
		} else if u.MFAActive {
			withMFA = append(withMFA, u.User)
		}
	}

	sb.WriteString(fmt.Sprintf("Users with MFA enabled: %d\n", len(withMFA)))
	sb.WriteString(fmt.Sprintf("Users without MFA: %d\n\n", len(withoutMFA)))

	if len(withoutMFA) > 0 {
		sb.WriteString("Users requiring MFA:\n")
		for _, u := range withoutMFA {
			sb.WriteString(fmt.Sprintf("  - %s\n", u))
		}
	}

	return sb.String()
}

func formatPermissionAnalysis(detail *RoleDetail) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Permission Analysis for Role: %s\n\n", detail.RoleName))

	// List attached policies
	sb.WriteString("Attached Managed Policies:\n")
	for _, p := range detail.AttachedPolicies {
		sb.WriteString(fmt.Sprintf("  - %s\n", p.PolicyName))
	}

	// List inline policies with their documents
	sb.WriteString("\nInline Policy Permissions:\n")
	for _, p := range detail.InlinePolicies {
		sb.WriteString(fmt.Sprintf("\n[%s]:\n%s\n", p.PolicyName, p.PolicyDocument))
	}

	return sb.String()
}

// Security analysis functions

func (c *Client) findOverpermissivePolicies(ctx context.Context) (string, error) {
	policies, err := c.ListPolicies(ctx)
	if err != nil {
		return "", err
	}

	var findings []string
	for _, p := range policies {
		detail, err := c.GetPolicyDocument(ctx, p.PolicyARN)
		if err != nil {
			continue
		}

		doc, err := ParsePolicyDocument(detail.PolicyDocument)
		if err != nil {
			continue
		}

		for _, stmt := range doc.Statement {
			if stmt.Effect != "Allow" {
				continue
			}

			actions := toStringSlice(stmt.Action)
			resources := toStringSlice(stmt.Resource)

			for _, action := range actions {
				if action == "*" || strings.HasSuffix(action, ":*") {
					findings = append(findings, fmt.Sprintf("Policy %s has wildcard action: %s", p.PolicyName, action))
					break
				}
			}

			for _, resource := range resources {
				if resource == "*" {
					findings = append(findings, fmt.Sprintf("Policy %s has wildcard resource", p.PolicyName))
					break
				}
			}
		}
	}

	if len(findings) == 0 {
		return "No overpermissive policies found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d overpermissive policy issues:\n\n", len(findings)))
	for _, f := range findings {
		sb.WriteString(fmt.Sprintf("- %s\n", f))
	}
	return sb.String(), nil
}

func (c *Client) findAdminAccess(ctx context.Context) (string, error) {
	roles, err := c.ListRoles(ctx)
	if err != nil {
		return "", err
	}

	var adminRoles []string
	for _, r := range roles {
		detail, err := c.GetRoleDetails(ctx, r.RoleName)
		if err != nil {
			continue
		}

		for _, p := range detail.AttachedPolicies {
			if strings.Contains(p.PolicyARN, "AdministratorAccess") ||
				strings.Contains(p.PolicyName, "AdministratorAccess") {
				adminRoles = append(adminRoles, fmt.Sprintf("%s (via %s)", r.RoleName, p.PolicyName))
				break
			}
		}
	}

	if len(adminRoles) == 0 {
		return "No roles with AdministratorAccess found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d roles with administrator access:\n\n", len(adminRoles)))
	for _, r := range adminRoles {
		sb.WriteString(fmt.Sprintf("- %s\n", r))
	}
	return sb.String(), nil
}

func (c *Client) findUnusedRoles(ctx context.Context) (string, error) {
	roles, err := c.ListRoles(ctx)
	if err != nil {
		return "", err
	}

	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	var unusedRoles []string

	for _, r := range roles {
		detail, err := c.GetRoleDetails(ctx, r.RoleName)
		if err != nil {
			continue
		}

		if detail.LastUsed == nil || detail.LastUsed.Before(thirtyDaysAgo) {
			lastUsed := "Never"
			if detail.LastUsed != nil {
				lastUsed = detail.LastUsed.Format("2006-01-02")
			}
			unusedRoles = append(unusedRoles, fmt.Sprintf("%s (Last used: %s)", r.RoleName, lastUsed))
		}
	}

	if len(unusedRoles) == 0 {
		return "No unused roles found (all roles used within last 30 days)", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d roles not used in 30+ days:\n\n", len(unusedRoles)))
	for _, r := range unusedRoles {
		sb.WriteString(fmt.Sprintf("- %s\n", r))
	}
	return sb.String(), nil
}

func (c *Client) findCrossAccountTrusts(ctx context.Context) (string, error) {
	roles, err := c.ListRoles(ctx)
	if err != nil {
		return "", err
	}

	var crossAccountRoles []string
	myAccountID := c.GetAccountID()

	for _, r := range roles {
		if r.AssumeRolePolicyDocument == "" {
			continue
		}

		var trustPolicy TrustPolicy
		if err := json.Unmarshal([]byte(r.AssumeRolePolicyDocument), &trustPolicy); err != nil {
			continue
		}

		for _, stmt := range trustPolicy.Statement {
			principals := extractPrincipals(stmt.Principal)
			for _, p := range principals {
				if strings.Contains(p, "arn:aws:iam::") && !strings.Contains(p, myAccountID) {
					crossAccountRoles = append(crossAccountRoles, fmt.Sprintf("%s trusts %s", r.RoleName, p))
				}
			}
		}
	}

	if len(crossAccountRoles) == 0 {
		return "No cross-account trust relationships found", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d cross-account trust relationships:\n\n", len(crossAccountRoles)))
	for _, r := range crossAccountRoles {
		sb.WriteString(fmt.Sprintf("- %s\n", r))
	}
	return sb.String(), nil
}

// Helper functions

func (c *Client) getStringParam(params map[string]interface{}, key, defaultVal string) string {
	if params == nil {
		return defaultVal
	}
	if val, ok := params[key].(string); ok && val != "" {
		return val
	}
	return defaultVal
}

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
