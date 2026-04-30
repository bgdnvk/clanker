package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestPrintWorkloadsAuditReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printWorkloadsAuditReport(&buf, nil)
	if !strings.Contains(buf.String(), "No workload health report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintWorkloadsAuditReport_Healthy(t *testing.T) {
	var buf bytes.Buffer
	printWorkloadsAuditReport(&buf, &sre.WorkloadHealthReport{TotalIssues: 0})
	out := buf.String()
	if !strings.Contains(out, "Total issues: 0") {
		t.Errorf("expected zero-issues header, got %q", out)
	}
	if !strings.Contains(out, "Cluster is healthy") {
		t.Errorf("expected healthy message, got %q", out)
	}
}

func TestPrintWorkloadsAuditReport_RendersSections(t *testing.T) {
	report := &sre.WorkloadHealthReport{
		TotalIssues: 3, Critical: 2, Warning: 1,
		ByCategory: []sre.CategoryCount{
			{Category: sre.HealthCategoryCrashLoop, Count: 2},
			{Category: sre.HealthCategoryOOMKilled, Count: 1},
		},
		HotPods: []sre.HotPod{
			{Namespace: "prod", Pod: "api-1", Issues: 2, Categories: []sre.HealthCategory{sre.HealthCategoryCrashLoop, sre.HealthCategoryRestartSpike}},
		},
		Issues: []sre.Issue{
			{Severity: sre.SeverityCritical, ResourceType: sre.ResourcePod, ResourceName: "api-1", Namespace: "prod", Message: "Container app is in CrashLoopBackOff"},
		},
	}
	var buf bytes.Buffer
	printWorkloadsAuditReport(&buf, report)
	out := buf.String()

	for _, want := range []string{
		"Total issues: 3",
		"critical 2",
		"By category",
		"CrashLoopBackOff",
		"Hot pods",
		"prod/api-1",
		"CRITICAL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

// TestFilterIssuesBySeverity already lives in cmd/k8s_health_test.go —
// the function is shared so we don't duplicate the test here.
