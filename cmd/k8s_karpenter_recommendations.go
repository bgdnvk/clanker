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

var k8sAutoscalerRecsCmd = &cobra.Command{
	Use:   "recommendations",
	Short: "Karpenter NodePool / NodeClaim recommendations",
	Long: `Analyse Karpenter NodePools and NodeClaims for cost / reliability issues:

  • NodePool with no consolidation policy (idle nodes never deprovisioned)
  • NodePool with no spec.limits (unbounded growth → runaway cost)
  • Stale NodePools (>7d old, never provisioned)
  • NodeClaims stuck not-Ready (provisioner / quota issue)
  • Multiple unweighted NodePools (Karpenter picks arbitrarily)

Read-only — only kubectl get is invoked.

Examples:
  clanker k8s autoscaler recommendations
  clanker k8s autoscaler recommendations -o json`,
	RunE: runKarpenterRecs,
}

func init() {
	k8sAutoscalerCmd.AddCommand(k8sAutoscalerRecsCmd)
	// --output / --kubeconfig / --context come from k8sAutoscalerCmd's
	// PersistentFlags, defined alongside `validate`.
}

func runKarpenterRecs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(autoscalerKubeconfig, autoscalerContext, debug)
	advisor := sre.NewKarpenterAdvisor(k8s.NewSREAdapter(client), debug)

	report, err := advisor.Advise(ctx)
	if err != nil {
		return fmt.Errorf("karpenter advisor failed: %w", err)
	}

	switch strings.ToLower(autoscalerOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printKarpenterRecsReport(os.Stdout, report)
		return nil
	}
}

func printKarpenterRecsReport(out io.Writer, report *sre.KarpenterAdvisorReport) {
	if report == nil {
		fmt.Fprintln(out, "No advisor report.")
		return
	}
	if !report.Installed {
		fmt.Fprintln(out, "Karpenter not installed in this cluster.")
		if report.Notes != "" {
			fmt.Fprintf(out, "Notes: %s\n", report.Notes)
		}
		return
	}

	fmt.Fprintf(out, "Karpenter detected — %d NodePool(s), %d NodeClaim(s)\n",
		report.NodePools, report.NodeClaims)
	if report.Notes != "" {
		fmt.Fprintf(out, "Notes: %s\n", report.Notes)
	}
	if len(report.Recommendations) == 0 {
		fmt.Fprintln(out, "\nNo recommendations — Karpenter looks healthy. ✓")
		return
	}

	fmt.Fprintf(out, "\n%d recommendation(s):\n", len(report.Recommendations))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEVERITY\tRESOURCE\tNAME\tISSUE")
	fmt.Fprintln(w, "--------\t--------\t----\t-----")
	for _, r := range report.Recommendations {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			strings.ToUpper(string(r.Severity)),
			r.Resource,
			truncate(r.Name, 40),
			truncate(r.Issue, 60),
		)
	}
	w.Flush()

	fmt.Fprintln(out, "\nDetails:")
	for _, r := range report.Recommendations {
		fmt.Fprintf(out, "  • [%s] %s/%s — %s\n", r.Severity, r.Resource, r.Name, r.Issue)
		if r.Detail != "" {
			fmt.Fprintf(out, "    %s\n", r.Detail)
		}
		if r.Suggestion != "" {
			fmt.Fprintf(out, "    → %s\n", r.Suggestion)
		}
	}
}
