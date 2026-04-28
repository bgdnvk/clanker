package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/sre"
)

func TestPrintAutoscalerReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printAutoscalerReport(&buf, nil)
	if !strings.Contains(buf.String(), "No autoscaler report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintAutoscalerReport_AllZeroCounts(t *testing.T) {
	var buf bytes.Buffer
	printAutoscalerReport(&buf, &sre.ScalingWasteReport{
		Inventory:       sre.AutoscalerInventory{Type: sre.AutoscalerNone},
		LookbackWindow:  "1h0m0s",
		EventsProcessed: 0,
	})
	out := buf.String()
	if !strings.Contains(out, "Autoscaler: none") {
		t.Errorf("expected inventory line, got %q", out)
	}
	if !strings.Contains(out, "Events processed: 0") {
		t.Errorf("expected events-processed footer, got %q", out)
	}
	// Zero counters should still render, not blow up.
	if !strings.Contains(out, "FailedScheduling") {
		t.Errorf("expected signal table, got %q", out)
	}
	// No pod failures section when empty.
	if strings.Contains(out, "Top pods stuck") {
		t.Errorf("should not print pod-failures section when empty:\n%s", out)
	}
}

func TestPrintAutoscalerReport_TruncationWarning(t *testing.T) {
	var buf bytes.Buffer
	printAutoscalerReport(&buf, &sre.ScalingWasteReport{
		Inventory:       sre.AutoscalerInventory{Type: sre.AutoscalerClusterAutoscaler, ClusterAutoscalerSeen: true},
		LookbackWindow:  "1h0m0s",
		EventsProcessed: 5000,
		EventsTruncated: true,
	})
	out := buf.String()
	if !strings.Contains(out, "Event stream truncated to 5000") {
		t.Errorf("expected truncation warning, got %q", out)
	}
	if strings.Contains(out, "Events processed:") && !strings.Contains(out, "Event stream truncated") {
		t.Errorf("when truncated, do not show plain events-processed line")
	}
}

func TestPrintAutoscalerReport_PodFailuresAndHotReasons(t *testing.T) {
	var buf bytes.Buffer
	report := &sre.ScalingWasteReport{
		Inventory:         sre.AutoscalerInventory{Type: sre.AutoscalerKarpenter, KarpenterPresent: true},
		LookbackWindow:    "6h0m0s",
		EventsProcessed:   42,
		FailedScheduling:  10,
		NotTriggerScaleUp: 3,
		TriggeredScaleUp:  5,
		TopFailingPods: []sre.PodFailure{
			{
				Namespace:        "prod",
				Name:             "api-7d9",
				FailedSchedCount: 8,
				NotScaleUpCount:  2,
				LastReason:       "NotTriggerScaleUp",
			},
			{
				Namespace:        "default",
				Name:             "worker-abc",
				FailedSchedCount: 2,
				LastReason:       "FailedScheduling",
			},
		},
		HotNodeReasons: []sre.ReasonCount{
			{Reason: "ScaleDownEmpty", Count: 5, SampleMessage: "removed empty node"},
			{Reason: "NodeNotReady", Count: 2, SampleMessage: "kubelet unresponsive"},
		},
	}

	printAutoscalerReport(&buf, report)
	out := buf.String()

	// Headline includes the pod-side waste warning (10 + 3 = 13 events).
	if !strings.Contains(out, "13 pod-side scaling waste events") {
		t.Errorf("expected combined pod-waste warning, got %q", out)
	}
	// Per-pod table rows.
	for _, want := range []string{"api-7d9", "worker-abc", "prod", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in pod table:\n%s", want, out)
		}
	}
	// Hot-node reasons section.
	if !strings.Contains(out, "ScaleDownEmpty × 5") {
		t.Errorf("expected hot-reason summary, got %q", out)
	}
	if !strings.Contains(out, "removed empty node") {
		t.Errorf("expected sample message in output, got %q", out)
	}
}

func TestPrintAutoscalerReport_NotesSurfaced(t *testing.T) {
	var buf bytes.Buffer
	printAutoscalerReport(&buf, &sre.ScalingWasteReport{
		Inventory: sre.AutoscalerInventory{
			Type:  sre.AutoscalerNone,
			Notes: "karpenter detection failed: connection refused",
		},
		LookbackWindow:  "1h0m0s",
		EventsProcessed: 0,
	})
	if !strings.Contains(buf.String(), "connection refused") {
		t.Errorf("expected Notes to surface, got %q", buf.String())
	}
}
