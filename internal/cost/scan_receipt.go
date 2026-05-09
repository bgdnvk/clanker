package cost

import "time"

// ScanReceipt is the wire shape returned by `clanker scan` and the
// clanker-cloud `/api/cost/scan` endpoint. It's the condensed "Spotify
// Wrapped"-style waste-receipt across every configured provider.
//
// The CLI receives this payload either by:
//
//  1. Calling the local clanker-cloud backend at localhost:8080..8084
//     (when the desktop app is running) — gets the full receipt with
//     operational findings (idle NATs, EOL'd EKS, stopped EC2, etc).
//  2. Falling back to local commitment recommendations from the AWS
//     Cost Explorer Savings Plans / Reserved Instance APIs — only
//     commitment findings, but works with no backend running.
//
// Both paths produce the same shape so the renderer stays simple.
type ScanReceipt struct {
	GeneratedAt                time.Time             `json:"generatedAt"`
	Mode                       string                `json:"mode"`
	ProvidersScanned           []string              `json:"providersScanned"`
	ProvidersSkipped           []ScanProviderSkip    `json:"providersSkipped,omitempty"`
	TotalMonthlyWasteUSD       float64               `json:"totalMonthlyWasteUsd"`
	EstimatedMonthlyRunRateUSD float64               `json:"estimatedMonthlyRunRateUsd,omitempty"`
	Categories                 []ScanCategorySummary `json:"categories"`
	Findings                   []ScanFinding         `json:"findings"`
	Anomalies                  []ScanAnomaly         `json:"anomalies,omitempty"`
	LLMSpend                   *ScanLLMSpend         `json:"llmSpend,omitempty"`
	DurationMS                 int64                 `json:"durationMs"`
	Notes                      string                `json:"notes,omitempty"`
}

// ScanProviderSkip records a configured provider that couldn't run.
type ScanProviderSkip struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// ScanCategorySummary is one rolled-up category line on the receipt.
type ScanCategorySummary struct {
	Category        string  `json:"category"`
	Count           int     `json:"count"`
	MonthlyWasteUSD float64 `json:"monthlyWasteUsd"`
	HighestSeverity string  `json:"highestSeverity"`
}

// ScanFinding is one waste row.
type ScanFinding struct {
	Provider        string  `json:"provider"`
	Category        string  `json:"category"`
	Severity        string  `json:"severity"`
	Service         string  `json:"service,omitempty"`
	ResourceID      string  `json:"resourceId,omitempty"`
	ResourceArn     string  `json:"resourceArn,omitempty"`
	Region          string  `json:"region,omitempty"`
	MonthlyWasteUSD float64 `json:"monthlyWasteUsd"`
	Action          string  `json:"action,omitempty"`
	Detail          string  `json:"detail,omitempty"`
	DocsURL         string  `json:"docsUrl,omitempty"`
}

// ScanAnomaly is a cost spike surfaced by the deep-mode scan. The
// shape mirrors clanker-cloud's models.CostAnomaly so JSON-decoding
// a backend-sourced receipt doesn't silently drop fields. Earlier
// drafts only carried `Cost` and lost expectedCost/actualCost/
// description on every deep scan — the renderer now has the data
// it needs to show context if it ever wants to.
type ScanAnomaly struct {
	Service       string  `json:"service"`
	Provider      string  `json:"provider"`
	ExpectedCost  float64 `json:"expectedCost"`
	ActualCost    float64 `json:"actualCost"`
	PercentChange float64 `json:"percentChange"`
	Description   string  `json:"description,omitempty"`
	// Cost is retained for backwards compatibility — older receipts
	// produced by the local fallback path before this PR set it
	// instead of ActualCost. Renderers should prefer ActualCost
	// when non-zero, falling back to Cost.
	Cost float64 `json:"cost,omitempty"`
}

// ScanLLMSpend is the AI-tax line item.
type ScanLLMSpend struct {
	TotalCostUSD    float64 `json:"totalCostUsd"`
	TotalTokens     int64   `json:"totalTokens"`
	TotalRequests   int     `json:"totalRequests"`
	PrimaryProvider string  `json:"primaryProvider,omitempty"`
}

// ProjectSavingsToReceipt converts a SavingsReport (the local CLI's
// commitment-only output) into a ScanReceipt so the same renderer can
// produce a coloured terminal receipt regardless of source.
//
// This is the fallback path when the desktop backend isn't running —
// the receipt has fewer categories (commitment only), but the layout
// matches a backend-sourced receipt so a screenshot is still useful.
func ProjectSavingsToReceipt(report *SavingsReport, mode string, started time.Time) *ScanReceipt {
	receipt := &ScanReceipt{
		GeneratedAt:      started.UTC(),
		Mode:             mode,
		ProvidersScanned: []string{},
		Findings:         []ScanFinding{},
		Categories:       []ScanCategorySummary{},
	}
	if report == nil {
		receipt.DurationMS = time.Since(started).Milliseconds()
		return receipt
	}

	provider := report.Provider
	if provider == "" {
		provider = "aws"
	}
	receipt.ProvidersScanned = []string{provider}
	receipt.Notes = report.Notes

	for _, rec := range report.Recommendations {
		f := ScanFinding{
			Provider:        provider,
			Category:        "commitment",
			Severity:        "medium",
			Service:         rec.Service,
			MonthlyWasteUSD: rec.EstimatedSavings,
			Detail:          rec.Detail,
		}
		if rec.Family != "" {
			if f.Service == "" {
				f.Service = rec.Family
			}
		}
		f.Action = formatCommitmentAction(rec)
		receipt.Findings = append(receipt.Findings, f)
		receipt.TotalMonthlyWasteUSD += f.MonthlyWasteUSD
	}

	if len(receipt.Findings) > 0 {
		receipt.Categories = []ScanCategorySummary{{
			Category:        "commitment",
			Count:           len(receipt.Findings),
			MonthlyWasteUSD: receipt.TotalMonthlyWasteUSD,
			HighestSeverity: "medium",
		}}
	}
	receipt.DurationMS = time.Since(started).Milliseconds()
	return receipt
}

// formatCommitmentAction produces a human-friendly action line for a
// commitment recommendation. Used by both ProjectSavingsToReceipt (the
// fallback path) and the backend-sourced receipt's row rendering.
func formatCommitmentAction(rec SavingsRecommendation) string {
	kind := "Commitment"
	switch rec.Kind {
	case SavingsKindSavingsPlan:
		kind = "Savings Plan"
	case SavingsKindReservedInstance:
		kind = "Reserved Instance"
	}
	if rec.Term != "" {
		return kind + " · " + rec.Term + " term"
	}
	return kind
}
