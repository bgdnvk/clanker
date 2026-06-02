package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	apiEndpoint = "https://api.linear.app/graphql"
	userAgent   = "clanker-cli"
)

// Client is a thin GraphQL wrapper around the Linear API.
//
// Auth is a Personal API Key from Settings → API → Personal API keys.
// IMPORTANT: Linear's auth header is `Authorization: <key>` — there is
// NO `Bearer ` prefix. This is the #1 Linear footgun; sending Bearer
// returns a 400 with a confusing error.
type Client struct {
	apiKey      string
	workspaceID string
	defaultTeam string
	httpClient  *http.Client
	debug       bool
}

func ResolveAPIKey() string {
	if v := strings.TrimSpace(viper.GetString("linear.api_key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("LINEAR_API_KEY")); v != "" {
		return v
	}
	return ""
}

func ResolveWorkspaceID() string {
	if v := strings.TrimSpace(viper.GetString("linear.workspace_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("LINEAR_WORKSPACE_ID")); v != "" {
		return v
	}
	return ""
}

func ResolveDefaultTeam() string {
	if v := strings.TrimSpace(viper.GetString("linear.default_team")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("LINEAR_TEAM")); v != "" {
		return v
	}
	return ""
}

// NewClient returns a Client. apiKey is required; workspaceID and team can
// be empty (callers either pass them via flags or set defaults later).
func NewClient(apiKey, workspaceID, defaultTeam string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("linear api_key is required")
	}
	return &Client{
		apiKey:      apiKey,
		workspaceID: strings.TrimSpace(workspaceID),
		defaultTeam: strings.TrimSpace(defaultTeam),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		debug: debug,
	}, nil
}

func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.httpClient = hc
	}
}

func (c *Client) WorkspaceID() string { return c.workspaceID }
func (c *Client) DefaultTeam() string { return c.defaultTeam }
func (c *Client) Debug() bool         { return c.debug }

// GraphQLError is one element of the GraphQL `errors` envelope.
type GraphQLError struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// APIError carries the HTTP status plus the first GraphQL error message.
type APIError struct {
	Status int
	Body   string
	Errors []GraphQLError
}

func (e *APIError) Error() string {
	if len(e.Errors) > 0 {
		return fmt.Sprintf("linear api error %d: %s", e.Status, e.Errors[0].Message)
	}
	return fmt.Sprintf("linear api error %d: %s", e.Status, e.Body)
}

// IsAuthError reports whether err is a 401/403 from Linear (or a 400 with
// a `AUTHENTICATION_ERROR` extension — Linear's most common shape).
func IsAuthError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden {
		return true
	}
	for _, e := range apiErr.Errors {
		if ext, ok := e.Extensions["code"].(string); ok {
			if ext == "AUTHENTICATION_ERROR" || ext == "FORBIDDEN" {
				return true
			}
		}
	}
	return false
}

// Do issues a GraphQL POST. variables may be nil. Decodes the `data` field
// into `out` (a pointer to a struct shaped like the query's selection set).
// 429s are retried with backoff that honors Retry-After when present.
func (c *Client) Do(ctx context.Context, query string, variables map[string]any, out any) error {
	const maxAttempts = 4
	for attempt := range maxAttempts {
		resp, body, err := c.doOnce(ctx, query, variables)
		if err != nil {
			if !isRetryableNetErr(err) || attempt == maxAttempts-1 {
				return err
			}
			sleepWithJitter(ctx, time.Duration(200*(attempt+1))*time.Millisecond)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt == maxAttempts-1 {
				return parseAPIError(resp, body)
			}
			wait := parseRetryWait(resp)
			if c.debug {
				fmt.Fprintf(os.Stderr, "[linear] 429 rate-limited, waiting %s (attempt %d)\n", wait, attempt+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode >= 400 {
			return parseAPIError(resp, body)
		}

		// GraphQL can return 200 + `errors` field. Always check.
		return decodeGraphQLResponse(body, out, resp.StatusCode)
	}
	return errors.New("linear api: exhausted retries")
}

func (c *Client) doOnce(ctx context.Context, query string, variables map[string]any) (*http.Response, []byte, error) {
	payload := map[string]any{"query": query}
	if len(variables) > 0 {
		payload["variables"] = variables
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiEndpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	// IMPORTANT: no Bearer prefix. Linear documents `Authorization: <key>`.
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	if c.debug {
		// Don't echo the API key. The query body is fine for debugging.
		fmt.Fprintf(os.Stderr, "[linear] POST %s len=%d\n", apiEndpoint, len(raw))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("read body: %w", err)
	}
	return resp, body, nil
}

// decodeGraphQLResponse handles the {data, errors} envelope. If errors is
// non-empty we surface them as APIError; otherwise we unmarshal `data` into
// out.
func decodeGraphQLResponse(body []byte, out any, status int) error {
	if out == nil {
		out = new(json.RawMessage)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []GraphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		preview := string(body)
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		return fmt.Errorf("decode linear response: %w (body: %s)", err, preview)
	}
	if len(env.Errors) > 0 {
		return &APIError{Status: status, Body: string(body), Errors: env.Errors}
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode linear data: %w", err)
	}
	return nil
}

func parseAPIError(resp *http.Response, body []byte) error {
	var env struct {
		Errors []GraphQLError `json:"errors"`
	}
	_ = json.Unmarshal(body, &env)
	return &APIError{Status: resp.StatusCode, Body: string(body), Errors: env.Errors}
}

func parseRetryWait(resp *http.Response) time.Duration {
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.ParseFloat(h, 64); err == nil && secs > 0 {
			d := time.Duration(secs * float64(time.Second))
			return min(d, 30*time.Second)
		}
	}
	return 2 * time.Second
}

func sleepWithJitter(ctx context.Context, base time.Duration) {
	if base <= 0 {
		return
	}
	jitter := time.Duration(rand.Int63n(int64(base)/2 + 1))
	select {
	case <-ctx.Done():
	case <-time.After(base + jitter):
	}
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
