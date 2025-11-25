// Package semantic provides lightweight NLP intent analysis for user queries.
package semantic

import (
	"math"
	"strings"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

type Analyzer struct {
	KeywordWeights  map[string]float64
	ContextPatterns map[string][]string
	ServiceMapping  map[string][]string
	IntentSignals   map[string]map[string]float64
	UrgencyKeywords map[string]float64
	TimeFrameWords  map[string]string
}

func NewAnalyzer() *Analyzer {
	return &Analyzer{
		KeywordWeights: map[string]float64{
			"error":    1.0,
			"failed":   0.9,
			"warning":  0.7,
			"critical": 1.0,
			"down":     0.9,
			"slow":     0.6,
			"timeout":  0.8,
			"crash":    1.0,
			"debug":    0.3,
			"info":     0.2,
			"success":  0.1,
			"healthy":  0.1,
		},
		ContextPatterns: map[string][]string{
			"troubleshoot": {"error", "failed", "broken", "issue", "problem", "trouble"},
			"monitor":      {"status", "health", "performance", "metrics", "dashboard"},
			"analyze":      {"data", "logs", "patterns", "trends", "analysis"},
			"investigate":  {"investigate", "find", "search", "look", "check"},
		},
		ServiceMapping: map[string][]string{
			"lambda":      {"lambda", "function", "serverless"},
			"ec2":         {"ec2", "instance", "server", "vm"},
			"rds":         {"rds", "database", "db", "mysql", "postgres"},
			"s3":          {"s3", "bucket", "storage", "object"},
			"cloudwatch":  {"cloudwatch", "logs", "metrics", "alarm"},
			"ecs":         {"ecs", "container", "docker", "task"},
			"api_gateway": {"api", "gateway", "endpoint", "rest"},
		},
		IntentSignals: map[string]map[string]float64{
			"troubleshoot": {
				"error":   1.0,
				"failed":  0.9,
				"issue":   0.8,
				"problem": 0.8,
			},
			"monitor": {
				"status":      0.9,
				"health":      0.8,
				"performance": 0.7,
				"metrics":     0.6,
			},
			"analyze": {
				"analyze":  1.0,
				"data":     0.7,
				"patterns": 0.8,
				"trends":   0.6,
			},
		},
		UrgencyKeywords: map[string]float64{
			"critical":  1.0,
			"urgent":    0.9,
			"emergency": 1.0,
			"down":      0.9,
			"outage":    1.0,
			"crash":     0.8,
			"failed":    0.7,
		},
		TimeFrameWords: map[string]string{
			"now":        "real_time",
			"current":    "real_time",
			"latest":     "recent",
			"recent":     "recent",
			"today":      "recent",
			"yesterday":  "recent",
			"last":       "recent",
			"historical": "historical",
			"past":       "historical",
			"old":        "historical",
		},
	}
}

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
