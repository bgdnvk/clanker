package sre

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// nodePoolJSON is a small helper to keep test fixtures concise. The
// fields mirror the parseNodePool consumer in karpenter.go.
func nodePoolJSON(name, consolidation string, weight int, age time.Duration, nodesProvisioned int, hasLimits bool) string {
	limits := ""
	if hasLimits {
		limits = `"limits": {"cpu": "100", "memory": "200Gi"},`
	}
	created := time.Now().UTC().Add(-age).Format(time.RFC3339)
	return `{
	  "metadata": {"name": "` + name + `", "creationTimestamp": "` + created + `"},
	  "spec": {` + limits + `"weight": ` + itoa(weight) + `, "disruption": {"consolidationPolicy": "` + consolidation + `"}}
	}`
}

func nodeClaimJSON(name, status string, age time.Duration) string {
	created := time.Now().UTC().Add(-age).Format(time.RFC3339)
	condStatus := "False"
	if status == "Ready" {
		condStatus = "True"
	}
	return `{
	  "metadata": {"name": "` + name + `", "creationTimestamp": "` + created + `"},
	  "status": {"conditions": [{"type": "Ready", "status": "` + condStatus + `"}]}
	}`
}

// itoa is a private re-implementation to keep this file self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func wrapItems(items ...string) []byte {
	return []byte(`{"items": [` + strings.Join(items, ",") + `]}`)
}

func TestAdvise_NotInstalled(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesErr: errors.New("not found"),
	}, false)
	report, err := a.Advise(context.Background())
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if report.Installed {
		t.Error("should report not-installed when api-resources errors")
	}
	if len(report.Recommendations) != 0 {
		t.Errorf("uninstalled cluster should have 0 recommendations, got %+v", report.Recommendations)
	}
}

func TestAdvise_FlagsMissingConsolidation(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("default", "", 1, time.Hour, 1, true)),
	}, false)
	report, _ := a.Advise(context.Background())
	if !report.Installed {
		t.Fatal("should detect karpenter installed")
	}
	found := false
	for _, r := range report.Recommendations {
		if r.Resource == "nodepool" && strings.Contains(r.Issue, "consolidation") {
			found = true
			if r.Severity != SeverityWarning {
				t.Errorf("missing-consolidation should be warning, got %s", r.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected missing-consolidation recommendation, got %+v", report.Recommendations)
	}
}

func TestAdvise_FlagsUnboundedLimits(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("default", "WhenUnderutilized", 0, time.Hour, 0, false)),
	}, false)
	report, _ := a.Advise(context.Background())
	found := false
	for _, r := range report.Recommendations {
		if strings.Contains(r.Issue, "no spec.limits") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unbounded-limits recommendation, got %+v", report.Recommendations)
	}
}

func TestAdvise_FlagsStaleNodePool(t *testing.T) {
	// Pool created > StaleNodePoolThreshold ago with NodesProvisioned=0.
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("ghost", "WhenUnderutilized", 1, StaleNodePoolThreshold+time.Hour, 0, true)),
	}, false)
	report, _ := a.Advise(context.Background())
	found := false
	for _, r := range report.Recommendations {
		if strings.Contains(r.Issue, "stale") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected stale-pool recommendation, got %+v", report.Recommendations)
	}
}

func TestAdvise_DoesNotFlagFreshUnusedPool(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("just-created", "WhenUnderutilized", 1, time.Hour, 0, true)),
	}, false)
	report, _ := a.Advise(context.Background())
	for _, r := range report.Recommendations {
		if strings.Contains(r.Issue, "stale") {
			t.Errorf("fresh pool should not be flagged stale, got %+v", r)
		}
	}
}

func TestAdvise_FlagsStuckNodeClaim(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\nnodeclaims.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("default", "WhenUnderutilized", 1, time.Hour, 1, true)),
		nodeClaimsJSON:  wrapItems(nodeClaimJSON("stuck", "Pending", NodeClaimStuckThreshold+time.Hour)),
	}, false)
	report, _ := a.Advise(context.Background())
	found := false
	for _, r := range report.Recommendations {
		if r.Resource == "nodeclaim" && strings.Contains(r.Issue, "stuck") {
			found = true
			if r.Severity != SeverityCritical {
				t.Errorf("stuck nodeclaim should be critical, got %s", r.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected stuck-nodeclaim recommendation, got %+v", report.Recommendations)
	}
}

func TestAdvise_DoesNotFlagReadyNodeClaim(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\nnodeclaims.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("default", "WhenUnderutilized", 1, time.Hour, 1, true)),
		nodeClaimsJSON:  wrapItems(nodeClaimJSON("healthy", "Ready", NodeClaimStuckThreshold+time.Hour)),
	}, false)
	report, _ := a.Advise(context.Background())
	for _, r := range report.Recommendations {
		if r.Resource == "nodeclaim" {
			t.Errorf("Ready nodeclaim should not be flagged, got %+v", r)
		}
	}
}

func TestAdvise_FlagsMultipleUnweightedPools(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
		nodePoolsJSON: wrapItems(
			nodePoolJSON("a", "WhenUnderutilized", 0, time.Hour, 1, true),
			nodePoolJSON("b", "WhenUnderutilized", 0, time.Hour, 1, true),
		),
	}, false)
	report, _ := a.Advise(context.Background())
	found := false
	for _, r := range report.Recommendations {
		if strings.Contains(r.Issue, "no weight") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unweighted-pools recommendation, got %+v", report.Recommendations)
	}
}

func TestAdvise_DoesNotFlagSinglePoolForWeights(t *testing.T) {
	// One pool, no weight — shouldn't flag the global "multiple unweighted"
	// rule because there's nothing to choose between.
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("only", "WhenUnderutilized", 0, time.Hour, 1, true)),
	}, false)
	report, _ := a.Advise(context.Background())
	for _, r := range report.Recommendations {
		if strings.Contains(r.Issue, "no weight") {
			t.Errorf("single pool should not trigger the unweighted-pools rule, got %+v", r)
		}
	}
}

func TestAdvise_RecommendationsSortedCriticalFirst(t *testing.T) {
	a := NewKarpenterAdvisor(&karpenterMock{
		apiResourcesOut: "nodepools.karpenter.sh\nnodeclaims.karpenter.sh\n",
		nodePoolsJSON:   wrapItems(nodePoolJSON("p1", "", 0, time.Hour, 1, false)),
		nodeClaimsJSON:  wrapItems(nodeClaimJSON("nc1", "Pending", NodeClaimStuckThreshold+time.Hour)),
	}, false)
	report, _ := a.Advise(context.Background())
	if len(report.Recommendations) == 0 {
		t.Fatal("expected at least one recommendation")
	}
	if report.Recommendations[0].Severity != SeverityCritical {
		t.Errorf("first recommendation should be critical (stuck NodeClaim), got %s severity", report.Recommendations[0].Severity)
	}
}
