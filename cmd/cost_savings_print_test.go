package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/cost"
)

func TestPrintSavingsReport_Nil(t *testing.T) {
	var buf bytes.Buffer
	printSavingsReport(&buf, nil, 10)
	if !strings.Contains(buf.String(), "No savings report") {
		t.Errorf("expected nil-report message, got %q", buf.String())
	}
}

func TestPrintSavingsReport_EmptyWithNote(t *testing.T) {
	var buf bytes.Buffer
	printSavingsReport(&buf, &cost.SavingsReport{
		Term:     "ONE_YEAR",
		Lookback: "SIXTY_DAYS",
		Notes:    "no commitment recommendations — account may not have enough usage history",
	}, 10)
	out := buf.String()
	if !strings.Contains(out, "term=ONE_YEAR") {
		t.Errorf("expected term in header, got %q", out)
	}
	if !strings.Contains(out, "Notes:") {
		t.Errorf("expected note line, got %q", out)
	}
	if !strings.Contains(out, "No commitment recommendations available") {
		t.Errorf("expected empty-state message, got %q", out)
	}
}

func TestPrintSavingsReport_RendersRows(t *testing.T) {
	var buf bytes.Buffer
	printSavingsReport(&buf, &cost.SavingsReport{
		Term: "ONE_YEAR", Lookback: "SIXTY_DAYS",
		TotalEstimatedSavings: 750.50,
		Recommendations: []cost.SavingsRecommendation{
			{Provider: "aws", Kind: cost.SavingsKindSavingsPlan, Family: "Compute", Term: "ONE_YEAR", HourlyCommitment: 0.05, EstimatedSavings: 500, EstimatedSavingsPc: 25, Detail: "family=m5, region=us-east-1"},
			{Provider: "aws", Kind: cost.SavingsKindReservedInstance, Service: "RDS", Family: "db.r6g", Term: "ONE_YEAR", UpfrontCost: 1200, EstimatedSavings: 250, EstimatedSavingsPc: 18, BreakevenMonths: 4.8, Detail: "qty=2"},
		},
	}, 10)
	out := buf.String()

	for _, want := range []string{
		"$750.50",
		"SP",
		"RI",
		"Compute",
		"RDS",
		"db.r6g",
		"$500.00",
		"$250.00",
		"4.8 mo",
		"Details:",
		"family=m5",
		"qty=2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestPrintSavingsReport_TopN(t *testing.T) {
	recs := []cost.SavingsRecommendation{
		{Kind: cost.SavingsKindSavingsPlan, Family: "Compute-1", EstimatedSavings: 100},
		{Kind: cost.SavingsKindSavingsPlan, Family: "Compute-2", EstimatedSavings: 50},
		{Kind: cost.SavingsKindSavingsPlan, Family: "Compute-3", EstimatedSavings: 25},
	}
	var buf bytes.Buffer
	printSavingsReport(&buf, &cost.SavingsReport{Recommendations: recs}, 2)
	out := buf.String()

	if !strings.Contains(out, "Compute-1") || !strings.Contains(out, "Compute-2") {
		t.Errorf("expected first two rows, got:\n%s", out)
	}
	if strings.Contains(out, "Compute-3") {
		t.Errorf("Compute-3 should be cut off by --top 2, got:\n%s", out)
	}
	if !strings.Contains(out, "showing top 2 of 3") {
		t.Errorf("expected truncation note, got:\n%s", out)
	}
}

func TestKindLabel(t *testing.T) {
	if got := kindLabel(cost.SavingsKindSavingsPlan); got != "SP" {
		t.Errorf("SavingsPlan label = %q, want SP", got)
	}
	if got := kindLabel(cost.SavingsKindReservedInstance); got != "RI" {
		t.Errorf("RI label = %q, want RI", got)
	}
	if got := kindLabel("custom"); got != "custom" {
		t.Errorf("unknown kind passthrough = %q, want custom", got)
	}
}
