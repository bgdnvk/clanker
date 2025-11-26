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
		{
			ID:         "security_alerts",
			Name:       "Security or IAM issues",
			Condition:  "contains_keywords(['security', 'breach', 'unauthorized', 'iam', 'key', 'credential', 'secret'])",
			Action:     "investigate_security",
			Priority:   9,
			AgentTypes: []string{"security"},
			Parameters: model.AWSData{"priority": "high"},
		},
		{
			ID:         "cost_anomaly",
			Name:       "Cost and usage anomaly",
			Condition:  "contains_keywords(['cost', 'spend', 'bill', 'budget', 'usage', 'savings'])",
			Action:     "investigate_costs",
			Priority:   6,
			AgentTypes: []string{"cost"},
			Parameters: model.AWSData{"focus": "recent"},
		},
		{
			ID:         "deployment_changes",
			Name:       "Deployment or release issues",
			Condition:  "contains_keywords(['deploy', 'release', 'rollout', 'pipeline', 'codebuild', 'codepipeline'])",
			Action:     "check_deployment_status",
			Priority:   8,
			AgentTypes: []string{"deployment"},
			Parameters: model.AWSData{"scope": "recent"},
		},
		{
			ID:         "data_pipeline_issues",
			Name:       "Data or ETL pipeline failures",
			Condition:  "contains_keywords(['etl', 'glue', 'step function', 'dataflow', 'airflow', 'pipeline'])",
			Action:     "inspect_data_pipelines",
			Priority:   7,
			AgentTypes: []string{"datapipeline"},
			Parameters: model.AWSData{"priority": "high"},
		},
		{
			ID:         "queue_backlog",
			Name:       "Queue depth or backlog",
			Condition:  "contains_keywords(['queue', 'backlog', 'message', 'kafka', 'sqs', 'sns'])",
			Action:     "inspect_queue_health",
			Priority:   7,
			AgentTypes: []string{"queue"},
			Parameters: model.AWSData{"focus": "backlog"},
		},
		{
			ID:         "availability_incident",
			Name:       "Availability or outage reports",
			Condition:  "contains_keywords(['outage', 'downtime', 'availability', 'region', 'uptime', 'sla'])",
			Action:     "check_availability",
			Priority:   9,
			AgentTypes: []string{"availability"},
			Parameters: model.AWSData{"priority": "critical"},
		},
		{
			ID:         "llm_observability",
			Name:       "LLM / inference support",
			Condition:  "contains_keywords(['llm', 'model', 'inference', 'tokens', 'bedrock', 'sagemaker', 'rag'])",
			Action:     "inspect_llm_stack",
			Priority:   8,
			AgentTypes: []string{"llm"},
			Parameters: model.AWSData{"focus": "model_health"},
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
