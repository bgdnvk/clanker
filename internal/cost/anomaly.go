package cost

import (
	"fmt"
	"sort"
	"time"
)

// anomalyDeviationPct is the absolute percent deviation from the rolling
// baseline that flags a single-day data point as an anomaly. 30% matches
// the previous AWS-specific heuristic and is conservative enough to avoid
// flagging weekend dips on noisy accounts.
const anomalyDeviationPct = 30.0

// minAnomalyBaseline filters out near-zero baselines that produce
// meaningless percent changes.
const minAnomalyBaseline = 0.50

// DetectDailyAnomaly compares the most recent day in trend against the
// average of the prior days and returns a CostAnomaly if the deviation
// exceeds thresholdPct in either direction. Returns nil when there isn't
// enough data, the baseline is too small to be meaningful, or no anomaly
// is found.
//
// Trend is expected to be sorted ascending by date but is sorted defensively.
func DetectDailyAnomaly(service, provider string, trend []DailyCost, thresholdPct float64) *CostAnomaly {
	if len(trend) < 2 {
		return nil
	}
	sorted := make([]DailyCost, len(trend))
	copy(sorted, trend)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date.Before(sorted[j].Date) })

	var total float64
	for _, dc := range sorted[:len(sorted)-1] {
		total += dc.Cost
	}
	avg := total / float64(len(sorted)-1)
	if avg < minAnomalyBaseline {
		return nil
	}

	today := sorted[len(sorted)-1]
	change := ((today.Cost - avg) / avg) * 100
	if change <= thresholdPct && change >= -thresholdPct {
		return nil
	}
	return &CostAnomaly{
		Service:       service,
		Provider:      provider,
		ExpectedCost:  avg,
		ActualCost:    today.Cost,
		PercentChange: change,
		Description:   fmt.Sprintf("Daily cost deviation of %.1f%% from %d-day average", change, len(sorted)-1),
	}
}

// DetectAnomaliesByProvider partitions trend by provider and runs
// DetectDailyAnomaly per partition. Used as the aggregator-level fallback
// so providers that don't implement AnomalyProvider still get coverage.
func DetectAnomaliesByProvider(trend []DailyCost, thresholdPct float64) []CostAnomaly {
	groups := make(map[string][]DailyCost)
	for _, dc := range trend {
		key := dc.Provider
		if key == "" {
			key = "unknown"
		}
		groups[key] = append(groups[key], dc)
	}

	out := make([]CostAnomaly, 0, len(groups))
	for provider, days := range groups {
		if a := DetectDailyAnomaly(fmt.Sprintf("Total %s", provider), provider, days, thresholdPct); a != nil {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PercentChange > out[j].PercentChange })
	return out
}

// since7Days returns a [end-7d, end) window suitable for daily anomaly
// detection, exposed so callers can keep one source of truth for the window.
func since7Days(now time.Time) (time.Time, time.Time) {
	return now.AddDate(0, 0, -7), now
}
