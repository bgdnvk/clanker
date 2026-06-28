package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const defaultProfile = "DEFAULT"

// Client wraps the official Oracle Cloud Infrastructure CLI.
type Client struct {
	profile       string
	compartmentID string
	tenancyOCID   string
	debug         bool
}

type ConfigProfile struct {
	Name        string
	TenancyOCID string
	UserOCID    string
	Fingerprint string
	KeyFile     string
	Region      string
}

func CLIInstalled() bool {
	_, err := exec.LookPath("oci")
	return err == nil
}

func ResolveProfile() string {
	if profile := strings.TrimSpace(viper.GetString("oracle.profile")); profile != "" {
		return profile
	}
	if profile := strings.TrimSpace(os.Getenv("OCI_CLI_PROFILE")); profile != "" {
		return profile
	}
	return defaultProfile
}

func ResolveCompartmentID() string {
	for _, value := range []string{
		viper.GetString("oracle.compartment_id"),
		os.Getenv("OCI_COMPARTMENT_ID"),
		os.Getenv("OCI_TENANCY_OCID"),
		os.Getenv("OCI_TENANCY_ID"),
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ResolveTenancyOCID(profile string) string {
	for _, value := range []string{
		viper.GetString("oracle.tenancy_ocid"),
		viper.GetString("oracle.tenancy_id"),
		os.Getenv("OCI_TENANCY_OCID"),
		os.Getenv("OCI_TENANCY_ID"),
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if cfg, ok := ReadConfigProfile(profile); ok {
		return cfg.TenancyOCID
	}
	return ""
}

func NewClient(profile, compartmentID, tenancyOCID string, debug bool) (*Client, error) {
	if !CLIInstalled() {
		return nil, fmt.Errorf("oci CLI not found in PATH (install from https://docs.oracle.com/en-us/iaas/Content/API/SDKDocs/cliinstall.htm)")
	}
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = ResolveProfile()
	}
	compartmentID = strings.TrimSpace(compartmentID)
	if compartmentID == "" {
		compartmentID = ResolveCompartmentID()
	}
	tenancyOCID = strings.TrimSpace(tenancyOCID)
	if tenancyOCID == "" {
		tenancyOCID = ResolveTenancyOCID(profile)
	}
	if compartmentID == "" {
		compartmentID = tenancyOCID
	}
	return &Client{
		profile:       profile,
		compartmentID: compartmentID,
		tenancyOCID:   tenancyOCID,
		debug:         debug,
	}, nil
}

func (c *Client) Profile() string {
	return c.profile
}

func (c *Client) CompartmentID() string {
	return c.compartmentID
}

func (c *Client) TenancyOCID() string {
	return c.tenancyOCID
}

func (c *Client) RunOCI(ctx context.Context, args ...string) (string, error) {
	fullArgs := []string{}
	if strings.TrimSpace(c.profile) != "" {
		fullArgs = append(fullArgs, "--profile", c.profile)
	}
	fullArgs = append(fullArgs, args...)
	if !hasFlag(fullArgs, "--output") {
		fullArgs = append(fullArgs, "--output", "json")
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string
	for attempt := 0; attempt < len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "oci", fullArgs...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if c.debug {
			fmt.Printf("[oracle] oci %s\n", strings.Join(args, " "))
		}
		err := cmd.Run()
		if err == nil {
			return stdout.String(), nil
		}
		lastErr = err
		lastStderr = strings.TrimSpace(stderr.String())
		if ctx.Err() != nil || !isRetryableOCIError(lastStderr) {
			break
		}
		time.Sleep(backoffs[attempt])
	}
	if lastErr == nil {
		return "", fmt.Errorf("oci command failed")
	}
	return "", fmt.Errorf("oci command failed: %w, stderr: %s%s", lastErr, lastStderr, ociErrorHint(lastStderr))
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))
	resources := resourcesForQuestion(questionLower)
	if len(resources) == 0 {
		resources = []string{"compartments", "instances", "vcns", "subnets", "load-balancers", "buckets", "oke-clusters", "databases"}
	}

	var out strings.Builder
	var warnings []string
	for _, resource := range resources {
		result, err := c.ListResource(ctx, resource)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", resource, err))
			continue
		}
		if len(result.Data) == 0 && strings.TrimSpace(result.Raw) == "" {
			continue
		}
		out.WriteString(result.Title)
		out.WriteString(":\n")
		if len(result.Data) > 0 {
			body, _ := json.MarshalIndent(map[string]any{"data": result.Data}, "", "  ")
			out.Write(body)
		} else {
			out.WriteString(result.Raw)
		}
		out.WriteString("\n")
	}
	if len(warnings) > 0 {
		out.WriteString("Oracle Cloud Warnings:\n")
		for i, warn := range warnings {
			if i >= 8 {
				out.WriteString("- (additional warnings omitted)\n")
				break
			}
			out.WriteString("- ")
			out.WriteString(warn)
			out.WriteString("\n")
		}
	}
	if strings.TrimSpace(out.String()) == "" {
		return "No Oracle Cloud Infrastructure data available (missing OCI CLI profile, permissions, compartments, or resources).", nil
	}
	return out.String(), nil
}

func resourcesForQuestion(questionLower string) []string {
	if questionLower == "" {
		return nil
	}
	var resources []string
	for _, def := range resourceDefinitions {
		for _, key := range def.Keys {
			if strings.Contains(questionLower, key) {
				resources = append(resources, def.Name)
				break
			}
		}
	}
	return uniqueSorted(resources)
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), flag) {
			return true
		}
	}
	return false
}

func isRetryableOCIError(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests") ||
		(strings.Contains(lower, "rate") && strings.Contains(lower, "limit")) ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "temporarily unavailable") ||
		strings.Contains(lower, "internal server error") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection refused")
}

func ociErrorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "not authenticated") || strings.Contains(lower, "auth") || strings.Contains(lower, "401"):
		return " (hint: run `oci setup config`, check OCI_CLI_PROFILE, and verify the API signing key/fingerprint)"
	case strings.Contains(lower, "notauthorizedornotfound") || strings.Contains(lower, "not authorized") || strings.Contains(lower, "403"):
		return " (hint: the OCI principal needs inspect/read permissions for the target compartment)"
	case strings.Contains(lower, "compartment"):
		return " (hint: pass --compartment-id or configure oracle.compartment_id / OCI_COMPARTMENT_ID)"
	case strings.Contains(lower, "config") || strings.Contains(lower, ".oci"):
		return " (hint: create ~/.oci/config with `oci setup config` or set OCI_CLI_CONFIG_FILE)"
	default:
		return ""
	}
}

func configPath() string {
	if path := strings.TrimSpace(os.Getenv("OCI_CLI_CONFIG_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".oci", "config")
}

func ReadConfigProfile(profile string) (ConfigProfile, bool) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = defaultProfile
	}
	path := configPath()
	if path == "" {
		return ConfigProfile{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigProfile{}, false
	}
	current := ""
	values := map[string]map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			current = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			if current != "" && values[current] == nil {
				values[current] = map[string]string{}
			}
			continue
		}
		if current == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		values[current][key] = value
	}
	v, ok := values[profile]
	if !ok {
		return ConfigProfile{}, false
	}
	return ConfigProfile{
		Name:        profile,
		TenancyOCID: v["tenancy"],
		UserOCID:    v["user"],
		Fingerprint: v["fingerprint"],
		KeyFile:     v["key_file"],
		Region:      v["region"],
	}, true
}

func ConfigProfiles() []ConfigProfile {
	path := configPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	names := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	profiles := make([]ConfigProfile, 0, len(names))
	for _, name := range names {
		if cfg, ok := ReadConfigProfile(name); ok {
			profiles = append(profiles, cfg)
		}
	}
	return profiles
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
