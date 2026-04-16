// Package vercel provides a client for the Vercel REST API and related CLI
// tooling (the `vercel` CLI for deploys). Mirrors the shape of the Cloudflare
// package so wiring into cmd/, routing, ask-mode and the desktop backend
// stays uniform.
package vercel

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

const baseURL = "https://api.vercel.com"

// Client wraps the Vercel REST API and the official `vercel` CLI.
type Client struct {
	apiToken string
	teamID   string
	debug    bool
	// raw, when set, causes the static CLI commands to print unformatted JSON
	// responses instead of pretty-printed summaries.
	raw bool
}

// ResolveAPIToken returns the Vercel API token from config or environment.
// Resolution order: `vercel.api_token` → VERCEL_TOKEN → VERCEL_API_TOKEN.
func ResolveAPIToken() string {
	if t := strings.TrimSpace(viper.GetString("vercel.api_token")); t != "" {
		return t
	}
	if env := strings.TrimSpace(os.Getenv("VERCEL_TOKEN")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("VERCEL_API_TOKEN")); env != "" {
		return env
	}
	return ""
}

// ResolveTeamID returns the Vercel team ID from config or environment.
// Resolution order: `vercel.team_id` → VERCEL_TEAM_ID → VERCEL_ORG_ID.
// Team scoping is optional — personal accounts have no team ID.
func ResolveTeamID() string {
	if t := strings.TrimSpace(viper.GetString("vercel.team_id")); t != "" {
		return t
	}
	if env := strings.TrimSpace(os.Getenv("VERCEL_TEAM_ID")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("VERCEL_ORG_ID")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Vercel client.
func NewClient(apiToken, teamID string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("vercel api_token is required")
	}
	return &Client{
		apiToken: apiToken,
		teamID:   teamID,
		debug:    debug,
	}, nil
}

// BackendVercelCredentials represents Vercel credentials retrieved from the
// backend credential store (clanker-backend).
type BackendVercelCredentials struct {
	APIToken string
	TeamID   string
}

// NewClientWithCredentials creates a new Vercel client using backend credentials.
func NewClientWithCredentials(creds *BackendVercelCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}
	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("vercel api_token is required")
	}
	return &Client{
		apiToken: creds.APIToken,
		teamID:   creds.TeamID,
		debug:    debug,
	}, nil
}

// GetAPIToken returns the API token.
func (c *Client) GetAPIToken() string { return c.apiToken }

// GetTeamID returns the team ID (may be empty for personal accounts).
func (c *Client) GetTeamID() string { return c.teamID }

// withTeam appends `teamId=<id>` to the endpoint when the client is team-scoped
// and the endpoint does not already carry a teamId parameter. Endpoints that
// already encode the team in the path (e.g. `/v1/teams/{teamID}/...`) are
// returned unchanged so we don't double-scope the request.
func (c *Client) withTeam(endpoint string) string {
	if c.teamID == "" {
		return endpoint
	}
	if strings.Contains(endpoint, "teamId=") {
		return endpoint
	}
	if strings.Contains(endpoint, "/teams/"+c.teamID) {
		return endpoint
	}
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + "teamId=" + c.teamID
}

// RunAPI executes a Vercel REST call with exponential backoff.
func (c *Client) RunAPI(method, endpoint, body string) (string, error) {
	return c.RunAPIWithContext(context.Background(), method, endpoint, body)
}

// RunAPIWithContext executes a Vercel REST call with a caller-controlled context.
func (c *Client) RunAPIWithContext(ctx context.Context, method, endpoint, body string) (string, error) {
	if _, err := exec.LookPath("curl"); err != nil {
		return "", fmt.Errorf("curl not found in PATH")
	}

	endpoint = c.withTeam(endpoint)

	// backoffs has three entries, meaning we make up to 3 total attempts
	// (the initial try + 2 retries) before giving up.
	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string
	var lastBody string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		args := []string{
			"-s",
			"-X", method,
			baseURL + endpoint,
			"-H", fmt.Sprintf("Authorization: Bearer %s", c.apiToken),
			"-H", "Content-Type: application/json",
		}

		if body != "" {
			args = append(args, "-d", body)
		}

		if c.debug {
			fmt.Printf("[vercel] curl -X %s %s%s\n", method, baseURL, endpoint)
		}

		cmd := exec.CommandContext(ctx, "curl", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			result := stdout.String()
			if apiErr := checkAPIError(result); apiErr != nil {
				// Retry on 429 / 5xx style responses (detected via the error body).
				if isRetryableError(apiErr.Error()) && attempt < len(backoffs)-1 {
					lastBody = result
					time.Sleep(backoffs[attempt])
					continue
				}
				return result, fmt.Errorf("%w%s", apiErr, errorHint(apiErr.Error()))
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

	if lastErr == nil && lastBody != "" {
		return "", fmt.Errorf("vercel API call failed after retries: %s", lastBody)
	}
	if lastErr == nil {
		return "", fmt.Errorf("vercel API call failed")
	}
	return "", fmt.Errorf("vercel API call failed: %w, stderr: %s%s", lastErr, lastStderr, errorHint(lastStderr))
}

// RunVercelCLI executes the official `vercel` CLI tool for operations the REST
// API does not model well (source upload, prebuilt output, etc.). Phase 2+
// callers shell out here; phase 1 clients just rely on REST.
func (c *Client) RunVercelCLI(args ...string) (string, error) {
	return c.RunVercelCLIWithContext(context.Background(), args...)
}

// RunVercelCLIWithContext executes the Vercel CLI with a caller-controlled context.
func (c *Client) RunVercelCLIWithContext(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("vercel"); err != nil {
		return "", fmt.Errorf("vercel not found in PATH (required for deploy operations, install with: npm install -g vercel)")
	}

	cmd := exec.CommandContext(ctx, "vercel", args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("VERCEL_TOKEN=%s", c.apiToken))
	if c.teamID != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("VERCEL_ORG_ID=%s", c.teamID))
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		fmt.Printf("[vercel] vercel %s\n", strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("vercel CLI failed: %w, stderr: %s%s", err, stderrStr, errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// PromoteDeployment promotes a deployment to production using the Vercel REST API.
// This is equivalent to `vercel promote <deploymentId>` but uses the API directly
// for programmatic use (maker plans, backend handlers, etc.).
func (c *Client) PromoteDeployment(ctx context.Context, projectID, deploymentID string) error {
	endpoint := fmt.Sprintf("/v10/projects/%s/promote/%s", projectID, deploymentID)
	_, err := c.RunAPIWithContext(ctx, "POST", endpoint, "")
	if err != nil {
		return fmt.Errorf("promote deployment %s: %w", deploymentID, err)
	}
	return nil
}

// CancelDeployment cancels an in-progress deployment using the Vercel REST API.
func (c *Client) CancelDeployment(ctx context.Context, deploymentID string) error {
	endpoint := fmt.Sprintf("/v12/deployments/%s/cancel", deploymentID)
	result, err := c.RunAPIWithContext(ctx, "PATCH", endpoint, "")
	if err != nil {
		return fmt.Errorf("cancel deployment %s: %w", deploymentID, err)
	}
	fmt.Println(result)
	return nil
}

// GetRelevantContext gathers Vercel context for LLM queries. The output is a
// best-effort dump of the resources most likely to be relevant to the user's
// question. Sections are keyword-gated to keep the context compact.
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name     string
		endpoint string
		keys     []string
	}

	// Team-scoped endpoints are only added when a team ID is available.
	sections := []section{
		{name: "Projects", endpoint: "/v9/projects?limit=50", keys: []string{"project", "vercel", "deploy", "site", "app", "next", "nextjs"}},
		{name: "Deployments", endpoint: "/v6/deployments?limit=20", keys: []string{"deployment", "deploy", "preview", "production", "build", "rollback", "promote"}},
		{name: "Domains", endpoint: "/v5/domains?limit=50", keys: []string{"domain", "dns", "alias", "custom", "vercel.app"}},
	}
	if c.teamID != "" {
		sections = append(sections, section{
			name:     "Usage",
			endpoint: fmt.Sprintf("/v1/teams/%s/analytics/usage?period=30d", c.teamID),
			keys:     []string{"usage", "bandwidth", "cost", "invocation", "function", "edge", "analytics", "speed"},
		})
	}

	// Default sections: always include the project list so the LLM has a
	// baseline view of what exists.
	defaultSections := map[string]bool{
		"Projects": true,
	}

	var out strings.Builder
	var warnings []string

	for _, s := range sections {
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
		out.WriteString("Vercel Warnings:\n")
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
		return "No Vercel data available (missing permissions or no resources).", nil
	}

	return out.String(), nil
}

// checkAPIError inspects a Vercel JSON response for a top-level `error` object.
// Vercel errors have the shape: {"error":{"code":"...", "message":"..."}}
// Successful responses never carry that field, so its absence means "ok".
func checkAPIError(response string) error {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return nil
	}
	// Non-JSON responses (rare, mostly CLI outputs) bubble up unchanged.
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil
	}

	var apiResp struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(trimmed), &apiResp); err != nil {
		return nil
	}

	if apiResp.Error != nil && (apiResp.Error.Code != "" || apiResp.Error.Message != "") {
		return fmt.Errorf("API error: [%s] %s", apiResp.Error.Code, apiResp.Error.Message)
	}
	return nil
}

// isRetryableError determines whether to retry a Vercel API failure. The input
// may be either a raw stderr string from curl or an API error body — both the
// Vercel-documented error codes and common transport failure phrases are
// matched here.
func isRetryableError(s string) bool {
	lower := strings.ToLower(s)
	// Vercel API error codes surfaced in the error body.
	retryableCodes := []string{
		"rate_limited",
		"too_many_requests",
		"internal_server_error",
		"bad_gateway",
		"service_unavailable",
		"gateway_timeout",
	}
	for _, code := range retryableCodes {
		if strings.Contains(lower, code) {
			return true
		}
	}
	if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "rate_limit") {
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

// errorHint returns an actionable hint for common Vercel error messages.
func errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "not_authorized"):
		return " (hint: your Vercel token may be missing scope for this operation)"
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid_token") || strings.Contains(lower, "invalid token"):
		return " (hint: check your VERCEL_TOKEN is valid)"
	case strings.Contains(lower, "not_found") || strings.Contains(lower, "404"):
		return " (hint: resource not found — check project/deployment ID and team scope)"
	case strings.Contains(lower, "rate") && strings.Contains(lower, "limit"):
		return " (hint: rate limited, retrying with backoff)"
	case strings.Contains(lower, "team_not_found"):
		return " (hint: check your VERCEL_TEAM_ID / vercel.team_id is correct)"
	default:
		return ""
	}
}

// formatAPIResponse formats a Vercel JSON response for display in LLM prompts.
// Vercel returns two common shapes:
//   - { "<collection>": [ ... ], "pagination": {...} }
//   - [ ... ]  (rare)
//
// We pretty-print whichever shape we find under the expected collection key.
func formatAPIResponse(name, response string) string {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return ""
	}

	// Try pretty-printing a collection keyed by the lowercase section name.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &generic); err == nil {
		if raw, ok := generic[strings.ToLower(name)]; ok {
			var decoded interface{}
			if err := json.Unmarshal(raw, &decoded); err == nil {
				if pretty, err := json.MarshalIndent(decoded, "", "  "); err == nil {
					return fmt.Sprintf("%s:\n%s", name, string(pretty))
				}
			}
		}
	}

	// Fallback: pretty-print the whole body.
	var root interface{}
	if err := json.Unmarshal([]byte(trimmed), &root); err == nil {
		if pretty, err := json.MarshalIndent(root, "", "  "); err == nil {
			return fmt.Sprintf("%s:\n%s", name, string(pretty))
		}
	}
	return fmt.Sprintf("%s:\n%s", name, trimmed)
}
