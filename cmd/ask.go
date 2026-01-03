package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/aws"
	ghclient "github.com/bgdnvk/clanker/internal/github"
	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/maker"
	tfclient "github.com/bgdnvk/clanker/internal/terraform"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// askCmd represents the ask command
const defaultGeminiModel = "gemini-3-pro-preview"

var askCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask AI about your AWS infrastructure or GitHub repository",
	Long: `Ask natural language questions about your AWS infrastructure or GitHub repository.
	
Examples:
  clanker ask "What EC2 instances are running?"
  clanker ask "Show me lambda functions with high error rates"
  clanker ask "What's the current RDS instance status?"
  clanker ask "Show me GitHub Actions workflow status"
  clanker ask "What pull requests are open?"`,
	Args: func(cmd *cobra.Command, args []string) error {
		apply, _ := cmd.Flags().GetBool("apply")
		if apply {
			return nil
		}
		if len(args) < 1 {
			return fmt.Errorf("requires a question")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		question := ""
		if len(args) > 0 {
			question = args[0]
		}

		// Get context from flags
		includeAWS, _ := cmd.Flags().GetBool("aws")
		includeGitHub, _ := cmd.Flags().GetBool("github")
		includeTerraform, _ := cmd.Flags().GetBool("terraform")
		debug := viper.GetBool("debug")
		discovery, _ := cmd.Flags().GetBool("discovery")
		compliance, _ := cmd.Flags().GetBool("compliance")
		profile, _ := cmd.Flags().GetString("profile")
		workspace, _ := cmd.Flags().GetString("workspace")
		aiProfile, _ := cmd.Flags().GetString("ai-profile")
		openaiKey, _ := cmd.Flags().GetString("openai-key")
		anthropicKey, _ := cmd.Flags().GetString("anthropic-key")
		geminiKey, _ := cmd.Flags().GetString("gemini-key")
		geminiModel, _ := cmd.Flags().GetString("gemini-model")
		makerMode, _ := cmd.Flags().GetBool("maker")
		applyMode, _ := cmd.Flags().GetBool("apply")
		planFile, _ := cmd.Flags().GetString("plan-file")
		destroyer, _ := cmd.Flags().GetBool("destroyer")
		agentTrace, _ := cmd.Flags().GetBool("agent-trace")
		if cmd.Flags().Changed("agent-trace") {
			viper.Set("agent.trace", agentTrace)
		}

		if makerMode {
			ctx := context.Background()

			// Resolve provider the same way as normal ask.
			var provider string
			if aiProfile != "" {
				provider = aiProfile
			} else {
				provider = viper.GetString("ai.default_provider")
				if provider == "" {
					provider = "openai"
				}
			}

			maybeOverrideGeminiModel(provider, geminiModel)

			// Resolve API key based on provider.
			var apiKey string
			switch provider {
			case "gemini":
				apiKey = ""
			case "gemini-api":
				apiKey = resolveGeminiAPIKey(geminiKey)
			case "openai":
				apiKey = resolveOpenAIKey(openaiKey)
			case "anthropic":
				if anthropicKey != "" {
					apiKey = anthropicKey
				} else {
					apiKey = viper.GetString("ai.providers.anthropic.api_key_env")
				}
			default:
				apiKey = viper.GetString("ai.api_key")
			}

			if applyMode {
				var rawPlan string
				if planFile != "" {
					data, err := os.ReadFile(planFile)
					if err != nil {
						return fmt.Errorf("failed to read plan file: %w", err)
					}
					rawPlan = string(data)
				} else {
					data, err := io.ReadAll(os.Stdin)
					if err != nil {
						return fmt.Errorf("failed to read plan from stdin: %w", err)
					}
					rawPlan = string(data)
				}

				plan, err := maker.ParsePlan(rawPlan)
				if err != nil {
					return fmt.Errorf("invalid plan: %w", err)
				}

				// Resolve AWS profile/region for execution.
				targetProfile := profile
				if targetProfile == "" {
					defaultEnv := viper.GetString("infra.default_environment")
					if defaultEnv == "" {
						defaultEnv = "dev"
					}
					targetProfile = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
					if targetProfile == "" {
						targetProfile = viper.GetString("aws.default_profile")
					}
					if targetProfile == "" {
						targetProfile = "default"
					}
				}

				region := ""
				if envRegion := strings.TrimSpace(os.Getenv("AWS_REGION")); envRegion != "" {
					region = envRegion
				} else if envRegion := strings.TrimSpace(os.Getenv("AWS_DEFAULT_REGION")); envRegion != "" {
					region = envRegion
				} else {
					// Prefer the profile's configured region so maker apply and infra analysis query the same region.
					cmd := exec.CommandContext(ctx, "aws", "configure", "get", "region", "--profile", targetProfile)
					if out, err := cmd.CombinedOutput(); err == nil {
						region = strings.TrimSpace(string(out))
					}
				}
				if region == "" {
					region = ai.FindInfraAnalysisRegion()
				}
				if region == "" {
					region = "us-east-1"
				}

				return maker.ExecutePlan(ctx, plan, maker.ExecOptions{
					Profile:    targetProfile,
					Region:     region,
					Writer:     os.Stdout,
					Destroyer:  destroyer,
					AIProvider: provider,
					AIAPIKey:   apiKey,
					AIProfile:  aiProfile,
					Debug:      debug,
				})
			}

			if strings.TrimSpace(question) == "" {
				return fmt.Errorf("requires a question")
			}

			aiClient := ai.NewClient(provider, apiKey, debug, aiProfile)
			prompt := maker.PlanPromptWithMode(question, destroyer)
			resp, err := aiClient.AskPrompt(ctx, prompt)
			if err != nil {
				return err
			}

			cleaned := aiClient.CleanJSONResponse(resp)
			plan, err := maker.ParsePlan(cleaned)
			if err != nil {
				return fmt.Errorf("failed to parse maker plan: %w", err)
			}

			// Resolve AWS profile/region for planning-time dependency expansion.
			targetProfile := profile
			if targetProfile == "" {
				defaultEnv := viper.GetString("infra.default_environment")
				if defaultEnv == "" {
					defaultEnv = "dev"
				}
				targetProfile = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
				if targetProfile == "" {
					targetProfile = viper.GetString("aws.default_profile")
				}
				if targetProfile == "" {
					targetProfile = "default"
				}
			}

			region := ""
			if envRegion := strings.TrimSpace(os.Getenv("AWS_REGION")); envRegion != "" {
				region = envRegion
			} else if envRegion := strings.TrimSpace(os.Getenv("AWS_DEFAULT_REGION")); envRegion != "" {
				region = envRegion
			} else {
				cmd := exec.CommandContext(ctx, "aws", "configure", "get", "region", "--profile", targetProfile)
				if out, err := cmd.CombinedOutput(); err == nil {
					region = strings.TrimSpace(string(out))
				}
			}
			if region == "" {
				region = ai.FindInfraAnalysisRegion()
			}
			if region == "" {
				region = "us-east-1"
			}

			_ = maker.EnrichPlan(ctx, plan, maker.ExecOptions{Profile: targetProfile, Region: region, Writer: io.Discard, Destroyer: destroyer})

			if plan.CreatedAt.IsZero() {
				plan.CreatedAt = time.Now().UTC()
			}
			plan.Question = question
			if plan.Version == 0 {
				plan.Version = maker.CurrentPlanVersion
			}

			out, err := json.MarshalIndent(plan, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		}

		// Compliance mode enables comprehensive service discovery with specific formatting
		if compliance {
			includeAWS = true
			includeTerraform = true
			discovery = true // Enable full discovery for comprehensive compliance data
			question = `Generate a comprehensive SSP (System Security Plan) compliance report "Services, Ports, and Protocols". 

Create a detailed table with the following columns exactly as specified:
- Reference # (sequential numbering)
- System (service name)
- Vendor (AWS, or specific vendor if applicable)
- Port (specific port numbers used)
- Protocol (TCP, UDP, HTTPS, etc.)
- External IP Address (public IPs, DNS names, or "Internal" if private)
- Description (detailed purpose and function)
- Hosting Environment (AWS region, VPC, or specific environment details)
- Risk/Impact/Mitigation (security measures, encryption, access controls)
- Authorizing Official (system owner or responsible party)

For each active AWS service with resources, identify:
1. The specific ports and protocols it uses
2. Whether it has external access or is internal-only
3. The security controls and mitigations in place
4. The hosting environment details

Include all active services: compute, storage, database, networking, security, ML/AI, analytics, and management services. Focus on services that actually have active resources deployed.

Format as a professional compliance table suitable for government security documentation.`
			if debug {
				fmt.Println("Compliance mode enabled: Full infrastructure discovery for comprehensive SSP documentation")
			}
		}

		// Discovery mode enables comprehensive infrastructure analysis
		if discovery {
			includeAWS = true
			includeTerraform = true
			if debug {
				fmt.Println("Discovery mode enabled: AWS and Terraform contexts activated")
			}
		}

		// If no specific context is requested, try to infer from the question
		if !includeAWS && !includeGitHub && !includeTerraform {
			var inferredTerraform bool
			var inferredCode bool
			var inferredK8s bool
			includeAWS, inferredCode, includeGitHub, inferredTerraform, inferredK8s = inferContext(question)
			_ = inferredCode

			if debug {
				fmt.Printf("Inferred context: AWS=%v, GitHub=%v, Terraform=%v, K8s=%v\n", includeAWS, includeGitHub, inferredTerraform, inferredK8s)
			}

			// Handle inferred Terraform context
			if inferredTerraform {
				includeTerraform = true
			}

			// Handle K8s queries by delegating to K8s agent
			if inferredK8s {
				return handleK8sQuery(context.Background(), question, debug, viper.GetString("kubernetes.kubeconfig"))
			}
		}

		ctx := context.Background()

		// Gather context
		var awsContext string
		var githubContext string
		var terraformContext string

		if includeAWS {
			var awsClient *aws.Client
			var err error

			// Use specified profile or default from config
			targetProfile := profile
			if targetProfile == "" {
				// Try infra config first
				defaultEnv := viper.GetString("infra.default_environment")
				if defaultEnv == "" {
					defaultEnv = "dev"
				}
				targetProfile = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
				if targetProfile == "" {
					targetProfile = viper.GetString("aws.default_profile")
				}
				if targetProfile == "" {
					targetProfile = "default" // fallback
				}
			}

			awsClient, err = aws.NewClientWithProfileAndDebug(ctx, targetProfile, debug)
			if err != nil {
				return fmt.Errorf("failed to create AWS client with profile %s: %w", targetProfile, err)
			}

			awsContext, err = awsClient.GetRelevantContext(ctx, question)
			if err != nil {
				return fmt.Errorf("failed to get AWS context: %w", err)
			}

			if discovery {
				rolesContext, err := awsClient.GetRelevantContext(ctx, "iam roles")
				if err != nil {
					return fmt.Errorf("failed to get AWS IAM roles context: %w", err)
				}
				if strings.TrimSpace(rolesContext) != "" {
					awsContext = awsContext + rolesContext
				}
			}
		}

		if includeGitHub {
			// Get GitHub configuration
			token := viper.GetString("github.token")
			owner := viper.GetString("github.owner")
			repo := viper.GetString("github.repo")

			if owner != "" && repo != "" {
				githubClient := ghclient.NewClient(token, owner, repo)
				var err error
				githubContext, err = githubClient.GetRelevantContext(ctx, question)
				if err != nil {
					return fmt.Errorf("failed to get GitHub context: %w", err)
				}
			}
		}

		if includeTerraform {
			// Only try to create Terraform client if workspaces are configured
			workspaces := viper.GetStringMap("terraform.workspaces")
			if len(workspaces) > 0 {
				tfClient, err := tfclient.NewClient(workspace)
				if err != nil {
					return fmt.Errorf("failed to create Terraform client: %w", err)
				}

				terraformContext, err = tfClient.GetRelevantContext(ctx, question)
				if err != nil {
					return fmt.Errorf("failed to get Terraform context: %w", err)
				}
			} else if debug {
				fmt.Println("Terraform context requested but no workspaces configured, skipping")
			}
		}

		// Query AI with tool support
		var aiClient *ai.Client
		var err error

		if debug {
			fmt.Printf("Tool calling check: includeAWS=%v, includeGitHub=%v\n", includeAWS, includeGitHub)
		}

		// Create AI client with AWS and GitHub clients for tool calling
		if includeAWS || includeGitHub {
			var awsClient *aws.Client
			var githubClient *ghclient.Client

			if includeAWS {
				// Use specified profile or default from config
				targetProfile := profile
				if targetProfile == "" {
					// Try infra config first
					defaultEnv := viper.GetString("infra.default_environment")
					if defaultEnv == "" {
						defaultEnv = "dev"
					}
					targetProfile = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
					if targetProfile == "" {
						targetProfile = viper.GetString("aws.default_profile")
					}
					if targetProfile == "" {
						targetProfile = "default" // fallback
					}
				}

				awsClient, err = aws.NewClientWithProfileAndDebug(ctx, targetProfile, debug)
				if err != nil {
					return fmt.Errorf("failed to create AWS client with profile %s: %w", targetProfile, err)
				}
				if debug {
					fmt.Printf("Successfully created AWS client with profile: %s\n", targetProfile)
				}
			}

			if includeGitHub {
				token := viper.GetString("github.token")
				owner := viper.GetString("github.owner")
				repo := viper.GetString("github.repo")
				if owner != "" && repo != "" {
					githubClient = ghclient.NewClient(token, owner, repo)
				}
			}

			// Get the provider from the AI profile, or use default
			var provider string
			if aiProfile != "" {
				// Use the specified AI profile name as the provider
				provider = aiProfile
			} else {
				// Use the default provider from config
				provider = viper.GetString("ai.default_provider")
				if provider == "" {
					provider = "openai" // fallback
				}
			}

			maybeOverrideGeminiModel(provider, geminiModel)

			// Get the appropriate API key based on provider
			var apiKey string
			switch provider {
			case "gemini":
				// Gemini uses Application Default Credentials - no API key needed
				apiKey = ""
			case "gemini-api":
				apiKey = resolveGeminiAPIKey(geminiKey)
			case "openai":
				apiKey = resolveOpenAIKey(openaiKey)
			case "anthropic":
				// Get Anthropic API key from flag or config
				if anthropicKey != "" {
					apiKey = anthropicKey
				} else {
					apiKey = viper.GetString("ai.providers.anthropic.api_key_env")
				}
			default:
				// Default/other providers
				apiKey = viper.GetString("ai.api_key")
			}

			aiClient = ai.NewClientWithTools(provider, apiKey, awsClient, githubClient, debug, aiProfile)
			if debug {
				fmt.Printf("Created AI client with tools: AWS=%v, GitHub=%v\n", awsClient != nil, githubClient != nil)
			}
		} else {
			// Get the provider from the AI profile, or use default
			var provider string
			if aiProfile != "" {
				// Use the specified AI profile name as the provider
				provider = aiProfile
			} else {
				// Use the default provider from config
				provider = viper.GetString("ai.default_provider")
				if provider == "" {
					provider = "openai" // fallback
				}
			}

			maybeOverrideGeminiModel(provider, geminiModel)

			// Get the appropriate API key based on provider
			var apiKey string
			switch provider {
			case "gemini":
				// Gemini uses Application Default Credentials - no API key needed
				apiKey = ""
			case "gemini-api":
				apiKey = resolveGeminiAPIKey(geminiKey)
			case "openai":
				apiKey = resolveOpenAIKey(openaiKey)
			case "anthropic":
				// Get Anthropic API key from flag or config
				if anthropicKey != "" {
					apiKey = anthropicKey
				} else {
					apiKey = viper.GetString("ai.providers.anthropic.api_key_env")
				}
			default:
				// Default/other providers
				apiKey = viper.GetString("ai.api_key")
			}

			aiClient = ai.NewClient(provider, apiKey, debug, aiProfile)
		}

		// Only Terraform context is supported here (code scanning disabled).
		combinedCodeContext := terraformContext

		// If no tools are enabled, skip the tool-calling pipeline entirely.
		// This avoids confusing "selected operations" output that cannot execute.
		if !includeAWS && !includeGitHub {
			if debug {
				fmt.Println("No tools enabled (AWS/GitHub). Skipping tool pipeline.")
			}
			response, err := aiClient.AskOriginal(ctx, question, awsContext, combinedCodeContext, githubContext)
			if err != nil {
				return fmt.Errorf("failed to get AI response: %w", err)
			}
			fmt.Println(response)
			return nil
		}

		// Use the same AWS profile for both infrastructure queries and tool calls
		awsProfileForTools := profile
		if awsProfileForTools == "" {
			// First try to get the profile from profile-infra-analysis configuration
			awsProfileForTools = ai.FindInfraAnalysisProfile()
		}

		if debug {
			fmt.Printf("Calling AskWithTools with AWS profile: %s\n", awsProfileForTools)
		}

		response, err := aiClient.AskWithTools(ctx, question, awsContext, combinedCodeContext, awsProfileForTools, githubContext)
		if err != nil {
			return fmt.Errorf("failed to get AI response: %w", err)
		}

		fmt.Println(response)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(askCmd)

	askCmd.Flags().Bool("aws", false, "Include AWS infrastructure context")
	askCmd.Flags().Bool("github", false, "Include GitHub repository context")
	askCmd.Flags().Bool("terraform", false, "Include Terraform workspace context")
	askCmd.Flags().Bool("discovery", false, "Run comprehensive infrastructure discovery (all services)")
	askCmd.Flags().Bool("compliance", false, "Generate compliance report showing all services, ports, and protocols")
	askCmd.Flags().String("profile", "", "AWS profile to use for infrastructure queries")
	askCmd.Flags().String("workspace", "", "Terraform workspace to use for infrastructure queries")
	askCmd.Flags().String("ai-profile", "", "AI profile to use (default: 'default')")
	askCmd.Flags().String("openai-key", "", "OpenAI API key (overrides config)")
	askCmd.Flags().String("anthropic-key", "", "Anthropic API key (overrides config)")
	askCmd.Flags().String("gemini-key", "", "Gemini API key (overrides config and env vars)")
	askCmd.Flags().String("gemini-model", "", "Gemini model to use (overrides config)")
	askCmd.Flags().Bool("agent-trace", false, "Show detailed coordinator agent lifecycle logs (overrides config)")
	askCmd.Flags().Bool("maker", false, "Generate an AWS CLI plan (JSON) for infrastructure changes")
	askCmd.Flags().Bool("destroyer", false, "Allow destructive AWS CLI operations when using --maker (requires explicit confirmation in UI/workflow)")
	askCmd.Flags().Bool("apply", false, "Apply an approved maker plan (reads from stdin unless --plan-file is provided)")
	askCmd.Flags().String("plan-file", "", "Optional path to maker plan JSON file for --apply")
}

func resolveGeminiAPIKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if key := viper.GetString("ai.providers.gemini-api.api_key"); key != "" {
		return key
	}
	if envName := viper.GetString("ai.providers.gemini-api.api_key_env"); envName != "" {
		if envVal := os.Getenv(envName); envVal != "" {
			return envVal
		}
	}
	if envVal := os.Getenv("GEMINI_API_KEY"); envVal != "" {
		return envVal
	}
	return ""
}

func resolveOpenAIKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if key := viper.GetString("ai.providers.openai.api_key"); key != "" {
		return key
	}
	if envName := viper.GetString("ai.providers.openai.api_key_env"); envName != "" {
		if envVal := os.Getenv(envName); envVal != "" {
			return envVal
		}
	}
	if envVal := os.Getenv("OPENAI_API_KEY"); envVal != "" {
		return envVal
	}
	return ""
}

func maybeOverrideGeminiModel(provider, flagValue string) {
	if provider != "gemini" && provider != "gemini-api" {
		return
	}

	if model := resolveGeminiModel(provider, flagValue); model != "" {
		viper.Set(fmt.Sprintf("ai.providers.%s.model", provider), model)
	}
}

func resolveGeminiModel(provider, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	configKey := fmt.Sprintf("ai.providers.%s.model", provider)
	model := viper.GetString(configKey)
	if model == "" || strings.EqualFold(model, "gemini-pro") {
		return defaultGeminiModel
	}

	return model
}

// inferContext tries to determine if the question is about AWS, GitHub, Terraform, or Kubernetes.
// Code scanning is disabled, so this never infers code context.
func inferContext(question string) (aws bool, code bool, github bool, terraform bool, k8s bool) {
	awsKeywords := []string{
		// Core services
		"ec2", "lambda", "rds", "s3", "ecs", "cloudwatch", "logs", "batch", "sqs", "sns", "dynamodb", "elasticache", "elb", "alb", "nlb", "route53", "cloudfront", "api-gateway", "cognito", "iam", "vpc", "subnet", "security-group", "nacl", "nat", "igw", "vpn", "direct-connect",
		// General terms
		"instance", "bucket", "database", "aws", "resources", "infrastructure", "running", "account", "error", "log", "job", "queue", "compute", "storage", "network", "cdn", "load-balancer", "auto-scaling", "scaling", "health", "metric", "alarm", "notification", "backup", "snapshot", "ami", "volume", "ebs", "efs", "fsx",
		// Compute and GPU terms
		"gpu", "cuda", "ml", "machine-learning", "training", "inference", "p2", "p3", "p4", "g3", "g4", "g5", "spot", "reserved", "dedicated",
		// Status and operations
		"status", "state", "healthy", "unhealthy", "available", "pending", "stopping", "stopped", "terminated", "creating", "deleting", "modifying", "active", "inactive", "enabled", "disabled",
		// Cost and billing
		"cost", "billing", "price", "usage", "spend", "budget",
		// Monitoring and debugging
		"monitor", "trace", "debug", "performance", "latency", "throughput", "error-rate", "failure", "timeout", "retry",
		// Infrastructure discovery
		"services", "active", "deployed", "discovery", "overview", "summary", "list-all", "what's-running", "what-services", "infrastructure-overview",
	}

	githubKeywords := []string{
		// GitHub platform
		"github", "git", "repository", "repo", "fork", "clone", "branch", "tag", "release", "issue", "discussion",
		// CI/CD and Actions
		"action", "workflow", "ci", "cd", "build", "deploy", "deployment", "pipeline", "job", "step", "runner", "artifact",
		// Collaboration
		"pr", "pull", "request", "merge", "commit", "push", "pull-request", "review", "approve", "comment", "assignee", "reviewer",
		// Project management
		"milestone", "project", "board", "epic", "story", "task", "bug", "feature", "enhancement", "label", "status",
		// Security and compliance
		"security", "vulnerability", "dependabot", "secret", "token", "permission", "access", "audit",
	}

	terraformKeywords := []string{
		// Terraform core
		"terraform", "tf", "hcl", "plan", "apply", "destroy", "init", "workspace", "state", "backend", "provider", "resource", "data", "module", "variable", "output", "local",
		// Operations
		"infrastructure-as-code", "iac", "provisioning", "deployment", "environment", "stack", "configuration", "template",
		// State management
		"tfstate", "state-file", "remote-state", "lock", "unlock", "drift", "refresh", "import", "taint", "untaint",
		// Workspaces and environments
		"dev", "stage", "staging", "prod", "production", "qa", "environment", "workspace",
	}

	k8sKeywords := []string{
		// Core K8s terms
		"kubernetes", "k8s", "kubectl", "kube",
		// Workloads
		"pod", "pods", "deployment", "deployments", "replicaset", "statefulset",
		"daemonset", "job", "cronjob",
		// Networking
		"service", "services", "ingress", "loadbalancer", "nodeport", "clusterip",
		"networkpolicy", "endpoint",
		// Storage
		"pv", "pvc", "persistentvolume", "storageclass", "configmap", "secret",
		// Cluster
		"node", "nodes", "namespace", "cluster", "kubeconfig", "context",
		// Tools
		"helm", "chart", "release", "tiller",
		// Providers
		"eks", "kubeadm", "kops", "k3s", "minikube",
		// Operations
		"rollout", "scale", "drain", "cordon", "taint",
	}

	questionLower := strings.ToLower(question)

	for _, keyword := range awsKeywords {
		if contains(questionLower, keyword) {
			aws = true
			break
		}
	}

	for _, keyword := range githubKeywords {
		if contains(questionLower, keyword) {
			github = true
			break
		}
	}

	for _, keyword := range terraformKeywords {
		if contains(questionLower, keyword) {
			terraform = true
			break
		}
	}

	for _, keyword := range k8sKeywords {
		if contains(questionLower, keyword) {
			k8s = true
			break
		}
	}

	// If no specific context detected, include AWS + GitHub by default.
	if !aws && !github && !terraform && !k8s {
		aws, github = true, true
	}

	return aws, code, github, terraform, k8s
}

// handleK8sQuery delegates a Kubernetes query to the K8s agent
func handleK8sQuery(ctx context.Context, question string, debug bool, kubeconfig string) error {
	if debug {
		fmt.Println("Delegating query to K8s agent...")
	}

	// Create K8s agent with AWS profile and region for EKS support
	// Resolve profile using same pattern as AWS client
	awsProfile := ""
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	awsProfile = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
	if awsProfile == "" {
		awsProfile = viper.GetString("aws.default_profile")
	}
	if awsProfile == "" {
		awsProfile = "default"
	}

	// Resolve region
	awsRegion := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", defaultEnv))
	if awsRegion == "" {
		awsRegion = viper.GetString("aws.default_region")
	}
	if awsRegion == "" {
		awsRegion = "us-east-1"
	}

	k8sAgent := k8s.NewAgentWithOptions(k8s.AgentOptions{
		Debug:      debug,
		AWSProfile: awsProfile,
		Region:     awsRegion,
		Kubeconfig: kubeconfig,
	})

	// Configure query options
	opts := k8s.QueryOptions{
		ClusterName: viper.GetString("kubernetes.default_cluster"),
		ClusterType: k8s.ClusterType(viper.GetString("kubernetes.default_type")),
		Namespace:   viper.GetString("kubernetes.default_namespace"),
		Kubeconfig:  kubeconfig,
	}

	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	if opts.ClusterType == "" {
		opts.ClusterType = k8s.ClusterTypeExisting
	}

	// Handle the query
	response, err := k8sAgent.HandleQuery(ctx, question, opts)
	if err != nil {
		return fmt.Errorf("K8s agent error: %w", err)
	}

	// Output based on response type
	switch response.Type {
	case k8s.ResponseTypePlan:
		// Output plan for user verification
		planJSON, err := json.MarshalIndent(response.Plan, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format plan: %w", err)
		}
		fmt.Println(string(planJSON))
		fmt.Println("\n// To apply this plan, run:")
		fmt.Println("// clanker ask --k8s-apply --plan-file <save-above-to-file.json>")

	case k8s.ResponseTypeResult:
		fmt.Println(response.Result)

	case k8s.ResponseTypeError:
		return response.Error
	}

	return nil
}

func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
