package cost

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"
)

// stubProvider implements Provider only — no AnomalyProvider — so the
// aggregator's fallback path will run for it.
type stubProvider struct {
	name       string
	configured bool
	costs      *ProviderCost
	costsErr   error
	trend      []DailyCost
	trendErr   error
}

func (s *stubProvider) GetName() string    { return s.name }
func (s *stubProvider) IsConfigured() bool { return s.configured }
func (s *stubProvider) GetCosts(_ context.Context, _, _ time.Time) (*ProviderCost, error) {
	return s.costs, s.costsErr
}
func (s *stubProvider) GetCostsByService(_ context.Context, _, _ time.Time) ([]ServiceCost, error) {
	if s.costs == nil {
		return nil, nil
	}
	return s.costs.ServiceBreakdown, nil
}
func (s *stubProvider) GetDailyTrend(_ context.Context, _, _ time.Time) ([]DailyCost, error) {
	return s.trend, s.trendErr
}

// stubAnomalyProvider extends stubProvider to return its own anomalies — used
// to verify the aggregator skips the fallback for providers that already
// implement AnomalyProvider.
type stubAnomalyProvider struct {
	stubProvider
	anomalies    []CostAnomaly
	anomaliesErr error
}

func (s *stubAnomalyProvider) GetAnomalies(_ context.Context) ([]CostAnomaly, error) {
	return s.anomalies, s.anomaliesErr
}

// makeSpike returns a 7-day trend ending in a spike that DetectDailyAnomaly
// will flag (>30% deviation from the 6-day average).
func makeSpike(provider string, base time.Time) []DailyCost {
	out := make([]DailyCost, 7)
	for i := 0; i < 6; i++ {
		out[i] = DailyCost{Date: base.AddDate(0, 0, i), Cost: 10, Provider: provider}
	}
	out[6] = DailyCost{Date: base.AddDate(0, 0, 6), Cost: 100, Provider: provider}
	return out
}

func TestGetAnomalies_FallbackForNonAnomalyProvider(t *testing.T) {
	base := time.Now().UTC().AddDate(0, 0, -7)

	gcp := &stubProvider{
		name:       "gcp",
		configured: true,
		trend:      makeSpike("gcp", base),
	}

	agg := NewAggregator(false)
	agg.RegisterProvider(gcp)

	resp, err := agg.GetAnomalies(context.Background())
	if err != nil {
		t.Fatalf("GetAnomalies: %v", err)
	}
	if len(resp.Anomalies) != 1 {
		t.Fatalf("expected 1 fallback anomaly for gcp, got %d: %+v", len(resp.Anomalies), resp.Anomalies)
	}
	if resp.Anomalies[0].Provider != "gcp" {
		t.Errorf("expected gcp provider, got %q", resp.Anomalies[0].Provider)
	}
}

func TestGetAnomalies_AnomalyProviderSkipsFallback(t *testing.T) {
	base := time.Now().UTC().AddDate(0, 0, -7)

	// AWS provider declares its own anomaly (200% spike on a service) and
	// also has a daily trend that would otherwise trigger the fallback. We
	// want exactly the provider-supplied anomaly, not a duplicate from the
	// fallback heuristic.
	aws := &stubAnomalyProvider{
		stubProvider: stubProvider{
			name:       "aws",
			configured: true,
			trend:      makeSpike("aws", base),
		},
		anomalies: []CostAnomaly{
			{Service: "EC2", Provider: "aws", PercentChange: 200, ExpectedCost: 10, ActualCost: 30},
		},
	}

	agg := NewAggregator(false)
	agg.RegisterProvider(aws)

	resp, err := agg.GetAnomalies(context.Background())
	if err != nil {
		t.Fatalf("GetAnomalies: %v", err)
	}
	if len(resp.Anomalies) != 1 {
		t.Fatalf("expected 1 anomaly (no fallback for AnomalyProvider), got %d: %+v", len(resp.Anomalies), resp.Anomalies)
	}
	if resp.Anomalies[0].Service != "EC2" {
		t.Errorf("expected provider-supplied EC2 anomaly, got %q", resp.Anomalies[0].Service)
	}
}

func TestGetAnomalies_MixedProvidersBothSurface(t *testing.T) {
	base := time.Now().UTC().AddDate(0, 0, -7)

	aws := &stubAnomalyProvider{
		stubProvider: stubProvider{name: "aws", configured: true},
		anomalies: []CostAnomaly{
			{Service: "EC2", Provider: "aws", PercentChange: 200},
		},
	}
	gcp := &stubProvider{name: "gcp", configured: true, trend: makeSpike("gcp", base)}

	agg := NewAggregator(false)
	agg.RegisterProvider(aws)
	agg.RegisterProvider(gcp)

	resp, err := agg.GetAnomalies(context.Background())
	if err != nil {
		t.Fatalf("GetAnomalies: %v", err)
	}
	if len(resp.Anomalies) != 2 {
		t.Fatalf("expected aws+gcp anomalies, got %d: %+v", len(resp.Anomalies), resp.Anomalies)
	}

	providers := make([]string, 0, 2)
	for _, a := range resp.Anomalies {
		providers = append(providers, a.Provider)
	}
	sort.Strings(providers)
	if providers[0] != "aws" || providers[1] != "gcp" {
		t.Errorf("expected providers [aws gcp], got %v", providers)
	}

	// Sorted by abs(percentChange) descending. makeSpike produces ~900%
	// (100 vs a 6-day average of 10), which beats aws's stubbed 200%.
	if resp.Anomalies[0].Provider != "gcp" {
		t.Errorf("expected gcp first (heuristic 900%% > aws stub 200%%), got %q", resp.Anomalies[0].Provider)
	}
}

func TestGetAnomalies_AnomalyProviderError_StillRunsOthers(t *testing.T) {
	base := time.Now().UTC().AddDate(0, 0, -7)

	failingAws := &stubAnomalyProvider{
		stubProvider: stubProvider{name: "aws", configured: true},
		anomaliesErr: errors.New("cost explorer 503"),
	}
	gcp := &stubProvider{name: "gcp", configured: true, trend: makeSpike("gcp", base)}

	agg := NewAggregator(false)
	agg.RegisterProvider(failingAws)
	agg.RegisterProvider(gcp)

	resp, err := agg.GetAnomalies(context.Background())
	if err != nil {
		t.Fatalf("GetAnomalies should not propagate provider errors, got: %v", err)
	}
	if len(resp.Anomalies) != 1 {
		t.Fatalf("expected gcp fallback anomaly, got %d: %+v", len(resp.Anomalies), resp.Anomalies)
	}
	if resp.Anomalies[0].Provider != "gcp" {
		t.Errorf("expected gcp, got %q", resp.Anomalies[0].Provider)
	}
}

func TestGetAnomalies_TrendErrorTreatedAsNoData(t *testing.T) {
	gcp := &stubProvider{
		name:       "gcp",
		configured: true,
		trendErr:   errors.New("billing api throttled"),
	}

	agg := NewAggregator(false)
	agg.RegisterProvider(gcp)

	resp, err := agg.GetAnomalies(context.Background())
	if err != nil {
		t.Fatalf("GetAnomalies: %v", err)
	}
	if len(resp.Anomalies) != 0 {
		t.Errorf("expected no anomalies when trend fetch fails, got %+v", resp.Anomalies)
	}
}

func TestGetAnomalies_UnconfiguredProvidersSkipped(t *testing.T) {
	base := time.Now().UTC().AddDate(0, 0, -7)

	configured := &stubProvider{name: "gcp", configured: true, trend: makeSpike("gcp", base)}
	unconfigured := &stubProvider{name: "azure", configured: false, trend: makeSpike("azure", base)}

	agg := NewAggregator(false)
	agg.RegisterProvider(configured)
	agg.RegisterProvider(unconfigured)

	resp, err := agg.GetAnomalies(context.Background())
	if err != nil {
		t.Fatalf("GetAnomalies: %v", err)
	}
	if len(resp.Anomalies) != 1 {
		t.Fatalf("expected only configured-provider anomaly, got %d: %+v", len(resp.Anomalies), resp.Anomalies)
	}
	if resp.Anomalies[0].Provider != "gcp" {
		t.Errorf("expected gcp, got %q", resp.Anomalies[0].Provider)
	}
}

// stubTagProvider extends stubProvider with TagProvider for AuditTags tests.
type stubTagProvider struct {
	stubProvider
	byKey map[string][]TagCost
}

func (s *stubTagProvider) GetCostsByTag(_ context.Context, key string, _, _ time.Time) ([]TagCost, error) {
	return s.byKey[key], nil
}

func TestAuditTags_PartitionsTaggedAndUntagged(t *testing.T) {
	aws := &stubTagProvider{
		stubProvider: stubProvider{name: "aws", configured: true},
		byKey: map[string][]TagCost{
			"Environment": {
				{TagKey: "Environment", TagValue: "prod", Cost: 100},
				{TagKey: "Environment", TagValue: "", Cost: 25},
			},
			"Owner": {
				{TagKey: "Owner", TagValue: "team-a", Cost: 50},
			},
		},
	}

	agg := NewAggregator(false)
	agg.RegisterProvider(aws)

	report, err := agg.AuditTags(context.Background(), []string{"Environment", "Owner"}, time.Now().AddDate(0, 0, -7), time.Now())
	if err != nil {
		t.Fatalf("AuditTags: %v", err)
	}
	if len(report.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(report.Entries), report.Entries)
	}

	byKey := map[string]TagAuditEntry{}
	for _, e := range report.Entries {
		byKey[e.TagKey] = e
	}

	env := byKey["Environment"]
	if env.TotalCost != 125 || env.UntaggedCost != 25 || env.TaggedValues != 1 {
		t.Errorf("Environment audit wrong: %+v", env)
	}
	if got := fmt.Sprintf("%.1f", env.UntaggedPct); got != "20.0" {
		t.Errorf("Environment UntaggedPct = %s, want 20.0", got)
	}

	owner := byKey["Owner"]
	if owner.UntaggedCost != 0 || owner.UntaggedSeen {
		t.Errorf("Owner audit should have no untagged spend, got %+v", owner)
	}
}

func TestAuditTags_UnsupportedNumOnlyCountsConfigured(t *testing.T) {
	// One tag-aware provider (configured), one configured non-tag provider,
	// one unconfigured provider that would otherwise skew the count.
	awsTag := &stubTagProvider{stubProvider: stubProvider{name: "aws", configured: true}, byKey: map[string][]TagCost{}}
	gcpNoTag := &stubProvider{name: "gcp", configured: true}
	azureUnconfigured := &stubProvider{name: "azure", configured: false}

	agg := NewAggregator(false)
	agg.RegisterProvider(awsTag)
	agg.RegisterProvider(gcpNoTag)
	agg.RegisterProvider(azureUnconfigured)

	report, err := agg.AuditTags(context.Background(), []string{"Environment"}, time.Now().AddDate(0, 0, -7), time.Now())
	if err != nil {
		t.Fatalf("AuditTags: %v", err)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(report.Entries))
	}
	got := report.Entries[0]
	// configuredProviders=2 (aws+gcp), tagProviders=1 (aws). Unsupported = 1.
	if got.UnsupportedNum != 1 {
		t.Errorf("UnsupportedNum = %d, want 1 (azure unconfigured must be ignored)", got.UnsupportedNum)
	}
	if got.ProvidersSeen != 1 {
		t.Errorf("ProvidersSeen = %d, want 1", got.ProvidersSeen)
	}
}

func TestAuditTags_SkipsBlankKeys(t *testing.T) {
	agg := NewAggregator(false)
	agg.RegisterProvider(&stubTagProvider{
		stubProvider: stubProvider{name: "aws", configured: true},
		byKey:        map[string][]TagCost{"Environment": {{TagValue: "prod", Cost: 1}}},
	})

	report, err := agg.AuditTags(context.Background(), []string{"  ", "Environment", ""}, time.Now().AddDate(0, 0, -7), time.Now())
	if err != nil {
		t.Fatalf("AuditTags: %v", err)
	}
	if len(report.Entries) != 1 || report.Entries[0].TagKey != "Environment" {
		t.Errorf("expected only Environment entry, got %+v", report.Entries)
	}
}
