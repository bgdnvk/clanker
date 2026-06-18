package digitalocean

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

// Client wraps the Digital Ocean doctl CLI tool
type Client struct {
	apiToken string
	debug    bool
}

// CLIInstalled reports whether doctl is available on PATH.
func CLIInstalled() bool {
	_, err := exec.LookPath("doctl")
	return err == nil
}

// CLIAuthenticated reports whether the current shell already has a working
// doctl auth context. This lets read-only inventory code reuse ambient CLI
// auth instead of requiring the token to be copied into config or env again.
func CLIAuthenticated(ctx context.Context) bool {
	if !CLIInstalled() {
		return false
	}
	probeCtx := ctx
	if probeCtx == nil {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(probeCtx, "doctl", "account", "get", "--output", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(stdout.String()) != ""
}

// CanUseLiveContext reports whether DigitalOcean live queries can run either
// with an explicit API token or an authenticated doctl context.
func CanUseLiveContext(ctx context.Context) bool {
	if strings.TrimSpace(ResolveAPIToken()) != "" {
		return true
	}
	return CLIAuthenticated(ctx)
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
	if strings.TrimSpace(apiToken) == "" && !CLIInstalled() {
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

	fullArgs := normalizeDoctlOutputArgs(args)
	if strings.TrimSpace(c.apiToken) != "" {
		fullArgs = append([]string{"--access-token", c.apiToken}, fullArgs...)
	}

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

func normalizeDoctlOutputArgs(args []string) []string {
	normalized := append([]string{}, args...)
	for i := 0; i < len(normalized)-1; i++ {
		if normalized[i] == "--format" && strings.EqualFold(strings.TrimSpace(normalized[i+1]), "json") {
			normalized[i] = "--output"
		}
	}
	return normalized
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
		{name: "Account", args: []string{"account", "get", "--format", "json"}, keys: nil},
		{name: "Recent Actions", args: []string{"compute", "action", "list", "--format", "json"}, keys: []string{"action", "actions", "activity"}},
		{name: "Droplets", args: []string{"compute", "droplet", "list", "--format", "json"}, keys: []string{"droplet", "vm", "server", "instance", "compute"}},
		{name: "Droplet Autoscale Pools", args: []string{"compute", "droplet-autoscale", "list", "--format", "json"}, keys: []string{"autoscale", "autoscaling", "droplet autoscale"}},
		{name: "Kubernetes Clusters", args: []string{"kubernetes", "cluster", "list", "--format", "json"}, keys: []string{"kubernetes", "k8s", "cluster", "doks"}},
		{name: "Databases", args: []string{"databases", "list", "--format", "json"}, keys: []string{"database", "db", "postgres", "mysql", "redis", "mongo"}},
		{name: "Spaces", args: []string{"compute", "cdn", "list", "--format", "json"}, keys: []string{"space", "spaces", "storage", "cdn", "object"}},
		{name: "Spaces Keys", args: []string{"spaces", "keys", "list", "--format", "json"}, keys: []string{"spaces key", "spaces keys", "object storage key"}},
		{name: "Apps", args: []string{"apps", "list", "--format", "json"}, keys: []string{"app", "apps", "platform"}},
		{name: "Functions", args: []string{"serverless", "functions", "list", "--format", "json"}, keys: []string{"function", "functions", "serverless"}},
		{name: "Function Namespaces", args: []string{"serverless", "namespaces", "list", "--format", "json"}, keys: []string{"function namespace", "function namespaces", "serverless namespace"}},
		{name: "Serverless Inference Models", args: []string{"serverless-inference", "models", "list", "--format", "json"}, keys: []string{"serverless inference", "inference model", "inference models"}},
		{name: "Dedicated Inference Endpoints", args: []string{"dedicated-inference", "list", "--format", "json"}, keys: []string{"dedicated inference", "gpu inference", "inference endpoint"}},
		{name: "Container Registries", args: []string{"registries", "list", "--format", "json"}, keys: []string{"registry", "registries", "container registry", "container registries"}},
		{name: "Projects", args: []string{"projects", "list", "--format", "json"}, keys: []string{"project", "projects"}},
		{name: "Gradient AI Models", args: []string{"gradient", "list-models", "--format", "json"}, keys: []string{"gradient", "model", "models", "ai model", "ai models"}},
		{name: "Gradient AI Regions", args: []string{"gradient", "list-regions", "--format", "json"}, keys: []string{"gradient region", "gradient regions", "ai region"}},
		{name: "Gradient AI Agents", args: []string{"gradient", "agent", "list", "--format", "json"}, keys: []string{"gradient agent", "gradient agents", "ai agent", "ai agents"}},
		{name: "Gradient Knowledge Bases", args: []string{"gradient", "knowledge-base", "list", "--format", "json"}, keys: []string{"gradient knowledge", "knowledge base", "knowledge bases", "rag"}},
		{name: "Gradient OpenAI API Keys", args: []string{"gradient", "openai-key", "list", "--format", "json"}, keys: []string{"gradient openai", "openai key", "openai keys"}},
		{name: "Load Balancers", args: []string{"compute", "load-balancer", "list", "--format", "json"}, keys: []string{"load balancer", "lb"}},
		{name: "CDN Endpoints", args: []string{"compute", "cdn", "list", "--format", "json"}, keys: []string{"cdn", "edge cache"}},
		{name: "Volumes", args: []string{"compute", "volume", "list", "--format", "json"}, keys: []string{"volume", "block storage", "disk"}},
		{name: "NFS Shares", args: []string{"nfs", "list", "--format", "json"}, keys: []string{"nfs", "network file", "file storage"}},
		{name: "VPCs", args: []string{"vpcs", "list", "--format", "json"}, keys: []string{"vpc", "network"}},
		{name: "VPC Peerings", args: []string{"vpcs", "peerings", "list", "--format", "json"}, keys: []string{"vpc peering", "peering"}},
		{name: "VPC NAT Gateways", args: []string{"compute", "vpc-nat-gateway", "list", "--format", "json"}, keys: []string{"nat gateway", "nat gateways"}},
		{name: "Domains", args: []string{"compute", "domain", "list", "--format", "json"}, keys: []string{"domain", "dns"}},
		{name: "Firewalls", args: []string{"compute", "firewall", "list", "--format", "json"}, keys: []string{"firewall", "security"}},
		{name: "Reserved IPs", args: []string{"compute", "reserved-ip", "list", "--format", "json"}, keys: []string{"reserved ip", "floating ip", "public ip"}},
		{name: "Reserved IPv6", args: []string{"compute", "reserved-ipv6", "list", "--format", "json"}, keys: []string{"reserved ipv6", "ipv6"}},
		{name: "Certificates", args: []string{"compute", "certificate", "list", "--format", "json"}, keys: []string{"certificate", "ssl", "tls"}},
		{name: "Images", args: []string{"compute", "image", "list", "--format", "json"}, keys: []string{"image", "images", "snapshot"}},
		{name: "Snapshots", args: []string{"compute", "snapshot", "list", "--format", "json"}, keys: []string{"snapshot", "snapshots", "backup"}},
		{name: "SSH Keys", args: []string{"compute", "ssh-key", "list", "--format", "json"}, keys: []string{"ssh key", "ssh keys"}},
		{name: "Tags", args: []string{"compute", "tag", "list", "--format", "json"}, keys: []string{"tag", "tags"}},
		{name: "Monitoring Alerts", args: []string{"monitoring", "alert", "list", "--format", "json"}, keys: []string{"monitoring alert", "alerts"}},
		{name: "Uptime Checks", args: []string{"monitoring", "uptime", "list", "--format", "json"}, keys: []string{"uptime", "uptime check", "uptime checks"}},
		{name: "Security Scans", args: []string{"security", "scans", "list", "--format", "json"}, keys: []string{"security scan", "cspm", "scan"}},
	}

	defaultSections := map[string]bool{
		"Account":  true,
		"Droplets": true,
	}

	var out strings.Builder
	var warnings []string
	var appsBody string

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
		if s.name == "Apps" {
			appsBody = result
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

	if digitalOceanObservabilityLogIntent(questionLower) {
		appLogs, err := c.collectAppPlatformLogsContext(ctx, appsBody)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("App Platform Logs: %v", err))
		} else if strings.TrimSpace(appLogs) != "" {
			out.WriteString("App Platform Logs:\n")
			out.WriteString(appLogs)
			if !strings.HasSuffix(appLogs, "\n") {
				out.WriteString("\n")
			}
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

func digitalOceanObservabilityLogIntent(questionLower string) bool {
	return containsAnyDigitalOceanPhrase(questionLower,
		"log", "logs", "event", "events", "error", "errors", "warning", "warnings",
		"trace", "traces", "metric", "metrics", "observability", "incident", "incidents")
}

func containsAnyDigitalOceanPhrase(value string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

type appPlatformSummary struct {
	ID   string
	Name string
}

func (c *Client) collectAppPlatformLogsContext(ctx context.Context, appsBody string) (string, error) {
	apps, err := parseDigitalOceanApps(appsBody)
	if err != nil || len(apps) == 0 {
		fetched, fetchErr := c.RunDoctl(ctx, "apps", "list", "--format", "json")
		if fetchErr != nil {
			if err != nil {
				return "", fmt.Errorf("%v; list apps: %w", err, fetchErr)
			}
			return "", fetchErr
		}
		apps, err = parseDigitalOceanApps(fetched)
		if err != nil {
			return "", err
		}
	}
	if len(apps) == 0 {
		return "No DigitalOcean App Platform apps found.\n", nil
	}

	maxApps := minDigitalOceanInt(len(apps), 3)
	var out strings.Builder
	for i := 0; i < maxApps; i++ {
		app := apps[i]
		if strings.TrimSpace(app.ID) == "" {
			continue
		}
		name := strings.TrimSpace(app.Name)
		if name == "" {
			name = app.ID
		}
		out.WriteString(fmt.Sprintf("%s (%s):\n", name, app.ID))
		deployments, depErr := c.RunDoctl(ctx, "apps", "list-deployments", app.ID, "--format", "json")
		if depErr != nil {
			out.WriteString(fmt.Sprintf("  deployments unavailable: %v\n", depErr))
		} else if strings.TrimSpace(deployments) != "" {
			out.WriteString("  deployments:\n")
			out.WriteString(indentDigitalOceanLines(truncateDigitalOceanContext(strings.TrimSpace(deployments), 1200), "    "))
		}
		logs, logErr := c.RunDoctl(ctx, "apps", "logs", app.ID, "--type", "run", "--tail", "100", "--no-prefix")
		if logErr != nil {
			out.WriteString(fmt.Sprintf("  run logs unavailable: %v\n", logErr))
			continue
		}
		out.WriteString("  run logs:\n")
		out.WriteString(indentDigitalOceanLines(truncateDigitalOceanContext(strings.TrimSpace(logs), 2500), "    "))
	}
	if len(apps) > maxApps {
		out.WriteString(fmt.Sprintf("(... %d more apps omitted)\n", len(apps)-maxApps))
	}
	return out.String(), nil
}

func parseDigitalOceanApps(body string) ([]appPlatformSummary, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil, nil
	}
	var rawApps []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rawApps); err != nil {
		var envelope struct {
			Apps []map[string]any `json:"apps"`
		}
		if err2 := json.Unmarshal([]byte(trimmed), &envelope); err2 != nil {
			return nil, err
		}
		rawApps = envelope.Apps
	}
	apps := make([]appPlatformSummary, 0, len(rawApps))
	for _, raw := range rawApps {
		id := stringFromAny(raw["id"])
		if id == "" {
			id = stringFromAny(raw["ID"])
		}
		name := stringFromAny(raw["name"])
		if spec, ok := raw["spec"].(map[string]any); ok {
			if specName := stringFromAny(spec["name"]); specName != "" {
				name = specName
			}
		}
		if id == "" {
			continue
		}
		apps = append(apps, appPlatformSummary{ID: id, Name: name})
	}
	return apps, nil
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func truncateDigitalOceanContext(value string, max int) string {
	if strings.TrimSpace(value) == "" {
		return "(no output)"
	}
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "...<truncated>"
}

func indentDigitalOceanLines(value string, prefix string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n") + "\n"
}

func minDigitalOceanInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
