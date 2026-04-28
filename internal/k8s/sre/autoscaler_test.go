package sre

import (
	"context"
	"errors"
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

// sampleEventsJSON renders a fixture whose timestamps are relative to
// time.Now() so the test stays correct as wall-clock advances. The previous
// hardcoded 2026 fixture would silently start failing in 2027 once the
// "current" events fell outside a 365-day lookback.
func sampleEventsJSON() string {
	now := time.Now().UTC()
	recent := now.Add(-30 * time.Minute).Format(time.RFC3339)
	scaleUp := now.Add(-28 * time.Minute).Format(time.RFC3339)
	scaleDown := now.Add(-15 * time.Minute).Format(time.RFC3339)
	ancient := now.AddDate(-2, 0, 0).Format(time.RFC3339)
	return `{
  "items": [
    {
      "type": "Warning", "reason": "FailedScheduling", "message": "0/3 nodes are available",
      "count": 5, "firstTimestamp": "` + recent + `", "lastTimestamp": "` + recent + `",
      "involvedObject": {"kind": "Pod", "name": "api-7d9", "namespace": "prod"}
    },
    {
      "type": "Normal", "reason": "TriggeredScaleUp", "message": "scaled up to 4 nodes",
      "count": 1, "lastTimestamp": "` + scaleUp + `",
      "involvedObject": {"kind": "Node", "name": "ip-10-0-1-23"}
    },
    {
      "type": "Warning", "reason": "NotTriggerScaleUp", "message": "no pool can fit pod",
      "count": 3, "lastTimestamp": "` + recent + `",
      "involvedObject": {"kind": "Pod", "name": "api-7d9", "namespace": "prod"}
    },
    {
      "type": "Normal", "reason": "ScaleDownEmpty", "message": "removed empty node",
      "count": 1, "lastTimestamp": "` + scaleDown + `",
      "involvedObject": {"kind": "Node", "name": "ip-10-0-1-99"}
    },
    {
      "type": "Warning", "reason": "FailedScheduling", "message": "insufficient cpu",
      "count": 2, "lastTimestamp": "` + recent + `",
      "involvedObject": {"kind": "Pod", "name": "worker-abc", "namespace": "default"}
    },
    {
      "type": "Warning", "reason": "FailedScheduling", "message": "ancient",
      "count": 100, "lastTimestamp": "` + ancient + `",
      "involvedObject": {"kind": "Pod", "name": "ancient", "namespace": "default"}
    }
  ]
}`
}

func TestAnalyzeScalingWaste_AggregatesAndWindows(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON: `{"items": []}`,
		eventsOutput: sampleEventsJSON(),
	}, false)

	// Use a 365d lookback so the recent (now-relative) events are kept
	// but the explicitly "ancient" event (2 years old) is filtered out.
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
		eventsOutput: sampleEventsJSON(),
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

// TestBump_ZeroCountEventsCountAsOne is a regression test for a math bug
// where the previous implementation guarded `rc.Count <= 0` AFTER the
// addition, causing two zero-count events to total 1 instead of 2.
func TestBump_ZeroCountEventsCountAsOne(t *testing.T) {
	m := map[string]*ReasonCount{}
	bump(m, EventInfo{Reason: "X", Count: 0, Message: "first"})
	bump(m, EventInfo{Reason: "X", Count: 0, Message: "second"})
	bump(m, EventInfo{Reason: "X", Count: 0, Message: "third"})
	if got := m["X"].Count; got != 3 {
		t.Errorf("three zero-count events should total 3 occurrences, got %d", got)
	}
	if m["X"].SampleMessage != "first" {
		t.Errorf("sample message should be from the first event, got %q", m["X"].SampleMessage)
	}
}

// TestBumpPod_PrefersNewerLastReason verifies that out-of-order events
// don't overwrite a newer reason with an older one.
func TestBumpPod_PrefersNewerLastReason(t *testing.T) {
	m := map[string]*PodFailure{}
	older := EventInfo{Reason: "FailedScheduling", Count: 1, Message: "old"}
	older.LastTimestamp = time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	older.InvolvedObject.Kind = "Pod"
	older.InvolvedObject.Name = "p1"
	older.InvolvedObject.Namespace = "default"
	newer := older
	newer.Reason = "NotTriggerScaleUp"
	newer.Message = "new"
	newer.LastTimestamp = time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	// Apply newer first, then older — buggy code would overwrite with older.
	bumpPod(m, newer, "NotTriggerScaleUp")
	bumpPod(m, older, "FailedScheduling")

	pf := m["default/p1"]
	if pf == nil {
		t.Fatal("missing pod entry")
	}
	if pf.LastReason != "NotTriggerScaleUp" {
		t.Errorf("LastReason = %q, want NotTriggerScaleUp (newer event must win)", pf.LastReason)
	}
	if pf.LastMessage != "new" {
		t.Errorf("LastMessage = %q, want %q", pf.LastMessage, "new")
	}
}

func TestEffectiveEventCount(t *testing.T) {
	cases := []struct {
		name string
		in   EventInfo
		want int
	}{
		{"zero defaults to one", EventInfo{Count: 0}, 1},
		{"negative defaults to one", EventInfo{Count: -5}, 1},
		{"positive passes through", EventInfo{Count: 7}, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveEventCount(c.in); got != c.want {
				t.Errorf("effectiveEventCount = %d, want %d", got, c.want)
			}
		})
	}
}

// TestAnalyzeScalingWaste_RespectsMaxEventsCap drops oldest events when the
// fetched list exceeds the cap and flags the truncation in the report.
func TestAnalyzeScalingWaste_RespectsMaxEventsCap(t *testing.T) {
	// Generate 50 events, all with the same reason so we can count exactly.
	var items []string
	for i := 0; i < 50; i++ {
		items = append(items, `{
			"type": "Warning", "reason": "FailedScheduling", "message": "x",
			"count": 1,
			"lastTimestamp": "2026-04-27T10:00:00Z",
			"involvedObject": {"kind": "Pod", "name": "p", "namespace": "default"}
		}`)
	}
	json := `{"items": [` + strings.Join(items, ",") + `]}`

	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON: `{"items": []}`,
		eventsOutput: json,
	}, false)
	a.MaxEvents = 10

	report, err := a.AnalyzeScalingWaste(context.Background(), 365*24*time.Hour)
	if err != nil {
		t.Fatalf("AnalyzeScalingWaste: %v", err)
	}
	if !report.EventsTruncated {
		t.Error("expected EventsTruncated=true when input exceeds cap")
	}
	if report.EventsProcessed != 10 {
		t.Errorf("EventsProcessed = %d, want 10", report.EventsProcessed)
	}
	// Cap kept 10 events × count=1 each = 10 FailedScheduling.
	if report.FailedScheduling != 10 {
		t.Errorf("FailedScheduling = %d, want 10", report.FailedScheduling)
	}
}

// TestDetectAutoscaler_KarpenterDetectErrorSurfacesInNotes verifies that a
// transient kube-apiserver failure to probe Karpenter is recorded rather
// than silently flipping the report to "no Karpenter".
func TestDetectAutoscaler_KarpenterDetectErrorSurfacesInNotes(t *testing.T) {
	a := NewAutoscalerAnalyzer(&autoscalerMock{
		caDeployJSON:    `{"items": []}`,
		apiResourcesErr: errors.New("connection refused"),
	}, false)
	inv, _ := a.DetectAutoscaler(context.Background())
	if inv.KarpenterPresent {
		t.Error("KarpenterPresent should remain false on probe error")
	}
	// detect inside karpenter.go currently swallows the api-resources error
	// into Notes itself. The autoscaler-level probe goes through Detect()
	// which returns (presence, nil). Notes should reflect "api-resources
	// lookup failed" via the karpenter detector or the autoscaler's own
	// surface. Either path is acceptable; we only require some message.
	if inv.Notes == "" {
		t.Error("expected a non-empty Notes when probe fails")
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
