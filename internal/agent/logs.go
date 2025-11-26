package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// discoverLogGroups dynamically discovers relevant log groups based on service name and query
func (a *Agent) discoverLogGroups(ctx context.Context, serviceName, originalQuery string) ([]string, error) {
	verbose := viper.GetBool("verbose")

	allLogGroups, err := a.getAllLogGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get log groups: %w", err)
	}

	if verbose {
		fmt.Printf("🔍 Discovered %d total log groups\n", len(allLogGroups))
	}

	var relevantGroups []string
	queryLower := strings.ToLower(originalQuery)
	serviceLower := strings.ToLower(serviceName)
	queryKeywords := a.extractKeywordsFromQuery(queryLower)

	for _, logGroup := range allLogGroups {
		logGroupLower := strings.ToLower(logGroup)

		if serviceLower != "" && serviceLower != "general" && strings.Contains(logGroupLower, serviceLower) {
			relevantGroups = append(relevantGroups, logGroup)
			continue
		}

		for _, keyword := range queryKeywords {
			if strings.Contains(logGroupLower, keyword) {
				relevantGroups = append(relevantGroups, logGroup)
				break
			}
		}
	}

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

// getAllLogGroups gets all CloudWatch log groups via CLI
func (a *Agent) getAllLogGroups(ctx context.Context) ([]string, error) {
	args := []string{
		"logs", "describe-log-groups",
		"--output", "json",
	}

	output, err := a.client.ExecCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	var logData struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
		} `json:"logGroups"`
	}

	if err := json.Unmarshal([]byte(output), &logData); err != nil {
		return nil, err
	}

	logGroups := make([]string, 0, len(logData.LogGroups))
	for _, group := range logData.LogGroups {
		logGroups = append(logGroups, group.LogGroupName)
	}

	return logGroups, nil
}

// extractKeywordsFromQuery extracts relevant keywords from the user's query
func (a *Agent) extractKeywordsFromQuery(query string) []string {
	var keywords []string
	words := strings.Fields(query)
	for _, word := range words {
		word = strings.Trim(word, ".,!?;:")
		if len(word) > 2 {
			keywords = append(keywords, word)
		}
	}
	return keywords
}

// getMostActiveLogGroups returns a limited slice of log groups as a fallback
func (a *Agent) getMostActiveLogGroups(allGroups []string, limit int) []string {
	if len(allGroups) <= limit {
		return allGroups
	}
	return allGroups[:limit]
}

// getRecentLogsFromGroup fetches recent log messages for a specific log group
func (a *Agent) getRecentLogsFromGroup(ctx context.Context, logGroup string) ([]string, error) {
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

	output, err := a.client.ExecCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	var logData struct {
		Events []struct {
			Message   string `json:"message"`
			Timestamp int64  `json:"timestamp"`
		} `json:"events"`
	}

	if err := json.Unmarshal([]byte(output), &logData); err != nil {
		return nil, err
	}

	logs := make([]string, 0, len(logData.Events))
	for _, event := range logData.Events {
		logs = append(logs, event.Message)
	}

	return logs, nil
}

// findErrorPatterns counts common error keywords within aggregated logs
func (a *Agent) findErrorPatterns(allLogs []string) ErrorPatterns {
	patterns := make(ErrorPatterns)
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
