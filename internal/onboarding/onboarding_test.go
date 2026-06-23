package onboarding

import (
	"context"
	"strings"
	"testing"
)

func TestScanIncludesOfficialAuthGuides(t *testing.T) {
	result := Scan(context.Background(), ScanOptions{WantedProviders: []string{"aws", "gcp", "azure", "railway", "supabase", "flyio", "tencent", "verda", "sentry", "linear", "notion"}})

	for _, id := range []string{"aws", "gcp", "azure", "railway", "supabase", "flyio", "tencent", "verda", "sentry", "linear", "notion"} {
		guide, ok := result.AuthGuides[id]
		if !ok {
			t.Fatalf("missing auth guide for %s", id)
		}
		if strings.TrimSpace(guide.DocsURL) == "" {
			t.Fatalf("%s auth guide missing docs URL", id)
		}
		if len(guide.LoginCommands) == 0 && strings.TrimSpace(guide.TokenURL) == "" {
			t.Fatalf("%s auth guide missing login commands and token URL", id)
		}
	}

	if !strings.Contains(result.AgentInstructions, "official docs and token URLs") {
		t.Fatalf("agent instructions do not enforce official auth sources:\n%s", result.AgentInstructions)
	}
	if !strings.Contains(result.AgentInstructions, "clanker_cloud_install_setup_dependencies") {
		t.Fatalf("agent instructions do not tell MCP agents to perform dependency install:\n%s", result.AgentInstructions)
	}
	if !strings.Contains(result.AgentInstructions, "wait for it before chat") {
		t.Fatalf("agent instructions do not require waiting for scan before app use:\n%s", result.AgentInstructions)
	}
	if !strings.Contains(result.AgentInstructions, "clanker_k8s_ask_cluster") {
		t.Fatalf("agent instructions do not tell MCP agents how to chat with Kubernetes clusters:\n%s", result.AgentInstructions)
	}
}

func TestGuidesPreferOfficialVendorDocs(t *testing.T) {
	guides := Guides()

	checks := map[string]string{
		"aws":      "https://docs.aws.amazon.com/",
		"gcloud":   "https://docs.cloud.google.com/",
		"az":       "https://learn.microsoft.com/",
		"doctl":    "https://docs.digitalocean.com/",
		"railway":  "https://docs.railway.com/",
		"supabase": "https://supabase.com/docs/",
		"flyctl":   "https://fly.io/docs/",
	}
	for id, prefix := range checks {
		guide, ok := guides[id]
		if !ok {
			t.Fatalf("missing tool guide for %s", id)
		}
		if !strings.HasPrefix(guide.DocsURL, prefix) {
			t.Fatalf("%s docs URL = %q, want prefix %q", id, guide.DocsURL, prefix)
		}
	}
}
