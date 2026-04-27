package sre

import (
	"context"
	"strings"
	"testing"
	"time"
)

// autoscalerMock fans out kubectl invocations to per-target responses.
// `Run` is what GetEvents (via DiagnosticsManager) actually calls.
type autoscalerMock struct {
	apiResourcesOut    string
	apiResourcesErr    error
	caDeployJSON       string // kubectl get deployment ... -o json
	caDeployErr        error
	caDeploySingleJSON string // fallback `get deployment cluster-autoscaler`
	eventsOutput       string // events JSON
	eventsErr          error
}

func (m *autoscalerMock) Run(_ context.Context, args ...string) (string, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "api-resources"):
		return m.apiResourcesOut, m.apiResourcesErr
	case strings.HasPrefix(full, "get events"):
		return m.eventsOutput, m.eventsErr
	}
	return "", nil
}
func (m *autoscalerMock) RunWithNamespace(_ context.Context, _ string, _ ...string) (string, error) {
	return "", nil
}
func (m *autoscalerMock) RunJSON(_ context.Context, args ...string) ([]byte, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "deployment cluster-autoscaler"):
		return []byte(m.caDeploySingleJSON), nil
	case strings.Contains(full, "deployment") && strings.Contains(full, "kube-system"):
		if m.caDeployErr != nil {
			return nil, m.caDeployErr
		}
		return []byte(m.caDeployJSON), nil
	}
	return nil, nil
}

func TestDetectAutoscaler_None(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON:    `{"items": []}`,
		apiResourcesOut: "",
	}, false)
	inv, err := a.DetectAutoscaler(context.Background())
	if err != nil {
		t.Fatalf("DetectAutoscaler: %v", err)
	}
	if inv.Type != AutoscalerNone {
		t.Errorf("expected None, got %s", inv.Type)
	}
}

func TestDetectAutoscaler_KarpenterOnly(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON:    `{"items": []}`,
		apiResourcesOut: "nodepools.karpenter.sh\nnodeclaims.karpenter.sh\n",
	}, false)
	inv, _ := a.DetectAutoscaler(context.Background())
	if inv.Type != AutoscalerKarpenter || !inv.KarpenterPresent || inv.ClusterAutoscalerSeen {
		t.Errorf("expected Karpenter only, got %+v", inv)
	}
}

func TestDetectAutoscaler_ClusterAutoscalerOnly(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON:    `{"items": [{"metadata": {"name": "cluster-autoscaler"}}]}`,
		apiResourcesOut: "",
	}, false)
	inv, _ := a.DetectAutoscaler(context.Background())
	if inv.Type != AutoscalerClusterAutoscaler || !inv.ClusterAutoscalerSeen || inv.KarpenterPresent {
		t.Errorf("expected CA only, got %+v", inv)
	}
}

func TestDetectAutoscaler_Both_PrefersKarpenter(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON:    `{"items": [{"metadata": {"name": "cluster-autoscaler"}}]}`,
		apiResourcesOut: "nodepools.karpenter.sh\n",
	}, false)
	inv, _ := a.DetectAutoscaler(context.Background())
	if inv.Type != AutoscalerKarpenter {
		t.Errorf("when both are present, Karpenter should win; got %s", inv.Type)
	}
	if !inv.ClusterAutoscalerSeen {
		t.Error("CA-seen flag should still be true so users see the coexistence")
	}
}

const sampleEventsJSON = `{
  "items": [
    {
      "type": "Warning", "reason": "FailedScheduling", "message": "0/3 nodes are available",
      "count": 5, "firstTimestamp": "2026-04-27T10:00:00Z", "lastTimestamp": "2026-04-27T10:30:00Z",
      "involvedObject": {"kind": "Pod", "name": "api-7d9", "namespace": "prod"}
    },
    {
      "type": "Normal", "reason": "TriggeredScaleUp", "message": "scaled up to 4 nodes",
      "count": 1, "lastTimestamp": "2026-04-27T10:32:00Z",
      "involvedObject": {"kind": "Node", "name": "ip-10-0-1-23"}
    },
    {
      "type": "Warning", "reason": "NotTriggerScaleUp", "message": "no pool can fit pod",
      "count": 3, "lastTimestamp": "2026-04-27T10:25:00Z",
      "involvedObject": {"kind": "Pod", "name": "api-7d9", "namespace": "prod"}
    },
    {
      "type": "Normal", "reason": "ScaleDownEmpty", "message": "removed empty node",
      "count": 1, "lastTimestamp": "2026-04-27T11:00:00Z",
      "involvedObject": {"kind": "Node", "name": "ip-10-0-1-99"}
    },
    {
      "type": "Warning", "reason": "FailedScheduling", "message": "insufficient cpu",
      "count": 2, "lastTimestamp": "2026-04-27T10:40:00Z",
      "involvedObject": {"kind": "Pod", "name": "worker-abc", "namespace": "default"}
    },
    {
      "type": "Warning", "reason": "FailedScheduling", "message": "ancient",
      "count": 100, "lastTimestamp": "2025-01-01T00:00:00Z",
      "involvedObject": {"kind": "Pod", "name": "ancient", "namespace": "default"}
    }
  ]
}`

func TestAnalyzeScalingWaste_AggregatesAndWindows(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON: `{"items": []}`,
		eventsOutput: sampleEventsJSON,
	}, false)

	// Use a wide lookback so the 2026 events are kept but the 2025-01-01
	// "ancient" event is filtered out (it's > a year old vs. now in 2026).
	report, err := a.AnalyzeScalingWaste(context.Background(), 365*24*time.Hour)
	if err != nil {
		t.Fatalf("AnalyzeScalingWaste: %v", err)
	}

	// 5 (api-7d9 FailedSched) + 2 (worker-abc) = 7. Ancient is excluded.
	if report.FailedScheduling != 7 {
		t.Errorf("FailedScheduling = %d, want 7 (ancient filtered)", report.FailedScheduling)
	}
	if report.NotTriggerScaleUp != 3 {
		t.Errorf("NotTriggerScaleUp = %d, want 3", report.NotTriggerScaleUp)
	}
	if report.TriggeredScaleUp != 1 {
		t.Errorf("TriggeredScaleUp = %d, want 1", report.TriggeredScaleUp)
	}
	if report.ScaleDownEmpty != 1 {
		t.Errorf("ScaleDownEmpty = %d, want 1", report.ScaleDownEmpty)
	}

	if len(report.TopFailingPods) == 0 {
		t.Fatal("expected at least one failing pod in TopFailingPods")
	}
	// api-7d9 has 5 FailedSched + 3 NotScaleUp = 8 → should be #1.
	top := report.TopFailingPods[0]
	if top.Name != "api-7d9" {
		t.Errorf("top failing pod = %q, want api-7d9", top.Name)
	}
	if top.FailedSchedCount != 5 || top.NotScaleUpCount != 3 {
		t.Errorf("api-7d9 counts = (%d, %d), want (5, 3)", top.FailedSchedCount, top.NotScaleUpCount)
	}
}

func TestAnalyzeScalingWaste_ShortLookbackFiltersOldEvents(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON: `{"items": []}`,
		eventsOutput: sampleEventsJSON,
	}, false)

	// 1ns lookback excludes everything except events with no timestamp.
	report, err := a.AnalyzeScalingWaste(context.Background(), time.Nanosecond)
	if err != nil {
		t.Fatalf("AnalyzeScalingWaste: %v", err)
	}
	if report.FailedScheduling != 0 || report.NotTriggerScaleUp != 0 {
		t.Errorf("everything should be filtered with 1ns lookback, got %+v", report)
	}
}

func TestAnalyzeScalingWaste_DefaultLookbackOnZero(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON: `{"items": []}`,
		eventsOutput: `{"items": []}`,
	}, false)

	report, err := a.AnalyzeScalingWaste(context.Background(), 0)
	if err != nil {
		t.Fatalf("AnalyzeScalingWaste: %v", err)
	}
	if report.LookbackWindow != "1h0m0s" {
		t.Errorf("zero lookback should default to 1h, got %q", report.LookbackWindow)
	}
}

func TestTopReasons_OrderAndCap(t *testing.T) {
	m := map[string]*ReasonCount{
		"A": {Reason: "A", Count: 1},
		"B": {Reason: "B", Count: 5},
		"C": {Reason: "C", Count: 3},
		"D": {Reason: "D", Count: 8},
	}
	got := topReasons(m, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Reason != "D" || got[1].Reason != "B" {
		t.Errorf("expected D, B; got %q, %q", got[0].Reason, got[1].Reason)
	}
}
