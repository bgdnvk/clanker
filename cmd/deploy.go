package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/deploy"
	"github.com/bgdnvk/clanker/internal/maker"
	"github.com/bgdnvk/clanker/internal/openclaw"
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
		openaiModel, _ := cmd.Flags().GetString("openai-model")
		anthropicModel, _ := cmd.Flags().GetString("anthropic-model")
		geminiModel, _ := cmd.Flags().GetString("gemini-model")
		targetProvider, _ := cmd.Flags().GetString("provider")
		deployTarget, _ := cmd.Flags().GetString("target")
		instanceType, _ := cmd.Flags().GetString("instance-type")
		newVPC, _ := cmd.Flags().GetBool("new-vpc")
		gcpProject, _ := cmd.Flags().GetString("gcp-project")
		azureSubscription, _ := cmd.Flags().GetString("azure-subscription")

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
			apiKey = resolveAnthropicKey(anthropicKey)
		default:
			apiKey = viper.GetString("ai.api_key")
		}

		maybeOverrideProviderModel(provider, openaiModel, anthropicModel, geminiModel)

		aiClient := ai.NewClient(provider, apiKey, debug, aiProfile)

		// log helper
		logf := func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}

		// 3. Resolve AWS profile/region early so intelligence pipeline can scan infra
		var targetProfile, region string
		if strings.EqualFold(strings.TrimSpace(targetProvider), "aws") {
			targetProfile = resolveAWSProfile(profile)
			region = resolveAWSRegion(ctx, targetProfile)
		}

		// Build deploy options from flags
		deployOpts := &deploy.DeployOptions{
			Target:       deployTarget,
			InstanceType: instanceType,
			NewVPC:       newVPC,
		}
		// Run-specific id so resource names get a fresh short-hash suffix each deploy.
		deployOpts.DeployID = time.Now().UTC().Format(time.RFC3339Nano)

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
		// Only prompt in apply mode because plan generation can run in non-interactive contexts
		// (e.g. backend API calls) where stdin is not available.
		var userConfig *deploy.UserConfig
		if applyMode && intel.DeepAnalysis != nil && rp.Language == "node" {
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

		// Fallback prompting: if deep analysis didn't produce requiredEnvVars, infer from prompt text
		// and docker-compose ${VAR} references.
		if applyMode && rp.Language == "node" && (userConfig == nil || len(userConfig.EnvVars) == 0) {
			inferred := inferEnvVarNamesFromText(intel.EnrichedPrompt)
			if intel.Docker != nil {
				inferred = append(inferred, intel.Docker.ReferencedEnvVars...)
			}
			values, pErr := deploy.PromptForEnvVarValues(inferred)
			if pErr != nil {
				return fmt.Errorf("configuration failed: %w", pErr)
			}
			if len(values) > 0 {
				if userConfig == nil {
					userConfig = deploy.DefaultUserConfig(intel.DeepAnalysis, rp)
				}
				for k, v := range values {
					userConfig.EnvVars[k] = v
				}
			}
		}

		// Default config if none collected
		if userConfig == nil {
			userConfig = deploy.DefaultUserConfig(intel.DeepAnalysis, rp)
		}

		baseQuestion := intel.EnrichedPrompt
		if debug {
			fmt.Fprintf(os.Stderr, "[deploy] enriched prompt:\n%s\n", baseQuestion)
		}

		planProvider := strings.ToLower(strings.TrimSpace(targetProvider))
		if planProvider == "" {
			planProvider = strings.ToLower(strings.TrimSpace(intel.Architecture.Provider))
		}
		if planProvider == "" {
			planProvider = "aws"
		}

		requiredLaunchOps := []string{}
		switch strings.ToLower(strings.TrimSpace(intel.Architecture.Method)) {
		case "ec2":
			requiredLaunchOps = []string{"ec2 run-instances"}
		case "ecs-fargate", "ecs":
			requiredLaunchOps = []string{"ecs create-service", "ecs run-task"}
		case "app-runner":
			requiredLaunchOps = []string{"apprunner create-service"}
		case "lambda":
			requiredLaunchOps = []string{"lambda create-function"}
		case "s3-cloudfront":
			requiredLaunchOps = []string{"s3api create-bucket", "cloudfront create-distribution"}
		case "lightsail":
			requiredLaunchOps = []string{"lightsail create-container-service"}
		case "cf-pages":
			requiredLaunchOps = []string{"wrangler pages"}
		case "cf-workers":
			requiredLaunchOps = []string{"wrangler deploy"}
		case "cf-containers":
			requiredLaunchOps = []string{"wrangler containers"}
		case "cloud-run":
			requiredLaunchOps = []string{"run deploy"}
		case "gcp-compute-engine", "gcp-compute", "compute-engine":
			requiredLaunchOps = []string{"compute instances"}
		case "gke":
			requiredLaunchOps = []string{"container clusters"}
		case "azure-vm":
			requiredLaunchOps = []string{"vm create"}
		case "azure-container-apps", "container-apps":
			requiredLaunchOps = []string{"containerapp create"}
		case "aks":
			requiredLaunchOps = []string{"aks create"}
		}

		// 4. Generate the maker plan via LLM
		fmt.Fprintf(os.Stderr, "[deploy] phase 3: generating execution plan with %s ...\n", provider)

		// Generate a plan incrementally in small pages to avoid LLM truncation.
		const maxPlanPages = 20
		const maxCommandsPerPage = 8
		var plan *maker.Plan
		var mustFixIssues []string
		var lastDetValidation *deploy.PlanValidation
		stuckPages := 0

		plan = &maker.Plan{
			Version:   maker.CurrentPlanVersion,
			CreatedAt: time.Now().UTC(),
			Provider:  planProvider,
			Question:  fmt.Sprintf("Deploy %s to %s (%s)", rp.RepoURL, planProvider, intel.Architecture.Method),
			Summary:   "",
			Commands:  nil,
		}
		if strings.TrimSpace(intel.Architecture.Provider) != "" {
			plan.Provider = strings.TrimSpace(intel.Architecture.Provider)
		}

		for pageRound := 1; pageRound <= maxPlanPages; pageRound++ {
			questionForPrompt := baseQuestion
			if len(questionForPrompt) > 18000 {
				questionForPrompt = questionForPrompt[:18000] + "…"
			}
			prompt := deploy.BuildPlanPagePrompt(planProvider, questionForPrompt, plan, requiredLaunchOps, mustFixIssues, maxCommandsPerPage)
			resp, err := aiClient.AskPrompt(ctx, prompt)
			if err != nil {
				return fmt.Errorf("plan generation failed: %w", err)
			}

			cleaned := aiClient.CleanJSONResponse(resp)
			page, err := deploy.ParsePlanPage(cleaned)
			if err != nil {
				logf("[deploy] warning: plan page parse failed (%v), retrying (page %d/%d)...", err, pageRound, maxPlanPages)
				continue
			}

			if len(page.Commands) > 0 {
				// Normalize args and validate command shapes via maker.ParsePlan.
				tmp := &maker.Plan{Provider: planProvider, Question: "", Summary: "", Commands: page.Commands}
				tmpJSON, _ := json.Marshal(tmp)
				normalized, nErr := maker.ParsePlan(string(tmpJSON))
				if nErr != nil {
					logf("[deploy] warning: plan page had invalid commands (%v), retrying (page %d/%d)...", nErr, pageRound, maxPlanPages)
					continue
				}
				page.Commands = normalized.Commands
			}

			added := deploy.AppendPlanPage(plan, page)
			logf("[deploy] plan page %d/%d: added %d command(s) (total=%d)", pageRound, maxPlanPages, added, len(plan.Commands))

			// Ensure plan metadata is consistent.
			if strings.TrimSpace(intel.Architecture.Provider) != "" {
				plan.Provider = strings.TrimSpace(intel.Architecture.Provider)
			}
			plan.Question = fmt.Sprintf("Deploy %s to %s (%s)", rp.RepoURL, strings.ToLower(strings.TrimSpace(plan.Provider)), intel.Architecture.Method)
			if plan.CreatedAt.IsZero() {
				plan.CreatedAt = time.Now().UTC()
			}
			if plan.Version == 0 {
				plan.Version = maker.CurrentPlanVersion
			}

			// Deterministic checkpoint validation (AWS only).
			if strings.EqualFold(strings.TrimSpace(planProvider), "aws") {
				planJSON, _ := json.MarshalIndent(plan, "", "  ")
				lastDetValidation = deploy.DeterministicValidatePlan(string(planJSON), rp, intel.DeepAnalysis, intel.Docker)
				if lastDetValidation != nil && !lastDetValidation.IsValid {
					mustFixIssues = lastDetValidation.Issues
				} else {
					mustFixIssues = nil
				}
			}
			if added == 0 {
				stuckPages++
			} else {
				stuckPages = 0
			}
			if stuckPages >= 3 && len(mustFixIssues) > 0 {
				logf("[deploy] error: planning is stuck (no new commands added for %d pages) while %d hard issue(s) remain", stuckPages, len(mustFixIssues))
				for i, issue := range mustFixIssues {
					if i >= 12 {
						break
					}
					logf("[deploy]   hard issue: %s", strings.TrimSpace(issue))
				}
				if applyMode {
					return fmt.Errorf("failed to generate a deterministically valid plan (stuck with issues=%d)", len(mustFixIssues))
				}
				logf("[deploy] warning: planning is stuck but continuing in plan-only mode")
				break
			}

			if page.Done {
				// Ignore done=true if deterministic hard issues remain; force another page.
				if len(mustFixIssues) == 0 {
					break
				}
				logf("[deploy] warning: model returned done=true but deterministic issues remain; continuing")
			}
		}

		if len(plan.Commands) == 0 {
			return fmt.Errorf("failed to generate a plan (no commands produced)")
		}
		plan = deploy.SanitizePlanConservative(plan, rp, intel.DeepAnalysis, intel.Docker, logf)
		if lastDetValidation != nil {
			intel.Validation = lastDetValidation
		}
		if lastDetValidation != nil && !lastDetValidation.IsValid {
			logf("[deploy] deterministic validation failed with %d issue(s)", len(lastDetValidation.Issues))
			for i, issue := range lastDetValidation.Issues {
				if i >= 12 {
					logf("[deploy]   issue: (and %d more)", len(lastDetValidation.Issues)-i)
					break
				}
				logf("[deploy]   issue: %s", strings.TrimSpace(issue))
			}
			for i, fix := range lastDetValidation.Fixes {
				if i >= 12 {
					break
				}
				if strings.TrimSpace(fix) == "" {
					continue
				}
				logf("[deploy]   fix: %s", strings.TrimSpace(fix))
			}

			// Try to repair deterministic issues (e.g., missing onboarding steps) before failing.
			repairAgent := deploy.NewPlanRepairAgent(aiClient.AskPrompt, aiClient.CleanJSONResponse, logf)
			requiredEnvNames := make([]string, 0, 16)
			if len(rp.EnvVars) > 0 {
				requiredEnvNames = append(requiredEnvNames, rp.EnvVars...)
			}
			if intel.DeepAnalysis != nil {
				for _, spec := range intel.DeepAnalysis.RequiredEnvVars {
					if strings.TrimSpace(spec.Name) != "" {
						requiredEnvNames = append(requiredEnvNames, strings.TrimSpace(spec.Name))
					}
				}
			}
			{
				seen := make(map[string]struct{}, len(requiredEnvNames))
				out := make([]string, 0, len(requiredEnvNames))
				for _, name := range requiredEnvNames {
					name = strings.TrimSpace(name)
					if name == "" {
						continue
					}
					if _, ok := seen[name]; ok {
						continue
					}
					seen[name] = struct{}{}
					out = append(out, name)
				}
				requiredEnvNames = out
			}

			repairCtx := deploy.PlanRepairContext{
				Provider:            intel.Architecture.Provider,
				Method:              intel.Architecture.Method,
				RepoURL:             rp.RepoURL,
				GCPProject:          strings.TrimSpace(gcpProject),
				AzureSubscriptionID: strings.TrimSpace(azureSubscription),
				CloudflareAccountID: "",
				Ports:               rp.Ports,
				ComposeHardEnvVars: func() []string {
					if intel.Preflight != nil {
						return intel.Preflight.ComposeHardEnvVars
					}
					return nil
				}(),
				RequiredEnvVarNames: requiredEnvNames,
				RequiredLaunchOps:   requiredLaunchOps,
				Region:              region,
				VPCID: func() string {
					if intel.InfraSnap != nil && intel.InfraSnap.VPC != nil {
						return intel.InfraSnap.VPC.VPCID
					}
					return ""
				}(),
				Subnets: func() []string {
					if intel.InfraSnap != nil && intel.InfraSnap.VPC != nil {
						return intel.InfraSnap.VPC.Subnets
					}
					return nil
				}(),
				AMIID: func() string {
					if intel.InfraSnap != nil {
						return intel.InfraSnap.LatestAMI
					}
					return ""
				}(),
				Account: func() string {
					if intel.InfraSnap != nil {
						return intel.InfraSnap.AccountID
					}
					return ""
				}(),
			}

			currentValidation := lastDetValidation
			currentPlanJSONBytes, _ := json.MarshalIndent(plan, "", "  ")
			currentPlanJSON := string(currentPlanJSONBytes)
			const maxDetRepairRounds = 3
			for r := 1; r <= maxDetRepairRounds; r++ {
				logf("[deploy] attempting deterministic repair (round %d/%d)...", r, maxDetRepairRounds)
				repairedRaw, rErr := repairAgent.Repair(ctx, currentPlanJSON, currentValidation, repairCtx)
				if rErr != nil {
					break
				}
				repaired, pErr := maker.ParsePlan(repairedRaw)
				if pErr != nil {
					break
				}
				repaired.Provider = intel.Architecture.Provider
				repaired.Question = fmt.Sprintf("Deploy %s to %s (%s)", rp.RepoURL, strings.ToLower(strings.TrimSpace(repaired.Provider)), intel.Architecture.Method)
				if repaired.CreatedAt.IsZero() {
					repaired.CreatedAt = time.Now().UTC()
				}
				if repaired.Version == 0 {
					repaired.Version = maker.CurrentPlanVersion
				}
				repaired = deploy.SanitizePlan(repaired)

				repairedJSONBytes, _ := json.MarshalIndent(repaired, "", "  ")
				detV := deploy.DeterministicValidatePlan(string(repairedJSONBytes), rp, intel.DeepAnalysis, intel.Docker)
				intel.Validation = detV
				if detV != nil && detV.IsValid {
					plan = repaired
					lastDetValidation = detV
					logf("[deploy] deterministic repair succeeded")
					break
				}
				currentValidation = detV
				currentPlanJSON = string(repairedJSONBytes)
			}

			if lastDetValidation != nil && !lastDetValidation.IsValid {
				if applyMode {
					return fmt.Errorf("failed to generate a deterministically valid plan (issues=%d)", len(lastDetValidation.Issues))
				}
				logf("[deploy] warning: deterministic issues remain after repair (issues=%d); continuing in plan-only mode", len(lastDetValidation.Issues))
			}
		}

		// Final validation (LLM) + optional repair pass.
		plan = deploy.SanitizePlanConservative(plan, rp, intel.DeepAnalysis, intel.Docker, logf)
		planJSON, _ := json.MarshalIndent(plan, "", "  ")
		validation, _, err := deploy.ValidatePlan(ctx,
			string(planJSON), rp, intel.DeepAnalysis,
			intel.Docker,
			false,
			aiClient.AskPrompt, aiClient.CleanJSONResponse, logf,
		)
		if err != nil {
			return fmt.Errorf("plan validation failed: %w", err)
		}
		intel.Validation = validation
		if validation != nil && !validation.IsValid {
			logf("[deploy] validation found %d issue(s)", len(validation.Issues))
			for i, issue := range validation.Issues {
				if i >= 12 {
					logf("[deploy]   issue: (and %d more)", len(validation.Issues)-i)
					break
				}
				logf("[deploy]   issue: %s", strings.TrimSpace(issue))
			}
			for i, fix := range validation.Fixes {
				if i >= 12 {
					break
				}
				if strings.TrimSpace(fix) == "" {
					continue
				}
				logf("[deploy]   fix: %s", strings.TrimSpace(fix))
			}
		}

		repairAgent := deploy.NewPlanRepairAgent(aiClient.AskPrompt, aiClient.CleanJSONResponse, logf)
		if !validation.IsValid {
			// Attempt repair passes to address validator feedback without re-generating from scratch.
			requiredEnvNames := make([]string, 0, 16)
			if len(rp.EnvVars) > 0 {
				requiredEnvNames = append(requiredEnvNames, rp.EnvVars...)
			}
			if intel.DeepAnalysis != nil {
				for _, spec := range intel.DeepAnalysis.RequiredEnvVars {
					if strings.TrimSpace(spec.Name) != "" {
						requiredEnvNames = append(requiredEnvNames, strings.TrimSpace(spec.Name))
					}
				}
			}
			{
				seen := make(map[string]struct{}, len(requiredEnvNames))
				out := make([]string, 0, len(requiredEnvNames))
				for _, name := range requiredEnvNames {
					name = strings.TrimSpace(name)
					if name == "" {
						continue
					}
					if _, ok := seen[name]; ok {
						continue
					}
					seen[name] = struct{}{}
					out = append(out, name)
				}
				requiredEnvNames = out
			}

			repairCtx := deploy.PlanRepairContext{
				Provider:            intel.Architecture.Provider,
				Method:              intel.Architecture.Method,
				RepoURL:             rp.RepoURL,
				GCPProject:          strings.TrimSpace(gcpProject),
				AzureSubscriptionID: strings.TrimSpace(azureSubscription),
				CloudflareAccountID: "",
				Ports:               rp.Ports,
				ComposeHardEnvVars: func() []string {
					if intel.Preflight != nil {
						return intel.Preflight.ComposeHardEnvVars
					}
					return nil
				}(),
				RequiredEnvVarNames: requiredEnvNames,
				RequiredLaunchOps:   requiredLaunchOps,
				Region:              region,
				VPCID: func() string {
					if intel.InfraSnap != nil && intel.InfraSnap.VPC != nil {
						return intel.InfraSnap.VPC.VPCID
					}
					return ""
				}(),
				Subnets: func() []string {
					if intel.InfraSnap != nil && intel.InfraSnap.VPC != nil {
						return intel.InfraSnap.VPC.Subnets
					}
					return nil
				}(),
				AMIID: func() string {
					if intel.InfraSnap != nil {
						return intel.InfraSnap.LatestAMI
					}
					return ""
				}(),
				Account: func() string {
					if intel.InfraSnap != nil {
						return intel.InfraSnap.AccountID
					}
					return ""
				}(),
			}

			const maxRepairRounds = 3
			currentValidation := validation
			currentPlanJSON := string(planJSON)
			for r := 1; r <= maxRepairRounds; r++ {
				logf("[deploy] attempting plan repair (round %d/%d)...", r, maxRepairRounds)
				repairedRaw, rErr := repairAgent.Repair(ctx, currentPlanJSON, currentValidation, repairCtx)
				if rErr != nil {
					if applyMode {
						return fmt.Errorf("plan is invalid and repair failed: %v", rErr)
					}
					logf("[deploy] warning: repair failed in plan-only mode (%v); continuing with deterministically valid plan", rErr)
					break
				}
				repaired, pErr := maker.ParsePlan(repairedRaw)
				if pErr != nil {
					if applyMode {
						return fmt.Errorf("plan repair produced an unparseable plan: %v", pErr)
					}
					logf("[deploy] warning: repair output unparseable in plan-only mode (%v); continuing with deterministically valid plan", pErr)
					break
				}
				repaired.Provider = intel.Architecture.Provider
				repaired.Question = fmt.Sprintf("Deploy %s to %s (%s)", rp.RepoURL, strings.ToLower(strings.TrimSpace(repaired.Provider)), intel.Architecture.Method)
				if repaired.CreatedAt.IsZero() {
					repaired.CreatedAt = time.Now().UTC()
				}
				if repaired.Version == 0 {
					repaired.Version = maker.CurrentPlanVersion
				}
				repaired = deploy.SanitizePlanConservative(repaired, rp, intel.DeepAnalysis, intel.Docker, logf)
				// Keep the latest repaired candidate even if validator still has concerns.
				plan = repaired

				repairedJSON, _ := json.MarshalIndent(repaired, "", "  ")
				repairedValidation, _, vErr := deploy.ValidatePlan(ctx,
					string(repairedJSON), rp, intel.DeepAnalysis,
					intel.Docker,
					false,
					aiClient.AskPrompt, aiClient.CleanJSONResponse, logf,
				)
				if vErr != nil {
					if applyMode {
						return fmt.Errorf("plan validation failed after repair: %v", vErr)
					}
					logf("[deploy] warning: validation failed after repair in plan-only mode (%v); continuing with deterministically valid plan", vErr)
					break
				}
				intel.Validation = repairedValidation

				if repairedValidation != nil && repairedValidation.IsValid {
					plan = repaired
					logf("[deploy] plan repaired + validated successfully")
					break
				}

				// Not valid yet; iterate.
				currentValidation = repairedValidation
				currentPlanJSON = string(repairedJSON)
				if repairedValidation != nil {
					logf("[deploy] repair round %d still invalid (issues=%d)", r, len(repairedValidation.Issues))
					for i, issue := range repairedValidation.Issues {
						if i >= 12 {
							logf("[deploy]   issue: (and %d more)", len(repairedValidation.Issues)-i)
							break
						}
						logf("[deploy]   issue: %s", strings.TrimSpace(issue))
					}
				}

				if r == maxRepairRounds {
					issueCount := 0
					if repairedValidation != nil {
						issueCount = len(repairedValidation.Issues)
					}
					if applyMode {
						return fmt.Errorf("plan is invalid after repair (issues=%d)", issueCount)
					}
					logf("[deploy] warning: plan is still LLM-invalid after repair (issues=%d), but deterministic checks passed; returning plan in plan-only mode", issueCount)
				}
			}
		}

		// Final non-blocking review pass: allow the reviewer agent to add missing
		// requirement commands to the latest plan (e.g. OpenClaw AWS CloudFront HTTPS).
		{
			reviewer := deploy.NewPlanReviewAgent(aiClient.AskPrompt, aiClient.CleanJSONResponse, logf)
			currentPlanJSON, _ := json.MarshalIndent(plan, "", "  ")
			isOpenClawRepo := deploy.IsOpenClawRepo(rp, intel.DeepAnalysis)
			openClawCloudFrontMissing := false
			if isOpenClawRepo {
				openClawCloudFrontMissing = !deploy.HasOpenClawCloudFront(string(currentPlanJSON))
			}
			reviewCtx := deploy.PlanReviewContext{
				Provider:                  intel.Architecture.Provider,
				Method:                    intel.Architecture.Method,
				RepoURL:                   rp.RepoURL,
				IsOpenClaw:                isOpenClawRepo,
				OpenClawCloudFrontMissing: openClawCloudFrontMissing,
				IsWordPress:               deploy.IsWordPressRepo(rp, intel.DeepAnalysis),
			}

			reviewedRaw, reviewErr := reviewer.Review(ctx, string(currentPlanJSON), reviewCtx)
			if reviewErr != nil {
				logf("[deploy] warning: final plan review skipped (%v)", reviewErr)
			} else {
				reviewedPlan, parseErr := maker.ParsePlan(reviewedRaw)
				if parseErr != nil {
					logf("[deploy] warning: final plan review produced unparseable plan (%v); keeping current plan", parseErr)
				} else if reviewedPlan != nil && len(reviewedPlan.Commands) > 0 {
					reviewedPlan.Provider = intel.Architecture.Provider
					reviewedPlan.Question = fmt.Sprintf("Deploy %s to %s (%s)", rp.RepoURL, strings.ToLower(strings.TrimSpace(reviewedPlan.Provider)), intel.Architecture.Method)
					if reviewedPlan.CreatedAt.IsZero() {
						reviewedPlan.CreatedAt = time.Now().UTC()
					}
					if reviewedPlan.Version == 0 {
						reviewedPlan.Version = maker.CurrentPlanVersion
					}
					plan = deploy.SanitizePlanConservative(reviewedPlan, rp, intel.DeepAnalysis, intel.Docker, logf)
					logf("[deploy] final plan review applied (commands=%d)", len(plan.Commands))
				}
			}
		}

		// 6. Enrich w/ existing infra context (AWS only)
		if strings.EqualFold(strings.TrimSpace(targetProvider), "aws") {
			_ = maker.EnrichPlan(ctx, plan, maker.ExecOptions{
				Profile: targetProfile, Region: region, Writer: io.Discard,
			})
		}

		// 7. Resolve placeholders before output
		// Always apply static bindings (AMI_ID, ACCOUNT_ID, REGION) - even with --new-vpc
		if strings.EqualFold(strings.TrimSpace(targetProvider), "aws") && intel.InfraSnap != nil {
			plan = deploy.ApplyStaticInfraBindings(plan, intel.InfraSnap)
		}

		// Full placeholder resolution (AWS only, skip --new-vpc since those use 'produces' chaining)
		if strings.EqualFold(strings.TrimSpace(targetProvider), "aws") && !newVPC {
			const maxPlaceholderRounds = 5
			for round := 1; round <= maxPlaceholderRounds; round++ {
				unresolvedNow := deploy.GetUnresolvedPlaceholders(plan)
				if len(unresolvedNow) == 0 {
					break
				}
				if deploy.AllPlaceholdersAreProduced(plan, unresolvedNow) {
					logf("[deploy] placeholder resolution complete: %d placeholders are runtime-produced via command chaining: %v", len(unresolvedNow), unresolvedNow)
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
		planJSON, err = json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}

		if !applyMode {
			fmt.Println(string(planJSON))
			return nil
		}

		// Apply mode: normalize any inline EC2 user-data scripts to base64 so heredocs like <<EOF
		// can't be misinterpreted as placeholders by downstream scanners.
		plan = deploy.Base64EncodeEC2UserDataScripts(plan)

		planProvider = strings.ToLower(strings.TrimSpace(plan.Provider))
		if planProvider == "" {
			planProvider = strings.ToLower(strings.TrimSpace(targetProvider))
		}
		if planProvider == "" {
			planProvider = "aws"
		}

		switch planProvider {
		case "gcp":
			if strings.TrimSpace(gcpProject) == "" {
				gcpProject = strings.TrimSpace(os.Getenv("GCP_PROJECT_ID"))
			}
			if strings.TrimSpace(gcpProject) == "" {
				gcpProject = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
			}
			if strings.TrimSpace(gcpProject) == "" {
				return fmt.Errorf("gcp project is required for GCP deploy (use --gcp-project or set GCP_PROJECT_ID)")
			}
			fmt.Fprintf(os.Stderr, "[deploy] applying GCP plan (%d commands)...\n", len(plan.Commands))
			return maker.ExecuteGCPPlan(ctx, plan, maker.ExecOptions{
				GCPProject: strings.TrimSpace(gcpProject),
				Writer:     os.Stdout,
				Destroyer:  false,
				Debug:      debug,
			})
		case "azure":
			azureSub := strings.TrimSpace(azureSubscription)
			if azureSub == "" {
				azureSub = azure.ResolveSubscriptionID()
			}
			if azureSub == "" {
				return fmt.Errorf("azure subscription is required (use --azure-subscription or set AZURE_SUBSCRIPTION_ID)")
			}
			fmt.Fprintf(os.Stderr, "[deploy] applying Azure plan (%d commands)...\n", len(plan.Commands))
			return maker.ExecuteAzurePlan(ctx, plan, maker.ExecOptions{
				AzureSubscriptionID: azureSub,
				Writer:              os.Stdout,
				Destroyer:           false,
				Debug:               debug,
			})
		case "cloudflare":
			cfToken := cloudflare.ResolveAPIToken()
			cfAccountID := cloudflare.ResolveAccountID()
			if cfToken == "" {
				return fmt.Errorf("cloudflare api token is required (set CLOUDFLARE_API_TOKEN or cloudflare.api_token)")
			}
			fmt.Fprintf(os.Stderr, "[deploy] applying Cloudflare plan (%d commands)...\n", len(plan.Commands))
			return maker.ExecuteCloudflarePlan(ctx, plan, maker.ExecOptions{
				CloudflareAPIToken:  cfToken,
				CloudflareAccountID: cfAccountID,
				Writer:              os.Stdout,
				Destroyer:           false,
				Debug:               debug,
			})
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
		if strings.EqualFold(strings.TrimSpace(targetProvider), "cloudflare") {
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
		if !isNativeDeployment && rp.HasDocker && outputBindings["ECR_URI"] != "" && strings.EqualFold(strings.TrimSpace(targetProvider), "aws") {
			if !maker.HasDockerInstalled() {
				return fmt.Errorf("Docker is required for deployment but was not found in PATH")
			}
			if !maker.DockerDaemonAvailableForCLI(ctx) {
				return fmt.Errorf("Docker is installed but the daemon is not running (start Docker Desktop / ensure docker engine is running, then retry)")
			}
			fmt.Fprintf(os.Stderr, "[deploy] phase 2: building and pushing Docker image...\n")
			imageURI, err := maker.BuildAndPushDockerImage(ctx, rp.ClonePath, outputBindings["ECR_URI"], targetProfile, region, "latest", os.Stdout)
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
		if albDNS != "" && strings.EqualFold(strings.TrimSpace(targetProvider), "aws") {
			fmt.Fprintf(os.Stderr, "[deploy] phase 4: verifying deployment health...\n")

			// Give the app time to start
			fmt.Fprintf(os.Stderr, "[deploy] waiting 30s for application to start...\n")
			select {
			case <-ctx.Done():
				return fmt.Errorf("deployment timed out during startup wait: %w", ctx.Err())
			case <-time.After(30 * time.Second):
			}

			// Prefer HTTPS URL (CloudFront) when present; otherwise fall back to ALB HTTP.
			httpsURL := strings.TrimSpace(outputBindings["HTTPS_URL"])
			baseURL := "http://" + albDNS
			if httpsURL != "" {
				baseURL = httpsURL
			}
			path := "/health"
			if openclaw.Detect(strings.TrimSpace(baseQuestion), rp.RepoURL) {
				path = "/"
			}
			if intel.DeepAnalysis != nil && strings.TrimSpace(intel.DeepAnalysis.HealthEndpoint) != "" {
				path = strings.TrimSpace(intel.DeepAnalysis.HealthEndpoint)
				if !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
			}
			endpoint := strings.TrimRight(baseURL, "/") + path
			if err := maker.VerifyDeployment(ctx, endpoint, 6*time.Minute, os.Stdout); err != nil {
				// Common fallback: app has no /health.
				fallback := strings.TrimRight(baseURL, "/") + "/"
				if err2 := maker.VerifyDeployment(ctx, fallback, 3*time.Minute, os.Stdout); err2 != nil {
					fmt.Fprintf(os.Stderr, "[deploy] health check failed: %v\n", err)
					fmt.Fprintf(os.Stderr, "[deploy] tip: check EC2 instance logs via SSM Session Manager\n")
					return fmt.Errorf("deployment verification failed: %w", err2)
				}
			}
		}

		// Print deployment summary with endpoint
		fmt.Fprintf(os.Stderr, "\n[deploy] deployment complete!\n")
		httpsURL := strings.TrimSpace(outputBindings["HTTPS_URL"])
		cfDomain := strings.TrimSpace(outputBindings["CLOUDFRONT_DOMAIN"])
		if httpsURL == "" && cfDomain != "" {
			httpsURL = "https://" + cfDomain
		}
		isOpenClaw := openclaw.Detect(strings.TrimSpace(baseQuestion), rp.RepoURL)
		if isOpenClaw && strings.TrimSpace(httpsURL) == "" {
			return fmt.Errorf("openclaw deploy requires HTTPS pairing URL but CloudFront URL is missing")
		}
		if httpsURL != "" {
			fmt.Fprintf(os.Stderr, "\n========================================\n")
			fmt.Fprintf(os.Stderr, "Application URL: %s\n", httpsURL)
			fmt.Fprintf(os.Stderr, "========================================\n\n")
		} else if albDNS != "" {
			fmt.Fprintf(os.Stderr, "\n========================================\n")
			fmt.Fprintf(os.Stderr, "Application URL: http://%s\n", albDNS)
			fmt.Fprintf(os.Stderr, "========================================\n\n")
		} else if instanceIP := outputBindings["PUBLIC_IP"]; instanceIP != "" {
			fmt.Fprintf(os.Stderr, "\n========================================\n")
			fmt.Fprintf(os.Stderr, "Instance IP: %s\n", instanceIP)
			fmt.Fprintf(os.Stderr, "========================================\n\n")
		}

		if isOpenClaw {
			fmt.Fprintf(os.Stderr, "[openclaw-summary] deployment + pairing endpoints\n")
			if httpsURL != "" {
				fmt.Fprintf(os.Stderr, "[openclaw-summary] Pairing URL (HTTPS): %s\n", httpsURL)
			}
			if cfDomain != "" {
				fmt.Fprintf(os.Stderr, "[openclaw-summary] CloudFront Domain: https://%s\n", cfDomain)
			}
			if albDNS != "" {
				fmt.Fprintf(os.Stderr, "[openclaw-summary] ALB Origin (HTTP): http://%s\n", albDNS)
			}
			if instanceID := strings.TrimSpace(outputBindings["INSTANCE_ID"]); instanceID != "" {
				fmt.Fprintf(os.Stderr, "[openclaw-summary] Local fallback (SSM): aws ssm start-session --target %s --document-name AWS-StartPortForwardingSession --parameters 'portNumber=[\"18789\"],localPortNumber=[\"18789\"]' --profile %s --region %s\n", instanceID, targetProfile, region)
			}
			fmt.Fprintf(os.Stderr, "[openclaw-summary] Use OPENCLAW_GATEWAY_TOKEN when prompted in the Control UI.\n\n")
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

func inferEnvVarNamesFromText(text string) []string {
	text = strings.ReplaceAll(text, "\r", "")
	lower := strings.ToLower(text)
	if strings.TrimSpace(lower) == "" {
		return nil
	}

	// Prefer lines that explicitly mention required env vars.
	lines := strings.Split(text, "\n")
	candidates := make([]string, 0, 16)
	for _, line := range lines {
		l := strings.ToLower(line)
		if strings.Contains(l, "required env") || strings.Contains(l, "required env vars") || strings.Contains(l, "required env var") {
			candidates = append(candidates, line)
		}
	}
	if len(candidates) == 0 {
		// Fallback: scan the whole text for common *_TOKEN / *_API_KEY / *_PASSWORD keys.
		candidates = append(candidates, text)
	}

	re := regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)
	seen := make(map[string]struct{})
	out := make([]string, 0, 24)
	for _, chunk := range candidates {
		for _, m := range re.FindAllString(chunk, -1) {
			m = strings.TrimSpace(m)
			if m == "" || !strings.Contains(m, "_") {
				continue
			}
			// Only keep plausible secret/config keys.
			if !(strings.Contains(m, "TOKEN") || strings.Contains(m, "KEY") || strings.Contains(m, "PASSWORD") || strings.Contains(m, "SECRET")) {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func init() {
	rootCmd.AddCommand(deployCmd)

	deployCmd.Flags().String("profile", "", "AWS profile to use")
	deployCmd.Flags().String("ai-profile", "", "AI profile to use")
	deployCmd.Flags().String("openai-key", "", "OpenAI API key")
	deployCmd.Flags().String("anthropic-key", "", "Anthropic API key")
	deployCmd.Flags().String("gemini-key", "", "Gemini API key")
	deployCmd.Flags().String("openai-model", "", "OpenAI model to use (overrides config)")
	deployCmd.Flags().String("anthropic-model", "", "Anthropic model to use (overrides config)")
	deployCmd.Flags().String("gemini-model", "", "Gemini model to use (overrides config)")
	deployCmd.Flags().Bool("apply", false, "Apply the plan immediately after generation")
	deployCmd.Flags().String("provider", "aws", "Cloud provider: aws, gcp, azure, or cloudflare")
	deployCmd.Flags().String("target", "fargate", "Deployment target: fargate (default), ec2, or eks")
	deployCmd.Flags().String("instance-type", "t3.small", "EC2 instance type (only used with --target ec2)")
	deployCmd.Flags().Bool("new-vpc", false, "Create a new VPC instead of using default")
	deployCmd.Flags().String("gcp-project", "", "GCP project ID (required for --provider gcp apply)")
	deployCmd.Flags().String("azure-subscription", "", "Azure subscription ID (required for --provider azure apply)")
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
