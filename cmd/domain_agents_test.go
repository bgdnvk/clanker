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
