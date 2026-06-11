package cmd

import (
	"strings"
	"testing"

	mcptransport "github.com/mark3labs/mcp-go/server"
)

func TestMCPCloudProviderTools_RegistrationDoesNotPanic(t *testing.T) {
	server := mcptransport.NewMCPServer("clanker-test", "0.0.0")
	registerCloudProviderMCPTools(server)
}

func TestMCPCloudProviderTools_ConfigCoverage(t *testing.T) {
	if len(cloudProviderMCPConfigs) != 6 {
		t.Fatalf("expected 6 cloud provider MCP configs, got %d", len(cloudProviderMCPConfigs))
	}

	found := map[string]bool{}
	for _, cfg := range cloudProviderMCPConfigs {
		found[cfg.Key] = true
		if strings.TrimSpace(cfg.ResourceHelp) == "" {
			t.Errorf("provider %s has empty resource help", cfg.Key)
		}
	}
	for _, key := range []string{"aws", "gcp", "azure", "cloudflare", "digitalocean", "hetzner"} {
		if !found[key] {
			t.Errorf("missing provider MCP config for %s", key)
		}
	}
}

func TestMCPCloudProviderTools_ResourceHelpIncludesScopedCoverage(t *testing.T) {
	tests := map[string][]string{
		"cloudflare":   {"ai-search-instances", "ai-gateway-logs", "browser-sessions", "secrets-stores"},
		"digitalocean": {"project-resources", "nfs", "dedicated-inference", "serverless-inference-models"},
	}

	byProvider := map[string]string{}
	for _, cfg := range cloudProviderMCPConfigs {
		byProvider[cfg.Key] = cfg.ResourceHelp
	}

	for provider, expected := range tests {
		help := byProvider[provider]
		if help == "" {
			t.Fatalf("missing provider MCP config for %s", provider)
		}
		for _, want := range expected {
			if !strings.Contains(help, want) {
				t.Errorf("%s resource help missing %q in %q", provider, want, help)
			}
		}
	}
}

func TestMCPCloudProviderContextQuestion_IncludesLatestCoverageHints(t *testing.T) {
	tests := map[string][]string{
		"cloudflare":   {"ai gateway", "browser rendering", "secrets store", "pipelines"},
		"digitalocean": {"gradient agents", "serverless inference", "dedicated inference", "nfs"},
		"aws":          {"resource explorer", "bedrock", "step functions", "verified permissions"},
		"gcp":          {"cloud asset inventory", "vertex ai", "alloydb", "workflows"},
		"azure":        {"resource graph", "container apps", "azure ai search", "private endpoints"},
		"hetzner":      {"placement groups", "server types", "isos"},
	}

	for provider, expected := range tests {
		got := strings.ToLower(mcpCloudProviderContextQuestion(provider, "inventory"))
		for _, want := range expected {
			if !strings.Contains(got, want) {
				t.Errorf("%s context hints missing %q in %q", provider, want, got)
			}
		}
	}
}
