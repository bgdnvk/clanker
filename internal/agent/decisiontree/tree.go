// Package decisiontree maps user intent to agent execution strategies.
package decisiontree

import (
	"strings"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

type Node struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Condition  string        `json:"condition"`
	Action     string        `json:"action"`
	Priority   int           `json:"priority"`
	Children   []*Node       `json:"children"`
	AgentTypes []string      `json:"agent_types"`
	Parameters model.AWSData `json:"parameters"`
}

type Tree struct {
	Root        *Node
	CurrentPath []string
	Decisions   []Node
}

func New() *Tree {
	root := &Node{
		ID:        "root",
		Name:      "Query Analysis Root",
		Condition: "always",
		Action:    "analyze_query",
		Priority:  10,
	}

	root.Children = []*Node{
		{
			ID:         "logs_priority",
			Name:       "Logs investigation priority",
			Condition:  "contains_keywords(['logs', 'log', 'errors', 'latest', 'recent', 'problems', 'investigate'])",
			Action:     "prioritize_log_investigation",
			Priority:   10,
			AgentTypes: []string{"log"},
			Parameters: model.AWSData{"priority": "critical", "focus": "targeted"},
			Children: []*Node{
				{
					ID:         "service_logs",
					Name:       "Specific service logs",
					Condition:  "contains_keywords(['service', 'api', 'lambda', 'function', 'logs', 'investigate'])",
					Action:     "investigate_service_logs",
					Priority:   10,
					AgentTypes: []string{"log"},
					Parameters: model.AWSData{"approach": "service_specific", "priority": "critical"},
				},
			},
		},
		{
			ID:         "service_discovery",
			Name:       "Service discovery needed",
			Condition:  "contains_keywords(['service', 'api', 'lambda', 'function', 'running', 'status', 'discover'])",
			Action:     "quick_service_discovery",
			Priority:   8,
			AgentTypes: []string{"infrastructure"},
			Parameters: model.AWSData{"scope": "targeted", "priority": "high"},
		},
		{
			ID:         "performance_check",
			Name:       "Performance investigation",
			Condition:  "contains_keywords(['performance', 'slow', 'metrics', 'cpu', 'memory', 'latency', 'errors'])",
			Action:     "focused_performance_check",
			Priority:   7,
			AgentTypes: []string{"metrics"},
			Parameters: model.AWSData{"focus": "key_metrics", "priority": "medium"},
		},
	}

	return &Tree{
		Root: root,
	}
}

func (t *Tree) Traverse(query string, ctx *model.AgentContext) []*Node {
	var applicable []*Node
	t.traverseNode(t.Root, query, ctx, &applicable)
	return applicable
}

func (t *Tree) traverseNode(node *Node, query string, ctx *model.AgentContext, applicable *[]*Node) {
	if t.evaluateCondition(node.Condition, query) {
		*applicable = append(*applicable, node)
		t.CurrentPath = append(t.CurrentPath, node.ID)
		t.Decisions = append(t.Decisions, *node)
		for _, child := range node.Children {
			t.traverseNode(child, query, ctx, applicable)
		}
	}
}

func (t *Tree) evaluateCondition(condition string, query string) bool {
	queryLower := strings.ToLower(query)

	switch {
	case condition == "always":
		return true
	case strings.HasPrefix(condition, "contains_keywords"):
		start := strings.Index(condition, "['")
		end := strings.Index(condition, "']")
		if start == -1 || end == -1 {
			return false
		}
		keywordsStr := condition[start+2 : end]
		parts := strings.Split(keywordsStr, "', '")
		for _, keyword := range parts {
			if strings.Contains(queryLower, strings.ToLower(keyword)) {
				return true
			}
		}
		return false
	default:
		return false
	}
}
