package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

func (a *Agent) runSequentialPlanner(ctx context.Context, agentCtx *AgentContext) error {
	verbose := viper.GetBool("debug")

	for agentCtx.CurrentStep < agentCtx.MaxSteps {
		agentCtx.CurrentStep++

		if verbose {
			fmt.Printf("🔍 Step %d/%d: Analyzing current context...\n", agentCtx.CurrentStep, agentCtx.MaxSteps)
		}

		a.addThought(agentCtx, fmt.Sprintf("Step %d: Analyzing what information is needed next", agentCtx.CurrentStep), "reason", "Evaluating available context and determining next action")

		decision, err := a.makeDecision(ctx, agentCtx)
		if err != nil {
			a.addThought(agentCtx, fmt.Sprintf("Failed to make decision: %v", err), "error", "Decision making failed")
			return fmt.Errorf("failed to make decision at step %d: %w", agentCtx.CurrentStep, err)
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

		if err := a.executeDecision(ctx, agentCtx, decision); err != nil {
			a.addThought(agentCtx, fmt.Sprintf("Execution failed: %v", err), "error", "Will continue with available information")
			if verbose {
				fmt.Printf("⚠️  Warning: Failed to execute decision: %v\n", err)
			}
		} else {
			a.addThought(agentCtx, fmt.Sprintf("Successfully executed %s", decision.Action), "success", "Information gathered successfully")
		}

		agentCtx.LastUpdateTime = time.Now()
	}

	return nil
}

func (a *Agent) makeDecision(ctx context.Context, agentCtx *AgentContext) (*AgentDecision, error) {
	if a.aiDecisionFn != nil {
		prompt := a.BuildDecisionPrompt(agentCtx)
		response, err := a.aiDecisionFn(ctx, prompt)
		if err != nil {
			if a.debug {
				fmt.Printf("⚠️  AI decision failed: %v, falling back to rule-based decision\n", err)
			}
		} else {
			var decision AgentDecision
			if err := json.Unmarshal([]byte(response), &decision); err != nil {
				if a.debug {
					fmt.Printf("⚠️  Failed to parse AI decision JSON: %v, falling back to rule-based decision\n", err)
				}
			} else {
				if a.debug {
					fmt.Printf("🧠 AI decision: %s (confidence: %.2f)\n", decision.Action, decision.Confidence)
				}
				return &decision, nil
			}
		}
	}

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

func (a *Agent) executeDecision(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("debug")

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

func (a *Agent) gatherLogs(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("debug")

	if verbose {
		fmt.Printf("📋 Gathering logs for service: %s\n", decision.Service)
	}

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

	for _, logGroup := range logGroups {
		if verbose {
			fmt.Printf("📋 Fetching recent logs from: %s\n", logGroup)
		}

		recentLogs, err := a.getRecentLogsFromGroup(ctx, logGroup)
		if err != nil {
			if verbose {
				fmt.Printf("⚠️  Failed to get recent logs from %s: %v\n", logGroup, err)
			}
			continue
		}

		errorLogs, err := a.getErrorLogsFromGroup(ctx, logGroup)
		if err != nil {
			if verbose {
				fmt.Printf("⚠️  Failed to get error logs from %s: %v\n", logGroup, err)
			}
		}

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

		logData := LogData{
			"log_group":     logGroup,
			"recent_logs":   recentLogs,
			"error_logs":    errorLogs,
			"log_streams":   logStreams,
			"total_entries": len(recentLogs),
			"error_count":   len(errorLogs),
			"stream_count":  len(logStreams),
		}

		serviceKey := fmt.Sprintf("%s_logs", decision.Service)
		if agentCtx.GatheredData[serviceKey] == nil {
			agentCtx.GatheredData[serviceKey] = []LogData{}
		}

		if existingLogs, ok := agentCtx.GatheredData[serviceKey].([]LogData); ok {
			agentCtx.GatheredData[serviceKey] = append(existingLogs, logData)
		} else {
			agentCtx.GatheredData[serviceKey] = []LogData{logData}
		}

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

func (a *Agent) gatherMetrics(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("debug")

	if verbose {
		fmt.Printf("📊 Gathering metrics for service: %s\n", decision.Service)
	}

	if len(decision.Operations) > 0 {
		result, err := a.client.ExecuteOperations(ctx, decision.Operations)
		if err != nil {
			return fmt.Errorf("failed to gather metrics: %w", err)
		}
		agentCtx.Metrics[decision.Service] = result
	}

	return nil
}

func (a *Agent) analyzeService(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("debug")

	if verbose {
		fmt.Printf("🔍 Analyzing service: %s\n", decision.Service)
	}

	if len(decision.Operations) > 0 {
		result, err := a.client.ExecuteOperations(ctx, decision.Operations)
		if err != nil {
			return fmt.Errorf("failed to analyze service: %w", err)
		}
		agentCtx.ServiceStatus[decision.Service] = result
	}

	return nil
}

func (a *Agent) investigateErrors(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("debug")

	if verbose {
		fmt.Printf("🚨 Investigating errors for service: %s\n", decision.Service)
	}

	var allLogs []string
	for key, data := range agentCtx.GatheredData {
		if strings.HasSuffix(key, "_logs") {
			switch v := data.(type) {
			case []string:
				allLogs = append(allLogs, v...)
			case []LogData:
				for _, entry := range v {
					if logs, ok := entry["recent_logs"].([]string); ok {
						allLogs = append(allLogs, logs...)
					}
					if logs, ok := entry["error_logs"].([]string); ok {
						allLogs = append(allLogs, logs...)
					}
				}
			}
		}
	}

	errorPatterns := a.findErrorPatterns(allLogs)
	agentCtx.GatheredData["error_patterns"] = errorPatterns

	return nil
}

func (a *Agent) executeAWSFunctionCalls(ctx context.Context, agentCtx *AgentContext, decision *AgentDecision) error {
	verbose := viper.GetBool("debug")

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

		resultKey := fmt.Sprintf("aws_function_%s_%s", awsFunc.ServiceType, awsFunc.Function)
		agentCtx.GatheredData[resultKey] = result

		if verbose {
			fmt.Printf("✅ AWS function %s completed successfully\n", awsFunc.Function)
		}
	}

	return nil
}

func (a *Agent) executeAWSFunction(ctx context.Context, awsFunc AWSFunctionCall) (any, error) {
	result, err := a.client.ExecuteOperation(ctx, awsFunc.Function, awsFunc.Parameters)
	if err != nil {
		return nil, err
	}
	return result, nil
}
