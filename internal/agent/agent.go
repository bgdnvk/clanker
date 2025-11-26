package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/agent/coordinator"
	dt "github.com/bgdnvk/clanker/internal/agent/decisiontree"
	"github.com/bgdnvk/clanker/internal/agent/memory"
	"github.com/bgdnvk/clanker/internal/agent/model"
	"github.com/bgdnvk/clanker/internal/agent/semantic"
	awsclient "github.com/bgdnvk/clanker/internal/aws"
	"github.com/spf13/viper"
)

type (
	AWSData          = model.AWSData
	ServiceData      = model.ServiceData
	MetricsData      = model.MetricsData
	LogData          = model.LogData
	ErrorPatterns    = model.ErrorPatterns
	QueryIntent      = model.QueryIntent
	QueryContext     = model.QueryContext
	HealthStatus     = model.HealthStatus
	HealthTrend      = model.HealthTrend
	Pattern          = model.Pattern
	AgentDecision    = model.AgentDecision
	AWSFunctionCall  = model.AWSFunctionCall
	ChainOfThought   = model.ChainOfThought
	AgentContext     = model.AgentContext
	SemanticAnalyzer = semantic.Analyzer
	AgentMemory      = memory.AgentMemory
	DecisionTree     = dt.Tree
	DecisionNode     = dt.Node
	LLMOperation     = awsclient.LLMOperation
)

// Agent represents the intelligent context-gathering agent
type Agent struct {
	client       *awsclient.Client
	debug        bool
	maxSteps     int
	memory       *AgentMemory
	aiDecisionFn func(context.Context, string) (string, error) // AI decision making function
}

// NewAgent creates a new intelligent agent for context gathering
func NewAgent(client *awsclient.Client, debug bool) *Agent {
	return &Agent{
		client:   client,
		debug:    debug,
		maxSteps: 3, // Reduced to 3 steps for faster decisions
	}
}

// SetAIDecisionFunction sets the AI decision making function
func (a *Agent) SetAIDecisionFunction(fn func(context.Context, string) (string, error)) {
	a.aiDecisionFn = fn
}

// InvestigateQuery intelligently investigates a query using decision trees and parallel agents
func (a *Agent) InvestigateQuery(ctx context.Context, query string) (*AgentContext, error) {
	verbose := viper.GetBool("verbose")

	// Perform semantic analysis on the query
	semanticAnalyzer := semantic.NewAnalyzer()
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
	agentCtx.GatheredData["semantic_analysis"] = map[string]any{
		"intent":          queryIntent,
		"confidence":      queryIntent.Confidence,
		"target_services": queryIntent.TargetServices,
		"urgency":         queryIntent.Urgency,
		"time_frame":      queryIntent.TimeFrame,
		"data_types":      queryIntent.DataTypes,
	}

	// Initialize agent memory if needed
	if a.memory == nil {
		a.memory = memory.New(50) // Keep last 50 queries
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
	coord := coordinator.New(agentCtx, a.client)

	// Traverse decision tree to determine what agents to spawn
	applicableNodes := coord.Analyze(query)

	if verbose {
		fmt.Printf("🌳 Decision tree analysis: %d applicable nodes found\n", len(applicableNodes))
		for _, node := range applicableNodes {
			fmt.Printf("  📊 Node: %s (priority: %d, agents: %v)\n", node.Name, node.Priority, node.AgentTypes)
		}
	}

	a.addThought(agentCtx, fmt.Sprintf("Decision tree identified %d applicable strategies", len(applicableNodes)), "analyze", "Determined parallel execution strategy")

	// Spawn parallel agents based on decision tree
	if len(applicableNodes) > 0 {
		coord.SpawnAgents(ctx, applicableNodes)

		// Wait for parallel agents to complete (with shorter timeout for faster decisions)
		timeout := 15 * time.Second
		err := coord.WaitForCompletion(ctx, timeout)
		if err != nil {
			a.addThought(agentCtx, fmt.Sprintf("Some parallel agents failed or timed out: %v", err), "warning", "Proceeding with available data")
			if verbose {
				fmt.Printf("⚠️  Warning: %v\n", err)
			}
		}

		// Aggregate results from parallel agents
		parallelResults := coord.AggregateResults()

		// Merge parallel results into main context
		for key, value := range parallelResults {
			agentCtx.GatheredData[key] = value
		}

		a.addThought(agentCtx, fmt.Sprintf("Completed parallel execution with %d agents", coord.TotalAgents), "success", "Data gathering completed")

		if verbose {
			fmt.Printf("🎉 Parallel execution completed: %d successful, %d failed\n",
				coord.CompletedCount, coord.FailedCount)
		}
	}

	// Fallback to traditional sequential approach if no parallel agents were spawned
	if len(applicableNodes) == 0 {
		a.addThought(agentCtx, "No specific parallel strategies identified, using sequential approach", "fallback", "Traditional investigation approach")

		if verbose {
			fmt.Printf("🔄 Using traditional sequential investigation approach\n")
		}

		if err := a.runSequentialPlanner(ctx, agentCtx); err != nil {
			return agentCtx, err
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

// Helper functions

// BuildDecisionPrompt creates a sophisticated prompt for AI-based decision making
func (a *Agent) BuildDecisionPrompt(agentCtx *AgentContext) string {
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

	output, err := a.client.ExecCLI(ctx, args)
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

	output, err := a.client.ExecCLI(ctx, args)
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
