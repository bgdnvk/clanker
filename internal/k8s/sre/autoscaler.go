package sre

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AutoscalerType identifies which (if any) cluster autoscaler is running.
type AutoscalerType string

const (
	AutoscalerNone              AutoscalerType = "none"
	AutoscalerClusterAutoscaler AutoscalerType = "cluster-autoscaler"
	AutoscalerKarpenter         AutoscalerType = "karpenter"
)

// AutoscalerAnalyzer reads kube-system events to surface scaling waste:
// pods stuck pending because no node fit, scale-ups that didn't happen,
// scale-downs that pulled back too quickly, etc. Read-only.
type AutoscalerAnalyzer struct {
	client      K8sClient
	karpenter   *KarpenterDetector
	diagnostics *DiagnosticsManager
	debug       bool

	// MaxEvents bounds how many events the client-side classifier will
	// process. kubectl get events -A on a busy cluster can return tens of
	// thousands of objects; this cap keeps the analyzer's memory and CPU
	// usage predictable. Events are sorted by LastTimestamp ascending, so
	// when we trim we keep the most recent ones. 0 disables the cap.
	MaxEvents int
}

// defaultMaxEvents is the cap applied when MaxEvents is unset. 5000 covers
// a busy cluster's last few hours without runaway memory.
const defaultMaxEvents = 5000

// NewAutoscalerAnalyzer wires up an analyzer using an existing k8s client.
func NewAutoscalerAnalyzer(client K8sClient, debug bool) *AutoscalerAnalyzer {
	return &AutoscalerAnalyzer{
		client:      client,
		karpenter:   NewKarpenterDetector(client, debug),
		diagnostics: NewDiagnosticsManager(client, debug),
		debug:       debug,
		MaxEvents:   defaultMaxEvents,
	}
}

// AutoscalerInventory describes what (if anything) is autoscaling the cluster.
type AutoscalerInventory struct {
	Type                  AutoscalerType `json:"type"`
	ClusterAutoscalerSeen bool           `json:"clusterAutoscalerSeen"`
	KarpenterPresent      bool           `json:"karpenterPresent"`
	Notes                 string         `json:"notes,omitempty"`
}

// ScalingWasteReport summarises waste signals derived from CA / Karpenter
// events over a lookback window.
type ScalingWasteReport struct {
	Inventory          AutoscalerInventory `json:"inventory"`
	LookbackWindow     string              `json:"lookbackWindow"`
	GeneratedAt        time.Time           `json:"generatedAt"`
	EventsProcessed    int                 `json:"eventsProcessed"`
	EventsTruncated    bool                `json:"eventsTruncated,omitempty"` // true when the cap kicked in
	FailedScheduling   int                 `json:"failedScheduling"`          // pods that couldn't fit
	NotTriggerScaleUp  int                 `json:"notTriggerScaleUp"`         // CA decided NOT to scale up
	TriggeredScaleUp   int                 `json:"triggeredScaleUp"`          // CA decided to scale up
	ScaleDownEmpty     int                 `json:"scaleDownEmpty"`            // CA scaled down empty nodes
	ScaleDownUnneeded  int                 `json:"scaleDownUnneeded"`         // CA scaled down underutilised nodes
	NodeNotReadyEvents int                 `json:"nodeNotReadyEvents"`        // node went NotReady mid-flight
	HotPodReasons      []ReasonCount       `json:"hotPodReasons,omitempty"`
	HotNodeReasons     []ReasonCount       `json:"hotNodeReasons,omitempty"`
	TopFailingPods     []PodFailure        `json:"topFailingPods,omitempty"`
}

// ReasonCount is a (reason, count, sample-message) triple for the top-N
// reason aggregates surfaced in the waste report.
type ReasonCount struct {
	Reason        string `json:"reason"`
	Count         int    `json:"count"`
	SampleMessage string `json:"sampleMessage,omitempty"`
}

// PodFailure groups events under the pod that's seeing them, so users can
// jump straight to "this is the workload struggling to schedule".
type PodFailure struct {
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	FailedSchedCount int    `json:"failedScheduling"`
	NotScaleUpCount  int    `json:"notTriggerScaleUp"`
	LastReason       string `json:"lastReason,omitempty"`
	LastMessage      string `json:"lastMessage,omitempty"`

	// lastTimestamp is the timestamp of the event that supplied LastReason
	// / LastMessage, used internally during aggregation to keep the most
	// recent reason rather than overwriting newer with older.
	lastTimestamp time.Time
}

// DetectAutoscaler returns which autoscaler (if any) is running. It checks
// for a cluster-autoscaler Deployment in kube-system AND for Karpenter CRDs.
// Both can coexist (rare but valid during a migration).
func (a *AutoscalerAnalyzer) DetectAutoscaler(ctx context.Context) (*AutoscalerInventory, error) {
	inv := &AutoscalerInventory{Type: AutoscalerNone}

	caSeen, caNote := a.detectClusterAutoscalerDeployment(ctx)
	inv.ClusterAutoscalerSeen = caSeen
	if !caSeen && caNote != "" {
		inv.Notes = caNote
	}

	if a.karpenter != nil {
		presence, err := a.karpenter.Detect(ctx)
		switch {
		case err != nil:
			// Surface the error so users notice transient failures instead
			// of silently being told "no Karpenter" on a flaky kube-apiserver.
			if inv.Notes == "" {
				inv.Notes = fmt.Sprintf("karpenter detection failed: %v", err)
			} else {
				inv.Notes += fmt.Sprintf("; karpenter detection failed: %v", err)
			}
		case presence != nil:
			inv.KarpenterPresent = presence.Installed
		}
	}

	switch {
	case inv.ClusterAutoscalerSeen && inv.KarpenterPresent:
		// Both → Karpenter typically takes over node provisioning while CA
		// might still be scaling node groups for non-Karpenter workloads.
		// Report Karpenter as the dominant signal but keep CA's seen flag.
		inv.Type = AutoscalerKarpenter
	case inv.KarpenterPresent:
		inv.Type = AutoscalerKarpenter
	case inv.ClusterAutoscalerSeen:
		inv.Type = AutoscalerClusterAutoscaler
	}
	return inv, nil
}

// detectClusterAutoscalerDeployment looks for a deployment named
// `cluster-autoscaler` (or matching common label) in kube-system. Returns
// (false, note) when not found.
func (a *AutoscalerAnalyzer) detectClusterAutoscalerDeployment(ctx context.Context) (bool, string) {
	raw, err := a.client.RunJSON(ctx, "get", "deployment", "-n", "kube-system",
		"-l", "app.kubernetes.io/name=cluster-autoscaler",
		"-o", "json")
	if err != nil {
		// Try the older label, then a name-based fallback.
		alt, altErr := a.client.RunJSON(ctx, "get", "deployment", "cluster-autoscaler", "-n", "kube-system", "-o", "json")
		if altErr != nil {
			return false, fmt.Sprintf("kube-system lookup failed: %v", err)
		}
		raw = alt
	}

	var single struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err == nil && len(list.Items) > 0 {
		return true, ""
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Metadata.Name != "" {
		return true, ""
	}
	return false, "no cluster-autoscaler Deployment in kube-system"
}

// AnalyzeScalingWaste fetches recent kube-system events plus default-namespace
// pod-scheduling events, classifies them, and returns a structured report.
// Lookback defaults to 1h when zero/negative.
func (a *AutoscalerAnalyzer) AnalyzeScalingWaste(ctx context.Context, lookback time.Duration) (*ScalingWasteReport, error) {
	if lookback <= 0 {
		lookback = time.Hour
	}

	inv, _ := a.DetectAutoscaler(ctx)

	report := &ScalingWasteReport{
		Inventory:      *inv,
		LookbackWindow: lookback.String(),
		GeneratedAt:    time.Now().UTC(),
	}

	events, err := a.gatherRelevantEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("event fetch failed: %w", err)
	}

	cap := a.MaxEvents
	if cap == 0 {
		cap = defaultMaxEvents
	}
	if cap > 0 && len(events) > cap {
		// kubectl returns events sorted by lastTimestamp ascending — drop
		// the oldest, keep the newest cap events.
		events = events[len(events)-cap:]
		report.EventsTruncated = true
	}
	report.EventsProcessed = len(events)

	cutoff := time.Now().Add(-lookback)
	podReasonCounts := map[string]*ReasonCount{}
	nodeReasonCounts := map[string]*ReasonCount{}
	podFailures := map[string]*PodFailure{} // ns/name → failure entry

	for _, e := range events {
		// Use LastTimestamp for windowing; events without timestamps are
		// included so we don't lose data when timestamps are missing.
		if !e.LastTimestamp.IsZero() && e.LastTimestamp.Before(cutoff) {
			continue
		}

		c := effectiveEventCount(e)
		switch e.Reason {
		case "FailedScheduling":
			report.FailedScheduling += c
			bumpPod(podFailures, e, "FailedScheduling")
			bump(podReasonCounts, e)
		case "NotTriggerScaleUp":
			report.NotTriggerScaleUp += c
			bumpPod(podFailures, e, "NotTriggerScaleUp")
			bump(podReasonCounts, e)
		case "TriggeredScaleUp":
			report.TriggeredScaleUp += c
		case "ScaleDownEmpty":
			report.ScaleDownEmpty += c
			bump(nodeReasonCounts, e)
		case "ScaleDown":
			report.ScaleDownUnneeded += c
			bump(nodeReasonCounts, e)
		case "NodeNotReady":
			report.NodeNotReadyEvents += c
			bump(nodeReasonCounts, e)
		}
	}

	report.HotPodReasons = topReasons(podReasonCounts, 5)
	report.HotNodeReasons = topReasons(nodeReasonCounts, 5)
	report.TopFailingPods = topPodFailures(podFailures, 5)

	return report, nil
}

// gatherRelevantEvents pulls all events cluster-wide that the analyzer cares
// about. We deliberately do this in one pass (kubectl get events -A) rather
// than multiple --field-selector calls because field-selector OR is not
// supported and Reason matching is cheap client-side.
func (a *AutoscalerAnalyzer) gatherRelevantEvents(ctx context.Context) ([]EventInfo, error) {
	return a.diagnostics.GetEvents(ctx, "", "")
}

// effectiveEventCount normalises a zero/negative event Count to 1. Events
// fetched from `kubectl get events` sometimes have an unset Count (the new
// events.k8s.io/v1 API drops Count entirely on single-occurrence events),
// and the previous "guard the accumulator" approach miscounted multiple
// zero-count events as a single occurrence.
func effectiveEventCount(e EventInfo) int {
	if e.Count <= 0 {
		return 1
	}
	return e.Count
}

func bump(m map[string]*ReasonCount, e EventInfo) {
	rc, ok := m[e.Reason]
	if !ok {
		rc = &ReasonCount{Reason: e.Reason, SampleMessage: e.Message}
		m[e.Reason] = rc
	}
	rc.Count += effectiveEventCount(e)
}

func bumpPod(m map[string]*PodFailure, e EventInfo, reason string) {
	if e.InvolvedObject.Kind != "Pod" {
		return
	}
	key := e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name
	pf, ok := m[key]
	if !ok {
		pf = &PodFailure{Namespace: e.InvolvedObject.Namespace, Name: e.InvolvedObject.Name}
		m[key] = pf
	}
	count := effectiveEventCount(e)
	switch reason {
	case "FailedScheduling":
		pf.FailedSchedCount += count
	case "NotTriggerScaleUp":
		pf.NotScaleUpCount += count
	}
	// Only adopt the new event's reason if it's strictly more recent than
	// what we've already recorded. Falls back to "first non-zero timestamp
	// wins" when the existing record has no timestamp yet.
	if pf.lastTimestamp.IsZero() || e.LastTimestamp.After(pf.lastTimestamp) {
		pf.lastTimestamp = e.LastTimestamp
		pf.LastReason = e.Reason
		pf.LastMessage = strings.TrimSpace(e.Message)
	}
}

func topReasons(m map[string]*ReasonCount, n int) []ReasonCount {
	out := make([]ReasonCount, 0, len(m))
	for _, rc := range m {
		out = append(out, *rc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func topPodFailures(m map[string]*PodFailure, n int) []PodFailure {
	out := make([]PodFailure, 0, len(m))
	for _, pf := range m {
		out = append(out, *pf)
	}
	sort.Slice(out, func(i, j int) bool {
		ti := out[i].FailedSchedCount + out[i].NotScaleUpCount
		tj := out[j].FailedSchedCount + out[j].NotScaleUpCount
		return ti > tj
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
