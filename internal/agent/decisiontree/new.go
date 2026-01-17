package decisiontree

import "github.com/bgdnvk/clanker/internal/agent/model"

// New constructs the default decision tree, wiring root children that match
// common observability scenarios.
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
			ID:         "k8s_context",
			Name:       "Kubernetes context gathering",
			Condition:  "contains_keywords(['kubernetes', 'k8s', 'kubectl', 'kube', 'namespace', 'pod', 'pods', 'deployment', 'deployments', 'statefulset', 'daemonset', 'ingress', 'helm', 'eks', 'kubeadm', 'k3s'])",
			Action:     "gather_k8s_context",
			Priority:   10,
			AgentTypes: []string{"k8s"},
			Parameters: model.AWSData{"scope": "cluster_resources"},
		},
		{
			ID:         "logs_priority",
			Name:       "Logs investigation priority",
			Condition:  "contains_keywords(['logs', 'log', 'errors', 'stacktrace', 'trace', 'panic', 'exception', 'latest', 'recent', 'problems', 'debug', 'investigate', 'crash'])",
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
			Condition:  "contains_keywords(['service', 'services', 'api', 'endpoint', 'lambda', 'function', 'component', 'microservice', 'tier', 'module', 'running', 'status', 'discover', 'unknown'])",
			Action:     "quick_service_discovery",
			Priority:   8,
			AgentTypes: []string{"infrastructure"},
			Parameters: model.AWSData{"scope": "targeted", "priority": "high"},
		},
		{
			ID:         "performance_check",
			Name:       "Performance investigation",
			Condition:  "contains_keywords(['performance', 'slow', 'laggy', 'lag', 'throughput', 'latency', 'jitter', 'metrics', 'cpu', 'memory', 'throttle', 'overload', 'errors'])",
			Action:     "focused_performance_check",
			Priority:   7,
			AgentTypes: []string{"metrics"},
			Parameters: model.AWSData{"focus": "key_metrics", "priority": "medium"},
		},
		{
			ID:         "security_alerts",
			Name:       "Security or IAM issues",
			Condition:  "contains_keywords(['security', 'breach', 'breached', 'unauthorized', 'iam', 'key', 'credential', 'secret', 'token', 'rotate', 'privilege', 'access', 'leak', 'compromise'])",
			Action:     "investigate_security",
			Priority:   9,
			AgentTypes: []string{"security"},
			Parameters: model.AWSData{"priority": "high"},
		},
		{
			ID:         "cost_anomaly",
			Name:       "Cost and usage anomaly",
			Condition:  "contains_keywords(['cost', 'spend', 'bill', 'billing', 'budget', 'usage', 'savings', 'expensive', 'increase', 'spike', 'surge'])",
			Action:     "investigate_costs",
			Priority:   6,
			AgentTypes: []string{"cost"},
			Parameters: model.AWSData{"focus": "recent"},
		},
		{
			ID:         "deployment_changes",
			Name:       "Deployment or release issues",
			Condition:  "contains_keywords(['deploy', 'deployed', 'release', 'rollout', 'ship', 'pipeline', 'codebuild', 'codepipeline', 'build', 'rollback', 'promotion', 'canary', 'launch'])",
			Action:     "check_deployment_status",
			Priority:   8,
			AgentTypes: []string{"deployment"},
			Parameters: model.AWSData{"scope": "recent"},
		},
		{
			ID:         "data_pipeline_issues",
			Name:       "Data or ETL pipeline failures",
			Condition:  "contains_keywords(['etl', 'glue', 'step function', 'stepfunction', 'dataflow', 'airflow', 'pipeline', 'dataproc', 'databricks', 'spark', 'batch job', 'scheduler', 'workflow'])",
			Action:     "inspect_data_pipelines",
			Priority:   7,
			AgentTypes: []string{"datapipeline"},
			Parameters: model.AWSData{"priority": "high"},
		},
		{
			ID:         "queue_backlog",
			Name:       "Queue depth or backlog",
			Condition:  "contains_keywords(['queue', 'queued', 'backlog', 'message', 'kafka', 'sqs', 'sns', 'pubsub', 'mq', 'dlq', 'stuck'])",
			Action:     "inspect_queue_health",
			Priority:   7,
			AgentTypes: []string{"queue"},
			Parameters: model.AWSData{"focus": "backlog"},
		},
		{
			ID:         "availability_incident",
			Name:       "Availability or outage reports",
			Condition:  "contains_keywords(['outage', 'downtime', 'down', 'downed', 'availability', 'region', 'zone', 'uptime', 'sla', 'failover', 'pager', 'sev'])",
			Action:     "check_availability",
			Priority:   9,
			AgentTypes: []string{"availability"},
			Parameters: model.AWSData{"priority": "critical"},
		},
		{
			ID:         "llm_observability",
			Name:       "LLM / inference support",
			Condition:  "contains_keywords(['llm', 'model', 'inference', 'tokens', 'bedrock', 'sagemaker', 'rag', 'embedding', 'vector', 'prompt', 'openai', 'anthropic'])",
			Action:     "inspect_llm_stack",
			Priority:   8,
			AgentTypes: []string{"llm"},
			Parameters: model.AWSData{"focus": "model_health"},
		},
		{
			ID:         "general_investigation",
			Name:       "General issue or vague request",
			Condition:  "contains_keywords(['issue', 'problem', 'broken', 'wtf', 'help', 'fix', 'investigation', 'what is happening', 'why'])",
			Action:     "broad_context_scan",
			Priority:   5,
			AgentTypes: []string{"log", "infrastructure", "metrics"},
			Parameters: model.AWSData{"scope": "broad", "priority": "medium"},
		},
	}

	return &Tree{Root: root}
}
