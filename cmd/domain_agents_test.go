package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildObservabilityFallbackReportIncludesEvidenceAndWarnings(t *testing.T) {
	report := buildObservabilityFallbackReport(
		"show me recent error logs",
		[]domainContextSection{{
			Title:   "Local Host Signals",
			Content: "macOS Recent Error Logs:\nerror: api worker failed\nwarning: retrying request",
		}},
		[]string{"Sentry observability: auth token and org slug are required"},
		errors.New("Clanker Cloud LLM request failed with status 503: Service Unavailable"),
	)

	for _, want := range []string{
		"Observability agent collected context, but AI synthesis failed.",
		"Synthesis error: Clanker Cloud LLM request failed with status 503: Service Unavailable",
		"Question: show me recent error logs",
		"## Local Host Signals",
		"error: api worker failed",
		"Unavailable Sources / Warnings",
		"Sentry observability: auth token and org slug are required",
		"Retry once the configured LLM provider is healthy",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("fallback report missing %q:\n%s", want, report)
		}
	}
}

func TestShouldQueryObservabilityProvider_BroadConfiguredProvidersIncludesClouds(t *testing.T) {
	query := "Show me recent error logs and warnings from this machine and any configured providers. Summarize what you can find."

	for _, provider := range []string{
		"local",
		"kubernetes",
		"aws",
		"gcp",
		"azure",
		"cloudflare",
		"digitalocean",
		"hetzner",
		"vercel",
		"flyio",
		"railway",
		"sentry",
	} {
		if !shouldQueryObservabilityProvider(query, provider) {
			t.Fatalf("provider %q should be queried for broad configured-provider observability prompt", provider)
		}
	}
}

func TestShouldQueryObservabilityProvider_ExplicitScopeStillLimitsProviders(t *testing.T) {
	query := "show sentry issues from the last deploy"

	if !shouldQueryObservabilityProvider(query, "sentry") {
		t.Fatal("sentry should be queried for explicit sentry observability prompt")
	}
	if shouldQueryObservabilityProvider(query, "aws") {
		t.Fatal("aws should not be queried for explicit sentry-only observability prompt")
	}
}

func TestHasAWSObservabilityAccess_ExplicitProfileCounts(t *testing.T) {
	if !hasAWSObservabilityAccess("clankercloud-tekbog") {
		t.Fatal("explicit profile from Clanker Cloud should count as AWS observability access")
	}
}
