package iam

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Client wraps the AWS IAM SDK client
type Client struct {
	iam       *iam.Client
	sts       *sts.Client
	profile   string
	region    string
	accountID string
	debug     bool
}

// NewClient creates a new IAM client with the specified profile and region
func NewClient(profile, region string, debug bool) (*Client, error) {
	ctx := context.Background()

	opts := []func(*config.LoadOptions) error{}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := &Client{
		iam:     iam.NewFromConfig(cfg),
		sts:     sts.NewFromConfig(cfg),
		profile: profile,
		region:  region,
		debug:   debug,
	}

	// Get account ID
	callerIdentity, err := client.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err == nil && callerIdentity.Account != nil {
		client.accountID = *callerIdentity.Account
	}

	return client, nil
}

// GetAccountID returns the AWS account ID
func (c *Client) GetAccountID() string {
	return c.accountID
}

// ListRoles returns all IAM roles in the account
func (c *Client) ListRoles(ctx context.Context) ([]RoleInfo, error) {
	paginator := iam.NewListRolesPaginator(c.iam, &iam.ListRolesInput{})

	var roles []RoleInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return roles, fmt.Errorf("failed to list roles: %w", err)
		}

		for _, role := range page.Roles {
			if role.RoleName == nil || role.Arn == nil {
				continue
			}

			roleInfo := RoleInfo{
				RoleName:           aws.ToString(role.RoleName),
				RoleARN:            aws.ToString(role.Arn),
				Path:               aws.ToString(role.Path),
				MaxSessionDuration: aws.ToInt32(role.MaxSessionDuration),
			}

			if role.CreateDate != nil {
				roleInfo.CreateDate = *role.CreateDate
			}
			if role.Description != nil {
				roleInfo.Description = *role.Description
			}
			if role.AssumeRolePolicyDocument != nil {
				roleInfo.AssumeRolePolicyDocument = decodeDocument(*role.AssumeRolePolicyDocument)
			}

			roleInfo.Tags = convertTags(role.Tags)
			roles = append(roles, roleInfo)
		}
	}

	return roles, nil
}

// ListPolicies returns all customer-managed IAM policies
func (c *Client) ListPolicies(ctx context.Context) ([]PolicyInfo, error) {
	paginator := iam.NewListPoliciesPaginator(c.iam, &iam.ListPoliciesInput{
		Scope: types.PolicyScopeTypeLocal,
	})

	var policies []PolicyInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return policies, fmt.Errorf("failed to list policies: %w", err)
		}

		for _, policy := range page.Policies {
			if policy.PolicyName == nil || policy.Arn == nil {
				continue
			}

			policyInfo := PolicyInfo{
				PolicyName:       aws.ToString(policy.PolicyName),
				PolicyARN:        aws.ToString(policy.Arn),
				Path:             aws.ToString(policy.Path),
				AttachmentCount:  aws.ToInt32(policy.AttachmentCount),
				IsAttachable:     policy.IsAttachable,
				DefaultVersionId: aws.ToString(policy.DefaultVersionId),
			}

			if policy.CreateDate != nil {
				policyInfo.CreateDate = *policy.CreateDate
			}
			if policy.UpdateDate != nil {
				policyInfo.UpdateDate = *policy.UpdateDate
			}
			if policy.Description != nil {
				policyInfo.Description = *policy.Description
			}

			policies = append(policies, policyInfo)
		}
	}

	return policies, nil
}

// GetRoleDetails returns detailed information about a specific role
func (c *Client) GetRoleDetails(ctx context.Context, roleName string) (*RoleDetail, error) {
	roleResp, err := c.iam.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get role %s: %w", roleName, err)
	}

	role := roleResp.Role
	detail := &RoleDetail{
		RoleInfo: RoleInfo{
			RoleName:           aws.ToString(role.RoleName),
			RoleARN:            aws.ToString(role.Arn),
			Path:               aws.ToString(role.Path),
			MaxSessionDuration: aws.ToInt32(role.MaxSessionDuration),
		},
	}

	if role.CreateDate != nil {
		detail.CreateDate = *role.CreateDate
	}
	if role.Description != nil {
		detail.Description = *role.Description
	}
	if role.AssumeRolePolicyDocument != nil {
		detail.AssumeRolePolicyDocument = decodeDocument(*role.AssumeRolePolicyDocument)
	}
	detail.Tags = convertTags(role.Tags)

	if role.RoleLastUsed != nil && role.RoleLastUsed.LastUsedDate != nil {
		detail.LastUsed = role.RoleLastUsed.LastUsedDate
	}

	// Get attached policies
	attachedResp, err := c.iam.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	if err == nil {
		for _, policy := range attachedResp.AttachedPolicies {
			detail.AttachedPolicies = append(detail.AttachedPolicies, PolicyInfo{
				PolicyName: aws.ToString(policy.PolicyName),
				PolicyARN:  aws.ToString(policy.PolicyArn),
			})
		}
	}

	// Get inline policies
	inlineResp, err := c.iam.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	if err == nil {
		for _, policyName := range inlineResp.PolicyNames {
			policyResp, err := c.iam.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
				RoleName:   aws.String(roleName),
				PolicyName: aws.String(policyName),
			})
			if err == nil && policyResp.PolicyDocument != nil {
				detail.InlinePolicies = append(detail.InlinePolicies, InlinePolicy{
					PolicyName:     policyName,
					PolicyDocument: decodeDocument(*policyResp.PolicyDocument),
				})
			}
		}
	}

	// Get instance profiles
	profilesResp, err := c.iam.ListInstanceProfilesForRole(ctx, &iam.ListInstanceProfilesForRoleInput{
		RoleName: aws.String(roleName),
	})
	if err == nil {
		for _, profile := range profilesResp.InstanceProfiles {
			if profile.InstanceProfileName != nil {
				detail.InstanceProfiles = append(detail.InstanceProfiles, *profile.InstanceProfileName)
			}
		}
	}

	return detail, nil
}

// GetPolicyDocument returns the policy document for a managed policy
func (c *Client) GetPolicyDocument(ctx context.Context, policyARN string) (*PolicyDetail, error) {
	policyResp, err := c.iam.GetPolicy(ctx, &iam.GetPolicyInput{
		PolicyArn: aws.String(policyARN),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get policy %s: %w", policyARN, err)
	}

	policy := policyResp.Policy
	detail := &PolicyDetail{
		PolicyInfo: PolicyInfo{
			PolicyName:       aws.ToString(policy.PolicyName),
			PolicyARN:        aws.ToString(policy.Arn),
			Path:             aws.ToString(policy.Path),
			AttachmentCount:  aws.ToInt32(policy.AttachmentCount),
			IsAttachable:     policy.IsAttachable,
			DefaultVersionId: aws.ToString(policy.DefaultVersionId),
		},
	}

	if policy.CreateDate != nil {
		detail.CreateDate = *policy.CreateDate
	}
	if policy.UpdateDate != nil {
		detail.UpdateDate = *policy.UpdateDate
	}
	if policy.Description != nil {
		detail.Description = *policy.Description
	}

	detail.Tags = make(map[string]string)
	for _, tag := range policy.Tags {
		if tag.Key != nil && tag.Value != nil {
			detail.Tags[*tag.Key] = *tag.Value
		}
	}

	// Get the default policy version document
	if policy.DefaultVersionId != nil {
		versionResp, err := c.iam.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
			PolicyArn: aws.String(policyARN),
			VersionId: policy.DefaultVersionId,
		})
		if err == nil && versionResp.PolicyVersion != nil && versionResp.PolicyVersion.Document != nil {
			detail.PolicyDocument = decodeDocument(*versionResp.PolicyVersion.Document)
		}
	}

	// Get all versions
	versionsResp, err := c.iam.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{
		PolicyArn: aws.String(policyARN),
	})
	if err == nil {
		for _, v := range versionsResp.Versions {
			pv := PolicyVersion{
				VersionId:        aws.ToString(v.VersionId),
				IsDefaultVersion: v.IsDefaultVersion,
			}
			if v.CreateDate != nil {
				pv.CreateDate = *v.CreateDate
			}
			detail.Versions = append(detail.Versions, pv)
		}
	}

	return detail, nil
}

// GetRolePolicyDocument returns the policy document for an inline policy
func (c *Client) GetRolePolicyDocument(ctx context.Context, roleName, policyName string) (string, error) {
	resp, err := c.iam.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String(policyName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get role policy %s/%s: %w", roleName, policyName, err)
	}

	if resp.PolicyDocument == nil {
		return "", fmt.Errorf("policy document is nil")
	}

	return decodeDocument(*resp.PolicyDocument), nil
}

// ListUsers returns all IAM users
func (c *Client) ListUsers(ctx context.Context) ([]UserInfo, error) {
	paginator := iam.NewListUsersPaginator(c.iam, &iam.ListUsersInput{})

	var users []UserInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return users, fmt.Errorf("failed to list users: %w", err)
		}

		for _, user := range page.Users {
			if user.UserName == nil || user.Arn == nil {
				continue
			}

			userInfo := UserInfo{
				UserName: aws.ToString(user.UserName),
				UserARN:  aws.ToString(user.Arn),
				Path:     aws.ToString(user.Path),
			}

			if user.CreateDate != nil {
				userInfo.CreateDate = *user.CreateDate
			}
			if user.PasswordLastUsed != nil {
				userInfo.PasswordLastUsed = user.PasswordLastUsed
			}

			userInfo.Tags = convertTags(user.Tags)
			users = append(users, userInfo)
		}
	}

	return users, nil
}

// ListAccessKeys returns access keys for a user
func (c *Client) ListAccessKeys(ctx context.Context, userName string) ([]AccessKeyInfo, error) {
	resp, err := c.iam.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list access keys for %s: %w", userName, err)
	}

	var keys []AccessKeyInfo
	for _, key := range resp.AccessKeyMetadata {
		keyInfo := AccessKeyInfo{
			UserName:    aws.ToString(key.UserName),
			AccessKeyId: aws.ToString(key.AccessKeyId),
			Status:      string(key.Status),
		}

		if key.CreateDate != nil {
			keyInfo.CreateDate = *key.CreateDate
		}

		// Get last used info
		lastUsedResp, err := c.iam.GetAccessKeyLastUsed(ctx, &iam.GetAccessKeyLastUsedInput{
			AccessKeyId: key.AccessKeyId,
		})
		if err == nil && lastUsedResp.AccessKeyLastUsed != nil {
			if lastUsedResp.AccessKeyLastUsed.LastUsedDate != nil {
				keyInfo.LastUsedDate = lastUsedResp.AccessKeyLastUsed.LastUsedDate
			}
			keyInfo.LastUsedService = aws.ToString(lastUsedResp.AccessKeyLastUsed.ServiceName)
			keyInfo.LastUsedRegion = aws.ToString(lastUsedResp.AccessKeyLastUsed.Region)
		}

		keys = append(keys, keyInfo)
	}

	return keys, nil
}

// GetCredentialReport generates and returns the credential report
func (c *Client) GetCredentialReport(ctx context.Context) (*CredentialReport, error) {
	// Generate credential report
	for i := 0; i < 10; i++ {
		_, err := c.iam.GenerateCredentialReport(ctx, &iam.GenerateCredentialReportInput{})
		if err == nil {
			break
		}

		// Wait and retry
		time.Sleep(time.Second * 2)
	}

	// Get the report
	resp, err := c.iam.GetCredentialReport(ctx, &iam.GetCredentialReportInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get credential report: %w", err)
	}

	report := &CredentialReport{}
	if resp.GeneratedTime != nil {
		report.GeneratedTime = *resp.GeneratedTime
	}

	// Parse CSV content
	reader := csv.NewReader(strings.NewReader(string(resp.Content)))
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse credential report: %w", err)
	}

	if len(records) < 2 {
		return report, nil
	}

	// Map header to indices
	headerMap := make(map[string]int)
	for i, h := range records[0] {
		headerMap[h] = i
	}

	// Parse entries
	for _, row := range records[1:] {
		entry := CredentialReportEntry{
			User: getCSVField(row, headerMap, "user"),
			ARN:  getCSVField(row, headerMap, "arn"),
		}

		entry.UserCreationTime = parseCSVTime(getCSVField(row, headerMap, "user_creation_time"))
		entry.PasswordEnabled = getCSVField(row, headerMap, "password_enabled") == "true"
		entry.MFAActive = getCSVField(row, headerMap, "mfa_active") == "true"
		entry.AccessKey1Active = getCSVField(row, headerMap, "access_key_1_active") == "true"
		entry.AccessKey2Active = getCSVField(row, headerMap, "access_key_2_active") == "true"

		if t := parseCSVTime(getCSVField(row, headerMap, "password_last_used")); !t.IsZero() {
			entry.PasswordLastUsed = &t
		}
		if t := parseCSVTime(getCSVField(row, headerMap, "password_last_changed")); !t.IsZero() {
			entry.PasswordLastChanged = &t
		}
		if t := parseCSVTime(getCSVField(row, headerMap, "access_key_1_last_rotated")); !t.IsZero() {
			entry.AccessKey1LastRotated = &t
		}
		if t := parseCSVTime(getCSVField(row, headerMap, "access_key_1_last_used_date")); !t.IsZero() {
			entry.AccessKey1LastUsedDate = &t
		}
		if t := parseCSVTime(getCSVField(row, headerMap, "access_key_2_last_rotated")); !t.IsZero() {
			entry.AccessKey2LastRotated = &t
		}
		if t := parseCSVTime(getCSVField(row, headerMap, "access_key_2_last_used_date")); !t.IsZero() {
			entry.AccessKey2LastUsedDate = &t
		}

		entry.AccessKey1LastUsedRegion = getCSVField(row, headerMap, "access_key_1_last_used_region")
		entry.AccessKey1LastUsedService = getCSVField(row, headerMap, "access_key_1_last_used_service")
		entry.AccessKey2LastUsedRegion = getCSVField(row, headerMap, "access_key_2_last_used_region")
		entry.AccessKey2LastUsedService = getCSVField(row, headerMap, "access_key_2_last_used_service")

		report.Users = append(report.Users, entry)
	}

	return report, nil
}

// GetAccountSummary returns IAM account summary statistics
func (c *Client) GetAccountSummary(ctx context.Context) (*AccountSummary, error) {
	resp, err := c.iam.GetAccountSummary(ctx, &iam.GetAccountSummaryInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get account summary: %w", err)
	}

	summary := &AccountSummary{}
	if val, ok := resp.SummaryMap["Roles"]; ok {
		summary.RoleCount = int(val)
	}
	if val, ok := resp.SummaryMap["Policies"]; ok {
		summary.PolicyCount = int(val)
	}
	if val, ok := resp.SummaryMap["Users"]; ok {
		summary.UserCount = int(val)
	}
	if val, ok := resp.SummaryMap["Groups"]; ok {
		summary.GroupCount = int(val)
	}
	if val, ok := resp.SummaryMap["InstanceProfiles"]; ok {
		summary.InstanceProfiles = int(val)
	}
	if val, ok := resp.SummaryMap["MFADevices"]; ok {
		summary.MFADevices = int(val)
	}
	if val, ok := resp.SummaryMap["AccessKeysPerUserQuota"]; ok {
		summary.AccessKeys = int(val)
	}

	return summary, nil
}

// ListGroups returns all IAM groups
func (c *Client) ListGroups(ctx context.Context) ([]GroupInfo, error) {
	paginator := iam.NewListGroupsPaginator(c.iam, &iam.ListGroupsInput{})

	var groups []GroupInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return groups, fmt.Errorf("failed to list groups: %w", err)
		}

		for _, group := range page.Groups {
			if group.GroupName == nil || group.Arn == nil {
				continue
			}

			groupInfo := GroupInfo{
				GroupName: aws.ToString(group.GroupName),
				GroupARN:  aws.ToString(group.Arn),
				Path:      aws.ToString(group.Path),
			}

			if group.CreateDate != nil {
				groupInfo.CreateDate = *group.CreateDate
			}

			groups = append(groups, groupInfo)
		}
	}

	return groups, nil
}

// CreatePolicyVersion creates a new version of an IAM policy
func (c *Client) CreatePolicyVersion(ctx context.Context, policyARN, document string, setAsDefault bool) error {
	_, err := c.iam.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{
		PolicyArn:      aws.String(policyARN),
		PolicyDocument: aws.String(document),
		SetAsDefault:   setAsDefault,
	})
	if err != nil {
		return fmt.Errorf("failed to create policy version: %w", err)
	}
	return nil
}

// DeletePolicyVersion deletes a specific version of an IAM policy
func (c *Client) DeletePolicyVersion(ctx context.Context, policyARN, versionID string) error {
	_, err := c.iam.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{
		PolicyArn: aws.String(policyARN),
		VersionId: aws.String(versionID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete policy version: %w", err)
	}
	return nil
}

// UpdateAssumeRolePolicy updates the trust policy for a role
func (c *Client) UpdateAssumeRolePolicy(ctx context.Context, roleName, document string) error {
	_, err := c.iam.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyDocument: aws.String(document),
	})
	if err != nil {
		return fmt.Errorf("failed to update assume role policy: %w", err)
	}
	return nil
}

// AttachRolePolicy attaches a managed policy to a role
func (c *Client) AttachRolePolicy(ctx context.Context, roleName, policyARN string) error {
	_, err := c.iam.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyARN),
	})
	if err != nil {
		return fmt.Errorf("failed to attach policy to role: %w", err)
	}
	return nil
}

// DetachRolePolicy detaches a managed policy from a role
func (c *Client) DetachRolePolicy(ctx context.Context, roleName, policyARN string) error {
	_, err := c.iam.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(policyARN),
	})
	if err != nil {
		return fmt.Errorf("failed to detach policy from role: %w", err)
	}
	return nil
}

// UpdateAccessKey updates the status of an access key
func (c *Client) UpdateAccessKey(ctx context.Context, userName, accessKeyID, status string) error {
	var keyStatus types.StatusType
	switch strings.ToLower(status) {
	case "active":
		keyStatus = types.StatusTypeActive
	case "inactive":
		keyStatus = types.StatusTypeInactive
	default:
		return fmt.Errorf("invalid status: %s", status)
	}

	_, err := c.iam.UpdateAccessKey(ctx, &iam.UpdateAccessKeyInput{
		UserName:    aws.String(userName),
		AccessKeyId: aws.String(accessKeyID),
		Status:      keyStatus,
	})
	if err != nil {
		return fmt.Errorf("failed to update access key: %w", err)
	}
	return nil
}

// ParsePolicyDocument parses a JSON policy document string
func ParsePolicyDocument(document string) (*PolicyDocument, error) {
	var doc PolicyDocument
	if err := json.Unmarshal([]byte(document), &doc); err != nil {
		return nil, fmt.Errorf("failed to parse policy document: %w", err)
	}
	return &doc, nil
}

// Helper functions

func decodeDocument(doc string) string {
	if doc == "" {
		return doc
	}
	decoded, err := url.QueryUnescape(doc)
	if err != nil {
		return doc
	}
	return decoded
}

func convertTags(tags []types.Tag) map[string]string {
	result := make(map[string]string)
	for _, tag := range tags {
		if tag.Key != nil && tag.Value != nil {
			result[*tag.Key] = *tag.Value
		}
	}
	return result
}

func getCSVField(row []string, headerMap map[string]int, field string) string {
	if idx, ok := headerMap[field]; ok && idx < len(row) {
		return row[idx]
	}
	return ""
}

func parseCSVTime(s string) time.Time {
	if s == "" || s == "N/A" || s == "not_supported" || s == "no_information" {
		return time.Time{}
	}

	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05+00:00",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}

	return time.Time{}
}
