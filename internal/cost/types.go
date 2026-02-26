package cost

import "time"

// CostSummary represents the overall cost summary across all providers
type CostSummary struct {
	TotalCost     float64        `json:"totalCost"`
	LastMonthCost float64        `json:"lastMonthCost"`
	Currency      string         `json:"currency"`
	Period        CostPeriod     `json:"period"`
	ProviderCosts []ProviderCost `json:"providerCosts"`
	TopServices   []ServiceCost  `json:"topServices"`
	DailyTrend    []DailyCost    `json:"dailyTrend"`
	Forecast      *CostForecast  `json:"forecast,omitempty"`
	LastUpdated   time.Time      `json:"lastUpdated"`
}

// CostPeriod represents the time period for cost data
type CostPeriod struct {
	StartDate   time.Time `json:"startDate"`
	EndDate     time.Time `json:"endDate"`
	Granularity string    `json:"granularity"` // daily, monthly
}

// ProviderCost represents costs for a single provider
type ProviderCost struct {
	Provider         string        `json:"provider"` // aws, gcp, azure, cloudflare, llm
	TotalCost        float64       `json:"totalCost"`
	Currency         string        `json:"currency"`
	ServiceBreakdown []ServiceCost `json:"serviceBreakdown"`
	Change           float64       `json:"change"` // % change from last period
}

// ServiceCost represents cost for a single service
type ServiceCost struct {
	Service       string  `json:"service"`
	Cost          float64 `json:"cost"`
	UsageQuantity float64 `json:"usageQuantity,omitempty"`
	UsageUnit     string  `json:"usageUnit,omitempty"`
	ResourceCount int     `json:"resourceCount"`
}

// TagCost represents cost grouped by tag
type TagCost struct {
	TagKey        string  `json:"tagKey"`
	TagValue      string  `json:"tagValue"`
	Cost          float64 `json:"cost"`
	ResourceCount int     `json:"resourceCount"`
}

// DailyCost represents cost for a single day
type DailyCost struct {
	Date     time.Time `json:"date"`
	Cost     float64   `json:"cost"`
	Provider string    `json:"provider,omitempty"`
}

// CostForecast represents cost forecast data
type CostForecast struct {
	EstimatedEndOfMonth float64 `json:"estimatedEndOfMonth"`
	EstimatedNextMonth  float64 `json:"estimatedNextMonth"`
	Confidence          float64 `json:"confidence"`
}

// CostAnomaly represents a detected cost anomaly
type CostAnomaly struct {
	Service       string  `json:"service"`
	Provider      string  `json:"provider"`
	ExpectedCost  float64 `json:"expectedCost"`
	ActualCost    float64 `json:"actualCost"`
	PercentChange float64 `json:"percentChange"`
	Description   string  `json:"description"`
}

// LLMUsage represents LLM usage data
type LLMUsage struct {
	Provider      string     `json:"provider"`
	Model         string     `json:"model"`
	InputTokens   int64      `json:"inputTokens"`
	OutputTokens  int64      `json:"outputTokens"`
	TotalTokens   int64      `json:"totalTokens"`
	EstimatedCost float64    `json:"estimatedCost"`
	RequestCount  int        `json:"requestCount"`
	Period        CostPeriod `json:"period"`
}

// LLMCostSummary represents aggregated LLM cost data
type LLMCostSummary struct {
	TotalCost     float64    `json:"totalCost"`
	TotalTokens   int64      `json:"totalTokens"`
	TotalRequests int        `json:"totalRequests"`
	ByProvider    []LLMUsage `json:"byProvider"`
	ByModel       []LLMUsage `json:"byModel"`
	Period        CostPeriod `json:"period"`
}

// CostExportRequest represents a request to export cost data
type CostExportRequest struct {
	Providers       []string          `json:"providers"`
	StartDate       string            `json:"startDate"`
	EndDate         string            `json:"endDate"`
	Format          string            `json:"format"` // csv, pdf, json
	GroupBy         string            `json:"groupBy"` // provider, service, tag
	TagFilters      map[string]string `json:"tagFilters"`
	IncludeForecast bool              `json:"includeForecast"`
}

// CostTrendResponse represents the response for cost trend data
type CostTrendResponse struct {
	Trend       []DailyCost `json:"trend"`
	Granularity string      `json:"granularity"`
}

// CostAnomaliesResponse represents the response for cost anomalies
type CostAnomaliesResponse struct {
	Anomalies []CostAnomaly `json:"anomalies"`
}

// ProvidersResponse represents the response for configured providers
type ProvidersResponse struct {
	Providers []string `json:"providers"`
}

// TagsResponse represents the response for cost by tags
type TagsResponse struct {
	Tags   []TagCost  `json:"tags"`
	TagKey string     `json:"tagKey"`
	Period CostPeriod `json:"period"`
}
