package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/cost"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	k8sCostOutput     string
	k8sCostKubeconfig string
	k8sCostContext    string
	k8sCostBy         string // pod | workload | namespace | node
	k8sCostPricesFile string
	k8sCostTopN       int
)

var k8sCostCmd = &cobra.Command{
	Use:   "cost",
	Short: "Attribute node cost to pods / workloads / namespaces",
	Long: `Estimate per-workload Kubernetes cost by walking pods + nodes
and attributing each pod's share of its host node's hourly price.

Pod share = max(cpu_request / node_alloc_cpu, mem_request / node_alloc_mem).
Pod cost  = node_hourly_price × pod_share. (Standard Kubecost-style model.)

Node prices come from a built-in static AWS on-demand fallback. Operators
with real billing data should pass --prices <file> with a JSON map of
instance-type → hourly USD; entries override the fallback.

Read-only — only kubectl get is invoked.

Examples:
  clanker k8s cost                              # per-pod (top 25)
  clanker k8s cost --by workload                # roll up to deployments/STS/DS
  clanker k8s cost --by namespace -o json
  clanker k8s cost --prices ./node-prices.json
  clanker k8s cost --top 100`,
	RunE: runK8sCost,
}

func init() {
	k8sCmd.AddCommand(k8sCostCmd)
	k8sCostCmd.Flags().StringVarP(&k8sCostOutput, "output", "o", "table", "Output format (table, json)")
	k8sCostCmd.Flags().StringVar(&k8sCostKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sCostCmd.Flags().StringVar(&k8sCostContext, "context", "", "kubectl context to use")
	k8sCostCmd.Flags().StringVar(&k8sCostBy, "by", "pod", "Aggregation level (pod, workload, namespace, node)")
	k8sCostCmd.Flags().StringVar(&k8sCostPricesFile, "prices", "", "Path to JSON file mapping instance-type → hourly USD (overrides built-in)")
	k8sCostCmd.Flags().IntVar(&k8sCostTopN, "top", 25, "Show only the top N rows (table mode); 0 = all")
}

func runK8sCost(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	priceLookup, err := loadPriceLookup(k8sCostPricesFile)
	if err != nil {
		return err
	}

	client := k8s.NewClient(k8sCostKubeconfig, k8sCostContext, debug)
	attributor := cost.NewWorkloadCostAttributor(k8s.NewK8sCostAdapter(client), priceLookup, debug)

	report, err := attributor.Attribute(ctx)
	if err != nil {
		return fmt.Errorf("workload cost attribution failed: %w", err)
	}

	switch strings.ToLower(k8sCostOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printK8sCostReport(os.Stdout, report, strings.ToLower(k8sCostBy), k8sCostTopN)
		return nil
	}
}

// loadPriceLookup builds the price lookup chain. With a custom file, the
// user table wins on a hit and falls through to the built-in static AWS
// on-demand table for misses.
func loadPriceLookup(path string) (cost.NodePriceLookup, error) {
	if path == "" {
		return cost.DefaultAWSOnDemandPrices(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prices file %q: %w", path, err)
	}
	var prices map[string]float64
	if err := json.Unmarshal(raw, &prices); err != nil {
		return nil, fmt.Errorf("parse prices file %q: %w", path, err)
	}
	return cost.CompositePriceLookup(cost.MapPriceLookup(prices), cost.DefaultAWSOnDemandPrices()), nil
}

func printK8sCostReport(out io.Writer, report *cost.WorkloadCostReport, by string, topN int) {
	if report == nil {
		fmt.Fprintln(out, "No cost report.")
		return
	}

	fmt.Fprintf(out, "Scanned %d node(s), %d pod(s)\n", report.NodesScanned, report.PodsScanned)
	if report.PodsWithoutNode > 0 {
		fmt.Fprintf(out, "  • %d pod(s) skipped (no node assignment — Pending/etc.)\n", report.PodsWithoutNode)
	}
	if report.PodsWithoutRequests > 0 {
		fmt.Fprintf(out, "  • %d pod(s) had no resource requests (0%% share)\n", report.PodsWithoutRequests)
	}
	if report.NodesWithoutPrice > 0 {
		fmt.Fprintf(out, "  • %d node(s) without a price match — pass --prices to fix\n", report.NodesWithoutPrice)
	}
	fmt.Fprintf(out, "Estimated total: $%.2f/hr  ($%.2f/mo)\n",
		report.TotalHourlyUSD, report.TotalMonthlyUSD)
	if report.Notes != "" {
		fmt.Fprintf(out, "Notes: %s\n", report.Notes)
	}
	fmt.Fprintln(out)

	if len(report.Pods) == 0 {
		fmt.Fprintln(out, "No pods attributed.")
		return
	}

	switch by {
	case "workload":
		printWorkloadRollup(out, cost.AggregateByWorkload(report.Pods), topN)
	case "namespace":
		printNamespaceRollup(out, cost.AggregateByNamespace(report.Pods), topN)
	case "node":
		printNodeRollup(out, report)
	default:
		printPodRollup(out, report.Pods, topN)
	}
}

func printPodRollup(out io.Writer, pods []cost.PodAttribution, topN int) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tWORKLOAD\tPOD\tNODE\tCPU(c)\tMEM(MiB)\tSHARE\tHOURLY\tMONTHLY")
	fmt.Fprintln(w, "---------\t--------\t---\t----\t------\t--------\t-----\t------\t-------")
	limit := len(pods)
	if topN > 0 && topN < limit {
		limit = topN
	}
	for _, p := range pods[:limit] {
		fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\t%.2f\t%.0f\t%.1f%%\t%s\t%s\n",
			p.Namespace,
			p.WorkloadKind, p.Workload,
			truncate(p.Pod, 40),
			truncate(p.Node, 30),
			p.CPURequestC, p.MemRequestMB,
			p.DominantShare*100,
			formatUSD(p.HourlyUSD, p.PriceKnown),
			formatUSD(p.MonthlyUSD, p.PriceKnown),
		)
	}
	w.Flush()
	if topN > 0 && len(pods) > topN {
		fmt.Fprintf(out, "\n(showing top %d of %d pods — pass --top 0 for all)\n", topN, len(pods))
	}
}

func printWorkloadRollup(out io.Writer, rollups []cost.WorkloadRollup, topN int) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tKIND\tWORKLOAD\tREPLICAS\tCPU(c)\tMEM(MiB)\tHOURLY\tMONTHLY")
	fmt.Fprintln(w, "---------\t----\t--------\t--------\t------\t--------\t------\t-------")
	limit := len(rollups)
	if topN > 0 && topN < limit {
		limit = topN
	}
	for _, r := range rollups[:limit] {
		marker := ""
		if r.AnyUnpriced {
			marker = " *"
		}
		fmt.Fprintf(w, "%s\t%s\t%s%s\t%d\t%.2f\t%.0f\t$%.4f\t$%.2f\n",
			r.Namespace, r.WorkloadKind, r.Workload, marker,
			r.Pods, r.CPURequestC, r.MemRequestMB,
			r.HourlyUSD, r.MonthlyUSD,
		)
	}
	w.Flush()
	if topN > 0 && len(rollups) > topN {
		fmt.Fprintf(out, "\n(showing top %d of %d workloads)\n", topN, len(rollups))
	}
	for _, r := range rollups[:limit] {
		if r.AnyUnpriced {
			fmt.Fprintln(out, "* = at least one replica on an unpriced node — totals are partial")
			break
		}
	}
}

func printNamespaceRollup(out io.Writer, rollups []cost.NamespaceRollup, topN int) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tPODS\tCPU(c)\tMEM(MiB)\tHOURLY\tMONTHLY")
	fmt.Fprintln(w, "---------\t----\t------\t--------\t------\t-------")
	limit := len(rollups)
	if topN > 0 && topN < limit {
		limit = topN
	}
	for _, r := range rollups[:limit] {
		marker := ""
		if r.AnyUnpriced {
			marker = " *"
		}
		fmt.Fprintf(w, "%s%s\t%d\t%.2f\t%.0f\t$%.4f\t$%.2f\n",
			r.Namespace, marker,
			r.Pods, r.CPURequestC, r.MemRequestMB,
			r.HourlyUSD, r.MonthlyUSD,
		)
	}
	w.Flush()
}

func printNodeRollup(out io.Writer, report *cost.WorkloadCostReport) {
	// Roll pods up by node so operators can see allocated-share-of-node.
	type nodeAgg struct {
		hourly        float64
		cpuRequestC   float64
		memRequestMB  float64
		dominantShare float64
		pods          int
	}
	byNode := map[string]*nodeAgg{}
	for _, p := range report.Pods {
		n, ok := byNode[p.Node]
		if !ok {
			n = &nodeAgg{}
			byNode[p.Node] = n
		}
		n.hourly += p.HourlyUSD
		n.cpuRequestC += p.CPURequestC
		n.memRequestMB += p.MemRequestMB
		n.dominantShare += p.DominantShare
		n.pods++
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tINSTANCE\tPRICED\tPODS\tALLOC CPU\tALLOC MEM\tREQUESTED CPU\tREQUESTED MEM\tBOOKED %\tNODE $/HR\tATTRIBUTED $/HR")
	fmt.Fprintln(w, "----\t--------\t------\t----\t---------\t---------\t-------------\t-------------\t--------\t---------\t---------------")
	for _, n := range report.Nodes {
		agg := byNode[n.Name]
		if agg == nil {
			agg = &nodeAgg{}
		}
		priced := "no"
		if n.PriceKnown {
			priced = "yes"
		}
		bookedShare := 0.0
		if n.AllocCPU > 0 || n.AllocMemMB > 0 {
			bookedShare = agg.dominantShare
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%.2f\t%.0fMi\t%.2f\t%.0fMi\t%.1f%%\t$%.4f\t$%.4f\n",
			truncate(n.Name, 40),
			n.InstanceType,
			priced,
			agg.pods,
			n.AllocCPU, n.AllocMemMB,
			agg.cpuRequestC, agg.memRequestMB,
			bookedShare*100,
			n.HourlyUSD,
			agg.hourly,
		)
	}
	w.Flush()
}

func formatUSD(v float64, known bool) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("$%.4f", v)
}
