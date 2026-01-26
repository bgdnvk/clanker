package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CloudflareClient defines the interface for Cloudflare API operations
type CloudflareClient interface {
	RunAPI(method, endpoint, body string) (string, error)
	RunAPIWithContext(ctx context.Context, method, endpoint, body string) (string, error)
	GetAccountID() string
}

// SubAgent handles DNS-related operations
type SubAgent struct {
	client CloudflareClient
	debug  bool
}

// NewSubAgent creates a new DNS sub-agent
func NewSubAgent(client CloudflareClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// HandleQuery processes DNS-related queries
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[dns] handling query: %s\n", query)
	}

	// Analyze the query
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[dns] analysis: readonly=%v, operation=%s, resourceType=%s\n",
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

// analyzeQuery determines the nature of a DNS query
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	queryLower := strings.ToLower(query)
	analysis := QueryAnalysis{}

	// Detect resource type
	if strings.Contains(queryLower, "zone") && !strings.Contains(queryLower, "record") {
		analysis.ResourceType = "zone"
	} else {
		analysis.ResourceType = "record"
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

	// Extract record type if mentioned
	analysis.RecordType = s.extractRecordType(queryLower)

	// Extract record name/value if mentioned
	analysis.RecordName = s.extractRecordName(query)
	analysis.RecordValue = s.extractRecordValue(query)

	// Extract TTL if mentioned
	analysis.TTL = s.extractTTL(query)

	// Extract proxied setting if mentioned
	analysis.Proxied = s.extractProxied(queryLower)

	return analysis
}

// detectOperation determines the operation type from the query
func (s *SubAgent) detectOperation(queryLower string) string {
	// Order matters - check more specific patterns first
	if strings.Contains(queryLower, "delete") || strings.Contains(queryLower, "remove") {
		return "delete"
	}
	if strings.Contains(queryLower, "update") || strings.Contains(queryLower, "change") || strings.Contains(queryLower, "modify") {
		return "update"
	}
	if strings.Contains(queryLower, "create") || strings.Contains(queryLower, "add") || strings.Contains(queryLower, "new") {
		return "create"
	}
	if strings.Contains(queryLower, "get") || strings.Contains(queryLower, "show") || strings.Contains(queryLower, "describe") {
		return "get"
	}
	// Default to list
	return "list"
}

// extractZoneName extracts domain/zone name from query
func (s *SubAgent) extractZoneName(query string) string {
	// Look for domain patterns like example.com, sub.example.co.uk
	domainRegex := regexp.MustCompile(`\b([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b`)
	matches := domainRegex.FindAllString(query, -1)
	if len(matches) > 0 {
		// Return the first match that looks like a root domain (fewest dots that is still valid)
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

// extractRecordType extracts DNS record type from query
func (s *SubAgent) extractRecordType(queryLower string) string {
	recordTypes := map[string]string{
		" a ": "A", " a,": "A", " a.": "A",
		"aaaa": "AAAA",
		"cname": "CNAME",
		"mx": "MX",
		"txt": "TXT",
		"ns": "NS",
		"srv": "SRV",
		"caa": "CAA",
		"ptr": "PTR",
	}

	for pattern, recType := range recordTypes {
		if strings.Contains(queryLower, pattern) {
			return recType
		}
	}

	// Check for "A record" pattern
	if strings.Contains(queryLower, "a record") {
		return "A"
	}

	return ""
}

// extractRecordName extracts the record name (subdomain) from query
func (s *SubAgent) extractRecordName(query string) string {
	// Look for patterns like "for api.example.com" or "api subdomain"
	patterns := []string{
		`for\s+([a-zA-Z0-9.-]+\.[a-zA-Z]{2,})`,
		`subdomain\s+([a-zA-Z0-9-]+)`,
		`record\s+([a-zA-Z0-9.-]+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractRecordValue extracts the record value (IP, target, etc.) from query
func (s *SubAgent) extractRecordValue(query string) string {
	// Look for IP addresses
	ipRegex := regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	if matches := ipRegex.FindStringSubmatch(query); len(matches) > 1 {
		return matches[1]
	}

	// Look for "pointing to X" or "to X" patterns
	toPatterns := []string{
		`pointing\s+to\s+([a-zA-Z0-9.-]+)`,
		`point\s+to\s+([a-zA-Z0-9.-]+)`,
		`to\s+([a-zA-Z0-9.-]+\.[a-zA-Z]{2,})`,
	}

	for _, pattern := range toPatterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractTTL extracts TTL value from query
func (s *SubAgent) extractTTL(query string) int {
	// Look for "ttl X" or "X seconds" patterns
	patterns := []string{
		`ttl\s*[:=]?\s*(\d+)`,
		`(\d+)\s*seconds?`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
			if ttl, err := strconv.Atoi(matches[1]); err == nil {
				return ttl
			}
		}
	}

	return 0 // 0 means use default (auto)
}

// extractProxied extracts proxied setting from query
func (s *SubAgent) extractProxied(queryLower string) *bool {
	if strings.Contains(queryLower, "proxied") || strings.Contains(queryLower, "proxy on") || strings.Contains(queryLower, "orange cloud") {
		proxied := true
		return &proxied
	}
	if strings.Contains(queryLower, "not proxied") || strings.Contains(queryLower, "dns only") || strings.Contains(queryLower, "proxy off") || strings.Contains(queryLower, "grey cloud") {
		proxied := false
		return &proxied
	}
	return nil
}

// executeReadOnly executes read-only DNS operations
func (s *SubAgent) executeReadOnly(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case "zone":
		return s.listZones(ctx, opts)
	case "record":
		return s.listRecords(ctx, analysis, opts)
	default:
		return s.listZones(ctx, opts)
	}
}

// listZones lists all zones
func (s *SubAgent) listZones(ctx context.Context, opts QueryOptions) (*Response, error) {
	result, err := s.client.RunAPIWithContext(ctx, "GET", "/zones", "")
	if err != nil {
		return nil, fmt.Errorf("failed to list zones: %w", err)
	}

	formatted := formatZonesResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// listRecords lists DNS records for a zone
func (s *SubAgent) listRecords(ctx context.Context, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	// Need a zone ID to list records
	zoneID := opts.ZoneID
	if zoneID == "" && analysis.ZoneName != "" {
		// Look up zone ID from zone name
		var err error
		zoneID, err = s.getZoneIDByName(ctx, analysis.ZoneName)
		if err != nil {
			return nil, err
		}
	}

	if zoneID == "" {
		// List zones instead and prompt user to specify
		return s.listZones(ctx, opts)
	}

	endpoint := fmt.Sprintf("/zones/%s/dns_records", zoneID)
	if analysis.RecordType != "" {
		endpoint += fmt.Sprintf("?type=%s", analysis.RecordType)
	}

	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list records: %w", err)
	}

	formatted := formatRecordsResponse(result)
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
		Success bool   `json:"success"`
		Result  []Zone `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return "", fmt.Errorf("failed to parse zone response: %w", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "", fmt.Errorf("zone not found: %s", zoneName)
	}

	return response.Result[0].ID, nil
}

// generatePlan generates a modification plan for DNS operations
func (s *SubAgent) generatePlan(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Plan, error) {
	plan := &Plan{
		Commands: make([]Command, 0),
	}

	// Get zone ID if we have a zone name
	zoneID := opts.ZoneID
	if zoneID == "" && analysis.ZoneName != "" {
		var err error
		zoneID, err = s.getZoneIDByName(ctx, analysis.ZoneName)
		if err != nil {
			return nil, err
		}
	}

	switch analysis.Operation {
	case "create":
		if zoneID == "" {
			return nil, fmt.Errorf("zone name or ID required to create a record")
		}
		cmd, err := s.buildCreateRecordCommand(zoneID, analysis)
		if err != nil {
			return nil, err
		}
		plan.Commands = append(plan.Commands, cmd)
		plan.Summary = fmt.Sprintf("Create %s record for %s", analysis.RecordType, analysis.RecordName)

	case "update":
		if zoneID == "" {
			return nil, fmt.Errorf("zone name or ID required to update a record")
		}
		// Need to look up existing record first
		cmd, err := s.buildUpdateRecordCommand(ctx, zoneID, analysis)
		if err != nil {
			return nil, err
		}
		plan.Commands = append(plan.Commands, cmd)
		plan.Summary = fmt.Sprintf("Update %s record for %s", analysis.RecordType, analysis.RecordName)

	case "delete":
		if zoneID == "" {
			return nil, fmt.Errorf("zone name or ID required to delete a record")
		}
		cmd, err := s.buildDeleteRecordCommand(ctx, zoneID, analysis)
		if err != nil {
			return nil, err
		}
		plan.Commands = append(plan.Commands, cmd)
		plan.Summary = fmt.Sprintf("Delete %s record for %s", analysis.RecordType, analysis.RecordName)

	default:
		return nil, fmt.Errorf("unsupported operation: %s", analysis.Operation)
	}

	return plan, nil
}

// buildCreateRecordCommand builds a command to create a DNS record
func (s *SubAgent) buildCreateRecordCommand(zoneID string, analysis QueryAnalysis) (Command, error) {
	if analysis.RecordType == "" {
		return Command{}, fmt.Errorf("record type required (e.g., A, CNAME, TXT)")
	}
	if analysis.RecordName == "" {
		return Command{}, fmt.Errorf("record name required")
	}
	if analysis.RecordValue == "" {
		return Command{}, fmt.Errorf("record value required")
	}

	body := map[string]interface{}{
		"type":    analysis.RecordType,
		"name":    analysis.RecordName,
		"content": analysis.RecordValue,
	}

	if analysis.TTL > 0 {
		body["ttl"] = analysis.TTL
	} else {
		body["ttl"] = 1 // Auto TTL
	}

	if analysis.Proxied != nil {
		body["proxied"] = *analysis.Proxied
	}

	bodyJSON, _ := json.Marshal(body)

	return Command{
		Method:   "POST",
		Endpoint: fmt.Sprintf("/zones/%s/dns_records", zoneID),
		Body:     string(bodyJSON),
		Reason:   fmt.Sprintf("Create %s record %s -> %s", analysis.RecordType, analysis.RecordName, analysis.RecordValue),
	}, nil
}

// buildUpdateRecordCommand builds a command to update a DNS record
func (s *SubAgent) buildUpdateRecordCommand(ctx context.Context, zoneID string, analysis QueryAnalysis) (Command, error) {
	// Look up existing record
	recordID, err := s.getRecordID(ctx, zoneID, analysis.RecordName, analysis.RecordType)
	if err != nil {
		return Command{}, err
	}

	body := map[string]interface{}{
		"type":    analysis.RecordType,
		"name":    analysis.RecordName,
		"content": analysis.RecordValue,
	}

	if analysis.TTL > 0 {
		body["ttl"] = analysis.TTL
	}

	if analysis.Proxied != nil {
		body["proxied"] = *analysis.Proxied
	}

	bodyJSON, _ := json.Marshal(body)

	return Command{
		Method:   "PUT",
		Endpoint: fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID),
		Body:     string(bodyJSON),
		Reason:   fmt.Sprintf("Update %s record %s -> %s", analysis.RecordType, analysis.RecordName, analysis.RecordValue),
	}, nil
}

// buildDeleteRecordCommand builds a command to delete a DNS record
func (s *SubAgent) buildDeleteRecordCommand(ctx context.Context, zoneID string, analysis QueryAnalysis) (Command, error) {
	// Look up existing record
	recordID, err := s.getRecordID(ctx, zoneID, analysis.RecordName, analysis.RecordType)
	if err != nil {
		return Command{}, err
	}

	return Command{
		Method:   "DELETE",
		Endpoint: fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID),
		Reason:   fmt.Sprintf("Delete %s record %s", analysis.RecordType, analysis.RecordName),
	}, nil
}

// getRecordID looks up a record ID by name and type
func (s *SubAgent) getRecordID(ctx context.Context, zoneID, recordName, recordType string) (string, error) {
	endpoint := fmt.Sprintf("/zones/%s/dns_records?name=%s", zoneID, recordName)
	if recordType != "" {
		endpoint += fmt.Sprintf("&type=%s", recordType)
	}

	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return "", fmt.Errorf("failed to look up record: %w", err)
	}

	var response struct {
		Success bool        `json:"success"`
		Result  []DNSRecord `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return "", fmt.Errorf("failed to parse record response: %w", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "", fmt.Errorf("record not found: %s (type: %s)", recordName, recordType)
	}

	return response.Result[0].ID, nil
}

// formatZonesResponse formats the zones API response for display
func formatZonesResponse(response string) string {
	var apiResp struct {
		Success bool   `json:"success"`
		Result  []Zone `json:"result"`
	}

	if err := json.Unmarshal([]byte(response), &apiResp); err != nil {
		return response
	}

	if !apiResp.Success || len(apiResp.Result) == 0 {
		return "No zones found."
	}

	var sb strings.Builder
	sb.WriteString("Cloudflare Zones:\n\n")

	for _, zone := range apiResp.Result {
		sb.WriteString(fmt.Sprintf("  %s (%s)\n", zone.Name, zone.ID))
		sb.WriteString(fmt.Sprintf("    Status: %s\n", zone.Status))
		sb.WriteString(fmt.Sprintf("    Plan: %s\n", zone.Plan.Name))
		if len(zone.NameServers) > 0 {
			sb.WriteString(fmt.Sprintf("    Nameservers: %s\n", strings.Join(zone.NameServers, ", ")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatRecordsResponse formats the records API response for display
func formatRecordsResponse(response string) string {
	var apiResp struct {
		Success bool        `json:"success"`
		Result  []DNSRecord `json:"result"`
	}

	if err := json.Unmarshal([]byte(response), &apiResp); err != nil {
		return response
	}

	if !apiResp.Success || len(apiResp.Result) == 0 {
		return "No DNS records found."
	}

	var sb strings.Builder
	sb.WriteString("DNS Records:\n\n")

	for _, record := range apiResp.Result {
		proxiedStr := "DNS only"
		if record.Proxied {
			proxiedStr = "Proxied"
		}

		ttlStr := "Auto"
		if record.TTL > 1 {
			ttlStr = fmt.Sprintf("%d", record.TTL)
		}

		sb.WriteString(fmt.Sprintf("  %s %s -> %s\n", record.Type, record.Name, record.Content))
		sb.WriteString(fmt.Sprintf("    TTL: %s, %s\n", ttlStr, proxiedStr))
		sb.WriteString("\n")
	}

	return sb.String()
}
