package cost

import (
	"testing"
	"time"
)

func dailyTrend(values ...float64) []DailyCost {
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	out := make([]DailyCost, len(values))
	for i, v := range values {
		out[i] = DailyCost{Date: base.AddDate(0, 0, i), Cost: v, Provider: "test"}
	}
	return out
}

func TestDetectDailyAnomaly_FlagsSpike(t *testing.T) {
	a := DetectDailyAnomaly("ec2", "aws", dailyTrend(10, 11, 9, 10, 12, 11, 50), anomalyDeviationPct)
	if a == nil {
		t.Fatal("expected anomaly to be detected for 5x spike")
	}
	if a.PercentChange < 100 {
		t.Errorf("expected percent change > 100, got %.1f", a.PercentChange)
	}
	if a.Service != "ec2" || a.Provider != "aws" {
		t.Errorf("anomaly metadata mismatch: %+v", a)
	}
}

func TestDetectDailyAnomaly_FlagsDrop(t *testing.T) {
	a := DetectDailyAnomaly("ec2", "aws", dailyTrend(100, 100, 100, 100, 100, 100, 10), anomalyDeviationPct)
	if a == nil {
		t.Fatal("expected anomaly for 90% drop")
	}
	if a.PercentChange > -50 {
		t.Errorf("expected sharply negative change, got %.1f", a.PercentChange)
	}
}

func TestDetectDailyAnomaly_IgnoresWithinThreshold(t *testing.T) {
	a := DetectDailyAnomaly("ec2", "aws", dailyTrend(10, 11, 9, 10, 12, 11, 12), anomalyDeviationPct)
	if a != nil {
		t.Errorf("did not expect anomaly, got %+v", a)
	}
}

func TestDetectDailyAnomaly_IgnoresShortSeries(t *testing.T) {
	if DetectDailyAnomaly("ec2", "aws", dailyTrend(100), anomalyDeviationPct) != nil {
		t.Error("single-day series should not yield an anomaly")
	}
	if DetectDailyAnomaly("ec2", "aws", nil, anomalyDeviationPct) != nil {
		t.Error("empty series should not yield an anomaly")
	}
}

func TestDetectDailyAnomaly_IgnoresNearZeroBaseline(t *testing.T) {
	// Baseline of 0.01/day; a $1 spike is a 9999% change but the absolute
	// dollars are immaterial — we don't want to spam users with these.
	a := DetectDailyAnomaly("test", "test", dailyTrend(0.01, 0.01, 0.01, 0.01, 0.01, 0.01, 1.0), anomalyDeviationPct)
	if a != nil {
		t.Errorf("expected near-zero baseline to be filtered, got %+v", a)
	}
}

func TestIsUntaggedValue(t *testing.T) {
	cases := map[string]bool{
		"":                true,
		"  ":              true,
		"Environment$":    true,
		"NO VALUE":        true,
		"untagged":        true,
		"prod":            false,
		"Environment$dev": false,
	}
	for in, want := range cases {
		if got := isUntaggedValue(in); got != want {
			t.Errorf("isUntaggedValue(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDetectAnomaliesByProvider_PartitionsByProvider(t *testing.T) {
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	mk := func(provider string, values ...float64) []DailyCost {
		out := make([]DailyCost, len(values))
		for i, v := range values {
			out[i] = DailyCost{Date: base.AddDate(0, 0, i), Cost: v, Provider: provider}
		}
		return out
	}

	var trend []DailyCost
	trend = append(trend, mk("aws", 10, 10, 10, 10, 10, 10, 100)...)   // spike
	trend = append(trend, mk("gcp", 10, 11, 9, 10, 12, 11, 12)...)     // normal
	trend = append(trend, mk("azure", 10, 10, 10, 10, 10, 10, 0.5)...) // drop

	got := DetectAnomaliesByProvider(trend, anomalyDeviationPct)
	if len(got) != 2 {
		t.Fatalf("expected 2 anomalies (aws spike, azure drop), got %d: %+v", len(got), got)
	}
	// Sorted by absolute percent change descending — aws spike (+800%) > azure drop (-95%)
	if got[0].Provider != "aws" {
		t.Errorf("expected aws spike first, got %s", got[0].Provider)
	}
}
