package semantic

// KeywordWeights biases confidence toward terms that historically correlate with outages.
type KeywordWeights map[string]float64

// ContextPatterns groups workflow-specific hint words (monitoring vs troubleshooting, etc.).
type ContextPatterns map[string][]string

// ServiceMapping tags AWS services based on keywords embedded in the query.
type ServiceMapping map[string][]string

// IntentSignals describes per-intent weights used during scoring.
type IntentSignals map[string]map[string]float64

// UrgencyKeywords help translate user language into urgency buckets.
type UrgencyKeywords map[string]float64

// TimeFrameWords maps colloquial phrases to time windows like historical or recent.
type TimeFrameWords map[string]string

// Analyzer keeps lightweight lexical resources used during semantic classification.
type Analyzer struct {
	KeywordWeights  KeywordWeights
	ContextPatterns ContextPatterns
	ServiceMapping  ServiceMapping
	IntentSignals   IntentSignals
	UrgencyKeywords UrgencyKeywords
	TimeFrameWords  TimeFrameWords
}
