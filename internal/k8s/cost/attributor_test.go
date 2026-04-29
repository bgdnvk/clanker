package cost

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

type attributorMock struct {
	nodes    string
	nodesErr error
	pods     string
	podsErr  error
}

func (m *attributorMock) RunJSON(_ context.Context, args ...string) ([]byte, error) {
	full := strings.Join(args, " ")
	switch {
	case strings.Contains(full, "get nodes"):
		return []byte(m.nodes), m.nodesErr
	case strings.Contains(full, "get pods"):
		return []byte(m.pods), m.podsErr
	}
	return []byte(`{"items": []}`), nil
}

const (
	twoNodes = `{
	  "items": [
	    {
	      "metadata": {"name": "node-a", "labels": {"node.kubernetes.io/instance-type": "m5.xlarge", "topology.kubernetes.io/region": "us-east-1"}},
	      "spec": {"providerID": "aws:///us-east-1a/i-aaaa"},
	      "status": {"allocatable": {"cpu": "4", "memory": "16384Mi"}}
	    },
	    {
	      "metadata": {"name": "node-b", "labels": {"node.kubernetes.io/instance-type": "unknown.huge"}},
	      "spec": {"providerID": "aws:///us-east-1a/i-bbbb"},
	      "status": {"allocatable": {"cpu": "8", "memory": "32768Mi"}}
	    }
	  ]
	}`

	mixedPods = `{
	  "items": [
	    {
	      "metadata": {"name": "api-7d9-xyz", "namespace": "prod", "ownerReferences": [{"kind": "ReplicaSet", "name": "api-7d9"}]},
	      "spec": {"nodeName": "node-a", "containers": [{"resources": {"requests": {"cpu": "1", "memory": "2Gi"}}}]},
	      "status": {"phase": "Running"}
	    },
	    {
	      "metadata": {"name": "worker-0", "namespace": "prod", "ownerReferences": [{"kind": "StatefulSet", "name": "worker"}]},
	      "spec": {"nodeName": "node-b", "containers": [{"resources": {"requests": {"cpu": "500m", "memory": "1Gi"}}}, {"resources": {"requests": {"cpu": "500m", "memory": "1Gi"}}}]},
	      "status": {"phase": "Running"}
	    },
	    {
	      "metadata": {"name": "stateless", "namespace": "default"},
	      "spec": {"nodeName": "node-a", "containers": [{"resources": {"requests": {"cpu": "100m", "memory": "128Mi"}}}]},
	      "status": {"phase": "Running"}
	    },
	    {
	      "metadata": {"name": "pending", "namespace": "default", "ownerReferences": [{"kind": "ReplicaSet", "name": "queue-1"}]},
	      "spec": {"containers": [{"resources": {"requests": {"cpu": "1", "memory": "1Gi"}}}]},
	      "status": {"phase": "Pending"}
	    },
	    {
	      "metadata": {"name": "succeeded-job", "namespace": "default", "ownerReferences": [{"kind": "Job", "name": "backup-1700000000"}]},
	      "spec": {"nodeName": "node-a", "containers": [{"resources": {"requests": {"cpu": "1"}}}]},
	      "status": {"phase": "Succeeded"}
	    }
	  ]
	}`
)

func TestAttribute_HappyPath(t *testing.T) {
	a := NewWorkloadCostAttributor(&attributorMock{nodes: twoNodes, pods: mixedPods}, nil, false)
	report, err := a.Attribute(context.Background())
	if err != nil {
		t.Fatalf("Attribute: %v", err)
	}

	if report.NodesScanned != 2 {
		t.Errorf("NodesScanned = %d, want 2", report.NodesScanned)
	}
	// 5 input pods, 1 Succeeded skipped → 4 listed.
	// 1 of those (pending) has no node → counted in PodsWithoutNode.
	if report.PodsScanned != 4 {
		t.Errorf("PodsScanned = %d, want 4", report.PodsScanned)
	}
	if report.PodsWithoutNode != 1 {
		t.Errorf("PodsWithoutNode = %d, want 1", report.PodsWithoutNode)
	}
	if report.NodesWithoutPrice != 1 {
		t.Errorf("NodesWithoutPrice = %d, want 1 (unknown.huge)", report.NodesWithoutPrice)
	}

	// 3 attributed pods.
	if len(report.Pods) != 3 {
		t.Fatalf("attributed pods = %d, want 3: %+v", len(report.Pods), report.Pods)
	}

	// api-7d9-xyz on node-a (m5.xlarge, $0.192/hr): cpu 1/4 = 0.25, mem 2048/16384 = 0.125 → dominant 0.25.
	// hourly = 0.192 * 0.25 = 0.048. monthly = 0.048 * 730 = 35.04.
	api := findPod(report.Pods, "api-7d9-xyz")
	if api == nil {
		t.Fatal("api-7d9-xyz missing from attribution")
	}
	if !approxEqual(api.DominantShare, 0.25, 1e-9) {
		t.Errorf("api dominant share = %v, want 0.25", api.DominantShare)
	}
	if !approxEqual(api.HourlyUSD, 0.048, 1e-9) {
		t.Errorf("api hourly = %v, want 0.048", api.HourlyUSD)
	}
	if api.WorkloadKind != "Deployment" || api.Workload != "api" {
		t.Errorf("api workload = %s/%s, want Deployment/api", api.WorkloadKind, api.Workload)
	}

	// worker-0 on node-b (no price): cpu 1/8 = 0.125, mem 2048/32768 = 0.0625 → dominant 0.125.
	worker := findPod(report.Pods, "worker-0")
	if worker == nil {
		t.Fatal("worker-0 missing from attribution")
	}
	if !approxEqual(worker.DominantShare, 0.125, 1e-9) {
		t.Errorf("worker dominant share = %v, want 0.125", worker.DominantShare)
	}
	if worker.PriceKnown {
		t.Errorf("worker should be unpriced (node-b is unknown.huge)")
	}
	if worker.WorkloadKind != "StatefulSet" || worker.Workload != "worker" {
		t.Errorf("worker workload = %s/%s, want StatefulSet/worker", worker.WorkloadKind, worker.Workload)
	}

	// First pod in the sorted list should be the highest-cost one (api).
	if report.Pods[0].Pod != "api-7d9-xyz" {
		t.Errorf("expected api-7d9-xyz first by monthly cost, got %s", report.Pods[0].Pod)
	}

	if report.Notes == "" {
		t.Error("expected note about unpriced nodes")
	}
}

func TestAttribute_NodeListErrorPropagates(t *testing.T) {
	a := NewWorkloadCostAttributor(&attributorMock{nodesErr: errors.New("forbidden")}, nil, false)
	if _, err := a.Attribute(context.Background()); err == nil {
		t.Error("expected error when node list fails")
	}
}

func TestAttribute_DivideByZeroNodeAllocSafe(t *testing.T) {
	// Node with empty allocatable (broken state). Pods on it should not
	// crash with NaN/Inf; share stays 0.
	a := NewWorkloadCostAttributor(&attributorMock{
		nodes: `{"items": [{"metadata": {"name": "n", "labels": {}}, "status": {"allocatable": {"cpu": "0", "memory": "0"}}}]}`,
		pods:  `{"items": [{"metadata": {"name": "p", "namespace": "x"}, "spec": {"nodeName": "n", "containers": [{"resources": {"requests": {"cpu": "1"}}}]}, "status": {"phase": "Running"}}]}`,
	}, nil, false)
	report, err := a.Attribute(context.Background())
	if err != nil {
		t.Fatalf("Attribute: %v", err)
	}
	if len(report.Pods) != 1 {
		t.Fatalf("attributed pods = %d, want 1", len(report.Pods))
	}
	if math.IsNaN(report.Pods[0].DominantShare) || math.IsInf(report.Pods[0].DominantShare, 0) {
		t.Errorf("dominant share should be 0 for zero-alloc node, got %v", report.Pods[0].DominantShare)
	}
	if report.Pods[0].DominantShare != 0 {
		t.Errorf("dominant share = %v, want 0", report.Pods[0].DominantShare)
	}
}

func TestAggregateByWorkload_SumsReplicas(t *testing.T) {
	pods := []PodAttribution{
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", HourlyUSD: 0.05, MonthlyUSD: 36.5, CPURequestC: 1, MemRequestMB: 2048, PriceKnown: true},
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", HourlyUSD: 0.05, MonthlyUSD: 36.5, CPURequestC: 1, MemRequestMB: 2048, PriceKnown: true},
		{Namespace: "prod", Workload: "api", WorkloadKind: "Deployment", HourlyUSD: 0, MonthlyUSD: 0, CPURequestC: 1, MemRequestMB: 2048, PriceKnown: false},
		{Namespace: "default", Workload: "queue", WorkloadKind: "Deployment", HourlyUSD: 0.01, MonthlyUSD: 7.3, PriceKnown: true},
	}
	out := AggregateByWorkload(pods)
	if len(out) != 2 {
		t.Fatalf("rollups = %d, want 2", len(out))
	}
	// First should be prod/api (highest cost).
	if out[0].Workload != "api" || out[0].Pods != 3 {
		t.Errorf("first rollup = %+v, want prod/api with 3 pods", out[0])
	}
	if !approxEqual(out[0].MonthlyUSD, 73.0, 1e-9) {
		t.Errorf("api monthly = %v, want 73.0", out[0].MonthlyUSD)
	}
	if !out[0].AnyUnpriced {
		t.Error("api should be flagged AnyUnpriced (one replica had no price)")
	}
	if out[1].Workload != "queue" || out[1].AnyUnpriced {
		t.Errorf("second rollup = %+v, want default/queue priced", out[1])
	}
}

func TestAggregateByNamespace_RollsUpAndSorts(t *testing.T) {
	pods := []PodAttribution{
		{Namespace: "prod", MonthlyUSD: 100, PriceKnown: true},
		{Namespace: "default", MonthlyUSD: 30, PriceKnown: true},
		{Namespace: "prod", MonthlyUSD: 50, PriceKnown: true},
	}
	out := AggregateByNamespace(pods)
	if len(out) != 2 {
		t.Fatalf("namespaces = %d, want 2", len(out))
	}
	if out[0].Namespace != "prod" || out[0].MonthlyUSD != 150 {
		t.Errorf("first namespace = %+v, want prod $150", out[0])
	}
}

func TestWorkloadFromOwner(t *testing.T) {
	type ref struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	cases := []struct {
		refs     []ref
		podName  string
		wantName string
		wantKind string
	}{
		{nil, "standalone", "standalone", "Pod"},
		{[]ref{{Kind: "ReplicaSet", Name: "api-7d9"}}, "api-7d9-xyz", "api", "Deployment"},
		{[]ref{{Kind: "StatefulSet", Name: "worker"}}, "worker-0", "worker", "StatefulSet"},
		{[]ref{{Kind: "DaemonSet", Name: "fluentd"}}, "fluentd-abc", "fluentd", "DaemonSet"},
		{[]ref{{Kind: "Job", Name: "backup-1700000000"}}, "backup-1700000000-xy", "backup", "CronJob"},
		{[]ref{{Kind: "Job", Name: "ad-hoc"}}, "ad-hoc-pod", "ad-hoc", "Job"},
	}
	for _, c := range cases {
		// Re-shape into the inline anonymous struct the function expects.
		shaped := make([]struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}, len(c.refs))
		for i, r := range c.refs {
			shaped[i].Kind = r.Kind
			shaped[i].Name = r.Name
		}
		gotName, gotKind := workloadFromOwner(shaped, c.podName)
		if gotName != c.wantName || gotKind != c.wantKind {
			t.Errorf("workloadFromOwner(%+v, %q) = (%q, %q), want (%q, %q)",
				c.refs, c.podName, gotName, gotKind, c.wantName, c.wantKind)
		}
	}
}

func TestProviderFromID(t *testing.T) {
	cases := map[string]string{
		"":                                  "",
		"aws:///us-east-1a/i-abc":           "aws",
		"gce://proj/us-central1-a/instance": "gcp",
		"azure:///subscriptions/sub/resourceGroups/rg":  "azure",
		"kind://docker/clanker-cluster/clanker-control": "kind",
		"foo://bar": "foo",
	}
	for in, want := range cases {
		if got := providerFromID(in); got != want {
			t.Errorf("providerFromID(%q) = %q, want %q", in, got, want)
		}
	}
}

func findPod(pods []PodAttribution, name string) *PodAttribution {
	for i := range pods {
		if pods[i].Pod == name {
			return &pods[i]
		}
	}
	return nil
}

func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < tol
}
