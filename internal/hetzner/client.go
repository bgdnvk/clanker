package hetzner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Client wraps the Hetzner Cloud hcloud CLI tool
type Client struct {
	apiToken string
	debug    bool
}

// ResolveAPIToken returns the Hetzner Cloud API token from config or environment
func ResolveAPIToken() string {
	if token := strings.TrimSpace(viper.GetString("hetzner.api_token")); token != "" {
		return token
	}
	if env := strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Hetzner Cloud client
func NewClient(apiToken string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("hetzner api_token is required")
	}

	return &Client{
		apiToken: apiToken,
		debug:    debug,
	}, nil
}

// BackendHetznerCredentials represents Hetzner credentials from the backend
type BackendHetznerCredentials struct {
	APIToken string
}

// NewClientWithCredentials creates a new Hetzner Cloud client using credentials from the backend
func NewClientWithCredentials(creds *BackendHetznerCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}

	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("hetzner api_token is required")
	}

	return &Client{
		apiToken: creds.APIToken,
		debug:    debug,
	}, nil
}

// GetAPIToken returns the API token
func (c *Client) GetAPIToken() string {
	return c.apiToken
}

// RunHcloud executes an hcloud CLI command with context
func (c *Client) RunHcloud(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("hcloud"); err != nil {
		return "", fmt.Errorf("hcloud not found in PATH (required for Hetzner operations, install from: https://github.com/hetznercloud/cli)")
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "hcloud", args...)
		cmd.Env = append(os.Environ(), "HCLOUD_TOKEN="+c.apiToken)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if c.debug {
			fmt.Printf("[hetzner] hcloud %s\n", strings.Join(args, " "))
		}

		err := cmd.Run()
		if err == nil {
			return stdout.String(), nil
		}

		lastErr = err
		lastStderr = strings.TrimSpace(stderr.String())

		if ctx.Err() != nil {
			break
		}

		if !isRetryableHetznerError(lastStderr) {
			break
		}

		time.Sleep(backoffs[attempt])
	}

	if lastErr == nil {
		return "", fmt.Errorf("hcloud command failed")
	}

	return "", fmt.Errorf("hcloud command failed: %w, stderr: %s%s", lastErr, lastStderr, hetznerErrorHint(lastStderr))
}

// GetRelevantContext gathers Hetzner Cloud context for LLM queries
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name string
		args []string
		keys []string
	}

	sections := []section{
		{name: "Servers", args: []string{"server", "list", "--output", "json"}, keys: []string{"server", "vm", "instance", "compute"}},
		{name: "Load Balancers", args: []string{"load-balancer", "list", "--output", "json"}, keys: []string{"load balancer", "lb"}},
		{name: "Volumes", args: []string{"volume", "list", "--output", "json"}, keys: []string{"volume", "block storage", "disk"}},
		{name: "Networks", args: []string{"network", "list", "--output", "json"}, keys: []string{"network", "subnet", "vpc"}},
		{name: "Firewalls", args: []string{"firewall", "list", "--output", "json"}, keys: []string{"firewall", "security"}},
		{name: "Floating IPs", args: []string{"floating-ip", "list", "--output", "json"}, keys: []string{"floating ip", "elastic ip", "public ip"}},
		{name: "Primary IPs", args: []string{"primary-ip", "list", "--output", "json"}, keys: []string{"primary ip", "ip address"}},
		{name: "SSH Keys", args: []string{"ssh-key", "list", "--output", "json"}, keys: []string{"ssh", "key", "ssh key"}},
		{name: "Images", args: []string{"image", "list", "--output", "json"}, keys: []string{"image", "snapshot", "backup"}},
		{name: "Certificates", args: []string{"certificate", "list", "--output", "json"}, keys: []string{"certificate", "ssl", "tls"}},
	}

	defaultSections := map[string]bool{
		"Servers": true,
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

		result, err := c.RunHcloud(ctx, s.args...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		if strings.TrimSpace(result) == "" {
			continue
		}
		out.WriteString(s.name)
		out.WriteString(":\n")
		out.WriteString(result)
		out.WriteString("\n")
	}

	if strings.TrimSpace(out.String()) == "" {
		for _, s := range sections {
			if !defaultSections[s.name] {
				continue
			}
			result, err := c.RunHcloud(ctx, s.args...)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", s.name, err))
				continue
			}
			if strings.TrimSpace(result) == "" {
				continue
			}
			out.WriteString(s.name)
			out.WriteString(":\n")
			out.WriteString(result)
			out.WriteString("\n")
		}
	}

	if len(warnings) > 0 {
		out.WriteString("Hetzner Warnings:\n")
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
		return "No Hetzner Cloud data available (missing permissions or no resources).", nil
	}

	return out.String(), nil
}

// isRetryableHetznerError checks if an error is retryable
func isRetryableHetznerError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "too many requests") || strings.Contains(lower, "429") {
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

// hetznerErrorHint returns helpful hints based on error messages
func hetznerErrorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "unable to authenticate"):
		return " (hint: check your HCLOUD_TOKEN is valid)"
	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission"):
		return " (hint: your API token may be missing required permissions)"
	case strings.Contains(lower, "not found") || strings.Contains(lower, "404"):
		return " (hint: resource not found or incorrect ID)"
	case strings.Contains(lower, "rate limit"):
		return " (hint: rate limited, try again in a few seconds)"
	default:
		return ""
	}
}
