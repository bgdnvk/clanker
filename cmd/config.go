package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage clanker configuration",
	Long:  `Configure clanker settings including AI provider and API keys.`,
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize configuration file",
	Long:  `Create a default configuration file in your home directory.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("error finding home directory: %w", err)
		}

		configPath := filepath.Join(home, ".clanker.yaml")

		// Check if config already exists
		if _, err := os.Stat(configPath); err == nil {
			fmt.Printf("Configuration file already exists at %s\n", configPath)
			return nil
		}

		// Create default config
		// TODO: service_keywords were removed from the default config to keep it minimal.
		// If we want keyword-based log routing, reintroduce them under `aws.service_keywords`.
		defaultConfig := `# Clanker Configuration
# Copy this to ~/.clanker.yaml and customize for your setup

# AI Providers Configuration
ai:
  default_provider: openai  # Default AI provider to use
  
  providers:
    bedrock:
      aws_profile: your-aws-profile  # AWS profile for Bedrock API calls
      model: us.anthropic.claude-sonnet-4-20250514-v1:0
      region: us-west-1
    
    openai:
      model: gpt-5
      api_key_env: OPENAI_API_KEY
    
    anthropic:
      model: claude-3-sonnet-20240229
      api_key_env: ANTHROPIC_API_KEY
    
    gemini:
      project_id: your-gcp-project-id
    
    gemini-api:
      model: gemini-2.5-flash
      api_key_env: GEMINI_API_KEY

# Infrastructure Providers Configuration
infra:
  default_environment: dev             # Default environment to use
  default_provider: aws                # Default infrastructure provider
  
  aws:
    environments:
      dev:
        profile: your-dev-profile
        region: us-east-1
        description: Development environment
      stage:
        profile: your-stage-profile
        region: us-east-1
        description: Staging environment
      prod:
        profile: your-prod-profile
        region: us-east-1
        description: Production environment

  gcp:
    project_id: your-gcp-project-id

github:
  token: ""                      # GitHub personal access token (optional for public repos)
  default_repo: your-repo      # Default repository to use
  repos:                         # List of GitHub repositories
    - owner: your-username
      repo: your-infrastructure-repo
      description: Infrastructure repository
    - owner: your-username
      repo: your-services-repo
      description: Services and database schemas
    - owner: your-username
      repo: your-app-repo
      description: Application repository

postgres:
  default_connection: dev  # Default PostgreSQL connection
  connections:               # PostgreSQL connections
    dev:
      host: localhost
      port: 5432
      database: your_dev_db
      username: postgres
      description: Development database
    stage:
      host: your-stage-db.example.com
      port: 5432
      database: your_stage_db
      username: app_user
      description: Staging database

terraform:
  default_workspace: dev  # Default Terraform workspace
  workspaces:               # Terraform workspaces
    dev:
      path: /path/to/your/infrastructure
      description: Development infrastructure
    stage:
      path: /path/to/your/infrastructure
      description: Staging infrastructure

codebase:
  paths:              # Paths to scan for code analysis
    - .
    - /path/to/your/services
    - /path/to/your/infrastructure
  exclude:            # Patterns to exclude
    - node_modules
    - .git
    - vendor
    - __pycache__
    - "*.log"
    - "*.tmp"
    - ".env*"
  max_file_size: 1048576  # Max file size to analyze (1MB)
  max_files: 100          # Max number of files to analyze per query

# General settings
timeout: 30  # Timeout for AI requests in seconds
`

		err = os.WriteFile(configPath, []byte(defaultConfig), 0644)
		if err != nil {
			return fmt.Errorf("error creating config file: %w", err)
		}

		fmt.Printf("Configuration file created at %s\n", configPath)
		fmt.Println("Please edit the file to add your AI provider API key.")
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	Long:  `Display the current configuration settings.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("error finding home directory: %w", err)
		}

		configPath := filepath.Join(home, ".clanker.yaml")

		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			fmt.Println("No configuration file found. Run 'clanker config init' to create one.")
			return nil
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}

		fmt.Printf("Configuration file: %s\n\n", configPath)
		fmt.Print(string(content))
		return nil
	},
}

var configScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan system for available credentials",
	Long: `Detect AWS profiles, GCP projects, Azure subscriptions, Cloudflare, and LLM API keys.

This command scans the local system for available cloud provider credentials
and API keys that can be used with clanker.

Examples:
  clanker config scan
  clanker config scan --output json`,
	RunE: runConfigScan,
}

// ScanResult holds all detected credentials
type ScanResult struct {
	AWS        AWSCredentialsScan        `json:"aws"`
	GCP        GCPCredentialsScan        `json:"gcp"`
	Azure      AzureCredentialsScan      `json:"azure"`
	Cloudflare CloudflareCredentialsScan `json:"cloudflare"`
	LLM        LLMCredentialsScan        `json:"llm"`
}

// AWSCredentialsScan holds detected AWS profiles
type AWSCredentialsScan struct {
	Profiles []AWSProfileInfo `json:"profiles"`
	Error    string           `json:"error,omitempty"`
}

// AWSProfileInfo holds info about a single AWS profile
type AWSProfileInfo struct {
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
	Source string `json:"source"`
}

// GCPCredentialsScan holds detected GCP credentials
type GCPCredentialsScan struct {
	HasADC       bool     `json:"hasADC"`
	ADCPath      string   `json:"adcPath,omitempty"`
	Projects     []string `json:"projects,omitempty"`
	CLIAvailable bool     `json:"cliAvailable"`
	Error        string   `json:"error,omitempty"`
}

// AzureCredentialsScan holds detected Azure subscriptions
type AzureCredentialsScan struct {
	CLIAvailable  bool                    `json:"cliAvailable"`
	Subscriptions []AzureSubscriptionInfo `json:"subscriptions,omitempty"`
	Error         string                  `json:"error,omitempty"`
}

// AzureSubscriptionInfo holds info about an Azure subscription
type AzureSubscriptionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// CloudflareCredentialsScan holds detected Cloudflare credentials
type CloudflareCredentialsScan struct {
	HasToken     bool   `json:"hasToken"`
	HasAccountID bool   `json:"hasAccountId"`
	Error        string `json:"error,omitempty"`
}

// LLMCredentialsScan holds detected LLM API keys
type LLMCredentialsScan struct {
	OpenAI    LLMKeyStatus `json:"openai"`
	Anthropic LLMKeyStatus `json:"anthropic"`
	Gemini    LLMKeyStatus `json:"gemini"`
}

// LLMKeyStatus indicates whether an LLM key was detected
type LLMKeyStatus struct {
	HasKey bool   `json:"hasKey"`
	Error  string `json:"error,omitempty"`
}

func runConfigScan(cmd *cobra.Command, args []string) error {
	outputFormat, _ := cmd.Flags().GetString("output")

	result := ScanResult{
		AWS:        scanAWSProfiles(),
		GCP:        scanGCPCredentials(),
		Azure:      scanAzureSubscriptions(),
		Cloudflare: scanCloudflareCredentials(),
		LLM:        scanLLMKeys(),
	}

	if outputFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	// Pretty print for human consumption
	printScanResult(result)
	return nil
}

func printScanResult(result ScanResult) {
	fmt.Println("=== System Credentials Scan ===")
	fmt.Println()

	// AWS
	fmt.Println("AWS Profiles:")
	if len(result.AWS.Profiles) == 0 {
		fmt.Println("  No profiles detected")
	} else {
		for _, p := range result.AWS.Profiles {
			region := p.Region
			if region == "" {
				region = "(no region)"
			}
			fmt.Printf("  - %s [%s] (%s)\n", p.Name, region, p.Source)
		}
	}
	if result.AWS.Error != "" {
		fmt.Printf("  Error: %s\n", result.AWS.Error)
	}
	fmt.Println()

	// GCP
	fmt.Println("GCP:")
	if result.GCP.HasADC {
		fmt.Printf("  Application Default Credentials: Found at %s\n", result.GCP.ADCPath)
	} else {
		fmt.Println("  Application Default Credentials: Not found")
	}
	fmt.Printf("  gcloud CLI: %v\n", result.GCP.CLIAvailable)
	if len(result.GCP.Projects) > 0 {
		fmt.Printf("  Projects: %s\n", strings.Join(result.GCP.Projects, ", "))
	}
	if result.GCP.Error != "" {
		fmt.Printf("  Error: %s\n", result.GCP.Error)
	}
	fmt.Println()

	// Azure
	fmt.Println("Azure:")
	fmt.Printf("  az CLI: %v\n", result.Azure.CLIAvailable)
	if len(result.Azure.Subscriptions) == 0 {
		fmt.Println("  Subscriptions: None detected")
	} else {
		fmt.Println("  Subscriptions:")
		for _, s := range result.Azure.Subscriptions {
			defaultMark := ""
			if s.IsDefault {
				defaultMark = " (default)"
			}
			fmt.Printf("    - %s (%s)%s\n", s.Name, s.ID, defaultMark)
		}
	}
	if result.Azure.Error != "" {
		fmt.Printf("  Error: %s\n", result.Azure.Error)
	}
	fmt.Println()

	// Cloudflare
	fmt.Println("Cloudflare:")
	fmt.Printf("  API Token (env): %v\n", result.Cloudflare.HasToken)
	fmt.Printf("  Account ID (env): %v\n", result.Cloudflare.HasAccountID)
	fmt.Println()

	// LLM Keys
	fmt.Println("LLM API Keys (from environment):")
	fmt.Printf("  OpenAI: %v\n", result.LLM.OpenAI.HasKey)
	fmt.Printf("  Anthropic: %v\n", result.LLM.Anthropic.HasKey)
	fmt.Printf("  Gemini: %v\n", result.LLM.Gemini.HasKey)
}

func scanAWSProfiles() AWSCredentialsScan {
	result := AWSCredentialsScan{
		Profiles: []AWSProfileInfo{},
	}

	home, err := os.UserHomeDir()
	if err != nil {
		result.Error = "could not determine home directory"
		return result
	}

	credPath := filepath.Join(home, ".aws", "credentials")
	configPath := filepath.Join(home, ".aws", "config")

	credProfiles := parseAWSINIFile(credPath, "credentials")
	configProfiles := parseAWSINIFile(configPath, "config")

	profileMap := make(map[string]*AWSProfileInfo)

	for _, p := range credProfiles {
		profileMap[p.Name] = &AWSProfileInfo{
			Name:   p.Name,
			Region: p.Region,
			Source: p.Source,
		}
	}

	for _, p := range configProfiles {
		if existing, ok := profileMap[p.Name]; ok {
			if existing.Region == "" && p.Region != "" {
				existing.Region = p.Region
			}
		} else {
			profileMap[p.Name] = &AWSProfileInfo{
				Name:   p.Name,
				Region: p.Region,
				Source: p.Source,
			}
		}
	}

	for _, p := range profileMap {
		result.Profiles = append(result.Profiles, *p)
	}

	return result
}

func parseAWSINIFile(path string, source string) []AWSProfileInfo {
	profiles := []AWSProfileInfo{}

	file, err := os.Open(path)
	if err != nil {
		return profiles
	}
	defer file.Close()

	sectionPattern := regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	kvPattern := regexp.MustCompile(`^\s*([^=\s]+)\s*=\s*(.+?)\s*$`)

	var currentProfile *AWSProfileInfo
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()

		if matches := sectionPattern.FindStringSubmatch(line); len(matches) == 2 {
			if currentProfile != nil {
				profiles = append(profiles, *currentProfile)
			}

			sectionName := strings.TrimSpace(matches[1])
			profileName := sectionName

			if source == "config" && strings.HasPrefix(sectionName, "profile ") {
				profileName = strings.TrimPrefix(sectionName, "profile ")
			}

			currentProfile = &AWSProfileInfo{
				Name:   profileName,
				Source: source,
			}
			continue
		}

		if currentProfile != nil {
			if matches := kvPattern.FindStringSubmatch(line); len(matches) == 3 {
				key := strings.ToLower(strings.TrimSpace(matches[1]))
				value := strings.TrimSpace(matches[2])

				if key == "region" {
					currentProfile.Region = value
				}
			}
		}
	}

	if currentProfile != nil {
		profiles = append(profiles, *currentProfile)
	}

	return profiles
}

func scanGCPCredentials() GCPCredentialsScan {
	result := GCPCredentialsScan{
		Projects: []string{},
	}

	home, err := os.UserHomeDir()
	if err != nil {
		result.Error = "could not determine home directory"
		return result
	}

	adcPath := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if _, err := os.Stat(adcPath); err == nil {
		result.HasADC = true
		result.ADCPath = adcPath
	}

	gcloudPath, err := findGcloudBinary()
	if err != nil {
		result.CLIAvailable = false
		return result
	}
	result.CLIAvailable = true

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, gcloudPath, "config", "get-value", "project")
	output, err := cmd.Output()
	if err == nil {
		project := strings.TrimSpace(string(output))
		if project != "" && project != "(unset)" {
			result.Projects = append(result.Projects, project)
		}
	}

	cmd = exec.CommandContext(ctx, gcloudPath, "config", "configurations", "list", "--format=json")
	output, err = cmd.Output()
	if err == nil {
		var configs []struct {
			Name       string `json:"name"`
			IsActive   bool   `json:"is_active"`
			Properties struct {
				Core struct {
					Project string `json:"project"`
				} `json:"core"`
			} `json:"properties"`
		}
		if json.Unmarshal(output, &configs) == nil {
			for _, cfg := range configs {
				if cfg.Properties.Core.Project != "" {
					found := false
					for _, p := range result.Projects {
						if p == cfg.Properties.Core.Project {
							found = true
							break
						}
					}
					if !found {
						result.Projects = append(result.Projects, cfg.Properties.Core.Project)
					}
				}
			}
		}
	}

	return result
}

func findGcloudBinary() (string, error) {
	names := []string{"gcloud"}
	if runtime.GOOS == "windows" {
		names = []string{"gcloud.cmd", "gcloud.exe", "gcloud"}
	}

	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	home, _ := os.UserHomeDir()
	var candidates []string

	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/opt/homebrew/bin/gcloud",
			"/usr/local/bin/gcloud",
		}
	case "linux":
		candidates = []string{
			"/usr/bin/gcloud",
			"/usr/local/bin/gcloud",
			"/snap/bin/gcloud",
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "google-cloud-sdk", "bin", "gcloud"))
		}
	case "windows":
		programFiles := os.Getenv("ProgramFiles")
		programFilesX86 := os.Getenv("ProgramFiles(x86)")
		if programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if programFilesX86 != "" {
			candidates = append(candidates, filepath.Join(programFilesX86, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "AppData", "Local", "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}

	return "", os.ErrNotExist
}

func scanAzureSubscriptions() AzureCredentialsScan {
	result := AzureCredentialsScan{
		Subscriptions: []AzureSubscriptionInfo{},
	}

	azPath, err := findAzureCLI()
	if err != nil {
		result.CLIAvailable = false
		return result
	}
	result.CLIAvailable = true

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, azPath, "account", "list", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		result.Error = "failed to list subscriptions (may need az login)"
		return result
	}

	var subs []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		State     string `json:"state"`
		IsDefault bool   `json:"isDefault"`
	}

	if json.Unmarshal(output, &subs) != nil {
		result.Error = "failed to parse subscription list"
		return result
	}

	for _, sub := range subs {
		result.Subscriptions = append(result.Subscriptions, AzureSubscriptionInfo{
			ID:        sub.ID,
			Name:      sub.Name,
			State:     sub.State,
			IsDefault: sub.IsDefault,
		})
	}

	return result
}

func findAzureCLI() (string, error) {
	names := []string{"az"}
	if runtime.GOOS == "windows" {
		names = []string{"az.cmd", "az.exe", "az"}
	}

	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	home, _ := os.UserHomeDir()
	var candidates []string

	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/opt/homebrew/bin/az",
			"/usr/local/bin/az",
		}
	case "linux":
		candidates = []string{
			"/usr/bin/az",
			"/usr/local/bin/az",
		}
	case "windows":
		programFiles := os.Getenv("ProgramFiles")
		programFilesX86 := os.Getenv("ProgramFiles(x86)")
		if programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Microsoft SDKs", "Azure", "CLI2", "wbin", "az.cmd"))
		}
		if programFilesX86 != "" {
			candidates = append(candidates, filepath.Join(programFilesX86, "Microsoft SDKs", "Azure", "CLI2", "wbin", "az.cmd"))
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "AppData", "Local", "Programs", "Azure CLI", "az.cmd"))
		}
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}

	return "", os.ErrNotExist
}

func scanCloudflareCredentials() CloudflareCredentialsScan {
	return CloudflareCredentialsScan{
		HasToken:     os.Getenv("CLOUDFLARE_API_TOKEN") != "",
		HasAccountID: os.Getenv("CLOUDFLARE_ACCOUNT_ID") != "",
	}
}

func scanLLMKeys() LLMCredentialsScan {
	return LLMCredentialsScan{
		OpenAI:    LLMKeyStatus{HasKey: os.Getenv("OPENAI_API_KEY") != ""},
		Anthropic: LLMKeyStatus{HasKey: os.Getenv("ANTHROPIC_API_KEY") != ""},
		Gemini:    LLMKeyStatus{HasKey: os.Getenv("GEMINI_API_KEY") != ""},
	}
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configScanCmd)

	configScanCmd.Flags().StringP("output", "o", "", "Output format (json for JSON output)")
}
