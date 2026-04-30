package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/bgdnvk/clanker/internal/cost"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	costSavingsLookback string
	costSavingsTerm     string
)

var costSavingsCmd = &cobra.Command{
	Use:   "savings",
	Short: "AWS Savings Plan + Reserved Instance purchase recommendations",
	Long: `Pull commitment-purchase recommendations from AWS Cost Explorer:

  • Savings Plans (Compute / EC2 Instance / SageMaker)
  • Reserved Instances (EC2 / RDS / ElastiCache / OpenSearch / Redshift)

Both shapes are merged into one ranked list so the highest-savings
commitment wins regardless of type. Read-only — no purchases are made.

Recommendations require usage history; brand-new accounts produce an
empty list with a note rather than an error.

Examples:
  clanker cost savings                            # 1y term, 60d lookback
  clanker cost savings --term 3                   # 3y term
  clanker cost savings --lookback 30 -o json
  clanker cost savings --profile prod`,
	RunE: runCostSavings,
}

func init() {
	costCmd.AddCommand(costSavingsCmd)
	costSavingsCmd.Flags().StringVar(&costSavingsLookback, "lookback", "60", "Lookback window in days (7, 30, 60)")
	costSavingsCmd.Flags().StringVar(&costSavingsTerm, "term", "1", "Commitment term in years (1 or 3)")
}

func runCostSavings(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	awsProfile := costProfile
	if awsProfile == "" {
		awsProfile = os.Getenv("AWS_PROFILE")
	}

	provider, err := cost.NewAWSProvider(ctx, awsProfile, debug)
	if err != nil {
		return fmt.Errorf("AWS provider unavailable: %w", err)
	}

	report, err := provider.GetSavingsRecommendations(ctx, costSavingsLookback, costSavingsTerm)
	if err != nil {
		return fmt.Errorf("savings recommendations failed: %w", err)
	}

	switch strings.ToLower(costFormat) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printSavingsReport(os.Stdout, report, costTop)
		return nil
	}
}

func printSavingsReport(out io.Writer, report *cost.SavingsReport, topN int) {
	if report == nil {
		fmt.Fprintln(out, "No savings report.")
		return
	}

	fmt.Fprintf(out, "AWS savings recommendations — term=%s, lookback=%s\n",
		report.Term, report.Lookback)
	fmt.Fprintf(out, "Estimated total monthly savings: $%.2f\n", report.TotalEstimatedSavings)
	if report.Notes != "" {
		fmt.Fprintf(out, "Notes: %s\n", report.Notes)
	}
	fmt.Fprintln(out)

	if len(report.Recommendations) == 0 {
		fmt.Fprintln(out, "No commitment recommendations available.")
		return
	}

	limit := len(report.Recommendations)
	if topN > 0 && topN < limit {
		limit = topN
	}

	fmt.Fprintf(out, "%d recommendation(s):\n", len(report.Recommendations))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tSERVICE/FAMILY\tHOURLY\tUPFRONT\tSAVINGS/MO\tSAVINGS %\tBREAKEVEN")
	fmt.Fprintln(w, "----\t--------------\t------\t-------\t----------\t---------\t---------")
	for _, r := range report.Recommendations[:limit] {
		label := r.Service
		if label == "" {
			label = r.Family
		} else if r.Family != "" {
			label = label + " (" + r.Family + ")"
		}
		breakeven := ""
		if r.BreakevenMonths > 0 {
			breakeven = fmt.Sprintf("%.1f mo", r.BreakevenMonths)
		} else {
			breakeven = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t$%.4f\t$%.2f\t$%.2f\t%.1f%%\t%s\n",
			kindLabel(r.Kind),
			truncate(label, 30),
			r.HourlyCommitment,
			r.UpfrontCost,
			r.EstimatedSavings,
			r.EstimatedSavingsPc,
			breakeven,
		)
	}
	w.Flush()

	if topN > 0 && len(report.Recommendations) > topN {
		fmt.Fprintf(out, "\n(showing top %d of %d — pass --top 0 for all)\n", topN, len(report.Recommendations))
	}

	fmt.Fprintln(out, "\nDetails:")
	for _, r := range report.Recommendations[:limit] {
		fmt.Fprintf(out, "  • [%s] %s — $%.2f/mo savings (%.1f%%)\n",
			kindLabel(r.Kind), r.Family, r.EstimatedSavings, r.EstimatedSavingsPc)
		if r.Detail != "" {
			fmt.Fprintf(out, "    %s\n", r.Detail)
		}
	}
}

func kindLabel(k cost.SavingsKind) string {
	switch k {
	case cost.SavingsKindSavingsPlan:
		return "SP"
	case cost.SavingsKindReservedInstance:
		return "RI"
	}
	return string(k)
}
