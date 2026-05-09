package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/cost"
	"github.com/bgdnvk/clanker/internal/maker"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	scanQuick      bool
	scanFormat     string
	scanExportPath string
	scanFix        string
	scanTop        int
	scanLookback   string
	scanTerm       string
	scanProfile    string
	scanNoColor    bool
	scanBackendURL string
	scanLocal      bool
)

// scanCmd is the new top-level "scan everything for cost waste"
// command. By design it is *richer* than `clanker cost savings`:
//
//   - When the clanker-cloud desktop backend is running on
//     localhost:8080..8084, the CLI calls /api/cost/scan and gets the
//     full operational-detector receipt (idle NAT, EOL'd EKS, stopped
//     EC2 with EBS, oversized RDS, untagged spend, cross-cloud
//     coverage where configured).
//   - When the backend is unreachable, the CLI falls back to its own
//     AWS Cost Explorer commitment-recommendation surface — a degraded
//     but still useful receipt focused on Savings Plans + RIs.
//
// The output is a coloured terminal "receipt" with category rollup,
// sorted findings, and per-row actions. --export and --fix turn the
// receipt into a Markdown/JSON file or a maker plan JSON respectively.
var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan every configured cloud for cost waste, produce a receipt",
	Long: `Scan every configured cloud account for actionable cost-savings opportunities
and print a colour-coded receipt summarising the waste found.

Sources composed (when available):

  • Operational detectors via the local clanker-cloud backend
    (idle NAT gateways, stopped EC2 with EBS, EKS extended-support,
    GP2→GP3 upgrades, orphan ELBs, GKE extended-support, CUDs,
    Reserved Instances, stale Cloudflare workers, …)
  • CLI commitment recommendations from AWS Cost Explorer
    (Savings Plans + Reserved Instances)
  • Cost summary, anomalies, and LLM-spend context (deep mode only)

Modes:

  --quick        Run the in-process detectors only. ~5–10s. Default.
  (default)      Deep scan — adds commitment recs + anomalies + LLM
                 context. ~15–30s.

Output formats:

  --format terminal   Coloured receipt (default).
  --format json       Machine-readable JSON.
  --format markdown   Markdown export.

Actions:

  --export <path>     Write the receipt to disk in --format shape.
  --fix <path>        Write a maker plan JSON ready for inspection
                      (and a future ` + "`clanker maker apply`" + `).

Examples:

  clanker scan                                    # full scan, coloured terminal
  clanker scan --quick                            # fast scan, detectors only
  clanker scan --format json                      # JSON to stdout
  clanker scan --export receipt.md --format markdown
  clanker scan --fix waste-fix-plan.json
  clanker scan --profile prod --quick`,
	RunE: runScan,
}

func init() {
	rootCmd.AddCommand(scanCmd)

	scanCmd.Flags().BoolVar(&scanQuick, "quick", false, "Quick scan (in-process detectors only). Skips commitment recs + anomalies + LLM spend.")
	scanCmd.Flags().StringVar(&scanFormat, "format", "terminal", "Output format: terminal, json, markdown")
	scanCmd.Flags().StringVar(&scanExportPath, "export", "", "Write the receipt to a file (uses --format)")
	scanCmd.Flags().StringVar(&scanFix, "fix", "", "Write a maker plan JSON to <path> with one command per finding")
	scanCmd.Flags().IntVar(&scanTop, "top", 20, "Limit findings to top N by monthly waste (0 = all)")
	scanCmd.Flags().StringVar(&scanLookback, "lookback", "60", "Commitment recommendation lookback (deep mode)")
	scanCmd.Flags().StringVar(&scanTerm, "term", "1", "Commitment recommendation term in years (deep mode)")
	scanCmd.Flags().StringVar(&scanProfile, "profile", "", "AWS profile to use (default: AWS_PROFILE env)")
	scanCmd.Flags().BoolVar(&scanNoColor, "no-color", false, "Disable ANSI colour output")
	scanCmd.Flags().StringVar(&scanBackendURL, "backend", "", "Override clanker-cloud backend URL (default: auto-discover localhost:8080-8084)")
	scanCmd.Flags().BoolVar(&scanLocal, "local", false, "Skip backend discovery; use local CLI commitment scan only")
}

func runScan(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	debug := viper.GetBool("debug")

	awsProfile := scanProfile
	if awsProfile == "" {
		awsProfile = os.Getenv("AWS_PROFILE")
	}

	mode := "deep"
	if scanQuick {
		mode = "quick"
	}

	useColor := !scanNoColor && shouldUseColor()
	started := time.Now()

	receipt, source, err := obtainScanReceipt(ctx, mode, awsProfile, debug)
	if err != nil {
		return err
	}
	// Source isn't user-visible in --format json (machine-readable
	// stays clean), but for --format terminal we surface it as a note
	// so users know they're seeing the degraded view when the backend
	// isn't running.
	if source == "local-fallback" && receipt != nil {
		if receipt.Notes == "" {
			receipt.Notes = "no clanker-cloud backend reachable; commitment recs only (run --local to silence)"
		}
	}

	switch strings.ToLower(scanFormat) {
	case "json":
		return emitScanReceipt(receipt, "json", scanExportPath)
	case "markdown", "md":
		return emitScanReceipt(receipt, "markdown", scanExportPath)
	case "terminal", "":
		out := cost.RenderScanReceipt(receipt, useColor, scanTop)
		if scanExportPath != "" {
			if err := os.WriteFile(scanExportPath, []byte(stripANSI(out)), 0644); err != nil {
				return fmt.Errorf("write export: %w", err)
			}
			fmt.Printf("Receipt written to %s\n", scanExportPath)
		} else {
			fmt.Print(out)
		}
	default:
		return fmt.Errorf("unsupported --format %q (expected terminal, json, or markdown)", scanFormat)
	}

	if scanFix != "" {
		path, err := writeFixPlan(receipt, scanFix, awsProfile, started)
		if err != nil {
			return fmt.Errorf("write fix plan: %w", err)
		}
		fmt.Printf("\nFix plan written to %s\n", path)
		fmt.Printf("Inspect with: clanker maker estimate --plan %s\n", path)
	}
	return nil
}

// obtainScanReceipt picks the right source — backend-first with a
// local commitment-only fallback. Returns the receipt, the source
// label ("backend" or "local-fallback"), and any error.
func obtainScanReceipt(ctx context.Context, mode, awsProfile string, debug bool) (*cost.ScanReceipt, string, error) {
	if !scanLocal {
		base := scanBackendURL
		if base == "" {
			base = cost.DiscoverScanBackend(ctx, debug)
		}
		if base != "" {
			client := cost.NewClient(base, debug)
			receipt, err := client.FetchScanReceipt(ctx, mode, awsProfile, scanLookback, scanTerm)
			if err == nil {
				return receipt, "backend", nil
			}
			if debug {
				fmt.Printf("[scan] backend at %s failed (%v); falling back to local commitments\n", base, err)
			}
			// Fall through to local fallback — backend was reachable
			// but errored, which we treat as a soft failure.
		} else if debug {
			fmt.Println("[scan] no clanker-cloud backend detected on localhost:8080-8084")
		}
	}

	// Local fallback: AWS Cost Explorer commitments only.
	if awsProfile == "" {
		awsProfile = "default"
	}
	provider, err := cost.NewAWSProvider(ctx, awsProfile, debug)
	if err != nil {
		return nil, "", fmt.Errorf("AWS provider unavailable: %w", err)
	}
	report, err := provider.GetSavingsRecommendations(ctx, scanLookback, scanTerm)
	if err != nil {
		return nil, "", fmt.Errorf("local scan: %w", err)
	}
	receipt := cost.ProjectSavingsToReceipt(report, mode, time.Now().UTC())
	return receipt, "local-fallback", nil
}

// emitScanReceipt writes JSON or Markdown to either stdout or the
// --export path. Terminal format is handled separately because it
// needs colour control.
func emitScanReceipt(receipt *cost.ScanReceipt, format, exportPath string) error {
	var (
		out []byte
		err error
	)
	switch format {
	case "json":
		out, err = cost.RenderScanReceiptJSON(receipt)
		if err != nil {
			return err
		}
	case "markdown":
		out = []byte(cost.RenderScanReceiptMarkdown(receipt))
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
	if exportPath != "" {
		if err := os.WriteFile(exportPath, out, 0644); err != nil {
			return err
		}
		fmt.Printf("Receipt written to %s\n", exportPath)
		return nil
	}
	_, err = os.Stdout.Write(append(out, '\n'))
	return err
}

// writeFixPlan converts the receipt's findings into a maker plan
// stub. Each finding becomes one Command{Args: ...} placeholder so
// the user can inspect, edit, and apply via the existing maker
// pipeline. The placeholder commands are intentionally ECHO-style
// rather than real cloud-modifying commands — applying a destructive
// fix without explicit user review is the kind of thing the FinOps
// review explicitly flagged in the workshop. The user runs `maker
// apply` themselves once they've reviewed.
func writeFixPlan(receipt *cost.ScanReceipt, path, awsProfile string, started time.Time) (string, error) {
	if receipt == nil {
		return "", errors.New("no receipt to project")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	commands := make([]maker.Command, 0, len(receipt.Findings))
	for _, f := range receipt.Findings {
		args := buildFixCommandArgs(f, awsProfile)
		if args == nil {
			continue
		}
		commands = append(commands, maker.Command{
			Args: args,
			Reason: fmt.Sprintf("[%s] %s — saves $%.2f/mo (severity=%s)",
				f.Category, displayName(f), f.MonthlyWasteUSD, f.Severity),
		})
	}
	plan := &maker.Plan{
		Version:   maker.CurrentPlanVersion,
		CreatedAt: started.UTC(),
		Provider:  primaryProvider(receipt),
		Question:  "Apply cost-saving fixes from clanker scan",
		Summary: fmt.Sprintf("Auto-generated from clanker scan — %d findings, $%.2f/mo total potential savings.",
			len(receipt.Findings), receipt.TotalMonthlyWasteUSD),
		Commands: commands,
		Notes: []string{
			"Review every command before applying. Some fixes are destructive (delete unused resources).",
			"Pass through `clanker maker estimate --plan <file>` to confirm cost impact.",
			"Receipts containing commitment recs do NOT auto-purchase Savings Plans / RIs — those need explicit purchase.",
		},
	}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, body, 0644); err != nil {
		return "", err
	}
	return abs, nil
}

// buildFixCommandArgs maps a finding to a placeholder CLI command.
// We deliberately keep these as ECHO/AWS-CLI dry-run shapes — the
// scan command never auto-fixes anything, it just prepares the
// command the user would run if they decided to.
func buildFixCommandArgs(f cost.ScanFinding, awsProfile string) []string {
	profileArgs := []string{}
	if awsProfile != "" {
		profileArgs = []string{"--profile", awsProfile}
	}
	switch f.Category {
	case "orphan", "rightsize":
		// Idle NAT, stopped EC2, oversized RDS, etc — represented as
		// a `aws <service> describe ...` command so the operator can
		// inspect before deciding the destructive action.
		switch strings.ToLower(f.Service) {
		case "ec2", "nat gateway":
			args := []string{"aws", "ec2", "describe-nat-gateways"}
			if f.ResourceID != "" {
				args = append(args, "--nat-gateway-ids", f.ResourceID)
			}
			args = append(args, profileArgs...)
			return args
		case "rds":
			args := []string{"aws", "rds", "describe-db-instances"}
			if f.ResourceID != "" {
				args = append(args, "--db-instance-identifier", f.ResourceID)
			}
			args = append(args, profileArgs...)
			return args
		}
	case "version-eol":
		if strings.EqualFold(f.Service, "EKS") && f.ResourceID != "" {
			args := []string{"aws", "eks", "describe-cluster", "--name", f.ResourceID}
			args = append(args, profileArgs...)
			return args
		}
	case "lifecycle":
		if strings.EqualFold(f.Service, "EC2") && f.ResourceID != "" {
			args := []string{"aws", "ec2", "describe-instances", "--instance-ids", f.ResourceID}
			args = append(args, profileArgs...)
			return args
		}
	case "commitment":
		// Commitment recs require interactive purchase — emit an echo
		// stub so the operator sees a placeholder.
		return []string{"echo", "Review this Savings Plan / RI: " + displayName(f) + " — saves $" +
			fmt.Sprintf("%.2f", f.MonthlyWasteUSD) + "/mo"}
	}
	// Fallback echo so every finding has at least an annotation.
	return []string{"echo", "Review: " + displayName(f) + " — saves $" + fmt.Sprintf("%.2f", f.MonthlyWasteUSD) + "/mo"}
}

func displayName(f cost.ScanFinding) string {
	parts := []string{}
	if f.Service != "" {
		parts = append(parts, f.Service)
	}
	if f.ResourceID != "" {
		parts = append(parts, f.ResourceID)
	}
	if f.Region != "" {
		parts = append(parts, f.Region)
	}
	if len(parts) == 0 {
		return f.Provider
	}
	return strings.Join(parts, " · ")
}

func primaryProvider(receipt *cost.ScanReceipt) string {
	if receipt == nil {
		return "aws"
	}
	if len(receipt.ProvidersScanned) > 0 {
		return receipt.ProvidersScanned[0]
	}
	if len(receipt.Findings) > 0 {
		return receipt.Findings[0].Provider
	}
	return "aws"
}

// stripANSI removes ANSI colour escapes from terminal-format output
// so that --export <file> writes a clean text file even when colour
// is enabled.
func stripANSI(s string) string {
	const esc = '\x1b'
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != esc {
			b.WriteByte(s[i])
			continue
		}
		// skip until alphabetic terminator
		i++
		for i < len(s) && !((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
			i++
		}
	}
	return b.String()
}

// shouldUseColor mirrors the convention used elsewhere in the CLI —
// honour the NO_COLOR env var per https://no-color.org. The check
// pairs with the explicit --no-color flag.
func shouldUseColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return true
}
