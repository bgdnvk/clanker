package sre

import (
	"context"
	"strings"
	"testing"
)

// healthAuditMock fans diagnostics-manager kubectl calls (RunJSON) to
// per-resource fixtures. Other interface methods stay no-op.
type healthAuditMock struct {
	nodes string
	pods  string
}

func (m *healthAuditMock) Run(_ context.Context, _ ...string) (string, error) {
	return "", nil
}
func (m *healthAuditMock) RunWithNamespace(_ context.Context, _ string, _ ...string) (string, error) {
	return "", nil
}
func (m *healthAuditMock) RunJSON(_ context.Context, args ...string) ([]byte, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "get nodes"):
		return []byte(m.nodes), nil
	case strings.Contains(full, "get pods"):
		return []byte(m.pods), nil
	}
	return []byte(`{"items": []}`), nil
}

func TestAudit_HealthyClusterEmptyReport(t *testing.T) {
	a := NewWorkloadHealthAuditor(&healthAuditMock{
		nodes: `{"items": []}`,
		pods:  `{"items": []}`,
	}, false)
	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if report.TotalIssues != 0 {
		t.Errorf("TotalIssues = %d, want 0", report.TotalIssues)
	}
	if len(report.ByCategory) != 0 || len(report.HotPods) != 0 {
		t.Errorf("expected empty rollup on healthy cluster, got %+v", report)
	}
}

func TestAudit_ClassifiesAndRollsUp(t *testing.T) {
	a := NewWorkloadHealthAuditor(&healthAuditMock{
		nodes: `{"items": []}`,
		pods: `{
		  "items": [
		    {
		      "metadata": {"name": "crash-1", "namespace": "prod"},
		      "spec": {"nodeName": "node-a"},
		      "status": {
		        "phase": "Running",
		        "containerStatuses": [
		          {"name": "app", "ready": false, "restartCount": 12, "state": {"waiting": {"reason": "CrashLoopBackOff", "message": "back-off restarting failed container"}}}
		        ]
		      }
		    },
		    {
		      "metadata": {"name": "oom-1", "namespace": "prod"},
		      "spec": {"nodeName": "node-a"},
		      "status": {
		        "phase": "Running",
		        "containerStatuses": [
		          {"name": "app", "ready": false, "restartCount": 3, "state": {"terminated": {"reason": "OOMKilled", "exitCode": 137}}}
		        ]
		      }
		    },
		    {
		      "metadata": {"name": "imgpull-1", "namespace": "default"},
		      "spec": {"nodeName": "node-a"},
		      "status": {
		        "phase": "Pending",
		        "containerStatuses": [
		          {"name": "app", "ready": false, "restartCount": 0, "state": {"waiting": {"reason": "ImagePullBackOff", "message": "manifest unknown"}}}
		        ]
		      }
		    }
		  ]
		}`,
	}, false)

	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if report.TotalIssues == 0 {
		t.Fatalf("expected issues to be detected, got 0")
	}

	// All categories we expect should be in the rollup.
	cats := map[HealthCategory]int{}
	for _, c := range report.ByCategory {
		cats[c.Category] = c.Count
	}
	for _, want := range []HealthCategory{HealthCategoryCrashLoop, HealthCategoryOOMKilled, HealthCategoryImagePull} {
		if cats[want] == 0 {
			t.Errorf("expected %s in rollup, got %+v", want, report.ByCategory)
		}
	}

	// crash-1 should appear in HotPods with CrashLoop + RestartSpike (it's
	// also flagged as restarting).
	var crashHot *HotPod
	for i := range report.HotPods {
		if report.HotPods[i].Pod == "crash-1" {
			crashHot = &report.HotPods[i]
			break
		}
	}
	if crashHot == nil {
		t.Fatal("crash-1 missing from HotPods")
	}
	if crashHot.Issues == 0 {
		t.Errorf("crash-1 should have at least one issue, got %+v", crashHot)
	}
}

func TestClassifyIssue(t *testing.T) {
	cases := []struct {
		message string
		want    HealthCategory
	}{
		{"Container app is in CrashLoopBackOff", HealthCategoryCrashLoop},
		{"Container app was OOMKilled", HealthCategoryOOMKilled},
		{"Container app has ImagePullBackOff", HealthCategoryImagePull},
		{"Container app has ErrImagePull", HealthCategoryImagePull},
		{"Container app has restarted 12 times", HealthCategoryRestartSpike},
		{"Pod is not ready", HealthCategoryNotReady},
		{"Node node-a has MemoryPressure", HealthCategoryNodePressure},
		{"Node node-a has DiskPressure", HealthCategoryNodePressure},
		{"Node node-a is NetworkUnavailable", HealthCategoryNodePressure},
		{"some unrelated message", HealthCategoryOther},
	}
	for _, c := range cases {
		got := classifyIssue(Issue{Message: c.message})
		if got != c.want {
			t.Errorf("classifyIssue(%q) = %s, want %s", c.message, got, c.want)
		}
	}
}

func TestHotPods_TopNTrim(t *testing.T) {
	// Build > 25 distinct pods each with one issue and confirm we trim
	// HotPods to 25.
	var items strings.Builder
	items.WriteString(`{"items": [`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			items.WriteString(",")
		}
		items.WriteString(`{
		  "metadata": {"name": "crash-`)
		// pad so each name is unique
		items.WriteString(fmtIndex(i))
		items.WriteString(`", "namespace": "prod"},
		  "spec": {"nodeName": "node-a"},
		  "status": {"phase": "Running", "containerStatuses": [{"name": "app", "ready": false, "restartCount": 1, "state": {"waiting": {"reason": "CrashLoopBackOff"}}}]}
		}`)
	}
	items.WriteString(`]}`)

	a := NewWorkloadHealthAuditor(&healthAuditMock{
		nodes: `{"items": []}`,
		pods:  items.String(),
	}, false)
	report, err := a.Audit(context.Background())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(report.HotPods) != 25 {
		t.Errorf("HotPods length = %d, want 25 (trimmed)", len(report.HotPods))
	}
}

func fmtIndex(i int) string {
	if i < 10 {
		return string(rune('0'+i)) + ""
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
