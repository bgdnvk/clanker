// Package flyio provides a client for the Fly.io Machines REST API + the
// legacy GraphQL endpoint, plus a runner for the `flyctl` CLI. Mirrors the
// shape of the Vercel package so wiring into cmd/, routing, ask-mode and the
// desktop backend stays uniform.
//
// Fly.io splits its surface between two APIs:
//   - REST (https://api.machines.dev/v1): apps, machines, volumes, secrets,
//     IPs, certificates, releases. Modern surface.
//   - GraphQL (https://api.fly.io/graphql): orgs, postgres, wireguard,
//     tokens, add-ons (redis/tigris/mysql/sentry). Legacy but still required.
//
// Both share the same Bearer token. Org slug is a *filter* (a token can see
// multiple orgs), not a scope — list calls without an org slug return
// resources across every org the token can access.
package flyio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	baseURL    = "https://api.machines.dev/v1"
	graphqlURL = "https://api.fly.io/graphql"
)

// Client wraps the Fly.io REST + GraphQL APIs and the official `flyctl` CLI.
type Client struct {
	apiToken string
	orgSlug  string
	debug    bool
	// raw, when set, causes the static CLI commands to print unformatted JSON
	// responses instead of pretty-printed summaries.
	raw bool
}

// ResolveAPIToken returns the Fly.io API token from config or environment.
// Resolution order: `flyio.api_token` → FLY_API_TOKEN → FLY_ACCESS_TOKEN.
func ResolveAPIToken() string {
	if t := strings.TrimSpace(viper.GetString("flyio.api_token")); t != "" {
		return t
	}
	if env := strings.TrimSpace(os.Getenv("FLY_API_TOKEN")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("FLY_ACCESS_TOKEN")); env != "" {
		return env
	}
	return ""
}

// ResolveOrgSlug returns the Fly.io org slug from config or environment.
// Resolution order: `flyio.org_slug` → FLY_ORG → FLY_ORG_SLUG.
// Org scoping is optional — a token without an explicit org slug sees
// resources across every org it has access to.
func ResolveOrgSlug() string {
	if s := strings.TrimSpace(viper.GetString("flyio.org_slug")); s != "" {
		return s
	}
	if env := strings.TrimSpace(os.Getenv("FLY_ORG")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("FLY_ORG_SLUG")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Fly.io client.
func NewClient(apiToken, orgSlug string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("flyio api_token is required")
	}
	return &Client{
		apiToken: apiToken,
		orgSlug:  orgSlug,
		debug:    debug,
	}, nil
}

// BackendFlyioCredentials represents Fly.io credentials retrieved from the
// backend credential store (clanker-backend).
type BackendFlyioCredentials struct {
	APIToken string
	OrgSlug  string
}

// NewClientWithCredentials creates a new Fly.io client using backend credentials.
func NewClientWithCredentials(creds *BackendFlyioCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}
	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("flyio api_token is required")
	}
	return &Client{
		apiToken: creds.APIToken,
		orgSlug:  creds.OrgSlug,
		debug:    debug,
	}, nil
}

// SetRaw toggles raw-JSON output for the static CLI commands.
func (c *Client) SetRaw(raw bool) { c.raw = raw }

// Raw returns whether the client is configured for raw-JSON output.
func (c *Client) Raw() bool { return c.raw }

// GetAPIToken returns the API token.
func (c *Client) GetAPIToken() string { return c.apiToken }

// GetOrgSlug returns the org slug (may be empty when the token sees all orgs).
func (c *Client) GetOrgSlug() string { return c.orgSlug }

// withOrg appends `org_slug=<slug>` to the endpoint when the client is
// org-scoped and the endpoint does not already carry an org_slug parameter.
func (c *Client) withOrg(endpoint string) string {
	if c.orgSlug == "" {
		return endpoint
	}
	if strings.Contains(endpoint, "org_slug=") {
		return endpoint
	}
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + "org_slug=" + url.QueryEscape(c.orgSlug)
}

// RunAPI executes a Fly.io REST call with exponential backoff.
func (c *Client) RunAPI(method, endpoint, body string) (string, error) {
	return c.RunAPIWithContext(context.Background(), method, endpoint, body)
}

// RunAPIWithContext executes a Fly.io REST call with a caller-controlled context.
func (c *Client) RunAPIWithContext(ctx context.Context, method, endpoint, body string) (string, error) {
	if _, err := exec.LookPath("curl"); err != nil {
		return "", fmt.Errorf("curl not found in PATH")
	}

	endpoint = c.withOrg(endpoint)

	// Up to 3 total attempts (initial + 2 retries) for transient errors.
	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string
	var lastBody string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		lastBody = ""

		args := []string{
			"-s",
			"-X", method,
			baseURL + endpoint,
			"-H", fmt.Sprintf("Authorization: Bearer %s", c.apiToken),
			"-H", "Content-Type: application/json",
			"-H", "Accept: application/json",
		}

		if body != "" {
			args = append(args, "-d", body)
		}

		if c.debug {
			fmt.Printf("[flyio] curl -X %s %s%s\n", method, baseURL, endpoint)
		}

		cmd := exec.CommandContext(ctx, "curl", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			result := stdout.String()
			if apiErr := checkAPIError(result); apiErr != nil {
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
		return "", fmt.Errorf("flyio API call failed after retries: %s", lastBody)
	}
	if lastErr == nil {
		return "", fmt.Errorf("flyio API call failed")
	}
	return "", fmt.Errorf("flyio API call failed: %w, stderr: %s%s", lastErr, lastStderr, errorHint(lastStderr))
}

// RunGraphQL executes a Fly.io GraphQL call. The Fly GraphQL endpoint at
// api.fly.io is still required for orgs/billing/Postgres/Wireguard/tokens/
// add-ons. Variables, when non-empty, are inlined as a JSON object.
func (c *Client) RunGraphQL(ctx context.Context, query string, variables map[string]interface{}) (string, error) {
	if _, err := exec.LookPath("curl"); err != nil {
		return "", fmt.Errorf("curl not found in PATH")
	}

	payload := map[string]interface{}{"query": query}
	if len(variables) > 0 {
		payload["variables"] = variables
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("flyio graphql: marshal payload: %w", err)
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string
	var lastBody string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		lastBody = ""

		args := []string{
			"-s",
			"-X", "POST",
			graphqlURL,
			"-H", fmt.Sprintf("Authorization: Bearer %s", c.apiToken),
			"-H", "Content-Type: application/json",
			"-H", "Accept: application/json",
			"-d", string(body),
		}

		if c.debug {
			fmt.Printf("[flyio] graphql POST %s\n", graphqlURL)
		}

		cmd := exec.CommandContext(ctx, "curl", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			result := stdout.String()
			if apiErr := checkGraphQLError(result); apiErr != nil {
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
		return "", fmt.Errorf("flyio graphql call failed after retries: %s", lastBody)
	}
	if lastErr == nil {
		return "", fmt.Errorf("flyio graphql call failed")
	}
	return "", fmt.Errorf("flyio graphql call failed: %w, stderr: %s%s", lastErr, lastStderr, errorHint(lastStderr))
}

// RunFlyctl executes the official `flyctl` CLI tool for operations the REST
// API does not model well (deploy from source, ssh console, proxy, etc.).
// Lazy detection: try `flyctl` first, then fall back to the `fly` alias.
func (c *Client) RunFlyctl(args ...string) (string, error) {
	return c.RunFlyctlWithContext(context.Background(), args...)
}

// RunFlyctlWithContext executes the flyctl CLI with a caller-controlled context.
func (c *Client) RunFlyctlWithContext(ctx context.Context, args ...string) (string, error) {
	bin, err := resolveFlyctlBin()
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = c.flyctlEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		fmt.Printf("[flyio] %s %s\n", bin, strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("flyctl failed: %w, stderr: %s%s", err, stderrStr, errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// RunFlyctlWithStdin runs flyctl piping stdinData to the process's standard
// input. Used for `flyctl secrets import` and similar value-bearing commands
// where echoing values on the command line is unsafe.
func (c *Client) RunFlyctlWithStdin(ctx context.Context, stdinData string, args ...string) (string, error) {
	bin, err := resolveFlyctlBin()
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = c.flyctlEnv()
	cmd.Stdin = strings.NewReader(stdinData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		fmt.Printf("[flyio] %s %s (stdin piped)\n", bin, strings.Join(args, " "))
	}

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("flyctl failed: %w, stderr: %s%s", err, stderrStr, errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// flyctlEnv returns the environment for a flyctl subprocess with the client's
// credentials injected (and FLY_ORG when scoped).
func (c *Client) flyctlEnv() []string {
	env := append(os.Environ(), fmt.Sprintf("FLY_API_TOKEN=%s", c.apiToken))
	if c.orgSlug != "" {
		env = append(env, fmt.Sprintf("FLY_ORG=%s", c.orgSlug))
	}
	return env
}

// resolveFlyctlBin returns the path to the flyctl binary, trying `flyctl`
// first then the `fly` alias. The install hint matches the documented Fly
// install paths for macOS, Linux, and Windows.
func resolveFlyctlBin() (string, error) {
	if path, err := exec.LookPath("flyctl"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("fly"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("flyctl not found in PATH (install with: brew install flyctl | curl -L https://fly.io/install.sh | sh)")
}

// FlyctlInstalled reports whether flyctl (or the `fly` alias) is on PATH.
// Helpers can call this up-front to surface a friendly install hint instead
// of failing inside a deeper RunFlyctl call.
func FlyctlInstalled() bool {
	if _, err := exec.LookPath("flyctl"); err == nil {
		return true
	}
	if _, err := exec.LookPath("fly"); err == nil {
		return true
	}
	return false
}

// GetRelevantContext gathers Fly.io context for LLM queries. The output is a
// best-effort dump of the resources most likely to be relevant to the user's
// question. Sections are keyword-gated to keep the context compact.
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name     string
		endpoint string
		keys     []string
	}

	sections := []section{
		{name: "Apps", endpoint: "/apps", keys: []string{"app", "fly", "deploy", "service", "running"}},
		{name: "Regions", endpoint: "/platform/regions", keys: []string{"region", "where", "geo", "iad", "lhr", "syd", "gru", "ewr", "fra"}},
	}

	// Apps is always included so the LLM has a baseline view.
	defaultSections := map[string]bool{
		"Apps": true,
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

	// Machines context: only fan out when the question mentions machines and
	// we have apps available. Per-app fetches are expensive so we cap at 5
	// apps and inline a summary instead of full machine bodies.
	if questionLower == "" || mentionsMachineKeywords(questionLower) {
		machineCtx, mErr := c.collectMachineContext(ctx)
		if mErr != nil {
			warnings = append(warnings, fmt.Sprintf("Machines: %v", mErr))
		} else if machineCtx != "" {
			out.WriteString(machineCtx)
			out.WriteString("\n")
		}
	}

	if flyioObservabilityLogIntent(questionLower) {
		logsCtx, logsErr := c.collectAppLogsContext(ctx)
		if logsErr != nil {
			warnings = append(warnings, fmt.Sprintf("App Logs: %v", logsErr))
		} else if logsCtx != "" {
			out.WriteString(logsCtx)
			out.WriteString("\n")
		}
	}

	// Volumes / Postgres / Secrets context: GraphQL fan-out when explicitly
	// asked about; Phase 1 keeps this minimal to keep prompt sizes small.

	if len(warnings) > 0 {
		out.WriteString("Fly.io Warnings:\n")
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
		return "No Fly.io data available (missing permissions or no resources).", nil
	}

	return out.String(), nil
}

func flyioObservabilityLogIntent(questionLower string) bool {
	return containsAnyFlyioPhrase(questionLower,
		"log", "logs", "event", "events", "error", "errors", "warning", "warnings",
		"trace", "traces", "metric", "metrics", "observability", "incident", "incidents")
}

func containsAnyFlyioPhrase(value string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

// mentionsMachineKeywords gates the per-app machine fan-out in context gather.
func mentionsMachineKeywords(q string) bool {
	keywords := []string{
		"machine", "machines", "vm", "vms", "instance", "instances",
		"running", "started", "stopped", "scale", "scaling", "replicas",
	}
	for _, k := range keywords {
		if strings.Contains(q, k) {
			return true
		}
	}
	return false
}

type flyioAppSummary struct {
	Name string `json:"name"`
}

func parseFlyioApps(body string) ([]flyioAppSummary, error) {
	var appsResp struct {
		Apps []flyioAppSummary `json:"apps"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &appsResp); err == nil && len(appsResp.Apps) > 0 {
		return appsResp.Apps, nil
	}
	var bare []flyioAppSummary
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &bare); err == nil {
		return bare, nil
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &appsResp); err != nil {
		return nil, err
	}
	return appsResp.Apps, nil
}

func (c *Client) collectAppLogsContext(ctx context.Context) (string, error) {
	appsBody, err := c.RunAPIWithContext(ctx, "GET", "/apps", "")
	if err != nil {
		return "", err
	}
	apps, err := parseFlyioApps(appsBody)
	if err != nil {
		return "", err
	}
	if len(apps) == 0 {
		return "App Logs:\nNo Fly.io apps found.\n", nil
	}

	maxApps := minFlyioInt(len(apps), 3)
	var out strings.Builder
	out.WriteString("App Logs:\n")
	for i := 0; i < maxApps; i++ {
		appName := strings.TrimSpace(apps[i].Name)
		if appName == "" {
			continue
		}
		body, err := c.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(appName)+"/logs", "")
		if err != nil {
			out.WriteString(fmt.Sprintf("%s: logs unavailable: %v\n", appName, err))
			continue
		}
		out.WriteString(fmt.Sprintf("%s:\n", appName))
		out.WriteString(indentFlyioLines(truncateFlyioContext(strings.TrimSpace(body), 2500), "  "))
		if !strings.HasSuffix(out.String(), "\n") {
			out.WriteString("\n")
		}
	}
	if len(apps) > maxApps {
		out.WriteString(fmt.Sprintf("(... %d more apps omitted)\n", len(apps)-maxApps))
	}
	return out.String(), nil
}

// collectMachineContext fetches up to 5 apps and lists their machines as a
// compact summary line per machine. Used by GetRelevantContext.
func (c *Client) collectMachineContext(ctx context.Context) (string, error) {
	appsBody, err := c.RunAPIWithContext(ctx, "GET", "/apps", "")
	if err != nil {
		return "", err
	}
	apps, err := parseFlyioApps(appsBody)
	if err != nil {
		return "", err
	}

	if len(apps) == 0 {
		return "", nil
	}

	maxApps := 5
	if len(apps) < maxApps {
		maxApps = len(apps)
	}

	var out strings.Builder
	out.WriteString("Machines (top apps):\n")
	for i := 0; i < maxApps; i++ {
		appName := apps[i].Name
		machinesBody, mErr := c.RunAPIWithContext(ctx, "GET", "/apps/"+url.PathEscape(appName)+"/machines", "")
		if mErr != nil {
			out.WriteString(fmt.Sprintf("  %s: (error: %v)\n", appName, mErr))
			continue
		}

		var machines []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			State  string `json:"state"`
			Region string `json:"region"`
			Config struct {
				Image string `json:"image"`
				Guest struct {
					CPUKind  string `json:"cpu_kind"`
					CPUs     int    `json:"cpus"`
					MemoryMB int    `json:"memory_mb"`
				} `json:"guest"`
			} `json:"config"`
		}
		if err := json.Unmarshal([]byte(machinesBody), &machines); err != nil {
			out.WriteString(fmt.Sprintf("  %s: (parse error)\n", appName))
			continue
		}

		if len(machines) == 0 {
			out.WriteString(fmt.Sprintf("  %s: (no machines)\n", appName))
			continue
		}

		out.WriteString(fmt.Sprintf("  %s:\n", appName))
		for _, m := range machines {
			guest := fmt.Sprintf("%s-%d %dMB", m.Config.Guest.CPUKind, m.Config.Guest.CPUs, m.Config.Guest.MemoryMB)
			out.WriteString(fmt.Sprintf("    - %s [%s] %s %s\n", m.ID, m.State, m.Region, guest))
		}
	}

	if len(apps) > maxApps {
		out.WriteString(fmt.Sprintf("  (... %d more apps omitted)\n", len(apps)-maxApps))
	}

	return out.String(), nil
}

func truncateFlyioContext(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "...<truncated>"
}

func indentFlyioLines(value string, prefix string) string {
	if strings.TrimSpace(value) == "" {
		return prefix + "(no log entries returned)\n"
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n") + "\n"
}

func minFlyioInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// checkAPIError inspects a Fly.io REST response for an error object. Fly
// REST errors come back as {"error":"message"} or sometimes with a deeper
// {"errors":[...]} envelope.
func checkAPIError(response string) error {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil
	}

	var apiResp struct {
		Error  string `json:"error"`
		Status string `json:"status"`
		// Some endpoints return {"errors":[{"message":"..."}]}.
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal([]byte(trimmed), &apiResp); err != nil {
		return nil
	}

	if strings.TrimSpace(apiResp.Error) != "" {
		return fmt.Errorf("API error: %s", apiResp.Error)
	}
	if len(apiResp.Errors) > 0 {
		messages := make([]string, 0, len(apiResp.Errors))
		for _, e := range apiResp.Errors {
			if strings.TrimSpace(e.Message) != "" {
				messages = append(messages, e.Message)
			}
		}
		if len(messages) > 0 {
			return fmt.Errorf("API error: %s", strings.Join(messages, "; "))
		}
	}

	return nil
}

// checkGraphQLError inspects a Fly.io GraphQL response for the standard
// `errors` envelope.
func checkGraphQLError(response string) error {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	var resp struct {
		Errors []struct {
			Message    string                 `json:"message"`
			Extensions map[string]interface{} `json:"extensions"`
		} `json:"errors"`
	}

	if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
		return nil
	}

	if len(resp.Errors) == 0 {
		return nil
	}

	messages := make([]string, 0, len(resp.Errors))
	for _, e := range resp.Errors {
		if strings.TrimSpace(e.Message) != "" {
			messages = append(messages, e.Message)
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("graphql error: %s", strings.Join(messages, "; "))
}

// isRetryableError mirrors the Vercel package's logic — Fly returns similar
// codes for the same conditions (429, 5xx, transport hiccups).
func isRetryableError(s string) bool {
	lower := strings.ToLower(s)
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

// errorHint returns an actionable hint for common Fly.io error messages.
func errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid_token") || strings.Contains(lower, "invalid token"):
		return " (hint: check your FLY_API_TOKEN is valid — generate one with `flyctl auth token` or at fly.io/dashboard/personal/tokens)"
	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "not_authorized"):
		return " (hint: your Fly token may lack scope for this org/app — try a deploy token instead of a read-only one)"
	case strings.Contains(lower, "app_not_found") || strings.Contains(lower, "not_found") || strings.Contains(lower, "404"):
		return " (hint: app/resource not found — check name and org scope)"
	case strings.Contains(lower, "rate") && strings.Contains(lower, "limit"):
		return " (hint: rate limited, retrying with backoff)"
	case strings.Contains(lower, "region_unavailable") || strings.Contains(lower, "no capacity"):
		return " (hint: region capacity exhausted — try a different region or wait and retry)"
	case strings.Contains(lower, "quota_exceeded") || strings.Contains(lower, "billing"):
		return " (hint: org quota or billing issue — check fly.io/dashboard/{org}/billing)"
	case strings.Contains(lower, "app_in_use"):
		return " (hint: app name already taken — pick a different name)"
	default:
		return ""
	}
}

// formatAPIResponse pretty-prints a Fly.io JSON response for LLM prompts.
// Fly REST returns two common shapes:
//   - {"apps":[...]} / {"machines":[...]} — keyed object
//   - [...]                              — bare array (less common)
//
// We pretty-print whichever shape we find. The section name is included in
// the output for context.
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
