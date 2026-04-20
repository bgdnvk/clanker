// Package verda provides a client for the Verda Cloud REST API and the
// `verda` CLI. Verda (ex-DataCrunch) is a European GPU/AI cloud. The package
// mirrors the shape of internal/vercel so wiring into cmd/, routing, ask-mode,
// and the desktop backend stays uniform.
package verda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// ErrCLINotInstalled is returned by RunVerdaCLI* when the `verda` binary is
// missing from PATH. Typed so callers can surface a clear, one-shot error
// instead of the generic exec failure and can branch with errors.Is.
var ErrCLINotInstalled = errors.New("verda CLI not installed")

// IsCLINotInstalled reports whether err indicates the verda binary is missing.
// Shorthand for `errors.Is(err, ErrCLINotInstalled)` so call sites don't need
// to import errors explicitly.
func IsCLINotInstalled(err error) bool { return errors.Is(err, ErrCLINotInstalled) }

// CLIInstalled reports whether the `verda` binary is reachable on PATH.
// Fast (just a lookpath) so callers can branch UX eagerly — e.g., to hide
// a "login with verda CLI" hint when the binary isn't present but the
// REST credentials are configured and usable.
func CLIInstalled() bool {
	_, err := exec.LookPath("verda")
	return err == nil
}

// BaseURL is the Verda API base; the `/v1` prefix is part of every path we call.
const BaseURL = "https://api.verda.com"

// baseURLForTest lets tests redirect API + token calls at an httptest server.
// When empty, the production BaseURL is used.
var baseURLForTest = ""

// SetBaseURLForTest exposes the test-only base URL override to external test
// packages (e.g. internal/maker/exec_verda_test.go that need to run the full
// executor against an httptest server). Returns the previous value so tests
// can restore on cleanup. Never call from production code.
func SetBaseURLForTest(url string) string {
	prev := baseURLForTest
	baseURLForTest = url
	return prev
}

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

// ensureToken returns a valid OAuth2 bearer token, fetching or refreshing
// as needed. The mutex is only held for cache reads and writes — HTTP I/O
// happens with the lock dropped so concurrent callers can't deadlock if
// the transport ever taps back into c.mu.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	// Fast path: cached token still valid.
	c.mu.Lock()
	if c.accessToken != "" && time.Until(c.tokenExpiry) > 30*time.Second {
		tok := c.accessToken
		c.mu.Unlock()
		return tok, nil
	}
	refreshTok := c.refreshToken
	c.mu.Unlock()

	// Slow path: attempt a refresh_token flow first when we have one,
	// fall back to client_credentials if refresh fails or is absent.
	if refreshTok != "" {
		tr, err := c.fetchToken(ctx, map[string]string{
			"grant_type":    "refresh_token",
			"refresh_token": refreshTok,
		})
		if err == nil {
			return c.storeToken(tr), nil
		}
		// Drop the broken refresh token so subsequent callers don't retry it.
		c.mu.Lock()
		if c.refreshToken == refreshTok {
			c.refreshToken = ""
		}
		c.mu.Unlock()
	}

	tr, err := c.fetchToken(ctx, map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
	})
	if err != nil {
		return "", err
	}
	return c.storeToken(tr), nil
}

// fetchToken performs a single POST /v1/oauth2/token with the provided body.
// Does no locking — callers are responsible for any synchronization.
func (c *Client) fetchToken(ctx context.Context, body map[string]string) (*TokenResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, effectiveBaseURL()+"/v1/oauth2/token", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.debug {
		fmt.Printf("[verda] POST /v1/oauth2/token grant=%s\n", body["grant_type"])
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("verda token request failed: %w", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, decodeAPIErrorBody(buf.Bytes(), resp.StatusCode)
	}

	var tr TokenResponse
	if err := json.Unmarshal(buf.Bytes(), &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w (body: %s)", err, buf.String())
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("verda token response missing access_token (body: %s)", buf.String())
	}
	return &tr, nil
}

// storeToken persists a TokenResponse into the client cache and returns the
// access token.
func (c *Client) storeToken(tr *TokenResponse) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = tr.AccessToken
	c.refreshToken = tr.RefreshToken
	if tr.ExpiresIn > 0 {
		c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		c.tokenExpiry = time.Now().Add(55 * time.Minute)
	}
	return c.accessToken
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
			if err := sleepCtx(ctx, backoffs[attempt]); err != nil {
				return "", err
			}
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
				if err := sleepCtx(ctx, wait); err != nil {
					return "", err
				}
				continue
			}
			return string(resp.Body), decodeAPIErrorBody(resp.Body, resp.StatusCode)
		}

		if resp.StatusCode >= 500 && resp.StatusCode < 600 && attempt < maxAttempts-1 {
			if err := sleepCtx(ctx, backoffs[attempt]); err != nil {
				return "", err
			}
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

// sleepCtx blocks for the given duration or returns when ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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

// ResolveInstanceID returns the Verda instance UUID for the given input. If
// `nameOrID` already looks like a UUID it's returned verbatim (lowercased).
// Otherwise we list instances and match by hostname. Used by CLI commands so
// users can pass a friendly hostname where an ID is expected.
func (c *Client) ResolveInstanceID(ctx context.Context, nameOrID string) (string, error) {
	trimmed := strings.TrimSpace(nameOrID)
	if trimmed == "" {
		return "", fmt.Errorf("empty instance identifier")
	}
	lowered := strings.ToLower(trimmed)
	if looksLikeUUIDString(lowered) {
		return lowered, nil
	}
	body, err := c.RunAPIWithContext(ctx, http.MethodGet, "/v1/instances", "")
	if err != nil {
		return "", err
	}
	var list []Instance
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return "", fmt.Errorf("decode instances: %w", err)
	}
	for _, inst := range list {
		if strings.EqualFold(inst.Hostname, trimmed) || inst.ID == trimmed {
			return inst.ID, nil
		}
	}
	return "", fmt.Errorf("no verda instance found for %q (tried hostname and ID match)", nameOrID)
}

// looksLikeUUIDString is the package-private mirror of the k8s cluster provider
// helper — kept here so CLI paths don't reach across packages just for this.
func looksLikeUUIDString(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
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
		return "", fmt.Errorf("%w — install it from https://docs.verda.com/cli/ (brew install verda-cloud/tap/verda-cli), or configure client_id / client_secret directly in ~/.clanker.yaml and use the REST paths instead", ErrCLINotInstalled)
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
		{name: "ContainerDeployments", path: "/v1/container-deployments", keys: []string{"container", "serverless", "inference", "endpoint", "replica", "scale"}},
		{name: "JobDeployments", path: "/v1/job-deployments", keys: []string{"job", "batch", "queue", "serverless", "scaled"}},
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
