package sre

import "testing"

func TestClassifyFindingsKeepsOnlyActionableIssues(t *testing.T) {
	findings := []string{
		"docker detected with 3 running containers sampled",
		"aws provider context detected",
		"ALERT: 2 CloudWatch alarms in ALARM state",
		"k8s: 1 services with no endpoints (broken selectors)",
		"SECURITY: 4 IAM users without MFA",
	}

	issues := ClassifyFindings(findings)
	if len(issues) != 3 {
		t.Fatalf("issues = %d, want 3: %#v", len(issues), issues)
	}
	if issues[0].Severity != "critical" {
		t.Fatalf("first severity = %q, want critical", issues[0].Severity)
	}
	if issues[0].Provider != "aws" {
		t.Fatalf("first provider = %q, want aws", issues[0].Provider)
	}
	if issues[2].Category != "security" {
		t.Fatalf("third category = %q, want security", issues[2].Category)
	}
}

func TestBuildCheckResultStatus(t *testing.T) {
	result := BuildCheckResult(Discovery{}, nil, []string{
		"terraform/opentofu drift or pending plan changes detected",
	})
	if result.Status != "warning" {
		t.Fatalf("status = %q, want warning", result.Status)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(result.Issues))
	}

	ok := BuildCheckResult(Discovery{}, nil, []string{"kubernetes control-plane access detected"})
	if ok.Status != "ok" {
		t.Fatalf("ok status = %q, want ok", ok.Status)
	}
}
