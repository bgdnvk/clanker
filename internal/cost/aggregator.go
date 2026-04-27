package cost

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// Aggregator collects cost data from multiple providers
type Aggregator struct {
	providers []Provider
	debug     bool
}

// NewAggregator creates a new cost aggregator
func NewAggregator(debug bool) *Aggregator {
	return &Aggregator{
		providers: make([]Provider, 0),
		debug:     debug,
	}
}

// RegisterProvider adds a cost provider to the aggregator
func (a *Aggregator) RegisterProvider(p Provider) {
	if p == nil {
		return
	}
	a.providers = append(a.providers, p)
	if a.debug {
		log.Printf("[cost] registered provider: %s (configured: %v)", p.GetName(), p.IsConfigured())
	}
}

// GetConfiguredProviders returns a list of configured provider names
func (a *Aggregator) GetConfiguredProviders() []string {
	var names []string
	for _, p := range a.providers {
		if p.IsConfigured() {
			names = append(names, p.GetName())
		}
	}
	sort.Strings(names)
	return names
}

// GetSummary returns aggregated costs from all configured providers
func (a *Aggregator) GetSummary(ctx context.Context, start, end time.Time) (*CostSummary, error) {
	var (
		wg            sync.WaitGroup
		mu            sync.Mutex
		providerCosts []ProviderCost
		allServices   []ServiceCost
		allDailyTrend []DailyCost
		totalCost     float64
		lastMonthCost float64
	)

	// Calculate last month's date range
	now := time.Now()
	thisMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonthStart := thisMonthStart.AddDate(0, -1, 0)
	lastMonthEnd := thisMonthStart

	if a.debug {
		log.Printf("[cost] fetching costs from %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
		log.Printf("[cost] fetching last month costs from %s to %s", lastMonthStart.Format("2006-01-02"), lastMonthEnd.Format("2006-01-02"))
	}

	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}

		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Get current period costs
			costs, err := p.GetCosts(ctx, start, end)
			if err != nil {
				if a.debug {
					log.Printf("[cost] error fetching costs from %s: %v", p.GetName(), err)
				}
			} else if costs != nil {
				mu.Lock()
				providerCosts = append(providerCosts, *costs)
				totalCost += costs.TotalCost
				allServices = append(allServices, costs.ServiceBreakdown...)
				mu.Unlock()
			}

			// Get last month's costs
			lmCosts, err := p.GetCosts(ctx, lastMonthStart, lastMonthEnd)
			if err != nil {
				if a.debug {
					log.Printf("[cost] error fetching last month costs from %s: %v", p.GetName(), err)
				}
			} else if lmCosts != nil {
				if a.debug {
					log.Printf("[cost] last month cost from %s: $%.2f", p.GetName(), lmCosts.TotalCost)
				}
				mu.Lock()
				lastMonthCost += lmCosts.TotalCost
				mu.Unlock()
			}

			// Get daily trend
			trend, err := p.GetDailyTrend(ctx, start, end)
			if err != nil {
				if a.debug {
					log.Printf("[cost] error fetching trend from %s: %v", p.GetName(), err)
				}
			} else {
				mu.Lock()
				allDailyTrend = append(allDailyTrend, trend...)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if a.debug {
		log.Printf("[cost] total last month cost: $%.2f", lastMonthCost)
	}

	// Sort services by cost (descending) and take top 10
	sort.Slice(allServices, func(i, j int) bool {
		return allServices[i].Cost > allServices[j].Cost
	})
	topServices := allServices
	if len(topServices) > 10 {
		topServices = topServices[:10]
	}

	// Sort provider costs by total cost (descending)
	sort.Slice(providerCosts, func(i, j int) bool {
		return providerCosts[i].TotalCost > providerCosts[j].TotalCost
	})

	// Sort daily trend by date
	sort.Slice(allDailyTrend, func(i, j int) bool {
		return allDailyTrend[i].Date.Before(allDailyTrend[j].Date)
	})

	// Try to get forecast from any provider that supports it
	var forecast *CostForecast
	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}
		if fp, ok := p.(ForecastProvider); ok {
			f, err := fp.GetForecast(ctx)
			if err == nil && f != nil {
				forecast = f
				break
			}
		}
	}

	return &CostSummary{
		TotalCost:     totalCost,
		LastMonthCost: lastMonthCost,
		Currency:      "USD",
		Period: CostPeriod{
			StartDate: start,
			EndDate:   end,
		},
		ProviderCosts: providerCosts,
		TopServices:   topServices,
		DailyTrend:    allDailyTrend,
		Forecast:      forecast,
		LastUpdated:   time.Now(),
	}, nil
}

// GetByProvider returns costs for a specific provider
func (a *Aggregator) GetByProvider(ctx context.Context, providerName string, start, end time.Time) (*ProviderCost, error) {
	for _, p := range a.providers {
		if p.GetName() == providerName && p.IsConfigured() {
			return p.GetCosts(ctx, start, end)
		}
	}
	return nil, nil
}

// GetTrend returns daily cost trend across all providers
func (a *Aggregator) GetTrend(ctx context.Context, start, end time.Time) (*CostTrendResponse, error) {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		trend []DailyCost
	)

	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}

		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			t, err := p.GetDailyTrend(ctx, start, end)
			if err != nil {
				if a.debug {
					log.Printf("[cost] error fetching trend from %s: %v", p.GetName(), err)
				}
				return
			}
			mu.Lock()
			trend = append(trend, t...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Sort by date
	sort.Slice(trend, func(i, j int) bool {
		return trend[i].Date.Before(trend[j].Date)
	})

	return &CostTrendResponse{
		Trend:       trend,
		Granularity: "daily",
	}, nil
}

// GetForecast returns cost forecast from the first provider that supports it
func (a *Aggregator) GetForecast(ctx context.Context) (*CostForecast, error) {
	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}
		if fp, ok := p.(ForecastProvider); ok {
			forecast, err := fp.GetForecast(ctx)
			if err == nil && forecast != nil {
				return forecast, nil
			}
		}
	}
	return nil, nil
}

// GetAnomalies returns cost anomalies from all providers. Providers that
// implement AnomalyProvider supply their own (typically richer, per-service)
// findings. For providers that don't, we run a generic z-style daily-deviation
// detector against their daily trend so coverage isn't AWS-only.
func (a *Aggregator) GetAnomalies(ctx context.Context) (*CostAnomaliesResponse, error) {
	var (
		wg               sync.WaitGroup
		mu               sync.Mutex
		anomalies        []CostAnomaly
		fallbackTrend    []DailyCost
		anomalyProviders = make(map[string]bool)
	)

	now := time.Now()
	start, end := since7Days(now)

	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}
		if ap, ok := p.(AnomalyProvider); ok {
			anomalyProviders[p.GetName()] = true
			ap := ap
			wg.Add(1)
			go func() {
				defer wg.Done()
				found, err := ap.GetAnomalies(ctx)
				if err != nil {
					if a.debug {
						log.Printf("[cost] anomaly fetch from %s: %v", ap.GetName(), err)
					}
					return
				}
				mu.Lock()
				anomalies = append(anomalies, found...)
				mu.Unlock()
			}()
		}
	}

	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}
		if anomalyProviders[p.GetName()] {
			continue
		}
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			trend, err := p.GetDailyTrend(ctx, start, end)
			if err != nil {
				if a.debug {
					log.Printf("[cost] daily trend for fallback anomaly detection on %s: %v", p.GetName(), err)
				}
				return
			}
			for i := range trend {
				if trend[i].Provider == "" {
					trend[i].Provider = p.GetName()
				}
			}
			mu.Lock()
			fallbackTrend = append(fallbackTrend, trend...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	anomalies = append(anomalies, DetectAnomaliesByProvider(fallbackTrend, anomalyDeviationPct)...)

	// Sort by absolute percent change descending — both spikes and drops
	// should bubble up, not just positive deltas.
	sort.Slice(anomalies, func(i, j int) bool {
		return absFloat(anomalies[i].PercentChange) > absFloat(anomalies[j].PercentChange)
	})

	return &CostAnomaliesResponse{
		Anomalies: anomalies,
	}, nil
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// AuditTags runs a tag-compliance audit for the supplied required tag keys.
// For each key it groups costs by tag value across providers and reports the
// portion of spend that fell into the untagged bucket (Cost Explorer returns
// these as the empty tag value).
func (a *Aggregator) AuditTags(ctx context.Context, requiredKeys []string, start, end time.Time) (*TagAuditReport, error) {
	if len(requiredKeys) == 0 {
		return &TagAuditReport{Period: CostPeriod{StartDate: start, EndDate: end}}, nil
	}

	report := &TagAuditReport{
		Period:  CostPeriod{StartDate: start, EndDate: end},
		Entries: make([]TagAuditEntry, 0, len(requiredKeys)),
	}

	tagProviders := 0
	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}
		if _, ok := p.(TagProvider); ok {
			tagProviders++
		}
	}

	for _, key := range requiredKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		resp, err := a.GetTags(ctx, key, start, end)
		if err != nil {
			if a.debug {
				log.Printf("[cost] tag audit: %s: %v", key, err)
			}
			continue
		}

		entry := TagAuditEntry{TagKey: key, ProvidersSeen: tagProviders}
		entry.UnsupportedNum = len(a.providers) - tagProviders
		for _, t := range resp.Tags {
			entry.TotalCost += t.Cost
			if isUntaggedValue(t.TagValue) {
				entry.UntaggedCost += t.Cost
				entry.UntaggedSeen = true
			} else {
				entry.TaggedValues++
			}
		}
		if entry.TotalCost > 0 {
			entry.UntaggedPct = (entry.UntaggedCost / entry.TotalCost) * 100
		}
		report.Entries = append(report.Entries, entry)
	}

	return report, nil
}

// isUntaggedValue identifies the untagged bucket returned by AWS Cost Explorer
// when grouping by a tag the resource doesn't carry.
func isUntaggedValue(v string) bool {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return true
	}
	// AWS prefixes the group key as "tagKey$value"; an unset value comes
	// through as "tagKey$" (i.e. trailing dollar with empty suffix).
	if strings.HasSuffix(trimmed, "$") {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "no value", "(notagkey)", "untagged":
		return true
	}
	return false
}

// GetTags returns costs grouped by tag across all providers
func (a *Aggregator) GetTags(ctx context.Context, tagKey string, start, end time.Time) (*TagsResponse, error) {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		tags []TagCost
	)

	for _, p := range a.providers {
		if !p.IsConfigured() {
			continue
		}
		if tp, ok := p.(TagProvider); ok {
			tp := tp
			wg.Add(1)
			go func() {
				defer wg.Done()
				t, err := tp.GetCostsByTag(ctx, tagKey, start, end)
				if err != nil {
					return
				}
				mu.Lock()
				tags = append(tags, t...)
				mu.Unlock()
			}()
		}
	}

	wg.Wait()

	// Sort by cost descending
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Cost > tags[j].Cost
	})

	return &TagsResponse{
		Tags:   tags,
		TagKey: tagKey,
	}, nil
}
