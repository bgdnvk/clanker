package clankercloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var appIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

const (
	appsAPIPath                      = "/v1/apps"
	MaxAppHTMLBytes                  = 2 << 20
	MaxAppAgenticDailyRequests       = 25
	MaxAppAgenticInputCharacters     = 16000
	MaxAppAgenticOutputTokens        = 1024
	DefaultAppAgenticDailyRequests   = MaxAppAgenticDailyRequests
	DefaultAppAgenticInputCharacters = MaxAppAgenticInputCharacters
	DefaultAppAgenticOutputTokens    = MaxAppAgenticOutputTokens
)

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
	AppSpec        *AppSpec          `json:"appSpec,omitempty"`
	HTML           *string           `json:"html,omitempty"`
	Agentic        *AppAgenticConfig `json:"agentic,omitempty"`
	IdempotencyKey string            `json:"-"`
}

type AppAgenticConfig struct {
	Providers          []string `json:"providers"`
	DailyRequestLimit  int      `json:"dailyRequestLimit"`
	MaxInputCharacters int      `json:"maxInputCharacters"`
	MaxOutputTokens    int      `json:"maxOutputTokens"`
}

// Avoid changing supplied HTML and canonical appSpec display text into JSON
// escape sequences before the service applies its byte limits.
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
	return c.callJSON(ctx, http.MethodGet, appsAPIPath, nil, "")
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
	return c.callJSON(ctx, http.MethodPost, appsAPIPath, payload, idempotencyKey)
}

func (c *AppsClient) GetApp(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodGet, appsAPIPath+"/"+escaped, nil, "")
}

func (c *AppsClient) DeleteApp(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodDelete, appsAPIPath+"/"+escaped, nil, "")
}

func (c *AppsClient) ListDeployments(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodGet, appsAPIPath+"/"+escaped+"/deployments", nil, "")
}

func (c *AppsClient) CreateDeployment(ctx context.Context, appID string, payload AppDeploymentCreateRequest) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	hasAppSpec := payload.AppSpec != nil
	hasHTML := payload.HTML != nil
	if hasAppSpec == hasHTML {
		return nil, fmt.Errorf("provide exactly one of deployment appSpec or html")
	}
	if payload.Agentic != nil {
		if !hasHTML {
			return nil, fmt.Errorf("agentic is only supported for html deployments")
		}
		normalized, err := ValidateAppAgenticConfig(*payload.Agentic)
		if err != nil {
			return nil, err
		}
		payload.Agentic = &normalized
	}
	if hasAppSpec {
		normalized := normalizeAppSpec(*payload.AppSpec)
		if err := ValidateAppSpec(normalized); err != nil {
			return nil, err
		}
		payload.AppSpec = &normalized
	} else {
		if !utf8.ValidString(*payload.HTML) {
			return nil, fmt.Errorf("deployment html must be valid UTF-8")
		}
		if len(*payload.HTML) == 0 {
			return nil, fmt.Errorf("deployment html must not be empty")
		}
		if len(*payload.HTML) > MaxAppHTMLBytes {
			return nil, fmt.Errorf("deployment html exceeds %d bytes", MaxAppHTMLBytes)
		}
	}
	idempotencyKey, err := ValidateAppIdempotencyKey(payload.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodPost, appsAPIPath+"/"+escaped+"/deployments", payload, idempotencyKey)
}

// ValidateAppAgenticConfig mirrors the hosted Apps launch contract and returns
// its canonical provider ordering for stable deployment request identity.
func ValidateAppAgenticConfig(input AppAgenticConfig) (AppAgenticConfig, error) {
	if len(input.Providers) == 0 || len(input.Providers) > 2 {
		return AppAgenticConfig{}, fmt.Errorf("agentic.providers must contain one or two providers")
	}
	providers := make([]string, 0, len(input.Providers))
	seen := make(map[string]struct{}, len(input.Providers))
	for _, raw := range input.Providers {
		provider := strings.ToLower(strings.TrimSpace(raw))
		if provider != "gemini" && provider != "kimi" {
			return AppAgenticConfig{}, fmt.Errorf("agentic.providers may contain only gemini or kimi")
		}
		if _, ok := seen[provider]; ok {
			return AppAgenticConfig{}, fmt.Errorf("agentic.providers must not contain duplicates")
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	if input.DailyRequestLimit < 1 || input.DailyRequestLimit > MaxAppAgenticDailyRequests {
		return AppAgenticConfig{}, fmt.Errorf(
			"agentic.dailyRequestLimit must be between 1 and %d",
			MaxAppAgenticDailyRequests,
		)
	}
	if input.MaxInputCharacters < 1 || input.MaxInputCharacters > MaxAppAgenticInputCharacters {
		return AppAgenticConfig{}, fmt.Errorf(
			"agentic.maxInputCharacters must be between 1 and %d",
			MaxAppAgenticInputCharacters,
		)
	}
	if input.MaxOutputTokens < 1 || input.MaxOutputTokens > MaxAppAgenticOutputTokens {
		return AppAgenticConfig{}, fmt.Errorf(
			"agentic.maxOutputTokens must be between 1 and %d",
			MaxAppAgenticOutputTokens,
		)
	}
	return AppAgenticConfig{
		Providers:          providers,
		DailyRequestLimit:  input.DailyRequestLimit,
		MaxInputCharacters: input.MaxInputCharacters,
		MaxOutputTokens:    input.MaxOutputTokens,
	}, nil
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
	path := appsAPIPath + "/" + escapedAppID + "/deployments/" + escapedDeploymentID + "/activate"
	return c.callJSON(ctx, http.MethodPost, path, map[string]any{}, "")
}

func (c *AppsClient) UnpublishApp(ctx context.Context, appID string) (*AppsAPIResult, error) {
	escaped, err := requiredCloudID("app", appID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodPost, appsAPIPath+"/"+escaped+"/unpublish", map[string]any{}, "")
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
