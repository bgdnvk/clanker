package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

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
	keywords := extractKeywords(query)

	lambdaData, hasLambda := discovered["lambda_functions"]
	if !hasLambda {
		if nested, ok := discovered["log_discover_services"].(map[string]any); ok {
			lambdaData, hasLambda = nested["lambda_functions"]
		}
	}

	if hasLambda {
		relevant := c.findRelevantServices(lambdaData, query)
		if len(relevant) == 0 && len(keywords) > 0 {
			names := extractLambdaFunctionNames(lambdaData)
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
	keywords := extractKeywords(query)
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

func extractKeywords(query string) []string {
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

func extractLambdaFunctionNames(lambdaData any) []string {
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

func (c *Coordinator) lookupAgentType(name string) (AgentType, bool) {
	switch name {
	case "k8s":
		return AgentTypeK8s, true
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

func (c *Coordinator) newParallelAgent(cfg AgentConfig) *ParallelAgent {
	return &ParallelAgent{
		ID:         fmt.Sprintf("%s_%d", cfg.AgentType.Name, time.Now().UnixNano()),
		Type:       cfg.AgentType,
		Status:     "running",
		StartTime:  time.Now(),
		Context:    CopyContextForAgent(c.MainContext),
		Results:    make(model.AWSData),
		Operations: c.operationsFor(cfg.AgentType, cfg.Parameters),
	}
}

func (c *Coordinator) persistProvidedData(agent *ParallelAgent) {
	if agent.Status != "completed" {
		return
	}
	if len(agent.Type.Dependencies.ProvidedData) == 0 {
		return
	}
	for _, provided := range agent.Type.Dependencies.ProvidedData {
		c.dataBus.Store(provided, agent.Results)
	}
}
