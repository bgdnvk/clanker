package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Client wraps Cloudflare API and CLI tools
type Client struct {
	accountID string
	apiToken  string
	debug     bool
}

// ResolveAccountID returns the Cloudflare account ID from config or environment
func ResolveAccountID() string {
	if accountID := strings.TrimSpace(viper.GetString("cloudflare.account_id")); accountID != "" {
		return accountID
	}
	if env := strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("CF_ACCOUNT_ID")); env != "" {
		return env
	}
	return ""
}

// ResolveAPIToken returns the Cloudflare API token from config or environment
func ResolveAPIToken() string {
	if apiToken := strings.TrimSpace(viper.GetString("cloudflare.api_token")); apiToken != "" {
		return apiToken
	}
	if env := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("CF_API_TOKEN")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Cloudflare client
func NewClient(accountID, apiToken string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("cloudflare api_token is required")
	}

	return &Client{
		accountID: accountID,
		apiToken:  apiToken,
		debug:     debug,
	}, nil
}

// BackendCloudflareCredentials represents Cloudflare credentials from the backend
type BackendCloudflareCredentials struct {
	APIToken  string
	AccountID string
	ZoneID    string
}

// NewClientWithCredentials creates a new Cloudflare client using credentials from the backend
func NewClientWithCredentials(creds *BackendCloudflareCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}

	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("cloudflare api_token is required")
	}

	return &Client{
		accountID: creds.AccountID,
		apiToken:  creds.APIToken,
		debug:     debug,
	}, nil
}

// GetAccountID returns the account ID
func (c *Client) GetAccountID() string {
	return c.accountID
}

// GetAPIToken returns the API token
func (c *Client) GetAPIToken() string {
	return c.apiToken
}

// RunAPI executes a Cloudflare API call via curl with exponential backoff
func (c *Client) RunAPI(method, endpoint, body string) (string, error) {
	return c.RunAPIWithContext(context.Background(), method, endpoint, body)
}

// RunAPIWithContext executes a Cloudflare API call with context
func (c *Client) RunAPIWithContext(ctx context.Context, method, endpoint, body string) (string, error) {
	if _, err := exec.LookPath("curl"); err != nil {
		return "", fmt.Errorf("curl not found in PATH")
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		args := []string{
			"-s",
			"-X", method,
			fmt.Sprintf("https://api.cloudflare.com/client/v4%s", endpoint),
			"-H", fmt.Sprintf("Authorization: Bearer %s", c.apiToken),
			"-H", "Content-Type: application/json",
		}

		if body != "" {
			args = append(args, "-d", body)
		}

		if c.debug {
			fmt.Printf("[cloudflare] curl -X %s https://api.cloudflare.com/client/v4%s\n", method, endpoint)
		}

		cmd := exec.CommandContext(ctx, "curl", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			result := stdout.String()
			if apiErr := checkAPIError(result); apiErr != nil {
				return result, apiErr
			}
			return result, nil
		}

		lastErr = err
		lastStderr = strings.TrimSpace(stderr.String())

		if ctx.Err() != nil {
			break
		}

		if !isRetryableError(lastStderr) {
			break
		}

		time.Sleep(backoffs[attempt])
	}

	if lastErr == nil {
		return "", fmt.Errorf("cloudflare API call failed")
	}

	return "", fmt.Errorf("cloudflare API call failed: %w, stderr: %s%s", lastErr, lastStderr, errorHint(lastStderr))
}

// RunWrangler executes a wrangler CLI command
func (c *Client) RunWrangler(args ...string) (string, error) {
	return c.RunWranglerWithContext(context.Background(), args...)
}

// RunWranglerWithContext executes a wrangler CLI command with context
func (c *Client) RunWranglerWithContext(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("wrangler"); err != nil {
		return "", fmt.Errorf("wrangler not found in PATH (required for Workers operations, install with: npm install -g wrangler)")
	}

	cmd := exec.CommandContext(ctx, "wrangler", args...)

	// Set environment variables for authentication
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CLOUDFLARE_API_TOKEN=%s", c.apiToken),
	)
	if c.accountID != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("CLOUDFLARE_ACCOUNT_ID=%s", c.accountID))
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		fmt.Printf("[cloudflare] wrangler %s\n", strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("wrangler command failed: %w, stderr: %s%s", err, stderrStr, errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// RunCloudflared executes a cloudflared CLI command
func (c *Client) RunCloudflared(args ...string) (string, error) {
	return c.RunCloudflaredWithContext(context.Background(), args...)
}

// RunCloudflaredWithContext executes a cloudflared CLI command with context
func (c *Client) RunCloudflaredWithContext(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("cloudflared"); err != nil {
		return "", fmt.Errorf("cloudflared not found in PATH (required for Tunnel operations, install from: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)")
	}

	cmd := exec.CommandContext(ctx, "cloudflared", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		fmt.Printf("[cloudflare] cloudflared %s\n", strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("cloudflared command failed: %w, stderr: %s%s", err, stderrStr, errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// GetRelevantContext gathers Cloudflare context for LLM queries
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name           string
		endpoint       string
		keys           []string
		requiresAcctID bool
	}

	sections := []section{
		{name: "Zones", endpoint: "/zones", keys: []string{"zone", "domain", "dns", "site"}},
		{name: "Account Details", endpoint: fmt.Sprintf("/accounts/%s", c.accountID), keys: []string{"account", "plan", "billing"}, requiresAcctID: true},
		{name: "Workers Scripts", endpoint: fmt.Sprintf("/accounts/%s/workers/scripts", c.accountID), keys: []string{"worker", "workers", "script", "serverless", "edge compute"}, requiresAcctID: true},
		{name: "Pages Projects", endpoint: fmt.Sprintf("/accounts/%s/pages/projects", c.accountID), keys: []string{"pages", "page", "static site", "frontend"}, requiresAcctID: true},
		{name: "Workers KV Namespaces", endpoint: fmt.Sprintf("/accounts/%s/storage/kv/namespaces", c.accountID), keys: []string{"kv", "key value", "key-value", "namespace", "namespaces"}, requiresAcctID: true},
		{name: "D1 Databases", endpoint: fmt.Sprintf("/accounts/%s/d1/database", c.accountID), keys: []string{"d1", "sqlite", "database", "databases"}, requiresAcctID: true},
		{name: "R2 Buckets", endpoint: fmt.Sprintf("/accounts/%s/r2/buckets", c.accountID), keys: []string{"r2", "bucket", "buckets", "object storage", "storage"}, requiresAcctID: true},
		{name: "Queues", endpoint: fmt.Sprintf("/accounts/%s/queues", c.accountID), keys: []string{"queue", "queues", "message", "event driven", "event-driven"}, requiresAcctID: true},
		{name: "Vectorize Indexes", endpoint: fmt.Sprintf("/accounts/%s/vectorize/v2/indexes", c.accountID), keys: []string{"vectorize", "vector", "vectors", "embedding", "embeddings", "semantic search"}, requiresAcctID: true},
		{name: "Hyperdrive Configs", endpoint: fmt.Sprintf("/accounts/%s/hyperdrive/configs", c.accountID), keys: []string{"hyperdrive", "postgres", "mysql", "database acceleration", "connection pooling"}, requiresAcctID: true},
		{name: "AI Gateway", endpoint: fmt.Sprintf("/accounts/%s/ai-gateway/gateways", c.accountID), keys: []string{"ai gateway", "gateway", "llm gateway", "model gateway", "ai"}, requiresAcctID: true},
		{name: "AI Search Namespaces", endpoint: fmt.Sprintf("/accounts/%s/ai-search/namespaces", c.accountID), keys: []string{"ai search", "search", "rag", "retrieval", "autorag"}, requiresAcctID: true},
		{name: "Durable Object Namespaces", endpoint: fmt.Sprintf("/accounts/%s/workers/durable_objects/namespaces", c.accountID), keys: []string{"durable object", "durable objects", "stateful", "actor"}, requiresAcctID: true},
		{name: "Browser Rendering Sessions", endpoint: fmt.Sprintf("/accounts/%s/browser-rendering/devtools/session", c.accountID), keys: []string{"browser rendering", "browser run", "browser session", "headless browser"}, requiresAcctID: true},
		{name: "Cloudflare Images", endpoint: fmt.Sprintf("/accounts/%s/images/v2", c.accountID), keys: []string{"cloudflare images", "image", "images", "media"}, requiresAcctID: true},
		{name: "Cloudflare Stream", endpoint: fmt.Sprintf("/accounts/%s/stream", c.accountID), keys: []string{"stream", "video", "videos", "media"}, requiresAcctID: true},
		{name: "Secrets Store", endpoint: fmt.Sprintf("/accounts/%s/secrets_store/stores", c.accountID), keys: []string{"secrets store", "secret store", "account secrets"}, requiresAcctID: true},
		{name: "Pipelines", endpoint: fmt.Sprintf("/accounts/%s/pipelines/v1/pipelines", c.accountID), keys: []string{"pipeline", "pipelines", "etl", "data pipeline"}, requiresAcctID: true},
		{name: "Pipeline Sinks", endpoint: fmt.Sprintf("/accounts/%s/pipelines/v1/sinks", c.accountID), keys: []string{"pipeline sink", "pipeline sinks", "data sink"}, requiresAcctID: true},
		{name: "Pipeline Streams", endpoint: fmt.Sprintf("/accounts/%s/pipelines/v1/streams", c.accountID), keys: []string{"pipeline stream", "pipeline streams", "data stream"}, requiresAcctID: true},
		{name: "Turnstile Widgets", endpoint: fmt.Sprintf("/accounts/%s/challenges/widgets", c.accountID), keys: []string{"turnstile", "captcha", "challenge"}, requiresAcctID: true},
		{name: "Zero Trust Tunnels", endpoint: fmt.Sprintf("/accounts/%s/cfd_tunnel", c.accountID), keys: []string{"tunnel", "tunnels", "zero trust", "cloudflared"}, requiresAcctID: true},
		{name: "Logpush Jobs", endpoint: fmt.Sprintf("/accounts/%s/logpush/jobs", c.accountID), keys: []string{"logpush", "logs", "observability"}, requiresAcctID: true},
		{name: "Rules Lists", endpoint: fmt.Sprintf("/accounts/%s/rules/lists", c.accountID), keys: []string{"rules list", "rules lists", "ip list"}, requiresAcctID: true},
		{name: "Account Roles", endpoint: fmt.Sprintf("/accounts/%s/roles", c.accountID), keys: []string{"account role", "roles", "iam"}, requiresAcctID: true},
		{name: "Account Members", endpoint: fmt.Sprintf("/accounts/%s/members", c.accountID), keys: []string{"member", "members", "users"}, requiresAcctID: true},
	}

	// Default sections to include
	defaultSections := map[string]bool{
		"Zones":           true,
		"Workers Scripts": c.accountID != "",
		"D1 Databases":    c.accountID != "",
		"R2 Buckets":      c.accountID != "",
	}

	var out strings.Builder
	var warnings []string

	for _, s := range sections {
		if s.requiresAcctID && strings.TrimSpace(c.accountID) == "" {
			if questionLower != "" {
				for _, key := range s.keys {
					if strings.Contains(questionLower, key) {
						warnings = append(warnings, fmt.Sprintf("%s: Cloudflare account ID is required (set CLOUDFLARE_ACCOUNT_ID or cloudflare.account_id)", s.name))
						break
					}
				}
			}
			continue
		}

		// Check if this section is relevant to the question
		if questionLower != "" && len(s.keys) > 0 {
			matched := false
			for _, key := range s.keys {
				if strings.Contains(questionLower, key) {
					matched = true
					break
				}
			}
			if !matched && !defaultSections[s.name] {
				continue
			}
		}

		result, err := c.RunAPIWithContext(ctx, "GET", s.endpoint, "")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}

		formatted := formatAPIResponse(s.name, result)
		if formatted != "" {
			out.WriteString(formatted)
			out.WriteString("\n")
		}
	}

	if len(warnings) > 0 {
		out.WriteString("Cloudflare Warnings:\n")
		for i, warn := range warnings {
			if i >= 8 {
				out.WriteString("- (additional warnings omitted)\n")
				break
			}
			out.WriteString("- ")
			out.WriteString(warn)
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	if strings.TrimSpace(out.String()) == "" {
		return "No Cloudflare data available (missing permissions or no resources).", nil
	}

	return out.String(), nil
}

// checkAPIError checks if the API response contains an error
func checkAPIError(response string) error {
	var apiResp struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal([]byte(response), &apiResp); err != nil {
		return nil // Not JSON or unexpected format, let caller handle
	}

	if !apiResp.Success && len(apiResp.Errors) > 0 {
		var errMsgs []string
		for _, e := range apiResp.Errors {
			errMsgs = append(errMsgs, fmt.Sprintf("[%d] %s", e.Code, e.Message))
		}
		return fmt.Errorf("API error: %s", strings.Join(errMsgs, "; "))
	}

	return nil
}

// isRetryableError checks if an error is retryable
func isRetryableError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") {
		return true
	}
	if strings.Contains(lower, "temporarily unavailable") || strings.Contains(lower, "internal error") {
		return true
	}
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset") {
		return true
	}
	return false
}

// errorHint returns helpful hints based on error messages
func errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid token"):
		return " (hint: check your CLOUDFLARE_API_TOKEN is valid)"
	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission"):
		return " (hint: your API token may be missing required permissions)"
	case strings.Contains(lower, "not found") || strings.Contains(lower, "404"):
		return " (hint: resource not found or incorrect zone/account ID)"
	case strings.Contains(lower, "rate limit"):
		return " (hint: rate limited, try again in a few seconds)"
	case strings.Contains(lower, "invalid") && strings.Contains(lower, "account"):
		return " (hint: check your CLOUDFLARE_ACCOUNT_ID is correct)"
	default:
		return ""
	}
}

// formatAPIResponse formats a Cloudflare API response for display
func formatAPIResponse(name, response string) string {
	var apiResp struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}

	if err := json.Unmarshal([]byte(response), &apiResp); err != nil {
		return ""
	}

	if !apiResp.Success {
		return ""
	}

	// Try to pretty-print the result
	var result interface{}
	if err := json.Unmarshal(apiResp.Result, &result); err != nil {
		return ""
	}

	formatted, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%s:\n%s", name, string(formatted))
}
