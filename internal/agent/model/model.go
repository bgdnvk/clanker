// Package model defines shared data structures used across the agent system.
package model

import (
	"time"

	eaws "github.com/bgdnvk/clanker/internal/aws"
)

type (
	AWSData       map[string]any
	ServiceData   map[string]any
	MetricsData   map[string]any
	LogData       map[string]any
	ErrorPatterns map[string]any
)

type QueryIntent struct {
	Primary        string   `json:"primary"`
	Secondary      []string `json:"secondary"`
	Confidence     float64  `json:"confidence"`
	TargetServices []string `json:"target_services"`
	Urgency        string   `json:"urgency"`
	TimeFrame      string   `json:"time_frame"`
	DataTypes      []string `json:"data_types"`
}

type QueryContext struct {
	Query         string        `json:"query"`
	Timestamp     time.Time     `json:"timestamp"`
	Intent        QueryIntent   `json:"intent"`
	Results       AWSData       `json:"results"`
	ExecutionTime time.Duration `json:"execution_time"`
	Success       bool          `json:"success"`
	UserFeedback  string        `json:"user_feedback,omitempty"`
}

type HealthStatus struct {
	Service     string         `json:"service"`
	Status      string         `json:"status"`
	LastChecked time.Time      `json:"last_checked"`
	ErrorCount  int            `json:"error_count"`
	Metrics     map[string]any `json:"metrics"`
	Trends      []HealthTrend  `json:"trends"`
}

type HealthTrend struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
	Metric    string    `json:"metric"`
}

type Pattern struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Frequency   int       `json:"frequency"`
	LastSeen    time.Time `json:"last_seen"`
	Accuracy    float64   `json:"accuracy"`
	Conditions  []string  `json:"conditions"`
}

type AWSFunctionCall struct {
	Function    string  `json:"function"`
	Parameters  AWSData `json:"parameters"`
	Reasoning   string  `json:"reasoning"`
	ServiceType string  `json:"service_type"`
}

type AgentDecision struct {
	Action       string              `json:"action"`
	Service      string              `json:"service"`
	Operations   []eaws.LLMOperation `json:"operations"`
	AWSFunctions []AWSFunctionCall   `json:"aws_functions"`
	Reasoning    string              `json:"reasoning"`
	Confidence   float64             `json:"confidence"`
	NextSteps    []string            `json:"next_steps"`
	IsComplete   bool                `json:"is_complete"`
	Parameters   AWSData             `json:"parameters"`
}

type ChainOfThought struct {
	Step      int       `json:"step"`
	Thought   string    `json:"thought"`
	Action    string    `json:"action"`
	Outcome   string    `json:"outcome"`
	Timestamp time.Time `json:"timestamp"`
}

type AgentContext struct {
	OriginalQuery  string            `json:"original_query"`
	CurrentStep    int               `json:"current_step"`
	MaxSteps       int               `json:"max_steps"`
	GatheredData   AWSData           `json:"gathered_data"`
	Decisions      []AgentDecision   `json:"decisions"`
	ChainOfThought []ChainOfThought  `json:"chain_of_thought"`
	ServiceData    ServiceData       `json:"service_data"`
	Metrics        MetricsData       `json:"metrics"`
	ServiceStatus  map[string]string `json:"service_status"`
	LastUpdateTime time.Time         `json:"last_update_time"`
}
