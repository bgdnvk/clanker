package sre

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// KarpenterAdvisor inspects detected Karpenter NodePools + NodeClaims and
// surfaces actionable recommendations: missing consolidation, unbounded
// pools, stale pools, NodeClaims stuck not-Ready, and weighted-but-zero
// configurations. Read-only.
//
// Usage: build a KarpenterDetector, call Advise(ctx). Returns an empty
// (non-nil) report when Karpenter is not installed so callers can render
// "no recommendations" without sniffing error strings.
type KarpenterAdvisor struct {
	detector *KarpenterDetector
	debug    bool
}

func NewKarpenterAdvisor(client K8sClient, debug bool) *KarpenterAdvisor {
	return &KarpenterAdvisor{
		detector: NewKarpenterDetector(client, debug),
		debug:    debug,
	}
}

// KarpenterRecommendation is one actionable finding on a NodePool /
// NodeClaim. Severity follows the rest of the sre package vocabulary.
type KarpenterRecommendation struct {
	Severity   IssueSeverity `json:"severity"`
	Resource   string        `json:"resource"` // "nodepool" or "nodeclaim"
	Name       string        `json:"name"`
	Issue      string        `json:"issue"`
	Detail     string        `json:"detail,omitempty"`
	Suggestion string        `json:"suggestion,omitempty"`
}

// KarpenterAdvisorReport aggregates the advisor output.
type KarpenterAdvisorReport struct {
	GeneratedAt     time.Time                 `json:"generatedAt"`
	Installed       bool                      `json:"installed"`
	NodePools       int                       `json:"nodePools"`
	NodeClaims      int                       `json:"nodeClaims"`
	Recommendations []KarpenterRecommendation `json:"recommendations,omitempty"`
	Notes           string                    `json:"notes,omitempty"`
}

// StaleNodePoolThreshold is how old (without provisioning any node) a
// NodePool can get before we flag it as likely abandoned. Exposed so
// tests can override without touching the package's defaults.
var StaleNodePoolThreshold = 7 * 24 * time.Hour

// NodeClaimStuckThreshold is how long a NodeClaim can sit not-Ready
// before we flag the provisioner as unhealthy.
var NodeClaimStuckThreshold = 30 * time.Minute

// Advise runs detection + listing + analysis and produces a report.
func (a *KarpenterAdvisor) Advise(ctx context.Context) (*KarpenterAdvisorReport, error) {
	report := &KarpenterAdvisorReport{GeneratedAt: time.Now().UTC()}

	presence, err := a.detector.Detect(ctx)
	if err != nil {
		return nil, fmt.Errorf("detect karpenter: %w", err)
	}
	report.Installed = presence.Installed
	if presence.Notes != "" {
		report.Notes = presence.Notes
	}
	if !presence.Installed {
		return report, nil
	}

	pools, err := a.detector.ListNodePools(ctx)
	if err != nil {
		appendNote(&report.Notes, fmt.Sprintf("list nodepools failed: %v", err))
	} else {
		report.NodePools = len(pools)
	}
	claims, err := a.detector.ListNodeClaims(ctx)
	if err != nil {
		appendNote(&report.Notes, fmt.Sprintf("list nodeclaims failed: %v", err))
	} else {
		report.NodeClaims = len(claims)
	}

	report.Recommendations = append(report.Recommendations, analysePools(pools)...)
	report.Recommendations = append(report.Recommendations, analyseClaims(claims)...)
	report.Recommendations = append(report.Recommendations, analyseGlobal(pools)...)

	sort.SliceStable(report.Recommendations, func(i, j int) bool {
		ri := karpenterSeverityRank(report.Recommendations[i].Severity)
		rj := karpenterSeverityRank(report.Recommendations[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if report.Recommendations[i].Resource != report.Recommendations[j].Resource {
			return report.Recommendations[i].Resource < report.Recommendations[j].Resource
		}
		return report.Recommendations[i].Name < report.Recommendations[j].Name
	})
	return report, nil
}

func analysePools(pools []NodePoolSummary) []KarpenterRecommendation {
	var out []KarpenterRecommendation
	now := time.Now().UTC()
	for _, p := range pools {
		// Missing consolidation policy → nodes never auto-deprovision.
		if p.Disruption == "" {
			out = append(out, KarpenterRecommendation{
				Severity:   SeverityWarning,
				Resource:   "nodepool",
				Name:       p.Name,
				Issue:      "no consolidation policy set",
				Detail:     "spec.disruption.consolidationPolicy is empty — nodes will not be consolidated when underutilised",
				Suggestion: `set spec.disruption.consolidationPolicy: "WhenUnderutilized" to recover idle node spend`,
			})
		}

		// Unbounded growth — empty Limits map means the pool can scale
		// arbitrarily. Often deliberate for system pools; flag as info.
		if len(p.Limits) == 0 {
			out = append(out, KarpenterRecommendation{
				Severity:   SeverityInfo,
				Resource:   "nodepool",
				Name:       p.Name,
				Issue:      "no spec.limits set",
				Detail:     "NodePool has no CPU/memory limits — runaway scheduling can drain budget",
				Suggestion: "set spec.limits.cpu and spec.limits.memory to bound the maximum spend",
			})
		}

		// Stale NodePool — created long ago but never provisioned a node.
		// Filter out pools that just look at zero today; we only care
		// about ones older than the staleness threshold.
		if !p.CreatedAt.IsZero() && p.NodesProvisioned == 0 && now.Sub(p.CreatedAt) > StaleNodePoolThreshold {
			out = append(out, KarpenterRecommendation{
				Severity:   SeverityInfo,
				Resource:   "nodepool",
				Name:       p.Name,
				Issue:      "stale — never provisioned a node",
				Detail:     fmt.Sprintf("NodePool is %s old with NodesProvisioned=0; selectors may not match any pending pods", p.Age),
				Suggestion: "review spec.template.spec.requirements / taints, or delete the pool if obsolete",
			})
		}
	}
	return out
}

func analyseClaims(claims []NodeClaimSummary) []KarpenterRecommendation {
	var out []KarpenterRecommendation
	now := time.Now().UTC()
	for _, c := range claims {
		if c.Status == "Ready" {
			continue
		}
		if c.CreatedAt.IsZero() {
			continue
		}
		age := now.Sub(c.CreatedAt)
		if age <= NodeClaimStuckThreshold {
			continue
		}
		out = append(out, KarpenterRecommendation{
			Severity: SeverityCritical,
			Resource: "nodeclaim",
			Name:     c.Name,
			Issue:    fmt.Sprintf("stuck %s for %s", c.Status, humanAge(age)),
			Detail: fmt.Sprintf("NodeClaim has not reached Ready in %s — provisioner or quota is likely unhealthy",
				humanAge(age)),
			Suggestion: "check `kubectl describe nodeclaim` for events; verify cloud quotas and provisioner pod logs",
		})
	}
	return out
}

// karpenterSeverityRank maps an IssueSeverity onto an integer for the
// sort comparator. Local copy so the sre package doesn't pull in the
// cmd-side severityRank.
func karpenterSeverityRank(s IssueSeverity) int {
	switch s {
	case SeverityCritical:
		return 2
	case SeverityWarning:
		return 1
	}
	return 0
}

// analyseGlobal looks for issues that only make sense across the whole
// pool set: weighted scheduling without weights, or duplicate weights.
func analyseGlobal(pools []NodePoolSummary) []KarpenterRecommendation {
	if len(pools) < 2 {
		return nil
	}
	allUnweighted := true
	for _, p := range pools {
		if p.Weight != 0 {
			allUnweighted = false
			break
		}
	}
	if allUnweighted {
		names := make([]string, 0, len(pools))
		for _, p := range pools {
			names = append(names, p.Name)
		}
		return []KarpenterRecommendation{{
			Severity:   SeverityInfo,
			Resource:   "nodepool",
			Name:       strings.Join(names, ","),
			Issue:      fmt.Sprintf("%d NodePools with no weight", len(pools)),
			Detail:     "multiple unweighted NodePools means Karpenter picks one arbitrarily; you have no preference signal",
			Suggestion: `set spec.weight on each NodePool (higher number wins) so cheaper / spot pools are preferred over fallback ones`,
		}}
	}
	return nil
}
