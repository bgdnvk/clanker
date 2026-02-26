package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/spf13/cobra"
)

// Cloudflare deploy command flags
var (
	cfDeployAccountID string
	cfDeployAPIToken  string
	cfDeployDebug     bool
)

var cfDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy resources to Cloudflare",
	Long: `Deploy Workers, Pages, and other resources to Cloudflare.

Examples:
  clanker cf deploy worker --name my-worker --script ./worker.js
  clanker cf deploy pages --project my-site --directory ./dist
  clanker cf deploy kv --name my-namespace
  clanker cf deploy d1 --name my-database
  clanker cf deploy r2 --name my-bucket
  clanker cf deploy tunnel --name my-tunnel
  clanker cf deploy dns --zone example.com --type A --name www --content 1.2.3.4`,
}

// Worker deploy command
var cfDeployWorkerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Deploy a Cloudflare Worker",
	Long: `Deploy a Cloudflare Worker script.

Examples:
  clanker cf deploy worker --name my-worker --script ./worker.js
  clanker cf deploy worker --name my-api --script ./api.js --route "example.com/*"
  clanker cf deploy worker --name my-worker --script ./worker.js --kv MY_KV=namespace-id`,
	RunE: runCfDeployWorker,
}

var (
	cfWorkerName        string
	cfWorkerScript      string
	cfWorkerCompat      string
	cfWorkerRoutes      []string
	cfWorkerKV          []string
	cfWorkerR2          []string
	cfWorkerD1          []string
	cfWorkerEnv         []string
)

func init() {
	// Add deploy command to cf command
	// This will be called from root.go

	// Common flags for all deploy subcommands
	cfDeployCmd.PersistentFlags().StringVar(&cfDeployAccountID, "account-id", "", "Cloudflare account ID")
	cfDeployCmd.PersistentFlags().StringVar(&cfDeployAPIToken, "api-token", "", "Cloudflare API token")
	cfDeployCmd.PersistentFlags().BoolVar(&cfDeployDebug, "debug", false, "Enable debug output")

	// Worker deploy flags
	cfDeployWorkerCmd.Flags().StringVar(&cfWorkerName, "name", "", "Worker name (required)")
	cfDeployWorkerCmd.Flags().StringVar(&cfWorkerScript, "script", "", "Path to worker script file (required)")
	cfDeployWorkerCmd.Flags().StringVar(&cfWorkerCompat, "compatibility-date", "", "Compatibility date (e.g., 2024-01-01)")
	cfDeployWorkerCmd.Flags().StringSliceVar(&cfWorkerRoutes, "route", nil, "Worker routes (can specify multiple)")
	cfDeployWorkerCmd.Flags().StringSliceVar(&cfWorkerKV, "kv", nil, "KV namespace bindings (BINDING=namespace-id)")
	cfDeployWorkerCmd.Flags().StringSliceVar(&cfWorkerR2, "r2", nil, "R2 bucket bindings (BINDING=bucket-name)")
	cfDeployWorkerCmd.Flags().StringSliceVar(&cfWorkerD1, "d1", nil, "D1 database bindings (BINDING=database-id)")
	cfDeployWorkerCmd.Flags().StringSliceVar(&cfWorkerEnv, "env", nil, "Environment variables (KEY=value)")
	cfDeployWorkerCmd.MarkFlagRequired("name")
	cfDeployWorkerCmd.MarkFlagRequired("script")

	// Pages deploy flags
	cfDeployPagesCmd.Flags().StringVar(&cfPagesProject, "project", "", "Pages project name (required)")
	cfDeployPagesCmd.Flags().StringVar(&cfPagesDirectory, "directory", "", "Directory containing built assets (required)")
	cfDeployPagesCmd.Flags().StringVar(&cfPagesBranch, "branch", "", "Branch name")
	cfDeployPagesCmd.Flags().StringVar(&cfPagesCommitHash, "commit-hash", "", "Commit hash")
	cfDeployPagesCmd.Flags().StringVar(&cfPagesCommitMsg, "commit-message", "", "Commit message")
	cfDeployPagesCmd.MarkFlagRequired("project")
	cfDeployPagesCmd.MarkFlagRequired("directory")

	// Pages create flags
	cfCreatePagesCmd.Flags().StringVar(&cfPagesCreateName, "name", "", "Project name (required)")
	cfCreatePagesCmd.Flags().StringVar(&cfPagesCreateBranch, "production-branch", "main", "Production branch")
	cfCreatePagesCmd.Flags().StringVar(&cfPagesCreateBuildCmd, "build-command", "", "Build command")
	cfCreatePagesCmd.Flags().StringVar(&cfPagesCreateBuildDir, "build-directory", "", "Build output directory")
	cfCreatePagesCmd.MarkFlagRequired("name")

	// KV create flags
	cfCreateKVCmd.Flags().StringVar(&cfKVName, "name", "", "KV namespace name (required)")
	cfCreateKVCmd.MarkFlagRequired("name")

	// D1 create flags
	cfCreateD1Cmd.Flags().StringVar(&cfD1Name, "name", "", "D1 database name (required)")
	cfCreateD1Cmd.MarkFlagRequired("name")

	// R2 create flags
	cfCreateR2Cmd.Flags().StringVar(&cfR2Name, "name", "", "R2 bucket name (required)")
	cfCreateR2Cmd.Flags().StringVar(&cfR2Location, "location", "", "Location hint (e.g., wnam, enam, weur, eeur, apac)")
	cfCreateR2Cmd.MarkFlagRequired("name")

	// Tunnel create flags
	cfCreateTunnelCmd.Flags().StringVar(&cfTunnelName, "name", "", "Tunnel name (required)")
	cfCreateTunnelCmd.MarkFlagRequired("name")

	// DNS create flags
	cfCreateDNSCmd.Flags().StringVar(&cfDNSZone, "zone", "", "Zone name or ID (required)")
	cfCreateDNSCmd.Flags().StringVar(&cfDNSType, "type", "", "Record type: A, AAAA, CNAME, MX, TXT, etc. (required)")
	cfCreateDNSCmd.Flags().StringVar(&cfDNSName, "name", "", "Record name (required)")
	cfCreateDNSCmd.Flags().StringVar(&cfDNSContent, "content", "", "Record content (required)")
	cfCreateDNSCmd.Flags().IntVar(&cfDNSTTL, "ttl", 1, "TTL in seconds (1 = auto)")
	cfCreateDNSCmd.Flags().BoolVar(&cfDNSProxied, "proxied", false, "Proxy through Cloudflare")
	cfCreateDNSCmd.Flags().IntVar(&cfDNSPriority, "priority", 0, "Priority (for MX records)")
	cfCreateDNSCmd.MarkFlagRequired("zone")
	cfCreateDNSCmd.MarkFlagRequired("type")
	cfCreateDNSCmd.MarkFlagRequired("name")
	cfCreateDNSCmd.MarkFlagRequired("content")

	// Delete flags
	cfDeleteWorkerCmd.Flags().StringVar(&cfDeleteName, "name", "", "Worker name (required)")
	cfDeleteWorkerCmd.MarkFlagRequired("name")

	cfDeletePagesCmd.Flags().StringVar(&cfDeleteName, "name", "", "Pages project name (required)")
	cfDeletePagesCmd.MarkFlagRequired("name")

	cfDeleteKVCmd.Flags().StringVar(&cfDeleteID, "id", "", "KV namespace ID (required)")
	cfDeleteKVCmd.MarkFlagRequired("id")

	cfDeleteD1Cmd.Flags().StringVar(&cfDeleteID, "id", "", "D1 database ID (required)")
	cfDeleteD1Cmd.MarkFlagRequired("id")

	cfDeleteR2Cmd.Flags().StringVar(&cfDeleteName, "name", "", "R2 bucket name (required)")
	cfDeleteR2Cmd.MarkFlagRequired("name")

	cfDeleteTunnelCmd.Flags().StringVar(&cfDeleteID, "id", "", "Tunnel ID (required)")
	cfDeleteTunnelCmd.MarkFlagRequired("id")

	cfDeleteDNSCmd.Flags().StringVar(&cfDNSZone, "zone", "", "Zone name or ID (required)")
	cfDeleteDNSCmd.Flags().StringVar(&cfDeleteID, "id", "", "DNS record ID (required)")
	cfDeleteDNSCmd.MarkFlagRequired("zone")
	cfDeleteDNSCmd.MarkFlagRequired("id")

	// Add subcommands to deploy
	cfDeployCmd.AddCommand(cfDeployWorkerCmd)
	cfDeployCmd.AddCommand(cfDeployPagesCmd)

	// Add create subcommands
	cfCreateCmd.AddCommand(cfCreatePagesCmd)
	cfCreateCmd.AddCommand(cfCreateKVCmd)
	cfCreateCmd.AddCommand(cfCreateD1Cmd)
	cfCreateCmd.AddCommand(cfCreateR2Cmd)
	cfCreateCmd.AddCommand(cfCreateTunnelCmd)
	cfCreateCmd.AddCommand(cfCreateDNSCmd)

	// Add delete subcommands
	cfDeleteCmd.AddCommand(cfDeleteWorkerCmd)
	cfDeleteCmd.AddCommand(cfDeletePagesCmd)
	cfDeleteCmd.AddCommand(cfDeleteKVCmd)
	cfDeleteCmd.AddCommand(cfDeleteD1Cmd)
	cfDeleteCmd.AddCommand(cfDeleteR2Cmd)
	cfDeleteCmd.AddCommand(cfDeleteTunnelCmd)
	cfDeleteCmd.AddCommand(cfDeleteDNSCmd)
}

// AddCfDeployCommands adds all deploy-related commands to the cf command
func AddCfDeployCommands(cfCmd *cobra.Command) {
	cfCmd.AddCommand(cfDeployCmd)
	cfCmd.AddCommand(cfCreateCmd)
	cfCmd.AddCommand(cfDeleteCmd)
}

func getCfClient() (*cloudflare.Client, error) {
	accountID := cfDeployAccountID
	if accountID == "" {
		accountID = cloudflare.ResolveAccountID()
	}

	apiToken := cfDeployAPIToken
	if apiToken == "" {
		apiToken = cloudflare.ResolveAPIToken()
	}

	if apiToken == "" {
		return nil, fmt.Errorf("cloudflare API token is required (set via --api-token, CLOUDFLARE_API_TOKEN, or cloudflare.api_token in config)")
	}

	return cloudflare.NewClient(accountID, apiToken, cfDeployDebug)
}

func runCfDeployWorker(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	opts := cloudflare.DeployWorkerOptions{
		Name:          cfWorkerName,
		ScriptPath:    cfWorkerScript,
		Compatibility: cfWorkerCompat,
		Routes:        cfWorkerRoutes,
		Bindings:      make(map[string]string),
	}

	// Parse environment variables
	for _, env := range cfWorkerEnv {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			opts.Bindings[parts[0]] = parts[1]
		}
	}

	// Parse KV bindings
	for _, kv := range cfWorkerKV {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			opts.KVNamespaces = append(opts.KVNamespaces, cloudflare.KVBinding{
				Name:        parts[0],
				NamespaceID: parts[1],
			})
		}
	}

	// Parse R2 bindings
	for _, r2 := range cfWorkerR2 {
		parts := strings.SplitN(r2, "=", 2)
		if len(parts) == 2 {
			opts.R2Buckets = append(opts.R2Buckets, cloudflare.R2Binding{
				Name:       parts[0],
				BucketName: parts[1],
			})
		}
	}

	// Parse D1 bindings
	for _, d1 := range cfWorkerD1 {
		parts := strings.SplitN(d1, "=", 2)
		if len(parts) == 2 {
			opts.D1Databases = append(opts.D1Databases, cloudflare.D1Binding{
				Name:       parts[0],
				DatabaseID: parts[1],
			})
		}
	}

	fmt.Printf("Deploying Worker '%s'...\n", cfWorkerName)

	result, err := client.DeployWorker(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to deploy worker: %w", err)
	}

	fmt.Printf("Worker deployed successfully!\n")
	if result.ScriptURL != "" {
		fmt.Printf("URL: %s\n", result.ScriptURL)
	}
	if result.ID != "" {
		fmt.Printf("ID: %s\n", result.ID)
	}

	return nil
}

// Pages deploy command
var cfDeployPagesCmd = &cobra.Command{
	Use:   "pages",
	Short: "Deploy to Cloudflare Pages",
	Long: `Deploy assets to a Cloudflare Pages project.

Examples:
  clanker cf deploy pages --project my-site --directory ./dist
  clanker cf deploy pages --project my-site --directory ./build --branch main`,
	RunE: runCfDeployPages,
}

var (
	cfPagesProject    string
	cfPagesDirectory  string
	cfPagesBranch     string
	cfPagesCommitHash string
	cfPagesCommitMsg  string
)

func runCfDeployPages(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	opts := cloudflare.DeployPagesOptions{
		ProjectName: cfPagesProject,
		Directory:   cfPagesDirectory,
		Branch:      cfPagesBranch,
		CommitHash:  cfPagesCommitHash,
		CommitMsg:   cfPagesCommitMsg,
	}

	fmt.Printf("Deploying to Pages project '%s'...\n", cfPagesProject)

	result, err := client.DeployPages(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to deploy pages: %w", err)
	}

	fmt.Printf("Pages deployed successfully!\n")
	if result.URL != "" {
		fmt.Printf("URL: %s\n", result.URL)
	}

	return nil
}

// Create commands
var cfCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create Cloudflare resources",
	Long: `Create Cloudflare resources like Pages projects, KV namespaces, D1 databases, R2 buckets, and Tunnels.

Examples:
  clanker cf create pages --name my-site
  clanker cf create kv --name my-namespace
  clanker cf create d1 --name my-database
  clanker cf create r2 --name my-bucket
  clanker cf create tunnel --name my-tunnel
  clanker cf create dns --zone example.com --type A --name www --content 1.2.3.4`,
}

var cfCreatePagesCmd = &cobra.Command{
	Use:   "pages",
	Short: "Create a Cloudflare Pages project",
	RunE:  runCfCreatePages,
}

var (
	cfPagesCreateName     string
	cfPagesCreateBranch   string
	cfPagesCreateBuildCmd string
	cfPagesCreateBuildDir string
)

func runCfCreatePages(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	opts := cloudflare.CreatePagesProjectOptions{
		Name:             cfPagesCreateName,
		ProductionBranch: cfPagesCreateBranch,
		BuildCommand:     cfPagesCreateBuildCmd,
		BuildDirectory:   cfPagesCreateBuildDir,
	}

	fmt.Printf("Creating Pages project '%s'...\n", cfPagesCreateName)

	result, err := client.CreatePagesProject(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to create pages project: %w", err)
	}

	fmt.Printf("Pages project created successfully!\n")
	fmt.Printf("ID: %s\n", result.ID)
	fmt.Printf("Name: %s\n", result.Name)
	fmt.Printf("Subdomain: %s.pages.dev\n", result.Subdomain)

	return nil
}

var cfCreateKVCmd = &cobra.Command{
	Use:   "kv",
	Short: "Create a Workers KV namespace",
	RunE:  runCfCreateKV,
}

var cfKVName string

func runCfCreateKV(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Creating KV namespace '%s'...\n", cfKVName)

	result, err := client.CreateKVNamespace(ctx, cfKVName)
	if err != nil {
		return fmt.Errorf("failed to create KV namespace: %w", err)
	}

	fmt.Printf("KV namespace created successfully!\n")
	fmt.Printf("ID: %s\n", result.ID)
	fmt.Printf("Title: %s\n", result.Title)

	return nil
}

var cfCreateD1Cmd = &cobra.Command{
	Use:   "d1",
	Short: "Create a D1 database",
	RunE:  runCfCreateD1,
}

var cfD1Name string

func runCfCreateD1(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Creating D1 database '%s'...\n", cfD1Name)

	result, err := client.CreateD1Database(ctx, cfD1Name)
	if err != nil {
		return fmt.Errorf("failed to create D1 database: %w", err)
	}

	fmt.Printf("D1 database created successfully!\n")
	fmt.Printf("UUID: %s\n", result.UUID)
	fmt.Printf("Name: %s\n", result.Name)

	return nil
}

var cfCreateR2Cmd = &cobra.Command{
	Use:   "r2",
	Short: "Create an R2 bucket",
	RunE:  runCfCreateR2,
}

var (
	cfR2Name     string
	cfR2Location string
)

func runCfCreateR2(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Creating R2 bucket '%s'...\n", cfR2Name)

	result, err := client.CreateR2Bucket(ctx, cfR2Name, cfR2Location)
	if err != nil {
		return fmt.Errorf("failed to create R2 bucket: %w", err)
	}

	fmt.Printf("R2 bucket created successfully!\n")
	fmt.Printf("Name: %s\n", result.Name)
	if result.Location != "" {
		fmt.Printf("Location: %s\n", result.Location)
	}

	return nil
}

var cfCreateTunnelCmd = &cobra.Command{
	Use:   "tunnel",
	Short: "Create a Cloudflare Tunnel",
	RunE:  runCfCreateTunnel,
}

var cfTunnelName string

func runCfCreateTunnel(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Creating Tunnel '%s'...\n", cfTunnelName)

	result, err := client.CreateTunnel(ctx, cfTunnelName)
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

	fmt.Printf("Tunnel created successfully!\n")
	fmt.Printf("ID: %s\n", result.ID)
	fmt.Printf("Name: %s\n", result.Name)

	return nil
}

var cfCreateDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "Create a DNS record",
	RunE:  runCfCreateDNS,
}

var (
	cfDNSZone     string
	cfDNSType     string
	cfDNSName     string
	cfDNSContent  string
	cfDNSTTL      int
	cfDNSProxied  bool
	cfDNSPriority int
)

func runCfCreateDNS(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Resolve zone ID if name was provided
	zoneID := cfDNSZone
	if !strings.Contains(zoneID, "-") { // Likely a domain name, not an ID
		id, err := client.GetZoneIDByName(ctx, zoneID)
		if err != nil {
			return fmt.Errorf("failed to find zone: %w", err)
		}
		zoneID = id
	}

	opts := cloudflare.CreateDNSRecordOptions{
		ZoneID:   zoneID,
		Type:     strings.ToUpper(cfDNSType),
		Name:     cfDNSName,
		Content:  cfDNSContent,
		TTL:      cfDNSTTL,
		Proxied:  cfDNSProxied,
		Priority: cfDNSPriority,
	}

	fmt.Printf("Creating DNS record %s %s -> %s...\n", opts.Type, opts.Name, opts.Content)

	result, err := client.CreateDNSRecord(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to create DNS record: %w", err)
	}

	fmt.Printf("DNS record created successfully!\n")
	fmt.Printf("ID: %s\n", result.ID)
	fmt.Printf("Type: %s\n", result.Type)
	fmt.Printf("Name: %s\n", result.Name)
	fmt.Printf("Content: %s\n", result.Content)
	fmt.Printf("Proxied: %v\n", result.Proxied)

	return nil
}

// Delete commands
var cfDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete Cloudflare resources",
	Long: `Delete Cloudflare resources.

Examples:
  clanker cf delete worker --name my-worker
  clanker cf delete pages --name my-site
  clanker cf delete kv --id namespace-id
  clanker cf delete d1 --id database-id
  clanker cf delete r2 --name my-bucket
  clanker cf delete tunnel --id tunnel-id
  clanker cf delete dns --zone example.com --id record-id`,
}

var (
	cfDeleteName string
	cfDeleteID   string
)

var cfDeleteWorkerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Delete a Cloudflare Worker",
	RunE:  runCfDeleteWorker,
}

func runCfDeleteWorker(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Deleting Worker '%s'...\n", cfDeleteName)

	if err := client.DeleteWorker(ctx, cfDeleteName); err != nil {
		return fmt.Errorf("failed to delete worker: %w", err)
	}

	fmt.Printf("Worker deleted successfully!\n")
	return nil
}

var cfDeletePagesCmd = &cobra.Command{
	Use:   "pages",
	Short: "Delete a Cloudflare Pages project",
	RunE:  runCfDeletePages,
}

func runCfDeletePages(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Deleting Pages project '%s'...\n", cfDeleteName)

	if err := client.DeletePagesProject(ctx, cfDeleteName); err != nil {
		return fmt.Errorf("failed to delete pages project: %w", err)
	}

	fmt.Printf("Pages project deleted successfully!\n")
	return nil
}

var cfDeleteKVCmd = &cobra.Command{
	Use:   "kv",
	Short: "Delete a Workers KV namespace",
	RunE:  runCfDeleteKV,
}

func runCfDeleteKV(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Deleting KV namespace '%s'...\n", cfDeleteID)

	if err := client.DeleteKVNamespace(ctx, cfDeleteID); err != nil {
		return fmt.Errorf("failed to delete KV namespace: %w", err)
	}

	fmt.Printf("KV namespace deleted successfully!\n")
	return nil
}

var cfDeleteD1Cmd = &cobra.Command{
	Use:   "d1",
	Short: "Delete a D1 database",
	RunE:  runCfDeleteD1,
}

func runCfDeleteD1(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Deleting D1 database '%s'...\n", cfDeleteID)

	if err := client.DeleteD1Database(ctx, cfDeleteID); err != nil {
		return fmt.Errorf("failed to delete D1 database: %w", err)
	}

	fmt.Printf("D1 database deleted successfully!\n")
	return nil
}

var cfDeleteR2Cmd = &cobra.Command{
	Use:   "r2",
	Short: "Delete an R2 bucket",
	RunE:  runCfDeleteR2,
}

func runCfDeleteR2(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Deleting R2 bucket '%s'...\n", cfDeleteName)

	if err := client.DeleteR2Bucket(ctx, cfDeleteName); err != nil {
		return fmt.Errorf("failed to delete R2 bucket: %w", err)
	}

	fmt.Printf("R2 bucket deleted successfully!\n")
	return nil
}

var cfDeleteTunnelCmd = &cobra.Command{
	Use:   "tunnel",
	Short: "Delete a Cloudflare Tunnel",
	RunE:  runCfDeleteTunnel,
}

func runCfDeleteTunnel(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("Deleting Tunnel '%s'...\n", cfDeleteID)

	if err := client.DeleteTunnel(ctx, cfDeleteID); err != nil {
		return fmt.Errorf("failed to delete tunnel: %w", err)
	}

	fmt.Printf("Tunnel deleted successfully!\n")
	return nil
}

var cfDeleteDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "Delete a DNS record",
	RunE:  runCfDeleteDNS,
}

func runCfDeleteDNS(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Resolve zone ID if name was provided
	zoneID := cfDNSZone
	if !strings.Contains(zoneID, "-") {
		id, err := client.GetZoneIDByName(ctx, zoneID)
		if err != nil {
			return fmt.Errorf("failed to find zone: %w", err)
		}
		zoneID = id
	}

	fmt.Printf("Deleting DNS record '%s'...\n", cfDeleteID)

	if err := client.DeleteDNSRecord(ctx, zoneID, cfDeleteID); err != nil {
		return fmt.Errorf("failed to delete DNS record: %w", err)
	}

	fmt.Printf("DNS record deleted successfully!\n")
	return nil
}

// List commands for resource discovery
var cfListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Cloudflare resources",
	Long: `List Cloudflare resources.

Examples:
  clanker cf list zones
  clanker cf list workers
  clanker cf list kv
  clanker cf list d1
  clanker cf list r2
  clanker cf list tunnels
  clanker cf list pages`,
}

var cfListZonesCmd = &cobra.Command{
	Use:   "zones",
	Short: "List all zones",
	RunE:  runCfListZones,
}

func runCfListZones(cmd *cobra.Command, args []string) error {
	client, err := getCfClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	zones, err := client.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("failed to list zones: %w", err)
	}

	if len(zones) == 0 {
		fmt.Println("No zones found.")
		return nil
	}

	output, _ := json.MarshalIndent(zones, "", "  ")
	fmt.Println(string(output))
	return nil
}

// AddCfListCommands adds list commands to the cf command
func AddCfListCommands(cfCmd *cobra.Command) {
	cfListCmd.AddCommand(cfListZonesCmd)
	cfCmd.AddCommand(cfListCmd)
}
