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
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/backend"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	cfanalytics "github.com/bgdnvk/clanker/internal/cloudflare/analytics"
	cfdns "github.com/bgdnvk/clanker/internal/cloudflare/dns"
	cfwaf "github.com/bgdnvk/clanker/internal/cloudflare/waf"
	cfworkers "github.com/bgdnvk/clanker/internal/cloudflare/workers"
	cfzerotrust "github.com/bgdnvk/clanker/internal/cloudflare/zerotrust"
	"github.com/bgdnvk/clanker/internal/gcp"
	ghclient "github.com/bgdnvk/clanker/internal/github"
	iamclient "github.com/bgdnvk/clanker/internal/iam"
	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/plan"
	"github.com/bgdnvk/clanker/internal/maker"
	"github.com/bgdnvk/clanker/internal/routing"
	tfclient "github.com/bgdnvk/clanker/internal/terraform"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// askCmd represents the ask command
const defaultGeminiModel = "gemini-2.5-flash"

var askCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask AI about your cloud infrastructure or GitHub repository",
	Long: `Ask natural language questions about your AWS or GCP infrastructure or GitHub repository.
	
Examples:
  clanker ask "What EC2 instances are running?"
  clanker ask --gcp "List Cloud Run services"
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
		includeGCP, _ := cmd.Flags().GetBool("gcp")
		includeAzure, _ := cmd.Flags().GetBool("azure")
		includeCloudflare, _ := cmd.Flags().GetBool("cloudflare")
		includeTerraform, _ := cmd.Flags().GetBool("terraform")
		includeIAM, _ := cmd.Flags().GetBool("iam")
		iamRoleARN, _ := cmd.Flags().GetString("role-arn")
		iamPolicyARN, _ := cmd.Flags().GetString("policy-arn")
		debug := viper.GetBool("debug")
		discovery, _ := cmd.Flags().GetBool("discovery")
		compliance, _ := cmd.Flags().GetBool("compliance")
		profile, _ := cmd.Flags().GetString("profile")
		workspace, _ := cmd.Flags().GetString("workspace")
		gcpProject, _ := cmd.Flags().GetString("gcp-project")
		azureSubscription, _ := cmd.Flags().GetString("azure-subscription")
		aiProfile, _ := cmd.Flags().GetString("ai-profile")
		openaiKey, _ := cmd.Flags().GetString("openai-key")
		anthropicKey, _ := cmd.Flags().GetString("anthropic-key")
		geminiKey, _ := cmd.Flags().GetString("gemini-key")
		deepseekKey, _ := cmd.Flags().GetString("deepseek-key")
		minimaxKey, _ := cmd.Flags().GetString("minimax-key")
		geminiModel, _ := cmd.Flags().GetString("gemini-model")
		openaiModel, _ := cmd.Flags().GetString("openai-model")
		anthropicModel, _ := cmd.Flags().GetString("anthropic-model")
		deepseekModel, _ := cmd.Flags().GetString("deepseek-model")
		minimaxModel, _ := cmd.Flags().GetString("minimax-model")
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

		// Handle apply mode (independent of maker mode)
		if applyMode {
			ctx := context.Background()
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

			// Check if this is a K8s plan (contains helm, eksctl, kubectl, or kubeadm commands)
			if isK8sPlan(rawPlan) {
				return executeK8sPlan(ctx, rawPlan, profile, debug)
			}

			// Fall back to maker plan execution
			makerPlan, err := maker.ParsePlan(rawPlan)
			if err != nil {
				return fmt.Errorf("invalid plan: %w", err)
			}

			if strings.EqualFold(strings.TrimSpace(makerPlan.Provider), "gcp") {
				return maker.ExecuteGCPPlan(ctx, makerPlan, maker.ExecOptions{
					GCPProject: gcpProject,
					Writer:     os.Stdout,
					Destroyer:  destroyer,
					Debug:      debug,
				})
			}

			if strings.EqualFold(strings.TrimSpace(makerPlan.Provider), "azure") {
				sub := strings.TrimSpace(azureSubscription)
				if sub == "" {
					sub = azure.ResolveSubscriptionID()
				}
				if sub == "" {
					return fmt.Errorf("azure subscription_id is required (set infra.azure.subscription_id, AZURE_SUBSCRIPTION_ID, or use --azure-subscription)")
				}
				return maker.ExecuteAzurePlan(ctx, makerPlan, maker.ExecOptions{
					AzureSubscriptionID: sub,
					Writer:              os.Stdout,
					Destroyer:           destroyer,
					Debug:               debug,
				})
			}

			if strings.EqualFold(strings.TrimSpace(makerPlan.Provider), "cloudflare") {
				cfToken := cloudflare.ResolveAPIToken()
				cfAccountID := cloudflare.ResolveAccountID()
				if cfToken == "" {
					return fmt.Errorf("cloudflare api_token is required (set cloudflare.api_token, CLOUDFLARE_API_TOKEN, or CF_API_TOKEN)")
				}
				return maker.ExecuteCloudflarePlan(ctx, makerPlan, maker.ExecOptions{
					CloudflareAPIToken:  cfToken,
					CloudflareAccountID: cfAccountID,
					Writer:              os.Stdout,
					Destroyer:           destroyer,
					Debug:               debug,
				})
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

			// Resolve provider for AI-assisted error handling
			var provider string
			if aiProfile != "" {
				provider = aiProfile
			} else {
				provider = viper.GetString("ai.default_provider")
				if provider == "" {
					provider = "openai"
				}
			}

			var apiKey string
			switch provider {
			case "gemini":
				apiKey = ""
			case "gemini-api":
				apiKey = resolveGeminiAPIKey(geminiKey)
			case "openai":
				apiKey = resolveOpenAIKey(openaiKey)
			case "anthropic":
				apiKey = resolveAnthropicKey(anthropicKey)
			case "deepseek":
				apiKey = resolveDeepSeekKey(deepseekKey)
			case "minimax":
				apiKey = resolveMiniMaxKey(minimaxKey)
			default:
				apiKey = viper.GetString("ai.api_key")
			}

			return maker.ExecutePlan(ctx, makerPlan, maker.ExecOptions{
				Profile:    targetProfile,
				Region:     region,
				GCPProject: gcpProject,
				Writer:     os.Stdout,
				Destroyer:  destroyer,
				AIProvider: provider,
				AIAPIKey:   apiKey,
				AIProfile:  aiProfile,
				Debug:      debug,
			})
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

			maybeOverrideProviderModel(provider, openaiModel, anthropicModel, geminiModel, deepseekModel, minimaxModel)

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
				apiKey = resolveAnthropicKey(anthropicKey)
			case "deepseek":
				apiKey = resolveDeepSeekKey(deepseekKey)
			case "minimax":
				apiKey = resolveMiniMaxKey(minimaxKey)
			default:
				apiKey = viper.GetString("ai.api_key")
			}

			// Generate maker plan
			if strings.TrimSpace(question) == "" {
				return fmt.Errorf("requires a question")
			}

			// Decide provider for maker plans.
			// Priority:
			//  1) Explicit flags win (--gcp / --aws / --cloudflare)
			//  2) Infer from question (cheap heuristic)
			makerProvider := "aws"
			makerProviderReason := "default"
			explicitGCP := cmd.Flags().Changed("gcp") && includeGCP
			explicitAWS := cmd.Flags().Changed("aws") && includeAWS
			explicitCloudflare := cmd.Flags().Changed("cloudflare") && includeCloudflare
			explicitAzure := cmd.Flags().Changed("azure") && includeAzure
			explicitCount := 0
			if explicitGCP {
				explicitCount++
			}
			if explicitAWS {
				explicitCount++
			}
			if explicitCloudflare {
				explicitCount++
			}
			if explicitAzure {
				explicitCount++
			}
			if explicitCount > 1 {
				return fmt.Errorf("cannot use multiple provider flags (--aws, --gcp, --azure, --cloudflare) together with --maker")
			}
			switch {
			case explicitCloudflare:
				makerProvider = "cloudflare"
				makerProviderReason = "explicit"
			case explicitAzure:
				makerProvider = "azure"
				makerProviderReason = "explicit"
			case explicitGCP:
				makerProvider = "gcp"
				makerProviderReason = "explicit"
			case explicitAWS:
				makerProvider = "aws"
				makerProviderReason = "explicit"
			default:
				svcCtx := routing.InferContext(questionForRouting(question))
				if svcCtx.Cloudflare {
					makerProvider = "cloudflare"
					makerProviderReason = "inferred"
				} else if svcCtx.Azure {
					makerProvider = "azure"
					makerProviderReason = "inferred"
				} else if svcCtx.GCP {
					makerProvider = "gcp"
					makerProviderReason = "inferred"
				}
			}

			// Log to stderr so stdout stays valid JSON.
			_, _ = fmt.Fprintf(os.Stderr, "[maker] provider=%s (%s)\n", makerProvider, makerProviderReason)

			aiClient := ai.NewClient(provider, apiKey, debug, aiProfile)
			var prompt string
			switch makerProvider {
			case "cloudflare":
				prompt = maker.CloudflarePlanPromptWithMode(question, destroyer)
			case "azure":
				prompt = maker.AzurePlanPromptWithMode(question, destroyer)
			case "gcp":
				prompt = maker.GCPPlanPromptWithMode(question, destroyer)
			default:
				prompt = maker.PlanPromptWithMode(question, destroyer)
			}

			const maxMakerPlanAttempts = 4
			var lastParseErr error
			var plan *maker.Plan
			for attempt := 1; attempt <= maxMakerPlanAttempts; attempt++ {
				attemptPrompt := prompt
				if attempt > 1 {
					extra := "Regenerate a VALID JSON plan that matches the schema exactly and includes a NON-EMPTY commands array."
					if lastParseErr != nil {
						errText := strings.ToLower(lastParseErr.Error())
						switch {
						case strings.Contains(errText, "plan has no commands") || strings.Contains(errText, "no commands"):
							extra = "Your previous output had an empty (or missing) commands array. Regenerate a VALID JSON plan with commands. If the user request is ambiguous or missing required details, output a DISCOVERY-ONLY plan with at least 3 READ-ONLY commands that gather the missing inputs (still a non-empty commands array)."
						case strings.Contains(errText, "empty plan") || strings.Contains(errText, "unexpected end of json") || strings.Contains(errText, "unexpected eof"):
							extra = "Your previous output was empty or truncated. Regenerate a COMPLETE, VALID JSON plan that matches the schema exactly and includes a NON-EMPTY commands array."
						case strings.Contains(errText, "invalid character") || strings.Contains(errText, "cannot unmarshal") || strings.Contains(errText, "json:"):
							extra = "Your previous output was not valid JSON. Output ONLY a single JSON object matching the schema exactly (no code fences, no markdown, no backticks, no commentary). Include a NON-EMPTY commands array."
						case strings.Contains(errText, "has empty args") || strings.Contains(errText, "empty args"):
							extra = "One of your commands had empty args. Ensure every command has a non-empty args array and args are individual tokens (no single-string commands)."
						}
					}

					attemptPrompt = fmt.Sprintf(
						"%s\n\nIMPORTANT: Your previous output was invalid (%v). %s\n\nOutput rules (STRICT):\n- Output ONLY JSON (no markdown, no prose).\n- Do NOT wrap in ``` or any other code fences.\n- The response MUST start with '{' and end with '}'.\n- Include required fields: version, createdAt (RFC3339), provider, question, summary, commands.\n- commands MUST be non-empty.",
						prompt,
						lastParseErr,
						extra,
					)
				}
				resp, err := aiClient.AskPrompt(ctx, attemptPrompt)
				if err != nil {
					return err
				}

				cleaned := aiClient.CleanJSONResponse(resp)
				trimmed := strings.TrimSpace(cleaned)
				if trimmed == "" {
					lastParseErr = fmt.Errorf("empty plan")
					continue
				}
				if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
					lastParseErr = fmt.Errorf("invalid json: response must be a single JSON object")
					continue
				}
				parsed, err := maker.ParsePlan(trimmed)
				if err == nil {
					plan = parsed
					break
				}
				lastParseErr = err
			}
			if plan == nil {
				return fmt.Errorf("failed to parse maker plan: %w", lastParseErr)
			}

			plan.Provider = makerProvider

			// Handle GCP, Azure, and Cloudflare plans (output directly, no enrichment)
			providerLower := strings.ToLower(strings.TrimSpace(plan.Provider))
			if providerLower == "gcp" || providerLower == "azure" || providerLower == "cloudflare" {
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
		if workspace != "" {
			includeTerraform = true
		}

		if !includeAWS && !includeGitHub && !includeTerraform && !includeGCP && !includeAzure && !includeCloudflare {
			routingQuestion := questionForRouting(question)

			// First, do quick keyword check for explicit terms
			svcCtx := routing.InferContext(routingQuestion)
			includeAWS = svcCtx.AWS
			includeGitHub = svcCtx.GitHub

			if debug {
				fmt.Printf("Keyword inference: AWS=%v, GitHub=%v, Terraform=%v, K8s=%v, GCP=%v, Cloudflare=%v\n",
					svcCtx.AWS, svcCtx.GitHub, svcCtx.Terraform, svcCtx.K8s, svcCtx.GCP, svcCtx.Cloudflare)
			}

			// For ambiguous queries (multiple services detected or Cloudflare detected),
			// use LLM to make the final routing decision
			if routing.NeedsLLMClassification(svcCtx) {
				if debug {
					fmt.Println("[routing] Ambiguous query detected, using LLM for classification...")
				}

				llmService, err := routing.ClassifyWithLLM(context.Background(), routingQuestion, debug)
				if err != nil {
					// FALLBACK: LLM classification failed, use keyword-based inference
					if debug {
						fmt.Printf("[routing] LLM classification failed (%v), falling back to keyword inference\n", err)
					}
					// Keep the keyword-inferred values as-is (no changes needed)
				} else {
					// LLM succeeded - override keyword-based inference with LLM decision
					routing.ApplyLLMClassification(&svcCtx, llmService)

					if debug {
						fmt.Printf("LLM override: AWS=%v, K8s=%v, GCP=%v, Azure=%v, Cloudflare=%v\n",
							svcCtx.AWS, svcCtx.K8s, svcCtx.GCP, svcCtx.Azure, svcCtx.Cloudflare)
					}
				}
			}

			// Handle inferred Terraform context
			if svcCtx.Terraform {
				includeTerraform = true
			}

			if svcCtx.GCP {
				includeGCP = true
			}

			if svcCtx.Azure {
				includeAzure = true
			}

			// Update includeAWS and includeGitHub from service context
			includeAWS = svcCtx.AWS
			includeGitHub = svcCtx.GitHub
			includeAzure = svcCtx.Azure

			// Handle Cloudflare queries by delegating to Cloudflare agent
			if svcCtx.Cloudflare {
				return handleCloudflareQuery(context.Background(), routingQuestion, debug)
			}

			// Handle IAM queries by delegating to IAM agent
			if includeIAM || svcCtx.IAM {
				return handleIAMQuery(context.Background(), routingQuestion, debug, iamRoleARN, iamPolicyARN)
			}

			// Handle K8s queries by delegating to K8s agent
			if svcCtx.K8s {
				return handleK8sQuery(context.Background(), routingQuestion, debug, viper.GetString("kubernetes.kubeconfig"))
			}
		}

		ctx := context.Background()

		// Gather context
		var awsContext string
		var githubContext string
		var terraformContext string
		var gcpContext string
		var azureContext string

		if includeAWS {
			var awsClient *aws.Client
			var err error

			// Check for backend API key first
			apiKeyFlag, _ := cmd.Flags().GetString("api-key")
			if apiKeyFlag == "" {
				apiKeyFlag, _ = cmd.Root().PersistentFlags().GetString("api-key")
			}
			backendAPIKey := backend.ResolveAPIKey(apiKeyFlag)

			if backendAPIKey != "" {
				// Try to get credentials from backend
				backendClient := backend.NewClient(backendAPIKey, debug)
				backendCreds, backendErr := backendClient.GetAWSCredentials(ctx)
				if backendErr == nil {
					if debug {
						fmt.Println("[backend] Using AWS credentials from backend")
					}
					awsClient, err = aws.NewClientWithCredentials(ctx, &aws.BackendAWSCredentials{
						AccessKeyID:     backendCreds.AccessKeyID,
						SecretAccessKey: backendCreds.SecretAccessKey,
						Region:          backendCreds.Region,
						SessionToken:    backendCreds.SessionToken,
					}, debug)
					if err != nil {
						return fmt.Errorf("failed to create AWS client with backend credentials: %w", err)
					}
				} else if debug {
					fmt.Printf("[backend] No AWS credentials available (%v), falling back to local\n", backendErr)
				}
			}

			// Fall back to local profile if backend credentials not available
			if awsClient == nil {
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
			workspaces := viper.GetStringMap("terraform.workspaces")
			if workspace == "" && len(workspaces) == 0 {
				if debug {
					fmt.Println("Terraform context requested but no workspaces configured, skipping")
				}
			} else {
				tfClient, err := tfclient.NewClient(workspace)
				if err != nil {
					return fmt.Errorf("failed to create Terraform client: %w", err)
				}

				ran, err := maybeRunTerraformCommand(ctx, question, tfClient)
				if err != nil {
					return err
				}
				if ran {
					return nil
				}

				terraformContext, err = tfClient.GetRelevantContext(ctx, question)
				if err != nil {
					return fmt.Errorf("failed to get Terraform context: %w", err)
				}
			}
		}

		if includeGCP {
			var gcpClient *gcp.Client
			var gcpCredsFile string

			// Check for backend API key first
			apiKeyFlag, _ := cmd.Flags().GetString("api-key")
			if apiKeyFlag == "" {
				apiKeyFlag, _ = cmd.Root().PersistentFlags().GetString("api-key")
			}
			backendAPIKey := backend.ResolveAPIKey(apiKeyFlag)

			if backendAPIKey != "" {
				// Try to get credentials from backend
				backendClient := backend.NewClient(backendAPIKey, debug)
				backendCreds, backendErr := backendClient.GetGCPCredentials(ctx)
				if backendErr == nil && backendCreds.ProjectID != "" {
					if debug {
						fmt.Println("[backend] Using GCP credentials from backend")
					}
					var err error
					gcpClient, gcpCredsFile, err = gcp.NewClientWithCredentials(&gcp.BackendGCPCredentials{
						ProjectID:          backendCreds.ProjectID,
						ServiceAccountJSON: backendCreds.ServiceAccountJSON,
					}, debug)
					if err != nil {
						return fmt.Errorf("failed to create GCP client with backend credentials: %w", err)
					}
					if gcpCredsFile != "" {
						defer gcp.CleanupCredentialsFile(gcpCredsFile)
					}
				} else if debug {
					fmt.Printf("[backend] No GCP credentials available (%v), falling back to local\n", backendErr)
				}
			}

			// Fall back to local config if backend credentials not available
			if gcpClient == nil {
				projectID := gcpProject
				if projectID == "" {
					projectID = gcp.ResolveProjectID()
				}
				if projectID == "" {
					return fmt.Errorf("gcp project_id is required (set infra.gcp.project_id or use --gcp-project)")
				}

				var err error
				gcpClient, err = gcp.NewClient(projectID, debug)
				if err != nil {
					return fmt.Errorf("failed to create GCP client: %w", err)
				}
			}

			var err error
			gcpContext, err = gcpClient.GetRelevantContext(ctx, question)
			if err != nil {
				return fmt.Errorf("failed to get GCP context: %w", err)
			}
		}

		if includeAzure {
			var azureClient *azure.Client

			// Check for backend API key first
			apiKeyFlag, _ := cmd.Flags().GetString("api-key")
			if apiKeyFlag == "" {
				apiKeyFlag, _ = cmd.Root().PersistentFlags().GetString("api-key")
			}
			backendAPIKey := backend.ResolveAPIKey(apiKeyFlag)

			if backendAPIKey != "" {
				// Try to get credentials from backend
				backendClient := backend.NewClient(backendAPIKey, debug)
				backendCreds, backendErr := backendClient.GetAzureCredentials(ctx)
				if backendErr == nil && backendCreds.SubscriptionID != "" {
					if debug {
						fmt.Println("[backend] Using Azure credentials from backend")
					}
					var err error
					azureClient, err = azure.NewClientWithCredentials(&azure.BackendAzureCredentials{
						SubscriptionID: backendCreds.SubscriptionID,
						TenantID:       backendCreds.TenantID,
						ClientID:       backendCreds.ClientID,
						ClientSecret:   backendCreds.ClientSecret,
					}, debug)
					if err != nil {
						return fmt.Errorf("failed to create Azure client with backend credentials: %w", err)
					}
				} else if debug {
					fmt.Printf("[backend] No Azure credentials available (%v), falling back to local\n", backendErr)
				}
			}

			// Fall back to local config if backend credentials not available
			if azureClient == nil {
				sub := strings.TrimSpace(azureSubscription)
				if sub == "" {
					sub = azure.ResolveSubscriptionID()
				}
				if sub == "" {
					return fmt.Errorf("azure subscription_id is required (set infra.azure.subscription_id, AZURE_SUBSCRIPTION_ID, or use --azure-subscription)")
				}
				var err error
				azureClient, err = azure.NewClient(sub, debug)
				if err != nil {
					return fmt.Errorf("failed to create Azure client: %w", err)
				}
			}

			var err error
			azureContext, err = azureClient.GetRelevantContext(ctx, question)
			if err != nil {
				return fmt.Errorf("failed to get Azure context: %w", err)
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
				// Check for backend API key first
				apiKeyFlag, _ := cmd.Flags().GetString("api-key")
				if apiKeyFlag == "" {
					apiKeyFlag, _ = cmd.Root().PersistentFlags().GetString("api-key")
				}
				backendAPIKey := backend.ResolveAPIKey(apiKeyFlag)

				if backendAPIKey != "" {
					// Try to get credentials from backend
					backendClient := backend.NewClient(backendAPIKey, debug)
					backendCreds, backendErr := backendClient.GetAWSCredentials(ctx)
					if backendErr == nil {
						if debug {
							fmt.Println("[backend] Using AWS credentials from backend for tool calling")
						}
						awsClient, err = aws.NewClientWithCredentials(ctx, &aws.BackendAWSCredentials{
							AccessKeyID:     backendCreds.AccessKeyID,
							SecretAccessKey: backendCreds.SecretAccessKey,
							Region:          backendCreds.Region,
							SessionToken:    backendCreds.SessionToken,
						}, debug)
						if err != nil {
							return fmt.Errorf("failed to create AWS client with backend credentials: %w", err)
						}
					} else if debug {
						fmt.Printf("[backend] No AWS credentials available for tools (%v), falling back to local\n", backendErr)
					}
				}

				// Fall back to local profile if backend credentials not available
				if awsClient == nil {
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

			maybeOverrideProviderModel(provider, openaiModel, anthropicModel, geminiModel, deepseekModel, minimaxModel)

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
				apiKey = resolveAnthropicKey(anthropicKey)
			case "deepseek":
				apiKey = resolveDeepSeekKey(deepseekKey)
			case "minimax":
				apiKey = resolveMiniMaxKey(minimaxKey)
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

			maybeOverrideProviderModel(provider, openaiModel, anthropicModel, geminiModel, deepseekModel, minimaxModel)

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
				apiKey = resolveAnthropicKey(anthropicKey)
			case "deepseek":
				apiKey = resolveDeepSeekKey(deepseekKey)
			case "minimax":
				apiKey = resolveMiniMaxKey(minimaxKey)
			default:
				// Default/other providers
				apiKey = viper.GetString("ai.api_key")
			}

			aiClient = ai.NewClient(provider, apiKey, debug, aiProfile)
		}

		// Only Terraform context is supported here (code scanning disabled).
		combinedCodeContext := terraformContext
		if strings.TrimSpace(gcpContext) != "" {
			if combinedCodeContext != "" {
				combinedCodeContext += "\n"
			}
			combinedCodeContext += "GCP Context:\n" + gcpContext
		}
		if strings.TrimSpace(azureContext) != "" {
			if combinedCodeContext != "" {
				combinedCodeContext += "\n"
			}
			combinedCodeContext += "Azure Context:\n" + azureContext
		}

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
	askCmd.Flags().Bool("gcp", false, "Include GCP infrastructure context")
	askCmd.Flags().Bool("azure", false, "Include Azure infrastructure context")
	askCmd.Flags().Bool("cloudflare", false, "Include Cloudflare infrastructure context")
	askCmd.Flags().Bool("github", false, "Include GitHub repository context")
	askCmd.Flags().Bool("terraform", false, "Include Terraform workspace context")
	askCmd.Flags().Bool("iam", false, "Route query to IAM agent for security analysis")
	askCmd.Flags().String("role-arn", "", "Scope IAM query to a specific role ARN")
	askCmd.Flags().String("policy-arn", "", "Scope IAM query to a specific policy ARN")
	askCmd.Flags().Bool("discovery", false, "Run comprehensive infrastructure discovery (all services)")
	askCmd.Flags().Bool("compliance", false, "Generate compliance report showing all services, ports, and protocols")
	askCmd.Flags().String("profile", "", "AWS profile to use for infrastructure queries")
	askCmd.Flags().String("gcp-project", "", "GCP project ID to use for infrastructure queries")
	askCmd.Flags().String("azure-subscription", "", "Azure subscription ID to use for infrastructure queries")
	askCmd.Flags().String("workspace", "", "Terraform workspace to use for infrastructure queries")
	askCmd.Flags().String("ai-profile", "", "AI profile to use (default: 'default')")
	askCmd.Flags().String("openai-key", "", "OpenAI API key (overrides config)")
	askCmd.Flags().String("anthropic-key", "", "Anthropic API key (overrides config)")
	askCmd.Flags().String("gemini-key", "", "Gemini API key (overrides config and env vars)")
	askCmd.Flags().String("deepseek-key", "", "DeepSeek API key (overrides config)")
	askCmd.Flags().String("minimax-key", "", "MiniMax API key (overrides config)")
	askCmd.Flags().String("openai-model", "", "OpenAI model to use (overrides config)")
	askCmd.Flags().String("anthropic-model", "", "Anthropic model to use (overrides config)")
	askCmd.Flags().String("gemini-model", "", "Gemini model to use (overrides config)")
	askCmd.Flags().String("deepseek-model", "", "DeepSeek model to use (overrides config)")
	askCmd.Flags().String("minimax-model", "", "MiniMax model to use (overrides config)")
	askCmd.Flags().Bool("agent-trace", false, "Show detailed coordinator agent lifecycle logs (overrides config)")
	askCmd.Flags().Bool("maker", false, "Generate an AWS, GCP, Azure, or Cloudflare CLI plan (JSON) for infrastructure changes")
	askCmd.Flags().Bool("destroyer", false, "Allow destructive operations when using --maker (requires explicit confirmation in UI/workflow)")
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

func resolveAnthropicKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if key := viper.GetString("ai.providers.anthropic.api_key"); key != "" {
		return key
	}
	if envName := viper.GetString("ai.providers.anthropic.api_key_env"); envName != "" {
		if envVal := os.Getenv(envName); envVal != "" {
			return envVal
		}
	}
	if envVal := os.Getenv("ANTHROPIC_API_KEY"); envVal != "" {
		return envVal
	}
	return ""
}

func resolveDeepSeekKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if key := viper.GetString("ai.providers.deepseek.api_key"); key != "" {
		return key
	}
	if envName := viper.GetString("ai.providers.deepseek.api_key_env"); envName != "" {
		if envVal := os.Getenv(envName); envVal != "" {
			return envVal
		}
	}
	if envVal := os.Getenv("DEEPSEEK_API_KEY"); envVal != "" {
		return envVal
	}
	return ""
}

func resolveMiniMaxKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if key := viper.GetString("ai.providers.minimax.api_key"); key != "" {
		return key
	}
	if envName := viper.GetString("ai.providers.minimax.api_key_env"); envName != "" {
		if envVal := os.Getenv(envName); envVal != "" {
			return envVal
		}
	}
	if envVal := os.Getenv("MINIMAX_API_KEY"); envVal != "" {
		return envVal
	}
	return ""
}

func maybeRunTerraformCommand(ctx context.Context, question string, tfClient *tfclient.Client) (bool, error) {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return false, nil
	}

	isInit := strings.Contains(q, "terraform init") || strings.Contains(q, "init terraform")
	isPlan := strings.Contains(q, "terraform plan") || strings.Contains(q, "plan terraform")
	isApply := strings.Contains(q, "terraform apply") || strings.Contains(q, "apply terraform")
	applyConfirmed := strings.Contains(q, "confirm apply") || strings.Contains(q, "approved apply") || strings.Contains(q, "apply confirmed")

	if !isInit && !isPlan && !isApply {
		return false, nil
	}

	var output string
	var err error
	if isInit {
		output, err = tfClient.RunInit(ctx)
	} else if isPlan {
		output, err = tfClient.RunPlan(ctx)
	} else if isApply {
		if !applyConfirmed {
			return true, fmt.Errorf("terraform apply requires confirmation: include 'confirm apply' in your request")
		}
		output, err = tfClient.RunApply(ctx)
	}
	if err != nil {
		return true, err
	}

	if output != "" {
		fmt.Println(output)
	}
	return true, nil
}

func maybeOverrideProviderModel(provider, openaiModel, anthropicModel, geminiModel, deepseekModel, minimaxModel string) {
	switch provider {
	case "openai":
		if strings.TrimSpace(openaiModel) != "" {
			viper.Set("ai.providers.openai.model", strings.TrimSpace(openaiModel))
		}
	case "anthropic":
		if strings.TrimSpace(anthropicModel) != "" {
			viper.Set("ai.providers.anthropic.model", strings.TrimSpace(anthropicModel))
		}
	case "gemini", "gemini-api":
		if model := resolveGeminiModel(provider, geminiModel); model != "" {
			viper.Set(fmt.Sprintf("ai.providers.%s.model", provider), model)
		}
	case "deepseek":
		if strings.TrimSpace(deepseekModel) != "" {
			viper.Set("ai.providers.deepseek.model", strings.TrimSpace(deepseekModel))
		}
	case "minimax":
		if strings.TrimSpace(minimaxModel) != "" {
			viper.Set("ai.providers.minimax.model", strings.TrimSpace(minimaxModel))
		}
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

func questionForRouting(question string) string {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return trimmed
	}

	// If the prompt contains a chat transcript (as emitted by clanker-cloud),
	// route based on the last explicit user turn.
	// Format we expect (roughly):
	//   You\n<question>\n\nClanker\n...
	start := strings.LastIndex(trimmed, "\nYou\n")
	startLen := len("\nYou\n")
	if start == -1 && strings.HasPrefix(trimmed, "You\n") {
		start = 0
		startLen = len("You\n")
	}

	if start != -1 {
		candidate := trimmed[start+startLen:]
		// End at next assistant turn marker if present.
		if end := strings.Index(candidate, "\n\nClanker\n"); end != -1 {
			candidate = candidate[:end]
		} else if end := strings.Index(candidate, "\nClanker\n"); end != -1 {
			candidate = candidate[:end]
		}
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}

	// Generic fallback: if a prompt appends one or more sections like
	// "Current <something> context:", route on the text before the first such section.
	lower := strings.ToLower(trimmed)
	if idx := strings.Index(lower, "\ncurrent "); idx != -1 {
		if strings.Contains(lower[idx:], " context:") {
			before := strings.TrimSpace(trimmed[:idx])
			if before != "" {
				return before
			}
		}
	}

	return trimmed
}

// handleCloudflareQuery delegates a Cloudflare query to the Cloudflare agent
func handleCloudflareQuery(ctx context.Context, question string, debug bool) error {
	if debug {
		fmt.Println("Delegating query to Cloudflare agent...")
	}

	var client *cloudflare.Client
	var err error

	// Check for backend API key first
	backendAPIKey := backend.ResolveAPIKey("")
	if backendAPIKey != "" {
		backendClient := backend.NewClient(backendAPIKey, debug)
		backendCreds, backendErr := backendClient.GetCloudflareCredentials(ctx)
		if backendErr == nil && backendCreds.APIToken != "" {
			if debug {
				fmt.Println("[backend] Using Cloudflare credentials from backend")
			}
			client, err = cloudflare.NewClientWithCredentials(&cloudflare.BackendCloudflareCredentials{
				APIToken:  backendCreds.APIToken,
				AccountID: backendCreds.AccountID,
			}, debug)
			if err != nil {
				return fmt.Errorf("failed to create Cloudflare client with backend credentials: %w", err)
			}
		} else if debug {
			fmt.Printf("[backend] No Cloudflare credentials available (%v), falling back to local\n", backendErr)
		}
	}

	// Fall back to local config if backend credentials not available
	if client == nil {
		accountID := cloudflare.ResolveAccountID()
		apiToken := cloudflare.ResolveAPIToken()

		if apiToken == "" {
			return fmt.Errorf("cloudflare api_token is required (set cloudflare.api_token, CLOUDFLARE_API_TOKEN, or CF_API_TOKEN)")
		}

		client, err = cloudflare.NewClient(accountID, apiToken, debug)
		if err != nil {
			return fmt.Errorf("failed to create Cloudflare client: %w", err)
		}
	}

	// Determine query type
	questionLower := strings.ToLower(question)

	// Check for WAF/Security queries
	isWAF := strings.Contains(questionLower, "firewall") ||
		strings.Contains(questionLower, "waf") ||
		strings.Contains(questionLower, "rate limit") ||
		strings.Contains(questionLower, "security level") ||
		strings.Contains(questionLower, "under attack") ||
		strings.Contains(questionLower, "ddos") ||
		strings.Contains(questionLower, "bot")

	if isWAF {
		// Use WAF subagent
		wafAgent := cfwaf.NewSubAgent(client, debug)
		opts := cfwaf.QueryOptions{}

		response, err := wafAgent.HandleQuery(ctx, question, opts)
		if err != nil {
			return fmt.Errorf("Cloudflare WAF agent error: %w", err)
		}

		switch response.Type {
		case cfwaf.ResponseTypePlan:
			planJSON, err := json.MarshalIndent(response.Plan, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to format plan: %w", err)
			}
			fmt.Println(string(planJSON))
			fmt.Println("\n// To apply this plan, run:")
			fmt.Println("// clanker ask --apply --plan-file <save-above-to-file.json>")
		case cfwaf.ResponseTypeResult:
			fmt.Println(response.Result)
		case cfwaf.ResponseTypeError:
			return response.Error
		}
		return nil
	}

	// Check for Workers queries
	isWorkers := strings.Contains(questionLower, "worker") ||
		strings.Contains(questionLower, "kv") ||
		strings.Contains(questionLower, "d1") ||
		strings.Contains(questionLower, "r2") ||
		strings.Contains(questionLower, "pages") ||
		strings.Contains(questionLower, "durable object")

	if isWorkers {
		// Use Workers subagent
		workersAgent := cfworkers.NewSubAgent(client, debug)
		opts := cfworkers.QueryOptions{
			AccountID: client.GetAccountID(),
		}

		response, err := workersAgent.HandleQuery(ctx, question, opts)
		if err != nil {
			return fmt.Errorf("Cloudflare Workers agent error: %w", err)
		}

		switch response.Type {
		case cfworkers.ResponseTypePlan:
			planJSON, err := json.MarshalIndent(response.Plan, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to format plan: %w", err)
			}
			fmt.Println(string(planJSON))
			fmt.Println("\n// To apply this plan, run:")
			fmt.Println("// clanker ask --apply --plan-file <save-above-to-file.json>")
		case cfworkers.ResponseTypeResult:
			fmt.Println(response.Result)
		case cfworkers.ResponseTypeError:
			return response.Error
		}
		return nil
	}

	// Check for Analytics queries
	isAnalytics := strings.Contains(questionLower, "analytics") ||
		strings.Contains(questionLower, "traffic") ||
		strings.Contains(questionLower, "bandwidth") ||
		strings.Contains(questionLower, "requests") ||
		strings.Contains(questionLower, "visitors") ||
		strings.Contains(questionLower, "page views") ||
		strings.Contains(questionLower, "performance metrics")

	if isAnalytics {
		// Use Analytics subagent
		analyticsAgent := cfanalytics.NewSubAgent(client, debug)
		opts := cfanalytics.QueryOptions{}

		response, err := analyticsAgent.HandleQuery(ctx, question, opts)
		if err != nil {
			return fmt.Errorf("Cloudflare Analytics agent error: %w", err)
		}

		switch response.Type {
		case cfanalytics.ResponseTypeResult:
			fmt.Println(response.Result)
		case cfanalytics.ResponseTypeError:
			return response.Error
		}
		return nil
	}

	// Check for Zero Trust queries
	isZeroTrust := strings.Contains(questionLower, "tunnel") ||
		strings.Contains(questionLower, "access app") ||
		strings.Contains(questionLower, "access policy") ||
		strings.Contains(questionLower, "zero trust") ||
		strings.Contains(questionLower, "cloudflared") ||
		strings.Contains(questionLower, "warp")

	if isZeroTrust {
		// Use Zero Trust subagent
		ztAgent := cfzerotrust.NewSubAgent(client, debug)
		opts := cfzerotrust.QueryOptions{
			AccountID: client.GetAccountID(),
		}

		response, err := ztAgent.HandleQuery(ctx, question, opts)
		if err != nil {
			return fmt.Errorf("Cloudflare Zero Trust agent error: %w", err)
		}

		switch response.Type {
		case cfzerotrust.ResponseTypePlan:
			planJSON, err := json.MarshalIndent(response.Plan, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to format plan: %w", err)
			}
			fmt.Println(string(planJSON))
			fmt.Println("\n// To apply this plan, run:")
			fmt.Println("// clanker ask --apply --plan-file <save-above-to-file.json>")
		case cfzerotrust.ResponseTypeResult:
			fmt.Println(response.Result)
		case cfzerotrust.ResponseTypeError:
			return response.Error
		}
		return nil
	}

	// Check for DNS queries
	isDNS := strings.Contains(questionLower, "dns") ||
		strings.Contains(questionLower, "record") ||
		strings.Contains(questionLower, "zone") ||
		strings.Contains(questionLower, "domain") ||
		strings.Contains(questionLower, "cname") ||
		strings.Contains(questionLower, "a record") ||
		strings.Contains(questionLower, "mx") ||
		strings.Contains(questionLower, "txt") ||
		strings.Contains(questionLower, "nameserver")

	if isDNS {
		// Use DNS subagent
		dnsAgent := cfdns.NewSubAgent(client, debug)
		opts := cfdns.QueryOptions{}

		response, err := dnsAgent.HandleQuery(ctx, question, opts)
		if err != nil {
			return fmt.Errorf("Cloudflare DNS agent error: %w", err)
		}

		switch response.Type {
		case cfdns.ResponseTypePlan:
			planJSON, err := json.MarshalIndent(response.Plan, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to format plan: %w", err)
			}
			fmt.Println(string(planJSON))
			fmt.Println("\n// To apply this plan, run:")
			fmt.Println("// clanker ask --apply --plan-file <save-above-to-file.json>")
		case cfdns.ResponseTypeResult:
			fmt.Println(response.Result)
		case cfdns.ResponseTypeError:
			return response.Error
		}
		return nil
	}

	// For non-DNS queries, use the general Cloudflare context
	cfContext, err := client.GetRelevantContext(ctx, question)
	if err != nil {
		return fmt.Errorf("failed to get Cloudflare context: %w", err)
	}

	// Get AI provider settings
	aiProfile := viper.GetString("ai.default_provider")
	if aiProfile == "" {
		aiProfile = "openai"
	}

	var apiKey string
	switch aiProfile {
	case "gemini", "gemini-api":
		apiKey = ""
	case "openai":
		apiKey = viper.GetString("ai.providers.openai.api_key")
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
	default:
		apiKey = viper.GetString("ai.api_key")
	}

	aiClient := ai.NewClient(aiProfile, apiKey, debug, aiProfile)

	// Build prompt with Cloudflare context
	prompt := fmt.Sprintf(`You are a Cloudflare infrastructure assistant. Answer the following question based on the Cloudflare account context provided.

Question: %s

Cloudflare Account Context:
%s

Provide a clear and helpful response.`, question, cfContext)

	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return fmt.Errorf("failed to get AI response: %w", err)
	}

	fmt.Println(response)
	return nil
}

// handleIAMQuery delegates an IAM query to the IAM agent
func handleIAMQuery(ctx context.Context, question string, debug bool, roleARN, policyARN string) error {
	if debug {
		fmt.Println("Delegating query to IAM agent...")
		if roleARN != "" {
			fmt.Printf("  Role ARN: %s\n", roleARN)
		}
		if policyARN != "" {
			fmt.Printf("  Policy ARN: %s\n", policyARN)
		}
	}

	// Resolve AWS profile
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

	// Create IAM agent
	iamAgent, err := iamclient.NewAgentWithOptions(iamclient.AgentOptions{
		Profile: awsProfile,
		Region:  awsRegion,
		Debug:   debug,
	})
	if err != nil {
		return fmt.Errorf("failed to create IAM agent: %w", err)
	}

	// Configure query options - scope to specific resource if ARN provided
	opts := iamclient.QueryOptions{
		AccountWide: roleARN == "" && policyARN == "",
		RoleARN:     roleARN,
		PolicyARN:   policyARN,
	}

	// Handle the query
	response, err := iamAgent.HandleQuery(ctx, question, opts)
	if err != nil {
		return fmt.Errorf("IAM agent error: %w", err)
	}

	// Output based on response type
	switch response.Type {
	case iamclient.ResponseTypePlan:
		// Output plan
		fmt.Println(response.Content)
		fmt.Println("\n// To apply this plan, review the commands and run them manually")

	case iamclient.ResponseTypeFindings:
		fmt.Println(response.Content)

	case iamclient.ResponseTypeResult:
		fmt.Println(response.Content)

	case iamclient.ResponseTypeError:
		return response.Error
	}

	return nil
}

// handleK8sQuery delegates a Kubernetes query to the K8s agent
func handleK8sQuery(ctx context.Context, question string, debug bool, kubeconfig string) error {
	if debug {
		fmt.Println("Delegating query to K8s agent...")
	}

	// Check for backend Kubernetes credentials first
	var backendKubeconfigPath string
	backendAPIKey := backend.ResolveAPIKey("")
	if backendAPIKey != "" && kubeconfig == "" {
		backendClient := backend.NewClient(backendAPIKey, debug)
		backendCreds, backendErr := backendClient.GetKubernetesCredentials(ctx)
		if backendErr == nil && backendCreds.KubeconfigContent != "" {
			if debug {
				fmt.Println("[backend] Using Kubernetes credentials from backend")
			}
			_, tempPath, err := k8s.NewClientWithCredentials(&k8s.BackendKubernetesCredentials{
				KubeconfigContent: backendCreds.KubeconfigContent,
				ContextName:       backendCreds.ContextName,
			}, debug)
			if err == nil {
				kubeconfig = tempPath
				backendKubeconfigPath = tempPath
				defer k8s.CleanupKubeconfig(backendKubeconfigPath)
			} else if debug {
				fmt.Printf("[backend] Failed to use backend kubeconfig: %v\n", err)
			}
		} else if debug {
			fmt.Printf("[backend] No Kubernetes credentials available (%v), falling back to local\n", backendErr)
		}
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
		// Output plan as JSON (like AWS maker)
		planJSON, err := json.MarshalIndent(response.Plan, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format plan: %w", err)
		}
		fmt.Println(string(planJSON))
		fmt.Println("\n// To apply this plan, run:")
		fmt.Println("// clanker ask --apply --plan-file <save-above-to-file.json>")

	case k8s.ResponseTypeResult:
		fmt.Println(response.Result)

	case k8s.ResponseTypeError:
		return response.Error
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
		strings.Contains(rawPlan, `"kubeadm"`) ||
		strings.Contains(rawPlan, `"helm_cmds"`) ||
		strings.Contains(rawPlan, `"helm"`)
}

// executeK8sPlan executes a K8s plan (supports both K8sPlan with helm_cmds and MakerPlan formats)
func executeK8sPlan(ctx context.Context, rawPlan string, profile string, debug bool) error {
	// First try to parse as K8sPlan (with helm_cmds)
	var k8sPlan k8s.K8sPlan
	if err := json.Unmarshal([]byte(rawPlan), &k8sPlan); err == nil && len(k8sPlan.HelmCmds) > 0 {
		fmt.Printf("\n[k8s] Executing plan: %s\n", k8sPlan.Summary)
		fmt.Println(strings.Repeat("-", 60))

		// Execute helm commands
		totalSteps := len(k8sPlan.HelmCmds) + len(k8sPlan.KubectlCmds)
		stepNum := 0

		for _, helmCmd := range k8sPlan.HelmCmds {
			stepNum++
			args := buildHelmArgs(helmCmd)
			if len(args) == 0 {
				continue
			}

			fmt.Printf("[k8s] running %d/%d: helm %s\n", stepNum, totalSteps, strings.Join(args, " "))

			cmd := exec.CommandContext(ctx, "helm", args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			if err := cmd.Run(); err != nil {
				return fmt.Errorf("helm command failed: %w", err)
			}
			fmt.Println()
		}

		// Execute kubectl commands
		for _, kubectlCmd := range k8sPlan.KubectlCmds {
			stepNum++
			fmt.Printf("[k8s] running %d/%d: kubectl %s\n", stepNum, totalSteps, strings.Join(kubectlCmd.Args, " "))

			cmd := exec.CommandContext(ctx, "kubectl", kubectlCmd.Args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			if err := cmd.Run(); err != nil {
				return fmt.Errorf("kubectl command failed: %w", err)
			}
			fmt.Println()
		}

		fmt.Println(strings.Repeat("-", 60))
		fmt.Println("[k8s] Plan executed successfully!")
		return nil
	}

	// Fall back to MakerPlan format (eksctl, kubectl, kubeadm commands)
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

	fmt.Printf("\n[k8s] Executing plan: %s\n", makerPlan.Summary)
	fmt.Println(strings.Repeat("-", 60))

	// Execute each command
	for i, cmd := range makerPlan.Commands {
		if len(cmd.Args) == 0 {
			continue
		}

		cmdName := cmd.Args[0]
		cmdArgs := cmd.Args[1:]

		// Handle eks commands - they need to run as "aws eks ..."
		if cmdName == "eks" {
			cmdName = "aws"
			cmdArgs = append([]string{"eks"}, cmdArgs...)
		}

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

	fmt.Println(strings.Repeat("-", 60))
	fmt.Println("[k8s] Plan executed successfully!")
	return nil
}

// determineRoutingDecision analyzes a question and returns which agent should handle it.
// This is used by the --route-only flag to return routing decisions without executing.
func determineRoutingDecision(question string) (agent string, reason string) {
	questionLower := strings.ToLower(question)

	// Check for IAM-specific queries first
	iamKeywords := []string{
		"iam role", "iam roles", "iam policy", "iam policies",
		"iam user", "iam users", "iam permission", "iam permissions",
		"trust policy", "assume role", "attached policies",
		"inline policies", "permission boundary",
		"access key", "access keys", "credential report",
		"least privilege", "security audit", "iam analysis",
		"overpermissive", "admin access", "cross-account trust",
		"mfa status", "unused role", "wildcard permission",
		"analyze iam", "fix iam", "iam security",
	}
	for _, kw := range iamKeywords {
		if strings.Contains(questionLower, kw) {
			return "iam", "IAM query or security analysis request"
		}
	}

	terraformSignals := []string{
		"terraform", "tf ", "tfstate", "tf plan", "tf apply", "tf destroy",
		"hcl", "module", "provider", "workspace", "state", "plan", "apply", "destroy",
		"drift", "refresh", "init",
	}
	for _, kw := range terraformSignals {
		if strings.Contains(questionLower, kw) {
			return "terraform", "Terraform query or analysis request"
		}
	}

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
