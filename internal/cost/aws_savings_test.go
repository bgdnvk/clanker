package cost

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

func strPtr(s string) *string { return &s }

func TestNormaliseLookback(t *testing.T) {
	cases := map[string]string{
		"":              "SIXTY_DAYS",
		"7":             "SEVEN_DAYS",
		"seven_days":    "SEVEN_DAYS",
		"30":            "THIRTY_DAYS",
		"60":            "SIXTY_DAYS",
		"sixty_days":    "SIXTY_DAYS",
		"  THIRTY_DAYS": "THIRTY_DAYS",
		"garbage":       "SIXTY_DAYS", // fallback
	}
	for in, want := range cases {
		if got := normaliseLookback(in); got != want {
			t.Errorf("normaliseLookback(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseTerm(t *testing.T) {
	cases := map[string]string{
		"":            "ONE_YEAR",
		"1":           "ONE_YEAR",
		"one_year":    "ONE_YEAR",
		"3":           "THREE_YEARS",
		"three_years": "THREE_YEARS",
		"  3":         "THREE_YEARS",
		"garbage":     "ONE_YEAR",
	}
	for in, want := range cases {
		if got := normaliseTerm(in); got != want {
			t.Errorf("normaliseTerm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFloatString(t *testing.T) {
	if v := parseFloatString(nil); v != 0 {
		t.Errorf("nil → %v, want 0", v)
	}
	cases := map[string]float64{
		"":        0,
		"   ":     0,
		"123.45":  123.45,
		" 0.001 ": 0.001,
		"not-a-#": 0,
	}
	for in, want := range cases {
		s := in
		if got := parseFloatString(&s); got != want {
			t.Errorf("parseFloatString(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBreakevenMonths(t *testing.T) {
	cases := []struct {
		upfront float64
		monthly float64
		want    float64
	}{
		{0, 100, 0},     // no upfront → instant
		{1200, 100, 12}, // 1y to break even
		{600, 0, 0},     // no savings → guard against div-by-zero
		{600, -10, 0},   // negative savings clamped to zero
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := breakevenMonths(c.upfront, c.monthly); got != c.want {
			t.Errorf("breakevenMonths(%v, %v) = %v, want %v", c.upfront, c.monthly, got, c.want)
		}
	}
}

func TestSortSavingsRecsByEstimatedSavings(t *testing.T) {
	recs := []SavingsRecommendation{
		{Service: "low", EstimatedSavings: 10},
		{Service: "high", EstimatedSavings: 500},
		{Service: "mid", EstimatedSavings: 100},
		{Service: "zero", EstimatedSavings: 0},
	}
	sortSavingsRecsByEstimatedSavings(recs)
	wantOrder := []string{"high", "mid", "low", "zero"}
	for i, w := range wantOrder {
		if recs[i].Service != w {
			t.Errorf("position %d = %q, want %q (full: %+v)", i, recs[i].Service, w, recs)
		}
	}
}

func TestAppendCostNote(t *testing.T) {
	var s string
	appendCostNote(&s, "")
	if s != "" {
		t.Errorf("empty append should be noop, got %q", s)
	}
	appendCostNote(&s, "first")
	if s != "first" {
		t.Errorf("first append = %q, want %q", s, "first")
	}
	appendCostNote(&s, "second")
	if s != "first; second" {
		t.Errorf("second append = %q, want %q", s, "first; second")
	}
}

func TestSavingsPlanFamilyLabel(t *testing.T) {
	cases := map[types.SupportedSavingsPlansType]string{
		types.SupportedSavingsPlansTypeComputeSp:     "Compute",
		types.SupportedSavingsPlansTypeEc2InstanceSp: "EC2 Instance",
		types.SupportedSavingsPlansTypeSagemakerSp:   "SageMaker",
	}
	for in, want := range cases {
		if got := savingsPlanFamilyLabel(in); got != want {
			t.Errorf("savingsPlanFamilyLabel(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestRIServiceLabel(t *testing.T) {
	cases := map[string]string{
		// Full AWS Cost Explorer service names (the canonical inputs
		// the live API accepts and that fetchRIRecs now passes).
		"Amazon Elastic Compute Cloud - Compute": "EC2",
		"Amazon Relational Database Service":     "RDS",
		"Amazon ElastiCache":                     "ElastiCache",
		"Amazon OpenSearch Service":              "OpenSearch",
		"Amazon Redshift":                        "Redshift",
		"Amazon MemoryDB Service":                "MemoryDB",
		"Amazon DynamoDB Service":                "DynamoDB",
		// Legacy short codes preserved for backward-compat with any
		// caller still passing the SDK-style identifier.
		"AmazonEC2":               "EC2",
		"AmazonRDS":               "RDS",
		"AmazonElastiCache":       "ElastiCache",
		"AmazonOpenSearchService": "OpenSearch",
		"AmazonRedshift":          "Redshift",
		"SomethingElse":           "SomethingElse", // fallback
	}
	for in, want := range cases {
		if got := riServiceLabel(in); got != want {
			t.Errorf("riServiceLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstanceFamilyFromDetails(t *testing.T) {
	if v := instanceFamilyFromDetails(nil); v != "" {
		t.Errorf("nil details → %q, want empty", v)
	}

	ec2 := &types.InstanceDetails{
		EC2InstanceDetails: &types.EC2InstanceDetails{Family: strPtr("m5")},
	}
	if v := instanceFamilyFromDetails(ec2); v != "m5" {
		t.Errorf("ec2 → %q, want m5", v)
	}

	rds := &types.InstanceDetails{
		RDSInstanceDetails: &types.RDSInstanceDetails{Family: strPtr("db.r6g")},
	}
	if v := instanceFamilyFromDetails(rds); v != "db.r6g" {
		t.Errorf("rds → %q, want db.r6g", v)
	}

	// Empty details → empty.
	if v := instanceFamilyFromDetails(&types.InstanceDetails{}); v != "" {
		t.Errorf("empty details → %q, want empty", v)
	}
}

func TestSavingsPlanDetail(t *testing.T) {
	d := types.SavingsPlansPurchaseRecommendationDetail{
		SavingsPlansDetails: &types.SavingsPlansDetails{
			InstanceFamily: strPtr("m5"),
			Region:         strPtr("us-east-1"),
		},
		CurrentAverageHourlyOnDemandSpend: strPtr("0.50"),
	}
	got := savingsPlanDetail(d)
	for _, want := range []string{"family=m5", "region=us-east-1", "current $/hr=0.50"} {
		if !contains(got, want) {
			t.Errorf("savingsPlanDetail missing %q, got %q", want, got)
		}
	}
	// Empty input — should not panic, returns empty.
	if v := savingsPlanDetail(types.SavingsPlansPurchaseRecommendationDetail{}); v != "" {
		t.Errorf("empty detail → %q, want empty", v)
	}
}

func TestRIDetail(t *testing.T) {
	d := types.ReservationPurchaseRecommendationDetail{
		RecommendedNumberOfInstancesToPurchase: strPtr("3"),
		InstanceDetails: &types.InstanceDetails{
			EC2InstanceDetails: &types.EC2InstanceDetails{
				InstanceType: strPtr("m5.xlarge"),
				Region:       strPtr("us-east-1"),
			},
		},
	}
	got := riDetail(d)
	for _, want := range []string{"qty=3", "instance=m5.xlarge", "region=us-east-1"} {
		if !contains(got, want) {
			t.Errorf("riDetail missing %q, got %q", want, got)
		}
	}
}

// contains is a tiny helper to avoid importing strings just for the check.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
