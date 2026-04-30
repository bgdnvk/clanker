package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// HoursPerMonth is the standard cloud-billing month (730 hours = 8760/12).
// Matches the convention used by AWS, GCP, and Kubecost.
const HoursPerMonth = 730.0

// K8sClient is the minimal kubectl surface this attributor needs. Mirrors
// the pattern used by sre / storage so the package can be built with
// k8s.NewClient via an adapter and stays mock-friendly in tests.
type K8sClient interface {
	RunJSON(ctx context.Context, args ...string) ([]byte, error)
}

// NodeInfo captures the subset of a node spec needed for attribution.
type NodeInfo struct {
	Name         string  `json:"name"`
	InstanceType string  `json:"instanceType,omitempty"`
	Region       string  `json:"region,omitempty"`
	Zone         string  `json:"zone,omitempty"`
	Provider     string  `json:"provider,omitempty"` // "aws", "gcp", "azure"
	AllocCPU     float64 `json:"allocCpuCores"`
	AllocMemMB   float64 `json:"allocMemMb"`
	HourlyUSD    float64 `json:"hourlyUsd,omitempty"`
	PriceKnown   bool    `json:"priceKnown"`
}

// PodAttribution is the cost attribution for a single pod. Shares are
// fractions in [0, 1]; DominantShare is max(CPUShare, MemShare) and is
// the share used to multiply the node's hourly price.
type PodAttribution struct {
	Namespace     string  `json:"namespace"`
	Pod           string  `json:"pod"`
	Workload      string  `json:"workload"`     // owner Deployment/STS/DS/Job name
	WorkloadKind  string  `json:"workloadKind"` // Deployment, StatefulSet, DaemonSet, ...
	Node          string  `json:"node,omitempty"`
	CPURequestC   float64 `json:"cpuRequestCores"`
	MemRequestMB  float64 `json:"memRequestMb"`
	CPUShare      float64 `json:"cpuShare"`
	MemShare      float64 `json:"memShare"`
	DominantShare float64 `json:"dominantShare"`
	HourlyUSD     float64 `json:"hourlyUsd,omitempty"`
	MonthlyUSD    float64 `json:"monthlyUsd,omitempty"`
	PriceKnown    bool    `json:"priceKnown"`
}

// WorkloadCostReport rolls up the pod-level attributions.
type WorkloadCostReport struct {
	GeneratedAt         time.Time        `json:"generatedAt"`
	NodesScanned        int              `json:"nodesScanned"`
	PodsScanned         int              `json:"podsScanned"`
	PodsWithoutNode     int              `json:"podsWithoutNode"`
	PodsWithoutRequests int              `json:"podsWithoutRequests"`
	NodesWithoutPrice   int              `json:"nodesWithoutPrice"`
	TotalHourlyUSD      float64          `json:"totalHourlyUsd"`
	TotalMonthlyUSD     float64          `json:"totalMonthlyUsd"`
	Nodes               []NodeInfo       `json:"nodes,omitempty"`
	Pods                []PodAttribution `json:"pods,omitempty"`
	Notes               string           `json:"notes,omitempty"`
}

// WorkloadCostAttributor walks pods + nodes and attributes node cost to
// each pod by max(cpu_share, mem_share) of the pod's host node. Rolls up
// to per-workload / per-namespace / per-node totals at the cmd layer.
//
// Read-only — only kubectl get is invoked.
type WorkloadCostAttributor struct {
	client K8sClient
	prices NodePriceLookup
	debug  bool
}

// NewWorkloadCostAttributor returns an attributor with the supplied price
// lookup. Pass nil to fall back to DefaultAWSOnDemandPrices(); pass
// CompositePriceLookup(custom, DefaultAWSOnDemandPrices()) to layer.
func NewWorkloadCostAttributor(client K8sClient, prices NodePriceLookup, debug bool) *WorkloadCostAttributor {
	if prices == nil {
		prices = DefaultAWSOnDemandPrices()
	}
	return &WorkloadCostAttributor{client: client, prices: prices, debug: debug}
}

// Attribute lists nodes + pods cluster-wide and produces a per-pod cost
// attribution. Pods missing a nodeName (Pending, Failed, etc.) are
// counted but not attributed — they show up in PodsWithoutNode.
func (a *WorkloadCostAttributor) Attribute(ctx context.Context) (*WorkloadCostReport, error) {
	report := &WorkloadCostReport{GeneratedAt: time.Now().UTC()}

	nodes, err := a.listNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	report.NodesScanned = len(nodes)

	// Apply price lookup once per node so the cmd layer can render node
	// totals without a second lookup.
	byNode := make(map[string]*NodeInfo, len(nodes))
	for i := range nodes {
		if p, ok := a.prices(nodes[i]); ok {
			nodes[i].HourlyUSD = p
			nodes[i].PriceKnown = true
		} else {
			report.NodesWithoutPrice++
		}
		byNode[nodes[i].Name] = &nodes[i]
	}
	report.Nodes = nodes

	pods, err := a.listPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	report.PodsScanned = len(pods)

	for _, p := range pods {
		if p.Node == "" {
			report.PodsWithoutNode++
			continue
		}
		if p.CPURequestC == 0 && p.MemRequestMB == 0 {
			report.PodsWithoutRequests++
			// Still record the pod so the operator can see un-requested
			// workloads — they'll show 0% share but are visible.
		}
		node, ok := byNode[p.Node]
		if !ok {
			// Pod references a node we didn't see (race: node deleted
			// between list calls). Skip rather than divide by zero.
			report.PodsWithoutNode++
			continue
		}

		if node.AllocCPU > 0 {
			p.CPUShare = p.CPURequestC / node.AllocCPU
		}
		if node.AllocMemMB > 0 {
			p.MemShare = p.MemRequestMB / node.AllocMemMB
		}
		p.DominantShare = p.CPUShare
		if p.MemShare > p.DominantShare {
			p.DominantShare = p.MemShare
		}

		if node.PriceKnown {
			p.HourlyUSD = node.HourlyUSD * p.DominantShare
			p.MonthlyUSD = p.HourlyUSD * HoursPerMonth
			p.PriceKnown = true
		}

		report.Pods = append(report.Pods, p)
		report.TotalHourlyUSD += p.HourlyUSD
	}
	report.TotalMonthlyUSD = report.TotalHourlyUSD * HoursPerMonth

	// Sort pods by monthly cost desc, then dominant-share desc as a
	// tiebreaker (so pods on un-priced nodes still get a stable order).
	sort.SliceStable(report.Pods, func(i, j int) bool {
		if report.Pods[i].MonthlyUSD != report.Pods[j].MonthlyUSD {
			return report.Pods[i].MonthlyUSD > report.Pods[j].MonthlyUSD
		}
		if report.Pods[i].DominantShare != report.Pods[j].DominantShare {
			return report.Pods[i].DominantShare > report.Pods[j].DominantShare
		}
		if report.Pods[i].Namespace != report.Pods[j].Namespace {
			return report.Pods[i].Namespace < report.Pods[j].Namespace
		}
		return report.Pods[i].Pod < report.Pods[j].Pod
	})

	if report.NodesWithoutPrice > 0 {
		report.Notes = fmt.Sprintf("%d/%d node(s) had no price match — install a custom NodePriceLookup or update the static table to fix",
			report.NodesWithoutPrice, report.NodesScanned)
	}
	return report, nil
}

// AggregateByWorkload rolls per-pod attributions up to per-workload
// totals (sum of replicas). Sorted by monthly cost desc.
func AggregateByWorkload(pods []PodAttribution) []WorkloadRollup {
	byKey := map[string]*WorkloadRollup{}
	for _, p := range pods {
		key := p.Namespace + "/" + p.WorkloadKind + "/" + p.Workload
		w, ok := byKey[key]
		if !ok {
			w = &WorkloadRollup{
				Namespace:    p.Namespace,
				Workload:     p.Workload,
				WorkloadKind: p.WorkloadKind,
			}
			byKey[key] = w
		}
		w.Pods++
		w.CPURequestC += p.CPURequestC
		w.MemRequestMB += p.MemRequestMB
		w.HourlyUSD += p.HourlyUSD
		w.MonthlyUSD += p.MonthlyUSD
		if !p.PriceKnown {
			w.AnyUnpriced = true
		}
	}
	out := make([]WorkloadRollup, 0, len(byKey))
	for _, w := range byKey {
		out = append(out, *w)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MonthlyUSD != out[j].MonthlyUSD {
			return out[i].MonthlyUSD > out[j].MonthlyUSD
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Workload < out[j].Workload
	})
	return out
}

// AggregateByNamespace rolls per-pod attributions up to per-namespace
// totals. Sorted by monthly cost desc.
func AggregateByNamespace(pods []PodAttribution) []NamespaceRollup {
	byNS := map[string]*NamespaceRollup{}
	for _, p := range pods {
		ns, ok := byNS[p.Namespace]
		if !ok {
			ns = &NamespaceRollup{Namespace: p.Namespace}
			byNS[p.Namespace] = ns
		}
		ns.Pods++
		ns.CPURequestC += p.CPURequestC
		ns.MemRequestMB += p.MemRequestMB
		ns.HourlyUSD += p.HourlyUSD
		ns.MonthlyUSD += p.MonthlyUSD
		if !p.PriceKnown {
			ns.AnyUnpriced = true
		}
	}
	out := make([]NamespaceRollup, 0, len(byNS))
	for _, ns := range byNS {
		out = append(out, *ns)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MonthlyUSD != out[j].MonthlyUSD {
			return out[i].MonthlyUSD > out[j].MonthlyUSD
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out
}

// WorkloadRollup is a workload-level aggregation of PodAttribution.
type WorkloadRollup struct {
	Namespace    string  `json:"namespace"`
	Workload     string  `json:"workload"`
	WorkloadKind string  `json:"workloadKind"`
	Pods         int     `json:"pods"`
	CPURequestC  float64 `json:"cpuRequestCores"`
	MemRequestMB float64 `json:"memRequestMb"`
	HourlyUSD    float64 `json:"hourlyUsd"`
	MonthlyUSD   float64 `json:"monthlyUsd"`
	AnyUnpriced  bool    `json:"anyUnpriced,omitempty"`
}

// NamespaceRollup is a namespace-level aggregation of PodAttribution.
type NamespaceRollup struct {
	Namespace    string  `json:"namespace"`
	Pods         int     `json:"pods"`
	CPURequestC  float64 `json:"cpuRequestCores"`
	MemRequestMB float64 `json:"memRequestMb"`
	HourlyUSD    float64 `json:"hourlyUsd"`
	MonthlyUSD   float64 `json:"monthlyUsd"`
	AnyUnpriced  bool    `json:"anyUnpriced,omitempty"`
}

// listNodes parses `kubectl get nodes -o json` into NodeInfo.
func (a *WorkloadCostAttributor) listNodes(ctx context.Context) ([]NodeInfo, error) {
	raw, err := a.client.RunJSON(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				ProviderID string `json:"providerID"`
			} `json:"spec"`
			Status struct {
				Allocatable struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"allocatable"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}

	out := make([]NodeInfo, 0, len(list.Items))
	for _, item := range list.Items {
		ni := NodeInfo{Name: item.Metadata.Name}
		// instance type — newer label preferred, fall back to legacy.
		if v, ok := item.Metadata.Labels["node.kubernetes.io/instance-type"]; ok {
			ni.InstanceType = v
		} else if v, ok := item.Metadata.Labels["beta.kubernetes.io/instance-type"]; ok {
			ni.InstanceType = v
		}
		if v, ok := item.Metadata.Labels["topology.kubernetes.io/region"]; ok {
			ni.Region = v
		}
		if v, ok := item.Metadata.Labels["topology.kubernetes.io/zone"]; ok {
			ni.Zone = v
		}
		ni.Provider = providerFromID(item.Spec.ProviderID)

		if c, err := parseCPUQuantity(item.Status.Allocatable.CPU); err == nil {
			ni.AllocCPU = c
		}
		if m, err := parseMemoryQuantity(item.Status.Allocatable.Memory); err == nil {
			ni.AllocMemMB = m
		}
		out = append(out, ni)
	}
	return out, nil
}

// listPods parses `kubectl get pods -A -o json` into PodAttribution
// stubs (without share / cost — Attribute fills those in).
func (a *WorkloadCostAttributor) listPods(ctx context.Context) ([]PodAttribution, error) {
	raw, err := a.client.RunJSON(ctx, "get", "pods", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name            string `json:"name"`
				Namespace       string `json:"namespace"`
				OwnerReferences []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Spec struct {
				NodeName   string `json:"nodeName"`
				Containers []struct {
					Resources struct {
						Requests struct {
							CPU    string `json:"cpu"`
							Memory string `json:"memory"`
						} `json:"requests"`
					} `json:"resources"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse pods: %w", err)
	}

	out := make([]PodAttribution, 0, len(list.Items))
	for _, item := range list.Items {
		// Skip terminal pods — they don't consume node resources.
		if strings.EqualFold(item.Status.Phase, "Succeeded") || strings.EqualFold(item.Status.Phase, "Failed") {
			continue
		}
		p := PodAttribution{
			Namespace: item.Metadata.Namespace,
			Pod:       item.Metadata.Name,
			Node:      item.Spec.NodeName,
		}
		p.Workload, p.WorkloadKind = workloadFromOwner(item.Metadata.OwnerReferences, item.Metadata.Name)

		var cpu, mem float64
		for _, c := range item.Spec.Containers {
			if v, err := parseCPUQuantity(c.Resources.Requests.CPU); err == nil {
				cpu += v
			}
			if v, err := parseMemoryQuantity(c.Resources.Requests.Memory); err == nil {
				mem += v
			}
		}
		p.CPURequestC = cpu
		p.MemRequestMB = mem
		out = append(out, p)
	}
	return out, nil
}

// workloadFromOwner derives the (workload-name, workload-kind) pair from
// a pod's ownerReferences. ReplicaSets are unwrapped to their parent
// Deployment by stripping the "-<hash>" suffix kubectl appends to RS
// names; Job-by-CronJob gets the same treatment. Standalone pods (no
// owner) are reported as kind="Pod".
func workloadFromOwner(refs []struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}, podName string) (string, string) {
	if len(refs) == 0 {
		return podName, "Pod"
	}
	o := refs[0]
	switch o.Kind {
	case "ReplicaSet":
		// "<deployment>-<podtemplate-hash>" → "<deployment>"
		if idx := strings.LastIndex(o.Name, "-"); idx > 0 {
			return o.Name[:idx], "Deployment"
		}
		return o.Name, "ReplicaSet"
	case "Job":
		// CronJob-owned Job names are "<cronjob>-<unix-ts>". Strip the
		// trailing numeric segment if present.
		if idx := strings.LastIndex(o.Name, "-"); idx > 0 {
			suffix := o.Name[idx+1:]
			allDigits := suffix != ""
			for _, c := range suffix {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return o.Name[:idx], "CronJob"
			}
		}
		return o.Name, "Job"
	default:
		return o.Name, o.Kind
	}
}

// providerFromID extracts the cloud provider from a node's spec.providerID.
// Examples: "aws:///us-east-1a/i-abc" → "aws", "gce://proj/zone/name" →
// "gcp", "azure:///subscriptions/..." → "azure".
func providerFromID(id string) string {
	if id == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(id, "aws://"):
		return "aws"
	case strings.HasPrefix(id, "gce://"):
		return "gcp"
	case strings.HasPrefix(id, "azure://"):
		return "azure"
	case strings.HasPrefix(id, "kind://"):
		return "kind"
	case strings.HasPrefix(id, "k3s://"):
		return "k3s"
	}
	if i := strings.Index(id, ":"); i > 0 {
		return id[:i]
	}
	return ""
}
