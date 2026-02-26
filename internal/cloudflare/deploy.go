package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeployWorkerOptions contains options for deploying a Worker
type DeployWorkerOptions struct {
	Name          string            // Worker name
	ScriptPath    string            // Path to the worker script file
	ScriptContent string            // Direct script content (alternative to ScriptPath)
	Compatibility string            // Compatibility date (e.g., "2024-01-01")
	Bindings      map[string]string // Environment variable bindings
	KVNamespaces  []KVBinding       // KV namespace bindings
	R2Buckets     []R2Binding       // R2 bucket bindings
	D1Databases   []D1Binding       // D1 database bindings
	Routes        []string          // Worker routes
	CustomDomains []string          // Custom domains
}

// KVBinding represents a KV namespace binding for a Worker
type KVBinding struct {
	Name        string // Binding name in the worker
	NamespaceID string // KV namespace ID
}

// R2Binding represents an R2 bucket binding for a Worker
type R2Binding struct {
	Name       string // Binding name in the worker
	BucketName string // R2 bucket name
}

// D1Binding represents a D1 database binding for a Worker
type D1Binding struct {
	Name       string // Binding name in the worker
	DatabaseID string // D1 database ID
}

// DeployWorkerResult contains the result of a Worker deployment
type DeployWorkerResult struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Etag      string   `json:"etag"`
	Size      int      `json:"size"`
	Routes    []string `json:"routes,omitempty"`
	ScriptURL string   `json:"script_url,omitempty"`
}

// DeployWorker deploys a Cloudflare Worker
func (c *Client) DeployWorker(ctx context.Context, opts DeployWorkerOptions) (*DeployWorkerResult, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("worker name is required")
	}

	// Get script content
	var script string
	if opts.ScriptContent != "" {
		script = opts.ScriptContent
	} else if opts.ScriptPath != "" {
		content, err := os.ReadFile(opts.ScriptPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read worker script: %w", err)
		}
		script = string(content)
	} else {
		return nil, fmt.Errorf("either ScriptPath or ScriptContent is required")
	}

	// Check if wrangler is available for deployment
	if _, err := c.RunWranglerWithContext(ctx, "--version"); err == nil {
		return c.deployWorkerWithWrangler(ctx, opts, script)
	}

	// Fallback to API deployment
	return c.deployWorkerWithAPI(ctx, opts, script)
}

// deployWorkerWithWrangler deploys a Worker using wrangler CLI
func (c *Client) deployWorkerWithWrangler(ctx context.Context, opts DeployWorkerOptions, script string) (*DeployWorkerResult, error) {
	// Create a temporary directory for the worker
	tmpDir, err := os.MkdirTemp("", "cf-worker-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write the worker script
	scriptPath := filepath.Join(tmpDir, "index.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		return nil, fmt.Errorf("failed to write worker script: %w", err)
	}

	// Generate wrangler.toml
	wranglerConfig := c.generateWranglerConfig(opts)
	configPath := filepath.Join(tmpDir, "wrangler.toml")
	if err := os.WriteFile(configPath, []byte(wranglerConfig), 0644); err != nil {
		return nil, fmt.Errorf("failed to write wrangler config: %w", err)
	}

	// Run wrangler deploy
	args := []string{"deploy", "--config", configPath}
	output, err := c.RunWranglerWithContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("wrangler deploy failed: %w", err)
	}

	return &DeployWorkerResult{
		Name:      opts.Name,
		ScriptURL: fmt.Sprintf("https://%s.%s.workers.dev", opts.Name, c.accountID),
	}, parseWranglerDeployOutput(output)
}

// generateWranglerConfig generates a wrangler.toml configuration
func (c *Client) generateWranglerConfig(opts DeployWorkerOptions) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("name = %q\n", opts.Name))
	sb.WriteString("main = \"index.js\"\n")

	if opts.Compatibility != "" {
		sb.WriteString(fmt.Sprintf("compatibility_date = %q\n", opts.Compatibility))
	} else {
		sb.WriteString("compatibility_date = \"2024-01-01\"\n")
	}

	if c.accountID != "" {
		sb.WriteString(fmt.Sprintf("account_id = %q\n", c.accountID))
	}

	// Environment variables
	if len(opts.Bindings) > 0 {
		sb.WriteString("\n[vars]\n")
		for key, value := range opts.Bindings {
			sb.WriteString(fmt.Sprintf("%s = %q\n", key, value))
		}
	}

	// KV namespace bindings
	for _, kv := range opts.KVNamespaces {
		sb.WriteString(fmt.Sprintf("\n[[kv_namespaces]]\n"))
		sb.WriteString(fmt.Sprintf("binding = %q\n", kv.Name))
		sb.WriteString(fmt.Sprintf("id = %q\n", kv.NamespaceID))
	}

	// R2 bucket bindings
	for _, r2 := range opts.R2Buckets {
		sb.WriteString(fmt.Sprintf("\n[[r2_buckets]]\n"))
		sb.WriteString(fmt.Sprintf("binding = %q\n", r2.Name))
		sb.WriteString(fmt.Sprintf("bucket_name = %q\n", r2.BucketName))
	}

	// D1 database bindings
	for _, d1 := range opts.D1Databases {
		sb.WriteString(fmt.Sprintf("\n[[d1_databases]]\n"))
		sb.WriteString(fmt.Sprintf("binding = %q\n", d1.Name))
		sb.WriteString(fmt.Sprintf("database_id = %q\n", d1.DatabaseID))
	}

	// Routes
	if len(opts.Routes) > 0 {
		sb.WriteString("\n")
		for _, route := range opts.Routes {
			sb.WriteString(fmt.Sprintf("route = %q\n", route))
		}
	}

	return sb.String()
}

// deployWorkerWithAPI deploys a Worker using the Cloudflare API directly
func (c *Client) deployWorkerWithAPI(ctx context.Context, opts DeployWorkerOptions, script string) (*DeployWorkerResult, error) {
	endpoint := fmt.Sprintf("/accounts/%s/workers/scripts/%s", c.accountID, opts.Name)

	// For simple scripts, we can use the direct PUT method
	// For scripts with bindings, we need to use the multipart form
	if len(opts.KVNamespaces) == 0 && len(opts.R2Buckets) == 0 && len(opts.D1Databases) == 0 {
		result, err := c.RunAPIWithContext(ctx, "PUT", endpoint, script)
		if err != nil {
			return nil, err
		}

		var response struct {
			Success bool `json:"success"`
			Result  struct {
				ID   string `json:"id"`
				Etag string `json:"etag"`
				Size int    `json:"size"`
			} `json:"result"`
		}

		if err := json.Unmarshal([]byte(result), &response); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		if !response.Success {
			return nil, fmt.Errorf("worker deployment failed")
		}

		return &DeployWorkerResult{
			ID:   response.Result.ID,
			Name: opts.Name,
			Etag: response.Result.Etag,
			Size: response.Result.Size,
		}, nil
	}

	// For complex deployments with bindings, use wrangler or return error
	return nil, fmt.Errorf("workers with bindings require wrangler CLI (install with: npm install -g wrangler)")
}

// parseWranglerDeployOutput parses the output from wrangler deploy
func parseWranglerDeployOutput(output string) error {
	if strings.Contains(strings.ToLower(output), "error") {
		return fmt.Errorf("deployment may have issues: %s", output)
	}
	return nil
}

// DeleteWorker deletes a Cloudflare Worker
func (c *Client) DeleteWorker(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("worker name is required")
	}

	endpoint := fmt.Sprintf("/accounts/%s/workers/scripts/%s", c.accountID, name)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// CreatePagesProjectOptions contains options for creating a Pages project
type CreatePagesProjectOptions struct {
	Name             string            // Project name
	ProductionBranch string            // Production branch name (default: main)
	BuildCommand     string            // Build command
	BuildDirectory   string            // Build output directory
	RootDirectory    string            // Root directory containing source
	EnvironmentVars  map[string]string // Environment variables
}

// CreatePagesProjectResult contains the result of creating a Pages project
type CreatePagesProjectResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
}

// CreatePagesProject creates a new Cloudflare Pages project
func (c *Client) CreatePagesProject(ctx context.Context, opts CreatePagesProjectOptions) (*CreatePagesProjectResult, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("project name is required")
	}

	productionBranch := opts.ProductionBranch
	if productionBranch == "" {
		productionBranch = "main"
	}

	body := map[string]interface{}{
		"name":              opts.Name,
		"production_branch": productionBranch,
	}

	if opts.BuildCommand != "" || opts.BuildDirectory != "" {
		buildConfig := map[string]string{}
		if opts.BuildCommand != "" {
			buildConfig["build_command"] = opts.BuildCommand
		}
		if opts.BuildDirectory != "" {
			buildConfig["destination_dir"] = opts.BuildDirectory
		}
		if opts.RootDirectory != "" {
			buildConfig["root_dir"] = opts.RootDirectory
		}
		body["build_config"] = buildConfig
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("/accounts/%s/pages/projects", c.accountID)
	result, err := c.RunAPIWithContext(ctx, "POST", endpoint, string(bodyJSON))
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Subdomain string `json:"subdomain"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to create pages project")
	}

	return &CreatePagesProjectResult{
		ID:        response.Result.ID,
		Name:      response.Result.Name,
		Subdomain: response.Result.Subdomain,
	}, nil
}

// DeployPagesOptions contains options for deploying to Pages
type DeployPagesOptions struct {
	ProjectName string // Pages project name
	Directory   string // Directory containing built assets
	Branch      string // Branch name (optional)
	CommitHash  string // Commit hash (optional)
	CommitMsg   string // Commit message (optional)
}

// DeployPagesResult contains the result of a Pages deployment
type DeployPagesResult struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Environment string `json:"environment"`
}

// DeployPages deploys assets to a Cloudflare Pages project
func (c *Client) DeployPages(ctx context.Context, opts DeployPagesOptions) (*DeployPagesResult, error) {
	if opts.ProjectName == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if opts.Directory == "" {
		return nil, fmt.Errorf("directory is required")
	}

	// Check if directory exists
	if _, err := os.Stat(opts.Directory); os.IsNotExist(err) {
		return nil, fmt.Errorf("directory does not exist: %s", opts.Directory)
	}

	// Use wrangler pages deploy
	args := []string{"pages", "deploy", opts.Directory, "--project-name", opts.ProjectName}

	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch)
	}
	if opts.CommitHash != "" {
		args = append(args, "--commit-hash", opts.CommitHash)
	}
	if opts.CommitMsg != "" {
		args = append(args, "--commit-message", opts.CommitMsg)
	}

	output, err := c.RunWranglerWithContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("pages deploy failed: %w", err)
	}

	// Parse output for deployment URL
	url := extractDeploymentURL(output)

	return &DeployPagesResult{
		URL: url,
	}, nil
}

// extractDeploymentURL extracts the deployment URL from wrangler output
func extractDeploymentURL(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}

// DeletePagesProject deletes a Cloudflare Pages project
func (c *Client) DeletePagesProject(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("project name is required")
	}

	endpoint := fmt.Sprintf("/accounts/%s/pages/projects/%s", c.accountID, name)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// CreateKVNamespaceResult contains the result of creating a KV namespace
type CreateKVNamespaceResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// CreateKVNamespace creates a new Workers KV namespace
func (c *Client) CreateKVNamespace(ctx context.Context, title string) (*CreateKVNamespaceResult, error) {
	if title == "" {
		return nil, fmt.Errorf("namespace title is required")
	}

	body := fmt.Sprintf(`{"title": %q}`, title)
	endpoint := fmt.Sprintf("/accounts/%s/storage/kv/namespaces", c.accountID)

	result, err := c.RunAPIWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to create KV namespace")
	}

	return &CreateKVNamespaceResult{
		ID:    response.Result.ID,
		Title: response.Result.Title,
	}, nil
}

// DeleteKVNamespace deletes a Workers KV namespace
func (c *Client) DeleteKVNamespace(ctx context.Context, namespaceID string) error {
	if namespaceID == "" {
		return fmt.Errorf("namespace ID is required")
	}

	endpoint := fmt.Sprintf("/accounts/%s/storage/kv/namespaces/%s", c.accountID, namespaceID)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// CreateD1DatabaseResult contains the result of creating a D1 database
type CreateD1DatabaseResult struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// CreateD1Database creates a new D1 database
func (c *Client) CreateD1Database(ctx context.Context, name string) (*CreateD1DatabaseResult, error) {
	if name == "" {
		return nil, fmt.Errorf("database name is required")
	}

	body := fmt.Sprintf(`{"name": %q}`, name)
	endpoint := fmt.Sprintf("/accounts/%s/d1/database", c.accountID)

	result, err := c.RunAPIWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to create D1 database")
	}

	return &CreateD1DatabaseResult{
		UUID: response.Result.UUID,
		Name: response.Result.Name,
	}, nil
}

// DeleteD1Database deletes a D1 database
func (c *Client) DeleteD1Database(ctx context.Context, databaseID string) error {
	if databaseID == "" {
		return fmt.Errorf("database ID is required")
	}

	endpoint := fmt.Sprintf("/accounts/%s/d1/database/%s", c.accountID, databaseID)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// CreateR2BucketResult contains the result of creating an R2 bucket
type CreateR2BucketResult struct {
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
}

// CreateR2Bucket creates a new R2 bucket
func (c *Client) CreateR2Bucket(ctx context.Context, name string, location string) (*CreateR2BucketResult, error) {
	if name == "" {
		return nil, fmt.Errorf("bucket name is required")
	}

	body := map[string]string{"name": name}
	if location != "" {
		body["locationHint"] = location
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("/accounts/%s/r2/buckets", c.accountID)
	result, err := c.RunAPIWithContext(ctx, "POST", endpoint, string(bodyJSON))
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Name     string `json:"name"`
			Location string `json:"location"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to create R2 bucket")
	}

	return &CreateR2BucketResult{
		Name:     response.Result.Name,
		Location: response.Result.Location,
	}, nil
}

// DeleteR2Bucket deletes an R2 bucket
func (c *Client) DeleteR2Bucket(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("bucket name is required")
	}

	endpoint := fmt.Sprintf("/accounts/%s/r2/buckets/%s", c.accountID, name)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// CreateTunnelResult contains the result of creating a Tunnel
type CreateTunnelResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

// CreateTunnel creates a new Cloudflare Tunnel
func (c *Client) CreateTunnel(ctx context.Context, name string) (*CreateTunnelResult, error) {
	if name == "" {
		return nil, fmt.Errorf("tunnel name is required")
	}

	body := fmt.Sprintf(`{"name": %q, "tunnel_secret": ""}`, name)
	endpoint := fmt.Sprintf("/accounts/%s/cfd_tunnel", c.accountID)

	result, err := c.RunAPIWithContext(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Secret string `json:"credentials_file,omitempty"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to create tunnel")
	}

	return &CreateTunnelResult{
		ID:     response.Result.ID,
		Name:   response.Result.Name,
		Secret: response.Result.Secret,
	}, nil
}

// DeleteTunnel deletes a Cloudflare Tunnel
func (c *Client) DeleteTunnel(ctx context.Context, tunnelID string) error {
	if tunnelID == "" {
		return fmt.Errorf("tunnel ID is required")
	}

	endpoint := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s", c.accountID, tunnelID)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// CreateDNSRecordOptions contains options for creating a DNS record
type CreateDNSRecordOptions struct {
	ZoneID   string // Zone ID
	Type     string // Record type (A, AAAA, CNAME, MX, TXT, etc.)
	Name     string // Record name
	Content  string // Record content
	TTL      int    // TTL in seconds (1 = auto)
	Proxied  bool   // Whether to proxy through Cloudflare
	Priority int    // Priority (for MX records)
}

// CreateDNSRecordResult contains the result of creating a DNS record
type CreateDNSRecordResult struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// CreateDNSRecord creates a new DNS record
func (c *Client) CreateDNSRecord(ctx context.Context, opts CreateDNSRecordOptions) (*CreateDNSRecordResult, error) {
	if opts.ZoneID == "" {
		return nil, fmt.Errorf("zone ID is required")
	}
	if opts.Type == "" {
		return nil, fmt.Errorf("record type is required")
	}
	if opts.Name == "" {
		return nil, fmt.Errorf("record name is required")
	}
	if opts.Content == "" {
		return nil, fmt.Errorf("record content is required")
	}

	body := map[string]interface{}{
		"type":    opts.Type,
		"name":    opts.Name,
		"content": opts.Content,
		"proxied": opts.Proxied,
	}

	if opts.TTL > 0 {
		body["ttl"] = opts.TTL
	} else {
		body["ttl"] = 1 // Auto
	}

	if opts.Priority > 0 && opts.Type == "MX" {
		body["priority"] = opts.Priority
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("/zones/%s/dns_records", opts.ZoneID)
	result, err := c.RunAPIWithContext(ctx, "POST", endpoint, string(bodyJSON))
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			Proxied bool   `json:"proxied"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to create DNS record")
	}

	return &CreateDNSRecordResult{
		ID:      response.Result.ID,
		Type:    response.Result.Type,
		Name:    response.Result.Name,
		Content: response.Result.Content,
		Proxied: response.Result.Proxied,
	}, nil
}

// UpdateDNSRecord updates an existing DNS record
func (c *Client) UpdateDNSRecord(ctx context.Context, zoneID, recordID string, opts CreateDNSRecordOptions) (*CreateDNSRecordResult, error) {
	if zoneID == "" {
		return nil, fmt.Errorf("zone ID is required")
	}
	if recordID == "" {
		return nil, fmt.Errorf("record ID is required")
	}

	body := map[string]interface{}{
		"type":    opts.Type,
		"name":    opts.Name,
		"content": opts.Content,
		"proxied": opts.Proxied,
	}

	if opts.TTL > 0 {
		body["ttl"] = opts.TTL
	} else {
		body["ttl"] = 1
	}

	if opts.Priority > 0 && opts.Type == "MX" {
		body["priority"] = opts.Priority
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID)
	result, err := c.RunAPIWithContext(ctx, "PUT", endpoint, string(bodyJSON))
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool `json:"success"`
		Result  struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			Proxied bool   `json:"proxied"`
		} `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to update DNS record")
	}

	return &CreateDNSRecordResult{
		ID:      response.Result.ID,
		Type:    response.Result.Type,
		Name:    response.Result.Name,
		Content: response.Result.Content,
		Proxied: response.Result.Proxied,
	}, nil
}

// DeleteDNSRecord deletes a DNS record
func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	if zoneID == "" {
		return fmt.Errorf("zone ID is required")
	}
	if recordID == "" {
		return fmt.Errorf("record ID is required")
	}

	endpoint := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID)
	_, err := c.RunAPIWithContext(ctx, "DELETE", endpoint, "")
	return err
}

// ListZones returns all zones for the account
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	result, err := c.RunAPIWithContext(ctx, "GET", "/zones", "")
	if err != nil {
		return nil, err
	}

	var response struct {
		Success bool   `json:"success"`
		Result  []Zone `json:"result"`
	}

	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to list zones")
	}

	return response.Result, nil
}

// Zone represents a Cloudflare zone
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// GetZoneIDByName finds a zone ID by domain name
func (c *Client) GetZoneIDByName(ctx context.Context, name string) (string, error) {
	zones, err := c.ListZones(ctx)
	if err != nil {
		return "", err
	}

	for _, zone := range zones {
		if strings.EqualFold(zone.Name, name) {
			return zone.ID, nil
		}
	}

	return "", fmt.Errorf("zone not found: %s", name)
}
