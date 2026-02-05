package backend

import "time"

// AWSCredentials represents AWS credentials stored in the backend
type AWSCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
}

// GCPCredentials represents GCP credentials stored in the backend
type GCPCredentials struct {
	ProjectID          string `json:"project_id"`
	ServiceAccountJSON string `json:"service_account_json,omitempty"`
}

// CloudflareCredentials represents Cloudflare credentials stored in the backend
type CloudflareCredentials struct {
	APIToken  string `json:"api_token"`
	AccountID string `json:"account_id,omitempty"`
	ZoneID    string `json:"zone_id,omitempty"`
}

// KubernetesCredentials represents Kubernetes credentials stored in the backend
type KubernetesCredentials struct {
	KubeconfigContent string `json:"kubeconfig_content"`
	ContextName       string `json:"context_name,omitempty"`
}

// AzureCredentials represents Azure credentials stored in the backend
type AzureCredentials struct {
	SubscriptionID string `json:"subscription_id"`
	TenantID       string `json:"tenant_id,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	ClientSecret   string `json:"client_secret,omitempty"`
}

// CredentialProvider represents supported credential providers
type CredentialProvider string

const (
	ProviderAWS        CredentialProvider = "aws"
	ProviderGCP        CredentialProvider = "gcp"
	ProviderAzure      CredentialProvider = "azure"
	ProviderCloudflare CredentialProvider = "cloudflare"
	ProviderKubernetes CredentialProvider = "kubernetes"
)

// CredentialEntry represents a stored credential in the backend
type CredentialEntry struct {
	Provider  CredentialProvider `json:"provider"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Masked    map[string]string  `json:"masked,omitempty"`
}

// CredentialListResponse represents the response from listing credentials
type CredentialListResponse struct {
	Success bool              `json:"success"`
	Data    []CredentialEntry `json:"data"`
}

// CredentialRawResponse represents the response from getting raw credentials
type CredentialRawResponse struct {
	Success bool            `json:"success"`
	Data    CredentialEntry `json:"data"`
}

// APIResponse represents a generic API response
type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}
