package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// CloudflareClient defines the interface for Cloudflare API operations
type CloudflareClient interface {
	RunAPI(method, endpoint, body string) (string, error)
	RunAPIWithContext(ctx context.Context, method, endpoint, body string) (string, error)
	GetAccountID() string
}

// SubAgent handles analytics-related operations
type SubAgent struct {
	client CloudflareClient
	debug  bool
}

// NewSubAgent creates a new Analytics sub-agent
func NewSubAgent(client CloudflareClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// HandleQuery processes analytics-related queries
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[analytics] handling query: %s\n", query)
	}

	// Analyze the query
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[analytics] analysis: resourceType=%s, timePeriod=%s\n",
			analysis.ResourceType, analysis.TimePeriod)
	}

	// Get zone ID
	zoneID := opts.ZoneID
	if zoneID == "" && analysis.ZoneName != "" {
		var err error
		zoneID, err = s.getZoneIDByName(ctx, analysis.ZoneName)
		if err != nil {
			return nil, err
		}
	}
	if zoneID == "" && opts.ZoneName != "" {
		var err error
		zoneID, err = s.getZoneIDByName(ctx, opts.ZoneName)
		if err != nil {
			return nil, err
		}
	}

	if zoneID == "" {
		return nil, fmt.Errorf("zone is required for analytics queries (specify zone name in query)")
	}

	// Get analytics based on type
	switch analysis.ResourceType {
	case "security":
		return s.getSecurityAnalytics(ctx, zoneID, analysis.TimePeriod)
	case "performance":
		return s.getPerformanceAnalytics(ctx, zoneID, analysis.TimePeriod)
	default:
		return s.getTrafficAnalytics(ctx, zoneID, analysis.TimePeriod)
	}
}

// analyzeQuery determines the nature of an analytics query
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	queryLower := strings.ToLower(query)
	analysis := QueryAnalysis{
		ResourceType: "traffic",
		TimePeriod:   "-1440", // Default: last 24 hours
	}

	// Detect resource type
	if strings.Contains(queryLower, "security") || strings.Contains(queryLower, "threat") || strings.Contains(queryLower, "attack") {
		analysis.ResourceType = "security"
	} else if strings.Contains(queryLower, "performance") || strings.Contains(queryLower, "speed") || strings.Contains(queryLower, "latency") {
		analysis.ResourceType = "performance"
	}

	// Detect time period
	if strings.Contains(queryLower, "week") || strings.Contains(queryLower, "7 day") || strings.Contains(queryLower, "7d") {
		analysis.TimePeriod = "-10080" // 7 days in minutes
	} else if strings.Contains(queryLower, "month") || strings.Contains(queryLower, "30 day") || strings.Contains(queryLower, "30d") {
		analysis.TimePeriod = "-43200" // 30 days in minutes
	} else if strings.Contains(queryLower, "hour") {
		analysis.TimePeriod = "-60" // 1 hour in minutes
	}

	// Extract zone name
	analysis.ZoneName = s.extractZoneName(query)

	return analysis
}

// extractZoneName extracts zone/domain name from query
func (s *SubAgent) extractZoneName(query string) string {
	domainRegex := regexp.MustCompile(`\b([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b`)
	matches := domainRegex.FindAllString(query, -1)
	if len(matches) > 0 {
		for _, match := range matches {
			parts := strings.Split(match, ".")
			if len(parts) == 2 || (len(parts) == 3 && len(parts[1]) <= 3) {
				return match
			}
		}
		return matches[0]
	}
	return ""
}

// getZoneIDByName looks up zone ID from zone name
func (s *SubAgent) getZoneIDByName(ctx context.Context, zoneName string) (string, error) {
	endpoint := fmt.Sprintf("/zones?name=%s", zoneName)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return "", fmt.Errorf("failed to look up zone: %w", err)
	}

	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return "", fmt.Errorf("failed to parse zone response: %w", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "", fmt.Errorf("zone not found: %s", zoneName)
	}

	return response.Result[0].ID, nil
}

// getTrafficAnalytics gets traffic analytics for a zone
func (s *SubAgent) getTrafficAnalytics(ctx context.Context, zoneID, timePeriod string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/analytics/dashboard?since=%s&continuous=true", zoneID, timePeriod)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get traffic analytics: %w", err)
	}

	formatted := s.formatTrafficAnalytics(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// getSecurityAnalytics gets security analytics for a zone
func (s *SubAgent) getSecurityAnalytics(ctx context.Context, zoneID, timePeriod string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/analytics/dashboard?since=%s&continuous=true", zoneID, timePeriod)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get security analytics: %w", err)
	}

	formatted := s.formatSecurityAnalytics(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// getPerformanceAnalytics gets performance analytics for a zone
func (s *SubAgent) getPerformanceAnalytics(ctx context.Context, zoneID, timePeriod string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/analytics/dashboard?since=%s&continuous=true", zoneID, timePeriod)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get performance analytics: %w", err)
	}

	formatted := s.formatPerformanceAnalytics(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// formatTrafficAnalytics formats traffic analytics response
func (s *SubAgent) formatTrafficAnalytics(result string) string {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Totals struct {
				Requests struct {
					All      int64 `json:"all"`
					Cached   int64 `json:"cached"`
					Uncached int64 `json:"uncached"`
				} `json:"requests"`
				Bandwidth struct {
					All      int64 `json:"all"`
					Cached   int64 `json:"cached"`
					Uncached int64 `json:"uncached"`
				} `json:"bandwidth"`
				Pageviews struct {
					All int64 `json:"all"`
				} `json:"pageviews"`
				Uniques struct {
					All int64 `json:"all"`
				} `json:"uniques"`
			} `json:"totals"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success {
		return "Failed to get traffic analytics."
	}

	totals := response.Result.Totals
	var sb strings.Builder
	sb.WriteString("Traffic Analytics:\n\n")
	sb.WriteString("  Requests:\n")
	sb.WriteString(fmt.Sprintf("    Total: %s\n", formatNumber(totals.Requests.All)))
	sb.WriteString(fmt.Sprintf("    Cached: %s (%.1f%%)\n", formatNumber(totals.Requests.Cached), percentage(totals.Requests.Cached, totals.Requests.All)))
	sb.WriteString(fmt.Sprintf("    Uncached: %s (%.1f%%)\n", formatNumber(totals.Requests.Uncached), percentage(totals.Requests.Uncached, totals.Requests.All)))
	sb.WriteString("\n  Bandwidth:\n")
	sb.WriteString(fmt.Sprintf("    Total: %s\n", formatBytes(totals.Bandwidth.All)))
	sb.WriteString(fmt.Sprintf("    Cached: %s (%.1f%%)\n", formatBytes(totals.Bandwidth.Cached), percentage(totals.Bandwidth.Cached, totals.Bandwidth.All)))
	sb.WriteString(fmt.Sprintf("    Uncached: %s (%.1f%%)\n", formatBytes(totals.Bandwidth.Uncached), percentage(totals.Bandwidth.Uncached, totals.Bandwidth.All)))
	sb.WriteString("\n  Visitors:\n")
	sb.WriteString(fmt.Sprintf("    Page Views: %s\n", formatNumber(totals.Pageviews.All)))
	sb.WriteString(fmt.Sprintf("    Unique Visitors: %s\n", formatNumber(totals.Uniques.All)))

	return sb.String()
}

// formatSecurityAnalytics formats security analytics response
func (s *SubAgent) formatSecurityAnalytics(result string) string {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Totals struct {
				Threats struct {
					All int64 `json:"all"`
				} `json:"threats"`
				Requests struct {
					All int64 `json:"all"`
					SSL struct {
						Encrypted   int64 `json:"encrypted"`
						Unencrypted int64 `json:"unencrypted"`
					} `json:"ssl"`
				} `json:"requests"`
			} `json:"totals"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success {
		return "Failed to get security analytics."
	}

	totals := response.Result.Totals
	var sb strings.Builder
	sb.WriteString("Security Analytics:\n\n")
	sb.WriteString("  Threats:\n")
	sb.WriteString(fmt.Sprintf("    Total Blocked: %s\n", formatNumber(totals.Threats.All)))
	sb.WriteString("\n  SSL/TLS:\n")
	sb.WriteString(fmt.Sprintf("    Encrypted Requests: %s (%.1f%%)\n", formatNumber(totals.Requests.SSL.Encrypted), percentage(totals.Requests.SSL.Encrypted, totals.Requests.All)))
	sb.WriteString(fmt.Sprintf("    Unencrypted Requests: %s (%.1f%%)\n", formatNumber(totals.Requests.SSL.Unencrypted), percentage(totals.Requests.SSL.Unencrypted, totals.Requests.All)))

	return sb.String()
}

// formatPerformanceAnalytics formats performance analytics response
func (s *SubAgent) formatPerformanceAnalytics(result string) string {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Totals struct {
				Requests struct {
					All      int64 `json:"all"`
					Cached   int64 `json:"cached"`
					Uncached int64 `json:"uncached"`
				} `json:"requests"`
				Bandwidth struct {
					All      int64 `json:"all"`
					Cached   int64 `json:"cached"`
					Uncached int64 `json:"uncached"`
				} `json:"bandwidth"`
			} `json:"totals"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success {
		return "Failed to get performance analytics."
	}

	totals := response.Result.Totals
	var sb strings.Builder
	sb.WriteString("Performance Analytics:\n\n")
	sb.WriteString("  Cache Performance:\n")
	sb.WriteString(fmt.Sprintf("    Cache Hit Rate: %.1f%%\n", percentage(totals.Requests.Cached, totals.Requests.All)))
	sb.WriteString(fmt.Sprintf("    Bandwidth Saved: %s (%.1f%%)\n", formatBytes(totals.Bandwidth.Cached), percentage(totals.Bandwidth.Cached, totals.Bandwidth.All)))
	sb.WriteString("\n  Request Summary:\n")
	sb.WriteString(fmt.Sprintf("    Total Requests: %s\n", formatNumber(totals.Requests.All)))
	sb.WriteString(fmt.Sprintf("    Served from Cache: %s\n", formatNumber(totals.Requests.Cached)))
	sb.WriteString(fmt.Sprintf("    Served from Origin: %s\n", formatNumber(totals.Requests.Uncached)))

	return sb.String()
}

// Helper functions

func formatNumber(n int64) string {
	if n >= 1000000000 {
		return fmt.Sprintf("%.1fB", float64(n)/1000000000)
	}
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func formatBytes(b int64) string {
	if b >= 1099511627776 {
		return fmt.Sprintf("%.1f TB", float64(b)/1099511627776)
	}
	if b >= 1073741824 {
		return fmt.Sprintf("%.1f GB", float64(b)/1073741824)
	}
	if b >= 1048576 {
		return fmt.Sprintf("%.1f MB", float64(b)/1048576)
	}
	if b >= 1024 {
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	}
	return fmt.Sprintf("%d B", b)
}

func percentage(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
