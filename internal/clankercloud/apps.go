package clankercloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var appIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

type AppsClient struct {
	httpClient *http.Client
	baseURL    string
	accountKey string
}

type AppsClientOptions struct {
	BaseURL    string
	AccountKey string
	HTTPClient *http.Client
}

type AppsAPIResult = APIResult

type AppCreateRequest struct {
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	ProjectID      string         `json:"projectId,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	IdempotencyKey string         `json:"-"`
}

type AppDeploymentCreateRequest struct {
	AppSpec        AppSpec `json:"appSpec"`
	IdempotencyKey string  `json:"-"`
}

// Avoid inflating canonical display text such as "<" into six-byte JSON escape
// sequences before the server applies the same 256 KiB raw appSpec limit.
func (AppDeploymentCreateRequest) escapeHTMLInCloudJSON() bool {
	return false
}

func NewAppsClient(opts AppsClientOptions) *AppsClient {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	configuredAppsBaseURL := os.Getenv("CLANKER_CLOUD_APPS_API_BASE_URL")
	configuredSandboxBaseURL := os.Getenv("CLANKER_CLOUD_SANDBOX_API_BASE_URL")
	configuredLegacyBaseURL := os.Getenv("CLANKER_SANDBOX_API_BASE_URL")
	accountKey := strings.TrimSpace(opts.AccountKey)
	if configuredCredentialAllowed(opts.BaseURL, configuredAppsBaseURL, configuredSandboxBaseURL, configuredLegacyBaseURL) {
		accountKey = firstNonEmpty(accountKey, os.Getenv("CLANKER_CLOUD_API_KEY"))
	}
	return &AppsClient{
		httpClient: httpClient,
		baseURL: firstNonEmpty(
			opts.BaseURL,
			configuredAppsBaseURL,
			configuredSandboxBaseURL,
			configuredLegacyBaseURL,
			DefaultCloudAPIBaseURL,
		),
		accountKey: accountKey,
	}
}

func (c *AppsClient) BaseURL() (string, error) {
	return NormalizeCloudAPIBaseURL(c.baseURL)
}

func (c *AppsClient) AccountKey() string {
	return strings.TrimSpace(c.accountKey)
}

func (c *AppsClient) ListApps(ctx context.Context) (*AppsAPIResult, error) {
	return c.callJSON(ctx, http.MethodGet, "/apps", nil, "")
}

func (c *AppsClient) CreateApp(ctx context.Context, payload AppCreateRequest) (*AppsAPIResult, error) {
	if strings.TrimSpace(payload.Name) == "" {
		return nil, fmt.Errorf("app name is required")
	}
	idempotencyKey, err := ValidateAppIdempotencyKey(payload.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	payload.Name = strings.TrimSpace(payload.Name)
	payload.Description = strings.TrimSpace(payload.Description)
	payload.ProjectID = strings.TrimSpace(payload.ProjectID)
	return c.callJSON(ctx, http.MethodPost, "/apps", payload, idempotencyKey)
}

func (c *AppsClient) GetApp(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodGet, "/apps/"+escaped, nil, "")
}

func (c *AppsClient) DeleteApp(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodDelete, "/apps/"+escaped, nil, "")
}

func (c *AppsClient) ListDeployments(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodGet, "/apps/"+escaped+"/deployments", nil, "")
}

func (c *AppsClient) CreateDeployment(ctx context.Context, appID string, payload AppDeploymentCreateRequest) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	payload.AppSpec = normalizeAppSpec(payload.AppSpec)
	if err := ValidateAppSpec(payload.AppSpec); err != nil {
		return nil, err
	}
	idempotencyKey, err := ValidateAppIdempotencyKey(payload.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodPost, "/apps/"+escaped+"/deployments", payload, idempotencyKey)
}

func (c *AppsClient) ActivateDeployment(ctx context.Context, appID string, deploymentID string) (*AppsAPIResult, error) {
	escapedAppID, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	escapedDeploymentID, err := requiredCloudID("deployment", deploymentID)
	if err != nil {
		return nil, err
	}
	path := "/apps/" + escapedAppID + "/deployments/" + escapedDeploymentID + "/activate"
	return c.callJSON(ctx, http.MethodPost, path, map[string]any{}, "")
}

func (c *AppsClient) UnpublishApp(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodPost, "/apps/"+escaped+"/unpublish", map[string]any{}, "")
}

func (c *AppsClient) callJSON(ctx context.Context, method string, path string, body any, idempotencyKey string) (*AppsAPIResult, error) {
	baseURL, err := c.BaseURL()
	if err != nil {
		return nil, err
	}
	if c.AccountKey() == "" {
		return nil, fmt.Errorf("Clanker Cloud account API key is required")
	}
	headers := make(http.Header)
	if key := strings.TrimSpace(idempotencyKey); key != "" {
		headers.Set("Idempotency-Key", key)
	}
	result, _, err := callCloudJSON(ctx, c.httpClient, baseURL, method, path, c.AccountKey(), body, headers)
	return result, err
}

func AppsResultOK(result *AppsAPIResult) bool {
	return CloudResultOK(result) || cloudDeleteResourceNotFound(result, "app not found")
}

func AppsResultStatusError(result *AppsAPIResult) error {
	if cloudDeleteResourceNotFound(result, "app not found") {
		return nil
	}
	return CloudResultStatusError("app", result)
}

// ValidateAppIdempotencyKey mirrors the hosted Apps API contract so invalid
// requests fail locally instead of consuming a network attempt.
func ValidateAppIdempotencyKey(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if !appIdempotencyKeyPattern.MatchString(value) {
		return "", fmt.Errorf("idempotency key must be 8-128 characters using letters, numbers, dot, underscore, colon, or hyphen")
	}
	return value, nil
}

func requiredCloudID(kind string, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%s id is required", kind)
	}
	return url.PathEscape(trimmed), nil
}
