package digitalocean

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

// Client wraps the Digital Ocean doctl CLI tool
type Client struct {
	apiToken string
	debug    bool
}

// ResolveAPIToken returns the Digital Ocean API token from config or environment
func ResolveAPIToken() string {
	if token := strings.TrimSpace(viper.GetString("digitalocean.api_token")); token != "" {
		return token
	}
	if env := strings.TrimSpace(os.Getenv("DO_API_TOKEN")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Digital Ocean client
func NewClient(apiToken string, debug bool) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("digitalocean api_token is required")
	}

	return &Client{
		apiToken: apiToken,
		debug:    debug,
	}, nil
}

// BackendDigitalOceanCredentials represents Digital Ocean credentials from the backend
type BackendDigitalOceanCredentials struct {
	APIToken string
}

// NewClientWithCredentials creates a new Digital Ocean client using credentials from the backend
func NewClientWithCredentials(creds *BackendDigitalOceanCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}

	if strings.TrimSpace(creds.APIToken) == "" {
		return nil, fmt.Errorf("digitalocean api_token is required")
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

// RunDoctl executes a doctl CLI command with context
func (c *Client) RunDoctl(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("doctl"); err != nil {
		return "", fmt.Errorf("doctl not found in PATH (required for Digital Ocean operations, install from: https://docs.digitalocean.com/reference/doctl/how-to/install/)")
	}

	// Inject access token
	fullArgs := append([]string{"--access-token", c.apiToken}, args...)

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "doctl", fullArgs...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if c.debug {
			fmt.Printf("[digitalocean] doctl %s\n", strings.Join(args, " "))
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

		if !isRetryableDOError(lastStderr) {
			break
		}

		time.Sleep(backoffs[attempt])
	}

	if lastErr == nil {
		return "", fmt.Errorf("doctl command failed")
	}

	return "", fmt.Errorf("doctl command failed: %w, stderr: %s%s", lastErr, lastStderr, doErrorHint(lastStderr))
}

// GetRelevantContext gathers Digital Ocean context for LLM queries
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name string
		args []string
		keys []string
	}

	sections := []section{
		{name: "Account", args: []string{"account", "get", "--output", "json"}, keys: nil},
		{name: "Droplets", args: []string{"compute", "droplet", "list", "--output", "json"}, keys: []string{"droplet", "vm", "server", "instance", "compute"}},
		{name: "Kubernetes Clusters", args: []string{"kubernetes", "cluster", "list", "--output", "json"}, keys: []string{"kubernetes", "k8s", "cluster", "doks"}},
		{name: "Databases", args: []string{"databases", "list", "--output", "json"}, keys: []string{"database", "db", "postgres", "mysql", "redis", "mongo"}},
		{name: "Spaces", args: []string{"compute", "cdn", "list", "--output", "json"}, keys: []string{"space", "spaces", "storage", "cdn", "object"}},
		{name: "Apps", args: []string{"apps", "list", "--output", "json"}, keys: []string{"app", "apps", "platform"}},
		{name: "Load Balancers", args: []string{"compute", "load-balancer", "list", "--output", "json"}, keys: []string{"load balancer", "lb"}},
		{name: "Volumes", args: []string{"compute", "volume", "list", "--output", "json"}, keys: []string{"volume", "block storage", "disk"}},
		{name: "VPCs", args: []string{"vpcs", "list", "--output", "json"}, keys: []string{"vpc", "network"}},
		{name: "Domains", args: []string{"compute", "domain", "list", "--output", "json"}, keys: []string{"domain", "dns"}},
		{name: "Firewalls", args: []string{"compute", "firewall", "list", "--output", "json"}, keys: []string{"firewall", "security"}},
	}

	defaultSections := map[string]bool{
		"Account":  true,
		"Droplets": true,
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

		result, err := c.RunDoctl(ctx, s.args...)
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
			result, err := c.RunDoctl(ctx, s.args...)
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
		out.WriteString("Digital Ocean Warnings:\n")
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
		return "No Digital Ocean data available (missing permissions or no resources).", nil
	}

	return out.String(), nil
}

// isRetryableDOError checks if an error is retryable
func isRetryableDOError(stderr string) bool {
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

// doErrorHint returns helpful hints based on error messages
func doErrorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "unable to authenticate"):
		return " (hint: check your DO_API_TOKEN or DIGITALOCEAN_ACCESS_TOKEN is valid)"
	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission"):
		return " (hint: your API token may be missing required scopes)"
	case strings.Contains(lower, "not found") || strings.Contains(lower, "404"):
		return " (hint: resource not found or incorrect ID)"
	case strings.Contains(lower, "rate limit"):
		return " (hint: rate limited, try again in a few seconds)"
	default:
		return ""
	}
}
