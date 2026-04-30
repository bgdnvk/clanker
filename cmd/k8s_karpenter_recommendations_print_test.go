package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestPrintKarpenterRecsReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterRecsReport(&buf, nil)
	if !strings.Contains(buf.String(), "No advisor report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintKarpenterRecsReport_NotInstalled(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterRecsReport(&buf, &sre.KarpenterAdvisorReport{Installed: false})
	if !strings.Contains(buf.String(), "Karpenter not installed") {
		t.Errorf("expected not-installed message, got %q", buf.String())
	}
}

func TestPrintKarpenterRecsReport_HealthyInstall(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterRecsReport(&buf, &sre.KarpenterAdvisorReport{
		Installed: true, NodePools: 2, NodeClaims: 3,
	})
	out := buf.String()
	if !strings.Contains(out, "2 NodePool(s)") {
		t.Errorf("expected NodePool count in output, got %q", out)
	}
	if !strings.Contains(out, "looks healthy") {
		t.Errorf("expected healthy message, got %q", out)
	}
}

func TestPrintKarpenterRecsReport_RendersDetails(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterRecsReport(&buf, &sre.KarpenterAdvisorReport{
		Installed: true, NodePools: 1, NodeClaims: 0,
		Recommendations: []sre.KarpenterRecommendation{
			{Severity: sre.SeverityWarning, Resource: "nodepool", Name: "default", Issue: "no consolidation policy set", Detail: "policy is empty", Suggestion: "set spec.disruption..."},
		},
	})
	out := buf.String()

	for _, want := range []string{
		"WARNING",
		"nodepool",
		"default",
		"no consolidation policy",
		"Details:",
		"set spec.disruption",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}
