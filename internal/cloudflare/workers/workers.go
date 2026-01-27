package workers

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
	RunWrangler(args ...string) (string, error)
	RunWranglerWithContext(ctx context.Context, args ...string) (string, error)
	GetAccountID() string
}

// SubAgent handles Workers-related operations
type SubAgent struct {
	client CloudflareClient
	debug  bool
}

// NewSubAgent creates a new Workers sub-agent
func NewSubAgent(client CloudflareClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// HandleQuery processes Workers-related queries
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[workers] handling query: %s\n", query)
	}

	// Analyze the query
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[workers] analysis: readonly=%v, operation=%s, resourceType=%s\n",
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

// analyzeQuery determines the nature of a Workers query
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	queryLower := strings.ToLower(query)
	analysis := QueryAnalysis{}

	// Detect resource type
	switch {
	case strings.Contains(queryLower, "kv") || strings.Contains(queryLower, "key-value") || strings.Contains(queryLower, "namespace"):
		analysis.ResourceType = "kv"
	case strings.Contains(queryLower, "d1") || strings.Contains(queryLower, "database"):
		analysis.ResourceType = "d1"
	case strings.Contains(queryLower, "r2") || strings.Contains(queryLower, "bucket") || strings.Contains(queryLower, "storage"):
		analysis.ResourceType = "r2"
	case strings.Contains(queryLower, "pages") || strings.Contains(queryLower, "site"):
		analysis.ResourceType = "pages"
	default:
		analysis.ResourceType = "worker"
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
		"list":   {"list", "show", "get all", "what", "which"},
		"get":    {"get", "describe", "details", "info about"},
		"create": {"create", "add", "new", "set up"},
		"delete": {"delete", "remove", "drop"},
		"deploy": {"deploy", "publish", "upload"},
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

// executeReadOnly handles read-only Workers operations
func (s *SubAgent) executeReadOnly(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case "worker":
		return s.listWorkers(ctx)
	case "kv":
		return s.listKVNamespaces(ctx)
	case "d1":
		return s.listD1Databases(ctx)
	case "r2":
		return s.listR2Buckets(ctx)
	case "pages":
		return s.listPagesProjects(ctx)
	default:
		return s.listWorkers(ctx)
	}
}

// listWorkers lists deployed Workers using wrangler
func (s *SubAgent) listWorkers(ctx context.Context) (*Response, error) {
	// Try using the API first
	accountID := s.client.GetAccountID()
	if accountID != "" {
		endpoint := fmt.Sprintf("/accounts/%s/workers/scripts", accountID)
		result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
		if err == nil {
			formatted := s.formatWorkersResponse(result)
			return &Response{
				Type:   ResponseTypeResult,
				Result: formatted,
			}, nil
		}
	}

	// Fall back to wrangler
	result, err := s.client.RunWranglerWithContext(ctx, "deployments", "list")
	if err != nil {
		// Try alternative command
		result, err = s.client.RunWranglerWithContext(ctx, "whoami")
		if err != nil {
			return nil, fmt.Errorf("failed to list workers: %w", err)
		}
		return &Response{
			Type:   ResponseTypeResult,
			Result: "Workers information:\n" + result + "\n\nNote: Use 'wrangler deployments list' directly for deployment info",
		}, nil
	}

	return &Response{
		Type:   ResponseTypeResult,
		Result: "Cloudflare Workers:\n\n" + result,
	}, nil
}

// listKVNamespaces lists KV namespaces using wrangler
func (s *SubAgent) listKVNamespaces(ctx context.Context) (*Response, error) {
	// Try API first
	accountID := s.client.GetAccountID()
	if accountID != "" {
		endpoint := fmt.Sprintf("/accounts/%s/storage/kv/namespaces", accountID)
		result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
		if err == nil {
			formatted := s.formatKVNamespacesResponse(result)
			return &Response{
				Type:   ResponseTypeResult,
				Result: formatted,
			}, nil
		}
	}

	// Fall back to wrangler
	result, err := s.client.RunWranglerWithContext(ctx, "kv", "namespace", "list")
	if err != nil {
		return nil, fmt.Errorf("failed to list KV namespaces: %w", err)
	}

	return &Response{
		Type:   ResponseTypeResult,
		Result: "Workers KV Namespaces:\n\n" + result,
	}, nil
}

// listD1Databases lists D1 databases using wrangler
func (s *SubAgent) listD1Databases(ctx context.Context) (*Response, error) {
	// Try API first
	accountID := s.client.GetAccountID()
	if accountID != "" {
		endpoint := fmt.Sprintf("/accounts/%s/d1/database", accountID)
		result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
		if err == nil {
			formatted := s.formatD1DatabasesResponse(result)
			return &Response{
				Type:   ResponseTypeResult,
				Result: formatted,
			}, nil
		}
	}

	// Fall back to wrangler
	result, err := s.client.RunWranglerWithContext(ctx, "d1", "list")
	if err != nil {
		return nil, fmt.Errorf("failed to list D1 databases: %w", err)
	}

	return &Response{
		Type:   ResponseTypeResult,
		Result: "D1 Databases:\n\n" + result,
	}, nil
}

// listR2Buckets lists R2 buckets using wrangler
func (s *SubAgent) listR2Buckets(ctx context.Context) (*Response, error) {
	// Try API first
	accountID := s.client.GetAccountID()
	if accountID != "" {
		endpoint := fmt.Sprintf("/accounts/%s/r2/buckets", accountID)
		result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
		if err == nil {
			formatted := s.formatR2BucketsResponse(result)
			return &Response{
				Type:   ResponseTypeResult,
				Result: formatted,
			}, nil
		}
	}

	// Fall back to wrangler
	result, err := s.client.RunWranglerWithContext(ctx, "r2", "bucket", "list")
	if err != nil {
		return nil, fmt.Errorf("failed to list R2 buckets: %w", err)
	}

	return &Response{
		Type:   ResponseTypeResult,
		Result: "R2 Buckets:\n\n" + result,
	}, nil
}

// listPagesProjects lists Pages projects
func (s *SubAgent) listPagesProjects(ctx context.Context) (*Response, error) {
	accountID := s.client.GetAccountID()
	if accountID == "" {
		return nil, fmt.Errorf("account ID is required for Pages queries")
	}

	endpoint := fmt.Sprintf("/accounts/%s/pages/projects", accountID)
	result, err := s.client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list Pages projects: %w", err)
	}

	formatted := s.formatPagesProjectsResponse(result)
	return &Response{
		Type:   ResponseTypeResult,
		Result: formatted,
	}, nil
}

// generatePlan creates a plan for Workers modifications
func (s *SubAgent) generatePlan(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Plan, error) {
	switch analysis.ResourceType {
	case "kv":
		return s.generateKVPlan(analysis)
	case "d1":
		return s.generateD1Plan(analysis)
	case "r2":
		return s.generateR2Plan(analysis)
	default:
		return nil, fmt.Errorf("worker deployment plans should use 'wrangler deploy' directly")
	}
}

// generateKVPlan creates a plan for KV operations
func (s *SubAgent) generateKVPlan(analysis QueryAnalysis) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		name := analysis.ResourceName
		if name == "" {
			name = "my-kv-namespace"
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "wrangler",
			Args:   []string{"kv", "namespace", "create", name},
			Reason: fmt.Sprintf("Create KV namespace '%s'", name),
		})
		plan.Summary = fmt.Sprintf("Create KV namespace: %s", name)

	case "delete":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("namespace name or ID is required to delete")
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "wrangler",
			Args:   []string{"kv", "namespace", "delete", "--namespace-id", analysis.ResourceName},
			Reason: fmt.Sprintf("Delete KV namespace '%s'", analysis.ResourceName),
		})
		plan.Summary = fmt.Sprintf("Delete KV namespace: %s", analysis.ResourceName)

	default:
		return nil, fmt.Errorf("unsupported KV operation: %s", analysis.Operation)
	}

	return plan, nil
}

// generateD1Plan creates a plan for D1 operations
func (s *SubAgent) generateD1Plan(analysis QueryAnalysis) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		name := analysis.ResourceName
		if name == "" {
			name = "my-database"
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "wrangler",
			Args:   []string{"d1", "create", name},
			Reason: fmt.Sprintf("Create D1 database '%s'", name),
		})
		plan.Summary = fmt.Sprintf("Create D1 database: %s", name)

	case "delete":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("database name is required to delete")
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "wrangler",
			Args:   []string{"d1", "delete", analysis.ResourceName},
			Reason: fmt.Sprintf("Delete D1 database '%s'", analysis.ResourceName),
		})
		plan.Summary = fmt.Sprintf("Delete D1 database: %s", analysis.ResourceName)

	default:
		return nil, fmt.Errorf("unsupported D1 operation: %s", analysis.Operation)
	}

	return plan, nil
}

// generateR2Plan creates a plan for R2 operations
func (s *SubAgent) generateR2Plan(analysis QueryAnalysis) (*Plan, error) {
	plan := &Plan{
		Commands: []Command{},
	}

	switch analysis.Operation {
	case "create":
		name := analysis.ResourceName
		if name == "" {
			name = "my-bucket"
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "wrangler",
			Args:   []string{"r2", "bucket", "create", name},
			Reason: fmt.Sprintf("Create R2 bucket '%s'", name),
		})
		plan.Summary = fmt.Sprintf("Create R2 bucket: %s", name)

	case "delete":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("bucket name is required to delete")
		}
		plan.Commands = append(plan.Commands, Command{
			Tool:   "wrangler",
			Args:   []string{"r2", "bucket", "delete", analysis.ResourceName},
			Reason: fmt.Sprintf("Delete R2 bucket '%s'", analysis.ResourceName),
		})
		plan.Summary = fmt.Sprintf("Delete R2 bucket: %s", analysis.ResourceName)

	default:
		return nil, fmt.Errorf("unsupported R2 operation: %s", analysis.Operation)
	}

	return plan, nil
}

// Format response helpers

func (s *SubAgent) formatWorkersResponse(result string) string {
	var response struct {
		Success bool     `json:"success"`
		Result  []Worker `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No Workers found."
	}

	var sb strings.Builder
	sb.WriteString("Cloudflare Workers:\n\n")

	for _, worker := range response.Result {
		name := worker.Name
		if name == "" {
			name = worker.ID
		}
		sb.WriteString(fmt.Sprintf("  %s\n", name))
		if worker.ID != "" && worker.ID != name {
			sb.WriteString(fmt.Sprintf("    ID: %s\n", worker.ID))
		}
		if !worker.ModifiedOn.IsZero() {
			sb.WriteString(fmt.Sprintf("    Modified: %s\n", worker.ModifiedOn.Format("2006-01-02 15:04:05")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatKVNamespacesResponse(result string) string {
	var response struct {
		Success bool          `json:"success"`
		Result  []KVNamespace `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No KV namespaces found."
	}

	var sb strings.Builder
	sb.WriteString("Workers KV Namespaces:\n\n")

	for _, ns := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", ns.Title))
		sb.WriteString(fmt.Sprintf("    ID: %s\n", ns.ID))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatD1DatabasesResponse(result string) string {
	var response struct {
		Success bool         `json:"success"`
		Result  []D1Database `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No D1 databases found."
	}

	var sb strings.Builder
	sb.WriteString("D1 Databases:\n\n")

	for _, db := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", db.Name))
		sb.WriteString(fmt.Sprintf("    UUID: %s\n", db.UUID))
		if db.NumTables > 0 {
			sb.WriteString(fmt.Sprintf("    Tables: %d\n", db.NumTables))
		}
		if db.FileSize > 0 {
			sb.WriteString(fmt.Sprintf("    Size: %d bytes\n", db.FileSize))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatR2BucketsResponse(result string) string {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Buckets []R2Bucket `json:"buckets"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result.Buckets) == 0 {
		return "No R2 buckets found."
	}

	var sb strings.Builder
	sb.WriteString("R2 Buckets:\n\n")

	for _, bucket := range response.Result.Buckets {
		sb.WriteString(fmt.Sprintf("  %s\n", bucket.Name))
		if bucket.Location != "" {
			sb.WriteString(fmt.Sprintf("    Location: %s\n", bucket.Location))
		}
		if !bucket.CreationDate.IsZero() {
			sb.WriteString(fmt.Sprintf("    Created: %s\n", bucket.CreationDate.Format("2006-01-02")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (s *SubAgent) formatPagesProjectsResponse(result string) string {
	var response struct {
		Success bool           `json:"success"`
		Result  []PagesProject `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return fmt.Sprintf("Error parsing response: %v", err)
	}

	if !response.Success || len(response.Result) == 0 {
		return "No Pages projects found."
	}

	var sb strings.Builder
	sb.WriteString("Cloudflare Pages Projects:\n\n")

	for _, project := range response.Result {
		sb.WriteString(fmt.Sprintf("  %s\n", project.Name))
		sb.WriteString(fmt.Sprintf("    Subdomain: %s.pages.dev\n", project.Subdomain))
		if project.ProductionBranch != "" {
			sb.WriteString(fmt.Sprintf("    Production Branch: %s\n", project.ProductionBranch))
		}
		if len(project.Domains) > 0 {
			sb.WriteString(fmt.Sprintf("    Custom Domains: %s\n", strings.Join(project.Domains, ", ")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
