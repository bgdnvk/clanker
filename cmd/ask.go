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
	"github.com/bgdnvk/clanker/internal/k8s/plan"
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
		routeOnly, _ := cmd.Flags().GetBool("route-only")

		// Handle route-only mode: return routing decision as JSON without executing
		if routeOnly {
			agent, reason := determineRoutingDecision(question)
			result := map[string]string{
				"agent":  agent,
				"reason": reason,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
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

				// Check if this is a K8s plan (contains eksctl, kubectl, or kubeadm commands)
				if isK8sPlan(rawPlan) {
					return executeK8sPlan(ctx, rawPlan, profile, debug)
				}

				makerPlan, err := maker.ParsePlan(rawPlan)
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

				return maker.ExecutePlan(ctx, makerPlan, maker.ExecOptions{
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
	askCmd.Flags().Bool("route-only", false, "Return routing decision as JSON without executing (for backend integration)")
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

	questionLower := strings.ToLower(question)

	// Check if this is a cluster provisioning request
	isClusterProvisioning := (strings.Contains(questionLower, "create") || strings.Contains(questionLower, "provision") || strings.Contains(questionLower, "setup")) &&
		(strings.Contains(questionLower, "cluster") || strings.Contains(questionLower, "eks") || strings.Contains(questionLower, "kubeadm"))

	if isClusterProvisioning {
		return handleK8sClusterProvisioning(ctx, question, questionLower, awsProfile, awsRegion, debug)
	}

	// Check if this is a deployment request (creating a deployment, not listing)
	// Exclude read-only queries that mention "deployment" or "deployments"
	isReadOnlyQuery := strings.Contains(questionLower, "list") ||
		strings.Contains(questionLower, "get") ||
		strings.Contains(questionLower, "show") ||
		strings.Contains(questionLower, "describe") ||
		strings.Contains(questionLower, "what") ||
		strings.Contains(questionLower, "how") ||
		strings.Contains(questionLower, "scale") ||
		strings.Contains(questionLower, "rollout") ||
		strings.Contains(questionLower, "status")

	// Check for actual deploy action words (not just substring match on "deployment")
	hasDeployAction := strings.Contains(questionLower, "deploy ") ||
		strings.HasPrefix(questionLower, "deploy") ||
		strings.Contains(questionLower, "run ")

	isDeployRequest := hasDeployAction &&
		!strings.Contains(questionLower, "cluster") &&
		!isReadOnlyQuery

	if isDeployRequest {
		return handleK8sDeployment(ctx, question, questionLower, debug)
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
		// Display plan summary
		fmt.Printf("\nPlan: %s\n", response.Plan.Summary)
		fmt.Println(strings.Repeat("-", 60))

		// Show helm commands if present
		if len(response.Plan.HelmCmds) > 0 {
			fmt.Println("\nSteps:")
			for i, cmd := range response.Plan.HelmCmds {
				if len(cmd.Args) > 0 {
					fmt.Printf("  %d. helm %s\n", i+1, strings.Join(cmd.Args, " "))
				} else {
					fmt.Printf("  %d. helm %s %s %s\n", i+1, cmd.Action, cmd.Release, cmd.Chart)
				}
				if cmd.Reason != "" {
					fmt.Printf("     Reason: %s\n", cmd.Reason)
				}
			}
		}

		// Show kubectl commands if present
		if len(response.Plan.KubectlCmds) > 0 {
			fmt.Println("\nKubectl Commands:")
			for i, cmd := range response.Plan.KubectlCmds {
				fmt.Printf("  %d. kubectl %s\n", i+1, strings.Join(cmd.Args, " "))
			}
		}

		// Show notes
		if len(response.Plan.Notes) > 0 {
			fmt.Println("\nNotes:")
			for _, note := range response.Plan.Notes {
				fmt.Printf("  - %s\n", note)
			}
		}

		fmt.Println(strings.Repeat("-", 60))

		// Prompt for approval
		fmt.Print("\nDo you want to apply this plan? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)

		if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
			fmt.Println("Plan cancelled.")
			return nil
		}

		// Execute the plan
		fmt.Println("\nApplying plan...")
		if err := executeK8sAgentPlan(ctx, response.Plan, debug); err != nil {
			return fmt.Errorf("plan execution failed: %w", err)
		}
		fmt.Println("\nPlan applied successfully!")

	case k8s.ResponseTypeResult:
		fmt.Println(response.Result)

	case k8s.ResponseTypeError:
		return response.Error
	}

	return nil
}

// executeK8sAgentPlan executes a K8s plan including helm and kubectl commands
func executeK8sAgentPlan(ctx context.Context, k8sPlan *k8s.K8sPlan, debug bool) error {
	// Execute helm commands
	for _, helmCmd := range k8sPlan.HelmCmds {
		if debug {
			fmt.Printf("[debug] Executing helm command: %s %s\n", helmCmd.Action, helmCmd.Release)
		}

		// Build command args based on the helm command structure
		args := buildHelmArgs(helmCmd)
		if len(args) == 0 {
			continue
		}

		fmt.Printf("  Running: helm %s\n", strings.Join(args, " "))

		cmd := exec.CommandContext(ctx, "helm", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("helm command failed: %w", err)
		}
	}

	// Execute kubectl commands
	for _, kubectlCmd := range k8sPlan.KubectlCmds {
		if debug {
			fmt.Printf("[debug] Executing kubectl command: %v\n", kubectlCmd.Args)
		}

		fmt.Printf("  Running: kubectl %s\n", strings.Join(kubectlCmd.Args, " "))

		cmd := exec.CommandContext(ctx, "kubectl", kubectlCmd.Args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kubectl command failed: %w", err)
		}
	}

	return nil
}

// buildHelmArgs builds helm command arguments from a HelmCmd
func buildHelmArgs(helmCmd k8s.HelmCmd) []string {
	// If raw Args are available, use them directly
	if len(helmCmd.Args) > 0 {
		return helmCmd.Args
	}

	// Otherwise, build args from structured fields
	var args []string

	switch helmCmd.Action {
	case "install":
		args = []string{"install", helmCmd.Release, helmCmd.Chart}
		if helmCmd.Namespace != "" {
			args = append(args, "-n", helmCmd.Namespace)
		}
		if helmCmd.Wait {
			args = append(args, "--wait")
		}
		if helmCmd.Timeout != "" {
			args = append(args, "--timeout", helmCmd.Timeout)
		}
	case "upgrade":
		args = []string{"upgrade", helmCmd.Release, helmCmd.Chart}
		if helmCmd.Namespace != "" {
			args = append(args, "-n", helmCmd.Namespace)
		}
		if helmCmd.Wait {
			args = append(args, "--wait")
		}
	case "uninstall":
		args = []string{"uninstall", helmCmd.Release}
		if helmCmd.Namespace != "" {
			args = append(args, "-n", helmCmd.Namespace)
		}
	case "rollback":
		args = []string{"rollback", helmCmd.Release}
		if helmCmd.Namespace != "" {
			args = append(args, "-n", helmCmd.Namespace)
		}
	}

	return args
}

// handleK8sClusterProvisioning handles cluster creation requests with plan display and approval
func handleK8sClusterProvisioning(ctx context.Context, question, questionLower, awsProfile, awsRegion string, debug bool) error {
	// Determine cluster type from question
	isEKS := strings.Contains(questionLower, "eks")
	isKubeadm := strings.Contains(questionLower, "kubeadm") || strings.Contains(questionLower, "ec2")

	// Default to EKS if not specified
	if !isEKS && !isKubeadm {
		isEKS = true
	}

	// Extract cluster name from question
	clusterName := extractClusterName(questionLower)
	if clusterName == "" {
		clusterName = "clanker-cluster"
	}

	// Extract node count
	nodeCount := extractNodeCount(questionLower)
	if nodeCount <= 0 {
		nodeCount = 1
	}

	// Extract instance type
	instanceType := extractInstanceType(questionLower)
	if instanceType == "" {
		instanceType = "t3.small"
	}

	if isEKS {
		return handleEKSCreation(ctx, clusterName, nodeCount, instanceType, awsProfile, awsRegion, debug)
	}

	return handleKubeadmCreation(ctx, clusterName, nodeCount, instanceType, awsProfile, awsRegion, debug)
}

// handleEKSCreation handles EKS cluster creation - outputs plan JSON like AWS maker
func handleEKSCreation(ctx context.Context, clusterName string, nodeCount int, instanceType, awsProfile, awsRegion string, debug bool) error {
	// Generate the plan
	k8sPlan := plan.GenerateEKSCreatePlan(plan.EKSCreateOptions{
		ClusterName:       clusterName,
		Region:            awsRegion,
		Profile:           awsProfile,
		NodeCount:         nodeCount,
		NodeType:          instanceType,
		KubernetesVersion: "1.29",
	})

	// Convert to maker-compatible format and output JSON (same as AWS maker)
	question := fmt.Sprintf("create an eks cluster called %s with %d node using %s", clusterName, nodeCount, instanceType)
	makerPlan := k8sPlan.ToMakerPlan(question)
	planJSON, err := json.MarshalIndent(makerPlan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format plan: %w", err)
	}
	fmt.Println(string(planJSON))

	return nil
}

// handleKubeadmCreation handles kubeadm cluster creation - outputs plan JSON like AWS maker
func handleKubeadmCreation(ctx context.Context, clusterName string, workerCount int, instanceType, awsProfile, awsRegion string, debug bool) error {
	// Default key pair name
	keyPairName := fmt.Sprintf("clanker-%s-key", clusterName)

	// Check/ensure SSH key exists (output to stderr so it doesn't mix with JSON)
	sshKeyInfo, err := plan.EnsureSSHKey(ctx, keyPairName, awsRegion, awsProfile, os.Stderr)
	if err != nil {
		return fmt.Errorf("failed to ensure SSH key: %w", err)
	}

	sshKeyPath := sshKeyInfo.PrivateKeyPath

	// Generate the plan
	k8sPlan := plan.GenerateKubeadmCreatePlan(plan.KubeadmCreateOptions{
		ClusterName:       clusterName,
		Region:            awsRegion,
		Profile:           awsProfile,
		WorkerCount:       workerCount,
		NodeType:          instanceType,
		ControlPlaneType:  instanceType,
		KubernetesVersion: "1.29",
		KeyPairName:       keyPairName,
		SSHKeyPath:        sshKeyPath,
		CNI:               "calico",
	})

	// Convert to maker-compatible format and output JSON (same as AWS maker)
	question := fmt.Sprintf("create a kubeadm cluster called %s with %d workers using %s", clusterName, workerCount, instanceType)
	makerPlan := k8sPlan.ToMakerPlan(question)
	planJSON, err := json.MarshalIndent(makerPlan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format plan: %w", err)
	}
	fmt.Println(string(planJSON))

	return nil
}

// handleK8sDeployment handles deployment requests - outputs plan JSON like AWS maker
func handleK8sDeployment(ctx context.Context, question, questionLower string, debug bool) error {
	// Extract image from question
	image := extractImage(questionLower)
	if image == "" {
		image = "nginx"
	}

	// Extract deployment name
	deployName := extractDeployName(questionLower)
	if deployName == "" {
		// Extract from image
		parts := strings.Split(image, "/")
		deployName = parts[len(parts)-1]
		if idx := strings.Index(deployName, ":"); idx > 0 {
			deployName = deployName[:idx]
		}
	}

	// Extract port
	port := 80

	// Extract replicas
	replicas := 1

	// Extract namespace
	namespace := "default"

	// Generate deploy plan
	deployPlan := plan.GenerateDeployPlan(plan.DeployOptions{
		Name:      deployName,
		Image:     image,
		Port:      port,
		Replicas:  replicas,
		Namespace: namespace,
		Type:      "deployment",
	})

	// Convert to maker-compatible format and output JSON (same as AWS maker)
	deployQuestion := fmt.Sprintf("deploy %s to kubernetes", image)
	makerPlan := deployPlan.ToMakerPlan(deployQuestion)
	planJSON, err := json.MarshalIndent(makerPlan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format plan: %w", err)
	}
	fmt.Println(string(planJSON))

	return nil
}

// Helper functions for parsing questions

func extractClusterName(question string) string {
	// Look for "called X" or "named X" patterns
	patterns := []string{"called ", "named ", "name "}
	for _, pattern := range patterns {
		if idx := strings.Index(question, pattern); idx != -1 {
			rest := question[idx+len(pattern):]
			words := strings.Fields(rest)
			if len(words) > 0 {
				name := words[0]
				// Clean up any trailing punctuation
				name = strings.TrimRight(name, ".,;:!?")
				return name
			}
		}
	}
	return ""
}

func extractNodeCount(question string) int {
	// Look for "X node" or "X worker" patterns
	words := strings.Fields(question)
	for i, word := range words {
		if (strings.Contains(word, "node") || strings.Contains(word, "worker")) && i > 0 {
			var count int
			if _, err := fmt.Sscanf(words[i-1], "%d", &count); err == nil {
				return count
			}
		}
	}
	return 0
}

func extractInstanceType(question string) string {
	// Look for common instance type patterns
	instanceTypes := []string{"t3.micro", "t3.small", "t3.medium", "t3.large", "t3.xlarge",
		"t2.micro", "t2.small", "t2.medium", "t2.large",
		"m5.large", "m5.xlarge", "m6i.large", "m6i.xlarge"}
	for _, t := range instanceTypes {
		if strings.Contains(question, t) {
			return t
		}
	}
	return ""
}

func extractImage(question string) string {
	// Look for common image patterns
	words := strings.Fields(question)
	for _, word := range words {
		// Check for docker image patterns
		if strings.Contains(word, "/") || strings.Contains(word, ":") {
			return strings.TrimRight(word, ".,;:!?")
		}
		// Check for common images
		commonImages := []string{"nginx", "redis", "postgres", "mysql", "mongo", "node", "python", "golang"}
		for _, img := range commonImages {
			if word == img {
				return img
			}
		}
	}
	return ""
}

func extractDeployName(question string) string {
	// Look for "called X" or "named X" patterns
	patterns := []string{"called ", "named ", "name "}
	for _, pattern := range patterns {
		if idx := strings.Index(question, pattern); idx != -1 {
			rest := question[idx+len(pattern):]
			words := strings.Fields(rest)
			if len(words) > 0 {
				name := words[0]
				name = strings.TrimRight(name, ".,;:!?")
				return name
			}
		}
	}
	return ""
}

func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// formatK8sCommand formats a command for display (like AWS maker formatAWSArgsForLog)
func formatK8sCommand(cmdName string, args []string) string {
	const maxArgLen = 160
	const maxTotalLen = 700

	parts := make([]string, 0, len(args)+1)
	parts = append(parts, cmdName)
	for _, a := range args {
		if len(a) > maxArgLen {
			a = a[:maxArgLen] + "..."
		}
		parts = append(parts, a)
	}
	s := strings.Join(parts, " ")
	if len(s) > maxTotalLen {
		s = s[:maxTotalLen] + "..."
	}
	return s
}

// isK8sPlan checks if a plan JSON is a K8s plan (contains eksctl, kubectl, or kubeadm commands)
func isK8sPlan(rawPlan string) bool {
	return strings.Contains(rawPlan, `"eksctl"`) ||
		strings.Contains(rawPlan, `"kubectl"`) ||
		strings.Contains(rawPlan, `"kubeadm"`)
}

// executeK8sPlan executes a K8s plan
func executeK8sPlan(ctx context.Context, rawPlan string, profile string, debug bool) error {
	// Parse the plan
	var makerPlan plan.MakerPlan
	if err := json.Unmarshal([]byte(rawPlan), &makerPlan); err != nil {
		return fmt.Errorf("failed to parse K8s plan: %w", err)
	}

	// Resolve AWS profile
	awsProfile := profile
	if awsProfile == "" {
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
	}

	// Resolve region
	awsRegion := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", viper.GetString("infra.default_environment")))
	if awsRegion == "" {
		awsRegion = viper.GetString("aws.default_region")
	}
	if awsRegion == "" {
		awsRegion = "us-east-1"
	}

	// Execute each command
	for i, cmd := range makerPlan.Commands {
		if len(cmd.Args) == 0 {
			continue
		}

		cmdName := cmd.Args[0]
		cmdArgs := cmd.Args[1:]

		// Add profile/region for AWS and eksctl commands
		if cmdName == "aws" || cmdName == "eksctl" {
			cmdArgs = append(cmdArgs, "--profile", awsProfile)
			if cmdName == "eksctl" {
				cmdArgs = append(cmdArgs, "--region", awsRegion)
			}
		}

		// Format command for display (like AWS maker)
		displayCmd := formatK8sCommand(cmdName, cmdArgs)
		fmt.Printf("[k8s] running %d/%d: %s\n", i+1, len(makerPlan.Commands), displayCmd)

		// Execute the command
		execCmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr

		if err := execCmd.Run(); err != nil {
			return fmt.Errorf("command failed: %s: %w", cmdName, err)
		}

		fmt.Println()
	}

	return nil
}

// determineRoutingDecision analyzes a question and returns which agent should handle it.
// This is used by the --route-only flag to return routing decisions without executing.
func determineRoutingDecision(question string) (agent string, reason string) {
	questionLower := strings.ToLower(question)

	// Check for diagram/visualization requests
	diagramKeywords := []string{
		"diagram", "visual", "visualize", "layout", "arrange",
		"draw", "illustrate", "show on diagram", "add to diagram",
		"update diagram", "modify diagram",
	}
	for _, kw := range diagramKeywords {
		if strings.Contains(questionLower, kw) {
			return "diagram", "Diagram or visualization request detected"
		}
	}

	// Action keywords for infrastructure provisioning
	actionKeywords := []string{
		"create", "provision", "deploy", "launch", "spin up", "set up", "setup",
		"add", "make", "build", "install", "configure", "enable", "start",
		"update", "modify", "change", "scale", "resize", "upgrade",
		"delete", "remove", "destroy", "terminate", "tear down", "teardown",
	}

	// K8s resources (checked first as more specific)
	k8sResources := []string{
		"kubernetes", "k8s", "pod", "pods", "deployment", "deployments",
		"service", "services", "ingress", "namespace", "configmap",
		"secret", "pvc", "persistent volume", "statefulset", "daemonset",
		"replicaset", "cronjob", "job", "container", "helm", "chart",
		"kubectl", "eksctl", "kubeadm", "nginx", "redis", "mysql", "postgres", "mongodb",
		"cluster", "node", "nodes", "kube",
	}

	// AWS resources (excluding EKS which is handled by K8s maker)
	awsResources := []string{
		"ec2", "instance", "lambda", "function", "s3", "bucket",
		"rds", "database", "dynamodb", "table", "sqs", "queue",
		"sns", "topic", "ecs", "fargate", "elasticache", "memcached",
		"elb", "alb", "nlb", "load balancer", "api gateway", "cloudfront", "cdn",
		"route53", "dns", "iam", "role", "policy", "user",
		"vpc", "subnet", "security group", "nat", "igw",
		"kinesis", "stream", "glue", "athena", "redshift",
		"elastic beanstalk", "codepipeline", "codebuild",
	}

	hasAction := false
	for _, action := range actionKeywords {
		if strings.Contains(questionLower, action) {
			hasAction = true
			break
		}
	}

	// Check if question mentions K8s resources
	hasK8sResource := false
	for _, resource := range k8sResources {
		if strings.Contains(questionLower, resource) {
			hasK8sResource = true
			break
		}
	}

	if hasAction {
		// Check K8s resources first (more specific)
		if hasK8sResource {
			return "k8s-maker", "K8s infrastructure provisioning or modification request"
		}
		// Check AWS resources
		for _, resource := range awsResources {
			if strings.Contains(questionLower, resource) {
				return "maker", "AWS infrastructure provisioning or modification request"
			}
		}
	}

	// K8s read queries (no action keyword but mentions K8s resources)
	if hasK8sResource {
		return "k8s", "K8s query or analysis request"
	}

	// Default to CLI for general queries
	return "cli", "General infrastructure query or analysis"
}
