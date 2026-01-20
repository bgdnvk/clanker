package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LogAnalyzer provides AI powered log analysis
type LogAnalyzer struct {
	aiDecisionFn AIDecisionFunc
	debug        bool
}

// NewLogAnalyzer creates a new log analyzer
func NewLogAnalyzer(debug bool) *LogAnalyzer {
	return &LogAnalyzer{
		debug: debug,
	}
}

// SetAIDecisionFunction sets the AI function
func (a *LogAnalyzer) SetAIDecisionFunction(fn AIDecisionFunc) {
	a.aiDecisionFn = fn
}

// Analyze performs AI powered analysis on aggregated logs
func (a *LogAnalyzer) Analyze(ctx context.Context, query string, logs *AggregatedLogs) (*LogAnalysis, error) {
	if a.aiDecisionFn == nil {
		return nil, fmt.Errorf("AI function not configured")
	}

	// Build the analysis prompt
	prompt := a.buildAnalysisPrompt(query, logs, false)

	if a.debug {
		fmt.Printf("[analyzer] sending analysis prompt (%d chars)\n", len(prompt))
	}

	response, err := a.aiDecisionFn(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	return a.parseAnalysisResponse(response, query)
}

// AnalyzeWithFixes performs analysis and suggests fixes
func (a *LogAnalyzer) AnalyzeWithFixes(ctx context.Context, query string, logs *AggregatedLogs) (*LogAnalysis, error) {
	if a.aiDecisionFn == nil {
		return nil, fmt.Errorf("AI function not configured")
	}

	prompt := a.buildAnalysisPrompt(query, logs, true)

	if a.debug {
		fmt.Printf("[analyzer] sending analysis with fixes prompt (%d chars)\n", len(prompt))
	}

	response, err := a.aiDecisionFn(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	return a.parseAnalysisResponse(response, query)
}

// buildAnalysisPrompt constructs the prompt for AI analysis
func (a *LogAnalyzer) buildAnalysisPrompt(query string, logs *AggregatedLogs, includeFixes bool) string {
	var sb strings.Builder

	sb.WriteString("You are analyzing Kubernetes container logs to identify issues and their root causes.\n\n")
	sb.WriteString(fmt.Sprintf("User Query: %s\n\n", query))
	sb.WriteString(fmt.Sprintf("Context: Logs from %s (%d pods, %d total lines)\n", logs.Source, logs.PodCount, logs.TotalLines))
	sb.WriteString(fmt.Sprintf("Error Count: %d, Warning Count: %d\n", logs.ErrorCount, logs.WarnCount))
	if !logs.TimeRange.Start.IsZero() {
		sb.WriteString(fmt.Sprintf("Time Range: %s to %s\n", logs.TimeRange.Start.Format(time.RFC3339), logs.TimeRange.End.Format(time.RFC3339)))
	}
	sb.WriteString("\n")

	// Include log entries, prioritizing errors
	sb.WriteString("=== Log Entries (errors prioritized) ===\n")

	// Get error logs first
	errorLogs := a.filterErrorLogs(logs.Entries, 30)
	for _, entry := range errorLogs {
		sb.WriteString(fmt.Sprintf("[%s] [%s/%s] %s\n", entry.Level, entry.Namespace, entry.Pod, truncateMessage(entry.Message, 500)))
	}

	// Add some non-error context if space allows
	if len(errorLogs) < 20 {
		remaining := 20 - len(errorLogs)
		count := 0
		for _, entry := range logs.Entries {
			if entry.Level != LevelError && count < remaining {
				sb.WriteString(fmt.Sprintf("[%s] [%s/%s] %s\n", entry.Level, entry.Namespace, entry.Pod, truncateMessage(entry.Message, 200)))
				count++
			}
		}
	}

	sb.WriteString("\n")

	// Request format
	sb.WriteString("Analyze these logs and respond with a JSON object containing:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"summary\": \"Brief overview of what is happening (2-3 sentences)\",\n")
	sb.WriteString("  \"issuesFound\": [\n")
	sb.WriteString("    {\"type\": \"error_type\", \"severity\": \"critical|warning|info\", \"description\": \"what the issue is\", \"occurrences\": number}\n")
	sb.WriteString("  ],\n")
	sb.WriteString("  \"rootCause\": \"What is likely causing these issues (be specific)\",\n")
	sb.WriteString("  \"impactAssessment\": \"How this affects the application or service\",\n")

	if includeFixes {
		sb.WriteString("  \"recommendations\": [\n")
		sb.WriteString("    {\n")
		sb.WriteString("      \"priority\": 1,\n")
		sb.WriteString("      \"action\": \"Short action title\",\n")
		sb.WriteString("      \"description\": \"Detailed explanation\",\n")
		sb.WriteString("      \"command\": \"kubectl command to run (if applicable)\",\n")
		sb.WriteString("      \"risk\": \"low|medium|high\"\n")
		sb.WriteString("    }\n")
		sb.WriteString("  ],\n")
	}

	sb.WriteString("  \"confidence\": 0.0 to 1.0\n")
	sb.WriteString("}\n\n")
	sb.WriteString("Respond with valid JSON only. Do not include markdown code blocks.")

	return sb.String()
}

// parseAnalysisResponse parses the AI response into structured analysis
func (a *LogAnalyzer) parseAnalysisResponse(response, query string) (*LogAnalysis, error) {
	analysis := &LogAnalysis{
		Query:      query,
		AnalyzedAt: time.Now(),
	}

	// Try to extract JSON from response
	jsonStr := extractJSON(response)
	if jsonStr != "" {
		var parsed struct {
			Summary          string `json:"summary"`
			IssuesFound      []struct {
				Type        string `json:"type"`
				Severity    string `json:"severity"`
				Description string `json:"description"`
				Occurrences int    `json:"occurrences"`
			} `json:"issuesFound"`
			RootCause        string  `json:"rootCause"`
			ImpactAssessment string  `json:"impactAssessment"`
			Recommendations  []struct {
				Priority    int    `json:"priority"`
				Action      string `json:"action"`
				Description string `json:"description"`
				Command     string `json:"command,omitempty"`
				Risk        string `json:"risk"`
			} `json:"recommendations"`
			Confidence float64 `json:"confidence"`
		}

		if err := json.Unmarshal([]byte(jsonStr), &parsed); err == nil {
			analysis.Summary = parsed.Summary
			analysis.RootCause = parsed.RootCause
			analysis.ImpactAssessment = parsed.ImpactAssessment
			analysis.Confidence = parsed.Confidence

			for _, issue := range parsed.IssuesFound {
				analysis.IssuesFound = append(analysis.IssuesFound, IdentifiedIssue{
					Type:        issue.Type,
					Severity:    issue.Severity,
					Description: issue.Description,
					Occurrences: issue.Occurrences,
				})
			}

			for _, rec := range parsed.Recommendations {
				analysis.Recommendations = append(analysis.Recommendations, Recommendation{
					Priority:    rec.Priority,
					Action:      rec.Action,
					Description: rec.Description,
					Command:     rec.Command,
					Risk:        rec.Risk,
				})
			}

			return analysis, nil
		}

		if a.debug {
			fmt.Printf("[analyzer] failed to parse JSON response, using raw text\n")
		}
	}

	// Fallback: use raw response as summary
	analysis.Summary = response
	analysis.Confidence = 0.5
	return analysis, nil
}

// filterErrorLogs returns up to maxLines of error logs, prioritizing by severity
func (a *LogAnalyzer) filterErrorLogs(entries []LogEntry, maxLines int) []LogEntry {
	var errors []LogEntry

	for _, entry := range entries {
		if entry.IsError || entry.Level == LevelError {
			errors = append(errors, entry)
		}
	}

	// Also include warnings if we have room
	if len(errors) < maxLines {
		for _, entry := range entries {
			if entry.Level == LevelWarn && len(errors) < maxLines {
				errors = append(errors, entry)
			}
		}
	}

	if len(errors) > maxLines {
		return errors[:maxLines]
	}

	return errors
}

// AnalyzeRootCause specifically analyzes for root cause of an issue
func (a *LogAnalyzer) AnalyzeRootCause(ctx context.Context, query string, logs *AggregatedLogs, resourceType, resourceName, namespace string) (*LogAnalysis, error) {
	if a.aiDecisionFn == nil {
		return nil, fmt.Errorf("AI function not configured")
	}

	prompt := a.buildRootCausePrompt(query, logs, resourceType, resourceName, namespace)

	if a.debug {
		fmt.Printf("[analyzer] sending root cause analysis prompt (%d chars)\n", len(prompt))
	}

	response, err := a.aiDecisionFn(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("root cause analysis failed: %w", err)
	}

	return a.parseAnalysisResponse(response, query)
}

// buildRootCausePrompt constructs a prompt specifically for root cause analysis
func (a *LogAnalyzer) buildRootCausePrompt(query string, logs *AggregatedLogs, resourceType, resourceName, namespace string) string {
	var sb strings.Builder

	sb.WriteString("You are a Kubernetes troubleshooting expert analyzing logs to find the root cause of an issue.\n\n")
	sb.WriteString(fmt.Sprintf("User Question: %s\n\n", query))
	sb.WriteString(fmt.Sprintf("Resource: %s/%s in namespace %s\n", resourceType, resourceName, namespace))
	sb.WriteString(fmt.Sprintf("Log Stats: %d total lines, %d errors, %d warnings\n\n", logs.TotalLines, logs.ErrorCount, logs.WarnCount))

	// Include error logs
	sb.WriteString("=== Error Logs ===\n")
	errorLogs := a.filterErrorLogs(logs.Entries, 40)
	for _, entry := range errorLogs {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", entry.Timestamp.Format("15:04:05"), truncateMessage(entry.Message, 400)))
	}

	sb.WriteString("\n\nBased on these logs, provide a root cause analysis in JSON format:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"summary\": \"Brief summary of the issue\",\n")
	sb.WriteString("  \"rootCause\": \"The specific root cause of this issue\",\n")
	sb.WriteString("  \"issuesFound\": [{\"type\": \"...\", \"severity\": \"...\", \"description\": \"...\", \"occurrences\": N}],\n")
	sb.WriteString("  \"impactAssessment\": \"What is affected by this issue\",\n")
	sb.WriteString("  \"recommendations\": [\n")
	sb.WriteString("    {\"priority\": 1, \"action\": \"...\", \"description\": \"...\", \"command\": \"kubectl ...\", \"risk\": \"low|medium|high\"}\n")
	sb.WriteString("  ],\n")
	sb.WriteString("  \"confidence\": 0.0 to 1.0\n")
	sb.WriteString("}\n\n")
	sb.WriteString("Be specific about the root cause and provide actionable recommendations. Respond with valid JSON only.")

	return sb.String()
}

// QuickAnalyze performs a quick analysis without AI for basic pattern detection
func (a *LogAnalyzer) QuickAnalyze(logs *AggregatedLogs) *LogAnalysis {
	analysis := &LogAnalysis{
		Query:      "quick analysis",
		AnalyzedAt: time.Now(),
		Confidence: 0.6,
	}

	// Count issues by pattern
	patternCounts := make(map[string]int)
	for _, entry := range logs.Entries {
		if !entry.IsError {
			continue
		}

		key := extractErrorKey(entry.Message)
		if key != "" {
			patternCounts[key]++
		}
	}

	// Build issues list
	for pattern, count := range patternCounts {
		severity := "warning"
		if count > 10 {
			severity = "critical"
		}

		analysis.IssuesFound = append(analysis.IssuesFound, IdentifiedIssue{
			Type:        pattern,
			Severity:    severity,
			Description: fmt.Sprintf("%s errors detected", pattern),
			Occurrences: count,
		})
	}

	// Generate summary
	if logs.ErrorCount == 0 {
		analysis.Summary = "No errors detected in the logs."
		analysis.Confidence = 0.9
	} else if logs.ErrorCount < 5 {
		analysis.Summary = fmt.Sprintf("Found %d errors across %d pods. Issues appear minor.", logs.ErrorCount, logs.PodCount)
	} else if logs.ErrorCount < 20 {
		analysis.Summary = fmt.Sprintf("Found %d errors across %d pods. Some issues need attention.", logs.ErrorCount, logs.PodCount)
	} else {
		analysis.Summary = fmt.Sprintf("Found %d errors across %d pods. Multiple issues detected that require investigation.", logs.ErrorCount, logs.PodCount)
	}

	// Add basic root cause hints based on patterns
	if patternCounts["OOM"] > 0 {
		analysis.RootCause = "Memory pressure detected. Containers may be exceeding memory limits."
		analysis.Recommendations = append(analysis.Recommendations, Recommendation{
			Priority:    1,
			Action:      "Check memory limits",
			Description: "Review and potentially increase memory limits for affected containers",
			Command:     "kubectl top pods",
			Risk:        "low",
		})
	} else if patternCounts["connection refused"] > 0 || patternCounts["connection reset"] > 0 {
		analysis.RootCause = "Network connectivity issues detected. Services may be unreachable."
		analysis.Recommendations = append(analysis.Recommendations, Recommendation{
			Priority:    1,
			Action:      "Check service endpoints",
			Description: "Verify that dependent services are running and accessible",
			Command:     "kubectl get endpoints",
			Risk:        "low",
		})
	} else if patternCounts["timeout"] > 0 {
		analysis.RootCause = "Timeout errors detected. Services may be slow or overloaded."
		analysis.Recommendations = append(analysis.Recommendations, Recommendation{
			Priority:    1,
			Action:      "Check resource usage",
			Description: "Review CPU and memory usage of affected pods",
			Command:     "kubectl top pods",
			Risk:        "low",
		})
	}

	return analysis
}

// truncateMessage truncates a message to maxLen characters
func truncateMessage(msg string, maxLen int) string {
	if len(msg) <= maxLen {
		return msg
	}
	return msg[:maxLen] + "..."
}

// FormatAnalysisForDisplay formats a LogAnalysis for terminal display
func FormatAnalysisForDisplay(analysis *LogAnalysis) string {
	var sb strings.Builder

	sb.WriteString("=== Log Analysis ===\n\n")

	sb.WriteString("Summary:\n")
	sb.WriteString(analysis.Summary)
	sb.WriteString("\n\n")

	if len(analysis.IssuesFound) > 0 {
		sb.WriteString("Issues Found:\n")
		for i, issue := range analysis.IssuesFound {
			sb.WriteString(fmt.Sprintf("  %d. [%s] %s - %s (%d occurrences)\n",
				i+1, issue.Severity, issue.Type, issue.Description, issue.Occurrences))
		}
		sb.WriteString("\n")
	}

	if analysis.RootCause != "" {
		sb.WriteString("Root Cause:\n")
		sb.WriteString("  " + analysis.RootCause)
		sb.WriteString("\n\n")
	}

	if analysis.ImpactAssessment != "" {
		sb.WriteString("Impact:\n")
		sb.WriteString("  " + analysis.ImpactAssessment)
		sb.WriteString("\n\n")
	}

	if len(analysis.Recommendations) > 0 {
		sb.WriteString("Recommendations:\n")
		for _, rec := range analysis.Recommendations {
			sb.WriteString(fmt.Sprintf("  %d. %s [%s risk]\n", rec.Priority, rec.Action, rec.Risk))
			sb.WriteString(fmt.Sprintf("     %s\n", rec.Description))
			if rec.Command != "" {
				sb.WriteString(fmt.Sprintf("     Command: %s\n", rec.Command))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(fmt.Sprintf("Confidence: %.0f%%\n", analysis.Confidence*100))

	return sb.String()
}
