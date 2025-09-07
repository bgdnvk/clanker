package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// AWS data structures
type AWSData map[string]any
type ServiceData map[string]any
type MetricsData map[string]any
type LogData map[string]any
type ErrorPatterns map[string]any

// Semantic Analysis structures
type QueryIntent struct {
	Primary        string   `json:"primary"`         // main intent: "troubleshoot", "monitor", "analyze", "investigate"
	Secondary      []string `json:"secondary"`       // secondary intents
	Confidence     float64  `json:"confidence"`      // 0.0 to 1.0
	TargetServices []string `json:"target_services"` // inferred services
	Urgency        string   `json:"urgency"`         // "low", "medium", "high", "critical"
	TimeFrame      string   `json:"time_frame"`      // "recent", "historical", "real_time"
	DataTypes      []string `json:"data_types"`      // "logs", "metrics", "config", "status"
}

type SemanticAnalyzer struct {
	KeywordWeights  map[string]float64            `json:"keyword_weights"`
	ContextPatterns map[string][]string           `json:"context_patterns"`
	ServiceMapping  map[string][]string           `json:"service_mapping"`
	IntentSignals   map[string]map[string]float64 `json:"intent_signals"`
	UrgencyKeywords map[string]float64            `json:"urgency_keywords"`
	TimeFrameWords  map[string]string             `json:"timeframe_words"`
}

// Agent Memory structures
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
	Service     string                 `json:"service"`
	Status      string                 `json:"status"` // "healthy", "degraded", "error", "unknown"
	LastChecked time.Time              `json:"last_checked"`
	ErrorCount  int                    `json:"error_count"`
	Metrics     map[string]interface{} `json:"metrics"`
	Trends      []HealthTrend          `json:"trends"`
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

type AgentMemory struct {
	PreviousQueries []QueryContext          `json:"previous_queries"`
	ServiceHealth   map[string]HealthStatus `json:"service_health"`
	UserPreferences map[string]interface{}  `json:"user_preferences"`
	LearnedPatterns []Pattern               `json:"learned_patterns"`
	LastUpdated     time.Time               `json:"last_updated"`
	MaxQueries      int                     `json:"max_queries"` // rolling window size
}

// Agent Dependency structures
type AgentDependency struct {
	RequiredData   []string      `json:"required_data"`   // data this agent needs from others
	ProvidedData   []string      `json:"provided_data"`   // data this agent provides
	ExecutionOrder int           `json:"execution_order"` // priority order (1 = first)
	WaitTimeout    time.Duration `json:"wait_timeout"`    // max time to wait for dependencies
}

// Decision tree structures
type DecisionNode struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Condition  string          `json:"condition"`   // Condition to evaluate
	Action     string          `json:"action"`      // Action to take if condition is true
	Priority   int             `json:"priority"`    // Priority level (1-10, 10 being highest)
	Children   []*DecisionNode `json:"children"`    // Child nodes
	AgentTypes []string        `json:"agent_types"` // Types of agents to spawn
	Parameters AWSData         `json:"parameters"`  // Parameters for the action
}

type DecisionTree struct {
	Root        *DecisionNode  `json:"root"`
	CurrentPath []string       `json:"current_path"` // Path taken through the tree
	Decisions   []DecisionNode `json:"decisions"`    // All decisions made
}

// Parallel agent structures with enhanced dependency management
type AgentType struct {
	Name         string          `json:"name"`
	Dependencies AgentDependency `json:"dependencies"`
}

// Predefined agent types with dependency configurations
var (
	AgentTypeLog = AgentType{
		Name: "log",
		Dependencies: AgentDependency{
			RequiredData:   []string{},
			ProvidedData:   []string{"logs", "error_patterns", "log_metrics"},
			ExecutionOrder: 1,
			WaitTimeout:    10 * time.Second,
		},
	}
	AgentTypeMetrics = AgentType{
		Name: "metrics",
		Dependencies: AgentDependency{
			RequiredData:   []string{},
			ProvidedData:   []string{"metrics", "performance_data", "thresholds"},
			ExecutionOrder: 1,
			WaitTimeout:    10 * time.Second,
		},
	}
	AgentTypeInfrastructure = AgentType{
		Name: "infrastructure",
		Dependencies: AgentDependency{
			RequiredData:   []string{},
			ProvidedData:   []string{"service_config", "deployment_status", "resource_health"},
			ExecutionOrder: 2,
			WaitTimeout:    15 * time.Second,
		},
	}
	AgentTypeSecurity = AgentType{
		Name: "security",
		Dependencies: AgentDependency{
			RequiredData:   []string{"logs", "service_config"},
			ProvidedData:   []string{"security_status", "access_patterns", "vulnerabilities"},
			ExecutionOrder: 3,
			WaitTimeout:    12 * time.Second,
		},
	}
	AgentTypeCost = AgentType{
		Name: "cost",
		Dependencies: AgentDependency{
			RequiredData:   []string{"metrics", "resource_health"},
			ProvidedData:   []string{"cost_analysis", "usage_patterns", "optimization_suggestions"},
			ExecutionOrder: 4,
			WaitTimeout:    20 * time.Second,
		},
	}
	AgentTypePerformance = AgentType{
		Name: "performance",
		Dependencies: AgentDependency{
			RequiredData:   []string{"metrics", "logs", "resource_health"},
			ProvidedData:   []string{"performance_analysis", "bottlenecks", "scaling_recommendations"},
			ExecutionOrder: 5,
			WaitTimeout:    15 * time.Second,
		},
	}
)

type ParallelAgent struct {
	ID         string         `json:"id"`
	Type       AgentType      `json:"type"`
	Status     string         `json:"status"` // "running", "completed", "failed"
	StartTime  time.Time      `json:"start_time"`
	EndTime    time.Time      `json:"end_time"`
	Context    *AgentContext  `json:"context"`
	Results    AWSData        `json:"results"`
	Error      error          `json:"error,omitempty"`
	Operations []LLMOperation `json:"operations"`
}

type AgentCoordinator struct {
	Agents         []*ParallelAgent `json:"agents"`
	DecisionTree   *DecisionTree    `json:"decision_tree"`
	MainContext    *AgentContext    `json:"main_context"`
	CompletedCount int              `json:"completed_count"`
	FailedCount    int              `json:"failed_count"`
	TotalAgents    int              `json:"total_agents"`
}

// AgentDecision represents the agent's decision about what to do next
type AgentDecision struct {
	Action       string            `json:"action"`        // "gather_logs", "gather_metrics", "analyze", "aws_function_call", "complete"
	Service      string            `json:"service"`       // "chat", "image", "general"
	Operations   []LLMOperation    `json:"operations"`    // AWS operations to execute
	AWSFunctions []AWSFunctionCall `json:"aws_functions"` // AWS function calls to make
	Reasoning    string            `json:"reasoning"`     // Why this action is needed
	Confidence   float64           `json:"confidence"`    // 0.0 to 1.0 how confident we are
	NextSteps    []string          `json:"next_steps"`    // What we plan to do next
	IsComplete   bool              `json:"is_complete"`   // Whether we have enough info
	Parameters   AWSData           `json:"parameters"`    // Additional parameters
}

// AWSFunctionCall represents a function call to AWS services
type AWSFunctionCall struct {
	Function    string  `json:"function"`     // Function name (e.g., "describe_log_groups", "get_recent_logs")
	Parameters  AWSData `json:"parameters"`   // Function parameters
	Reasoning   string  `json:"reasoning"`    // Why this function is needed
	ServiceType string  `json:"service_type"` // AWS service type (logs, ec2, lambda, etc.)
}

// ChainOfThought represents a single thought/reasoning step
type ChainOfThought struct {
	Step      int       `json:"step"`
	Thought   string    `json:"thought"`
	Action    string    `json:"action"`
	Outcome   string    `json:"outcome"`
	Timestamp time.Time `json:"timestamp"`
}

// AgentContext holds all the information gathered during the agent's investigation
type AgentContext struct {
	OriginalQuery  string            `json:"original_query"`
	CurrentStep    int               `json:"current_step"`
	MaxSteps       int               `json:"max_steps"`
	GatheredData   AWSData           `json:"gathered_data"`
	Decisions      []AgentDecision   `json:"decisions"`
	ChainOfThought []ChainOfThought  `json:"chain_of_thought"`
	ServiceData    ServiceData       `json:"service_data"` // Generic service data
	Metrics        MetricsData       `json:"metrics"`
	ServiceStatus  map[string]string `json:"service_status"`
	LastUpdateTime time.Time         `json:"last_update_time"`
}

// Agent represents the intelligent context-gathering agent
type Agent struct {
	client   *Client
	debug    bool
	maxSteps int
	memory   *AgentMemory
}

// NewAgent creates a new intelligent agent for context gathering
func NewAgent(client *Client, debug bool) *Agent {
	return &Agent{
		client:   client,
		debug:    debug,
		maxSteps: 6, // Maximum 6 back-and-forth calls before final response
	}
}

// InvestigateQuery intelligently investigates a query using decision trees and parallel agents
func (a *Agent) InvestigateQuery(ctx context.Context, query string) (*AgentContext, error) {
	verbose := viper.GetBool("verbose")

	// Perform semantic analysis on the query
	semanticAnalyzer := NewSemanticAnalyzer()
	queryIntent := semanticAnalyzer.AnalyzeQuery(query)

	agentCtx := &AgentContext{
		OriginalQuery:  query,
		CurrentStep:    0,
		MaxSteps:       a.maxSteps,
		GatheredData:   make(AWSData),
		Decisions:      []AgentDecision{},
		ChainOfThought: []ChainOfThought{},
		ServiceData:    make(ServiceData),
		Metrics:        make(MetricsData),
		ServiceStatus:  make(map[string]string),
		LastUpdateTime: time.Now(),
	}

	// Add semantic analysis results to context
	agentCtx.GatheredData["semantic_analysis"] = map[string]interface{}{
		"intent":          queryIntent,
		"confidence":      queryIntent.Confidence,
		"target_services": queryIntent.TargetServices,
		"urgency":         queryIntent.Urgency,
		"time_frame":      queryIntent.TimeFrame,
		"data_types":      queryIntent.DataTypes,
	}

	// Initialize agent memory if needed
	if a.memory == nil {
		a.memory = NewAgentMemory(50) // Keep last 50 queries
	}

	// Check for similar queries in memory
	similarQueries := a.memory.GetSimilarQueries(queryIntent, 3)
	if len(similarQueries) > 0 && verbose {
		fmt.Printf("🧠 Found %d similar queries in memory\n", len(similarQueries))
	}

	// Initial chain of thought with semantic analysis
	a.addThought(agentCtx, fmt.Sprintf("Starting investigation of query: '%s'", query), "analyze", "Query received, beginning analysis")
	a.addThought(agentCtx, fmt.Sprintf("Semantic analysis: Intent=%s, Confidence=%.2f, Urgency=%s",
		queryIntent.Primary, queryIntent.Confidence, queryIntent.Urgency), "analyze", "Performed semantic analysis")

	if verbose {
		fmt.Printf("🤖 Agent starting investigation of query: %s\n", query)
		fmt.Printf("🧠 Semantic Analysis: Intent=%s (%.1f%% confidence), Urgency=%s, Services=%v\n",
			queryIntent.Primary, queryIntent.Confidence*100, queryIntent.Urgency, queryIntent.TargetServices)
		fmt.Printf("🎯 Maximum investigation steps: %d\n", a.maxSteps)
	}

	// Create agent coordinator with decision tree
	coordinator := NewAgentCoordinator(agentCtx)

	// Traverse decision tree to determine what agents to spawn
	applicableNodes := coordinator.DecisionTree.traverseTree(query, agentCtx)

	if verbose {
		fmt.Printf("🌳 Decision tree analysis: %d applicable nodes found\n", len(applicableNodes))
		for _, node := range applicableNodes {
			fmt.Printf("  📊 Node: %s (priority: %d, agents: %v)\n", node.Name, node.Priority, node.AgentTypes)
		}
	}

	a.addThought(agentCtx, fmt.Sprintf("Decision tree identified %d applicable strategies", len(applicableNodes)), "analyze", "Determined parallel execution strategy")

	// Spawn parallel agents based on decision tree
	if len(applicableNodes) > 0 {
		coordinator.SpawnAgents(ctx, a, applicableNodes)

		// Wait for parallel agents to complete (with timeout)
		timeout := 30 * time.Second
		err := coordinator.WaitForCompletion(ctx, timeout)
		if err != nil {
			a.addThought(agentCtx, fmt.Sprintf("Some parallel agents failed or timed out: %v", err), "warning", "Proceeding with available data")
			if verbose {
				fmt.Printf("⚠️  Warning: %v\n", err)
			}
		}

		// Aggregate results from parallel agents
		parallelResults := coordinator.AggregateResults()

		// Merge parallel results into main context
		for key, value := range parallelResults {
			agentCtx.GatheredData[key] = value
		}

		a.addThought(agentCtx, fmt.Sprintf("Completed parallel execution with %d agents", coordinator.TotalAgents), "success", "Data gathering completed")

		if verbose {
			fmt.Printf("🎉 Parallel execution completed: %d successful, %d failed\n",
				coordinator.CompletedCount, coordinator.FailedCount)
		}
	}

	// Fallback to traditional sequential approach if no parallel agents were spawned
	if len(applicableNodes) == 0 {
		a.addThought(agentCtx, "No specific parallel strategies identified, using sequential approach", "fallback", "Traditional investigation approach")

		if verbose {
			fmt.Printf("🔄 Using traditional sequential investigation approach\n")
		}

		// Traditional investigation loop (simplified)
		for agentCtx.CurrentStep < agentCtx.MaxSteps {
			agentCtx.CurrentStep++

			if verbose {
				fmt.Printf("🔍 Step %d/%d: Analyzing current context...\n", agentCtx.CurrentStep, agentCtx.MaxSteps)
			}

			a.addThought(agentCtx, fmt.Sprintf("Step %d: Analyzing what information is needed next", agentCtx.CurrentStep), "reason", "Evaluating available context and determining next action")

			decision, err := a.makeDecision(ctx, agentCtx)
			if err != nil {
				a.addThought(agentCtx, fmt.Sprintf("Failed to make decision: %v", err), "error", "Decision making failed")
				return agentCtx, fmt.Errorf("failed to make decision at step %d: %w", agentCtx.CurrentStep, err)
			}

			agentCtx.Decisions = append(agentCtx.Decisions, *decision)
			a.addThought(agentCtx, fmt.Sprintf("Decision: %s for service '%s' (confidence: %.2f)", decision.Action, decision.Service, decision.Confidence), "decide", decision.Reasoning)

			if verbose {
				fmt.Printf("🧠 Agent decision: %s (confidence: %.2f)\n", decision.Action, decision.Confidence)
				fmt.Printf("📝 Reasoning: %s\n", decision.Reasoning)
				a.displayChainOfThought(agentCtx)
			}

			if decision.IsComplete {
				a.addThought(agentCtx, "Investigation complete - sufficient information gathered", "complete", "Ready to provide final answer")
				if verbose {
					fmt.Printf("✅ Agent believes it has sufficient information to answer the query\n")
				}
				break
			}

			a.addThought(agentCtx, fmt.Sprintf("Executing action: %s", decision.Action), "execute", fmt.Sprintf("Gathering %s data for service: %s", decision.Action, decision.Service))

			err = a.executeDecision(ctx, agentCtx, decision)
			if err != nil {
				a.addThought(agentCtx, fmt.Sprintf("Execution failed: %v", err), "error", "Will continue with available information")
				if verbose {
					fmt.Printf("⚠️  Warning: Failed to execute decision: %v\n", err)
				}
			} else {
				a.addThought(agentCtx, fmt.Sprintf("Successfully executed %s", decision.Action), "success", "Information gathered successfully")
			}

			agentCtx.LastUpdateTime = time.Now()
		}
	}

	if agentCtx.CurrentStep >= agentCtx.MaxSteps {
		a.addThought(agentCtx, "Maximum investigation steps reached", "limit", "Proceeding with available information")
		if verbose {
			fmt.Printf("🔄 Maximum investigation steps reached (%d). Proceeding with available information.\n", agentCtx.MaxSteps)
		}
	}

	// Final summary thought
	dataCount := 0
	for key := range agentCtx.GatheredData {
		if data, ok := agentCtx.GatheredData[key]; ok {
			if logs, isLogs := data.([]string); isLogs {
				dataCount += len(logs)
			} else {
				dataCount++
			}
		}
	}
	a.addThought(agentCtx, fmt.Sprintf("Investigation complete: %d data points gathered across %d steps", dataCount, agentCtx.CurrentStep), "summary", "Ready to analyze findings and provide response")

	// Save query context to memory
	queryContext := QueryContext{
		Query:         query,
		Timestamp:     time.Now(),
		Intent:        queryIntent,
		Results:       agentCtx.GatheredData,
		ExecutionTime: time.Since(agentCtx.LastUpdateTime),
		Success:       len(agentCtx.GatheredData) > 0,
	}
	a.memory.AddQueryContext(queryContext)

	// Learn patterns from successful investigations
	if queryContext.Success && queryIntent.Confidence > 0.7 {
		conditions := []string{
			fmt.Sprintf("intent=%s", queryIntent.Primary),
			fmt.Sprintf("urgency=%s", queryIntent.Urgency),
		}
		for _, service := range queryIntent.TargetServices {
			conditions = append(conditions, fmt.Sprintf("service=%s", service))
		}

		patternName := fmt.Sprintf("%s_%s_pattern", queryIntent.Primary, queryIntent.Urgency)
		description := fmt.Sprintf("Successful %s investigation with %s urgency", queryIntent.Primary, queryIntent.Urgency)
		a.memory.LearnPattern(patternName, description, conditions)
	}

	return agentCtx, nil
}

// makeDecision returns a simple decision based on the query without AI calls
func (a *Agent) makeDecision(ctx context.Context, agentCtx *AgentContext) (*AgentDecision, error) {
	// Simple rule-based decision making instead of AI calls
	// The agent focuses only on infrastructure operations

	query := strings.ToLower(agentCtx.OriginalQuery)

	if strings.Contains(query, "error") || strings.Contains(query, "fail") {
		return &AgentDecision{
			Action:    "investigate_errors",
			Service:   "logs",
			Reasoning: "Query mentions errors, investigating CloudWatch logs",
			Operations: []LLMOperation{
				{Operation: "get_recent_logs", Reason: "Check for recent error logs", Parameters: map[string]any{}},
			},
			Confidence: 0.8,
		}, nil
	}

	if strings.Contains(query, "performance") || strings.Contains(query, "slow") {
		return &AgentDecision{
			Action:    "investigate_performance",
			Service:   "cloudwatch",
			Reasoning: "Query mentions performance, checking metrics",
			Operations: []LLMOperation{
				{Operation: "list_cloudwatch_alarms", Reason: "Check for performance alarms", Parameters: map[string]any{}},
			},
			Confidence: 0.8,
		}, nil
	}

	// Default: general infrastructure check
	return &AgentDecision{
		Action:    "general_check",
		Service:   "general",
		Reasoning: "General infrastructure investigation",
		Operations: []LLMOperation{
			{Operation: "list_lambda_functions", Reason: "Check Lambda functions", Parameters: map[string]any{}},
		},
		Confidence: 0.6,
	}, nil
}

// executeDecision executes the agent's decision by gathering the requested information
func (a *Agent) executeDecision(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("verbose")

	switch decision.Action {
	case "gather_logs":
		return a.gatherLogs(ctx, agentCtx, decision)
	case "gather_metrics":
		return a.gatherMetrics(ctx, agentCtx, decision)
	case "analyze_service":
		return a.analyzeService(ctx, agentCtx, decision)
	case "investigate_errors":
		return a.investigateErrors(ctx, agentCtx, decision)
	case "aws_function_call":
		return a.executeAWSFunctionCalls(ctx, agentCtx, decision)
	default:
		if verbose {
			fmt.Printf("🤷 Unknown action: %s\n", decision.Action)
		}
		return nil
	}
}

// gatherLogs gathers logs for the specified service or discovers relevant log groups
func (a *Agent) gatherLogs(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("verbose")

	if verbose {
		fmt.Printf("📋 Gathering logs for service: %s\n", decision.Service)
	}

	// Discover log groups dynamically based on service name or keywords
	logGroups, err := a.discoverLogGroups(ctx, decision.Service, agentCtx.OriginalQuery)
	if err != nil {
		if verbose {
			fmt.Printf("⚠️  Failed to discover log groups: %v\n", err)
		}
		return err
	}

	if verbose {
		fmt.Printf("🔍 Found %d relevant log groups\n", len(logGroups))
	}

	// Get recent logs from each discovered log group and analyze them
	for _, logGroup := range logGroups {
		if verbose {
			fmt.Printf("📋 Fetching recent logs from: %s\n", logGroup)
		}

		// Get recent logs (last hour)
		recentLogs, err := a.getRecentLogsFromGroup(ctx, logGroup)
		if err != nil {
			if verbose {
				fmt.Printf("⚠️  Failed to get recent logs from %s: %v\n", logGroup, err)
			}
			continue
		}

		// Get error logs specifically
		errorLogs, err := a.getErrorLogsFromGroup(ctx, logGroup)
		if err != nil {
			if verbose {
				fmt.Printf("⚠️  Failed to get error logs from %s: %v\n", logGroup, err)
			}
		}

		// Get log stream info
		logStreams, err := a.getLogStreamsFromGroup(ctx, logGroup)
		if err != nil {
			if verbose {
				fmt.Printf("⚠️  Failed to get log streams from %s: %v\n", logGroup, err)
			}
		}

		if verbose {
			fmt.Printf("✅ Retrieved %d recent log entries, %d error logs, %d streams from %s\n",
				len(recentLogs), len(errorLogs), len(logStreams), logGroup)
		}

		// Store comprehensive log data
		logData := LogData{
			"log_group":     logGroup,
			"recent_logs":   recentLogs,
			"error_logs":    errorLogs,
			"log_streams":   logStreams,
			"total_entries": len(recentLogs),
			"error_count":   len(errorLogs),
			"stream_count":  len(logStreams),
		}

		// Store logs in the appropriate context based on service type
		serviceKey := fmt.Sprintf("%s_logs", decision.Service)
		if agentCtx.GatheredData[serviceKey] == nil {
			agentCtx.GatheredData[serviceKey] = []LogData{}
		}

		if existingLogs, ok := agentCtx.GatheredData[serviceKey].([]LogData); ok {
			agentCtx.GatheredData[serviceKey] = append(existingLogs, logData)
		} else {
			agentCtx.GatheredData[serviceKey] = []LogData{logData}
		}

		// Also store individual log entries for analysis
		allLogsKey := fmt.Sprintf("%s_all_log_entries", decision.Service)
		if agentCtx.GatheredData[allLogsKey] == nil {
			agentCtx.GatheredData[allLogsKey] = []string{}
		}

		if existingEntries, ok := agentCtx.GatheredData[allLogsKey].([]string); ok {
			combinedLogs := append(recentLogs, errorLogs...)
			agentCtx.GatheredData[allLogsKey] = append(existingEntries, combinedLogs...)
		} else {
			combinedLogs := append(recentLogs, errorLogs...)
			agentCtx.GatheredData[allLogsKey] = combinedLogs
		}
	}

	return nil
}

// gatherMetrics gathers CloudWatch metrics for the specified service
func (a *Agent) gatherMetrics(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("verbose")

	if verbose {
		fmt.Printf("📊 Gathering metrics for service: %s\n", decision.Service)
	}

	// Execute CloudWatch metrics operations
	if len(decision.Operations) > 0 {
		result, err := a.client.executeOperations(ctx, decision.Operations)
		if err != nil {
			return fmt.Errorf("failed to gather metrics: %w", err)
		}

		agentCtx.Metrics[decision.Service] = result
	}

	return nil
}

// analyzeService analyzes the overall health and status of a service
func (a *Agent) analyzeService(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("verbose")

	if verbose {
		fmt.Printf("🔍 Analyzing service: %s\n", decision.Service)
	}

	// Execute service analysis operations
	if len(decision.Operations) > 0 {
		result, err := a.client.executeOperations(ctx, decision.Operations)
		if err != nil {
			return fmt.Errorf("failed to analyze service: %w", err)
		}

		agentCtx.ServiceStatus[decision.Service] = result
	}

	return nil
}

// investigateErrors looks for error patterns and issues
func (a *Agent) investigateErrors(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("verbose")

	if verbose {
		fmt.Printf("🚨 Investigating errors for service: %s\n", decision.Service)
	}

	// Look for error patterns in gathered logs
	var allLogs []string

	// Collect all logs from service data
	for key, data := range agentCtx.GatheredData {
		if strings.HasSuffix(key, "_logs") {
			if logs, ok := data.([]string); ok {
				allLogs = append(allLogs, logs...)
			}
		}
	}

	errorPatterns := a.findErrorPatterns(allLogs)
	agentCtx.GatheredData["error_patterns"] = errorPatterns

	return nil
}

// Helper functions

// discoverLogGroups dynamically discovers relevant log groups based on service name and query
func (a *Agent) discoverLogGroups(ctx context.Context, serviceName, originalQuery string) ([]string, error) {
	verbose := viper.GetBool("verbose")

	// Get all log groups first
	allLogGroups, err := a.getAllLogGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get log groups: %w", err)
	}

	if verbose {
		fmt.Printf("🔍 Discovered %d total log groups\n", len(allLogGroups))
	}

	// Filter log groups based on service name and query keywords
	var relevantGroups []string
	queryLower := strings.ToLower(originalQuery)
	serviceLower := strings.ToLower(serviceName)

	// Extract keywords from the query for better matching
	queryKeywords := a.extractKeywordsFromQuery(queryLower)

	for _, logGroup := range allLogGroups {
		logGroupLower := strings.ToLower(logGroup)

		// Check if log group matches service name
		if serviceLower != "" && serviceLower != "general" && strings.Contains(logGroupLower, serviceLower) {
			relevantGroups = append(relevantGroups, logGroup)
			continue
		}

		// Check if log group matches any query keywords
		for _, keyword := range queryKeywords {
			if strings.Contains(logGroupLower, keyword) {
				relevantGroups = append(relevantGroups, logGroup)
				break
			}
		}
	}

	// If no specific matches found, return most active/recent log groups
	if len(relevantGroups) == 0 {
		if verbose {
			fmt.Printf("🔍 No specific matches found, using most active log groups\n")
		}
		relevantGroups = a.getMostActiveLogGroups(allLogGroups, 10)
	}

	if verbose {
		fmt.Printf("📋 Selected %d relevant log groups: %v\n", len(relevantGroups), relevantGroups)
	}

	return relevantGroups, nil
}

// getAllLogGroups gets all CloudWatch log groups
func (a *Agent) getAllLogGroups(ctx context.Context) ([]string, error) {
	args := []string{
		"logs", "describe-log-groups",
		"--output", "json",
	}

	output, err := a.client.execCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	var logData struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
		} `json:"logGroups"`
	}

	err = json.Unmarshal([]byte(output), &logData)
	if err != nil {
		return nil, err
	}

	var logGroups []string
	for _, group := range logData.LogGroups {
		logGroups = append(logGroups, group.LogGroupName)
	}

	return logGroups, nil
}

// extractKeywordsFromQuery extracts relevant keywords from the user's query
func (a *Agent) extractKeywordsFromQuery(query string) []string {
	// Common service/technology keywords that might appear in log group names
	potentialKeywords := []string{
		"api", "lambda", "ecs", "batch", "fargate", "ec2", "rds", "s3",
		"chat", "image", "process", "worker", "job", "task", "service",
		"auth", "user", "admin", "health", "monitor", "log", "error",
		"dev", "stage", "prod", "test", "qa",
	}

	var keywords []string
	for _, keyword := range potentialKeywords {
		if strings.Contains(query, keyword) {
			keywords = append(keywords, keyword)
		}
	}

	// Also extract any quoted strings or specific service names mentioned
	words := strings.Fields(query)
	for _, word := range words {
		// Clean word of punctuation
		word = strings.Trim(word, ".,!?;:")
		if len(word) > 2 { // Only consider words longer than 2 chars
			keywords = append(keywords, word)
		}
	}

	return keywords
}

// getMostActiveLogGroups returns the most recently active log groups
func (a *Agent) getMostActiveLogGroups(allGroups []string, limit int) []string {
	// For now, return the first 'limit' groups
	// In the future, this could be enhanced to check last event time
	if len(allGroups) <= limit {
		return allGroups
	}
	return allGroups[:limit]
}

// discoverServices dynamically discovers AWS services that might be relevant
func (a *Agent) discoverServices(ctx context.Context, query string) ([]string, error) {
	queryLower := strings.ToLower(query)
	var services []string

	// Service keywords mapping
	serviceKeywords := map[string][]string{
		"lambda":     {"lambda", "function", "serverless"},
		"ecs":        {"ecs", "container", "fargate", "task"},
		"ec2":        {"ec2", "instance", "server", "compute"},
		"rds":        {"rds", "database", "db", "mysql", "postgres"},
		"s3":         {"s3", "bucket", "storage", "file"},
		"apigateway": {"api", "gateway", "endpoint", "rest"},
		"batch":      {"batch", "job", "queue", "processing"},
		"cloudwatch": {"logs", "metrics", "alarm", "monitor"},
	}

	for service, keywords := range serviceKeywords {
		for _, keyword := range keywords {
			if strings.Contains(queryLower, keyword) {
				services = append(services, service)
				break
			}
		}
	}

	// If no specific services detected, return common services
	if len(services) == 0 {
		services = []string{"lambda", "ecs", "ec2", "rds", "s3"}
	}

	return services, nil
}

func (a *Agent) getRecentLogsFromGroup(ctx context.Context, logGroup string) ([]string, error) {
	// Get logs from the last hour
	endTime := time.Now()
	startTime := endTime.Add(-1 * time.Hour)

	args := []string{
		"logs", "filter-log-events",
		"--log-group-name", logGroup,
		"--start-time", fmt.Sprintf("%d", startTime.Unix()*1000),
		"--end-time", fmt.Sprintf("%d", endTime.Unix()*1000),
		"--limit", "100",
		"--output", "json",
	}

	output, err := a.client.execCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	// Parse the log events and extract messages
	var logData struct {
		Events []struct {
			Message   string `json:"message"`
			Timestamp int64  `json:"timestamp"`
		} `json:"events"`
	}

	err = json.Unmarshal([]byte(output), &logData)
	if err != nil {
		return nil, err
	}

	var logs []string
	for _, event := range logData.Events {
		logs = append(logs, event.Message)
	}

	return logs, nil
}

func (a *Agent) findErrorPatterns(allLogs []string) ErrorPatterns {
	patterns := make(ErrorPatterns)

	// Look for common error patterns
	errorCount := 0
	timeoutCount := 0
	connectionCount := 0

	for _, log := range allLogs {
		logLower := strings.ToLower(log)
		if strings.Contains(logLower, "error") {
			errorCount++
		}
		if strings.Contains(logLower, "timeout") {
			timeoutCount++
		}
		if strings.Contains(logLower, "connection") && (strings.Contains(logLower, "failed") || strings.Contains(logLower, "refused")) {
			connectionCount++
		}
	}

	patterns["total_errors"] = errorCount
	patterns["timeout_errors"] = timeoutCount
	patterns["connection_errors"] = connectionCount
	patterns["total_logs_analyzed"] = len(allLogs)

	return patterns
}

func (a *Agent) encodePrompt(prompt string) string {
	// This would encode the prompt for Bedrock API call
	// For now, returning the prompt as-is for simplicity
	return prompt
}

func (a *Agent) buildDecisionPrompt(agentCtx *AgentContext) string {
	// Count gathered data
	logCount := 0
	for key, data := range agentCtx.GatheredData {
		if strings.HasSuffix(key, "_logs") {
			if logs, ok := data.([]string); ok {
				logCount += len(logs)
			}
		}
	}

	return fmt.Sprintf(`You are an intelligent AWS investigation agent. Your job is to determine what information you need to gather to answer the user's query effectively.

Original Query: "%s"
Current Step: %d/%d

Information gathered so far:
- Log entries: %d across all services
- Service data: %d types
- Metrics: %d services
- Service status: %d services

Previous decisions: %v

Analyze the query and current context. Decide what action to take next:

1. "aws_function_call" - Make direct AWS service calls using available functions
2. "gather_logs" - Get recent logs from specific services 
3. "gather_metrics" - Get CloudWatch metrics for performance analysis
4. "analyze_service" - Check service health and configuration
5. "investigate_errors" - Look for error patterns and issues
6. "complete" - You have enough information to answer

Available AWS Functions for aws_function_call:
- describe_log_groups: List CloudWatch log groups (parameters: service_filter)
- get_recent_logs: Get recent log entries (parameters: log_group_name, hours_back, limit)
- get_error_logs: Get error log entries (parameters: log_group_name, hours_back, filter_pattern)
- describe_lambda_functions: List Lambda functions (parameters: function_name_filter)
- describe_ecs_services: List ECS services (parameters: cluster_name, service_filter)
- list_s3_buckets: List S3 buckets (parameters: bucket_filter)
- describe_ec2_instances: List EC2 instances (parameters: instance_filter, state_filter)

For service parameter, specify:
- Specific service name from the query (e.g., "chat", "api", "lambda")
- "general" for broad investigation
- Service type keywords that might appear in log group names

Respond with ONLY a JSON object:
{
  "action": "aws_function_call|gather_logs|gather_metrics|analyze_service|investigate_errors|complete",
  "service": "service_name_or_general",
  "operations": [{"operation": "name", "reason": "why", "parameters": {}}],
  "aws_functions": [{"function": "function_name", "parameters": {"key": "value"}, "reasoning": "why", "service_type": "aws_service"}],
  "reasoning": "detailed explanation of why this action is needed",
  "confidence": 0.85,
  "next_steps": ["what you plan to do after this"],
  "is_complete": false,
  "parameters": {"key": "value"}
}

If you believe you have sufficient information to answer the user's query, set "is_complete": true and "action": "complete".

Focus on being efficient - use aws_function_call first to directly gather specific AWS data, then use other actions if needed.`,
		agentCtx.OriginalQuery,
		agentCtx.CurrentStep,
		agentCtx.MaxSteps,
		logCount,
		len(agentCtx.GatheredData),
		len(agentCtx.Metrics),
		len(agentCtx.ServiceStatus),
		agentCtx.Decisions)
}

// BuildFinalContext creates the final context string for the LLM with all gathered information
func (a *Agent) BuildFinalContext(agentCtx *AgentContext) string {
	var context strings.Builder

	context.WriteString("=== INTELLIGENT AGENT INVESTIGATION RESULTS ===\n")
	context.WriteString(fmt.Sprintf("Query: %s\n\n", agentCtx.OriginalQuery))

	// Add semantic analysis if available
	if semanticData, exists := agentCtx.GatheredData["semantic_analysis"]; exists {
		context.WriteString("🧠 SEMANTIC ANALYSIS:\n")
		if semData, ok := semanticData.(map[string]interface{}); ok {
			for key, value := range semData {
				context.WriteString(fmt.Sprintf("  %s: %v\n", key, value))
			}
		}
		context.WriteString("\n")
	}

	// DEBUG: Show all gathered data keys to understand the structure
	context.WriteString("DEBUG - All Gathered Data Keys:\n")
	for key := range agentCtx.GatheredData {
		context.WriteString(fmt.Sprintf("  - %s\n", key))
	}
	context.WriteString("\n")

	// Process ALL data from parallel agents to ensure we don't miss anything
	context.WriteString("📋 COMPLETE PARALLEL AGENT RESULTS:\n")
	context.WriteString("=======================================\n")

	for key, data := range agentCtx.GatheredData {
		// Skip semantic analysis and metadata as we handle those separately
		if key == "semantic_analysis" || key == "_metadata" {
			continue
		}

		context.WriteString(fmt.Sprintf("\n📊 %s:\n", strings.ToUpper(key)))
		context.WriteString("=" + strings.Repeat("=", len(key)) + "\n")

		if strValue, ok := data.(string); ok {
			context.WriteString(strValue)
		} else if awsData, ok := data.(AWSData); ok {
			// Handle nested agent data structure properly
			for subKey, subValue := range awsData {
				context.WriteString(fmt.Sprintf("\n🔍 %s:\n", strings.ToUpper(subKey)))
				context.WriteString(strings.Repeat("-", len(subKey)) + "\n")

				if strSubValue, ok := subValue.(string); ok {
					context.WriteString(strSubValue)
				} else {
					context.WriteString(fmt.Sprintf("%v", subValue))
				}
				context.WriteString("\n")
			}
		} else {
			context.WriteString(fmt.Sprintf("%v", data))
		}
		context.WriteString("\n")
	}
	for key, data := range agentCtx.GatheredData {
		if strings.Contains(key, "analyze_lambda_errors") {
			context.WriteString("\n🚨 CRITICAL LAMBDA ERROR ANALYSIS\n")
			context.WriteString("=====================================\n")

			context.WriteString(fmt.Sprintf("\n📋 ANALYSIS RESULTS (%s):\n", strings.ToUpper(key)))
			context.WriteString(strings.Repeat("-", 50) + "\n")

			if strValue, ok := data.(string); ok {
				context.WriteString(strValue)
			} else {
				context.WriteString(fmt.Sprintf("%v", data))
			}
			context.WriteString("\n")
		}
	}

	// Also check if we have log agent data stored as nested structure
	if logData, exists := agentCtx.GatheredData["log"]; exists {
		if awsData, ok := logData.(AWSData); ok {
			for subKey, subValue := range awsData {
				if strings.Contains(subKey, "analyze_lambda_errors") {
					context.WriteString("\n🚨 CRITICAL LAMBDA ERROR ANALYSIS\n")
					context.WriteString("=====================================\n")

					context.WriteString(fmt.Sprintf("\n📋 NESTED ANALYSIS RESULTS (%s):\n", strings.ToUpper(subKey)))
					context.WriteString(strings.Repeat("-", 50) + "\n")

					if strSubValue, ok := subValue.(string); ok {
						context.WriteString(strSubValue)
					} else {
						context.WriteString(fmt.Sprintf("%v", subValue))
					}
					context.WriteString("\n")
				}
			}
		}
	}

	for key, data := range agentCtx.GatheredData {
		// Skip semantic analysis and metadata as we handle those separately
		if key == "semantic_analysis" || key == "_metadata" {
			continue
		}

		// Skip Lambda error analysis as we handled it above
		if strings.Contains(key, "analyze_lambda_errors") {
			continue
		}

		context.WriteString(fmt.Sprintf("\n� %s:\n", strings.ToUpper(key)))
		context.WriteString("=" + strings.Repeat("=", len(key)) + "\n")

		if strValue, ok := data.(string); ok {
			context.WriteString(strValue)
		} else if awsData, ok := data.(AWSData); ok {
			for subKey, subValue := range awsData {
				context.WriteString(fmt.Sprintf("\n📊 %s:\n", strings.ToUpper(subKey)))
				if strSubValue, ok := subValue.(string); ok {
					context.WriteString(strSubValue)
				} else {
					context.WriteString(fmt.Sprintf("%v", subValue))
				}
				context.WriteString("\n")
			}
		} else {
			context.WriteString(fmt.Sprintf("%v", data))
		}
		context.WriteString("\n\n")
	}

	// Legacy format support - Display detailed log analysis by service
	for key, data := range agentCtx.GatheredData {
		if strings.HasSuffix(key, "_logs") && !strings.HasSuffix(key, "_all_log_entries") {
			serviceName := strings.TrimSuffix(key, "_logs")
			context.WriteString(fmt.Sprintf("=== %s SERVICE LOG ANALYSIS (Legacy) ===\n", strings.ToUpper(serviceName)))

			if logGroups, ok := data.([]LogData); ok {
				for _, logGroupData := range logGroups {
					if logGroup, exists := logGroupData["log_group"]; exists {
						context.WriteString(fmt.Sprintf("Log Group: %s\n", logGroup))
					}

					if totalEntries, exists := logGroupData["total_entries"]; exists {
						context.WriteString(fmt.Sprintf("Total Recent Entries: %v\n", totalEntries))
					}

					if errorCount, exists := logGroupData["error_count"]; exists {
						context.WriteString(fmt.Sprintf("Error Entries: %v\n", errorCount))
					}

					if streamCount, exists := logGroupData["stream_count"]; exists {
						context.WriteString(fmt.Sprintf("Active Streams: %v\n", streamCount))
					}

					// Display recent logs
					if recentLogs, exists := logGroupData["recent_logs"]; exists {
						if logs, ok := recentLogs.([]string); ok && len(logs) > 0 {
							context.WriteString("\n--- Recent Log Entries ---\n")
							for i, log := range logs {
								if i < 20 { // Show most recent 20 entries
									context.WriteString(fmt.Sprintf("%s\n", log))
								}
							}
						}
					}

					// Display error logs
					if errorLogs, exists := logGroupData["error_logs"]; exists {
						if logs, ok := errorLogs.([]string); ok && len(logs) > 0 {
							context.WriteString("\n--- Error Log Entries ---\n")
							for i, log := range logs {
								if i < 10 { // Show most recent 10 error entries
									context.WriteString(fmt.Sprintf("ERROR: %s\n", log))
								}
							}
						}
					}

					// Display log streams
					if logStreams, exists := logGroupData["log_streams"]; exists {
						if streams, ok := logStreams.([]string); ok && len(streams) > 0 {
							context.WriteString("\n--- Active Log Streams ---\n")
							for _, stream := range streams {
								context.WriteString(fmt.Sprintf("%s\n", stream))
							}
						}
					}

					context.WriteString("\n")
				}
			}
		}
	}

	// Also display raw logs if available (fallback for older format)
	for key, data := range agentCtx.GatheredData {
		if strings.HasSuffix(key, "_all_log_entries") {
			serviceName := strings.TrimSuffix(key, "_all_log_entries")
			if logs, ok := data.([]string); ok && len(logs) > 0 {
				context.WriteString(fmt.Sprintf("=== %s RAW LOG ENTRIES ===\n", strings.ToUpper(serviceName)))
				for i, log := range logs {
					if i < 30 { // Limit to most recent 30 raw logs
						context.WriteString(fmt.Sprintf("%s\n", log))
					}
				}
				context.WriteString("\n")
			}
		}
	}

	// Display service data
	if len(agentCtx.ServiceData) > 0 {
		context.WriteString("=== SERVICE DATA ===\n")
		for service, data := range agentCtx.ServiceData {
			context.WriteString(fmt.Sprintf("Service: %s\n%v\n", service, data))
		}
		context.WriteString("\n")
	}

	// Display metrics
	if len(agentCtx.Metrics) > 0 {
		context.WriteString("=== SERVICE METRICS ===\n")
		for service, metrics := range agentCtx.Metrics {
			context.WriteString(fmt.Sprintf("Service: %s\n%v\n", service, metrics))
		}
		context.WriteString("\n")
	}

	// Display service status
	if len(agentCtx.ServiceStatus) > 0 {
		context.WriteString("=== SERVICE STATUS ===\n")
		for service, status := range agentCtx.ServiceStatus {
			context.WriteString(fmt.Sprintf("Service: %s\nStatus: %s\n", service, status))
		}
		context.WriteString("\n")
	}

	// Display error analysis if available
	if errorPatterns, exists := agentCtx.GatheredData["error_patterns"]; exists {
		context.WriteString("=== ERROR ANALYSIS ===\n")
		context.WriteString(fmt.Sprintf("%v\n\n", errorPatterns))
	}

	// Display any other gathered data
	for key, data := range agentCtx.GatheredData {
		if !strings.HasSuffix(key, "_logs") && key != "error_patterns" {
			context.WriteString(fmt.Sprintf("=== %s ===\n", strings.ToUpper(key)))
			context.WriteString(fmt.Sprintf("%v\n\n", data))
		}
	}

	context.WriteString(fmt.Sprintf("Investigation completed in %d steps.\n", agentCtx.CurrentStep))

	// Add chain of thought summary
	if len(agentCtx.ChainOfThought) > 0 {
		context.WriteString("\n=== AGENT REASONING CHAIN ===\n")
		for _, thought := range agentCtx.ChainOfThought {
			context.WriteString(fmt.Sprintf("Step %d [%s]: %s\n", thought.Step, thought.Action, thought.Thought))
			if thought.Outcome != "" {
				context.WriteString(fmt.Sprintf("  → %s\n", thought.Outcome))
			}
		}
	}

	return context.String()
}

// addThought adds a reasoning step to the chain of thought
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

// displayChainOfThought shows the current reasoning chain to the user
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

// getErrorLogsFromGroup gets error logs from a specific log group
func (a *Agent) getErrorLogsFromGroup(ctx context.Context, logGroup string) ([]string, error) {
	// Get logs from the last hour with ERROR filter
	endTime := time.Now()
	startTime := endTime.Add(-1 * time.Hour)

	args := []string{
		"logs", "filter-log-events",
		"--log-group-name", logGroup,
		"--start-time", fmt.Sprintf("%d", startTime.Unix()*1000),
		"--end-time", fmt.Sprintf("%d", endTime.Unix()*1000),
		"--filter-pattern", "ERROR",
		"--limit", "50",
		"--output", "json",
	}

	output, err := a.client.execCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	// Parse the log events and extract messages
	var logData struct {
		Events []struct {
			Message   string `json:"message"`
			Timestamp int64  `json:"timestamp"`
		} `json:"events"`
	}

	err = json.Unmarshal([]byte(output), &logData)
	if err != nil {
		return nil, err
	}

	var logs []string
	for _, event := range logData.Events {
		logs = append(logs, event.Message)
	}

	return logs, nil
}

// getLogStreamsFromGroup gets log stream information from a log group
func (a *Agent) getLogStreamsFromGroup(ctx context.Context, logGroup string) ([]string, error) {
	args := []string{
		"logs", "describe-log-streams",
		"--log-group-name", logGroup,
		"--order-by", "LastEventTime",
		"--descending",
		"--max-items", "5",
		"--output", "json",
	}

	output, err := a.client.execCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	// Parse the log streams
	var logData struct {
		LogStreams []struct {
			LogStreamName     string `json:"logStreamName"`
			LastEventTime     int64  `json:"lastEventTime"`
			LastIngestionTime int64  `json:"lastIngestionTime"`
			StoredBytes       int64  `json:"storedBytes"`
		} `json:"logStreams"`
	}

	err = json.Unmarshal([]byte(output), &logData)
	if err != nil {
		return nil, err
	}

	var streams []string
	for _, stream := range logData.LogStreams {
		lastEvent := time.Unix(stream.LastEventTime/1000, 0)
		streamInfo := fmt.Sprintf("Stream: %s, Last Event: %s, Size: %d bytes",
			stream.LogStreamName, lastEvent.Format("2006-01-02 15:04:05"), stream.StoredBytes)
		streams = append(streams, streamInfo)
	}

	return streams, nil
}

// executeAWSFunctionCalls executes the AWS function calls specified by the agent
func (a *Agent) executeAWSFunctionCalls(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("verbose")

	if verbose {
		fmt.Printf("🔧 Executing %d AWS function calls\n", len(decision.AWSFunctions))
	}

	for _, awsFunc := range decision.AWSFunctions {
		if verbose {
			fmt.Printf("⚡ Calling AWS function: %s\n", awsFunc.Function)
		}

		result, err := a.executeAWSFunction(ctx, awsFunc)
		if err != nil {
			if verbose {
				fmt.Printf("⚠️  AWS function %s failed: %v\n", awsFunc.Function, err)
			}
			continue
		}

		// Store the result in gathered data
		resultKey := fmt.Sprintf("aws_function_%s_%s", awsFunc.ServiceType, awsFunc.Function)
		agentCtx.GatheredData[resultKey] = result

		if verbose {
			fmt.Printf("✅ AWS function %s completed successfully\n", awsFunc.Function)
		}
	}

	return nil
}

// executeAWSFunction executes a single AWS function call using the existing executeAWSOperation from llm.go
func (a *Agent) executeAWSFunction(ctx context.Context, awsFunc AWSFunctionCall) (any, error) {
	result, err := a.client.executeOperation(ctx, awsFunc.Function, awsFunc.Parameters)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Decision Tree Implementation

// NewDecisionTree creates a new decision tree for intelligent data gathering
func NewDecisionTree() *DecisionTree {
	root := &DecisionNode{
		ID:         "root",
		Name:       "Query Analysis Root",
		Condition:  "always",
		Action:     "analyze_query",
		Priority:   10,
		Children:   []*DecisionNode{},
		AgentTypes: []string{},
		Parameters: make(AWSData),
	}

	// Build the decision tree structure
	root.Children = []*DecisionNode{
		{
			ID:         "logs_check",
			Name:       "Check if logs are needed",
			Condition:  "contains_keywords(['logs', 'errors', 'latest', 'recent', 'problems'])",
			Action:     "spawn_log_agents",
			Priority:   9,
			AgentTypes: []string{"log"},
			Parameters: AWSData{"priority": "high"},
			Children: []*DecisionNode{
				{
					ID:         "error_focus",
					Name:       "Focus on error logs",
					Condition:  "contains_keywords(['error', 'fail', 'exception', 'problem'])",
					Action:     "prioritize_error_logs",
					Priority:   10,
					AgentTypes: []string{"log"},
					Parameters: AWSData{"filter": "ERROR", "priority": "critical"},
				},
				{
					ID:         "recent_focus",
					Name:       "Focus on recent logs",
					Condition:  "contains_keywords(['latest', 'recent', 'last'])",
					Action:     "prioritize_recent_logs",
					Priority:   8,
					AgentTypes: []string{"log"},
					Parameters: AWSData{"time_range": "1h", "priority": "high"},
				},
			},
		},
		{
			ID:         "metrics_check",
			Name:       "Check if metrics are needed",
			Condition:  "contains_keywords(['performance', 'metrics', 'cpu', 'memory', 'latency'])",
			Action:     "spawn_metrics_agents",
			Priority:   7,
			AgentTypes: []string{"metrics", "performance"},
			Parameters: AWSData{"priority": "medium"},
		},
		{
			ID:         "infrastructure_check",
			Name:       "Check if infrastructure info is needed",
			Condition:  "contains_keywords(['service', 'status', 'running', 'instances', 'infrastructure'])",
			Action:     "spawn_infrastructure_agents",
			Priority:   6,
			AgentTypes: []string{"infrastructure"},
			Parameters: AWSData{"priority": "medium"},
		},
		{
			ID:         "security_check",
			Name:       "Check if security info is needed",
			Condition:  "contains_keywords(['security', 'access', 'permissions', 'roles', 'iam'])",
			Action:     "spawn_security_agents",
			Priority:   5,
			AgentTypes: []string{"security"},
			Parameters: AWSData{"priority": "low"},
		},
		{
			ID:         "cost_check",
			Name:       "Check if cost info is needed",
			Condition:  "contains_keywords(['cost', 'billing', 'spend', 'budget'])",
			Action:     "spawn_cost_agents",
			Priority:   4,
			AgentTypes: []string{"cost"},
			Parameters: AWSData{"priority": "low"},
		},
	}

	return &DecisionTree{
		Root:        root,
		CurrentPath: []string{},
		Decisions:   []DecisionNode{},
	}
}

// evaluateCondition evaluates a condition against the query and context
func (dt *DecisionTree) evaluateCondition(condition string, query string, context *AgentContext) bool {
	query = strings.ToLower(query)

	switch {
	case condition == "always":
		return true
	case strings.HasPrefix(condition, "contains_keywords"):
		// Extract keywords from condition string
		start := strings.Index(condition, "['")
		end := strings.Index(condition, "']")
		if start == -1 || end == -1 {
			return false
		}
		keywordsStr := condition[start+2 : end]
		keywords := strings.Split(keywordsStr, "', '")

		for _, keyword := range keywords {
			if strings.Contains(query, strings.ToLower(keyword)) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// traverseTree traverses the decision tree and returns applicable nodes
func (dt *DecisionTree) traverseTree(query string, context *AgentContext) []*DecisionNode {
	var applicableNodes []*DecisionNode
	dt.traverseNode(dt.Root, query, context, &applicableNodes)
	return applicableNodes
}

// traverseNode recursively traverses tree nodes
func (dt *DecisionTree) traverseNode(node *DecisionNode, query string, context *AgentContext, applicable *[]*DecisionNode) {
	if dt.evaluateCondition(node.Condition, query, context) {
		*applicable = append(*applicable, node)
		dt.CurrentPath = append(dt.CurrentPath, node.ID)

		// Add to decisions history
		dt.Decisions = append(dt.Decisions, *node)

		// Traverse children
		for _, child := range node.Children {
			dt.traverseNode(child, query, context, applicable)
		}
	}
}

// Parallel Agent Implementation

// NewAgentCoordinator creates a new agent coordinator
func NewAgentCoordinator(mainContext *AgentContext) *AgentCoordinator {
	return &AgentCoordinator{
		Agents:         []*ParallelAgent{},
		DecisionTree:   NewDecisionTree(),
		MainContext:    mainContext,
		CompletedCount: 0,
		FailedCount:    0,
		TotalAgents:    0,
	}
}

// SpawnAgents creates and starts parallel agents based on decision tree results with dependency management
func (ac *AgentCoordinator) SpawnAgents(ctx context.Context, agent *Agent, applicableNodes []*DecisionNode) {
	verbose := viper.GetBool("verbose")

	// Collect unique agent types with their priorities and parameters
	agentConfigs := make(map[string]struct {
		Priority   int
		Parameters AWSData
		AgentType  AgentType
	})

	for _, node := range applicableNodes {
		for _, agentTypeStr := range node.AgentTypes {
			var agentType AgentType
			switch agentTypeStr {
			case "log":
				agentType = AgentTypeLog
			case "metrics":
				agentType = AgentTypeMetrics
			case "infrastructure":
				agentType = AgentTypeInfrastructure
			case "security":
				agentType = AgentTypeSecurity
			case "cost":
				agentType = AgentTypeCost
			case "performance":
				agentType = AgentTypePerformance
			default:
				continue
			}

			if existing, exists := agentConfigs[agentTypeStr]; !exists || node.Priority > existing.Priority {
				agentConfigs[agentTypeStr] = struct {
					Priority   int
					Parameters AWSData
					AgentType  AgentType
				}{
					Priority:   node.Priority,
					Parameters: node.Parameters,
					AgentType:  agentType,
				}
			}
		}
	}

	if verbose {
		fmt.Printf("🚀 Spawning %d parallel agents with dependency management\n", len(agentConfigs))
	}

	// Sort agents by execution order
	sortedAgents := ac.sortAgentsByDependencies(agentConfigs)

	// Create shared data store for inter-agent communication
	sharedData := make(map[string]interface{})

	// Execute agents in dependency order with parallel execution within same order
	for orderGroup, agentsInGroup := range sortedAgents {
		if verbose {
			fmt.Printf("📊 Executing order group %d with %d agents\n", orderGroup, len(agentsInGroup))
		}

		// Execute all agents in this order group in parallel
		var wg sync.WaitGroup
		for _, agentConfig := range agentsInGroup {
			// Check if all dependencies are satisfied
			if ac.dependenciesSatisfied(agentConfig.AgentType, sharedData) {
				wg.Add(1)
				go ac.executeAgentWithDependencies(ctx, agent, &wg, agentConfig, sharedData)
			} else if verbose {
				fmt.Printf("⏸️  Agent %s waiting for dependencies\n", agentConfig.AgentType.Name)
			}
		}

		// Wait for this order group to complete before proceeding
		wg.Wait()

		if verbose {
			fmt.Printf("✅ Order group %d completed\n", orderGroup)
		}
	}
}

// runParallelAgent runs a single parallel agent
func (ac *AgentCoordinator) runParallelAgent(ctx context.Context, agent *Agent, parallelAgent *ParallelAgent) {
	defer func() {
		parallelAgent.EndTime = time.Now()
		if parallelAgent.Error != nil {
			parallelAgent.Status = "failed"
			ac.FailedCount++
		} else {
			parallelAgent.Status = "completed"
			ac.CompletedCount++
		}
	}()

	verbose := viper.GetBool("verbose")
	if verbose {
		fmt.Printf("🤖 Agent %s (%s) executing %d operations\n",
			parallelAgent.ID, parallelAgent.Type.Name, len(parallelAgent.Operations))
	}

	// Execute operations for this agent type
	for _, operation := range parallelAgent.Operations {
		result, err := agent.client.executeOperation(ctx, operation.Operation, operation.Parameters)
		if err != nil {
			parallelAgent.Error = err
			if verbose {
				fmt.Printf("❌ Agent %s operation %s failed: %v\n", parallelAgent.ID, operation.Operation, err)
			}
			return
		}

		// Store result
		resultKey := fmt.Sprintf("%s_%s", parallelAgent.Type.Name, operation.Operation)
		parallelAgent.Results[resultKey] = result

		if verbose {
			fmt.Printf("✅ Agent %s completed operation: %s\n", parallelAgent.ID, operation.Operation)
		}
	}
}

// copyContextForAgent creates a copy of the main context for an agent
func (ac *AgentCoordinator) copyContextForAgent(agentType AgentType) *AgentContext {
	return &AgentContext{
		OriginalQuery:  ac.MainContext.OriginalQuery,
		CurrentStep:    0,
		MaxSteps:       3, // Parallel agents get fewer steps
		GatheredData:   make(AWSData),
		Decisions:      []AgentDecision{},
		ChainOfThought: []ChainOfThought{},
		ServiceData:    make(ServiceData),
		Metrics:        make(MetricsData),
		ServiceStatus:  make(map[string]string),
		LastUpdateTime: time.Now(),
	}
}

// getOperationsForAgentType returns operations specific to each agent type
func (ac *AgentCoordinator) getOperationsForAgentType(agentType AgentType, parameters AWSData) []LLMOperation {
	switch agentType.Name {
	case "log":
		operations := []LLMOperation{
			{Operation: "list_log_groups", Reason: "Find available log groups", Parameters: map[string]any{}},
			{Operation: "get_recent_logs", Reason: "Get recent log entries", Parameters: map[string]any{"hours_back": 1}},
			{Operation: "get_error_logs", Reason: "Get error log entries", Parameters: map[string]any{"filter_pattern": "ERROR"}},
		}

		// Add specific Lambda function analysis for critical functions
		lambdaFunctions := []string{"abel-dev-api", "abel-dev-chat", "abel-dev-health-checker"}
		for _, funcName := range lambdaFunctions {
			operations = append(operations, []LLMOperation{
				{Operation: "analyze_lambda_errors", Reason: fmt.Sprintf("Analyze errors for %s", funcName), Parameters: map[string]any{"function_name": funcName}},
				{Operation: "analyze_lambda_performance", Reason: fmt.Sprintf("Analyze performance for %s", funcName), Parameters: map[string]any{"function_name": funcName}},
				{Operation: "get_lambda_recent_logs", Reason: fmt.Sprintf("Get recent logs for %s", funcName), Parameters: map[string]any{"function_name": funcName}},
			}...)
		}

		return operations
	case "metrics":
		return []LLMOperation{
			{Operation: "list_cloudwatch_alarms", Reason: "Get CloudWatch alarms", Parameters: map[string]any{}},
			{Operation: "get_cost_and_usage", Reason: "Get cost metrics", Parameters: map[string]any{}},
		}
	case "infrastructure":
		operations := []LLMOperation{
			{Operation: "list_ec2_instances", Reason: "List EC2 instances", Parameters: map[string]any{}},
			{Operation: "list_ecs_clusters", Reason: "List ECS clusters", Parameters: map[string]any{}},
			{Operation: "list_lambda_functions", Reason: "List Lambda functions", Parameters: map[string]any{}},
			{Operation: "list_rds_instances", Reason: "List RDS instances", Parameters: map[string]any{}},
		}

		// Add ECS service log analysis if ECS is detected
		ecsServices := []string{"abel-dev"} // Common service pattern
		for _, serviceName := range ecsServices {
			operations = append(operations, LLMOperation{
				Operation:  "analyze_ecs_service_logs",
				Reason:     fmt.Sprintf("Analyze ECS service logs for %s", serviceName),
				Parameters: map[string]any{"service_name": serviceName},
			})
		}

		return operations
	case "security":
		return []LLMOperation{
			{Operation: "list_iam_roles", Reason: "List IAM roles", Parameters: map[string]any{}},
			{Operation: "list_security_groups", Reason: "List security groups", Parameters: map[string]any{}},
		}
	case "cost":
		return []LLMOperation{
			{Operation: "get_cost_and_usage", Reason: "Get cost analysis", Parameters: map[string]any{}},
			{Operation: "list_budgets", Reason: "List budgets", Parameters: map[string]any{}},
		}
	case "performance":
		return []LLMOperation{
			{Operation: "list_cloudwatch_alarms", Reason: "Get performance alarms", Parameters: map[string]any{}},
			{Operation: "describe_auto_scaling_group", Reason: "Get auto scaling info", Parameters: map[string]any{}},
		}
	default:
		return []LLMOperation{}
	}
}

// WaitForCompletion waits for all parallel agents to complete
func (ac *AgentCoordinator) WaitForCompletion(ctx context.Context, timeout time.Duration) error {
	verbose := viper.GetBool("verbose")
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		completed := ac.CompletedCount + ac.FailedCount
		if completed >= ac.TotalAgents {
			if verbose {
				fmt.Printf("🎉 All %d agents completed (%d successful, %d failed)\n",
					ac.TotalAgents, ac.CompletedCount, ac.FailedCount)
			}
			return nil
		}

		if verbose && completed > 0 {
			fmt.Printf("⏳ Waiting for agents: %d/%d completed\n", completed, ac.TotalAgents)
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for agents to complete")
}

// AggregateResults combines results from all parallel agents
func (ac *AgentCoordinator) AggregateResults() AWSData {
	aggregated := make(AWSData)

	for _, agent := range ac.Agents {
		if agent.Status == "completed" {
			// Merge agent results into aggregated data
			agentKey := agent.Type.Name
			aggregated[agentKey] = agent.Results

			// Also merge individual results with prefixed keys
			for key, value := range agent.Results {
				aggregatedKey := fmt.Sprintf("%s_%s", agentKey, key)
				aggregated[aggregatedKey] = value
			}
		}
	}

	// Add metadata about the parallel execution
	aggregated["_metadata"] = AWSData{
		"total_agents":    ac.TotalAgents,
		"completed_count": ac.CompletedCount,
		"failed_count":    ac.FailedCount,
		"decision_path":   ac.DecisionTree.CurrentPath,
		"execution_time":  time.Now().Format(time.RFC3339),
	}

	return aggregated
}

// NewSemanticAnalyzer creates a semantic analyzer with predefined patterns
func NewSemanticAnalyzer() *SemanticAnalyzer {
	return &SemanticAnalyzer{
		KeywordWeights: map[string]float64{
			"error":    1.0,
			"failed":   0.9,
			"warning":  0.7,
			"critical": 1.0,
			"down":     0.9,
			"slow":     0.6,
			"timeout":  0.8,
			"crash":    1.0,
			"debug":    0.3,
			"info":     0.2,
			"success":  0.1,
			"healthy":  0.1,
		},
		ContextPatterns: map[string][]string{
			"troubleshoot": {"error", "failed", "broken", "issue", "problem", "trouble"},
			"monitor":      {"status", "health", "performance", "metrics", "dashboard"},
			"analyze":      {"data", "logs", "patterns", "trends", "analysis"},
			"investigate":  {"investigate", "find", "search", "look", "check"},
		},
		ServiceMapping: map[string][]string{
			"lambda":      {"lambda", "function", "serverless"},
			"ec2":         {"ec2", "instance", "server", "vm"},
			"rds":         {"rds", "database", "db", "mysql", "postgres"},
			"s3":          {"s3", "bucket", "storage", "object"},
			"cloudwatch":  {"cloudwatch", "logs", "metrics", "alarm"},
			"ecs":         {"ecs", "container", "docker", "task"},
			"api_gateway": {"api", "gateway", "endpoint", "rest"},
		},
		IntentSignals: map[string]map[string]float64{
			"troubleshoot": {
				"error":   1.0,
				"failed":  0.9,
				"issue":   0.8,
				"problem": 0.8,
			},
			"monitor": {
				"status":      0.9,
				"health":      0.8,
				"performance": 0.7,
				"metrics":     0.6,
			},
			"analyze": {
				"analyze":  1.0,
				"data":     0.7,
				"patterns": 0.8,
				"trends":   0.6,
			},
		},
		UrgencyKeywords: map[string]float64{
			"critical":  1.0,
			"urgent":    0.9,
			"emergency": 1.0,
			"down":      0.9,
			"outage":    1.0,
			"crash":     0.8,
			"failed":    0.7,
		},
		TimeFrameWords: map[string]string{
			"now":        "real_time",
			"current":    "real_time",
			"latest":     "recent",
			"recent":     "recent",
			"today":      "recent",
			"yesterday":  "recent",
			"last":       "recent",
			"historical": "historical",
			"past":       "historical",
			"old":        "historical",
		},
	}
}

// AnalyzeQuery performs semantic analysis on a user query
func (sa *SemanticAnalyzer) AnalyzeQuery(query string) QueryIntent {
	queryLower := strings.ToLower(query)
	words := strings.Fields(queryLower)

	intent := QueryIntent{
		Confidence:     0.0,
		TargetServices: []string{},
		Urgency:        "medium",
		TimeFrame:      "recent",
		DataTypes:      []string{},
	}

	// Analyze primary intent
	intentScores := make(map[string]float64)
	for intentType, signals := range sa.IntentSignals {
		score := 0.0
		for _, word := range words {
			if weight, exists := signals[word]; exists {
				score += weight
			}
		}
		intentScores[intentType] = score
	}

	// Find primary intent
	maxScore := 0.0
	for intentType, score := range intentScores {
		if score > maxScore {
			maxScore = score
			intent.Primary = intentType
		}
	}

	// Set confidence based on score
	intent.Confidence = math.Min(maxScore/float64(len(words)), 1.0)

	// Identify target services
	for service, keywords := range sa.ServiceMapping {
		for _, keyword := range keywords {
			if strings.Contains(queryLower, keyword) {
				intent.TargetServices = append(intent.TargetServices, service)
				break
			}
		}
	}

	// Determine urgency
	urgencyScore := 0.0
	for _, word := range words {
		if weight, exists := sa.UrgencyKeywords[word]; exists {
			urgencyScore += weight
		}
	}
	if urgencyScore >= 1.0 {
		intent.Urgency = "critical"
	} else if urgencyScore >= 0.7 {
		intent.Urgency = "high"
	} else if urgencyScore >= 0.3 {
		intent.Urgency = "medium"
	} else {
		intent.Urgency = "low"
	}

	// Determine time frame
	for _, word := range words {
		if timeFrame, exists := sa.TimeFrameWords[word]; exists {
			intent.TimeFrame = timeFrame
			break
		}
	}

	// Determine data types needed
	dataTypeKeywords := map[string]string{
		"log":     "logs",
		"logs":    "logs",
		"metric":  "metrics",
		"metrics": "metrics",
		"config":  "config",
		"status":  "status",
	}
	for _, word := range words {
		if dataType, exists := dataTypeKeywords[word]; exists {
			intent.DataTypes = append(intent.DataTypes, dataType)
		}
	}

	// Default data types if none specified
	if len(intent.DataTypes) == 0 {
		switch intent.Primary {
		case "troubleshoot":
			intent.DataTypes = []string{"logs", "metrics", "status"}
		case "monitor":
			intent.DataTypes = []string{"metrics", "status"}
		case "analyze":
			intent.DataTypes = []string{"logs", "metrics"}
		default:
			intent.DataTypes = []string{"status"}
		}
	}

	return intent
}

// NewAgentMemory creates a new agent memory instance
func NewAgentMemory(maxQueries int) *AgentMemory {
	return &AgentMemory{
		PreviousQueries: make([]QueryContext, 0, maxQueries),
		ServiceHealth:   make(map[string]HealthStatus),
		UserPreferences: make(map[string]interface{}),
		LearnedPatterns: make([]Pattern, 0),
		LastUpdated:     time.Now(),
		MaxQueries:      maxQueries,
	}
}

// AddQueryContext adds a new query context to memory
func (am *AgentMemory) AddQueryContext(ctx QueryContext) {
	am.PreviousQueries = append(am.PreviousQueries, ctx)

	// Keep only the last MaxQueries entries
	if len(am.PreviousQueries) > am.MaxQueries {
		am.PreviousQueries = am.PreviousQueries[1:]
	}

	am.LastUpdated = time.Now()
}

// UpdateServiceHealth updates the health status of a service
func (am *AgentMemory) UpdateServiceHealth(service string, status HealthStatus) {
	am.ServiceHealth[service] = status
	am.LastUpdated = time.Now()
}

// GetSimilarQueries finds queries similar to the current one
func (am *AgentMemory) GetSimilarQueries(intent QueryIntent, limit int) []QueryContext {
	var similar []QueryContext

	for _, prev := range am.PreviousQueries {
		score := am.calculateSimilarity(intent, prev.Intent)
		if score > 0.5 { // threshold for similarity
			similar = append(similar, prev)
		}
	}

	// Sort by similarity and return top results
	sort.Slice(similar, func(i, j int) bool {
		return am.calculateSimilarity(intent, similar[i].Intent) >
			am.calculateSimilarity(intent, similar[j].Intent)
	})

	if len(similar) > limit {
		similar = similar[:limit]
	}

	return similar
}

// calculateSimilarity calculates similarity between two query intents
func (am *AgentMemory) calculateSimilarity(a, b QueryIntent) float64 {
	score := 0.0

	// Primary intent match
	if a.Primary == b.Primary {
		score += 0.4
	}

	// Service overlap
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

	// Urgency similarity
	urgencyWeights := map[string]float64{"low": 1, "medium": 2, "high": 3, "critical": 4}
	urgencyDiff := math.Abs(urgencyWeights[a.Urgency] - urgencyWeights[b.Urgency])
	score += 0.2 * (1.0 - urgencyDiff/3.0)

	// Time frame match
	if a.TimeFrame == b.TimeFrame {
		score += 0.1
	}

	return score
}

// LearnPattern learns a new pattern from query results
func (am *AgentMemory) LearnPattern(name, description string, conditions []string) {
	// Check if pattern already exists
	for i, pattern := range am.LearnedPatterns {
		if pattern.Name == name {
			am.LearnedPatterns[i].Frequency++
			am.LearnedPatterns[i].LastSeen = time.Now()
			return
		}
	}

	// Add new pattern
	newPattern := Pattern{
		Name:        name,
		Description: description,
		Frequency:   1,
		LastSeen:    time.Now(),
		Accuracy:    0.5, // default accuracy
		Conditions:  conditions,
	}

	am.LearnedPatterns = append(am.LearnedPatterns, newPattern)
	am.LastUpdated = time.Now()
}

// sortAgentsByDependencies sorts agents into execution order groups
func (ac *AgentCoordinator) sortAgentsByDependencies(agentConfigs map[string]struct {
	Priority   int
	Parameters AWSData
	AgentType  AgentType
}) map[int][]struct {
	Priority   int
	Parameters AWSData
	AgentType  AgentType
} {
	orderGroups := make(map[int][]struct {
		Priority   int
		Parameters AWSData
		AgentType  AgentType
	})

	for _, config := range agentConfigs {
		order := config.AgentType.Dependencies.ExecutionOrder
		orderGroups[order] = append(orderGroups[order], config)
	}

	return orderGroups
}

// dependenciesSatisfied checks if an agent's dependencies are satisfied
func (ac *AgentCoordinator) dependenciesSatisfied(agentType AgentType, sharedData map[string]interface{}) bool {
	for _, requiredData := range agentType.Dependencies.RequiredData {
		if _, exists := sharedData[requiredData]; !exists {
			return false
		}
	}
	return true
}

// executeAgentWithDependencies executes an agent with dependency management
func (ac *AgentCoordinator) executeAgentWithDependencies(
	ctx context.Context,
	agent *Agent,
	wg *sync.WaitGroup,
	config struct {
		Priority   int
		Parameters AWSData
		AgentType  AgentType
	},
	sharedData map[string]interface{},
) {
	defer wg.Done()

	verbose := viper.GetBool("verbose")

	// Create parallel agent
	parallelAgent := &ParallelAgent{
		ID:         fmt.Sprintf("%s_%d", config.AgentType.Name, time.Now().UnixNano()),
		Type:       config.AgentType,
		Status:     "running",
		StartTime:  time.Now(),
		Context:    ac.copyContextForAgent(config.AgentType),
		Results:    make(AWSData),
		Operations: ac.getOperationsForAgentType(config.AgentType, config.Parameters),
	}

	ac.Agents = append(ac.Agents, parallelAgent)
	ac.TotalAgents++

	if verbose {
		fmt.Printf("  ✨ Started %s agent (ID: %s) with dependencies\n", config.AgentType.Name, parallelAgent.ID)
	}

	// Execute the agent
	ac.runParallelAgent(ctx, agent, parallelAgent)

	// Add provided data to shared store
	for _, providedData := range config.AgentType.Dependencies.ProvidedData {
		if data, exists := parallelAgent.Results[providedData]; exists {
			sharedData[providedData] = data
		}
	}

	if verbose {
		fmt.Printf("✅ Agent %s completed, provided data: %v\n",
			config.AgentType.Name, config.AgentType.Dependencies.ProvidedData)
	}
}
