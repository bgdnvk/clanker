package cost

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// RenderScanReceipt produces a coloured, tabwriter-aligned terminal
// receipt for `clanker scan`. Designed to be screenshot-friendly:
// headlines stand out, finding rows align cleanly, severity tints
// guide the eye to the worst offenders.
//
// The `color` flag mirrors the existing Formatter's pattern — emit
// raw ANSI when true, plain text when false (set false for piped
// output / --no-color / NO_COLOR env).
func RenderScanReceipt(receipt *ScanReceipt, color bool, top int) string {
	if receipt == nil {
		return "No scan receipt.\n"
	}
	r := &scanRenderer{color: color, top: top}
	return r.render(receipt)
}

// RenderScanReceiptJSON serialises the receipt as indented JSON for
// --format json or --export *.json.
func RenderScanReceiptJSON(receipt *ScanReceipt) ([]byte, error) {
	return json.MarshalIndent(receipt, "", "  ")
}

// RenderScanReceiptMarkdown produces a Markdown export — same content
// as the terminal version but plain ASCII tables.
func RenderScanReceiptMarkdown(receipt *ScanReceipt) string {
	if receipt == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Clanker — Cloud scan receipt\n\n")
	b.WriteString(fmt.Sprintf("Generated: %s\n", receipt.GeneratedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("Mode: %s\n", receipt.Mode))
	if len(receipt.ProvidersScanned) > 0 {
		b.WriteString(fmt.Sprintf("Providers scanned: %s\n", strings.Join(receipt.ProvidersScanned, ", ")))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("**Total monthly waste: $%.2f**\n", receipt.TotalMonthlyWasteUSD))
	if receipt.EstimatedMonthlyRunRateUSD > 0 {
		b.WriteString(fmt.Sprintf("Of $%.2f monthly run-rate\n", receipt.EstimatedMonthlyRunRateUSD))
	}
	b.WriteString("\n")
	if len(receipt.Categories) > 0 {
		b.WriteString("## By category\n\n")
		b.WriteString("| Category | Count | Monthly waste | Highest severity |\n")
		b.WriteString("|---|---:|---:|---|\n")
		for _, c := range receipt.Categories {
			sev := c.HighestSeverity
			if sev == "" {
				sev = "—"
			}
			b.WriteString(fmt.Sprintf("| %s | %d | $%.2f | %s |\n",
				c.Category, c.Count, c.MonthlyWasteUSD, sev))
		}
		b.WriteString("\n")
	}
	if len(receipt.Findings) > 0 {
		b.WriteString("## Findings\n\n")
		b.WriteString("| Provider | Service | Resource | Severity | $/mo | Action |\n")
		b.WriteString("|---|---|---|---|---:|---|\n")
		for _, f := range receipt.Findings {
			action := strings.ReplaceAll(f.Action, "|", "\\|")
			action = strings.ReplaceAll(action, "\n", " ")
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | $%.2f | %s |\n",
				f.Provider, f.Service, f.ResourceID, f.Severity, f.MonthlyWasteUSD, action))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// scanRenderer holds the shared style state for the terminal receipt.
type scanRenderer struct {
	color bool
	top   int
}

func (r *scanRenderer) render(receipt *ScanReceipt) string {
	var b strings.Builder

	// --- Header band -------------------------------------------------
	b.WriteString(r.color1(colorBold+colorCyan, "\n┌─ CLANKER · CLOUD SCAN RECEIPT ────────────────────────────────────┐\n"))
	b.WriteString(r.color1(colorCyan, "│"))
	b.WriteString(fmt.Sprintf(" Generated: %s   Mode: %s",
		receipt.GeneratedAt.Format("2006-01-02 15:04:05"),
		strings.ToUpper(receipt.Mode)))
	pad := 67 - len(fmt.Sprintf(" Generated: %s   Mode: %s",
		receipt.GeneratedAt.Format("2006-01-02 15:04:05"), strings.ToUpper(receipt.Mode)))
	if pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString(r.color1(colorCyan, "│\n"))
	b.WriteString(r.color1(colorCyan, "└───────────────────────────────────────────────────────────────────┘\n"))

	// --- Headline total ---------------------------------------------
	totalText := fmt.Sprintf("$%.2f", receipt.TotalMonthlyWasteUSD)
	b.WriteString("\n")
	b.WriteString(r.color1(colorBold+colorGreen, "  "+totalText))
	b.WriteString(r.color1(colorWhite, " /month of waste"))
	if receipt.EstimatedMonthlyRunRateUSD > 0 {
		b.WriteString(r.color1(colorWhite, fmt.Sprintf("  (of $%.2f run-rate)", receipt.EstimatedMonthlyRunRateUSD)))
	}
	b.WriteString("\n")
	if len(receipt.ProvidersScanned) > 0 {
		b.WriteString(r.color1(colorWhite, fmt.Sprintf("  Providers: %s\n",
			strings.ToUpper(strings.Join(receipt.ProvidersScanned, ", ")))))
	}
	if receipt.DurationMS > 0 {
		b.WriteString(r.color1(colorWhite, fmt.Sprintf("  Scanned in %.1fs\n",
			float64(receipt.DurationMS)/1000)))
	}
	b.WriteString("\n")

	// --- Provider skips (deep-mode CLI fallback notes etc) ----------
	if len(receipt.ProvidersSkipped) > 0 {
		b.WriteString(r.color1(colorYellow, "  Skipped:\n"))
		for _, s := range receipt.ProvidersSkipped {
			b.WriteString(fmt.Sprintf("    • %s — %s\n", s.Provider, s.Reason))
		}
		b.WriteString("\n")
	}

	// --- Categories rollup ------------------------------------------
	if len(receipt.Categories) > 0 {
		b.WriteString(r.color1(colorBold+colorYellow, "  BY CATEGORY\n"))
		w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "    CATEGORY\tCOUNT\t$/MO\tHIGHEST SEVERITY")
		fmt.Fprintln(w, "    --------\t-----\t----\t----------------")
		for _, c := range receipt.Categories {
			sev := c.HighestSeverity
			if sev == "" {
				sev = "—"
			}
			catLabel := c.Category
			if r.color {
				catLabel = r.color1(categoryColor(c.Category), c.Category)
				sev = r.color1(severityColor(sev), sev)
			}
			fmt.Fprintf(w, "    %s\t%d\t$%.2f\t%s\n",
				catLabel, c.Count, c.MonthlyWasteUSD, sev)
		}
		w.Flush()
		b.WriteString("\n")
	}

	// --- Findings table (top N) -------------------------------------
	if len(receipt.Findings) == 0 {
		b.WriteString(r.color1(colorGreen, "  ✓ No actionable waste detected.\n\n"))
	} else {
		findings := receipt.Findings
		// Findings already arrive savings-desc from the backend, but
		// guard for the fallback path which may not sort.
		sort.SliceStable(findings, func(i, j int) bool {
			return findings[i].MonthlyWasteUSD > findings[j].MonthlyWasteUSD
		})
		limit := len(findings)
		if r.top > 0 && r.top < limit {
			limit = r.top
		}
		b.WriteString(r.color1(colorBold+colorYellow,
			fmt.Sprintf("  FINDINGS (%d shown of %d)\n", limit, len(findings))))
		w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "    PROVIDER\tCATEGORY\tSEV\t$/MO\tRESOURCE")
		fmt.Fprintln(w, "    --------\t--------\t---\t----\t--------")
		for _, f := range findings[:limit] {
			provider := strings.ToUpper(f.Provider)
			cat := f.Category
			sev := f.Severity
			res := f.Service
			if f.ResourceID != "" {
				if res != "" {
					res = res + " · " + truncateScan(f.ResourceID, 32)
				} else {
					res = truncateScan(f.ResourceID, 32)
				}
			}
			if f.Region != "" {
				res = res + " · " + f.Region
			}
			if r.color {
				cat = r.color1(categoryColor(f.Category), cat)
				sev = r.color1(severityColor(f.Severity), strings.ToUpper(sev))
			} else {
				sev = strings.ToUpper(sev)
			}
			fmt.Fprintf(w, "    %s\t%s\t%s\t$%.2f\t%s\n",
				provider, cat, sev, f.MonthlyWasteUSD, res)
		}
		w.Flush()
		if r.top > 0 && len(findings) > r.top {
			b.WriteString(r.color1(colorWhite,
				fmt.Sprintf("\n    (showing top %d — pass --top 0 for all)\n", r.top)))
		}
		b.WriteString("\n")

		// --- Action details (one block per finding) -----------------
		b.WriteString(r.color1(colorBold+colorYellow, "  DETAILS\n"))
		for _, f := range findings[:limit] {
			b.WriteString(fmt.Sprintf("    %s ", r.color1(colorBold, "•")))
			b.WriteString(r.color1(colorBold, fmt.Sprintf("%s/%s", strings.ToUpper(f.Provider), f.Service)))
			if f.ResourceID != "" {
				b.WriteString(r.color1(colorWhite, " · "+f.ResourceID))
			}
			b.WriteString(r.color1(colorGreen, fmt.Sprintf("  $%.2f/mo\n", f.MonthlyWasteUSD)))
			if f.Action != "" {
				b.WriteString(fmt.Sprintf("      %s\n", f.Action))
			}
			if f.Detail != "" && f.Detail != f.Action {
				b.WriteString(r.color1(colorWhite, fmt.Sprintf("      %s\n", f.Detail)))
			}
			if f.DocsURL != "" {
				b.WriteString(r.color1(colorBlue, fmt.Sprintf("      → %s\n", f.DocsURL)))
			}
		}
		b.WriteString("\n")
	}

	// --- Anomalies (deep mode) --------------------------------------
	if len(receipt.Anomalies) > 0 {
		b.WriteString(r.color1(colorBold+colorRed, "  ANOMALIES\n"))
		for _, a := range receipt.Anomalies {
			// Prefer ActualCost (modern backend shape); fall back to
			// Cost for receipts produced before the wire shape was
			// aligned with the backend.
			cost := a.ActualCost
			if cost == 0 {
				cost = a.Cost
			}
			b.WriteString(fmt.Sprintf("    • %s/%s — $%.2f", strings.ToUpper(a.Provider), a.Service, cost))
			if a.PercentChange != 0 {
				b.WriteString(fmt.Sprintf(" (%+.0f%%)", a.PercentChange))
			}
			if a.ExpectedCost > 0 && a.ActualCost > 0 {
				b.WriteString(fmt.Sprintf(" (expected $%.2f)", a.ExpectedCost))
			}
			b.WriteString("\n")
			if a.Description != "" {
				b.WriteString(r.color1(colorWhite, "      "+a.Description+"\n"))
			}
		}
		b.WriteString("\n")
	}

	// --- LLM spend (deep mode) --------------------------------------
	if receipt.LLMSpend != nil {
		b.WriteString(r.color1(colorBold+colorPurple, "  AI TAX\n"))
		b.WriteString(r.color1(colorPurple, fmt.Sprintf("    $%.2f total · %d requests · %s tokens",
			receipt.LLMSpend.TotalCostUSD,
			receipt.LLMSpend.TotalRequests,
			formatTokensScan(receipt.LLMSpend.TotalTokens))))
		if receipt.LLMSpend.PrimaryProvider != "" {
			b.WriteString(r.color1(colorWhite, " · primarily "+receipt.LLMSpend.PrimaryProvider))
		}
		b.WriteString("\n\n")
	}

	// --- Footer band ------------------------------------------------
	b.WriteString(r.color1(colorCyan, "  ─────────────────────────────────────────────────────────────────\n"))
	b.WriteString(r.color1(colorWhite, "  clankercloud.ai — apply with `clanker scan --fix <out.json>`\n"))
	if receipt.Notes != "" {
		b.WriteString(r.color1(colorWhite, "  Notes: "+receipt.Notes+"\n"))
	}
	b.WriteString("\n")
	return b.String()
}

func (r *scanRenderer) color1(code, text string) string {
	if !r.color {
		return text
	}
	return code + text + colorReset
}

func categoryColor(cat string) string {
	switch cat {
	case "version-eol":
		return colorRed
	case "orphan":
		return colorYellow
	case "rightsize":
		return colorYellow
	case "lifecycle":
		return colorGreen
	case "commitment":
		return colorBlue
	default:
		return colorCyan
	}
}

func severityColor(sev string) string {
	switch strings.ToLower(sev) {
	case "critical":
		return colorRed + colorBold
	case "high":
		return colorRed
	case "medium":
		return colorYellow
	case "low":
		return colorGreen
	default:
		return colorWhite
	}
}

func truncateScan(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max < 4 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func formatTokensScan(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}
