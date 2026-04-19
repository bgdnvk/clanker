// Package verda provides a client for the Verda Cloud REST API and the
// `verda` CLI. Verda (ex-DataCrunch) is a European GPU/AI cloud. The package
// mirrors the shape of internal/vercel so wiring into cmd/, routing, ask-mode,
// and the desktop backend stays uniform.
package verda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// BaseURL is the Verda API base; the `/v1` prefix is part of every path we call.
const BaseURL = "https://api.verda.com"

// baseURLForTest lets tests redirect API + token calls at an httptest server.
// When empty, the production BaseURL is used.
var baseURLForTest = ""

func effectiveBaseURL() string {
	if baseURLForTest != "" {
		return baseURLForTest
	}
	return BaseURL
}

// Client wraps the Verda REST API (OAuth2 Client Credentials) and the official
// `verda` CLI. A single client is safe to share across goroutines — the token
// cache is mutex-protected.
type Client struct {
	clientID     string
	clientSecret string
	projectID    string
	debug        bool
	httpClient   *http.Client

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	tokenExpiry  time.Time
}

// ResolveClientID returns the Verda client ID.
// Resolution order: `verda.client_id` viper key → VERDA_CLIENT_ID env →
// parsed ~/.verda/credentials.
func ResolveClientID() string {
	if v := strings.TrimSpace(viper.GetString("verda.client_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("VERDA_CLIENT_ID")); v != "" {
		return v
	}
	if v, _ := readVerdaCredentials(); v.ClientID != "" {
		return v.ClientID
	}
	return ""
}

// ResolveClientSecret returns the Verda client secret.
// Resolution order mirrors ResolveClientID.
func ResolveClientSecret() string {
	if v := strings.TrimSpace(viper.GetString("verda.client_secret")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("VERDA_CLIENT_SECRET")); v != "" {
		return v
	}
	if v, _ := readVerdaCredentials(); v.ClientSecret != "" {
		return v.ClientSecret
	}
	return ""
}

// ResolveProjectID returns the Verda project ID (used as conversation scope).
// Resolution order: `verda.default_project_id` → VERDA_PROJECT_ID → empty.
func ResolveProjectID() string {
	if v := strings.TrimSpace(viper.GetString("verda.default_project_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("VERDA_PROJECT_ID")); v != "" {
		return v
	}
	return ""
}

// ResolveDefaultLocation returns the configured default Verda location code
// (e.g. "FIN-01"). Used by create-flow helpers when the user doesn't pass --location.
func ResolveDefaultLocation() string {
	return strings.TrimSpace(viper.GetString("verda.default_location"))
}

// ResolveDefaultSSHKeyID returns the configured default Verda SSH key UUID.
// Used by `clanker verda deploy` and the verda-instant k8s cluster provider.
func ResolveDefaultSSHKeyID() string {
	return strings.TrimSpace(viper.GetString("verda.default_ssh_key_id"))
}

// ResolveSSHKeyPath returns the local private-key path used when pulling
// kubeconfig off a Verda Instant Cluster's head node. Defaults to
// ~/.ssh/id_ed25519 if not configured.
func ResolveSSHKeyPath() string {
	if v := strings.TrimSpace(viper.GetString("verda.ssh_key_path")); v != "" {
		return expandHome(v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ssh", "id_ed25519")
	}
	return ""
}

// verdaCredsFile mirrors the minimum fields we care about in ~/.verda/credentials.
// The file format isn't officially documented; the Verda CLI uses a YAML-ish layout.
// We tolerate both YAML and JSON and fall through gracefully when neither parses.
type verdaCredsFile struct {
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"client_secret"`
	// The official CLI uses named profiles; we pick the active one when present.
	ActiveProfile string                     `yaml:"active_profile" json:"active_profile"`
	Profiles      map[string]verdaCredsEntry `yaml:"profiles" json:"profiles"`
}

type verdaCredsEntry struct {
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"client_secret"`
}

// readVerdaCredentials parses ~/.verda/credentials in either YAML or JSON form.
// Returns the effective client_id / client_secret pair, preferring the active
// profile if the file uses the multi-profile layout.
func readVerdaCredentials() (verdaCredsEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return verdaCredsEntry{}, err
	}
	path := filepath.Join(home, ".verda", "credentials")
	data, err := os.ReadFile(path)
	if err != nil {
		return verdaCredsEntry{}, err
	}

	// Try YAML first (the Verda CLI's native format).
	var y verdaCredsFile
	if err := yaml.Unmarshal(data, &y); err == nil {
		if entry, ok := activeProfile(y); ok {
			return entry, nil
		}
	}

	// Fallback: JSON.
	var j verdaCredsFile
	if err := json.Unmarshal(data, &j); err == nil {
		if entry, ok := activeProfile(j); ok {
			return entry, nil
		}
	}

	return verdaCredsEntry{}, fmt.Errorf("could not parse %s", path)
}

func activeProfile(f verdaCredsFile) (verdaCredsEntry, bool) {
	if f.ClientID != "" || f.ClientSecret != "" {
		return verdaCredsEntry{ClientID: f.ClientID, ClientSecret: f.ClientSecret}, true
	}
	if len(f.Profiles) == 0 {
		return verdaCredsEntry{}, false
	}
	name := f.ActiveProfile
	if name == "" {
		name = "default"
	}
	if entry, ok := f.Profiles[name]; ok && (entry.ClientID != "" || entry.ClientSecret != "") {
		return entry, true
	}
	for _, entry := range f.Profiles {
		if entry.ClientID != "" || entry.ClientSecret != "" {
			return entry, true
		}
	}
	return verdaCredsEntry{}, false
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// NewClient creates a Verda client. The projectID is optional and used only
// for conversation-history keying.
func NewClient(clientID, clientSecret, projectID string, debug bool) (*Client, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("verda client_id and client_secret are required")
	}
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		projectID:    projectID,
		debug:        debug,
		httpClient:   &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// ClientID exposes the client ID (used for conversation-history keying).
func (c *Client) ClientID() string { return c.clientID }

// ProjectID exposes the project ID.
func (c *Client) ProjectID() string { return c.projectID }

// ensureToken obtains or refreshes the OAuth2 bearer token. Safe to call from
// any request path.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Keep a small margin before expiry so we don't race the clock.
	if c.accessToken != "" && time.Until(c.tokenExpiry) > 30*time.Second {
		return c.accessToken, nil
	}

	var body map[string]string
	if c.refreshToken != "" {
		body = map[string]string{
			"grant_type":    "refresh_token",
			"refresh_token": c.refreshToken,
		}
	} else {
		body = map[string]string{
			"grant_type":    "client_credentials",
			"client_id":     c.clientID,
			"client_secret": c.clientSecret,
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, effectiveBaseURL()+"/v1/oauth2/token", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.debug {
		fmt.Printf("[verda] POST /v1/oauth2/token grant=%s\n", body["grant_type"])
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Refresh can fail if the refresh token has expired; retry once with
		// client_credentials so the caller doesn't need to reason about it.
		if c.refreshToken != "" {
			c.refreshToken = ""
			c.accessToken = ""
			c.tokenExpiry = time.Time{}
			return c.ensureTokenLocked(ctx)
		}
		return "", fmt.Errorf("verda token request failed: %w", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Retry once with fresh client credentials if we just tried refresh_token.
		if c.refreshToken != "" {
			c.refreshToken = ""
			c.accessToken = ""
			c.tokenExpiry = time.Time{}
			return c.ensureTokenLocked(ctx)
		}
		return "", decodeAPIErrorBody(buf.Bytes(), resp.StatusCode)
	}

	var tr TokenResponse
	if err := json.Unmarshal(buf.Bytes(), &tr); err != nil {
		return "", fmt.Errorf("decode token response: %w (body: %s)", err, buf.String())
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("verda token response missing access_token (body: %s)", buf.String())
	}

	c.accessToken = tr.AccessToken
	c.refreshToken = tr.RefreshToken
	if tr.ExpiresIn > 0 {
		c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		c.tokenExpiry = time.Now().Add(55 * time.Minute)
	}

	return c.accessToken, nil
}

// ensureTokenLocked is called after we've reset the cached token state inside
// ensureToken; it drops the mutex safely because the caller holds it.
func (c *Client) ensureTokenLocked(ctx context.Context) (string, error) {
	// We're already holding c.mu — unlock and reacquire via the public path
	// would deadlock. Inline the minimum client-credentials flow here.
	body, err := json.Marshal(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, effectiveBaseURL()+"/v1/oauth2/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 400 {
		return "", decodeAPIErrorBody(buf.Bytes(), resp.StatusCode)
	}
	var tr TokenResponse
	if err := json.Unmarshal(buf.Bytes(), &tr); err != nil {
		return "", err
	}
	c.accessToken = tr.AccessToken
	c.refreshToken = tr.RefreshToken
	if tr.ExpiresIn > 0 {
		c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		c.tokenExpiry = time.Now().Add(55 * time.Minute)
	}
	return c.accessToken, nil
}

// apiResponse carries the decoded parts of a Verda API call we may care about
// downstream (status, body, rate-limit headers, multi-status array).
type apiResponse struct {
	StatusCode int
	Body       []byte
	RetryAfter time.Duration
}

// RunAPI executes a Verda REST call with retry/backoff.
func (c *Client) RunAPI(method, path, body string) (string, error) {
	return c.RunAPIWithContext(context.Background(), method, path, body)
}

// RunAPIWithContext executes a Verda REST call with a caller-controlled context.
// Path should start with `/v1/...`. Bodies are passed as a JSON string (empty for GETs).
// Retries on 429 (honoring Retry-After) and 5xx with capped exponential backoff.
func (c *Client) RunAPIWithContext(ctx context.Context, method, path, body string) (string, error) {
	const maxAttempts = 4
	backoffs := []time.Duration{
		250 * time.Millisecond,
		750 * time.Millisecond,
		2 * time.Second,
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, err := c.ensureToken(ctx)
		if err != nil {
			return "", err
		}

		resp, err := c.doRequest(ctx, method, path, body, token)
		if err != nil {
			lastErr = err
			if !isRetryableNetError(err) || attempt == maxAttempts-1 {
				return "", err
			}
			time.Sleep(backoffs[attempt])
			continue
		}

		// 401 once means our cached token is stale (server-side revoke) — drop it
		// and retry once.
		if resp.StatusCode == http.StatusUnauthorized {
			c.mu.Lock()
			c.accessToken = ""
			c.refreshToken = ""
			c.tokenExpiry = time.Time{}
			c.mu.Unlock()
			if attempt < maxAttempts-1 {
				continue
			}
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt < maxAttempts-1 {
				wait := resp.RetryAfter
				if wait == 0 {
					wait = backoffs[attempt]
				}
				time.Sleep(wait)
				continue
			}
			return string(resp.Body), decodeAPIErrorBody(resp.Body, resp.StatusCode)
		}

		if resp.StatusCode >= 500 && resp.StatusCode < 600 && attempt < maxAttempts-1 {
			time.Sleep(backoffs[attempt])
			continue
		}

		if resp.StatusCode >= 400 {
			return string(resp.Body), decodeAPIErrorBody(resp.Body, resp.StatusCode)
		}

		return string(resp.Body), nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("verda API call failed after %d attempts", maxAttempts)
	}
	return "", lastErr
}

func (c *Client) doRequest(ctx context.Context, method, path, body, token string) (*apiResponse, error) {
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}

	fullURL := effectiveBaseURL() + path
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(ctx, method, fullURL, reader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, fullURL, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.debug {
		fmt.Printf("[verda] %s %s\n", method, path)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	out := &apiResponse{
		StatusCode: resp.StatusCode,
		Body:       buf.Bytes(),
	}

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			out.RetryAfter = time.Duration(secs) * time.Second
		}
	}

	return out, nil
}

// decodeAPIErrorBody turns a non-2xx body into an *APIError when it matches
// Verda's {code,message} shape; otherwise returns a plain-text error that
// preserves the raw body for debugging.
func decodeAPIErrorBody(body []byte, status int) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var apiErr APIError
		if err := json.Unmarshal(trimmed, &apiErr); err == nil && (apiErr.Code != "" || apiErr.Message != "") {
			return &apiErr
		}
	}
	return fmt.Errorf("verda API HTTP %d: %s", status, strings.TrimSpace(string(body)))
}

func isRetryableNetError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "temporarily unavailable")
}

// DecodeActionResults decodes a 207 Multi-Status body from PUT /v1/instances.
// When the Verda API succeeds uniformly it returns 202 with a plain JSON body;
// a partial failure comes back as an array of ActionResult entries.
func DecodeActionResults(body string) ([]ActionResult, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return nil, nil
	}
	var results []ActionResult
	if err := json.Unmarshal([]byte(trimmed), &results); err != nil {
		return nil, fmt.Errorf("decode action results: %w", err)
	}
	return results, nil
}

// RunVerdaCLI shells out to the `verda` binary with `--agent` enforced so we
// get structured JSON on stdout. Useful for commands the CLI covers but we
// don't want to re-plumb through REST (e.g. `verda auth show`).
func (c *Client) RunVerdaCLI(args ...string) (string, error) {
	return c.RunVerdaCLIWithContext(context.Background(), args...)
}

// RunVerdaCLIWithContext runs the Verda CLI with a caller-controlled context.
// Credentials flow through env vars so the child process picks them up without
// needing a logged-in profile.
func (c *Client) RunVerdaCLIWithContext(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("verda"); err != nil {
		return "", fmt.Errorf("verda CLI not found in PATH (install from https://docs.verda.com/cli/)")
	}

	fullArgs := append([]string{"--agent"}, args...)

	cmd := exec.CommandContext(ctx, "verda", fullArgs...)
	cmd.Env = append(os.Environ(),
		"VERDA_CLIENT_ID="+c.clientID,
		"VERDA_CLIENT_SECRET="+c.clientSecret,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if c.debug {
		fmt.Printf("[verda] verda %s\n", strings.Join(fullArgs, " "))
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("verda CLI failed: %w, stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// GetRelevantContext gathers Verda context for LLM queries. Keyword-gated so
// we don't fetch every resource type on every question. Sections the user's
// question doesn't reference are skipped unless they're marked default.
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name   string
		path   string
		keys   []string
		always bool
	}

	sections := []section{
		{name: "Instances", path: "/v1/instances", keys: []string{"instance", "vm", "gpu", "server", "running", "spot", "h100", "a100", "h200", "b200", "l40s", "v100", "a6000"}, always: true},
		{name: "Clusters", path: "/v1/clusters", keys: []string{"cluster", "kubernetes", "k8s", "slurm", "instant cluster", "training"}},
		{name: "Volumes", path: "/v1/volumes", keys: []string{"volume", "disk", "storage", "sfs", "shared filesystem", "nvme", "hdd"}},
		{name: "SSHKeys", path: "/v1/ssh-keys", keys: []string{"ssh", "key", "access"}},
		{name: "Scripts", path: "/v1/scripts?pageSize=25", keys: []string{"script", "startup", "cloud-init"}},
		{name: "Balance", path: "/v1/balance", keys: []string{"balance", "credit", "cost", "bill", "spend", "afford"}, always: true},
		{name: "Locations", path: "/v1/locations", keys: []string{"location", "region", "datacenter", "availability"}},
	}

	var out strings.Builder
	var warnings []string

	for _, s := range sections {
		if !s.always && questionLower != "" && len(s.keys) > 0 {
			matched := false
			for _, key := range s.keys {
				if strings.Contains(questionLower, key) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		result, err := c.RunAPIWithContext(ctx, http.MethodGet, s.path, "")
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
		out.WriteString("Verda Warnings:\n")
		for i, w := range warnings {
			if i >= 8 {
				out.WriteString("- (additional warnings omitted)\n")
				break
			}
			out.WriteString("- ")
			out.WriteString(w)
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	if strings.TrimSpace(out.String()) == "" {
		return "No Verda data available (missing permissions or no resources).", nil
	}

	return out.String(), nil
}

// formatAPIResponse pretty-prints a Verda JSON response for inclusion in an
// LLM prompt. Verda returns plain arrays for most list endpoints and plain
// objects for scalar endpoints (balance, locations items).
func formatAPIResponse(name, response string) string {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return ""
	}
	var root interface{}
	if err := json.Unmarshal([]byte(trimmed), &root); err == nil {
		if pretty, err := json.MarshalIndent(root, "", "  "); err == nil {
			return fmt.Sprintf("%s:\n%s", name, string(pretty))
		}
	}
	return fmt.Sprintf("%s:\n%s", name, trimmed)
}
