package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/sre"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	healthOutput     string
	healthKubeconfig string
	healthContext    string
	healthSeverity   string
)

var k8sHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Score cluster health and list per-resource issues",
	Long: `Run a cluster-wide health check that aggregates node, workload, storage,
and network signals. Surfaces concrete issues (CrashLoopBackOff, OOMKilled,
restart-rate spikes, NotReady nodes, …) with severity and remediation
suggestions.

The check is read-only — it issues kubectl get/describe commands; nothing is
modified.

Examples:
  clanker k8s health
  clanker k8s health --severity critical
  clanker k8s health -o json
  clanker k8s health --kubeconfig ~/.kube/prod`,
	RunE: runK8sHealth,
}

func init() {
	k8sCmd.AddCommand(k8sHealthCmd)
	k8sHealthCmd.Flags().StringVarP(&healthOutput, "output", "o", "table", "Output format (table, json)")
	k8sHealthCmd.Flags().StringVar(&healthKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sHealthCmd.Flags().StringVar(&healthContext, "context", "", "kubectl context to use")
	k8sHealthCmd.Flags().StringVar(&healthSeverity, "severity", "", "Minimum severity to surface (info, warning, critical) — default shows all")
}

func runK8sHealth(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(healthKubeconfig, healthContext, debug)
	checker := sre.NewHealthChecker(k8s.NewSREAdapter(client), debug)

	summary, issues, err := checker.CheckClusterDetailed(ctx)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	filtered := filterIssuesBySeverity(issues, healthSeverity)

	switch strings.ToLower(healthOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Summary *sre.ClusterHealthSummary `json:"summary"`
			Issues  []sre.Issue               `json:"issues"`
		}{Summary: summary, Issues: filtered})
	default:
		printHealthSummary(os.Stdout, summary, filtered)
		return nil
	}
}

// filterIssuesBySeverity drops issues below the requested floor. Empty
// (default) returns all. Unknown values are treated as "show everything"
// so a typo doesn't silently hide critical issues.
func filterIssuesBySeverity(issues []sre.Issue, floor string) []sre.Issue {
	floor = strings.ToLower(strings.TrimSpace(floor))
	if floor == "" {
		return issues
	}
	rank := map[string]int{"info": 0, "warning": 1, "critical": 2}
	cut, ok := rank[floor]
	if !ok {
		return issues
	}
	out := make([]sre.Issue, 0, len(issues))
	for _, i := range issues {
		if rank[strings.ToLower(string(i.Severity))] >= cut {
			out = append(out, i)
		}
	}
	return out
}

func printHealthSummary(out io.Writer, summary *sre.ClusterHealthSummary, issues []sre.Issue) {
	if summary == nil {
		fmt.Fprintln(out, "No health summary returned.")
		return
	}

	fmt.Fprintf(out, "Cluster health: %s (score %d/100)\n", strings.ToUpper(summary.OverallHealth), summary.Score)
	fmt.Fprintf(out, "  critical issues: %d   warning issues: %d\n\n", summary.CriticalIssues, summary.WarningIssues)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "COMPONENT\tSTATUS\tSCORE")
	fmt.Fprintln(w, "---------\t------\t-----")
	fmt.Fprintf(w, "nodes\t%s\t%d\n", summary.NodeHealth.Status, summary.NodeHealth.Score)
	fmt.Fprintf(w, "workloads\t%s\t%d\n", summary.WorkloadHealth.Status, summary.WorkloadHealth.Score)
	fmt.Fprintf(w, "storage\t%s\t%d\n", summary.StorageHealth.Status, summary.StorageHealth.Score)
	fmt.Fprintf(w, "network\t%s\t%d\n", summary.NetworkHealth.Status, summary.NetworkHealth.Score)
	w.Flush()

	fmt.Fprintf(out, "\nPods: %d total | %d running | %d pending | %d failed\n",
		summary.TotalPods, summary.RunningPods, summary.PendingPods, summary.FailedPods)

	if len(issues) == 0 {
		fmt.Fprintln(out, "\nNo issues detected at the requested severity floor. ✓")
		return
	}

	// Sort issues critical first, then warning, then info.
	sort.SliceStable(issues, func(i, j int) bool {
		return severityRank(issues[i].Severity) > severityRank(issues[j].Severity)
	})

	fmt.Fprintf(out, "\n%d issue(s):\n", len(issues))
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tRESOURCE\tNAMESPACE\tCATEGORY\tMESSAGE")
	fmt.Fprintln(tw, "--------\t--------\t---------\t--------\t-------")
	for _, i := range issues {
		ns := i.Namespace
		if ns == "" {
			ns = "-"
		}
		fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%s\t%s\n",
			strings.ToUpper(string(i.Severity)),
			i.ResourceType, i.ResourceName,
			ns,
			i.Category,
			truncate(i.Message, 80),
		)
	}
	tw.Flush()
}

func severityRank(s sre.IssueSeverity) int {
	switch strings.ToLower(string(s)) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

// truncate caps a string at n runes, replacing the tail with "…" when it
// overflows. Operates on runes (not bytes) so the result has predictable
// display width regardless of UTF-8 content.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
