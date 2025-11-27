// Package semantic provides lightweight NLP intent analysis for user queries.
package semantic

import (
	"math"
	"strings"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

func NewAnalyzer() *Analyzer {
	return &Analyzer{
		KeywordWeights:  cloneKeywordWeights(defaultKeywordWeights),
		ContextPatterns: cloneContextPatterns(defaultContextPatterns),
		ServiceMapping:  cloneServiceMapping(defaultServiceMapping),
		IntentSignals:   cloneIntentSignals(defaultIntentSignals),
		UrgencyKeywords: cloneUrgencyKeywords(defaultUrgencyKeywords),
		TimeFrameWords:  cloneTimeFrameWords(defaultTimeFrameWords),
	}
}

// AnalyzeQuery performs a purely lexical pass that classifies the user's
// question without calling external NLP services. It scores intent by summing
// word weights, infers urgency and timeframe from keyword buckets, tags cloud
// services via the mapping table, and fills in likely data types so downstream
// planners can quickly decide which collectors to run.
func (sa *Analyzer) AnalyzeQuery(query string) model.QueryIntent {
	queryLower := strings.ToLower(query)
	words := strings.Fields(queryLower)

	intent := model.QueryIntent{
		Confidence:     0.0,
		TargetServices: []string{},
		Urgency:        "medium",
		TimeFrame:      "recent",
		DataTypes:      []string{},
	}

	intentScores := make(map[string]float64)
	for intentType, signals := range sa.IntentSignals {
		score := 0.0
		for _, word := range words {
			if weight, exists := signals[word]; exists {
				score += weight
			}
		}
		intentScores[intentType] = score
	}

	maxScore := 0.0
	for intentType, score := range intentScores {
		if score > maxScore {
			maxScore = score
			intent.Primary = intentType
		}
	}

	if len(words) > 0 {
		intent.Confidence = math.Min(maxScore/float64(len(words)), 1.0)
	}

	for service, keywords := range sa.ServiceMapping {
		for _, keyword := range keywords {
			if strings.Contains(queryLower, keyword) {
				intent.TargetServices = append(intent.TargetServices, service)
				break
			}
		}
	}

	urgencyScore := 0.0
	for _, word := range words {
		if weight, exists := sa.UrgencyKeywords[word]; exists {
			urgencyScore += weight
		}
	}
	switch {
	case urgencyScore >= 1.0:
		intent.Urgency = "critical"
	case urgencyScore >= 0.7:
		intent.Urgency = "high"
	case urgencyScore >= 0.3:
		intent.Urgency = "medium"
	default:
		intent.Urgency = "low"
	}

	for _, word := range words {
		if timeFrame, exists := sa.TimeFrameWords[word]; exists {
			intent.TimeFrame = timeFrame
			break
		}
	}

	dataTypeKeywords := map[string]string{
		"log":     "logs",
		"logs":    "logs",
		"metric":  "metrics",
		"metrics": "metrics",
		"config":  "config",
		"status":  "status",
	}
	for _, word := range words {
		if dataType, exists := dataTypeKeywords[word]; exists {
			intent.DataTypes = append(intent.DataTypes, dataType)
		}
	}

	if len(intent.DataTypes) == 0 {
		switch intent.Primary {
		case "troubleshoot":
			intent.DataTypes = []string{"logs", "metrics", "status"}
		case "monitor":
			intent.DataTypes = []string{"metrics", "status"}
		case "analyze":
			intent.DataTypes = []string{"logs", "metrics"}
		default:
			intent.DataTypes = []string{"status"}
		}
	}

	return intent
}
