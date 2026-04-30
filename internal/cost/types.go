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
	Format          string            `json:"format"`  // csv, pdf, json
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

// TagAuditEntry summarizes tagging compliance for a single required tag key.
type TagAuditEntry struct {
	TagKey         string  `json:"tagKey"`
	TotalCost      float64 `json:"totalCost"`
	UntaggedCost   float64 `json:"untaggedCost"`
	UntaggedPct    float64 `json:"untaggedPct"`
	TaggedValues   int     `json:"taggedValues"`
	UntaggedSeen   bool    `json:"untaggedSeen"`
	ProvidersSeen  int     `json:"providersSeen"`
	UnsupportedNum int     `json:"unsupportedProviders"`
}

// TagAuditReport is the response of a tag-compliance audit.
type TagAuditReport struct {
	Entries []TagAuditEntry `json:"entries"`
	Period  CostPeriod      `json:"period"`
}

// SavingsKind names the recommendation type — "savings_plan" (AWS
// Savings Plan, also covers Compute / EC2 Instance / SageMaker SP) or
// "reserved_instance" (RDS / ElastiCache / Redshift / OpenSearch RI).
type SavingsKind string

const (
	SavingsKindSavingsPlan      SavingsKind = "savings_plan"
	SavingsKindReservedInstance SavingsKind = "reserved_instance"
)

// SavingsRecommendation is one purchase recommendation row.
type SavingsRecommendation struct {
	Provider           string      `json:"provider"`
	Kind               SavingsKind `json:"kind"`
	Service            string      `json:"service,omitempty"`       // e.g. "EC2", "RDS"
	Family             string      `json:"family,omitempty"`        // e.g. "Compute", "EC2 Instance"
	Term               string      `json:"term"`                    // "ONE_YEAR" or "THREE_YEARS"
	PaymentOption      string      `json:"paymentOption,omitempty"` // "ALL_UPFRONT" / "PARTIAL_UPFRONT" / "NO_UPFRONT"
	UpfrontCost        float64     `json:"upfrontCost"`             // USD
	HourlyCommitment   float64     `json:"hourlyCommitment,omitempty"`
	EstimatedSavings   float64     `json:"estimatedSavingsUsd"`       // monthly USD
	EstimatedSavingsPc float64     `json:"estimatedSavingsPct"`       // 0..100
	BreakevenMonths    float64     `json:"breakevenMonths,omitempty"` // upfront / monthly savings; 0 if NO_UPFRONT
	Detail             string      `json:"detail,omitempty"`
}

// SavingsReport rolls up commitment-purchase recommendations.
type SavingsReport struct {
	GeneratedAt           time.Time               `json:"generatedAt"`
	Provider              string                  `json:"provider"`
	Lookback              string                  `json:"lookback"`
	Term                  string                  `json:"term"`
	Recommendations       []SavingsRecommendation `json:"recommendations,omitempty"`
	TotalEstimatedSavings float64                 `json:"totalEstimatedSavingsUsd"`
	Notes                 string                  `json:"notes,omitempty"`
}
