package railway

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

// Client wraps the Railway CLI for account and linked-project queries.
type Client struct {
	apiToken string
	debug    bool
}

// ResolveAPIToken returns a Railway token from config or environment.
func ResolveAPIToken() string {
	if token := strings.TrimSpace(viper.GetString("railway.api_token")); token != "" {
		return token
	}
	if token := strings.TrimSpace(viper.GetString("railway.token")); token != "" {
		return token
	}
	if env := strings.TrimSpace(os.Getenv("RAILWAY_API_TOKEN")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("RAILWAY_TOKEN")); env != "" {
		return env
	}
	return ""
}

// NewClient creates a new Railway client.
// Token is optional because the Railway CLI may already be authenticated locally.
func NewClient(apiToken string, debug bool) (*Client, error) {
	return &Client{apiToken: strings.TrimSpace(apiToken), debug: debug}, nil
}

// RunRailway executes a Railway CLI command.
func (c *Client) RunRailway(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("railway"); err != nil {
		return "", fmt.Errorf("railway CLI not found in PATH (install from: https://docs.railway.com/cli)")
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "railway", args...)
		cmd.Env = os.Environ()
		if c.apiToken != "" {
			cmd.Env = append(cmd.Env, "RAILWAY_API_TOKEN="+c.apiToken)
		}

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if c.debug {
			fmt.Printf("[railway] railway %s\n", strings.Join(args, " "))
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
		if !isRetryableRailwayError(lastStderr) {
			break
		}

		time.Sleep(backoffs[attempt])
	}

	if lastErr == nil {
		return "", fmt.Errorf("railway command failed")
	}

	return "", fmt.Errorf("railway command failed: %w, stderr: %s%s", lastErr, lastStderr, railwayErrorHint(lastStderr))
}

// GetRelevantContext gathers Railway context for LLM queries.
func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name string
		args []string
		keys []string
	}

	sections := []section{
		{name: "Account", args: []string{"whoami", "--json"}, keys: nil},
		{name: "Projects", args: []string{"list", "--json"}, keys: []string{"project", "projects", "workspace", "account", "list"}},
		{name: "Linked Project Status", args: []string{"status", "--json"}, keys: []string{"status", "service", "deployment", "deploy", "environment", "domain", "volume", "function", "railway"}},
	}

	defaultSections := map[string]bool{
		"Account":               true,
		"Linked Project Status": true,
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

		result, err := c.RunRailway(ctx, s.args...)
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

	if len(warnings) > 0 {
		out.WriteString("Railway Warnings:\n")
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
		return "No Railway data available (missing login/token, no linked project, or no accessible resources).", nil
	}

	return out.String(), nil
}

func isRetryableRailwayError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "429") {
		return true
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") {
		return true
	}
	if strings.Contains(lower, "temporarily unavailable") || strings.Contains(lower, "internal error") {
		return true
	}
	if strings.Contains(lower, "connection reset") || strings.Contains(lower, "connection refused") {
		return true
	}
	return false
}

func railwayErrorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "not logged in") || strings.Contains(lower, "login"):
		return " (hint: run 'railway login --browserless' or set RAILWAY_API_TOKEN)"
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication"):
		return " (hint: check your RAILWAY_API_TOKEN or RAILWAY_TOKEN is valid)"
	case strings.Contains(lower, "linked") || strings.Contains(lower, "project"):
		return " (hint: run 'railway link' in the project directory or use a workspace where Railway is linked)"
	default:
		return ""
	}
}
