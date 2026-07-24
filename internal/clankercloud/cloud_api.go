package clankercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	DefaultCloudAPIBaseURL   = "https://clankercloud.ai/api"
	maxCloudAPIResponseBytes = 8 << 20
)

// APIResult is the safe, user-presentable envelope returned by hosted Clanker
// Cloud API clients. Credential-bearing fields and unsafe response headers are
// removed before this value leaves the client package.
type APIResult struct {
	BaseURL     string            `json:"baseUrl"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Status      int               `json:"status"`
	ContentType string            `json:"contentType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        any               `json:"body,omitempty"`
}

// NormalizeCloudAPIBaseURL accepts HTTPS endpoints and loopback HTTP endpoints
// used by local development. The API prefix remains explicit so credentials
// cannot accidentally be sent to a site root.
func NormalizeCloudAPIBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return "", fmt.Errorf("clanker cloud API base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse clanker cloud API base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("clanker cloud API base URL must use https or loopback http")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("clanker cloud API base URL must include a host")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("clanker cloud API base URL must use https unless the host is loopback")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("clanker cloud API base URL must not include user info")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("clanker cloud API base URL must not include query or fragment")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path == "" {
		parsed.Path = "/api"
		parsed.RawPath = ""
	} else if path != "/api" && !strings.HasSuffix(path, "/api") {
		return "", fmt.Errorf("clanker cloud API base URL must end with /api")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func hardenedCloudHTTPClient(source *http.Client) *http.Client {
	if source == nil {
		source = &http.Client{Timeout: defaultHTTPTimeout}
	}
	clone := *source
	originalCheckRedirect := source.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOriginURL(via[0].URL, req.URL) {
			return fmt.Errorf("refusing cross-origin Clanker Cloud API redirect")
		}
		if originalCheckRedirect != nil {
			return originalCheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

func sameOriginURL(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Host, right.Host)
}

// configuredCredentialAllowed prevents an untrusted per-call base URL from
// silently receiving credentials loaded from the process environment. A
// caller using a custom explicit URL must also pass its credential explicitly.
func configuredCredentialAllowed(explicitBaseURL string, configuredBaseURLs ...string) bool {
	if strings.TrimSpace(explicitBaseURL) == "" {
		return true
	}
	explicit, err := NormalizeCloudAPIBaseURL(explicitBaseURL)
	if err != nil {
		return false
	}
	candidates := append([]string{DefaultCloudAPIBaseURL}, configuredBaseURLs...)
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		normalized, err := NormalizeCloudAPIBaseURL(candidate)
		if err == nil && sameOriginAndPath(explicit, normalized) {
			return true
		}
	}
	return false
}

func sameOriginAndPath(left string, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil || !sameOriginURL(leftURL, rightURL) {
		return false
	}
	return strings.TrimRight(leftURL.EscapedPath(), "/") == strings.TrimRight(rightURL.EscapedPath(), "/")
}

func callCloudJSON(
	ctx context.Context,
	httpClient *http.Client,
	baseURL string,
	method string,
	path string,
	token string,
	body any,
	extraHeaders http.Header,
) (*APIResult, any, error) {
	normalizedBaseURL, err := NormalizeCloudAPIBaseURL(baseURL)
	if err != nil {
		return nil, nil, err
	}
	trimmedPath := strings.TrimSpace(path)
	if !strings.HasPrefix(trimmedPath, "/") {
		trimmedPath = "/" + trimmedPath
	}

	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode Clanker Cloud request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(normalizedBaseURL, "/")+trimmedPath, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("create Clanker Cloud request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range extraHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if trimmedToken := strings.TrimSpace(token); trimmedToken != "" {
		req.Header.Set("X-API-Key", trimmedToken)
	}

	resp, err := hardenedCloudHTTPClient(httpClient).Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("perform Clanker Cloud request: %w", err)
	}
	defer resp.Body.Close()

	result := &APIResult{
		BaseURL:     normalizedBaseURL,
		Method:      method,
		Path:        trimmedPath,
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     safeCloudResponseHeaders(resp.Header),
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudAPIResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read Clanker Cloud response: %w", err)
	}
	if len(rawBody) > maxCloudAPIResponseBytes {
		return nil, nil, fmt.Errorf("Clanker Cloud response exceeds %d bytes", maxCloudAPIResponseBytes)
	}
	decoded := decodeBody(rawBody)
	result.Body = RedactCloudSecrets(decoded)
	return result, decoded, nil
}

func safeCloudResponseHeaders(headers http.Header) map[string]string {
	allowed := []string{
		"Cache-Control",
		"CF-Ray",
		"ETag",
		"Retry-After",
		"Traceparent",
		"X-Request-ID",
	}
	safe := make(map[string]string, len(allowed))
	for _, key := range allowed {
		if values := headers.Values(key); len(values) > 0 {
			safe[http.CanonicalHeaderKey(key)] = strings.Join(values, ", ")
		}
	}
	return safe
}

// RedactCloudSecrets creates a redacted copy suitable for CLI and MCP output.
func RedactCloudSecrets(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveCloudField(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = RedactCloudSecrets(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = RedactCloudSecrets(item)
		}
		return redacted
	default:
		return value
	}
}

func sensitiveCloudField(key string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "apikey",
		"authorization",
		"bearer",
		"clientsecret",
		"cookie",
		"credential",
		"credentials",
		"deletetoken",
		"idtoken",
		"jwt",
		"password",
		"privatekey",
		"refreshtoken",
		"runtimetoken",
		"sandboxtoken",
		"secret",
		"secretkey",
		"setcookie",
		"signingkey",
		"token",
		"accesstoken":
		return true
	default:
		return strings.HasSuffix(normalized, "token") ||
			strings.HasSuffix(normalized, "secret") ||
			strings.HasSuffix(normalized, "password") ||
			strings.HasSuffix(normalized, "credential")
	}
}

func CloudResultOK(result *APIResult) bool {
	return result != nil && result.Status >= 200 && result.Status < 300
}

func CloudResultStatusError(resource string, result *APIResult) error {
	if CloudResultOK(result) {
		return nil
	}
	if result == nil {
		return fmt.Errorf("%s request failed", resource)
	}
	return fmt.Errorf("%s request returned status %d", resource, result.Status)
}

func cloudDeleteResourceNotFound(result *APIResult, expectedErrors ...string) bool {
	if result == nil || result.Method != http.MethodDelete || result.Status != http.StatusNotFound {
		return false
	}
	body, ok := result.Body.(map[string]any)
	if !ok {
		return false
	}
	if responseOK, present := body["ok"]; !present || responseOK != false {
		return false
	}
	message, ok := body["error"].(string)
	if !ok {
		return false
	}
	message = strings.TrimSpace(message)
	for _, expected := range expectedErrors {
		if message == expected {
			return true
		}
	}
	return false
}
