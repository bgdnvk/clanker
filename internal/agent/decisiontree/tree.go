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

// Traverse walks the tree depth-first and returns every node whose condition
// matches the query/context. The returned slice (applicable nodes) is fed to
// the coordinator to spawn agents, while CurrentPath/Decisions capture the
// rules that matched for auditing and metadata.
func (t *Tree) Traverse(query string, ctx *model.AgentContext) []*Node {
	var applicable []*Node
	t.traverseNode(t.Root, query, ctx, &applicable)
	return applicable
}

// traverseNode evaluates a single node and, when it matches, records it in the
// applicable slice and tracking arrays before visiting children. This drives
// depth-first evaluation so downstream orchestration understands both the
// actions to run and the reasoning path taken.
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
