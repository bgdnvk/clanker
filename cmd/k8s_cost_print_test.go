package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/k8s/cost"
)

func TestPrintK8sCostReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printK8sCostReport(&buf, nil, "pod", 25)
	if !strings.Contains(buf.String(), "No cost report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintK8sCostReport_Empty(t *testing.T) {
	var buf bytes.Buffer
	printK8sCostReport(&buf, &cost.WorkloadCostReport{
		NodesScanned: 3, PodsScanned: 0,
	}, "pod", 25)
	out := buf.String()
	if !strings.Contains(out, "Scanned 3 node(s), 0 pod(s)") {
		t.Errorf("expected header, got %q", out)
	}
	if !strings.Contains(out, "No pods attributed") {
		t.Errorf("expected empty-state message, got %q", out)
	}
}

func TestPrintK8sCostReport_PodTopN(t *testing.T) {
	pods := []cost.PodAttribution{
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", Pod: "api-1", Node: "node-a", DominantShare: 0.5, HourlyUSD: 0.1, MonthlyUSD: 73, PriceKnown: true},
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", Pod: "api-2", Node: "node-a", DominantShare: 0.4, HourlyUSD: 0.08, MonthlyUSD: 58.4, PriceKnown: true},
		{Namespace: "default", Workload: "queue", WorkloadKind: "Deployment", Pod: "queue-1", Node: "node-b", DominantShare: 0.2, HourlyUSD: 0, MonthlyUSD: 0, PriceKnown: false},
	}
	var buf bytes.Buffer
	printK8sCostReport(&buf, &cost.WorkloadCostReport{
		NodesScanned: 2, PodsScanned: 3, Pods: pods, TotalHourlyUSD: 0.18, TotalMonthlyUSD: 131.4,
	}, "pod", 2)
	out := buf.String()

	// Should show first 2 pods + a (showing top 2 of 3) message.
	if !strings.Contains(out, "api-1") || !strings.Contains(out, "api-2") {
		t.Errorf("expected api-1 and api-2 in output, got:\n%s", out)
	}
	if strings.Contains(out, "queue-1") {
		t.Errorf("queue-1 should be cut off by --top 2, got:\n%s", out)
	}
	if !strings.Contains(out, "showing top 2 of 3") {
		t.Errorf("expected truncation note, got:\n%s", out)
	}
	// Unpriced pod renders as "—" — verify formatUSD path even though
	// queue isn't shown here, by placing it first.
}

func TestPrintK8sCostReport_WorkloadAggregation(t *testing.T) {
	pods := []cost.PodAttribution{
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", HourlyUSD: 0.1, MonthlyUSD: 73, PriceKnown: true},
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", HourlyUSD: 0, MonthlyUSD: 0, PriceKnown: false},
	}
	var buf bytes.Buffer
	printK8sCostReport(&buf, &cost.WorkloadCostReport{
		PodsScanned: 2, Pods: pods,
	}, "workload", 25)
	out := buf.String()

	if !strings.Contains(out, "Deployment") || !strings.Contains(out, "api") {
		t.Errorf("expected Deployment/api row, got:\n%s", out)
	}
	if !strings.Contains(out, "*") {
		t.Errorf("expected unpriced marker, got:\n%s", out)
	}
}

func TestPrintK8sCostReport_NamespaceAggregation(t *testing.T) {
	pods := []cost.PodAttribution{
		{Namespace: "prod", HourlyUSD: 0.1, MonthlyUSD: 73, PriceKnown: true},
		{Namespace: "default", HourlyUSD: 0.02, MonthlyUSD: 14.6, PriceKnown: true},
	}
	var buf bytes.Buffer
	printK8sCostReport(&buf, &cost.WorkloadCostReport{
		PodsScanned: 2, Pods: pods,
	}, "namespace", 25)
	out := buf.String()

	if !strings.Contains(out, "prod") || !strings.Contains(out, "default") {
		t.Errorf("expected both namespaces, got:\n%s", out)
	}
	// prod has higher cost — should appear first.
	if strings.Index(out, "prod") > strings.Index(out, "default") {
		t.Errorf("expected prod (higher cost) before default, got:\n%s", out)
	}
}

func TestPrintK8sCostReport_NodeAggregation(t *testing.T) {
	report := &cost.WorkloadCostReport{
		NodesScanned: 1,
		PodsScanned:  2,
		Nodes: []cost.NodeInfo{
			{Name: "node-a", InstanceType: "m5.xlarge", AllocCPU: 4, AllocMemMB: 16384, HourlyUSD: 0.192, PriceKnown: true},
		},
		Pods: []cost.PodAttribution{
			{Namespace: "prod", Pod: "p1", Node: "node-a", CPURequestC: 1, MemRequestMB: 2048, DominantShare: 0.25, HourlyUSD: 0.048, PriceKnown: true},
			{Namespace: "prod", Pod: "p2", Node: "node-a", CPURequestC: 0.5, MemRequestMB: 1024, DominantShare: 0.125, HourlyUSD: 0.024, PriceKnown: true},
		},
	}
	var buf bytes.Buffer
	printK8sCostReport(&buf, report, "node", 25)
	out := buf.String()

	for _, want := range []string{"node-a", "m5.xlarge", "yes", "$0.1920", "$0.0720"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}
