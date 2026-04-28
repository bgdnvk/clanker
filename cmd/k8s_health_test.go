package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestFilterIssuesBySeverity(t *testing.T) {
	all := []sre.Issue{
		{Severity: sre.SeverityCritical, ResourceName: "p1"},
		{Severity: sre.SeverityWarning, ResourceName: "p2"},
		{Severity: sre.SeverityInfo, ResourceName: "p3"},
	}

	cases := []struct {
		floor string
		want  []string // ResourceNames in result
	}{
		{floor: "", want: []string{"p1", "p2", "p3"}},
		{floor: "info", want: []string{"p1", "p2", "p3"}},
		{floor: "warning", want: []string{"p1", "p2"}},
		{floor: "WARNING", want: []string{"p1", "p2"}}, // case-insensitive
		{floor: "critical", want: []string{"p1"}},
		{floor: "garbage", want: []string{"p1", "p2", "p3"}}, // unknown → fail-open, surface everything
	}

	for _, c := range cases {
		got := filterIssuesBySeverity(all, c.floor)
		var names []string
		for _, i := range got {
			names = append(names, i.ResourceName)
		}
		if !equalSlices(names, c.want) {
			t.Errorf("filter floor=%q got %v, want %v", c.floor, names, c.want)
		}
	}
}

func TestPrintHealthSummary_NoIssuesShowsCheckmark(t *testing.T) {
	var buf bytes.Buffer
	summary := &sre.ClusterHealthSummary{
		OverallHealth: "healthy", Score: 95,
		NodeHealth:     sre.ComponentHealth{Status: "healthy", Score: 100},
		WorkloadHealth: sre.ComponentHealth{Status: "healthy", Score: 95},
		StorageHealth:  sre.ComponentHealth{Status: "healthy", Score: 100},
		NetworkHealth:  sre.ComponentHealth{Status: "healthy", Score: 100},
		TotalPods:      10, RunningPods: 10,
	}
	printHealthSummary(&buf, summary, nil)
	out := buf.String()
	if !strings.Contains(out, "score 95/100") {
		t.Errorf("missing score line:\n%s", out)
	}
	if !strings.Contains(out, "No issues detected") {
		t.Errorf("missing no-issues message:\n%s", out)
	}
}

func TestPrintHealthSummary_IssuesSortedCriticalFirst(t *testing.T) {
	var buf bytes.Buffer
	summary := &sre.ClusterHealthSummary{
		OverallHealth: "critical", Score: 40,
		NodeHealth: sre.ComponentHealth{Status: "degraded", Score: 70},
	}
	issues := []sre.Issue{
		{Severity: sre.SeverityWarning, ResourceType: "pod", ResourceName: "warn-pod", Message: "warn"},
		{Severity: sre.SeverityCritical, ResourceType: "pod", ResourceName: "crit-pod", Message: "boom"},
		{Severity: sre.SeverityInfo, ResourceType: "node", ResourceName: "info-node", Message: "info"},
	}
	printHealthSummary(&buf, summary, issues)
	out := buf.String()

	criticalIdx := strings.Index(out, "crit-pod")
	warnIdx := strings.Index(out, "warn-pod")
	infoIdx := strings.Index(out, "info-node")

	if criticalIdx == -1 || warnIdx == -1 || infoIdx == -1 {
		t.Fatalf("missing rows in output:\n%s", out)
	}
	if !(criticalIdx < warnIdx && warnIdx < infoIdx) {
		t.Errorf("expected order critical < warning < info, got positions %d %d %d:\n%s",
			criticalIdx, warnIdx, infoIdx, out)
	}
}

func TestPrintHealthSummary_NilSummary(t *testing.T) {
	var buf bytes.Buffer
	printHealthSummary(&buf, nil, nil)
	if !strings.Contains(buf.String(), "No health summary returned") {
		t.Errorf("expected nil-summary message, got %q", buf.String())
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 80) != "short" {
		t.Error("short string should pass through unchanged")
	}
	long := strings.Repeat("x", 100)
	got := truncate(long, 10)
	if runes := []rune(got); len(runes) != 10 {
		t.Errorf("truncate returned %d runes, want 10", len(runes))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate output should end with ellipsis, got %q", got)
	}

	// Multi-byte input: truncate must count runes, not bytes, so a string
	// of emoji caps cleanly at the requested width.
	emoji := strings.Repeat("🚀", 50)
	if got := truncate(emoji, 5); len([]rune(got)) != 5 {
		t.Errorf("emoji truncate: %d runes, want 5", len([]rune(got)))
	}

	if truncate("anything", 0) != "" {
		t.Error("zero cap should return empty string")
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
