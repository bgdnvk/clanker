package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestPrintKarpenterReport_NotInstalled(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterReport(&buf, karpenterReport{
		Presence: &sre.KarpenterPresence{Installed: false, Notes: "api-resources timeout"},
	})
	out := buf.String()
	if !strings.Contains(out, "Karpenter is not installed") {
		t.Errorf("expected not-installed message, got %q", out)
	}
	if !strings.Contains(out, "api-resources timeout") {
		t.Errorf("expected note to be included, got %q", out)
	}
}

func TestPrintKarpenterReport_NoPoolsOrClaims(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterReport(&buf, karpenterReport{
		Presence: &sre.KarpenterPresence{
			Installed:           true,
			APIGroup:            "karpenter.sh",
			NodePoolsAvailable:  true,
			NodeClaimsAvailable: true,
		},
	})
	out := buf.String()
	if !strings.Contains(out, "Karpenter detected") {
		t.Errorf("expected detection header, got %q", out)
	}
	if !strings.Contains(out, "No NodePools defined") || !strings.Contains(out, "No NodeClaims provisioned") {
		t.Errorf("expected empty-state messages, got %q", out)
	}
}

func TestPrintKarpenterReport_PoolsAndClaimsTable(t *testing.T) {
	var buf bytes.Buffer
	printKarpenterReport(&buf, karpenterReport{
		Presence: &sre.KarpenterPresence{
			Installed:           true,
			APIGroup:            "karpenter.sh",
			NodePoolsAvailable:  true,
			NodeClaimsAvailable: true,
		},
		NodePools: []sre.NodePoolSummary{
			{Name: "gpu", NodeClass: "gpu-ec2", Weight: 100, Disruption: "WhenEmpty", Age: "5d"},
			{Name: "default", NodeClass: "default-ec2", Weight: 10, Disruption: "WhenUnderutilized", Age: "12d"},
		},
		NodeClaims: []sre.NodeClaimSummary{
			{Name: "claim-pending", NodePool: "gpu", Status: "Pending"},
			{Name: "claim-ready", NodePool: "default", NodeName: "ip-10-0-0-1", Status: "Ready", InstanceID: "i-abc"},
		},
	})
	out := buf.String()

	// Pools sorted by name → default before gpu
	defaultIdx := strings.Index(out, "default-ec2")
	gpuIdx := strings.Index(out, "gpu-ec2")
	if defaultIdx == -1 || gpuIdx == -1 || defaultIdx > gpuIdx {
		t.Errorf("pools should be name-sorted (default before gpu), got positions default=%d gpu=%d", defaultIdx, gpuIdx)
	}

	// Headline counts include pending (1)
	if !strings.Contains(out, "NodeClaims (2, 1 not Ready)") {
		t.Errorf("expected pending count in NodeClaims header, got: %s", out)
	}

	// Both claims appear
	for _, want := range []string{"claim-ready", "claim-pending", "i-abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output: %s", want, out)
		}
	}
}

func TestYesNoAvailable(t *testing.T) {
	if yesNoAvailable(true) != "yes" || yesNoAvailable(false) != "missing" {
		t.Error("yesNoAvailable wrong")
	}
}

func TestOrDash(t *testing.T) {
	if orDash("") != "-" {
		t.Error("orDash should replace empty with -")
	}
	if orDash("real") != "real" {
		t.Error("orDash should pass through non-empty")
	}
}
