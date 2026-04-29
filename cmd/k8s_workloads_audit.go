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
	"github.com/bgdnvk/clanker/internal/k8s/sre"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	workloadsAuditOutput     string
	workloadsAuditKubeconfig string
	workloadsAuditContext    string
	workloadsAuditSeverity   string
)

var k8sWorkloadsCmd = &cobra.Command{
	Use:   "workloads",
	Short: "Inspect Kubernetes workload posture",
	Long:  `Inspect or audit pod / deployment / node health signals cluster-wide.`,
}

var k8sWorkloadsAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Roll cluster-wide workload health issues into a categorised report",
	Long: `Surface what's broken in the cluster in one shot:

  • CrashLoopBackOff containers
  • OOMKilled containers
  • ImagePullBackOff / ErrImagePull
  • Pods with restart spikes (≥5 restarts)
  • Pods stuck NotReady
  • Nodes under pressure (Memory/Disk/PID/NetworkUnavailable)

Read-only — only kubectl get is invoked.

Examples:
  clanker k8s workloads audit
  clanker k8s workloads audit -o json
  clanker k8s workloads audit --severity warning`,
	RunE: runWorkloadsAudit,
}

func init() {
	k8sCmd.AddCommand(k8sWorkloadsCmd)
	k8sWorkloadsCmd.AddCommand(k8sWorkloadsAuditCmd)
	k8sWorkloadsCmd.PersistentFlags().StringVarP(&workloadsAuditOutput, "output", "o", "table", "Output format (table, json)")
	k8sWorkloadsCmd.PersistentFlags().StringVar(&workloadsAuditKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sWorkloadsCmd.PersistentFlags().StringVar(&workloadsAuditContext, "context", "", "kubectl context to use")
	k8sWorkloadsAuditCmd.Flags().StringVar(&workloadsAuditSeverity, "severity", "", "Minimum severity to surface in Issues (info, warning, critical) — default shows all")
}

func runWorkloadsAudit(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(workloadsAuditKubeconfig, workloadsAuditContext, debug)
	auditor := sre.NewWorkloadHealthAuditor(k8s.NewSREAdapter(client), debug)

	report, err := auditor.Audit(ctx)
	if err != nil {
		return fmt.Errorf("workload audit failed: %w", err)
	}

	report.Issues = filterIssuesBySeverity(report.Issues, workloadsAuditSeverity)

	switch strings.ToLower(workloadsAuditOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printWorkloadsAuditReport(os.Stdout, report)
		return nil
	}
}

// filterIssuesBySeverity is reused from cmd/k8s_health.go.

func printWorkloadsAuditReport(out io.Writer, report *sre.WorkloadHealthReport) {
	if report == nil {
		fmt.Fprintln(out, "No workload health report.")
		return
	}

	fmt.Fprintf(out, "Total issues: %d   (critical %d, warning %d, info %d)\n",
		report.TotalIssues, report.Critical, report.Warning, report.Info)
	if report.TotalIssues == 0 {
		fmt.Fprintln(out, "Cluster is healthy. ✓")
		return
	}

	fmt.Fprintln(out, "\nBy category:")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CATEGORY\tCOUNT")
	fmt.Fprintln(w, "--------\t-----")
	for _, c := range report.ByCategory {
		fmt.Fprintf(w, "%s\t%d\n", c.Category, c.Count)
	}
	w.Flush()

	if len(report.HotPods) > 0 {
		fmt.Fprintln(out, "\nHot pods (most issues):")
		hw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(hw, "NAMESPACE/POD\tISSUES\tCATEGORIES")
		fmt.Fprintln(hw, "-------------\t------\t----------")
		for _, p := range report.HotPods {
			cats := make([]string, len(p.Categories))
			for i, c := range p.Categories {
				cats[i] = string(c)
			}
			fmt.Fprintf(hw, "%s/%s\t%d\t%s\n",
				p.Namespace, p.Pod, p.Issues, strings.Join(cats, ", "))
		}
		hw.Flush()
	}

	if len(report.Issues) > 0 && len(report.Issues) <= 50 {
		fmt.Fprintf(out, "\n%d issue(s):\n", len(report.Issues))
		iw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(iw, "SEVERITY\tKIND\tNAMESPACE/NAME\tMESSAGE")
		fmt.Fprintln(iw, "--------\t----\t--------------\t-------")
		for _, i := range report.Issues {
			ns := i.Namespace
			if ns == "" {
				ns = "-"
			}
			fmt.Fprintf(iw, "%s\t%s\t%s/%s\t%s\n",
				strings.ToUpper(string(i.Severity)),
				i.ResourceType,
				ns, i.ResourceName,
				truncate(i.Message, 80),
			)
		}
		iw.Flush()
	} else if len(report.Issues) > 50 {
		fmt.Fprintf(out, "\n%d issue(s) — pass -o json for full list.\n", len(report.Issues))
	}
}
