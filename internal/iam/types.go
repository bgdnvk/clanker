package iam

import "time"

// IAMOperation represents an IAM operation requested by the LLM
type IAMOperation struct {
	Operation  string                 `json:"operation"`
	Reason     string                 `json:"reason"`
	Parameters map[string]interface{} `json:"parameters"`
}

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

// Severity levels for findings
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityInfo     = "info"
)

// Finding types
const (
	FindingOverpermissivePolicy  = "overpermissive_policy"
	FindingAdminAccess           = "admin_access"
	FindingWildcardResource      = "wildcard_resource"
	FindingUnusedRole            = "unused_role"
	FindingCrossAccountTrust     = "cross_account_trust"
	FindingMissingMFA            = "missing_mfa"
	FindingOldAccessKeys         = "old_access_keys"
	FindingInactiveKeys          = "inactive_keys"
	FindingRootAccountUsage      = "root_account_usage"
	FindingPublicS3Access        = "public_s3_access"
	FindingExcessivePermissions  = "excessive_permissions"
	FindingMissingResourceScoping = "missing_resource_scoping"
)

// FixPlan represents a plan to remediate a security finding
type FixPlan struct {
	ID        string       `json:"id"`
	Summary   string       `json:"summary"`
	Finding   SecurityFinding `json:"finding"`
	Commands  []FixCommand `json:"commands"`
	Notes     []string     `json:"notes,omitempty"`
	Warnings  []string     `json:"warnings,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

// FixCommand represents a single remediation command
type FixCommand struct {
	ID         string                 `json:"id"`
	Action     string                 `json:"action"`
	ResourceARN string                `json:"resource_arn"`
	Parameters map[string]interface{} `json:"parameters"`
	Reason     string                 `json:"reason"`
	Rollback   *FixCommand            `json:"rollback,omitempty"`
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

// QueryOptions configures how IAM queries are executed
type QueryOptions struct {
	AccountWide bool   `json:"account_wide"`
	RoleARN     string `json:"role_arn,omitempty"`
	PolicyARN   string `json:"policy_arn,omitempty"`
	UserName    string `json:"user_name,omitempty"`
	GroupName   string `json:"group_name,omitempty"`
}

// Response types
const (
	ResponseTypeResult   = "result"
	ResponseTypePlan     = "plan"
	ResponseTypeFindings = "findings"
	ResponseTypeError    = "error"
)

// Response represents the IAM agent response
type Response struct {
	Type     string            `json:"type"`
	Content  string            `json:"content,omitempty"`
	Plan     *FixPlan          `json:"plan,omitempty"`
	Findings []SecurityFinding `json:"findings,omitempty"`
	Error    error             `json:"error,omitempty"`
}

// RoleInfo contains basic role information
type RoleInfo struct {
	RoleName                 string            `json:"role_name"`
	RoleARN                  string            `json:"role_arn"`
	Path                     string            `json:"path"`
	CreateDate               time.Time         `json:"create_date"`
	Description              string            `json:"description,omitempty"`
	MaxSessionDuration       int32             `json:"max_session_duration"`
	AssumeRolePolicyDocument string            `json:"assume_role_policy_document"`
	Tags                     map[string]string `json:"tags,omitempty"`
}

// RoleDetail contains detailed role information including policies
type RoleDetail struct {
	RoleInfo
	AttachedPolicies []PolicyInfo   `json:"attached_policies"`
	InlinePolicies   []InlinePolicy `json:"inline_policies"`
	InstanceProfiles []string       `json:"instance_profiles,omitempty"`
	LastUsed         *time.Time     `json:"last_used,omitempty"`
}

// PolicyInfo contains basic policy information
type PolicyInfo struct {
	PolicyName       string    `json:"policy_name"`
	PolicyARN        string    `json:"policy_arn"`
	Path             string    `json:"path"`
	CreateDate       time.Time `json:"create_date"`
	UpdateDate       time.Time `json:"update_date"`
	AttachmentCount  int32     `json:"attachment_count"`
	IsAttachable     bool      `json:"is_attachable"`
	DefaultVersionId string    `json:"default_version_id"`
	Description      string    `json:"description,omitempty"`
}

// PolicyDetail contains detailed policy information including document
type PolicyDetail struct {
	PolicyInfo
	PolicyDocument string            `json:"policy_document"`
	Versions       []PolicyVersion   `json:"versions,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// PolicyVersion represents a specific version of an IAM policy
type PolicyVersion struct {
	VersionId        string    `json:"version_id"`
	IsDefaultVersion bool      `json:"is_default_version"`
	CreateDate       time.Time `json:"create_date"`
	Document         string    `json:"document,omitempty"`
}

// InlinePolicy represents an inline policy attached to a role
type InlinePolicy struct {
	PolicyName     string `json:"policy_name"`
	PolicyDocument string `json:"policy_document"`
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

// UserInfo contains basic user information
type UserInfo struct {
	UserName         string            `json:"user_name"`
	UserARN          string            `json:"user_arn"`
	Path             string            `json:"path"`
	CreateDate       time.Time         `json:"create_date"`
	PasswordLastUsed *time.Time        `json:"password_last_used,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
}

// AccessKeyInfo contains access key metadata
type AccessKeyInfo struct {
	UserName        string    `json:"user_name"`
	AccessKeyId     string    `json:"access_key_id"`
	Status          string    `json:"status"`
	CreateDate      time.Time `json:"create_date"`
	LastUsedDate    *time.Time `json:"last_used_date,omitempty"`
	LastUsedService string    `json:"last_used_service,omitempty"`
	LastUsedRegion  string    `json:"last_used_region,omitempty"`
}

// CredentialReport represents an IAM credential report
type CredentialReport struct {
	GeneratedTime time.Time               `json:"generated_time"`
	Users         []CredentialReportEntry `json:"users"`
}

// CredentialReportEntry represents a single user entry in the credential report
type CredentialReportEntry struct {
	User                      string     `json:"user"`
	ARN                       string     `json:"arn"`
	UserCreationTime          time.Time  `json:"user_creation_time"`
	PasswordEnabled           bool       `json:"password_enabled"`
	PasswordLastUsed          *time.Time `json:"password_last_used,omitempty"`
	PasswordLastChanged       *time.Time `json:"password_last_changed,omitempty"`
	PasswordNextRotation      *time.Time `json:"password_next_rotation,omitempty"`
	MFAActive                 bool       `json:"mfa_active"`
	AccessKey1Active          bool       `json:"access_key_1_active"`
	AccessKey1LastRotated     *time.Time `json:"access_key_1_last_rotated,omitempty"`
	AccessKey1LastUsedDate    *time.Time `json:"access_key_1_last_used_date,omitempty"`
	AccessKey1LastUsedRegion  string     `json:"access_key_1_last_used_region,omitempty"`
	AccessKey1LastUsedService string     `json:"access_key_1_last_used_service,omitempty"`
	AccessKey2Active          bool       `json:"access_key_2_active"`
	AccessKey2LastRotated     *time.Time `json:"access_key_2_last_rotated,omitempty"`
	AccessKey2LastUsedDate    *time.Time `json:"access_key_2_last_used_date,omitempty"`
	AccessKey2LastUsedRegion  string     `json:"access_key_2_last_used_region,omitempty"`
	AccessKey2LastUsedService string     `json:"access_key_2_last_used_service,omitempty"`
}

// GroupInfo contains basic group information
type GroupInfo struct {
	GroupName  string    `json:"group_name"`
	GroupARN   string    `json:"group_arn"`
	Path       string    `json:"path"`
	CreateDate time.Time `json:"create_date"`
}

// AccountSummary represents a summary of IAM resources in an account
type AccountSummary struct {
	RoleCount       int `json:"role_count"`
	PolicyCount     int `json:"policy_count"`
	UserCount       int `json:"user_count"`
	GroupCount      int `json:"group_count"`
	InstanceProfiles int `json:"instance_profiles"`
	MFADevices      int `json:"mfa_devices"`
	AccessKeys      int `json:"access_keys"`
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
