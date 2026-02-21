package analyzer

import (
	"encoding/json"
	"time"
)

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

// ParsePolicyDocument parses a JSON policy document string
func ParsePolicyDocument(document string) (*PolicyDocument, error) {
	var doc PolicyDocument
	if err := parseJSON(document, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func parseJSON(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}
