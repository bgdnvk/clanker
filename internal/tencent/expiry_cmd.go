package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// buildExpiryCmd registers `clanker tencent expiry` — the cron-facing alert
// for prepaid resources about to expire. The shared --region flag from the
// parent command sets the default region scope when --regions is omitted.
//
// Exit codes are designed for cron pipelines:
//
//	0 — nothing flagged (cron MAILTO stays quiet)
//	1 — one or more items inside the threshold (paper trail)
//	2 — one or more items already past expiry (escalate)
func buildExpiryCmd(defaultRegion *string) *cobra.Command {
	var (
		regionsFlag string
		threshold   int
		manualOnly  bool
		includeSSL  bool
		format      string
	)

	cmd := &cobra.Command{
		Use:   "expiry",
		Short: "Report PREPAID resources approaching their renewal deadline",
		Long: `Walks every PREPAID-capable resource type across the requested regions
(CVM, Lighthouse, CBS, MySQL, Postgres, Redis, MongoDB, CynosDB, CLB,
AntiDDoS — and SSL with --include-ssl) and reports anything within
--threshold days of expiry.

Designed for cron / GitHub Actions: --format json emits a stable shape,
and exit code 1 (flagged) / 2 (already expired) lets a wrapper script
or cron MAILTO surface the alert without parsing output.

Examples:
  # 30-day window, manual-renew only, configured region
  clanker tencent expiry

  # 14-day window across two regions, JSON for a script
  clanker tencent expiry --regions=ap-singapore,ap-jakarta --threshold=14 --format=json

  # Daily cron line — only alerts when something needs attention
  0 9 * * * /usr/local/bin/clanker tencent expiry --threshold=14 || mail -s "tencent renewals" me@example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			creds := ResolveCredentials()
			if defaultRegion != nil && *defaultRegion != "" {
				creds.Region = *defaultRegion
			}
			client, err := NewClient(creds, viper.GetBool("debug"))
			if err != nil {
				return err
			}

			var regions []string
			for _, r := range strings.Split(regionsFlag, ",") {
				if r = strings.TrimSpace(r); r != "" {
					regions = append(regions, r)
				}
			}

			report, err := client.BuildExpiryReport(context.Background(), ExpiryReportOptions{
				Regions:       regions,
				ThresholdDays: threshold,
				ManualOnly:    manualOnly,
				IncludeSSL:    includeSSL,
			})
			if err != nil {
				return err
			}

			switch strings.ToLower(format) {
			case "json":
				if err := writeExpiryJSON(report); err != nil {
					return err
				}
			default:
				writeExpiryTable(report)
			}

			// Exit-code semantics: expired beats merely-flagged.
			if report.Counts.Expired > 0 {
				os.Exit(2)
			}
			if len(report.Items) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&regionsFlag, "regions", "", "Comma-separated regions to scan (defaults to the configured region; pass two or more for multi-region cron)")
	cmd.Flags().IntVar(&threshold, "threshold", 30, "Flag items this many days from expiry or closer")
	cmd.Flags().BoolVar(&manualOnly, "manual-only", true, "Only flag items with auto_renew=false; auto-renewing ones are counted but not listed")
	cmd.Flags().BoolVar(&includeSSL, "include-ssl", false, "Include SSL certificate validity (different signal from subscription expiry but useful for cron coverage)")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table | json")
	return cmd
}

func writeExpiryJSON(report *ExpiryReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func writeExpiryTable(report *ExpiryReport) {
	regions := strings.Join(report.Regions, ",")
	fmt.Printf("Tencent Cloud renewal scan (regions=%s, threshold=%dd, manual_only=%t)\n",
		regions, report.ThresholdDays, report.ManualOnly)
	fmt.Printf("Scanned %d PREPAID resources — %d flagged (%d already expired, %d auto-renewing).\n\n",
		report.Counts.Total, report.Counts.Flagged, report.Counts.Expired, report.Counts.AutoRenew)

	if len(report.Items) == 0 {
		fmt.Println("  Nothing to alert on.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tREGION\tID\tNAME\tDAYS\tEXPIRES\tAUTO_RENEW\tSTATE")
	for _, it := range report.Items {
		ar := "—"
		if it.AutoRenew != nil {
			ar = strconv.FormatBool(*it.AutoRenew)
		}
		flag := strconv.Itoa(it.DaysLeft)
		if it.DaysLeft < 0 {
			flag = "EXPIRED"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			it.Type, it.Region, it.ID, it.Name, flag, it.ExpiresAt, ar, it.State)
	}
	_ = tw.Flush()
}
