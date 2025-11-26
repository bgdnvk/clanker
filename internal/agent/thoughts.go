package agent

import (
	"fmt"
	"time"
)

// addThought adds a reasoning step to the chain of thought and timestamps it.
func (a *Agent) addThought(agentCtx *AgentContext, thought, action, outcome string) {
	chainStep := ChainOfThought{
		Step:      len(agentCtx.ChainOfThought) + 1,
		Thought:   thought,
		Action:    action,
		Outcome:   outcome,
		Timestamp: time.Now(),
	}
	agentCtx.ChainOfThought = append(agentCtx.ChainOfThought, chainStep)
}

// displayChainOfThought streams the latest reasoning entries to stdout for transparency.
func (a *Agent) displayChainOfThought(agentCtx *AgentContext) {
	if len(agentCtx.ChainOfThought) == 0 {
		return
	}

	fmt.Printf("💭 Agent Reasoning Chain:\n")
	for i, thought := range agentCtx.ChainOfThought {
		if i >= len(agentCtx.ChainOfThought)-3 { // Show last 3 thoughts
			timestamp := thought.Timestamp.Format("15:04:05")
			fmt.Printf("   [%s] %s: %s\n", timestamp, thought.Action, thought.Thought)
			if thought.Outcome != "" {
				fmt.Printf("   → %s\n", thought.Outcome)
			}
		}
	}
	fmt.Printf("\n")
}
