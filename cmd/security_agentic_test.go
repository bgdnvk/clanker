package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildSecurityAgenticFindingsDetectsToolMCPAndSupplyChainRisks(t *testing.T) {
	resource := deepResearchResource{
		ID:          "agent-runtime-1",
		Type:        "mcp-agent-runtime",
		Name:        "support-rag-agent",
		Region:      "us-east-1",
		IAMRole:     "agent-admin-role",
		IAMPolicies: []string{"AdministratorAccess"},
		Attributes: map[string]interface{}{
			"publicUrl":       "https://agent.example.com",
			"inputChannels":   []interface{}{"email", "web upload", "github pull_request"},
			"tools":           []interface{}{"shell", "database", "kubernetes"},
			"memory":          "vector rag conversation history",
			"mcpServer":       "github.com/example/untrusted-mcp",
			"containerImage":  "ghcr.io/example/agent:latest",
			"installedSkills": []interface{}{"browser-skill", "deploy-skill"},
		},
	}

	agentic := buildSecurityAgenticSurfaceFindings([]deepResearchResource{resource})
	if !hasSecurityFindingCategory(agentic, "agentic-risk") {
		t.Fatalf("expected agentic-risk finding, got %#v", agentic)
	}
	enrichedAgentic := enrichSecurityFindingsWithRemediation(agentic)
	if len(enrichedAgentic) == 0 || enrichedAgentic[0].AttackerView == "" || enrichedAgentic[0].DefenderView == "" || len(enrichedAgentic[0].RegressionTests) == 0 {
		t.Fatalf("expected enriched agentic finding perspectives and regression tests, got %#v", enrichedAgentic)
	}
	if !hasSecurityFindingCategory(agentic, "identity-pivot") {
		t.Fatalf("expected identity-pivot finding for privileged agent, got %#v", agentic)
	}

	mcp := buildSecurityMCPToolingFindings([]deepResearchResource{resource})
	if !hasSecurityFindingCategory(mcp, "mcp-tooling") {
		t.Fatalf("expected mcp-tooling finding, got %#v", mcp)
	}

	supplyChain := buildSecurityAgentSupplyChainFindings([]deepResearchResource{resource})
	if !hasSecurityFindingCategory(supplyChain, "agent-supply-chain") {
		t.Fatalf("expected agent-supply-chain finding, got %#v", supplyChain)
	}
	if len(supplyChain[0].Frameworks) == 0 || len(supplyChain[0].Threats) == 0 {
		t.Fatalf("expected framework and threat tags on supply-chain finding, got %#v", supplyChain[0])
	}
}

func hasSecurityFindingCategory(findings []securityFinding, category string) bool {
	for _, finding := range findings {
		if finding.Category == category {
			return true
		}
	}
	return false
}

func TestExtractSecurityProviderCandidateTargetsSkipsProviderHelpURLs(t *testing.T) {
	entry := securityProviderContextLine{
		Section: "GCP Warnings",
		Line:    "Cloud DNS API is disabled. Enable it by visiting https://console.developers.google.com/apis/api/dns.googleapis.com/overview?project=demo. See https://cloud.google.com/run/docs/troubleshooting for help.",
	}
	if got := extractSecurityProviderCandidateTargets("gcp", entry); len(got) != 0 {
		t.Fatalf("provider help URLs should not become endpoint candidates: %#v", got)
	}

	entry = securityProviderContextLine{
		Section: "Cloud Run Services",
		Line:    "Service: api, URL: https://api.example.com, Auth: none",
	}
	got := extractSecurityProviderCandidateTargets("gcp", entry)
	if len(got) != 1 || got[0] != "https://api.example.com" {
		t.Fatalf("expected real service URL to remain, got %#v", got)
	}
}

func TestBuildSecurityHTTPPostureFindingsDetectsCurlStyleRisks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "clanker-security.invalid" || r.Header.Get("X-Forwarded-Host") == "clanker-security.invalid" {
			w.Header().Set("Location", "http://clanker-security.invalid/login")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Allow", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Add("Set-Cookie", "session=abc")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	candidate := securitySurfaceCandidate{
		ResourceID:   "web-1",
		ResourceName: "public-web",
		ResourceType: "cloud-run-service",
		Provider:     "gcp",
		Endpoint:     server.URL,
		LikelyPublic: true,
	}
	probe, err := probeSecurityEndpoint(context.Background(), candidate, securityRuntimeAuthPack{})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	findings := buildSecurityHTTPPostureFindings([]securitySurfaceCandidate{candidate}, []securityProbeObservation{probe})
	for _, category := range []string{"http-hardening", "cors-misconfiguration", "tls-posture", "risky-methods", "sensitive-path", "header-trust", "cache-policy", "api-resource-controls"} {
		if !hasSecurityFindingCategory(findings, category) {
			t.Fatalf("expected %s finding in %#v", category, findings)
		}
	}
	enriched := enrichSecurityFindingsWithRemediation(findings)
	if len(enriched) == 0 || enriched[0].AttackerView == "" || len(enriched[0].RegressionTests) == 0 {
		t.Fatalf("expected enriched web posture findings, got %#v", enriched)
	}
}
