package cost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleReceipt() *ScanReceipt {
	return &ScanReceipt{
		GeneratedAt:                time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		Mode:                       "deep",
		ProvidersScanned:           []string{"aws", "gcp"},
		TotalMonthlyWasteUSD:       332,
		EstimatedMonthlyRunRateUSD: 1500,
		Categories: []ScanCategorySummary{
			{Category: "version-eol", Count: 1, MonthlyWasteUSD: 288, HighestSeverity: "high"},
			{Category: "orphan", Count: 1, MonthlyWasteUSD: 32, HighestSeverity: "medium"},
			{Category: "lifecycle", Count: 1, MonthlyWasteUSD: 12, HighestSeverity: "low"},
		},
		Findings: []ScanFinding{
			{Provider: "aws", Category: "version-eol", Severity: "high", Service: "EKS",
				ResourceID: "old-cluster", MonthlyWasteUSD: 288, Action: "Upgrade EKS to v1.35"},
			{Provider: "aws", Category: "orphan", Severity: "medium", Service: "EC2",
				ResourceID: "nat-0123abc", MonthlyWasteUSD: 32, Action: "Delete idle NAT gateway"},
			{Provider: "gcp", Category: "lifecycle", Severity: "low", Service: "Storage",
				ResourceID: "logs-bucket", MonthlyWasteUSD: 12, Action: "Add 90-day lifecycle"},
		},
		DurationMS: 1500,
	}
}

func TestRenderScanReceipt_TerminalContainsHeadlinesAndFindings(t *testing.T) {
	out := RenderScanReceipt(sampleReceipt(), false, 20)
	mustContain(t, out, "$332.00")
	mustContain(t, out, "/month of waste")
	mustContain(t, out, "AWS, GCP")
	mustContain(t, out, "FINDINGS (3 shown of 3)")
	mustContain(t, out, "old-cluster")
	mustContain(t, out, "Upgrade EKS to v1.35")
	mustContain(t, out, "BY CATEGORY")
	// Run-rate context surfaces.
	mustContain(t, out, "$1500.00 run-rate")
}

func TestRenderScanReceipt_TerminalNoColorIsAnsiFree(t *testing.T) {
	out := RenderScanReceipt(sampleReceipt(), false, 20)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("--no-color output contained ANSI escape: %q", out)
	}
}

func TestRenderScanReceipt_TerminalColorEmitsAnsi(t *testing.T) {
	out := RenderScanReceipt(sampleReceipt(), true, 20)
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("color=true output missing ANSI escapes")
	}
}

func TestRenderScanReceipt_RespectsTopLimit(t *testing.T) {
	receipt := sampleReceipt()
	out := RenderScanReceipt(receipt, false, 1)
	mustContain(t, out, "FINDINGS (1 shown of 3)")
	mustContain(t, out, "old-cluster") // top finding by waste
	if strings.Contains(out, "logs-bucket") {
		t.Errorf("top=1 should hide finding #3; got: %s", out)
	}
	mustContain(t, out, "(showing top 1")
}

func TestRenderScanReceipt_NilIsSafe(t *testing.T) {
	out := RenderScanReceipt(nil, false, 10)
	if !strings.Contains(out, "No scan receipt") {
		t.Errorf("nil receipt should produce a friendly message, got %q", out)
	}
}

func TestRenderScanReceipt_EmptyFindingsShowsCleanState(t *testing.T) {
	receipt := &ScanReceipt{
		GeneratedAt:      time.Now().UTC(),
		Mode:             "quick",
		ProvidersScanned: []string{"aws"},
		Findings:         []ScanFinding{},
		Categories:       []ScanCategorySummary{},
	}
	out := RenderScanReceipt(receipt, false, 10)
	mustContain(t, out, "No actionable waste detected")
}

func TestRenderScanReceipt_AnomaliesAndLLMSurface(t *testing.T) {
	r := sampleReceipt()
	r.Anomalies = []ScanAnomaly{
		{Provider: "aws", Service: "DataTransfer", Cost: 240, PercentChange: 38},
	}
	r.LLMSpend = &ScanLLMSpend{
		TotalCostUSD:    18.42,
		TotalTokens:     1_250_000,
		TotalRequests:   312,
		PrimaryProvider: "anthropic",
	}
	out := RenderScanReceipt(r, false, 20)
	mustContain(t, out, "ANOMALIES")
	mustContain(t, out, "DataTransfer")
	mustContain(t, out, "AI TAX")
	mustContain(t, out, "$18.42 total")
	mustContain(t, out, "anthropic")
	// Token formatting check — 1.25M renders compactly.
	mustContain(t, out, "1.25M")
}

func TestRenderScanReceiptJSON_RoundTrips(t *testing.T) {
	bytes, err := RenderScanReceiptJSON(sampleReceipt())
	if err != nil {
		t.Fatalf("RenderScanReceiptJSON: %v", err)
	}
	var roundTrip ScanReceipt
	if err := json.Unmarshal(bytes, &roundTrip); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if roundTrip.TotalMonthlyWasteUSD != 332 {
		t.Errorf("round-trip TotalMonthlyWasteUSD=%.2f, want 332", roundTrip.TotalMonthlyWasteUSD)
	}
	if len(roundTrip.Findings) != 3 {
		t.Errorf("round-trip findings=%d, want 3", len(roundTrip.Findings))
	}
}

func TestRenderScanReceiptMarkdown_ContainsTablesAndTotal(t *testing.T) {
	out := RenderScanReceiptMarkdown(sampleReceipt())
	mustContain(t, out, "# Clanker — Cloud scan receipt")
	mustContain(t, out, "Total monthly waste: $332.00")
	mustContain(t, out, "Of $1500.00 monthly run-rate")
	mustContain(t, out, "## By category")
	mustContain(t, out, "| version-eol | 1 | $288.00 | high |")
	mustContain(t, out, "## Findings")
	mustContain(t, out, "| aws | EKS | old-cluster | high | $288.00 | Upgrade EKS to v1.35 |")
}

func TestRenderScanReceiptMarkdown_EscapesPipesInActions(t *testing.T) {
	r := sampleReceipt()
	r.Findings[0].Action = "pipe | inside | text\nwith newline"
	out := RenderScanReceiptMarkdown(r)
	mustContain(t, out, "pipe \\| inside \\| text with newline")
}

func TestProjectSavingsToReceipt_ProducesUsableReceipt(t *testing.T) {
	report := &SavingsReport{
		Provider: "aws",
		Recommendations: []SavingsRecommendation{
			{Provider: "aws", Kind: SavingsKindSavingsPlan, Service: "EC2", Family: "Compute",
				Term: "ONE_YEAR", EstimatedSavings: 145.50, EstimatedSavingsPc: 24.3},
			{Provider: "aws", Kind: SavingsKindReservedInstance, Service: "RDS", Family: "db.r6g",
				Term: "ONE_YEAR", EstimatedSavings: 50.0, EstimatedSavingsPc: 12.0},
		},
	}
	receipt := ProjectSavingsToReceipt(report, "deep", time.Now().UTC().Add(-50*time.Millisecond))
	if receipt == nil {
		t.Fatal("expected non-nil receipt")
	}
	if receipt.TotalMonthlyWasteUSD != 195.5 {
		t.Errorf("total=%.2f, want 195.5", receipt.TotalMonthlyWasteUSD)
	}
	if len(receipt.Findings) != 2 {
		t.Fatalf("findings=%d, want 2", len(receipt.Findings))
	}
	for _, f := range receipt.Findings {
		if f.Category != "commitment" {
			t.Errorf("finding category=%q, want commitment: %+v", f.Category, f)
		}
	}
	if len(receipt.Categories) != 1 || receipt.Categories[0].Category != "commitment" {
		t.Errorf("expected single 'commitment' category rollup, got %#v", receipt.Categories)
	}
}

func TestProjectSavingsToReceipt_NilReportIsSafe(t *testing.T) {
	receipt := ProjectSavingsToReceipt(nil, "quick", time.Now())
	if receipt == nil {
		t.Fatal("expected non-nil receipt for nil input")
	}
	if receipt.TotalMonthlyWasteUSD != 0 || len(receipt.Findings) != 0 {
		t.Errorf("expected empty receipt, got %+v", receipt)
	}
}

func TestFormatTokensScan(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{1500, "1.5K"},
		{1_000_000, "1.00M"},
		{1_250_000, "1.25M"},
	}
	for _, tc := range cases {
		if got := formatTokensScan(tc.in); got != tc.want {
			t.Errorf("formatTokensScan(%d)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeverityAndCategoryColorsAreDistinct(t *testing.T) {
	// Smoke test — make sure the helpers don't panic and return non-empty
	// strings for every documented value.
	for _, s := range []string{"critical", "high", "medium", "low", "unknown"} {
		if severityColor(s) == "" {
			t.Errorf("severityColor(%q) returned empty", s)
		}
	}
	for _, c := range []string{"version-eol", "orphan", "rightsize", "lifecycle", "commitment", "other"} {
		if categoryColor(c) == "" {
			t.Errorf("categoryColor(%q) returned empty", c)
		}
	}
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q\n--- output ---\n%s", needle, haystack)
	}
}
