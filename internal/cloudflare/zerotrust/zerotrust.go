package zerotrust

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CloudflareClient defines the interface for Cloudflare operations
type CloudflareClient interface {
	RunAPI(method, endpoint, body string) (string, error)
	RunAPIWithContext(ctx context.Context, method, endpoint, body string) (string, error)
	RunCloudflared(args ...string) (string, error)
	RunCloudflaredWithContext(ctx context.Context, args ...string) (string, error)
	GetAccountID() string
}

// SubAgent handles Zero Trust related operations
type SubAgent struct {
	client CloudflareClient
	debug  bool
}

// NewSubAgent creates a new Zero Trust sub-agent
func NewSubAgent(client CloudflareClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// HandleQuery processes Zero Trust related queries
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[zerotrust] handling query: %s\n", query)
	}

	// Analyze the query
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[zerotrust] analysis: readonly=%v, operation=%s, resourceType=%s\n",
			analysis.IsReadOnly, analysis.Operation, analysis.ResourceType)
	}

	// Get account ID
	accountID := opts.AccountID
	if accountID == "" {
		accountID = s.client.GetAccountID()
	}
	if accountID == "" {
		return nil, fmt.Errorf("account ID is required for Zero Trust queries")
	}

	// For read-only operations, execute immediately
	if analysis.IsReadOnly {
		return s.executeReadOnly(ctx, query, analysis, accountID)
	}

	// For modifications, generate a plan
	plan, err := s.generatePlan(ctx, query, analysis, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	return &Response{
		Type:    ResponseTypePlan,
		Plan:    plan,
		Message: plan.Summary,
	}, nil
}

// analyzeQuery determines the nature of a Zero Trust query
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	queryLower := strings.ToLower(query)
	analysis := QueryAnalysis{}

	// Detect resource type
	switch {
	case strings.Contains(queryLower, "tunnel"):
		analysis.ResourceType = "tunnel"
	case strings.Contains(queryLower, "access app") || strings.Contains(queryLower, "application"):
		analysis.ResourceType = "access_app"
	case strings.Contains(queryLower, "access polic") || strings.Contains(queryLower, "policy"):
		analysis.ResourceType = "access_policy"
	default:
		analysis.ResourceType = "tunnel"
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

	// Extract resource name if mentioned
	analysis.ResourceName = s.extractResourceName(query)

	return analysis
}

// detectOperation determines the operation type from query
func (s *SubAgent) detectOperation(queryLower string) string {
	operations := map[string][]string{
		"list":   {"list", "show all", "get all", "what", "which"},
		"get":    {"get", "describe", "details", "info about", "status"},
		"create": {"create", "add", "new", "set up"},
		"delete": {"delete", "remove", "destroy"},
		"route":  {"route", "connect", "point"},
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

// extractResourceName extracts the resource name from query
func (s *SubAgent) extractResourceName(query string) string {
	// Look for quoted names
	if strings.Contains(query, "\"") {
		parts := strings.Split(query, "\"")
		if len(parts) > 1 {
			return parts[1]
		}
	}
	if strings.Contains(query, "'") {
		parts := strings.Split(query, "'")
		if len(parts) > 1 {
			return parts[1]
		}
	}

	return ""
}

// executeReadOnly handles read-only Zero Trust operations
func (s *SubAgent) executeReadOnly(ctx context.Context, query string, analysis QueryAnalysis, accountID string) (*Response, error) {
	switch analysis.ResourceType {
	case "tunnel":
		if analysis.Operation == "get" && analysis.ResourceName != "" {
			return s.getTunnel(ctx, accountID, analysis.ResourceName)
		}
		return s.listTunnels(ctx, accountID)
	case "access_app":
		return s.listAccessApplications(ctx, accountID)
	case "access_policy":
		return s.listAccessPolicies(ctx, accountID, analysis.ResourceName)
	default:
		return s.listTunnels(ctx, accountID)
	}
}

// listTunnels lists Cloudflare Tunnels
func (s *SubAgent) listTunnels(ctx context.Context, accountID string) (*Response, error) {
	// Try API first
	endpoint := fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err == nil {
		formatted := s.formatTunnelsResponse(result)
		return &Response{
			Type:   ResponseTypeResult,
			Result: formatted,
		}, nil
	}

	// Fall back to cloudflared CLI
	result, err = s.client.RunCloudflaredWithContext(ctx, "tunnel", "list")
	if err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	return &Response{
		Type:   ResponseTypeResult,
		Result: "Cloudflare Tunnels:\n\n" + result,
	}, nil
}

// getTunnel gets details of a specific tunnel
func (s *SubAgent) getTunnel(ctx context.Context, accountID, tunnelID string) (*Response, error) {
	endpoint := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s", accountID, tunnelID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get tunnel: %w", err)
	}

	formatted := s.formatTunnelResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// listAccessApplications lists Zero Trust Access applications
func (s *SubAgent) listAccessApplications(ctx context.Context, accountID string) (*Response, error) {
	endpoint := fmt.Sprintf("/accounts/%s/access/apps", accountID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list Access applications: %w", err)
	}

	formatted := s.formatAccessAppsResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// listAccessPolicies lists Access policies for an application
func (s *SubAgent) listAccessPolicies(ctx context.Context, accountID, appID string) (*Response, error) {
	if appID == "" {
		// List all apps first to help user
		return s.listAccessApplications(ctx, accountID)
	}

	endpoint := fmt.Sprintf("/accounts/%s/access/apps/%s/policies", accountID, appID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list Access policies: %w", err)
	}

	formatted := s.formatAccessPoliciesResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// generatePlan creates a plan for Zero Trust modifications
func (s *SubAgent) generatePlan(ctx context.Context, query string, analysis QueryAnalysis, accountID string) (*Plan, error) {
	switch analysis.ResourceType {
	case "tunnel":
		return s.generateTunnelPlan(analysis, accountID)
	case "access_app":
		return s.generateAccessAppPlan(analysis, accountID)
	default:
		return nil, fmt.Errorf("unsupported Zero Trust resource type: %s", analysis.ResourceType)
	}
}

// generateTunnelPlan creates a plan for tunnel operations
func (s *SubAgent) generateTunnelPlan(analysis QueryAnalysis, accountID string) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		name := analysis.ResourceName
		if name == "" {
			name = "my-tunnel"
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "cloudflared",
			Args:   []string{"tunnel", "create", name},
			Reason: fmt.Sprintf("Create tunnel '%s'", name),
		})
		plan.Summary = fmt.Sprintf("Create Cloudflare Tunnel: %s", name)

	case "delete":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("tunnel name or ID is required to delete")
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "cloudflared",
			Args:   []string{"tunnel", "delete", analysis.ResourceName},
			Reason: fmt.Sprintf("Delete tunnel '%s'", analysis.ResourceName),
		})
		plan.Summary = fmt.Sprintf("Delete Cloudflare Tunnel: %s", analysis.ResourceName)

	case "route":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("tunnel name and hostname are required for routing")
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "cloudflared",
			Args:   []string{"tunnel", "route", "dns", analysis.ResourceName, "<hostname>"},
			Reason: fmt.Sprintf("Route DNS to tunnel '%s'", analysis.ResourceName),
		})
		plan.Summary = fmt.Sprintf("Route DNS to Cloudflare Tunnel: %s", analysis.ResourceName)

	default:
		return nil, fmt.Errorf("unsupported tunnel operation: %s", analysis.Operation)
	}

	return plan, nil
}

// generateAccessAppPlan creates a plan for Access application operations
func (s *SubAgent) generateAccessAppPlan(analysis QueryAnalysis, accountID string) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		name := analysis.ResourceName
		if name == "" {
			name = "my-app"
		}
		// Access apps require API, not CLI
		body := fmt.Sprintf(`{"name":"%s","domain":"<domain>","type":"self_hosted"}`, name)
		plan.Commands = append(plan.Commands, Command{
			Tool:     "api",
			Method:   "POST",
			Endpoint: fmt.Sprintf("/accounts/%s/access/apps", accountID),
			Body:     body,
			Reason:   fmt.Sprintf("Create Access application '%s'", name),
		})
		plan.Summary = fmt.Sprintf("Create Access Application: %s", name)

	case "delete":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("application ID is required to delete")
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:     "api",
			Method:   "DELETE",
			Endpoint: fmt.Sprintf("/accounts/%s/access/apps/%s", accountID, analysis.ResourceName),
			Reason:   fmt.Sprintf("Delete Access application '%s'", analysis.ResourceName),
		})
		plan.Summary = fmt.Sprintf("Delete Access Application: %s", analysis.ResourceName)

	default:
		return nil, fmt.Errorf("unsupported Access application operation: %s", analysis.Operation)
	}

	return plan, nil
}

// Format response helpers

func (s *SubAgent) formatTunnelsResponse(result string) string {
	var response struct {
		Success bool     `json:"success"`
		Result  []Tunnel `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No tunnels found."
	}

	var sb strings.Builder
	sb.WriteString("Cloudflare Tunnels:\n\n")

	for _, tunnel := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", tunnel.Name))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", tunnel.ID))
		sb.WriteString(fmt.Sprintf("    Status: %s\n", tunnel.Status))
		if len(tunnel.Connections) > 0 {
			sb.WriteString(fmt.Sprintf("    Active Connections: %d\n", len(tunnel.Connections)))
			for _, conn := range tunnel.Connections {
				if conn.IsActive {
					sb.WriteString(fmt.Sprintf("      - %s (from %s)\n", conn.ColoName, conn.OriginIP))
				}
			}
		}
		if !tunnel.CreatedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("    Created: %s\n", tunnel.CreatedAt.Format("2006-01-02 15:04:05")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatTunnelResponse(result string) string {
	var response struct {
		Success bool   `json:"success"`
		Result  Tunnel `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success {
		return "Tunnel not found."
	}

	tunnel := response.Result
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Tunnel: %s\n\n", tunnel.Name))
	sb.WriteString(fmt.Sprintf("  ID: %s\n", tunnel.ID))
	sb.WriteString(fmt.Sprintf("  Status: %s\n", tunnel.Status))

	if len(tunnel.Connections) > 0 {
		sb.WriteString("\n  Connections:\n")
		for _, conn := range tunnel.Connections {
			status := "inactive"
			if conn.IsActive {
				status = "active"
			}
			sb.WriteString(fmt.Sprintf("    - %s (%s)\n", conn.ColoName, status))
			if conn.OriginIP != "" {
				sb.WriteString(fmt.Sprintf("      Origin IP: %s\n", conn.OriginIP))
			}
			if conn.ClientVersion != "" {
				sb.WriteString(fmt.Sprintf("      Client Version: %s\n", conn.ClientVersion))
			}
		}
	}

	if !tunnel.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("\n  Created: %s\n", tunnel.CreatedAt.Format("2006-01-02 15:04:05")))
	}

	return sb.String()
}

func (s *SubAgent) formatAccessAppsResponse(result string) string {
	var response struct {
		Success bool                `json:"success"`
		Result  []AccessApplication `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No Access applications found."
	}

	var sb strings.Builder
	sb.WriteString("Zero Trust Access Applications:\n\n")

	for _, app := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", app.Name))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", app.ID))
		sb.WriteString(fmt.Sprintf("    Domain: %s\n", app.Domain))
		sb.WriteString(fmt.Sprintf("    Type: %s\n", app.Type))
		if app.SessionDuration != "" {
			sb.WriteString(fmt.Sprintf("    Session Duration: %s\n", app.SessionDuration))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatAccessPoliciesResponse(result string) string {
	var response struct {
		Success bool           `json:"success"`
		Result  []AccessPolicy `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No Access policies found."
	}

	var sb strings.Builder
	sb.WriteString("Access Policies:\n\n")

	for _, policy := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", policy.Name))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", policy.ID))
		sb.WriteString(fmt.Sprintf("    Decision: %s\n", policy.Decision))
		sb.WriteString(fmt.Sprintf("    Precedence: %d\n", policy.Precedence))
		if len(policy.Include) > 0 {
			sb.WriteString(fmt.Sprintf("    Include Rules: %d\n", len(policy.Include)))
		}
		if len(policy.Exclude) > 0 {
			sb.WriteString(fmt.Sprintf("    Exclude Rules: %d\n", len(policy.Exclude)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
