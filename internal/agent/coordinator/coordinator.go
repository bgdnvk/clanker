// Package coordinator orchestrates dependency-aware parallel agent execution.
package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	dt "github.com/bgdnvk/clanker/internal/agent/decisiontree"
	"github.com/bgdnvk/clanker/internal/agent/model"
	awsclient "github.com/bgdnvk/clanker/internal/aws"
	"github.com/spf13/viper"
)

// Dependency captures coordination requirements for a parallel agent type.

// ParallelAgent represents a running worker instance.
type ParallelAgent struct {
	ID         string
	Type       AgentType
	Status     string
	StartTime  time.Time
	EndTime    time.Time
	Context    *model.AgentContext
	Results    model.AWSData
	Error      error
	Operations []awsclient.LLMOperation
}

// Coordinator drives decision-tree-based parallel execution.
type Coordinator struct {
	Agents         []*ParallelAgent
	DecisionTree   *dt.Tree
	MainContext    *model.AgentContext
	CompletedCount int
	FailedCount    int
	TotalAgents    int

	client *awsclient.Client
}

// New returns a ready-to-use coordinator.
func New(mainContext *model.AgentContext, client *awsclient.Client) *Coordinator {
	return &Coordinator{
		DecisionTree: dt.New(),
		MainContext:  mainContext,
		client:       client,
	}
}

// Analyze traverses the decision tree for the provided query.
func (c *Coordinator) Analyze(query string) []*dt.Node {
	return c.DecisionTree.Traverse(query, c.MainContext)
}

// SpawnAgents starts agents grouped by dependency order.
func (c *Coordinator) SpawnAgents(ctx context.Context, applicable []*dt.Node) {
	agentConfigs := make(map[string]struct {
		Priority   int
		Parameters model.AWSData
		AgentType  AgentType
	})

	for _, node := range applicable {
		for _, name := range node.AgentTypes {
			agt, ok := c.lookupAgentType(name)
			if !ok {
				continue
			}
			if existing, exists := agentConfigs[name]; !exists || node.Priority > existing.Priority {
				agentConfigs[name] = struct {
					Priority   int
					Parameters model.AWSData
					AgentType  AgentType
				}{
					Priority:   node.Priority,
					Parameters: node.Parameters,
					AgentType:  agt,
				}
			}
		}
	}

	if len(agentConfigs) == 0 {
		return
	}

	sorted := c.sortAgentsByDependencies(agentConfigs)
	var sharedData sync.Map
	verbose := viper.GetBool("verbose")

	for order, configs := range sorted {
		if verbose {
			fmt.Printf("📊 Executing order group %d with %d agents\n", order, len(configs))
		}
		var wg sync.WaitGroup
		for _, cfg := range configs {
			if c.dependenciesSatisfied(cfg.AgentType, &sharedData) {
				wg.Add(1)
				go c.executeAgentWithDependencies(ctx, &wg, cfg, &sharedData)
			} else if verbose {
				fmt.Printf("⏸️  Agent %s waiting for dependencies\n", cfg.AgentType.Name)
			}
		}
		wg.Wait()
		if verbose {
			fmt.Printf("✅ Order group %d completed\n", order)
		}
	}
}

// WaitForCompletion blocks until all agents finish or timeout occurs.
func (c *Coordinator) WaitForCompletion(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	verbose := viper.GetBool("verbose")

	for time.Now().Before(deadline) {
		completed := c.CompletedCount + c.FailedCount
		if completed >= c.TotalAgents {
			if verbose {
				fmt.Printf("🎉 All %d agents completed (%d successful, %d failed)\n",
					c.TotalAgents, c.CompletedCount, c.FailedCount)
			}
			return nil
		}
		if verbose && completed > 0 {
			fmt.Printf("⏳ Waiting for agents: %d/%d completed\n", completed, c.TotalAgents)
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for agents to complete")
}

// AggregateResults merges successful agent outputs.
func (c *Coordinator) AggregateResults() model.AWSData {
	aggregated := make(model.AWSData)
	for _, agent := range c.Agents {
		if agent.Status != "completed" {
			continue
		}
		agentKey := agent.Type.Name
		aggregated[agentKey] = agent.Results
		for key, value := range agent.Results {
			aggregated[fmt.Sprintf("%s_%s", agentKey, key)] = value
		}
	}

	aggregated["_metadata"] = model.AWSData{
		"total_agents":    c.TotalAgents,
		"completed_count": c.CompletedCount,
		"failed_count":    c.FailedCount,
		"decision_path":   c.DecisionTree.CurrentPath,
		"execution_time":  time.Now().Format(time.RFC3339),
	}

	return aggregated
}

func (c *Coordinator) executeAgentWithDependencies(
	ctx context.Context,
	wg *sync.WaitGroup,
	cfg struct {
		Priority   int
		Parameters model.AWSData
		AgentType  AgentType
	},
	sharedData *sync.Map,
) {
	defer wg.Done()
	verbose := viper.GetBool("verbose")

	parallelAgent := &ParallelAgent{
		ID:         fmt.Sprintf("%s_%d", cfg.AgentType.Name, time.Now().UnixNano()),
		Type:       cfg.AgentType,
		Status:     "running",
		StartTime:  time.Now(),
		Context:    c.copyContextForAgent(cfg.AgentType),
		Results:    make(model.AWSData),
		Operations: c.getOperationsForAgentType(cfg.AgentType, cfg.Parameters),
	}

	c.Agents = append(c.Agents, parallelAgent)
	c.TotalAgents++

	if verbose {
		fmt.Printf("  ✨ Started %s agent (ID: %s) with dependencies\n", cfg.AgentType.Name, parallelAgent.ID)
	}

	c.runParallelAgent(ctx, parallelAgent)

	for _, provided := range cfg.AgentType.Dependencies.ProvidedData {
		if data, ok := parallelAgent.Results[provided]; ok {
			sharedData.Store(provided, data)
		}
	}

	if verbose {
		fmt.Printf("✅ Agent %s completed, provided data: %v\n",
			cfg.AgentType.Name, cfg.AgentType.Dependencies.ProvidedData)
	}
}

func (c *Coordinator) runParallelAgent(ctx context.Context, agent *ParallelAgent) {
	defer func() {
		agent.EndTime = time.Now()
		if agent.Error != nil {
			agent.Status = "failed"
			c.FailedCount++
			return
		}
		agent.Status = "completed"
		c.CompletedCount++
	}()

	verbose := viper.GetBool("verbose")
	if verbose {
		fmt.Printf("🤖 Agent %s (%s) executing %d operations\n",
			agent.ID, agent.Type.Name, len(agent.Operations))
	}

	for _, op := range agent.Operations {
		var (
			result any
			err    error
		)

		switch op.Operation {
		case "discover_services":
			result, err = c.discoverServicesWithAI(ctx, op.Parameters)
		case "investigate_service_logs":
			var discovered map[string]any
			if skip, exists := op.Parameters["skip_discovery"]; exists && skip == true {
				discovered = make(map[string]any)
				for k, v := range c.MainContext.ServiceData {
					discovered[k] = v
				}
			} else {
				discovered = agent.Results
			}
			result, err = c.investigateServiceLogsWithAI(ctx, op.Parameters, discovered)
		default:
			result, err = c.client.ExecuteOperation(ctx, op.Operation, op.Parameters)
		}

		if err != nil {
			agent.Error = err
			if verbose {
				fmt.Printf("❌ Agent %s operation %s failed: %v\n", agent.ID, op.Operation, err)
			}
			if op.Operation == "discover_services" || op.Operation == "investigate_service_logs" {
				agent.Error = nil
				continue
			}
			return
		}

		key := fmt.Sprintf("%s_%s", agent.Type.Name, op.Operation)
		agent.Results[key] = result
		if verbose {
			fmt.Printf("✅ Agent %s completed operation: %s\n", agent.ID, op.Operation)
		}
	}
}

func (c *Coordinator) discoverServicesWithAI(ctx context.Context, params map[string]any) (any, error) {
	if len(c.MainContext.ServiceData) > 0 {
		return c.MainContext.ServiceData, nil
	}
	services, err := c.minimalServiceDiscovery(ctx)
	if err == nil {
		if typed, ok := services.(map[string]any); ok {
			for k, v := range typed {
				c.MainContext.ServiceData[k] = v
			}
		}
	}
	return services, err
}

func (c *Coordinator) minimalServiceDiscovery(ctx context.Context) (any, error) {
	services := make(map[string]any)
	if lambdaResult, err := c.client.ExecuteOperation(ctx, "list_lambda_functions", map[string]any{}); err == nil {
		services["lambda_functions"] = lambdaResult
	}
	if logsResult, err := c.client.ExecuteOperation(ctx, "list_log_groups", map[string]any{}); err == nil {
		services["log_groups"] = logsResult
	}
	return services, nil
}

func (c *Coordinator) investigateServiceLogsWithAI(ctx context.Context, params map[string]any, discovered map[string]any) (any, error) {
	return c.fallbackLogInvestigation(ctx, discovered)
}

func (c *Coordinator) fallbackLogInvestigation(ctx context.Context, discovered map[string]any) (any, error) {
	results := make(map[string]any)
	query := strings.ToLower(c.MainContext.OriginalQuery)
	keywords := c.extractKeywords(query)

	lambdaData, hasLambda := discovered["lambda_functions"]
	if !hasLambda {
		if nested, ok := discovered["log_discover_services"].(map[string]any); ok {
			lambdaData, hasLambda = nested["lambda_functions"]
		}
	}

	if hasLambda {
		relevant := c.findRelevantServices(lambdaData, query)
		if len(relevant) == 0 && len(keywords) > 0 {
			names := c.extractLambdaFunctionNames(lambdaData)
			for _, keyword := range keywords {
				for _, name := range names {
					if strings.Contains(strings.ToLower(name), keyword) {
						relevant = append(relevant, name)
					}
				}
			}
		}
		targets := uniqueStrings(relevant)
		if len(targets) > 5 {
			targets = targets[:5]
		}
		for _, name := range targets {
			logGroup := fmt.Sprintf("/aws/lambda/%s", name)
			out, err := c.client.ExecuteOperation(ctx, "get_recent_logs", map[string]any{
				"log_group_name": logGroup,
				"hours_back":     24,
				"limit":          300,
				"filter_pattern": "?ERROR ?Exception ?CRITICAL ?WARN ?WARNING",
			})
			if err == nil {
				results[fmt.Sprintf("lambda_%s_logs", name)] = out
			}
		}
	}

	if strings.Contains(query, "api") || strings.Contains(query, "gateway") {
		if data, ok := discovered["log_groups"]; ok {
			lines := strings.Split(fmt.Sprintf("%v", data), "\n")
			count := 0
			for _, line := range lines {
				if count >= 3 {
					break
				}
				if strings.Contains(line, "API-Gateway-Execution-Logs") && strings.Contains(line, "|") {
					parts := strings.Split(line, "|")
					for _, part := range parts {
						name := strings.TrimSpace(part)
						if strings.HasPrefix(name, "/") {
							out, err := c.client.ExecuteOperation(ctx, "get_recent_logs", map[string]any{
								"log_group_name": name,
								"hours_back":     24,
								"limit":          200,
								"filter_pattern": "?ERROR ?EXCEPTION ?5xx ?4xx",
							})
							if err == nil {
								results[fmt.Sprintf("apigw_%d_logs", count)] = out
								count++
							}
							break
						}
					}
				}
			}
		}
	}

	if len(results) == 0 {
		if data, ok := discovered["log_groups"]; ok {
			if general, err := c.investigateGeneralLogs(ctx, data); err == nil {
				results["general_logs"] = general
			}
		}
	}

	return results, nil
}

func (c *Coordinator) investigateGeneralLogs(ctx context.Context, logGroupData any) (any, error) {
	return c.client.ExecuteOperation(ctx, "get_recent_logs", map[string]any{
		"hours_back":     6,
		"limit":          200,
		"filter_pattern": "?ERROR ?Exception ?CRITICAL ?WARN ?WARNING",
	})
}

func (c *Coordinator) findRelevantServices(lambdaData any, query string) []string {
	var relevant []string
	keywords := c.extractKeywords(query)
	if lambdaMap, ok := lambdaData.(map[string]any); ok {
		if functions, exists := lambdaMap["lambda_functions"]; exists {
			lines := strings.Split(fmt.Sprintf("%v", functions), "\n")
			for _, line := range lines {
				lower := strings.ToLower(line)
				for _, keyword := range keywords {
					if strings.Contains(lower, keyword) && strings.Contains(line, "|") {
						parts := strings.Split(line, "|")
						for _, part := range parts {
							part = strings.TrimSpace(part)
							if strings.Contains(strings.ToLower(part), keyword) && !strings.Contains(part, "/") {
								relevant = append(relevant, part)
							}
						}
					}
				}
			}
		}
	}
	return relevant
}

func (c *Coordinator) extractKeywords(query string) []string {
	words := strings.FieldsFunc(query, func(r rune) bool {
		switch r {
		case ' ', ',', '.', '!', '?', ';', ':':
			return true
		}
		return false
	})
	seen := make(map[string]struct{})
	keywords := make([]string, 0, len(words))
	for _, word := range words {
		word = strings.Trim(word, ".,!?;:")
		if len(word) <= 2 {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		keywords = append(keywords, word)
	}
	return keywords
}

func (c *Coordinator) extractLambdaFunctionNames(lambdaData any) []string {
	var names []string
	lines := strings.Split(fmt.Sprintf("%v", lambdaData), "\n")
	nameIdx := -1
	for _, line := range lines {
		if strings.Contains(line, "|") && strings.Contains(line, "Name") {
			parts := strings.Split(line, "|")
			for idx, part := range parts {
				if strings.TrimSpace(part) == "Name" {
					nameIdx = idx
					break
				}
			}
			break
		}
	}
	for _, line := range lines {
		if !strings.Contains(line, "|") || strings.Contains(line, "---") {
			continue
		}
		parts := strings.Split(line, "|")
		if nameIdx >= 0 && nameIdx < len(parts) {
			name := strings.TrimSpace(parts[nameIdx])
			if name != "" && name != "Name" {
				names = append(names, name)
			}
			continue
		}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if len(part) > 3 && !strings.Contains(part, " ") && (strings.Contains(part, "-") || strings.Contains(part, "_")) {
				names = append(names, part)
				break
			}
		}
	}
	return uniqueStrings(names)
}

func (c *Coordinator) copyContextForAgent(agentType AgentType) *model.AgentContext {
	return &model.AgentContext{
		OriginalQuery:  c.MainContext.OriginalQuery,
		CurrentStep:    0,
		MaxSteps:       3,
		GatheredData:   make(model.AWSData),
		Decisions:      []model.AgentDecision{},
		ChainOfThought: []model.ChainOfThought{},
		ServiceData:    make(model.ServiceData),
		Metrics:        make(model.MetricsData),
		ServiceStatus:  make(map[string]string),
		LastUpdateTime: time.Now(),
	}
}

func (c *Coordinator) getOperationsForAgentType(agentType AgentType, params model.AWSData) []awsclient.LLMOperation {
	switch agentType.Name {
	case "log":
		if len(c.MainContext.ServiceData) > 0 {
			return []awsclient.LLMOperation{
				{Operation: "investigate_service_logs", Reason: "Investigate logs for already discovered services", Parameters: map[string]any{"query": c.MainContext.OriginalQuery, "skip_discovery": true}},
			}
		}
		return []awsclient.LLMOperation{
			{Operation: "discover_services", Reason: "Discover services matching the query", Parameters: map[string]any{"query": c.MainContext.OriginalQuery}},
			{Operation: "investigate_service_logs", Reason: "Investigate logs for discovered services", Parameters: map[string]any{"query": c.MainContext.OriginalQuery}},
		}
	case "metrics":
		return []awsclient.LLMOperation{{Operation: "list_cloudwatch_alarms", Reason: "Get CloudWatch alarms for performance issues", Parameters: map[string]any{}}}
	case "infrastructure":
		priority := "medium"
		if p, ok := params["priority"].(string); ok {
			priority = p
		}
		if priority == "high" || priority == "critical" {
			return []awsclient.LLMOperation{
				{Operation: "list_lambda_functions", Reason: "Quick Lambda discovery", Parameters: map[string]any{}},
				{Operation: "describe_log_groups", Reason: "Get log groups", Parameters: map[string]any{}},
			}
		}
		return []awsclient.LLMOperation{
			{Operation: "list_lambda_functions", Reason: "Broader Lambda discovery", Parameters: map[string]any{}},
			{Operation: "describe_log_groups", Reason: "Discover log groups", Parameters: map[string]any{}},
			{Operation: "describe_ecs_clusters", Reason: "Check ECS clusters", Parameters: map[string]any{}},
		}
	case "security":
		return []awsclient.LLMOperation{{Operation: "describe_guardduty_findings", Reason: "Check GuardDuty alerts", Parameters: map[string]any{}}}
	case "cost":
		return []awsclient.LLMOperation{{Operation: "get_cost_and_usage", Reason: "Analyze spending", Parameters: map[string]any{}}}
	case "performance":
		return []awsclient.LLMOperation{{Operation: "describe_auto_scaling_groups", Reason: "Check scaling state", Parameters: map[string]any{}}}
	case "deployment":
		return []awsclient.LLMOperation{
			{Operation: "list_codepipelines", Reason: "List active deployment pipelines", Parameters: map[string]any{}},
			{Operation: "list_codebuild_projects", Reason: "Check build projects for recent failures", Parameters: map[string]any{}},
		}
	case "datapipeline":
		return []awsclient.LLMOperation{
			{Operation: "list_glue_jobs", Reason: "Inspect Glue/ETL jobs", Parameters: map[string]any{}},
			{Operation: "list_step_functions", Reason: "Check orchestration state machines", Parameters: map[string]any{}},
			{Operation: "list_kinesis_streams", Reason: "Review streaming pipelines", Parameters: map[string]any{}},
		}
	case "queue":
		return []awsclient.LLMOperation{
			{Operation: "list_sqs_queues", Reason: "List queues and their attributes", Parameters: map[string]any{}},
			{Operation: "list_sns_topics", Reason: "Review SNS topics feeding queues", Parameters: map[string]any{}},
		}
	case "availability":
		return []awsclient.LLMOperation{
			{Operation: "check_route53_service", Reason: "Check DNS health", Parameters: map[string]any{}},
			{Operation: "list_route53_zones", Reason: "Inspect hosted zones for issues", Parameters: map[string]any{}},
		}
	case "llm":
		return []awsclient.LLMOperation{
			{Operation: "list_bedrock_foundation_models", Reason: "Review Bedrock model status", Parameters: map[string]any{}},
			{Operation: "list_sagemaker_endpoints", Reason: "Check SageMaker endpoint health", Parameters: map[string]any{}},
			{Operation: "list_sagemaker_models", Reason: "Reference deployed models", Parameters: map[string]any{}},
		}
	default:
		return nil
	}
}

func (c *Coordinator) sortAgentsByDependencies(agentConfigs map[string]struct {
	Priority   int
	Parameters model.AWSData
	AgentType  AgentType
}) map[int][]struct {
	Priority   int
	Parameters model.AWSData
	AgentType  AgentType
} {
	orderGroups := make(map[int][]struct {
		Priority   int
		Parameters model.AWSData
		AgentType  AgentType
	})
	for _, cfg := range agentConfigs {
		order := cfg.AgentType.Dependencies.ExecutionOrder
		orderGroups[order] = append(orderGroups[order], cfg)
	}
	return orderGroups
}

func (c *Coordinator) dependenciesSatisfied(agentType AgentType, sharedData *sync.Map) bool {
	for _, required := range agentType.Dependencies.RequiredData {
		if _, ok := sharedData.Load(required); !ok {
			return false
		}
	}
	return true
}

func (c *Coordinator) lookupAgentType(name string) (AgentType, bool) {
	switch name {
	case "log":
		return AgentTypeLog, true
	case "metrics":
		return AgentTypeMetrics, true
	case "infrastructure":
		return AgentTypeInfrastructure, true
	case "security":
		return AgentTypeSecurity, true
	case "cost":
		return AgentTypeCost, true
	case "performance":
		return AgentTypePerformance, true
	case "deployment":
		return AgentTypeDeployment, true
	case "datapipeline":
		return AgentTypeDataPipeline, true
	case "queue":
		return AgentTypeQueue, true
	case "availability":
		return AgentTypeAvailability, true
	case "llm":
		return AgentTypeLLM, true
	default:
		return AgentType{}, false
	}
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(in))
	for _, item := range in {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
