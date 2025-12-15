package cmd

import (
	"fmt"
	"os"
	"path/filepath"

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
      model: gemini-pro
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

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configShowCmd)
}
