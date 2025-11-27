package coordinator

import "time"

// Dependency captures coordination requirements for a parallel agent type.
type Dependency struct {
	RequiredData   []string
	ProvidedData   []string
	ExecutionOrder int
	WaitTimeout    time.Duration
}

// AgentType represents a specialized worker that can run in parallel.
type AgentType struct {
	Name         string
	Dependencies Dependency
}

var (
	AgentTypeLog = AgentType{
		Name: "log",
		Dependencies: Dependency{
			ProvidedData:   []string{"logs", "error_patterns", "log_metrics"},
			ExecutionOrder: 1,
			WaitTimeout:    5 * time.Second,
		},
	}
	AgentTypeMetrics = AgentType{
		Name: "metrics",
		Dependencies: Dependency{
			ProvidedData:   []string{"metrics", "performance_data", "thresholds"},
			ExecutionOrder: 1,
			WaitTimeout:    5 * time.Second,
		},
	}
	AgentTypeInfrastructure = AgentType{
		Name: "infrastructure",
		Dependencies: Dependency{
			ProvidedData:   []string{"service_config", "deployment_status", "resource_health"},
			ExecutionOrder: 2,
			WaitTimeout:    8 * time.Second,
		},
	}
	AgentTypeSecurity = AgentType{
		Name: "security",
		Dependencies: Dependency{
			RequiredData:   []string{"logs", "service_config"},
			ProvidedData:   []string{"security_status", "access_patterns", "vulnerabilities"},
			ExecutionOrder: 3,
			WaitTimeout:    6 * time.Second,
		},
	}
	AgentTypeCost = AgentType{
		Name: "cost",
		Dependencies: Dependency{
			RequiredData:   []string{"metrics", "resource_health"},
			ProvidedData:   []string{"cost_analysis", "usage_patterns", "optimization_suggestions"},
			ExecutionOrder: 4,
			WaitTimeout:    8 * time.Second,
		},
	}
	AgentTypePerformance = AgentType{
		Name: "performance",
		Dependencies: Dependency{
			RequiredData:   []string{"metrics", "logs", "resource_health"},
			ProvidedData:   []string{"performance_analysis", "bottlenecks", "scaling_recommendations"},
			ExecutionOrder: 5,
			WaitTimeout:    8 * time.Second,
		},
	}
	AgentTypeDeployment = AgentType{
		Name: "deployment",
		Dependencies: Dependency{
			ProvidedData:   []string{"deployment_status", "recent_changes"},
			ExecutionOrder: 2,
			WaitTimeout:    6 * time.Second,
		},
	}
	AgentTypeDataPipeline = AgentType{
		Name: "datapipeline",
		Dependencies: Dependency{
			ProvidedData:   []string{"pipeline_status", "etl_health"},
			ExecutionOrder: 3,
			WaitTimeout:    8 * time.Second,
		},
	}
	AgentTypeQueue = AgentType{
		Name: "queue",
		Dependencies: Dependency{
			ProvidedData:   []string{"queue_health", "backlog_metrics"},
			ExecutionOrder: 3,
			WaitTimeout:    6 * time.Second,
		},
	}
	AgentTypeAvailability = AgentType{
		Name: "availability",
		Dependencies: Dependency{
			ProvidedData:   []string{"availability_status", "region_health"},
			ExecutionOrder: 4,
			WaitTimeout:    6 * time.Second,
		},
	}
	AgentTypeLLM = AgentType{
		Name: "llm",
		Dependencies: Dependency{
			ProvidedData:   []string{"llm_metrics", "model_health"},
			ExecutionOrder: 2,
			WaitTimeout:    6 * time.Second,
		},
	}
)
