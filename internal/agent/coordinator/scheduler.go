package coordinator

import (
	"sort"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

// AgentConfig captures a runnable agent definition emitted by the decision tree.
type AgentConfig struct {
	Priority   int
	Parameters model.AWSData
	AgentType  AgentType
}

// OrderGroup represents a batch of agent configs that share the same execution order.
type OrderGroup struct {
	Order  int
	Agents []AgentConfig
}

// DependencyScheduler produces execution groups honoring dependency order.
type DependencyScheduler struct{}

// NewDependencyScheduler constructs a scheduler instance.
func NewDependencyScheduler() *DependencyScheduler {
	return &DependencyScheduler{}
}

// Plan groups agent configs by execution order so the coordinator can fan them out deterministically.
func (s *DependencyScheduler) Plan(agentConfigs map[string]AgentConfig) []OrderGroup {
	if len(agentConfigs) == 0 {
		return nil
	}

	orderGroups := make(map[int][]AgentConfig)
	for _, cfg := range agentConfigs {
		order := cfg.AgentType.Dependencies.ExecutionOrder
		orderGroups[order] = append(orderGroups[order], cfg)
	}

	orders := make([]int, 0, len(orderGroups))
	for order := range orderGroups {
		orders = append(orders, order)
	}
	sort.Ints(orders)

	planned := make([]OrderGroup, 0, len(orders))
	for _, order := range orders {
		planned = append(planned, OrderGroup{Order: order, Agents: orderGroups[order]})
	}
	return planned
}

// Ready reports whether an agent's dependencies are satisfied on the shared bus.
func (s *DependencyScheduler) Ready(agentType AgentType, bus *SharedDataBus) bool {
	return bus.HasAll(agentType.Dependencies.RequiredData)
}
