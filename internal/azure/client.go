package azure

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

type Client struct {
	subscriptionID string
	debug          bool
}

func ResolveSubscriptionID() string {
	if sub := strings.TrimSpace(viper.GetString("infra.azure.subscription_id")); sub != "" {
		return sub
	}
	if env := strings.TrimSpace(os.Getenv("AZURE_SUBSCRIPTION_ID")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("AZ_SUBSCRIPTION_ID")); env != "" {
		return env
	}
	return ""
}

func NewClient(subscriptionID string, debug bool) (*Client, error) {
	if strings.TrimSpace(subscriptionID) == "" {
		return nil, fmt.Errorf("azure subscription_id is required")
	}
	return &Client{subscriptionID: strings.TrimSpace(subscriptionID), debug: debug}, nil
}

// BackendAzureCredentials represents Azure credentials from the backend
type BackendAzureCredentials struct {
	SubscriptionID string
	TenantID       string
	ClientID       string
	ClientSecret   string
}

// NewClientWithCredentials creates a new Azure client using credentials from the backend.
// If service principal credentials are provided (TenantID, ClientID, ClientSecret),
// it performs az login with the service principal.
func NewClientWithCredentials(creds *BackendAzureCredentials, debug bool) (*Client, error) {
	if creds == nil {
		return nil, fmt.Errorf("credentials cannot be nil")
	}

	if strings.TrimSpace(creds.SubscriptionID) == "" {
		return nil, fmt.Errorf("azure subscription_id is required")
	}

	// If service principal credentials are provided, login with them
	if creds.TenantID != "" && creds.ClientID != "" && creds.ClientSecret != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		args := []string{
			"login",
			"--service-principal",
			"--username", creds.ClientID,
			"--password", creds.ClientSecret,
			"--tenant", creds.TenantID,
			"--output", "none",
		}

		cmd := exec.CommandContext(ctx, "az", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if debug {
			fmt.Printf("[azure] logging in with service principal (client_id: %s)\n", creds.ClientID)
		}

		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("az login with service principal failed: %w, stderr: %s", err, stderr.String())
		}
	}

	return &Client{subscriptionID: strings.TrimSpace(creds.SubscriptionID), debug: debug}, nil
}

func (c *Client) execAz(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("az"); err != nil {
		return "", fmt.Errorf("az not found in PATH")
	}

	if c.subscriptionID != "" && !hasFlag(args, "--subscription") {
		args = append(args, "--subscription", c.subscriptionID)
	}
	if !hasFlag(args, "--only-show-errors") {
		args = append(args, "--only-show-errors")
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt < len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "az", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			return stdout.String(), nil
		}

		lastErr = err
		lastStderr = strings.TrimSpace(stderr.String())

		if ctx.Err() != nil {
			break
		}
		if !isRetryableAzError(lastStderr) {
			break
		}
		time.Sleep(backoffs[attempt])
	}

	if lastErr == nil {
		return "", fmt.Errorf("az command failed")
	}
	return "", fmt.Errorf("az command failed: %w, stderr: %s%s", lastErr, lastStderr, azErrorHint(lastStderr))
}

func isRetryableAzError(stderr string) bool {
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
	return false
}

func azErrorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "login") || strings.Contains(lower, "az login") || strings.Contains(lower, "not logged"):
		return " (hint: run az login)"
	case strings.Contains(lower, "insufficient") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission"):
		return " (hint: missing RBAC permissions on the subscription/resource group)"
	case strings.Contains(lower, "subscription") && strings.Contains(lower, "not found"):
		return " (hint: subscription id may be incorrect)"
	default:
		return ""
	}
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	questionLower := strings.ToLower(strings.TrimSpace(question))

	type section struct {
		name string
		args []string
		keys []string
	}

	sections := []section{
		{name: "Azure Account", args: []string{"account", "show", "--output", "json"}, keys: nil},
		{name: "Resource Groups", args: []string{"group", "list", "--output", "table"}, keys: []string{"resource group", "resource groups", "rg"}},
		{name: "Virtual Machines", args: []string{"vm", "list", "-d", "--output", "table"}, keys: []string{"vm", "vms", "virtual machine", "virtual machines"}},
		{name: "AKS Clusters", args: []string{"aks", "list", "--output", "table"}, keys: []string{"aks", "kubernetes", "k8s"}},
		{name: "App Services", args: []string{"webapp", "list", "--output", "table"}, keys: []string{"app service", "webapp", "web app", "appservice"}},
		{name: "Function Apps", args: []string{"functionapp", "list", "--output", "table"}, keys: []string{"function", "function app", "functionapp"}},
		{name: "Storage Accounts", args: []string{"storage", "account", "list", "--output", "table"}, keys: []string{"storage", "storage account", "blob"}},
		{name: "Key Vaults", args: []string{"keyvault", "list", "--output", "table"}, keys: []string{"key vault", "keyvault", "vault"}},
		{name: "Cosmos DB", args: []string{"cosmosdb", "list", "--output", "table"}, keys: []string{"cosmos", "cosmosdb"}},
		{name: "Azure Resources (top)", args: []string{"resource", "list", "--query", "[:50].{name:name,type:type,location:location,resourceGroup:resourceGroup}", "--output", "table"}, keys: []string{"resources", "inventory", "list all"}},
	}

	defaultSections := map[string]bool{
		"Azure Account":   true,
		"Resource Groups": true,
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
			if !matched {
				continue
			}
		}

		result, err := c.execAz(ctx, s.args...)
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
			result, err := c.execAz(ctx, s.args...)
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
		out.WriteString("Warnings:\n")
		for _, w := range warnings {
			out.WriteString("- ")
			out.WriteString(w)
			out.WriteString("\n")
		}
	}

	return out.String(), nil
}

func hasFlag(args []string, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, a := range args {
		lower := strings.ToLower(strings.TrimSpace(a))
		if lower == name {
			return true
		}
		if strings.HasPrefix(lower, name+"=") {
			return true
		}
	}
	return false
}
