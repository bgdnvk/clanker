package sre

import (
	"context"
	"sort"
	"strings"
	"time"
)

// WorkloadHealthAuditor wraps the existing DiagnosticsManager and rolls
// per-resource issues up into a categorised report so operators get a
// single "what's broken in this cluster" view instead of a flat list.
//
// Read-only — only kubectl get is invoked via DiagnosticsManager.
type WorkloadHealthAuditor struct {
	client K8sClient
	debug  bool
}

func NewWorkloadHealthAuditor(client K8sClient, debug bool) *WorkloadHealthAuditor {
	return &WorkloadHealthAuditor{client: client, debug: debug}
}

// HealthCategory groups the kinds of failures we count as headline
// reliability signals. Anything outside these buckets falls into Other.
type HealthCategory string

const (
	HealthCategoryCrashLoop    HealthCategory = "CrashLoopBackOff"
	HealthCategoryOOMKilled    HealthCategory = "OOMKilled"
	HealthCategoryImagePull    HealthCategory = "ImagePullBackOff"
	HealthCategoryRestartSpike HealthCategory = "RestartSpike"
	HealthCategoryNotReady     HealthCategory = "NotReady"
	HealthCategoryNodePressure HealthCategory = "NodePressure"
	HealthCategoryOther        HealthCategory = "Other"
)

// CategoryCount is one row of the rollup.
type CategoryCount struct {
	Category HealthCategory `json:"category"`
	Count    int            `json:"count"`
}

// HotPod is a frequently-failing pod surfaced to the top of the report.
type HotPod struct {
	Namespace  string           `json:"namespace"`
	Pod        string           `json:"pod"`
	Issues     int              `json:"issues"`
	Categories []HealthCategory `json:"categories"`
}

// WorkloadHealthReport is the audit output.
type WorkloadHealthReport struct {
	GeneratedAt time.Time       `json:"generatedAt"`
	TotalIssues int             `json:"totalIssues"`
	Critical    int             `json:"critical"`
	Warning     int             `json:"warning"`
	Info        int             `json:"info"`
	ByCategory  []CategoryCount `json:"byCategory,omitempty"`
	HotPods     []HotPod        `json:"hotPods,omitempty"`
	Issues      []Issue         `json:"issues,omitempty"`
	Notes       string          `json:"notes,omitempty"`
}

// Audit runs DetectClusterIssues, classifies the resulting issues into
// reliability categories, and returns a sorted rollup. Empty clusters
// produce an empty report (not an error).
func (a *WorkloadHealthAuditor) Audit(ctx context.Context) (*WorkloadHealthReport, error) {
	dm := NewDiagnosticsManager(a.client, a.debug)
	issues, err := dm.DetectClusterIssues(ctx)
	if err != nil {
		return nil, err
	}

	report := &WorkloadHealthReport{
		GeneratedAt: time.Now().UTC(),
		TotalIssues: len(issues),
		Issues:      issues,
	}

	categoryCounts := map[HealthCategory]int{}
	hotPods := map[string]*HotPod{}
	for _, iss := range issues {
		switch iss.Severity {
		case SeverityCritical:
			report.Critical++
		case SeverityWarning:
			report.Warning++
		case SeverityInfo:
			report.Info++
		}
		cat := classifyIssue(iss)
		categoryCounts[cat]++
		if iss.ResourceType == ResourcePod {
			key := iss.Namespace + "/" + iss.ResourceName
			hp, ok := hotPods[key]
			if !ok {
				hp = &HotPod{Namespace: iss.Namespace, Pod: iss.ResourceName}
				hotPods[key] = hp
			}
			hp.Issues++
			if !containsCategory(hp.Categories, cat) {
				hp.Categories = append(hp.Categories, cat)
			}
		}
	}

	for cat, n := range categoryCounts {
		report.ByCategory = append(report.ByCategory, CategoryCount{Category: cat, Count: n})
	}
	sort.Slice(report.ByCategory, func(i, j int) bool {
		if report.ByCategory[i].Count != report.ByCategory[j].Count {
			return report.ByCategory[i].Count > report.ByCategory[j].Count
		}
		return string(report.ByCategory[i].Category) < string(report.ByCategory[j].Category)
	})

	for _, hp := range hotPods {
		report.HotPods = append(report.HotPods, *hp)
	}
	sort.Slice(report.HotPods, func(i, j int) bool {
		if report.HotPods[i].Issues != report.HotPods[j].Issues {
			return report.HotPods[i].Issues > report.HotPods[j].Issues
		}
		if report.HotPods[i].Namespace != report.HotPods[j].Namespace {
			return report.HotPods[i].Namespace < report.HotPods[j].Namespace
		}
		return report.HotPods[i].Pod < report.HotPods[j].Pod
	})
	// Trim hot pods to a sensible top-N. Operators wanting more can
	// inspect Issues directly.
	if len(report.HotPods) > 25 {
		report.HotPods = report.HotPods[:25]
	}

	return report, nil
}

// classifyIssue maps a diagnostic Issue to a HealthCategory by matching
// the message prefix that detectPodIssues / detectNodeIssues emit. The
// match is intentionally loose because the existing code uses Sprintf
// with embedded names ("Container %s is in CrashLoopBackOff") — we want
// the substring match to survive name variation.
func classifyIssue(iss Issue) HealthCategory {
	msg := iss.Message
	switch {
	case strings.Contains(msg, "CrashLoopBackOff"):
		return HealthCategoryCrashLoop
	case strings.Contains(msg, "OOMKilled"),
		strings.Contains(msg, "OOM killed"),
		strings.Contains(msg, "OOM-killed"):
		return HealthCategoryOOMKilled
	case strings.Contains(msg, "ImagePullBackOff"), strings.Contains(msg, "ErrImagePull"):
		return HealthCategoryImagePull
	case strings.Contains(msg, "restarted"):
		return HealthCategoryRestartSpike
	case strings.Contains(msg, "not ready"), strings.Contains(msg, "NotReady"):
		return HealthCategoryNotReady
	case strings.Contains(msg, "MemoryPressure"),
		strings.Contains(msg, "DiskPressure"),
		strings.Contains(msg, "PIDPressure"),
		strings.Contains(msg, "NetworkUnavailable"):
		return HealthCategoryNodePressure
	}
	return HealthCategoryOther
}

func containsCategory(s []HealthCategory, c HealthCategory) bool {
	for _, x := range s {
		if x == c {
			return true
		}
	}
	return false
}
