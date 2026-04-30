package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/maker"
)

func TestPrintPlanCostEstimate_Nil(t *testing.T) {
	var buf bytes.Buffer
	printPlanCostEstimate(&buf, nil)
	if !strings.Contains(buf.String(), "No estimate") {
		t.Errorf("expected nil-message, got %q", buf.String())
	}
}

func TestPrintPlanCostEstimate_Empty(t *testing.T) {
	var buf bytes.Buffer
	printPlanCostEstimate(&buf, &maker.PlanCostEstimate{})
	out := buf.String()
	if !strings.Contains(out, "Estimated cost: $0.0000/hr") {
		t.Errorf("expected zero header, got %q", out)
	}
	if !strings.Contains(out, "No cost-bearing items") {
		t.Errorf("expected empty-state message, got %q", out)
	}
}

func TestPrintPlanCostEstimate_RendersItems(t *testing.T) {
	var buf bytes.Buffer
	printPlanCostEstimate(&buf, &maker.PlanCostEstimate{
		HourlyUSD:           0.192,
		MonthlyUSD:          140.16,
		UnknownPriceItems:   1,
		UnestimatedCommands: 2,
		Items: []maker.PlanCostItem{
			{Provider: "aws", Resource: "ec2", Family: "m5.xlarge", Count: 1, HourlyUSD: 0.192, MonthlyUSD: 140.16, PriceKnown: true},
			{Provider: "aws", Resource: "lambda", Count: 1, Note: "metered", PriceKnown: false},
		},
		Notes: []string{"some items have no price"},
	})
	out := buf.String()

	for _, want := range []string{
		"$0.1920/hr",
		"$140.16/mo",
		"1 item(s) without a price",
		"2 command(s) not classified",
		"some items have no price",
		"aws",
		"m5.xlarge",
		"$0.1920",
		"lambda",
		"metered", // unpriced item shows the note in the cost column
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestDashIfEmpty(t *testing.T) {
	if got := dashIfEmpty(""); got != "—" {
		t.Errorf("empty → %q, want em-dash", got)
	}
	if got := dashIfEmpty("foo"); got != "foo" {
		t.Errorf("non-empty pass through; got %q", got)
	}
}
