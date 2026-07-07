package clankercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const DefaultSandboxAPIBaseURL = "https://clankercloud.ai/api"

type SandboxClient struct {
	httpClient   *http.Client
	baseURL      string
	accountKey   string
	sandboxToken string
}

type SandboxClientOptions struct {
	BaseURL      string
	AccountKey   string
	SandboxToken string
	HTTPClient   *http.Client
}

type SandboxAPIResult struct {
	BaseURL     string            `json:"baseUrl"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Status      int               `json:"status"`
	ContentType string            `json:"contentType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        any               `json:"body,omitempty"`
}

type SandboxCreateRequest struct {
	Name     string         `json:"name,omitempty"`
	Agent    string         `json:"agent,omitempty"`
	Region   string         `json:"region,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type SandboxCommandRequest struct {
	Command        string            `json:"command"`
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
	return &SandboxClient{
		httpClient:   httpClient,
		baseURL:      firstNonEmpty(opts.BaseURL, os.Getenv("CLANKER_CLOUD_SANDBOX_API_BASE_URL"), os.Getenv("CLANKER_SANDBOX_API_BASE_URL"), DefaultSandboxAPIBaseURL),
		accountKey:   firstNonEmpty(opts.AccountKey, os.Getenv("CLANKER_CLOUD_API_KEY")),
		sandboxToken: firstNonEmpty(opts.SandboxToken, os.Getenv("CLANKER_SANDBOX_TOKEN")),
	}
}

func NormalizeSandboxAPIBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return "", fmt.Errorf("clanker cloud sandbox API base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse clanker cloud sandbox API base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("clanker cloud sandbox API base URL must use http or https")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("clanker cloud sandbox API base URL must not include user info")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("clanker cloud sandbox API base URL must not include query or fragment")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path == "" {
		parsed.Path = "/api"
	} else if path != "/api" && !strings.HasSuffix(path, "/api") {
		return "", fmt.Errorf("clanker cloud sandbox API base URL must end with /api")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (c *SandboxClient) BaseURL() (string, error) {
	return NormalizeSandboxAPIBaseURL(c.baseURL)
}

func (c *SandboxClient) AccountKey() string {
	return strings.TrimSpace(c.accountKey)
}

func (c *SandboxClient) SandboxToken() string {
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
	return c.callJSON(ctx, http.MethodPost, "/sandboxes", c.AccountKey(), payload)
}

func (c *SandboxClient) List(ctx context.Context) (*SandboxAPIResult, error) {
	return c.callJSON(ctx, http.MethodGet, "/sandboxes", c.AccountKey(), nil)
}

func (c *SandboxClient) Inspect(ctx context.Context, sandboxID string) (*SandboxAPIResult, error) {
	return c.callJSON(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(strings.TrimSpace(sandboxID)), c.preferredSandboxToken(), nil)
}

func (c *SandboxClient) Delete(ctx context.Context, sandboxID string) (*SandboxAPIResult, error) {
	return c.callJSON(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(strings.TrimSpace(sandboxID)), c.preferredSandboxToken(), nil)
}

func (c *SandboxClient) Command(ctx context.Context, sandboxID string, payload SandboxCommandRequest) (*SandboxAPIResult, error) {
	return c.callJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(strings.TrimSpace(sandboxID))+"/commands", c.preferredSandboxToken(), payload)
}

func (c *SandboxClient) Message(ctx context.Context, sandboxID string, payload SandboxMessageRequest) (*SandboxAPIResult, error) {
	if strings.TrimSpace(payload.Role) == "" {
		payload.Role = "user"
	}
	return c.callJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(strings.TrimSpace(sandboxID))+"/messages", c.preferredSandboxToken(), payload)
}

func (c *SandboxClient) preferredSandboxToken() string {
	return firstNonEmpty(c.sandboxToken, c.accountKey)
}

func (c *SandboxClient) callJSON(ctx context.Context, method string, path string, token string, body any) (*SandboxAPIResult, error) {
	baseURL, err := c.BaseURL()
	if err != nil {
		return nil, err
	}
	trimmedPath := strings.TrimSpace(path)
	if !strings.HasPrefix(trimmedPath, "/") {
		trimmedPath = "/" + trimmedPath
	}

	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode sandbox request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+trimmedPath, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create sandbox request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if trimmedToken := strings.TrimSpace(token); trimmedToken != "" {
		req.Header.Set("X-API-Key", trimmedToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform sandbox request: %w", err)
	}
	defer resp.Body.Close()

	result := &SandboxAPIResult{
		BaseURL:     baseURL,
		Method:      method,
		Path:        trimmedPath,
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     map[string]string{},
	}
	for key, values := range resp.Header {
		if len(values) > 0 {
			result.Headers[key] = strings.Join(values, ", ")
		}
	}
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read sandbox response: %w", err)
	}
	result.Body = decodeBody(rawBody)
	return result, nil
}

func SandboxResultOK(result *SandboxAPIResult) bool {
	return result != nil && result.Status >= 200 && result.Status < 300
}

func SandboxResultStatusError(result *SandboxAPIResult) error {
	if SandboxResultOK(result) {
		return nil
	}
	if result == nil {
		return fmt.Errorf("sandbox request failed")
	}
	return fmt.Errorf("sandbox request returned status %d", result.Status)
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
		return token
	}
	return extractStringAt(body, "token")
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
