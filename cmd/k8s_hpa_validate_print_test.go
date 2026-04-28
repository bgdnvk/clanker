package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestPrintHPAValidationReport_Empty(t *testing.T) {
	var buf bytes.Buffer
	printHPAValidationReport(&buf, &sre.HPAValidationReport{})
	out := buf.String()
	if !strings.Contains(out, "No configuration issues detected") {
		t.Errorf("expected clean-bill message, got %q", out)
	}
}

func TestPrintHPAValidationReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printHPAValidationReport(&buf, nil)
	if !strings.Contains(buf.String(), "No validation report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintHPAValidationReport_KEDADetectionLine(t *testing.T) {
	var buf bytes.Buffer
	printHPAValidationReport(&buf, &sre.HPAValidationReport{
		HPAsScanned: 3, ScaledObjectsScanned: 1, KEDAInstalled: true,
	})
	out := buf.String()
	if !strings.Contains(out, "Scanned 3 HPA(s) + 1 KEDA ScaledObject(s)") {
		t.Errorf("expected counts in header, got %q", out)
	}
	if !strings.Contains(out, "KEDA: detected") {
		t.Errorf("expected 'KEDA: detected', got %q", out)
	}
}

func TestPrintHPAValidationReport_FindingsSortedCriticalFirst(t *testing.T) {
	var buf bytes.Buffer
	printHPAValidationReport(&buf, &sre.HPAValidationReport{
		HPAsScanned: 3,
		Findings: []sre.HPAFinding{
			{Severity: sre.SeverityWarning, Resource: "hpa", Namespace: "default", Name: "warn", Issue: "min == max"},
			{Severity: sre.SeverityCritical, Resource: "hpa", Namespace: "prod", Name: "crit", Issue: "no metrics configured"},
			{Severity: sre.SeverityInfo, Resource: "scaledobject", Namespace: "default", Name: "info", Issue: "fast polling"},
		},
	})
	out := buf.String()

	critIdx := strings.Index(out, "crit")
	warnIdx := strings.Index(out, "warn")
	infoIdx := strings.Index(out, "info")

	if critIdx == -1 || warnIdx == -1 || infoIdx == -1 {
		t.Fatalf("missing rows in output:\n%s", out)
	}
	if !(critIdx < warnIdx && warnIdx < infoIdx) {
		t.Errorf("findings should sort critical → warning → info; got positions %d %d %d", critIdx, warnIdx, infoIdx)
	}
}

func TestFilterHPAFindingsBySeverity(t *testing.T) {
	all := []sre.HPAFinding{
		{Severity: sre.SeverityCritical, Name: "c"},
		{Severity: sre.SeverityWarning, Name: "w"},
		{Severity: sre.SeverityInfo, Name: "i"},
	}
	cases := []struct {
		floor string
		want  []string
	}{
		{"", []string{"c", "w", "i"}},
		{"info", []string{"c", "w", "i"}},
		{"warning", []string{"c", "w"}},
		{"WARNING", []string{"c", "w"}}, // case-insensitive
		{"critical", []string{"c"}},
		{"unknown", []string{"c", "w", "i"}}, // fail-open on typo
	}
	for _, tt := range cases {
		got := filterHPAFindingsBySeverity(all, tt.floor)
		var names []string
		for _, f := range got {
			names = append(names, f.Name)
		}
		if !equalSlices(names, tt.want) {
			t.Errorf("floor=%q got %v, want %v", tt.floor, names, tt.want)
		}
	}
}
