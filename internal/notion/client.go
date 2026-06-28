package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/secrand"
	"github.com/spf13/viper"
)

const (
	baseURL       = "https://api.notion.com/v1"
	notionVersion = "2022-06-28"
	userAgent     = "clanker-cli"
)

// Client wraps Notion's REST API. Auth is an Internal Integration token
// passed as a standard Bearer header — Notion DOES use the Bearer prefix
// (unlike Linear, which is the inverse footgun).
//
// IMPORTANT — Notion tokens start out with ZERO access. The user must
// explicitly share each page/database with the integration via "..." →
// "Connections". Surface this in empty-state copy; agents should suggest
// sharing when search returns no results.
type Client struct {
	token             string
	defaultDatabaseID string
	httpClient        *http.Client
	debug             bool
}

func ResolveToken() string {
	if v := strings.TrimSpace(viper.GetString("notion.integration_token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("NOTION_API_KEY")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); v != "" {
		return v
	}
	return ""
}

func ResolveDefaultDatabaseID() string {
	if v := strings.TrimSpace(viper.GetString("notion.default_database_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("NOTION_DATABASE_ID")); v != "" {
		return v
	}
	return ""
}

func NewClient(token, defaultDatabaseID string, debug bool) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("notion integration_token is required")
	}
	return &Client{
		token:             token,
		defaultDatabaseID: strings.TrimSpace(defaultDatabaseID),
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

func (c *Client) DefaultDatabaseID() string { return c.defaultDatabaseID }
func (c *Client) Debug() bool               { return c.debug }

// APIError carries the HTTP status + Notion's `code` + `message`.
type APIError struct {
	Status  int
	Code    string
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("notion api error %d [%s]: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("notion api error %d: %s", e.Status, e.Body)
}

// IsAuthError reports whether err is an auth failure (401, 403, or
// Notion's `unauthorized` / `restricted_resource` error codes).
func IsAuthError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden {
		return true
	}
	return apiErr.Code == "unauthorized" || apiErr.Code == "restricted_resource"
}

// Do executes a Notion request with 429 backoff. method is GET/POST/PATCH;
// path is the path AFTER /v1 (e.g. "/search"); body is marshalled if
// non-nil. out is the response decoding target.
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	const maxAttempts = 4
	for attempt := range maxAttempts {
		resp, respBody, err := c.doOnce(ctx, method, path, body)
		if err != nil {
			if !isRetryableNetErr(err) || attempt == maxAttempts-1 {
				return err
			}
			sleepWithJitter(ctx, time.Duration(200<<attempt)*time.Millisecond)
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt == maxAttempts-1 {
				return parseAPIError(resp, respBody)
			}
			wait := parseRetryWait(resp)
			if c.debug {
				fmt.Fprintf(os.Stderr, "[notion] 429 rate-limited, waiting %s (attempt %d)\n", wait, attempt+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return parseAPIError(resp, respBody)
		}
		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			preview := string(respBody)
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			return fmt.Errorf("decode notion response: %w (body: %s)", err, preview)
		}
		return nil
	}
	return errors.New("notion api: exhausted retries")
}

func (c *Client) doOnce(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.debug {
		fmt.Fprintf(os.Stderr, "[notion] %s %s\n", method, path)
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

func parseAPIError(resp *http.Response, body []byte) error {
	var env struct {
		Object  string `json:"object"`
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &env)
	preview := string(body)
	if len(preview) > 512 {
		preview = preview[:512] + "..."
	}
	return &APIError{
		Status:  resp.StatusCode,
		Code:    env.Code,
		Message: env.Message,
		Body:    preview,
	}
}

func parseRetryWait(resp *http.Response) time.Duration {
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.ParseFloat(h, 64); err == nil && secs > 0 {
			return min(time.Duration(secs*float64(time.Second)), 30*time.Second)
		}
	}
	return 2 * time.Second
}

func sleepWithJitter(ctx context.Context, base time.Duration) {
	if base <= 0 {
		return
	}
	jitter := secrand.Duration(base/2 + 1)
	select {
	case <-ctx.Done():
	case <-time.After(base + jitter):
	}
}

// isRetryableNetErr matches the post-review behaviour from Linear:
// timeouts and connection-reset are transient; DNS / refused-connection
// are permanent for the request lifetime so we don't waste retries.
func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "eof")
}
