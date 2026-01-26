package waf

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

// SubAgent handles WAF/Security-related operations
type SubAgent struct {
	client CloudflareClient
	debug  bool
}

// NewSubAgent creates a new WAF sub-agent
func NewSubAgent(client CloudflareClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// HandleQuery processes WAF-related queries
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[waf] handling query: %s\n", query)
	}

	// Analyze the query
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[waf] analysis: readonly=%v, operation=%s, resourceType=%s\n",
			analysis.IsReadOnly, analysis.Operation, analysis.ResourceType)
	}

	// For read-only operations, execute immediately
	if analysis.IsReadOnly {
		return s.executeReadOnly(ctx, query, analysis, opts)
	}

	// For modifications, generate a plan
	plan, err := s.generatePlan(ctx, query, analysis, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	return &Response{
		Type:    ResponseTypePlan,
		Plan:    plan,
		Message: plan.Summary,
	}, nil
}

// analyzeQuery determines the nature of a WAF query
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	queryLower := strings.ToLower(query)
	analysis := QueryAnalysis{}

	// Detect resource type
	switch {
	case strings.Contains(queryLower, "rate limit") || strings.Contains(queryLower, "ratelimit"):
		analysis.ResourceType = "rate-limit"
	case strings.Contains(queryLower, "waf rule") || strings.Contains(queryLower, "managed rule"):
		analysis.ResourceType = "waf-rule"
	case strings.Contains(queryLower, "security level") || strings.Contains(queryLower, "under attack"):
		analysis.ResourceType = "security-level"
	case strings.Contains(queryLower, "waf package") || strings.Contains(queryLower, "ruleset"):
		analysis.ResourceType = "waf-package"
	default:
		analysis.ResourceType = "firewall-rule"
	}

	// Detect operation
	analysis.Operation = s.detectOperation(queryLower)

	// Determine if read-only
	readOnlyOps := []string{"list", "get", "show", "describe", "status", "check"}
	for _, op := range readOnlyOps {
		if analysis.Operation == op {
			analysis.IsReadOnly = true
			break
		}
	}

	// Extract zone name if mentioned
	analysis.ZoneName = s.extractZoneName(query)

	// Extract action if mentioned
	analysis.Action = s.extractAction(queryLower)

	// Extract expression if mentioned
	analysis.Expression = s.extractExpression(query)

	// Extract description if mentioned
	analysis.Description = s.extractDescription(query)

	return analysis
}

// detectOperation determines the operation type from query
func (s *SubAgent) detectOperation(queryLower string) string {
	operations := map[string][]string{
		"list":    {"list", "show", "get all", "what", "which"},
		"get":     {"get", "describe", "details", "info about"},
		"create":  {"create", "add", "new", "set up", "configure"},
		"update":  {"update", "modify", "change", "edit"},
		"delete":  {"delete", "remove", "drop"},
		"enable":  {"enable", "activate", "turn on"},
		"disable": {"disable", "deactivate", "turn off", "pause"},
	}

	for op, keywords := range operations {
		for _, kw := range keywords {
			if strings.Contains(queryLower, kw) {
				return op
			}
		}
	}

	return "list"
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

// extractAction extracts firewall action from query
func (s *SubAgent) extractAction(queryLower string) string {
	actions := []string{"block", "challenge", "js_challenge", "managed_challenge", "allow", "log", "bypass"}
	for _, action := range actions {
		if strings.Contains(queryLower, action) {
			return action
		}
	}
	return ""
}

// extractExpression extracts firewall expression from query
func (s *SubAgent) extractExpression(query string) string {
	// Look for expression patterns
	patterns := []string{
		`expression\s*[=:]\s*["']([^"']+)["']`,
		`filter\s*[=:]\s*["']([^"']+)["']`,
		`rule\s*[=:]\s*["']([^"']+)["']`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractDescription extracts rule description from query
func (s *SubAgent) extractDescription(query string) string {
	patterns := []string{
		`description\s*[=:]\s*["']([^"']+)["']`,
		`named?\s+["']([^"']+)["']`,
		`called\s+["']([^"']+)["']`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// executeReadOnly handles read-only WAF operations
func (s *SubAgent) executeReadOnly(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	zoneID := opts.ZoneID

	// Look up zone ID if we have zone name
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
		return nil, fmt.Errorf("zone is required for WAF queries (specify zone name in query or use --zone-name)")
	}

	switch analysis.ResourceType {
	case "firewall-rule":
		return s.listFirewallRules(ctx, zoneID)
	case "rate-limit":
		return s.listRateLimits(ctx, zoneID)
	case "waf-package":
		return s.listWAFPackages(ctx, zoneID)
	case "security-level":
		return s.getSecurityLevel(ctx, zoneID)
	default:
		return s.listFirewallRules(ctx, zoneID)
	}
}

// listFirewallRules lists firewall rules for a zone
func (s *SubAgent) listFirewallRules(ctx context.Context, zoneID string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/firewall/rules", zoneID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list firewall rules: %w", err)
	}

	formatted := s.formatFirewallRulesResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// listRateLimits lists rate limiting rules for a zone
func (s *SubAgent) listRateLimits(ctx context.Context, zoneID string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/rate_limits", zoneID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list rate limits: %w", err)
	}

	formatted := s.formatRateLimitsResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// listWAFPackages lists WAF packages for a zone
func (s *SubAgent) listWAFPackages(ctx context.Context, zoneID string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/firewall/waf/packages", zoneID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list WAF packages: %w", err)
	}

	formatted := s.formatWAFPackagesResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// getSecurityLevel gets the security level for a zone
func (s *SubAgent) getSecurityLevel(ctx context.Context, zoneID string) (*Response, error) {
	endpoint := fmt.Sprintf("/zones/%s/settings/security_level", zoneID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get security level: %w", err)
	}

	formatted := s.formatSecurityLevelResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
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

// generatePlan creates a plan for WAF modifications
func (s *SubAgent) generatePlan(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Plan, error) {
	zoneID := opts.ZoneID

	if zoneID == "" && analysis.ZoneName != "" {
		var err error
		zoneID, err = s.getZoneIDByName(ctx, analysis.ZoneName)
		if err != nil {
			return nil, err
		}
	}

	if zoneID == "" {
		return nil, fmt.Errorf("zone is required for WAF modifications")
	}

	switch analysis.ResourceType {
	case "firewall-rule":
		return s.generateFirewallRulePlan(analysis, zoneID)
	case "rate-limit":
		return s.generateRateLimitPlan(analysis, zoneID)
	case "security-level":
		return s.generateSecurityLevelPlan(analysis, zoneID)
	default:
		return nil, fmt.Errorf("unsupported WAF resource type: %s", analysis.ResourceType)
	}
}

// generateFirewallRulePlan creates a plan for firewall rule operations
func (s *SubAgent) generateFirewallRulePlan(analysis QueryAnalysis, zoneID string) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		if analysis.Expression == "" {
			return nil, fmt.Errorf("expression is required to create a firewall rule")
		}
		action := analysis.Action
		if action == "" {
			action = "block"
		}
		description := analysis.Description
		if description == "" {
			description = "Created via clanker"
		}

		// First create a filter
		filterBody := fmt.Sprintf(`{"expression":"%s","description":"%s"}`, analysis.Expression, description)
		plan.Commands = append(plan.Commands, Command{
			Method:   "POST",
			Endpoint: fmt.Sprintf("/zones/%s/filters", zoneID),
			Body:     filterBody,
			Reason:   "Create filter for firewall rule",
		})

		// Then create the rule referencing the filter
		ruleBody := fmt.Sprintf(`{"filter":{"id":"$FILTER_ID"},"action":"%s","description":"%s"}`, action, description)
		plan.Commands = append(plan.Commands, Command{
			Method:   "POST",
			Endpoint: fmt.Sprintf("/zones/%s/firewall/rules", zoneID),
			Body:     ruleBody,
			Reason:   fmt.Sprintf("Create firewall rule with action '%s'", action),
		})

		plan.Summary = fmt.Sprintf("Create firewall rule: %s (action: %s)", description, action)

	case "delete":
		if analysis.RuleID == "" {
			return nil, fmt.Errorf("rule ID is required to delete a firewall rule")
		}
		plan.Commands = append(plan.Commands, Command{
			Method:   "DELETE",
			Endpoint: fmt.Sprintf("/zones/%s/firewall/rules/%s", zoneID, analysis.RuleID),
			Reason:   "Delete firewall rule",
		})
		plan.Summary = fmt.Sprintf("Delete firewall rule: %s", analysis.RuleID)

	case "enable", "disable":
		if analysis.RuleID == "" {
			return nil, fmt.Errorf("rule ID is required to enable/disable a firewall rule")
		}
		paused := analysis.Operation == "disable"
		body := fmt.Sprintf(`{"paused":%t}`, paused)
		plan.Commands = append(plan.Commands, Command{
			Method:   "PATCH",
			Endpoint: fmt.Sprintf("/zones/%s/firewall/rules/%s", zoneID, analysis.RuleID),
			Body:     body,
			Reason:   fmt.Sprintf("%s firewall rule", analysis.Operation),
		})
		plan.Summary = fmt.Sprintf("%s firewall rule: %s", strings.Title(analysis.Operation), analysis.RuleID)

	default:
		return nil, fmt.Errorf("unsupported firewall rule operation: %s", analysis.Operation)
	}

	return plan, nil
}

// generateRateLimitPlan creates a plan for rate limit operations
func (s *SubAgent) generateRateLimitPlan(analysis QueryAnalysis, zoneID string) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		// Default rate limit settings
		body := `{
			"threshold": 100,
			"period": 60,
			"action": {"mode": "challenge", "timeout": 3600},
			"match": {"request": {"url_pattern": "*"}}
		}`
		plan.Commands = append(plan.Commands, Command{
			Method:   "POST",
			Endpoint: fmt.Sprintf("/zones/%s/rate_limits", zoneID),
			Body:     body,
			Reason:   "Create rate limiting rule",
		})
		plan.Summary = "Create rate limiting rule"

	case "delete":
		if analysis.RuleID == "" {
			return nil, fmt.Errorf("rule ID is required to delete a rate limit")
		}
		plan.Commands = append(plan.Commands, Command{
			Method:   "DELETE",
			Endpoint: fmt.Sprintf("/zones/%s/rate_limits/%s", zoneID, analysis.RuleID),
			Reason:   "Delete rate limiting rule",
		})
		plan.Summary = fmt.Sprintf("Delete rate limiting rule: %s", analysis.RuleID)

	default:
		return nil, fmt.Errorf("unsupported rate limit operation: %s", analysis.Operation)
	}

	return plan, nil
}

// generateSecurityLevelPlan creates a plan for security level changes
func (s *SubAgent) generateSecurityLevelPlan(analysis QueryAnalysis, zoneID string) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	// Determine security level
	level := "medium"
	queryLower := strings.ToLower(analysis.Description)
	if strings.Contains(queryLower, "under attack") || strings.Contains(queryLower, "high") {
		level = "under_attack"
	} else if strings.Contains(queryLower, "low") {
		level = "low"
	} else if strings.Contains(queryLower, "off") {
		level = "essentially_off"
	}

	body := fmt.Sprintf(`{"value":"%s"}`, level)
	plan.Commands = append(plan.Commands, Command{
		Method:   "PATCH",
		Endpoint: fmt.Sprintf("/zones/%s/settings/security_level", zoneID),
		Body:     body,
		Reason:   fmt.Sprintf("Set security level to '%s'", level),
	})
	plan.Summary = fmt.Sprintf("Set security level to: %s", level)

	return plan, nil
}

// Format response helpers

func (s *SubAgent) formatFirewallRulesResponse(result string) string {
	var response struct {
		Success bool           `json:"success"`
		Result  []FirewallRule `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No firewall rules found."
	}

	var sb strings.Builder
	sb.WriteString("Firewall Rules:\n\n")

	for _, rule := range response.Result {
		status := "Active"
		if rule.Paused {
			status = "Paused"
		}
		sb.WriteString(fmt.Sprintf("  %s\n", rule.Description))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", rule.ID))
		sb.WriteString(fmt.Sprintf("    Action: %s\n", rule.Action))
		sb.WriteString(fmt.Sprintf("    Priority: %d\n", rule.Priority))
		sb.WriteString(fmt.Sprintf("    Status: %s\n", status))
		if rule.Filter.Expression != "" {
			sb.WriteString(fmt.Sprintf("    Expression: %s\n", rule.Filter.Expression))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatRateLimitsResponse(result string) string {
	var response struct {
		Success bool            `json:"success"`
		Result  []RateLimitRule `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No rate limiting rules found."
	}

	var sb strings.Builder
	sb.WriteString("Rate Limiting Rules:\n\n")

	for _, rule := range response.Result {
		status := "Active"
		if rule.Disabled {
			status = "Disabled"
		}
		sb.WriteString(fmt.Sprintf("  %s\n", rule.Description))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", rule.ID))
		sb.WriteString(fmt.Sprintf("    Threshold: %d requests per %d seconds\n", rule.Threshold, rule.Period))
		sb.WriteString(fmt.Sprintf("    Action: %s\n", rule.Action.Mode))
		sb.WriteString(fmt.Sprintf("    Status: %s\n", status))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatWAFPackagesResponse(result string) string {
	var response struct {
		Success bool         `json:"success"`
		Result  []WAFPackage `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No WAF packages found."
	}

	var sb strings.Builder
	sb.WriteString("WAF Packages:\n\n")

	for _, pkg := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", pkg.Name))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", pkg.ID))
		sb.WriteString(fmt.Sprintf("    Description: %s\n", pkg.Description))
		sb.WriteString(fmt.Sprintf("    Detection Mode: %s\n", pkg.DetectionMode))
		sb.WriteString(fmt.Sprintf("    Sensitivity: %s\n", pkg.Sensitivity))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatSecurityLevelResponse(result string) string {
	var response struct {
		Success bool          `json:"success"`
		Result  SecurityLevel `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success {
		return "Failed to get security level."
	}

	var sb strings.Builder
	sb.WriteString("Security Level:\n\n")
	sb.WriteString(fmt.Sprintf("  Current Level: %s\n", response.Result.Value))
	sb.WriteString(fmt.Sprintf("  Editable: %t\n", response.Result.Editable))
	if response.Result.ModifiedOn != "" {
		sb.WriteString(fmt.Sprintf("  Last Modified: %s\n", response.Result.ModifiedOn))
	}

	return sb.String()
}
