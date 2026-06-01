package sentry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	defaultHost   = "sentry.io"
	apiPathPrefix = "/api/0"
	userAgent     = "clanker-cli"
)

// Client is a thin REST wrapper around the Sentry management API.
//
// Auth is Bearer-token via the User Auth Token (Settings → Account → API).
// We hit https://{host}/api/0/ directly with net/http — there is no official
// Go management SDK; getsentry/sentry-go is for error reporting only.
type Client struct {
	host       string
	authToken  string
	orgSlug    string
	httpClient *http.Client
	debug      bool
}

// ResolveAuthToken returns the configured Sentry User Auth Token, checking
// config first then environment, mirroring how cloudflare/client.go resolves
// its credentials.
func ResolveAuthToken() string {
	if t := strings.TrimSpace(viper.GetString("sentry.auth_token")); t != "" {
		return t
	}
	if t := strings.TrimSpace(os.Getenv("SENTRY_AUTH_TOKEN")); t != "" {
		return t
	}
	return ""
}

func ResolveOrgSlug() string {
	if s := strings.TrimSpace(viper.GetString("sentry.org_slug")); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("SENTRY_ORG")); s != "" {
		return s
	}
	return ""
}

func ResolveDefaultProject() string {
	if s := strings.TrimSpace(viper.GetString("sentry.default_project")); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("SENTRY_PROJECT")); s != "" {
		return s
	}
	return ""
}

// ResolveHost returns the Sentry host, defaulting to sentry.io. Self-hosted
// users set this to their on-prem URL host; EU single-tenant users to
// `<org>.sentry.io`.
func ResolveHost() string {
	if h := strings.TrimSpace(viper.GetString("sentry.host")); h != "" {
		return h
	}
	if h := strings.TrimSpace(os.Getenv("SENTRY_HOST")); h != "" {
		return h
	}
	return defaultHost
}

// NewClient returns a Client. orgSlug is optional — many endpoints scope by
// org but a handful (list orgs) don't. Empty host falls back to sentry.io.
func NewClient(authToken, orgSlug, host string, debug bool) (*Client, error) {
	if strings.TrimSpace(authToken) == "" {
		return nil, errors.New("sentry auth_token is required")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultHost
	}
	if err := validateHost(host); err != nil {
		return nil, err
	}
	return &Client{
		host:      host,
		authToken: authToken,
		orgSlug:   strings.TrimSpace(orgSlug),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		debug: debug,
	}, nil
}

// validHostRE accepts a hostname (no scheme, no path, no port). Sentry SaaS
// is sentry.io / *.sentry.io; self-hosted is whatever DNS name the operator
// runs Sentry on. Characters with hostname-injection potential (`:` `/` `@`
// `?` `#`) are rejected — those would be a clear SSRF attempt like
// `127.0.0.1:8080` or `evil.com/?@sentry.io`.
var validHostRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?)*$`)

// validateHost guards against SSRF via a hostile sentry.host config or
// SENTRY_HOST env. Without this, a user (or a process injecting env vars)
// could point the CLI at 169.254.169.254 to leak the Bearer token. We
// block IP literals, raw loopback / cloud-metadata names, and anything
// not shaped like a DNS hostname.
func validateHost(host string) error {
	if len(host) > 253 {
		return errors.New("sentry host too long")
	}
	if !validHostRE.MatchString(host) {
		return fmt.Errorf("sentry host contains invalid characters: %q", host)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("sentry host must be a DNS name, not an IP: %q", host)
	}
	lower := strings.ToLower(host)
	for _, bad := range []string{"localhost", "metadata.google.internal", "instance-data"} {
		if lower == bad || strings.HasSuffix(lower, "."+bad) {
			return fmt.Errorf("sentry host refers to an internal address: %q", host)
		}
	}
	return nil
}

// SetHTTPClient lets tests swap in an httptest.Server-backed client.
func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.httpClient = hc
	}
}

func (c *Client) Host() string    { return c.host }
func (c *Client) OrgSlug() string { return c.orgSlug }
func (c *Client) Debug() bool     { return c.debug }

// BaseURL returns the canonical base for Sentry REST calls. Always trailing
// `/api/0` with no slash; callers prepend `/...` paths.
func (c *Client) BaseURL() string {
	return fmt.Sprintf("https://%s%s", c.host, apiPathPrefix)
}

// APIError carries an HTTP-status-aware error from the upstream API.
type APIError struct {
	Status int
	Body   string
	Detail string
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("sentry api error %d: %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("sentry api error %d: %s", e.Status, e.Body)
}

// Do executes a Sentry API call with exponential backoff on 429s, honoring
// Retry-After and X-Sentry-Rate-Limit-Reset. path must begin with `/`. If
// body is non-nil it is JSON-marshalled.
func (c *Client) Do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	const maxAttempts = 4
	var lastResp *http.Response
	var lastBody []byte
	var lastErr error

	for attempt := range maxAttempts {
		resp, b, err := c.doOnce(ctx, method, path, body)
		if err != nil {
			lastErr = err
			// Network-level errors are retryable with backoff (DNS hiccups,
			// dropped connections). Hard auth errors come back through the
			// resp.StatusCode path below, not as `err`.
			if !isRetryableNetErr(err) || attempt == maxAttempts-1 {
				return nil, nil, err
			}
			sleepWithJitter(time.Duration(200*(attempt+1)) * time.Millisecond)
			continue
		}
		lastResp = resp
		lastBody = b

		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}

		// 429 — honor server-supplied wait if present. Retry-After is in
		// seconds; X-Sentry-Rate-Limit-Reset is an absolute unix-seconds
		// timestamp. Prefer the longer of the two so we don't hammer back
		// in immediately when both are advertised.
		wait := parseRetryWait(resp)
		if c.debug {
			fmt.Fprintf(os.Stderr, "[sentry] 429 rate-limited, waiting %s (attempt %d)\n", wait, attempt+1)
		}
		if attempt == maxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return resp, b, ctx.Err()
		case <-time.After(wait):
		}
	}

	if lastErr != nil {
		return nil, nil, lastErr
	}
	if lastResp.StatusCode >= 400 {
		return lastResp, lastBody, &APIError{
			Status: lastResp.StatusCode,
			Body:   string(lastBody),
			Detail: extractErrorDetail(lastBody),
		}
	}
	return lastResp, lastBody, nil
}

func (c *Client) doOnce(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	fullURL := path
	if !strings.HasPrefix(path, "http") {
		fullURL = c.BaseURL() + path
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.debug {
		fmt.Fprintf(os.Stderr, "[sentry] %s %s\n", method, fullURL)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("read body: %w", err)
	}
	return resp, respBody, nil
}

// DecodeJSON unmarshals body into v, returning a clearer error than the raw
// json package message when the response is e.g. an HTML login redirect.
func DecodeJSON(body []byte, v any) error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return fmt.Errorf("decode sentry response: %w (body: %s)", err, preview)
	}
	return nil
}

// ParseNextCursor returns the next-page cursor from the Link header, or empty
// string when there is no next page (results="false" or no rel=next entry).
// Sentry uses RFC 5988 with a `results` extension that flags whether a
// follow-up call would actually return rows.
var linkEntryRE = regexp.MustCompile(`<([^>]+)>; rel="([^"]+)"(?:; results="([^"]+)")?(?:; cursor="([^"]+)")?`)

func ParseNextCursor(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	link := resp.Header.Get("Link")
	if link == "" {
		return ""
	}
	for _, match := range linkEntryRE.FindAllStringSubmatch(link, -1) {
		if len(match) < 5 {
			continue
		}
		rel := match[2]
		results := match[3]
		cursor := match[4]
		if rel == "next" && results == "true" && cursor != "" {
			return cursor
		}
	}
	return ""
}

// BuildQuery formats a Sentry endpoint query string from a values bag,
// stripping empty values so we don't send `?query=`-style noise.
func BuildQuery(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	v := url.Values{}
	for key, val := range params {
		if strings.TrimSpace(val) == "" {
			continue
		}
		v.Set(key, val)
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func parseRetryWait(resp *http.Response) time.Duration {
	candidates := []time.Duration{}

	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.ParseFloat(h, 64); err == nil {
			candidates = append(candidates, time.Duration(secs*float64(time.Second)))
		}
	}
	if h := resp.Header.Get("X-Sentry-Rate-Limit-Reset"); h != "" {
		if reset, err := strconv.ParseInt(h, 10, 64); err == nil {
			wait := time.Until(time.Unix(reset, 0))
			if wait > 0 {
				candidates = append(candidates, wait)
			}
		}
	}

	max := 2 * time.Second
	for _, d := range candidates {
		if d > max {
			max = d
		}
	}
	// Cap the wait at 30s — beyond that the caller should see the failure
	// instead of hanging on what is almost certainly a misconfigured limit.
	if max > 30*time.Second {
		max = 30 * time.Second
	}
	return max
}

func sleepWithJitter(base time.Duration) {
	jitter := time.Duration(rand.Int63n(int64(base) / 2))
	time.Sleep(base + jitter)
}

func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "eof")
}

// extractErrorDetail tries to pull a `detail` field from the upstream JSON
// error envelope. Sentry returns either `{"detail":"..."}` or `{"<field>":["msg"]}`.
func extractErrorDetail(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var single struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &single); err == nil && single.Detail != "" {
		return single.Detail
	}
	var multi map[string]any
	if err := json.Unmarshal(body, &multi); err == nil {
		for k, v := range multi {
			switch vv := v.(type) {
			case string:
				return fmt.Sprintf("%s: %s", k, vv)
			case []any:
				if len(vv) > 0 {
					return fmt.Sprintf("%s: %v", k, vv[0])
				}
			}
		}
	}
	return ""
}
