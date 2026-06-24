package sre

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type CheckIssue struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"`
	Provider string   `json:"provider,omitempty"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Evidence []string `json:"evidence,omitempty"`
}

type CheckResult struct {
	GeneratedAt  string         `json:"generatedAt"`
	Status       string         `json:"status"`
	Summary      string         `json:"summary"`
	Providers    []string       `json:"providers"`
	Findings     []string       `json:"findings"`
	Issues       []CheckIssue   `json:"issues"`
	Discovery    Discovery      `json:"discovery"`
	Observations map[string]any `json:"observations"`
}

func Check(ctx context.Context) CheckResult {
	discovery := Discover(ctx)
	SortDiscovery(&discovery)
	observations := CollectObservations(ctx, discovery)
	findings := BuildFindings(discovery, observations)
	return BuildCheckResult(discovery, observations, findings)
}

func BuildCheckResult(discovery Discovery, observations map[string]any, findings []string) CheckResult {
	issues := ClassifyFindings(findings)
	providers := make([]string, 0, len(discovery.Providers))
	for _, provider := range discovery.Providers {
		if provider.Available {
			providers = append(providers, strings.ToLower(strings.TrimSpace(provider.ID)))
		}
	}
	sort.Strings(providers)

	status := "ok"
	for _, issue := range issues {
		switch issue.Severity {
		case "critical":
			status = "critical"
		case "warning":
			if status == "ok" {
				status = "warning"
			}
		}
	}

	summary := fmt.Sprintf("Live SRE check completed with %d finding%s", len(findings), pluralSuffix(len(findings)))
	if len(issues) > 0 {
		summary = fmt.Sprintf("Live SRE found %d actionable issue%s", len(issues), pluralSuffix(len(issues)))
	}

	return CheckResult{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Status:       status,
		Summary:      summary,
		Providers:    providers,
		Findings:     dedupeStrings(findings),
		Issues:       issues,
		Discovery:    discovery,
		Observations: observations,
	}
}

func ClassifyFindings(findings []string) []CheckIssue {
	issues := make([]CheckIssue, 0)
	seen := map[string]bool{}
	for _, raw := range findings {
		finding := strings.TrimSpace(raw)
		if finding == "" {
			continue
		}
		severity := classifyFindingSeverity(finding)
		if severity == "" {
			if findingLooksInformational(finding) {
				continue
			}
			continue
		}
		provider := inferFindingProvider(finding)
		category := inferFindingCategory(finding)
		title := findingTitle(finding, provider, category)
		id := stableIssueID(provider, category, finding)
		if seen[id] {
			continue
		}
		seen[id] = true
		issues = append(issues, CheckIssue{
			ID:       id,
			Severity: severity,
			Provider: provider,
			Category: category,
			Title:    title,
			Message:  finding,
			Evidence: []string{finding},
		})
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return severityRank(issues[i].Severity) > severityRank(issues[j].Severity)
	})
	return issues
}

func findingLooksInformational(value string) bool {
	lower := strings.ToLower(value)
	infoPhrases := []string{
		" detected",
		" sampled",
		" tracked",
		"available",
		"provider context",
		"heartbeat",
	}
	for _, phrase := range infoPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func classifyFindingSeverity(value string) string {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "alert:") ||
		strings.Contains(lower, "critical") ||
		strings.Contains(lower, "prod down") ||
		strings.Contains(lower, "unhealthy pods") ||
		strings.Contains(lower, "no endpoints") ||
		strings.Contains(lower, "not-ready") ||
		strings.Contains(lower, "failed state") {
		return "critical"
	}
	warningTerms := []string{
		"security:",
		"fired",
		"failure",
		"failed",
		"error",
		"drift",
		"unavailable",
		"scaling ceiling",
		"pending",
		"restart",
		"throttle",
		"alarm",
		"oomkilled",
		"crashloop",
	}
	for _, term := range warningTerms {
		if strings.Contains(lower, term) {
			return "warning"
		}
	}
	return ""
}

func inferFindingProvider(value string) string {
	lower := strings.ToLower(value)
	providers := []string{
		"aws",
		"cloudwatch",
		"gcp",
		"azure",
		"cloudflare",
		"digitalocean",
		"hetzner",
		"vercel",
		"railway",
		"flyio",
		"fly.io",
		"verda",
		"tencent",
		"supabase",
		"sentry",
		"kubernetes",
		"k8s",
		"docker",
		"terraform",
		"opentofu",
		"systemd",
	}
	for _, provider := range providers {
		if strings.Contains(lower, provider) {
			switch provider {
			case "fly.io":
				return "flyio"
			case "cloudwatch":
				return "aws"
			case "kubernetes":
				return "k8s"
			case "opentofu":
				return "terraform"
			default:
				return provider
			}
		}
	}
	return "infrastructure"
}

func inferFindingCategory(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "log") || strings.Contains(lower, "error reporting"):
		return "logs"
	case strings.Contains(lower, "alarm") || strings.Contains(lower, "alert") || strings.Contains(lower, "fired"):
		return "alert"
	case strings.Contains(lower, "mfa") || strings.Contains(lower, "security"):
		return "security"
	case strings.Contains(lower, "drift") || strings.Contains(lower, "terraform") || strings.Contains(lower, "opentofu"):
		return "iac"
	case strings.Contains(lower, "hpa") || strings.Contains(lower, "scaling") || strings.Contains(lower, "throttle"):
		return "scaling"
	case strings.Contains(lower, "pod") || strings.Contains(lower, "node") || strings.Contains(lower, "deployment") || strings.Contains(lower, "service"):
		return "availability"
	default:
		return "reliability"
	}
}

var leadingFindingPrefixRe = regexp.MustCompile(`(?i)^(alert|security|warning|critical):\s*`)

func findingTitle(value string, provider string, category string) string {
	cleaned := leadingFindingPrefixRe.ReplaceAllString(strings.TrimSpace(value), "")
	if len(cleaned) > 96 {
		cleaned = strings.TrimSpace(cleaned[:96]) + "..."
	}
	if provider != "" && provider != "infrastructure" {
		return strings.ToUpper(provider) + " " + category + ": " + cleaned
	}
	return strings.Title(category) + ": " + cleaned
}

func stableIssueID(provider string, category string, message string) string {
	h := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(provider + "|" + category + "|" + message))))
	return "sre-" + hex.EncodeToString(h[:])[:12]
}

func severityRank(value string) int {
	switch value {
	case "critical":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
