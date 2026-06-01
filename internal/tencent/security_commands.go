package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// securityScan describes one of the ten Tencent security scans the CLI
// exposes. Each scan returns raw JSON shaped for both the dashboard
// surface and `jq` piping. The HTTP API under `clanker server` already
// surfaces the same set at /api/v1/tencent/scan/* — this command suite
// is the shell-out path used by clanker-cloud's TencentSecurityPanel.
type securityScan struct {
	name    string // sub-command verb, e.g. "public-exposure"
	short   string // one-line description for `--help`
	needsRG bool   // true when the underlying call accepts a region
	run     func(ctx context.Context, c *Client, region string, days int) (string, error)
}

// securityScans is the load-bearing registry. Adding a new scan here is
// the only place that needs to change to surface it both as a sub-command
// and inside `clanker tencent security all`.
var securityScans = []securityScan{
	{
		name:    "public-exposure",
		short:   "CVMs reachable from the public internet (CVM × SG × public IP)",
		needsRG: true,
		run: func(ctx context.Context, c *Client, region string, _ int) (string, error) {
			return c.PublicExposureScanJSON(ctx, region)
		},
	},
	{
		name:    "clb-exposure",
		short:   "Public-facing CLB listeners with risky protocol/port combos",
		needsRG: true,
		run: func(ctx context.Context, c *Client, region string, _ int) (string, error) {
			return c.CLBExposureScanJSON(ctx, region)
		},
	},
	{
		name:    "db-exposure",
		short:   "MySQL/Postgres/Redis/MongoDB instances exposed beyond the VPC",
		needsRG: true,
		run: func(ctx context.Context, c *Client, region string, _ int) (string, error) {
			return c.DBExposureScanJSON(ctx, region)
		},
	},
	{
		name:    "idle-eips",
		short:   "Unassociated Elastic IPs still billed at the hourly idle rate",
		needsRG: true,
		run: func(ctx context.Context, c *Client, region string, _ int) (string, error) {
			return c.IdleEIPScanJSON(ctx, region)
		},
	},
	{
		name:    "unencrypted-cbs",
		short:   "CBS volumes that are not server-side encrypted",
		needsRG: true,
		run: func(ctx context.Context, c *Client, region string, _ int) (string, error) {
			return c.UnencryptedCBSScanJSON(ctx, region)
		},
	},
	{
		name:  "cert-expiry",
		short: "SSL certificates expiring within --days (default 30)",
		run: func(ctx context.Context, c *Client, _ string, days int) (string, error) {
			return c.CertExpiryScanJSON(ctx, days)
		},
	},
	{
		name:  "cam-hygiene",
		short: "CAM sub-accounts missing MFA, with old access keys, or no login restriction",
		run: func(ctx context.Context, c *Client, _ string, _ int) (string, error) {
			return c.CAMHygieneScanJSON(ctx)
		},
	},
	{
		name:  "waf-coverage",
		short: "EdgeOne / CLB / public CVM hosts that don't have WAF in front",
		run: func(ctx context.Context, c *Client, _ string, _ int) (string, error) {
			return c.WAFCoverageScanJSON(ctx)
		},
	},
	{
		name:    "antiddos-coverage",
		short:   "Public Elastic IPs not protected by Anti-DDoS Advanced",
		needsRG: true,
		run: func(ctx context.Context, c *Client, region string, _ int) (string, error) {
			return c.AntiDDoSCoverageScanJSON(ctx, region)
		},
	},
	{
		name:  "audit-coverage",
		short: "Whether Cloud Audit is enabled and writing to durable storage",
		run: func(ctx context.Context, c *Client, _ string, _ int) (string, error) {
			return c.AuditLogCoverageScanJSON(ctx)
		},
	},
}

// buildSecurityCmd builds the `clanker tencent security` subtree. region
// is shared with the parent command's persistent --region flag so users
// can write `clanker tencent --region ap-jakarta security clb-exposure`.
func buildSecurityCmd(region *string) *cobra.Command {
	securityCmd := &cobra.Command{
		Use:   "security",
		Short: "Run Tencent Cloud security scans",
		Long: `Run one (or all) of the ten Tencent Cloud security scans the
clanker-cloud dashboard surfaces.

Each scan returns raw JSON on stdout so it's safe to pipe into jq, an
incident ticket, or the clanker-cloud HTTP API. The scan envelopes are
shaped for both human reading and machine parsing — see the dashboard's
Security tab for the canonical UI.`,
	}

	// Per-scan sub-commands. Bind the loop variable so each closure
	// captures its own scan definition rather than the last iteration.
	for _, scan := range securityScans {
		scan := scan
		sub := &cobra.Command{
			Use:   scan.name,
			Short: scan.short,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSecurityScan(cmd.Context(), scan, region, cmd)
			},
		}
		if scan.name == "cert-expiry" {
			sub.Flags().Int("days", 30, "Flag certificates expiring within this many days")
		}
		securityCmd.AddCommand(sub)
	}

	// `all` fan-out — runs the 10 scans in parallel and emits a wrapped
	// envelope so callers can consume the whole set in one round-trip.
	allCmd := &cobra.Command{
		Use:   "all",
		Short: "Run every security scan and emit a wrapped JSON envelope",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds := ResolveCredentials()
			if region != nil && *region != "" {
				creds.Region = *region
			}
			client, err := NewClient(creds, viper.GetBool("debug"))
			if err != nil {
				return err
			}
			days, _ := cmd.Flags().GetInt("days")
			return runAllSecurityScans(cmd.Context(), client, creds.Region, days, securityScans, cmd)
		},
	}
	allCmd.Flags().Int("days", 30, "Days threshold for cert-expiry within the bundle")
	securityCmd.AddCommand(allCmd)

	return securityCmd
}

// runSecurityScan executes a single scan and emits its raw JSON on stdout.
// Region defaults flow through ResolveCredentials so the same precedence
// (--region > env > config > ap-singapore) applies as the other tencent
// commands.
func runSecurityScan(ctx context.Context, scan securityScan, regionFlag *string, cmd *cobra.Command) error {
	creds := ResolveCredentials()
	if regionFlag != nil && strings.TrimSpace(*regionFlag) != "" {
		creds.Region = *regionFlag
	}
	client, err := NewClient(creds, viper.GetBool("debug"))
	if err != nil {
		return err
	}

	days := 30
	if scan.name == "cert-expiry" {
		if v, err := cmd.Flags().GetInt("days"); err == nil && v > 0 {
			days = v
		}
	}

	body, err := scan.run(ctx, client, creds.Region, days)
	if err != nil {
		return fmt.Errorf("%s scan: %w", scan.name, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), body)
	return nil
}

// allScanResult is the per-scan record inside the `security all` envelope.
// `data` is the raw JSON the individual scan produced (re-encoded so the
// outer wrapper stays valid JSON); `error` is set when that scan failed
// without aborting the rest of the bundle.
type allScanResult struct {
	Name  string          `json:"name"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// runAllSecurityScans fans out across every registered scan in parallel.
// Each scan's failure is captured in its envelope rather than aborting
// the whole call — operators want to see the 9 scans that succeeded
// even if one IAM-permission gap broke the 10th. `scans` is passed in
// rather than read from the package global so tests can supply a fake
// registry without mutating shared state under -race.
func runAllSecurityScans(ctx context.Context, client *Client, region string, days int, scans []securityScan, cmd *cobra.Command) error {
	results := make([]allScanResult, len(scans))
	var wg sync.WaitGroup
	for i, scan := range scans {
		i, scan := i, scan
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := scan.run(ctx, client, region, days)
			res := allScanResult{Name: scan.name}
			if err != nil {
				res.Error = err.Error()
			} else if json.Valid([]byte(body)) {
				res.Data = json.RawMessage(body)
			} else {
				res.Error = "scan returned non-JSON output"
			}
			results[i] = res
		}()
	}
	wg.Wait()

	envelope := struct {
		Region string          `json:"region"`
		Scans  []allScanResult `json:"scans"`
	}{Region: region, Scans: results}
	out, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode security-all envelope: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
	return nil
}
