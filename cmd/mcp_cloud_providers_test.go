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

func TestMCPCloudProviderContextQuestion_IncludesLatestCoverageHints(t *testing.T) {
	tests := map[string][]string{
		"cloudflare":   {"ai gateway", "vectorize", "hyperdrive", "durable objects"},
		"digitalocean": {"gradient agents", "serverless functions", "registries"},
		"aws":          {"bedrock", "qbusiness", "verified permissions"},
		"gcp":          {"vertex ai", "alloydb", "workflows"},
		"azure":        {"container apps", "azure ai search", "service bus"},
		"hetzner":      {"placement groups", "server types", "datacenters"},
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
