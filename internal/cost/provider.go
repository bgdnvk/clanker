package cost

import (
	"context"
	"time"
)

// Provider defines the interface for fetching cost data from cloud providers
type Provider interface {
	// GetName returns the provider identifier (aws, gcp, azure, cloudflare)
	GetName() string

	// IsConfigured returns true if the provider has valid credentials
	IsConfigured() bool

	// GetCosts returns total costs for the given time period
	GetCosts(ctx context.Context, start, end time.Time) (*ProviderCost, error)

	// GetCostsByService returns costs broken down by service
	GetCostsByService(ctx context.Context, start, end time.Time) ([]ServiceCost, error)

	// GetDailyTrend returns daily cost breakdown
	GetDailyTrend(ctx context.Context, start, end time.Time) ([]DailyCost, error)
}

// ForecastProvider is implemented by providers that support cost forecasting
type ForecastProvider interface {
	Provider
	GetForecast(ctx context.Context) (*CostForecast, error)
}

// AnomalyProvider is implemented by providers that support anomaly detection
type AnomalyProvider interface {
	Provider
	GetAnomalies(ctx context.Context) ([]CostAnomaly, error)
}

// TagProvider is implemented by providers that support cost allocation tags
type TagProvider interface {
	Provider
	GetCostsByTag(ctx context.Context, tagKey string, start, end time.Time) ([]TagCost, error)
}
