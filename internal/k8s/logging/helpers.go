package logging

import (
	"regexp"
	"strings"
	"time"
)

// containsAny checks if string contains any of the substrings
func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// extractNamespace extracts namespace from a natural language query
func extractNamespace(query string) string {
	patterns := []string{
		`namespace\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`-n\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`in\s+ns\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`in\s+([a-z0-9][a-z0-9-]*[a-z0-9])\s+namespace`,
		`from\s+namespace\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`from\s+ns\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	// Check for common namespaces mentioned directly
	commonNamespaces := []string{"kube-system", "kube-public", "default", "monitoring", "ingress-nginx"}
	for _, ns := range commonNamespaces {
		if strings.Contains(query, ns) {
			return ns
		}
	}

	return ""
}

// extractResourceName extracts a resource name from the query based on resource type
func extractResourceName(query, resourceType string) string {
	patterns := []string{
		resourceType + `\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		resourceType + `\s+named\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		resourceType + `\s+called\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`from\s+` + resourceType + `\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`for\s+` + resourceType + `\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			name := matches[1]
			if !isCommonWord(name) {
				return name
			}
		}
	}

	return ""
}

// extractNodeName extracts node name from a query
func extractNodeName(query string) string {
	patterns := []string{
		`node\s+([a-z0-9][a-z0-9.-]*[a-z0-9])`,
		`node\s+named\s+([a-z0-9][a-z0-9.-]*[a-z0-9])`,
		`on\s+node\s+([a-z0-9][a-z0-9.-]*[a-z0-9])`,
		`from\s+node\s+([a-z0-9][a-z0-9.-]*[a-z0-9])`,
		`worker\s+([a-z0-9][a-z0-9.-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			name := matches[1]
			if !isCommonWord(name) {
				return name
			}
		}
	}

	return ""
}

// extractPodName extracts pod name from a query
func extractPodName(query string) string {
	patterns := []string{
		`pod\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`pod\s+named\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`from\s+pod\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`for\s+pod\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			name := matches[1]
			if !isCommonWord(name) {
				return name
			}
		}
	}

	return ""
}

// extractErrorPatterns extracts error patterns mentioned in the query
func extractErrorPatterns(query string) []string {
	var patterns []string

	// HTTP status codes
	httpCodes := regexp.MustCompile(`\b([45]\d{2})\b`)
	if matches := httpCodes.FindAllString(query, -1); len(matches) > 0 {
		patterns = append(patterns, matches...)
	}

	// Common error keywords
	errorKeywords := []string{
		"timeout", "connection refused", "connection reset",
		"oom", "out of memory", "killed",
		"crash", "panic", "fatal",
		"failed", "failure", "error",
		"unauthorized", "forbidden", "denied",
		"not found", "404",
		"internal server error", "502", "503", "504",
	}

	q := strings.ToLower(query)
	for _, keyword := range errorKeywords {
		if strings.Contains(q, keyword) {
			patterns = append(patterns, keyword)
		}
	}

	return patterns
}

// extractTimeConstraint extracts time constraints from the query
func extractTimeConstraint(query string) string {
	q := strings.ToLower(query)

	// Specific duration patterns
	durationPatterns := []struct {
		pattern string
		result  string
	}{
		{`last\s+(\d+)\s*h(our)?s?`, ""},    // Will be computed
		{`last\s+(\d+)\s*m(in(ute)?)?s?`, ""}, // Will be computed
		{`past\s+(\d+)\s*h(our)?s?`, ""},
		{`past\s+(\d+)\s*m(in(ute)?)?s?`, ""},
		{`since\s+(\d+)\s*h(our)?s?\s+ago`, ""},
		{`since\s+(\d+)\s*m(in(ute)?)?s?\s+ago`, ""},
	}

	for _, dp := range durationPatterns {
		re := regexp.MustCompile(dp.pattern)
		if matches := re.FindStringSubmatch(q); len(matches) > 1 {
			num := matches[1]
			if strings.Contains(dp.pattern, "h(our)") {
				return num + "h"
			}
			return num + "m"
		}
	}

	// Common time phrases
	if containsAny(q, []string{"last hour", "past hour"}) {
		return "1h"
	}
	if containsAny(q, []string{"last 30 minutes", "past 30 minutes", "last half hour"}) {
		return "30m"
	}
	if containsAny(q, []string{"last 15 minutes", "past 15 minutes"}) {
		return "15m"
	}
	if containsAny(q, []string{"last 5 minutes", "past 5 minutes"}) {
		return "5m"
	}
	if containsAny(q, []string{"today", "last day", "past day", "last 24 hours"}) {
		return "24h"
	}
	if containsAny(q, []string{"recently", "recent"}) {
		return "30m"
	}

	return ""
}

// extractJSON extracts JSON block from a string response
func extractJSON(response string) string {
	// Try to find JSON object
	start := strings.Index(response, "{")
	if start == -1 {
		return ""
	}

	// Find matching closing brace
	depth := 0
	for i := start; i < len(response); i++ {
		switch response[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return response[start : i+1]
			}
		}
	}

	return ""
}

// filterByPatterns filters log entries by patterns
func filterByPatterns(logs *AggregatedLogs, patterns []string) *AggregatedLogs {
	if len(patterns) == 0 {
		return logs
	}

	var filtered []LogEntry
	for _, entry := range logs.Entries {
		msgLower := strings.ToLower(entry.Message)
		for _, pattern := range patterns {
			if strings.Contains(msgLower, strings.ToLower(pattern)) {
				entry.Pattern = pattern
				filtered = append(filtered, entry)
				break
			}
		}
	}

	// Rebuild counts
	errorCount := 0
	warnCount := 0
	for _, entry := range filtered {
		if entry.IsError {
			errorCount++
		}
		if entry.Level == LevelWarn {
			warnCount++
		}
	}

	return &AggregatedLogs{
		Source:     logs.Source,
		Scope:      logs.Scope,
		TotalLines: len(filtered),
		PodCount:   logs.PodCount,
		TimeRange:  logs.TimeRange,
		Entries:    filtered,
		ErrorCount: errorCount,
		WarnCount:  warnCount,
	}
}

// filterByLevel filters log entries by log level
func filterByLevel(logs *AggregatedLogs, levels []LogLevel) *AggregatedLogs {
	if len(levels) == 0 {
		return logs
	}

	levelSet := make(map[LogLevel]bool)
	for _, level := range levels {
		levelSet[level] = true
	}

	var filtered []LogEntry
	for _, entry := range logs.Entries {
		if levelSet[entry.Level] {
			filtered = append(filtered, entry)
		}
	}

	errorCount := 0
	warnCount := 0
	for _, entry := range filtered {
		if entry.IsError {
			errorCount++
		}
		if entry.Level == LevelWarn {
			warnCount++
		}
	}

	return &AggregatedLogs{
		Source:     logs.Source,
		Scope:      logs.Scope,
		TotalLines: len(filtered),
		PodCount:   logs.PodCount,
		TimeRange:  logs.TimeRange,
		Entries:    filtered,
		ErrorCount: errorCount,
		WarnCount:  warnCount,
	}
}

// isCommonWord checks if a word is a common word that should not be treated as a resource name
func isCommonWord(word string) bool {
	commonWords := map[string]bool{
		"the": true, "all": true, "any": true, "some": true,
		"this": true, "that": true, "these": true, "those": true,
		"my": true, "your": true, "our": true, "their": true,
		"cluster": true, "namespace": true, "resource": true,
		"status": true, "health": true, "logs": true, "events": true,
		"issues": true, "problems": true, "errors": true,
		"show": true, "get": true, "list": true, "find": true,
		"from": true, "in": true, "on": true, "for": true,
		"with": true, "about": true, "what": true, "why": true,
	}
	return commonWords[word]
}

// parseTimestamp attempts to parse a timestamp from a log line
func parseTimestamp(line string) time.Time {
	// Common timestamp formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",
		"Jan 02 15:04:05",
		"Jan  2 15:04:05",
	}

	// Try to extract timestamp from beginning of line
	for _, format := range formats {
		// Estimate the length based on format
		if len(line) >= len(format) {
			candidate := line[:min(len(line), len(format)+10)]
			if t, err := time.Parse(format, strings.TrimSpace(candidate)); err == nil {
				return t
			}
		}
	}

	// Try regex patterns
	isoPattern := regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?)`)
	if matches := isoPattern.FindStringSubmatch(line); len(matches) > 1 {
		if t, err := time.Parse(time.RFC3339Nano, matches[1]); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, matches[1]); err == nil {
			return t
		}
	}

	return time.Now()
}

// detectLogLevel detects the log level from a log line
func detectLogLevel(line string) LogLevel {
	lineLower := strings.ToLower(line)

	if containsAny(lineLower, []string{"error", "fatal", "panic", "exception", "fail", "critical"}) {
		return LevelError
	}
	if containsAny(lineLower, []string{"warn", "warning"}) {
		return LevelWarn
	}
	if containsAny(lineLower, []string{"debug", "trace"}) {
		return LevelDebug
	}

	return LevelInfo
}

// buildLogSummary creates a summary from aggregated logs
func buildLogSummary(logs *AggregatedLogs) *LogSummary {
	summary := &LogSummary{
		TotalLines: logs.TotalLines,
		ErrorCount: logs.ErrorCount,
		WarnCount:  logs.WarnCount,
		PodCount:   logs.PodCount,
		TimeRange:  logs.TimeRange,
		TopErrors:  []ErrorPattern{},
	}

	// Count error patterns
	patternCounts := make(map[string]*ErrorPattern)
	for _, entry := range logs.Entries {
		if !entry.IsError {
			continue
		}

		// Extract a key part of the error message
		key := extractErrorKey(entry.Message)
		if key == "" {
			key = "unknown error"
		}

		if ep, ok := patternCounts[key]; ok {
			ep.Count++
			if entry.Timestamp.Before(ep.FirstSeen) {
				ep.FirstSeen = entry.Timestamp
			}
			if entry.Timestamp.After(ep.LastSeen) {
				ep.LastSeen = entry.Timestamp
			}
			if len(ep.SampleLines) < 3 {
				ep.SampleLines = append(ep.SampleLines, entry.Message)
			}
		} else {
			patternCounts[key] = &ErrorPattern{
				Pattern:     key,
				Count:       1,
				FirstSeen:   entry.Timestamp,
				LastSeen:    entry.Timestamp,
				SampleLines: []string{entry.Message},
			}
		}
	}

	// Convert to slice and sort by count
	for _, ep := range patternCounts {
		summary.TopErrors = append(summary.TopErrors, *ep)
	}

	// Sort by count descending (simple bubble sort for small lists)
	for i := 0; i < len(summary.TopErrors)-1; i++ {
		for j := 0; j < len(summary.TopErrors)-i-1; j++ {
			if summary.TopErrors[j].Count < summary.TopErrors[j+1].Count {
				summary.TopErrors[j], summary.TopErrors[j+1] = summary.TopErrors[j+1], summary.TopErrors[j]
			}
		}
	}

	// Keep only top 10
	if len(summary.TopErrors) > 10 {
		summary.TopErrors = summary.TopErrors[:10]
	}

	return summary
}

// extractErrorKey extracts a key identifier from an error message
func extractErrorKey(message string) string {
	// Try to extract HTTP status codes
	httpPattern := regexp.MustCompile(`\b([45]\d{2})\b`)
	if matches := httpPattern.FindStringSubmatch(message); len(matches) > 1 {
		return "HTTP " + matches[1]
	}

	// Common error patterns
	patterns := []struct {
		regex string
		key   string
	}{
		{`connection refused`, "connection refused"},
		{`connection reset`, "connection reset"},
		{`timeout`, "timeout"},
		{`out of memory`, "OOM"},
		{`oom`, "OOM"},
		{`killed`, "killed"},
		{`crash`, "crash"},
		{`panic`, "panic"},
		{`fatal`, "fatal"},
		{`permission denied`, "permission denied"},
		{`access denied`, "access denied"},
		{`unauthorized`, "unauthorized"},
		{`not found`, "not found"},
	}

	msgLower := strings.ToLower(message)
	for _, p := range patterns {
		if strings.Contains(msgLower, p.regex) {
			return p.key
		}
	}

	return ""
}

// formatLogsForDisplay formats logs for terminal display
func formatLogsForDisplay(logs *AggregatedLogs, showTimestamps bool) string {
	var sb strings.Builder

	for _, entry := range logs.Entries {
		if showTimestamps {
			sb.WriteString(entry.Timestamp.Format("15:04:05"))
			sb.WriteString(" ")
		}

		// Add pod info for multi-pod logs
		if logs.PodCount > 1 {
			sb.WriteString("[")
			sb.WriteString(entry.Pod)
			sb.WriteString("] ")
		}

		sb.WriteString(entry.Message)
		sb.WriteString("\n")
	}

	return sb.String()
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
