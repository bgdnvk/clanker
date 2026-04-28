package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/sre"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	autoscalerOutput     string
	autoscalerKubeconfig string
	autoscalerContext    string
	autoscalerLookback   string
	autoscalerMaxEvents  int
)

var k8sAutoscalerCmd = &cobra.Command{
	Use:   "autoscaler",
	Short: "Analyze cluster-autoscaler / Karpenter scaling behaviour",
	Long: `Analyze recent kube-system events to spot scaling waste — pods stuck
pending because no node fit, scale-ups that didn't happen, scale-downs that
pulled back too quickly.

Read-only: only kubectl get/describe is invoked.`,
}

var k8sAutoscalerAnalyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Detect autoscaler type and surface scaling waste signals",
	Long: `Detect whether cluster-autoscaler or Karpenter is running, then
classify recent events into useful categories: FailedScheduling,
NotTriggerScaleUp, TriggeredScaleUp, ScaleDown[Empty|Unneeded], NodeNotReady.

Examples:
  clanker k8s autoscaler analyze
  clanker k8s autoscaler analyze --lookback 6h
  clanker k8s autoscaler analyze -o json`,
	RunE: runAutoscalerAnalyze,
}

func init() {
	k8sCmd.AddCommand(k8sAutoscalerCmd)
	k8sAutoscalerCmd.AddCommand(k8sAutoscalerAnalyzeCmd)

	k8sAutoscalerCmd.PersistentFlags().StringVarP(&autoscalerOutput, "output", "o", "table", "Output format (table, json)")
	k8sAutoscalerCmd.PersistentFlags().StringVar(&autoscalerKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sAutoscalerCmd.PersistentFlags().StringVar(&autoscalerContext, "context", "", "kubectl context to use")
	k8sAutoscalerAnalyzeCmd.Flags().StringVar(&autoscalerLookback, "lookback", "1h", "Lookback window for event analysis (e.g. 30m, 6h, 24h)")
	k8sAutoscalerAnalyzeCmd.Flags().IntVar(&autoscalerMaxEvents, "max-events", 5000, "Maximum events to classify (most-recent kept). 0 disables the cap.")
}

func runAutoscalerAnalyze(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	lookback, err := time.ParseDuration(autoscalerLookback)
	if err != nil {
		return fmt.Errorf("invalid --lookback %q: %w", autoscalerLookback, err)
	}

	client := k8s.NewClient(autoscalerKubeconfig, autoscalerContext, debug)
	analyzer := sre.NewAutoscalerAnalyzer(k8s.NewSREAdapter(client), debug)
	analyzer.MaxEvents = autoscalerMaxEvents

	report, err := analyzer.AnalyzeScalingWaste(ctx, lookback)
	if err != nil {
		return fmt.Errorf("autoscaler analysis failed: %w", err)
	}

	switch strings.ToLower(autoscalerOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printAutoscalerReport(os.Stdout, report)
		return nil
	}
}

func printAutoscalerReport(out io.Writer, report *sre.ScalingWasteReport) {
	if report == nil {
		fmt.Fprintln(out, "No autoscaler report.")
		return
	}

	fmt.Fprintf(out, "Autoscaler: %s   (CA seen: %v, Karpenter present: %v)\n",
		report.Inventory.Type, report.Inventory.ClusterAutoscalerSeen, report.Inventory.KarpenterPresent)
	if report.Inventory.Notes != "" {
		fmt.Fprintf(out, "  note: %s\n", report.Inventory.Notes)
	}
	fmt.Fprintf(out, "Lookback: %s   Generated: %s\n", report.LookbackWindow, report.GeneratedAt.Format(time.RFC3339))
	if report.EventsTruncated {
		fmt.Fprintf(out, "⚠  Event stream truncated to %d most-recent entries (some history dropped). Raise --max-events for full coverage.\n",
			report.EventsProcessed)
	} else {
		fmt.Fprintf(out, "Events processed: %d\n", report.EventsProcessed)
	}
	fmt.Fprintln(out)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SIGNAL\tCOUNT")
	fmt.Fprintln(w, "------\t-----")
	fmt.Fprintf(w, "FailedScheduling\t%d\n", report.FailedScheduling)
	fmt.Fprintf(w, "NotTriggerScaleUp\t%d\n", report.NotTriggerScaleUp)
	fmt.Fprintf(w, "TriggeredScaleUp\t%d\n", report.TriggeredScaleUp)
	fmt.Fprintf(w, "ScaleDownEmpty\t%d\n", report.ScaleDownEmpty)
	fmt.Fprintf(w, "ScaleDownUnneeded\t%d\n", report.ScaleDownUnneeded)
	fmt.Fprintf(w, "NodeNotReady\t%d\n", report.NodeNotReadyEvents)
	w.Flush()

	if total := report.FailedScheduling + report.NotTriggerScaleUp; total > 0 {
		fmt.Fprintf(out, "\n⚠  %d pod-side scaling waste events in the last %s\n", total, report.LookbackWindow)
	}

	if len(report.TopFailingPods) > 0 {
		fmt.Fprintln(out, "\nTop pods stuck waiting for capacity:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAMESPACE\tPOD\tFAILED-SCHED\tNOT-SCALE-UP\tLAST REASON")
		fmt.Fprintln(tw, "---------\t---\t------------\t-------------\t-----------")
		for _, p := range report.TopFailingPods {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n",
				orDash(p.Namespace), p.Name,
				p.FailedSchedCount, p.NotScaleUpCount,
				orDash(p.LastReason),
			)
		}
		tw.Flush()
	}

	if len(report.HotNodeReasons) > 0 {
		fmt.Fprintln(out, "\nHot node reasons:")
		for _, r := range report.HotNodeReasons {
			fmt.Fprintf(out, "  %s × %d   %s\n", r.Reason, r.Count, truncate(r.SampleMessage, 80))
		}
	}
}
