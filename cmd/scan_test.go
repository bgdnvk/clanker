package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bgdnvk/clanker/internal/cost"
	"github.com/bgdnvk/clanker/internal/maker"
)

func TestStripANSI_RemovesEscapeSequences(t *testing.T) {
	in := "\x1b[31mred\x1b[0m plain \x1b[1;33mbold yellow\x1b[0m"
	got := stripANSI(in)
	want := "red plain bold yellow"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_PassesThroughCleanText(t *testing.T) {
	in := "no color here"
	if got := stripANSI(in); got != in {
		t.Errorf("stripANSI(%q) = %q, want unchanged", in, got)
	}
}

func TestDisplayName_PrefersLabelOverResourceID(t *testing.T) {
	cases := []struct {
		f    cost.ScanFinding
		want string
	}{
		// Label preferred when present (the friendly display string).
		{cost.ScanFinding{Service: "EC2", ResourceID: "i-0abc", Label: "eval-dev (i-0abc)", Region: "us-east-1"},
			"EC2 · eval-dev (i-0abc) · us-east-1"},
		// Falls back to ResourceID when Label is empty (legacy receipts).
		{cost.ScanFinding{Service: "EKS", ResourceID: "old", Region: "us-east-1"}, "EKS · old · us-east-1"},
		{cost.ScanFinding{Service: "EC2"}, "EC2"},
		{cost.ScanFinding{ResourceID: "i-abc"}, "i-abc"},
		{cost.ScanFinding{Provider: "gcp"}, "gcp"},
	}
	for _, c := range cases {
		if got := displayName(c.f); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

// TestBuildFixCommandArgs_UsesCanonicalResourceID is the round-2
// regression test for the live bug — even when the receipt carries a
// human-friendly Label, the maker plan command must use only the bare
// AWS ID from ResourceID. (Detector emits canonical i-xxx; CLI must
// not concatenate Label into the --instance-ids flag.)
func TestBuildFixCommandArgs_UsesCanonicalResourceID(t *testing.T) {
	f := cost.ScanFinding{
		Category:   "lifecycle",
		Service:    "EC2",
		ResourceID: "i-0abc123",
		Label:      "eval-dev (i-0abc123)", // present but must NOT leak into the args
	}
	args := buildFixCommandArgs(f, "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--instance-ids i-0abc123") {
		t.Errorf("args=%q, want '--instance-ids i-0abc123' (bare ID, not Label)", joined)
	}
	if strings.Contains(joined, "eval-dev") {
		t.Errorf("args=%q must NOT contain Label content; ResourceID stays canonical", joined)
	}
}

func TestPrimaryProvider(t *testing.T) {
	if got := primaryProvider(nil); got != "aws" {
		t.Errorf("nil receipt should default to aws, got %q", got)
	}
	r := &cost.ScanReceipt{ProvidersScanned: []string{"gcp", "aws"}}
	if got := primaryProvider(r); got != "gcp" {
		t.Errorf("first scanned provider should win, got %q", got)
	}
	r = &cost.ScanReceipt{Findings: []cost.ScanFinding{{Provider: "azure"}}}
	if got := primaryProvider(r); got != "azure" {
		t.Errorf("fallback to first finding's provider, got %q", got)
	}
}

func TestBuildFixCommandArgs_HandlesEachCategory(t *testing.T) {
	cases := []struct {
		name     string
		f        cost.ScanFinding
		profile  string
		contains []string
	}{
		{
			name:     "orphan NAT gateway emits describe-nat-gateways",
			f:        cost.ScanFinding{Category: "orphan", Service: "NAT Gateway", ResourceID: "nat-1"},
			profile:  "prod",
			contains: []string{"aws", "ec2", "describe-nat-gateways", "--nat-gateway-ids", "nat-1", "--profile", "prod"},
		},
		{
			name:     "orphan EC2 emits describe-instances",
			f:        cost.ScanFinding{Category: "orphan", Service: "EC2", ResourceID: "i-abc"},
			profile:  "prod",
			contains: []string{"aws", "ec2", "describe-instances", "--instance-ids", "i-abc"},
		},
		{
			name:     "rightsize EBS volume emits describe-volumes",
			f:        cost.ScanFinding{Category: "rightsize", Service: "EBS", ResourceID: "vol-1"},
			profile:  "default",
			contains: []string{"aws", "ec2", "describe-volumes", "--volume-ids", "vol-1"},
		},
		{
			name:     "orphan ALB LB emits elbv2 describe-load-balancers",
			f:        cost.ScanFinding{Category: "orphan", Service: "ALB LB", ResourceID: "arn:aws:elasticloadbalancing:..."},
			profile:  "",
			contains: []string{"aws", "elbv2", "describe-load-balancers", "--load-balancer-arns"},
		},
		{
			name:     "version-eol EKS emits describe-cluster",
			f:        cost.ScanFinding{Category: "version-eol", Service: "EKS", ResourceID: "old-cluster"},
			profile:  "",
			contains: []string{"aws", "eks", "describe-cluster", "--name", "old-cluster"},
		},
		{
			name:     "lifecycle EC2 emits describe-instances",
			f:        cost.ScanFinding{Category: "lifecycle", Service: "EC2", ResourceID: "i-abc"},
			profile:  "default",
			contains: []string{"aws", "ec2", "describe-instances", "--instance-ids", "i-abc"},
		},
		{
			name:     "commitment recs emit echo placeholder",
			f:        cost.ScanFinding{Category: "commitment", Service: "EC2", MonthlyWasteUSD: 100},
			profile:  "",
			contains: []string{"echo"},
		},
		{
			name:     "unknown category falls back to echo",
			f:        cost.ScanFinding{Category: "weird", Service: "X"},
			profile:  "",
			contains: []string{"echo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := buildFixCommandArgs(tc.f, tc.profile)
			if len(args) == 0 {
				t.Fatalf("expected args, got none")
			}
			joined := strings.Join(args, " ")
			for _, want := range tc.contains {
				if !strings.Contains(joined, want) {
					t.Errorf("args=%v missing %q", args, want)
				}
			}
		})
	}
}

func TestWriteFixPlan_WritesValidMakerPlanJSON(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fix.json")

	receipt := &cost.ScanReceipt{
		GeneratedAt:          time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		Mode:                 "deep",
		ProvidersScanned:     []string{"aws"},
		TotalMonthlyWasteUSD: 320,
		Findings: []cost.ScanFinding{
			{Provider: "aws", Category: "orphan", Service: "EC2", ResourceID: "nat-1", MonthlyWasteUSD: 32},
			{Provider: "aws", Category: "version-eol", Service: "EKS", ResourceID: "cl-old", MonthlyWasteUSD: 288},
		},
	}

	written, err := writeFixPlan(receipt, out, "prod", time.Now())
	if err != nil {
		t.Fatalf("writeFixPlan: %v", err)
	}
	if written == "" {
		t.Fatal("expected absolute path return")
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	var plan maker.Plan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("invalid plan JSON: %v\n%s", err, body)
	}
	if plan.Version != maker.CurrentPlanVersion {
		t.Errorf("plan.Version=%d, want %d", plan.Version, maker.CurrentPlanVersion)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(plan.Commands))
	}
	if plan.Provider != "aws" {
		t.Errorf("plan.Provider=%q, want aws", plan.Provider)
	}
	if !strings.Contains(plan.Summary, "$320.00") {
		t.Errorf("plan.Summary missing total: %q", plan.Summary)
	}
	// Each command should have a Reason that mentions the savings.
	for _, c := range plan.Commands {
		if c.Reason == "" {
			t.Errorf("command missing reason: %+v", c)
		}
		if !strings.Contains(c.Reason, "saves $") {
			t.Errorf("command reason should mention savings: %q", c.Reason)
		}
	}
	// Notes should warn about review-before-apply.
	if len(plan.Notes) == 0 {
		t.Errorf("plan should include safety notes")
	}
	joined := strings.Join(plan.Notes, " ")
	if !strings.Contains(joined, "Review") {
		t.Errorf("notes should mention review: %v", plan.Notes)
	}
}

func TestWriteFixPlan_NilReceiptErrors(t *testing.T) {
	_, err := writeFixPlan(nil, filepath.Join(t.TempDir(), "x.json"), "", time.Now())
	if err == nil {
		t.Fatal("expected error for nil receipt")
	}
}

func TestWriteFixPlan_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fix.json")

	receipt := &cost.ScanReceipt{
		ProvidersScanned: []string{"aws"},
		Findings: []cost.ScanFinding{
			{Provider: "aws", Category: "orphan", Service: "EC2", ResourceID: "i-1", MonthlyWasteUSD: 5},
		},
	}
	if _, err := writeFixPlan(receipt, out, "", time.Now()); err != nil {
		t.Fatalf("first write should succeed: %v", err)
	}
	// Second call must refuse rather than silently destroying the
	// user's edits to the first plan.
	_, err := writeFixPlan(receipt, out, "", time.Now())
	if err == nil {
		t.Fatal("expected refuse-to-overwrite error on second write")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("err=%q, want to mention overwrite", err)
	}
}
