package clankercloud

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const DefaultSandboxAPIBaseURL = DefaultCloudAPIBaseURL

type SandboxClient struct {
	httpClient   *http.Client
	baseURL      string
	accountKey   string
	tokenMu      sync.RWMutex
	sandboxToken string
}

type SandboxClientOptions struct {
	BaseURL      string
	AccountKey   string
	SandboxToken string
	HTTPClient   *http.Client
}

type SandboxAPIResult = APIResult

type SandboxCreateRequest struct {
	Name     string         `json:"name,omitempty"`
	Agent    string         `json:"agent,omitempty"`
	Region   string         `json:"region,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type SandboxCommandRequest struct {
	Command        string            `json:"command"`
	WorkingDir     string            `json:"workingDir,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

type SandboxMessageRequest struct {
	Role     string         `json:"role,omitempty"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func NewSandboxClient(opts SandboxClientOptions) *SandboxClient {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	configuredSandboxBaseURL := os.Getenv("CLANKER_CLOUD_SANDBOX_API_BASE_URL")
	configuredLegacyBaseURL := os.Getenv("CLANKER_SANDBOX_API_BASE_URL")
	accountKey := strings.TrimSpace(opts.AccountKey)
	sandboxToken := strings.TrimSpace(opts.SandboxToken)
	if configuredCredentialAllowed(opts.BaseURL, configuredSandboxBaseURL, configuredLegacyBaseURL) {
		accountKey = firstNonEmpty(accountKey, os.Getenv("CLANKER_CLOUD_API_KEY"))
		sandboxToken = firstNonEmpty(sandboxToken, os.Getenv("CLANKER_SANDBOX_TOKEN"))
	}
	return &SandboxClient{
		httpClient:   httpClient,
		baseURL:      firstNonEmpty(opts.BaseURL, configuredSandboxBaseURL, configuredLegacyBaseURL, DefaultSandboxAPIBaseURL),
		accountKey:   accountKey,
		sandboxToken: sandboxToken,
	}
}

func NormalizeSandboxAPIBaseURL(raw string) (string, error) {
	return NormalizeCloudAPIBaseURL(raw)
}

func (c *SandboxClient) BaseURL() (string, error) {
	return NormalizeSandboxAPIBaseURL(c.baseURL)
}

func (c *SandboxClient) AccountKey() string {
	return strings.TrimSpace(c.accountKey)
}

func (c *SandboxClient) SandboxToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return strings.TrimSpace(c.sandboxToken)
}

func (c *SandboxClient) Create(ctx context.Context, payload SandboxCreateRequest) (*SandboxAPIResult, error) {
	if strings.TrimSpace(payload.Name) == "" {
		payload.Name = "agent-sandbox"
	}
	if strings.TrimSpace(payload.Agent) == "" {
		payload.Agent = "clanker-cli"
	}
	if strings.TrimSpace(payload.Region) == "" {
		payload.Region = "earth"
	}
	result, rawBody, err := c.callJSONWithRawBody(ctx, http.MethodPost, "/sandboxes", c.AccountKey(), payload)
	if err == nil && SandboxResultOK(result) {
		if token := ExtractSandboxToken(rawBody); token != "" {
			c.setSandboxToken(token)
		}
	}
	return result, err
}

func (c *SandboxClient) List(ctx context.Context) (*SandboxAPIResult, error) {
	return c.callJSON(ctx, http.MethodGet, "/sandboxes", c.AccountKey(), nil)
}

func (c *SandboxClient) Inspect(ctx context.Context, sandboxID string) (*SandboxAPIResult, error) {
	escaped, err := requiredCloudID("sandbox", sandboxID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodGet, "/sandboxes/"+escaped, c.preferredSandboxToken(), nil)
}

func (c *SandboxClient) Delete(ctx context.Context, sandboxID string) (*SandboxAPIResult, error) {
	escaped, err := requiredCloudID("sandbox", sandboxID)
	if err != nil {
		return nil, err
	}
	path := "/sandboxes/" + escaped
	token := c.preferredSandboxToken()
	result, err := c.callJSON(ctx, http.MethodDelete, path, token, nil)
	if err == nil &&
		result != nil &&
		(result.Status == http.StatusUnauthorized || result.Status == http.StatusForbidden) &&
		c.AccountKey() != "" &&
		token != c.AccountKey() {
		return c.callJSON(ctx, http.MethodDelete, path, c.AccountKey(), nil)
	}
	return result, err
}

// Dispose deletes a sandbox with a fresh bounded context so cleanup still runs
// after a command or parent request is cancelled. DELETE 404 is treated as an
// idempotent already-disposed result.
func (c *SandboxClient) Dispose(ctx context.Context, sandboxID string) (*SandboxAPIResult, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	result, err := c.Delete(cleanupCtx, sandboxID)
	if err != nil {
		return result, err
	}
	return result, SandboxResultStatusError(result)
}

func (c *SandboxClient) Command(ctx context.Context, sandboxID string, payload SandboxCommandRequest) (*SandboxAPIResult, error) {
	escaped, err := requiredCloudID("sandbox", sandboxID)
	if err != nil {
		return nil, err
	}
	return c.callJSON(ctx, http.MethodPost, "/sandboxes/"+escaped+"/commands", c.preferredSandboxToken(), payload)
}

func (c *SandboxClient) Message(ctx context.Context, sandboxID string, payload SandboxMessageRequest) (*SandboxAPIResult, error) {
	escaped, err := requiredCloudID("sandbox", sandboxID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Role) == "" {
		payload.Role = "user"
	}
	return c.callJSON(ctx, http.MethodPost, "/sandboxes/"+escaped+"/messages", c.preferredSandboxToken(), payload)
}

func (c *SandboxClient) preferredSandboxToken() string {
	return firstNonEmpty(c.SandboxToken(), c.accountKey)
}

func (c *SandboxClient) callJSON(ctx context.Context, method string, path string, token string, body any) (*SandboxAPIResult, error) {
	result, _, err := c.callJSONWithRawBody(ctx, method, path, token, body)
	return result, err
}

func (c *SandboxClient) callJSONWithRawBody(ctx context.Context, method string, path string, token string, body any) (*SandboxAPIResult, any, error) {
	baseURL, err := c.BaseURL()
	if err != nil {
		return nil, nil, err
	}
	return callCloudJSON(ctx, c.httpClient, baseURL, method, path, token, body, nil)
}

func (c *SandboxClient) setSandboxToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.sandboxToken = strings.TrimSpace(token)
}

func SandboxResultOK(result *SandboxAPIResult) bool {
	return CloudResultOK(result) ||
		cloudDeleteResourceNotFound(result, "sandbox not found", "box not found")
}

func SandboxResultStatusError(result *SandboxAPIResult) error {
	if cloudDeleteResourceNotFound(result, "sandbox not found", "box not found") {
		return nil
	}
	return CloudResultStatusError("sandbox", result)
}

func ExtractSandboxID(body any) string {
	if id := extractStringAt(body, "box", "id"); id != "" {
		return id
	}
	if id := extractStringAt(body, "sandbox", "id"); id != "" {
		return id
	}
	return extractStringAt(body, "id")
}

func ExtractSandboxToken(body any) string {
	if token := extractStringAt(body, "sandboxToken"); token != "" {
		if token != "[REDACTED]" {
			return token
		}
		return ""
	}
	token := extractStringAt(body, "token")
	if token == "[REDACTED]" {
		return ""
	}
	return token
}

func extractStringAt(value any, path ...string) string {
	current := value
	for _, part := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[part]
	}
	if text, ok := current.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
