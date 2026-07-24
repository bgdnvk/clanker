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

type AppFile struct {
	Path        string  `json:"path"`
	Content     *string `json:"content,omitempty"`
	Base64      *string `json:"base64,omitempty"`
	ContentType string  `json:"contentType,omitempty"`
}

type AppCreateRequest struct {
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	ProjectID      string         `json:"projectId,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	IdempotencyKey string         `json:"-"`
}

// AppDeploymentInput creates an immutable private deployment. It does not
// activate or publish that deployment.
type AppDeploymentInput struct {
	HTML          string         `json:"html,omitempty"`
	Files         []AppFile      `json:"files,omitempty"`
	Entrypoint    string         `json:"entrypoint,omitempty"`
	SPA           bool           `json:"spa,omitempty"`
	DataSummary   map[string]any `json:"dataSummary,omitempty"`
	NetworkPolicy string         `json:"networkPolicy"`
	Exposure      map[string]any `json:"exposure,omitempty"`
}

type AppDeploymentCreateRequest struct {
	AppDeploymentInput
	IdempotencyKey string `json:"-"`
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
	hasHTML := payload.HTML != ""
	hasFiles := len(payload.Files) > 0
	if hasHTML == hasFiles {
		return nil, fmt.Errorf("provide exactly one of deployment html or files")
	}
	for _, file := range payload.Files {
		if strings.TrimSpace(file.Path) == "" {
			return nil, fmt.Errorf("deployment file path is required")
		}
		if (file.Content == nil) == (file.Base64 == nil) {
			return nil, fmt.Errorf("%s must provide exactly one of content or base64", strings.TrimSpace(file.Path))
		}
	}
	if strings.TrimSpace(payload.Entrypoint) == "" {
		payload.Entrypoint = "index.html"
	}
	if strings.TrimSpace(payload.NetworkPolicy) == "" {
		payload.NetworkPolicy = "none"
	}
	if !strings.EqualFold(strings.TrimSpace(payload.NetworkPolicy), "none") {
		return nil, fmt.Errorf("network policy must be none for static app deployments")
	}
	idempotencyKey, err := ValidateAppIdempotencyKey(payload.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	payload.NetworkPolicy = "none"
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
