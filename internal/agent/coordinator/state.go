package coordinator

import (
	"sync"
	"time"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

// SharedDataBus stores dependency data produced by agents.
type SharedDataBus struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewSharedDataBus returns an initialized bus.
func NewSharedDataBus() *SharedDataBus {
	return &SharedDataBus{data: make(map[string]any)}
}

// Store saves the provided value under the supplied key.
func (b *SharedDataBus) Store(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[key] = value
}

// Load retrieves a value if it exists.
func (b *SharedDataBus) Load(key string) (any, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	val, ok := b.data[key]
	return val, ok
}

// HasAll returns true if each key exists in the bus.
func (b *SharedDataBus) HasAll(keys []string) bool {
	if len(keys) == 0 {
		return true
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, key := range keys {
		if _, ok := b.data[key]; !ok {
			return false
		}
	}
	return true
}

// AgentStats tracks counts for coordinator telemetry.
type AgentStats struct {
	Total     int
	Completed int
	Failed    int
}

// AgentRegistry tracks agents in a concurrency-safe fashion.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents []*ParallelAgent
	stats  AgentStats
}

// NewAgentRegistry constructs an empty registry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{}
}

// Register stores an agent and increments totals.
func (r *AgentRegistry) Register(agent *ParallelAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append(r.agents, agent)
	r.stats.Total++
}

// MarkCompleted marks an agent completion event.
func (r *AgentRegistry) MarkCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Completed++
}

// MarkFailed marks an agent failure event.
func (r *AgentRegistry) MarkFailed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Failed++
}

// Agents returns a snapshot of the current agents slice.
func (r *AgentRegistry) Agents() []*ParallelAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	copySlice := make([]*ParallelAgent, len(r.agents))
	copy(copySlice, r.agents)
	return copySlice
}

// Stats returns a snapshot of the aggregate stats.
func (r *AgentRegistry) Stats() AgentStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stats
}

// Reset clears the registry to support reuse.
func (r *AgentRegistry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = nil
	r.stats = AgentStats{}
}

// CopyContextForAgent clones the main context for an agent run.
func CopyContextForAgent(main *model.AgentContext) *model.AgentContext {
	return &model.AgentContext{
		OriginalQuery:  main.OriginalQuery,
		CurrentStep:    0,
		MaxSteps:       3,
		GatheredData:   make(model.AWSData),
		Decisions:      append([]model.AgentDecision(nil), main.Decisions...),
		ChainOfThought: append([]model.ChainOfThought(nil), main.ChainOfThought...),
		ServiceData:    make(model.ServiceData),
		Metrics:        make(model.MetricsData),
		ServiceStatus:  make(map[string]string),
		LastUpdateTime: time.Now(),
	}
}
