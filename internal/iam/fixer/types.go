package fixer

import "time"

// SecurityFinding represents a security issue found during IAM analysis
type SecurityFinding struct {
	ID          string   `json:"id"`
	Severity    string   `json:"severity"`
	Type        string   `json:"type"`
	ResourceARN string   `json:"resource_arn"`
	Description string   `json:"description"`
	Remediation string   `json:"remediation"`
	Actions     []string `json:"actions,omitempty"`
	Resources   []string `json:"resources,omitempty"`
}

// FixPlan represents a plan to remediate a security finding
type FixPlan struct {
	ID        string          `json:"id"`
	Summary   string          `json:"summary"`
	Finding   SecurityFinding `json:"finding"`
	Commands  []FixCommand    `json:"commands"`
	Notes     []string        `json:"notes,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// FixCommand represents a single remediation command
type FixCommand struct {
	ID          string                 `json:"id"`
	Action      string                 `json:"action"`
	ResourceARN string                 `json:"resource_arn"`
	Parameters  map[string]interface{} `json:"parameters"`
	Reason      string                 `json:"reason"`
	Rollback    *FixCommand            `json:"rollback,omitempty"`
}

// Action types for fix commands
const (
	ActionUpdatePolicy        = "update_policy"
	ActionCreatePolicyVersion = "create_policy_version"
	ActionAttachPolicy        = "attach_policy"
	ActionDetachPolicy        = "detach_policy"
	ActionDeletePolicyVersion = "delete_policy_version"
	ActionDeactivateAccessKey = "deactivate_access_key"
	ActionDeleteAccessKey     = "delete_access_key"
	ActionRotateAccessKey     = "rotate_access_key"
	ActionUpdateTrustPolicy   = "update_trust_policy"
)

// Finding types
const (
	FindingOverpermissivePolicy   = "overpermissive_policy"
	FindingAdminAccess            = "admin_access"
	FindingWildcardResource       = "wildcard_resource"
	FindingUnusedRole             = "unused_role"
	FindingCrossAccountTrust      = "cross_account_trust"
	FindingMissingMFA             = "missing_mfa"
	FindingOldAccessKeys          = "old_access_keys"
	FindingInactiveKeys           = "inactive_keys"
	FindingRootAccountUsage       = "root_account_usage"
	FindingPublicS3Access         = "public_s3_access"
	FindingExcessivePermissions   = "excessive_permissions"
	FindingMissingResourceScoping = "missing_resource_scoping"
)

// RoleDetail contains detailed role information including policies
type RoleDetail struct {
	RoleName                 string            `json:"role_name"`
	RoleARN                  string            `json:"role_arn"`
	AssumeRolePolicyDocument string            `json:"assume_role_policy_document"`
	AttachedPolicies         []PolicyInfo      `json:"attached_policies"`
	InlinePolicies           []InlinePolicy    `json:"inline_policies"`
	LastUsed                 *time.Time        `json:"last_used,omitempty"`
	Tags                     map[string]string `json:"tags,omitempty"`
}

// PolicyInfo contains basic policy information
type PolicyInfo struct {
	PolicyName string `json:"policy_name"`
	PolicyARN  string `json:"policy_arn"`
}

// InlinePolicy represents an inline policy attached to a role
type InlinePolicy struct {
	PolicyName     string `json:"policy_name"`
	PolicyDocument string `json:"policy_document"`
}

// PolicyDetail contains detailed policy information including document
type PolicyDetail struct {
	PolicyDocument string `json:"policy_document"`
}

// AccessKeyInfo contains access key metadata
type AccessKeyInfo struct {
	UserName        string     `json:"user_name"`
	AccessKeyId     string     `json:"access_key_id"`
	Status          string     `json:"status"`
	CreateDate      time.Time  `json:"create_date"`
	LastUsedDate    *time.Time `json:"last_used_date,omitempty"`
	LastUsedService string     `json:"last_used_service,omitempty"`
	LastUsedRegion  string     `json:"last_used_region,omitempty"`
}

// IAMClient interface defines the methods needed from the IAM client
type IAMClient interface {
	GetRoleDetails(ctx interface{}, roleName string) (*RoleDetail, error)
	GetPolicyDocument(ctx interface{}, policyARN string) (*PolicyDetail, error)
	ListAccessKeys(ctx interface{}, userName string) ([]AccessKeyInfo, error)
	CreatePolicyVersion(ctx interface{}, policyARN, document string, setAsDefault bool) error
	UpdateAssumeRolePolicy(ctx interface{}, roleName, document string) error
	AttachRolePolicy(ctx interface{}, roleName, policyARN string) error
	DetachRolePolicy(ctx interface{}, roleName, policyARN string) error
	UpdateAccessKey(ctx interface{}, userName, accessKeyID, status string) error
}

// TrustPolicy represents a parsed trust policy
type TrustPolicy struct {
	Version   string           `json:"Version"`
	Statement []TrustStatement `json:"Statement"`
}

// TrustStatement represents a statement in a trust policy
type TrustStatement struct {
	Effect    string      `json:"Effect"`
	Principal interface{} `json:"Principal"`
	Action    interface{} `json:"Action"`
	Condition interface{} `json:"Condition,omitempty"`
}

// PolicyDocument represents a parsed IAM policy document
type PolicyDocument struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

// Statement represents a single statement in an IAM policy
type Statement struct {
	Sid       string      `json:"Sid,omitempty"`
	Effect    string      `json:"Effect"`
	Principal interface{} `json:"Principal,omitempty"`
	Action    interface{} `json:"Action"`
	Resource  interface{} `json:"Resource"`
	Condition interface{} `json:"Condition,omitempty"`
}
