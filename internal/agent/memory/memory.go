// Package memory tracks recent agent queries and learned service health data.
package memory

import (
	"math"
	"sort"
	"time"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

type AgentMemory struct {
	PreviousQueries []model.QueryContext
	ServiceHealth   map[string]model.HealthStatus
	UserPreferences map[string]any
	LearnedPatterns []model.Pattern
	LastUpdated     time.Time
	MaxQueries      int
}

func New(maxQueries int) *AgentMemory {
	return &AgentMemory{
		PreviousQueries: make([]model.QueryContext, 0, maxQueries),
		ServiceHealth:   make(map[string]model.HealthStatus),
		UserPreferences: make(map[string]any),
		LearnedPatterns: make([]model.Pattern, 0),
		LastUpdated:     time.Now(),
		MaxQueries:      maxQueries,
	}
}

func (am *AgentMemory) AddQueryContext(ctx model.QueryContext) {
	am.PreviousQueries = append(am.PreviousQueries, ctx)
	if len(am.PreviousQueries) > am.MaxQueries {
		am.PreviousQueries = am.PreviousQueries[1:]
	}
	am.LastUpdated = time.Now()
}

func (am *AgentMemory) UpdateServiceHealth(service string, status model.HealthStatus) {
	am.ServiceHealth[service] = status
	am.LastUpdated = time.Now()
}

func (am *AgentMemory) GetSimilarQueries(intent model.QueryIntent, limit int) []model.QueryContext {
	var similar []model.QueryContext
	for _, prev := range am.PreviousQueries {
		score := am.calculateSimilarity(intent, prev.Intent)
		if score > 0.5 {
			similar = append(similar, prev)
		}
	}
	sort.Slice(similar, func(i, j int) bool {
		return am.calculateSimilarity(intent, similar[i].Intent) > am.calculateSimilarity(intent, similar[j].Intent)
	})
	if len(similar) > limit {
		similar = similar[:limit]
	}
	return similar
}

func (am *AgentMemory) LearnPattern(name, description string, conditions []string) {
	for i, pattern := range am.LearnedPatterns {
		if pattern.Name == name {
			am.LearnedPatterns[i].Frequency++
			am.LearnedPatterns[i].LastSeen = time.Now()
			return
		}
	}
	am.LearnedPatterns = append(am.LearnedPatterns, model.Pattern{
		Name:        name,
		Description: description,
		Frequency:   1,
		LastSeen:    time.Now(),
		Accuracy:    0.5,
		Conditions:  conditions,
	})
	am.LastUpdated = time.Now()
}

func (am *AgentMemory) calculateSimilarity(a, b model.QueryIntent) float64 {
	score := 0.0
	if a.Primary == b.Primary {
		score += 0.4
	}
	serviceOverlap := 0
	for _, serviceA := range a.TargetServices {
		for _, serviceB := range b.TargetServices {
			if serviceA == serviceB {
				serviceOverlap++
				break
			}
		}
	}
	if len(a.TargetServices) > 0 && len(b.TargetServices) > 0 {
		score += 0.3 * float64(serviceOverlap) / float64(len(a.TargetServices))
	}
	urgencyWeights := map[string]float64{"low": 1, "medium": 2, "high": 3, "critical": 4}
	if len(a.Urgency) > 0 && len(b.Urgency) > 0 {
		diff := math.Abs(urgencyWeights[a.Urgency] - urgencyWeights[b.Urgency])
		score += 0.2 * (1.0 - diff/3.0)
	}
	if a.TimeFrame == b.TimeFrame {
		score += 0.1
	}
	return score
}
