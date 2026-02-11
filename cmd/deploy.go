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
	"github.com/bgdnvk/clanker/internal/deploy"
	"github.com/bgdnvk/clanker/internal/maker"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var deployCmd = &cobra.Command{
	Use:   "deploy [repo-url]",
	Short: "Analyze and deploy a GitHub repo to the cloud",
	Long: `Clone a GitHub repository, analyze its stack, and generate a deployment plan.

Examples:
  clanker deploy https://github.com/user/repo
  clanker deploy https://github.com/user/repo --apply
  clanker deploy https://github.com/user/repo --target ec2
  clanker deploy https://github.com/user/repo --target eks
  clanker deploy https://github.com/user/repo --provider cloudflare
  clanker deploy https://github.com/user/repo --profile prod`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoURL := args[0]
		// Create deployment context with 20-minute timeout
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		debug := viper.GetBool("debug")
		profile, _ := cmd.Flags().GetString("profile")
		applyMode, _ := cmd.Flags().GetBool("apply")
		aiProfile, _ := cmd.Flags().GetString("ai-profile")
		openaiKey, _ := cmd.Flags().GetString("openai-key")
		anthropicKey, _ := cmd.Flags().GetString("anthropic-key")
		geminiKey, _ := cmd.Flags().GetString("gemini-key")
		targetProvider, _ := cmd.Flags().GetString("provider")
		deployTarget, _ := cmd.Flags().GetString("target")
		instanceType, _ := cmd.Flags().GetString("instance-type")
		newVPC, _ := cmd.Flags().GetBool("new-vpc")

		// 1. Clone + analyze
		fmt.Fprintf(os.Stderr, "[deploy] cloning %s ...\n", repoURL)
		rp, err := deploy.CloneAndAnalyze(ctx, repoURL)
		if err != nil {
			return fmt.Errorf("analysis failed: %w", err)
		}
		defer os.RemoveAll(rp.ClonePath)

		fmt.Fprintf(os.Stderr, "[deploy] analysis: %s\n", rp.Summary)

		// 2. Resolve AI provider + key (need it for architect call too)
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
			if anthropicKey != "" {
				apiKey = anthropicKey
			} else {
				apiKey = viper.GetString("ai.providers.anthropic.api_key_env")
			}
		default:
			apiKey = viper.GetString("ai.api_key")
		}

		aiClient := ai.NewClient(provider, apiKey, debug, aiProfile)

		// log helper
		logf := func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}

		// 3. Resolve AWS profile/region early so intelligence pipeline can scan infra
		var targetProfile, region string
		if targetProvider != "cloudflare" {
			targetProfile = resolveAWSProfile(profile)
			region = resolveAWSRegion(ctx, targetProfile)
		}

		// Build deploy options from flags
		deployOpts := &deploy.DeployOptions{
			Target:       deployTarget,
			InstanceType: instanceType,
			NewVPC:       newVPC,
		}

		// 4. Run multi-phase intelligence pipeline (explore → deep analysis → infra scan → architecture)
		intel, err := deploy.RunIntelligence(ctx, rp,
			aiClient.AskPrompt,
			aiClient.CleanJSONResponse,
			debug, targetProvider, targetProfile, region, deployOpts, logf,
		)
		if err != nil {
			return fmt.Errorf("intelligence pipeline failed: %w", err)
		}

		// 4.5. Prompt user for required configuration (Node.js apps)
		var userConfig *deploy.UserConfig
		if intel.DeepAnalysis != nil && rp.Language == "node" {
			// Show detected app info
			if intel.DeepAnalysis.ListeningPort > 0 {
				fmt.Fprintf(os.Stderr, "[deploy] detected port from analysis: %d\n", intel.DeepAnalysis.ListeningPort)
				// Update RepoProfile with detected port
				if len(rp.Ports) == 0 || rp.Ports[0] != intel.DeepAnalysis.ListeningPort {
					rp.Ports = []int{intel.DeepAnalysis.ListeningPort}
				}
			}

			// Collect user config if there are required env vars
			if len(intel.DeepAnalysis.RequiredEnvVars) > 0 || len(intel.DeepAnalysis.OptionalEnvVars) > 0 {
				userConfig, err = deploy.PromptForConfig(intel.DeepAnalysis, rp)
				if err != nil {
					return fmt.Errorf("configuration failed: %w", err)
				}
			}
		}

		// Default config if none collected
		if userConfig == nil {
			userConfig = deploy.DefaultUserConfig(intel.DeepAnalysis, rp)
		}

		enrichedQuestion := intel.EnrichedPrompt
		if debug {
			fmt.Fprintf(os.Stderr, "[deploy] enriched prompt:\n%s\n", enrichedQuestion)
		}

		// 4. Generate the maker plan via LLM
		fmt.Fprintf(os.Stderr, "[deploy] phase 3: generating execution plan with %s ...\n", provider)

		const maxValidationRounds = 5
		var plan *maker.Plan

		for round := 0; round <= maxValidationRounds; round++ {
			prompt := maker.PlanPromptWithMode(enrichedQuestion, false)
			resp, err := aiClient.AskPrompt(ctx, prompt)
			if err != nil {
				return fmt.Errorf("plan generation failed: %w", err)
			}

			cleaned := aiClient.CleanJSONResponse(resp)
			plan, err = maker.ParsePlan(cleaned)
			if err != nil {
				return fmt.Errorf("failed to parse plan: %w", err)
			}

			plan.Provider = intel.Architecture.Provider
			plan.Question = enrichedQuestion
			if plan.CreatedAt.IsZero() {
				plan.CreatedAt = time.Now().UTC()
			}
			if plan.Version == 0 {
				plan.Version = maker.CurrentPlanVersion
			}

			// skip validation on last round
			if round == maxValidationRounds {
				break
			}

			// 5. Validate the plan (LLM reviews its own work)
			planJSON, _ := json.MarshalIndent(plan, "", "  ")
			validation, fixPrompt, err := deploy.ValidatePlan(ctx,
				string(planJSON), rp, intel.DeepAnalysis,
				aiClient.AskPrompt, aiClient.CleanJSONResponse, logf,
			)
			if err != nil {
				logf("[deploy] warning: validation failed (%v), using plan as-is", err)
				break
			}

			intel.Validation = validation

			if validation.IsValid {
				logf("[deploy] plan validated successfully")
				if len(validation.Warnings) > 0 {
					for _, w := range validation.Warnings {
						logf("[deploy] warning: %s", w)
					}
				}
				break
			}

			// plan has issues — feed fixes back into prompt and retry
			logf("[deploy] validation found %d issues, regenerating (round %d/%d)...",
				len(validation.Issues), round+1, maxValidationRounds)
			for _, issue := range validation.Issues {
				logf("[deploy]   issue: %s", issue)
			}
			enrichedQuestion += fixPrompt
		}

		// 6. Enrich w/ existing infra context (skip for Cloudflare)
		if targetProvider != "cloudflare" {
			_ = maker.EnrichPlan(ctx, plan, maker.ExecOptions{
				Profile: targetProfile, Region: region, Writer: io.Discard,
			})
		}

		// 7. Resolve placeholders before output
		// Always apply static bindings (AMI_ID, ACCOUNT_ID, REGION) - even with --new-vpc
		if targetProvider != "cloudflare" && intel.InfraSnap != nil {
			plan = deploy.ApplyStaticInfraBindings(plan, intel.InfraSnap)
		}

		// Full placeholder resolution (skip for Cloudflare and --new-vpc since those use 'produces' chaining)
		if targetProvider != "cloudflare" && !newVPC {
			const maxPlaceholderRounds = 5
			for round := 1; round <= maxPlaceholderRounds; round++ {
				if !deploy.HasUnresolvedPlaceholders(plan) {
					break
				}

				logf("[deploy] resolving placeholders (round %d/%d)...", round, maxPlaceholderRounds)
				resolved, unresolved, err := deploy.ResolvePlanPlaceholders(
					ctx, plan, intel.InfraSnap,
					aiClient.AskPrompt, aiClient.CleanJSONResponse, logf,
				)
				if err != nil {
					logf("[deploy] warning: placeholder resolution failed: %v", err)
					break
				}
				plan = resolved

				if len(unresolved) == 0 {
					logf("[deploy] all placeholders resolved")
					break
				}

				if round == maxPlaceholderRounds {
					logf("[deploy] warning: %d placeholders remain unresolved after %d rounds: %v",
						len(unresolved), maxPlaceholderRounds, unresolved)
				}
			}
		}

		// 8. Output plan JSON (or apply)
		planJSON, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}

		if !applyMode {
			fmt.Println(string(planJSON))
			return nil
		}

		// apply mode: execute the plan in phases
		fmt.Fprintf(os.Stderr, "[deploy] applying plan (%d commands)...\n", len(plan.Commands))

		// Split plan: infrastructure first, then app deployment (after Docker build)
		infraPlan, appPlan := splitPlanAtDockerBuild(plan)

		outputBindings := make(map[string]string)

		// Inject user config into output bindings for native Node.js deployment
		if userConfig != nil {
			for name, value := range userConfig.EnvVars {
				outputBindings["ENV_"+name] = value
			}
			outputBindings["APP_PORT"] = fmt.Sprintf("%d", userConfig.AppPort)
			// Also pass PORT as env var so the container knows which port to listen on
			outputBindings["ENV_PORT"] = fmt.Sprintf("%d", userConfig.AppPort)
			outputBindings["DEPLOY_MODE"] = userConfig.DeployMode

			// Pass start command with port for containers that need --port flag
			if intel.DeepAnalysis != nil && intel.DeepAnalysis.StartCommand != "" && userConfig.AppPort > 0 {
				// Build start command with correct port (e.g., "node app.js --port 18789")
				startCmd := intel.DeepAnalysis.StartCommand
				// Replace common port placeholders or append port flag
				if !strings.Contains(startCmd, fmt.Sprintf("%d", userConfig.AppPort)) {
					// If the command doesn't already include the correct port, append it
					startCmd = fmt.Sprintf("%s --port %d", startCmd, userConfig.AppPort)
				}
				outputBindings["START_COMMAND"] = startCmd
			}

			// Generate native Node.js user-data if not using Docker
			if userConfig.DeployMode == "native" {
				outputBindings["NODEJS_USER_DATA"] = deploy.GenerateNodeJSUserData(rp.RepoURL, intel.DeepAnalysis, userConfig)
				fmt.Fprintf(os.Stderr, "[deploy] using native Node.js deployment (PM2)\n")
			}
		}
		execOpts := maker.ExecOptions{
			Profile:        targetProfile,
			Region:         region,
			Writer:         os.Stdout,
			Destroyer:      false,
			AIProvider:     provider,
			AIAPIKey:       apiKey,
			AIProfile:      aiProfile,
			Debug:          debug,
			OutputBindings: outputBindings,
		}
		if targetProvider == "cloudflare" {
			execOpts.Profile = ""
			execOpts.Region = ""
		}

		// Phase 1: Create infrastructure (ECR repo, VPC, security groups, IAM)
		if len(infraPlan.Commands) > 0 {
			fmt.Fprintf(os.Stderr, "[deploy] phase 1: creating infrastructure (%d commands)...\n", len(infraPlan.Commands))
			if err := maker.ExecutePlan(ctx, infraPlan, execOpts); err != nil {
				return fmt.Errorf("infrastructure creation failed: %w", err)
			}
		}

		// Phase 2: Build and push Docker image (if applicable, skip for native deployment)
		isNativeDeployment := userConfig != nil && userConfig.DeployMode == "native"
		if !isNativeDeployment && rp.HasDocker && outputBindings["ECR_URI"] != "" && targetProvider != "cloudflare" {
			if !maker.HasDockerInstalled() {
				return fmt.Errorf("Docker is required for deployment but not installed locally")
			}
			fmt.Fprintf(os.Stderr, "[deploy] phase 2: building and pushing Docker image...\n")
			imageURI, err := maker.BuildAndPushDockerImage(ctx, rp.ClonePath, outputBindings["ECR_URI"], targetProfile, region, os.Stdout)
			if err != nil {
				return fmt.Errorf("docker build/push failed: %w", err)
			}
			outputBindings["IMAGE_URI"] = imageURI
			fmt.Fprintf(os.Stderr, "[deploy] image pushed: %s\n", imageURI)
		} else if isNativeDeployment {
			fmt.Fprintf(os.Stderr, "[deploy] phase 2: skipping Docker build (native Node.js deployment)\n")
		}

		// Phase 3: Launch application (EC2, ALB, etc.)
		if len(appPlan.Commands) > 0 {
			fmt.Fprintf(os.Stderr, "[deploy] phase 3: launching application (%d commands)...\n", len(appPlan.Commands))
			if err := maker.ExecutePlan(ctx, appPlan, execOpts); err != nil {
				return fmt.Errorf("application deployment failed: %w", err)
			}
		}

		// Phase 4: Verify deployment is working
		albDNS := outputBindings["ALB_DNS"]
		if albDNS != "" && targetProvider != "cloudflare" {
			fmt.Fprintf(os.Stderr, "[deploy] phase 4: verifying deployment health...\n")

			// Give the app time to start
			fmt.Fprintf(os.Stderr, "[deploy] waiting 30s for application to start...\n")
			select {
			case <-ctx.Done():
				return fmt.Errorf("deployment timed out during startup wait: %w", ctx.Err())
			case <-time.After(30 * time.Second):
			}

			// Build health check config based on app type
			appPort := 3000
			if userConfig != nil && userConfig.AppPort > 0 {
				appPort = userConfig.AppPort
			}

			healthConfig := maker.HealthCheckConfig{
				Host:        albDNS,
				Port:        appPort,
				ExposesHTTP: true, // default to HTTP
			}

			// Use deep analysis to determine health check type
			if intel.DeepAnalysis != nil {
				healthConfig.ExposesHTTP = intel.DeepAnalysis.ExposesHTTP
				healthConfig.HTTPEndpoint = intel.DeepAnalysis.HealthEndpoint
			}

			if err := maker.VerifyNodeJSDeployment(ctx, healthConfig, 5*time.Minute, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "[deploy] health check failed: %v\n", err)
				fmt.Fprintf(os.Stderr, "[deploy] tip: check EC2 instance logs via SSM Session Manager\n")
				return fmt.Errorf("deployment verification failed: %w", err)
			}
		}

		// Print deployment summary with endpoint
		fmt.Fprintf(os.Stderr, "\n[deploy] deployment complete!\n")
		if albDNS != "" {
			fmt.Fprintf(os.Stderr, "\n========================================\n")
			fmt.Fprintf(os.Stderr, "Application URL: http://%s\n", albDNS)
			fmt.Fprintf(os.Stderr, "========================================\n\n")
		} else if instanceIP := outputBindings["PUBLIC_IP"]; instanceIP != "" {
			fmt.Fprintf(os.Stderr, "\n========================================\n")
			fmt.Fprintf(os.Stderr, "Instance IP: %s\n", instanceIP)
			fmt.Fprintf(os.Stderr, "========================================\n\n")
		}
		return nil
	},
}

// resolveAWSProfile picks the aws profile from flag, config, or default
func resolveAWSProfile(flag string) string {
	if flag != "" {
		return flag
	}
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	p := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
	if p != "" {
		return p
	}
	p = viper.GetString("aws.default_profile")
	if p != "" {
		return p
	}
	return "default"
}

// resolveAWSRegion picks the region from env, aws config, or default
func resolveAWSRegion(ctx context.Context, profile string) string {
	if r := strings.TrimSpace(os.Getenv("AWS_REGION")); r != "" {
		return r
	}
	if r := strings.TrimSpace(os.Getenv("AWS_DEFAULT_REGION")); r != "" {
		return r
	}
	cmd := exec.CommandContext(ctx, "aws", "configure", "get", "region", "--profile", profile)
	if out, err := cmd.CombinedOutput(); err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			return r
		}
	}
	if r := ai.FindInfraAnalysisRegion(); r != "" {
		return r
	}
	return "us-east-1"
}

func init() {
	rootCmd.AddCommand(deployCmd)

	deployCmd.Flags().String("profile", "", "AWS profile to use")
	deployCmd.Flags().String("ai-profile", "", "AI profile to use")
	deployCmd.Flags().String("openai-key", "", "OpenAI API key")
	deployCmd.Flags().String("anthropic-key", "", "Anthropic API key")
	deployCmd.Flags().String("gemini-key", "", "Gemini API key")
	deployCmd.Flags().Bool("apply", false, "Apply the plan immediately after generation")
	deployCmd.Flags().String("provider", "aws", "Cloud provider: aws or cloudflare")
	deployCmd.Flags().String("target", "fargate", "Deployment target: fargate (default), ec2, or eks")
	deployCmd.Flags().String("instance-type", "t3.small", "EC2 instance type (only used with --target ec2)")
	deployCmd.Flags().Bool("new-vpc", false, "Create a new VPC instead of using default")
}

// splitPlanAtDockerBuild separates infrastructure setup from app deployment.
// Infrastructure commands (ECR, VPC, security groups, IAM) run first,
// then Docker build happens locally, then app deployment (EC2, ALB).
func splitPlanAtDockerBuild(plan *maker.Plan) (*maker.Plan, *maker.Plan) {
	infraCommands := []maker.Command{}
	appCommands := []maker.Command{}

	// Find the EC2 run-instances command as the split point
	foundEC2 := false
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 && cmd.Args[0] == "ec2" && cmd.Args[1] == "run-instances" {
			foundEC2 = true
		}
		if foundEC2 {
			appCommands = append(appCommands, cmd)
		} else {
			infraCommands = append(infraCommands, cmd)
		}
	}

	// If no EC2 command found, don't split (could be Fargate or other deployment)
	if !foundEC2 {
		return plan, &maker.Plan{Commands: []maker.Command{}, Provider: plan.Provider}
	}

	return &maker.Plan{Commands: infraCommands, Provider: plan.Provider, Question: plan.Question},
		&maker.Plan{Commands: appCommands, Provider: plan.Provider, Question: plan.Question}
}
