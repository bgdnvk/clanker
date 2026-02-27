package cmd

import (
	"context"
	"encoding/json"
	"errors"
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
		enforceImageDeploy, _ := cmd.Flags().GetBool("enforce-image-deploy")

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
		isOpenClawDeploy := openclaw.Detect(strings.TrimSpace(baseQuestion), rp.RepoURL)
		if isOpenClawDeploy && !enforceImageDeploy {
			enforceImageDeploy = true
			fmt.Fprintf(os.Stderr, "[deploy] openclaw detected: enabling image deploy enforcement by default\n")
		}

		planProvider := strings.ToLower(strings.TrimSpace(targetProvider))
		if planProvider == "" {
			planProvider = strings.ToLower(strings.TrimSpace(intel.Architecture.Provider))
		}
		if planProvider == "" {
			planProvider = "aws"
		}
		deployObjectiveContext := withOneClickDeployContext(baseQuestion, planProvider, intel.Architecture.Method, enforceImageDeploy)
		planningContext := compactPlanningContext(deployObjectiveContext, planProvider)
		projectSummaryForLLM := strings.TrimSpace(rp.Summary)
		if intel.DeepAnalysis != nil && strings.TrimSpace(intel.DeepAnalysis.AppDescription) != "" {
			projectSummaryForLLM = strings.TrimSpace(intel.DeepAnalysis.AppDescription)
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
		const maxConsecutivePageFailures = 5
		const earlyRepairAfterFailures = 3
		const openClawSoftPlanCommands = 56
		const openClawHardPlanCommands = 80
		var plan *maker.Plan
		var mustFixIssues []string
		var lastDetValidation *deploy.PlanValidation
		stuckPages := 0
		consecutivePageFailures := 0
		pageFormatHint := ""

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
			prompt := deploy.BuildPlanPagePrompt(planProvider, planningContext, plan, requiredLaunchOps, mustFixIssues, maxCommandsPerPage, pageFormatHint)
			resp, err := aiClient.AskPrompt(ctx, prompt)
			if err != nil {
				if !applyMode && len(plan.Commands) > 0 && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")) {
					logf("[deploy] warning: planner request timed out after %d page(s); continuing with partial plan (%d command(s))", pageRound-1, len(plan.Commands))
					break
				}
				return fmt.Errorf("plan generation failed: %w", err)
			}

			cleaned := aiClient.CleanJSONResponse(resp)
			page, err := deploy.ParsePlanPage(cleaned)
			if err != nil {
				repairedPage, repairedRaw, rErr := deploy.RepairPlanPageWithLLM(ctx, aiClient.AskPrompt, aiClient.CleanJSONResponse, planProvider, planningContext, projectSummaryForLLM, cleaned, pageFormatHint, logf)
				if rErr == nil && repairedPage != nil {
					logf("[deploy] plan page parse auto-repaired via LLM (root=%s)", jsonRootKind(repairedRaw))
					page = repairedPage
					cleaned = repairedRaw
					err = nil
				}
			}
			if err != nil {
				consecutivePageFailures++
				pageFormatHint = "Last response was invalid JSON for this schema (often an array of prose strings). Return ONLY one JSON object with keys: done, commands, optional summary, optional notes. Do not return arrays of explanations."
				logf("[deploy] warning: plan page parse failed (%v, root=%s, sample=%q), retrying (page %d/%d)...", err, jsonRootKind(cleaned), compactOneLine(cleaned, 180), pageRound, maxPlanPages)
				if consecutivePageFailures >= earlyRepairAfterFailures && len(plan.Commands) > 0 {
					logf("[deploy] warning: switching early to deterministic repair after %d consecutive page failures", consecutivePageFailures)
					break
				}
				if consecutivePageFailures >= maxConsecutivePageFailures {
					if !applyMode && len(plan.Commands) > 0 {
						logf("[deploy] warning: stopping after %d consecutive page failures; continuing with partial plan (%d command(s))", consecutivePageFailures, len(plan.Commands))
						break
					}
					return fmt.Errorf("plan generation failed: too many consecutive page parse failures (%d)", consecutivePageFailures)
				}
				continue
			}

			if len(page.Commands) > 0 {
				// Normalize args and validate command shapes via maker.ParsePlan.
				tmp := &maker.Plan{Provider: planProvider, Question: "", Summary: "", Commands: page.Commands}
				tmpJSON, _ := json.Marshal(tmp)
				normalized, nErr := maker.ParsePlan(string(tmpJSON))
				if nErr != nil {
					repairedPage, repairedRaw, rErr := deploy.RepairPlanPageWithLLM(
						ctx,
						aiClient.AskPrompt,
						aiClient.CleanJSONResponse,
						planProvider,
						planningContext,
						projectSummaryForLLM,
						cleaned,
						"Last response included command args that failed normalization. Return command args arrays only and keep the same deployment intent.",
						logf,
					)
					if rErr == nil && repairedPage != nil && len(repairedPage.Commands) > 0 {
						tmp = &maker.Plan{Provider: planProvider, Question: "", Summary: "", Commands: repairedPage.Commands}
						tmpJSON, _ = json.Marshal(tmp)
						normalized, nErr = maker.ParsePlan(string(tmpJSON))
						if nErr == nil {
							logf("[deploy] plan page command normalization auto-repaired via LLM")
							page.Commands = normalized.Commands
							consecutivePageFailures = 0
							pageFormatHint = ""
							goto pageNormalized
						}
						logf("[deploy] warning: auto-repaired page still invalid (%v, sample=%q)", nErr, compactOneLine(repairedRaw, 180))
					}
					consecutivePageFailures++
					pageFormatHint = "Last response included commands that failed normalization. Return CLI argument arrays only, with no prose fields beyond reason/produces."
					logf("[deploy] warning: plan page had invalid commands (%v), retrying (page %d/%d)...", nErr, pageRound, maxPlanPages)
					if consecutivePageFailures >= earlyRepairAfterFailures && len(plan.Commands) > 0 {
						logf("[deploy] warning: switching early to deterministic repair after %d consecutive page failures", consecutivePageFailures)
						break
					}
					if consecutivePageFailures >= maxConsecutivePageFailures {
						if !applyMode && len(plan.Commands) > 0 {
							logf("[deploy] warning: stopping after %d consecutive page failures; continuing with partial plan (%d command(s))", consecutivePageFailures, len(plan.Commands))
							break
						}
						return fmt.Errorf("plan generation failed: too many consecutive invalid plan pages (%d)", consecutivePageFailures)
					}
					continue
				}
				page.Commands = normalized.Commands
				if len(page.Commands) > maxCommandsPerPage {
					logf("[deploy] warning: page returned %d commands; clamping to %d", len(page.Commands), maxCommandsPerPage)
					page.Commands = page.Commands[:maxCommandsPerPage]
				}
			}
		pageNormalized:
			consecutivePageFailures = 0
			pageFormatHint = ""

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

			if isOpenClawDeploy {
				if patched := deploy.ApplyOpenClawPlanAutofix(plan, rp, intel.DeepAnalysis, logf); patched != nil {
					plan = patched
				}
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
					logf("[deploy] warning: planning is stuck with hard issues=%d; continuing so execution/self-heal can proceed", len(mustFixIssues))
				} else {
					logf("[deploy] warning: planning is stuck but continuing in plan-only mode")
				}
				break
			}

			if page.Done {
				// Ignore done=true if deterministic hard issues remain; force another page.
				if len(mustFixIssues) == 0 {
					break
				}
				logf("[deploy] warning: model returned done=true but deterministic issues remain; continuing")
			}

			if isOpenClawDeploy {
				if len(plan.Commands) >= openClawHardPlanCommands {
					logf("[deploy] warning: openclaw plan exceeded hard command ceiling (%d); moving to validation/repair", openClawHardPlanCommands)
					break
				}
				if len(plan.Commands) >= openClawSoftPlanCommands && len(mustFixIssues) == 0 && added <= 1 {
					logf("[deploy] info: openclaw plan reached soft ceiling (%d) with low incremental progress; moving to validation/repair", openClawSoftPlanCommands)
					break
				}
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

			if lastDetValidation != nil && !lastDetValidation.IsValid {
				logf("[deploy] deterministic hard-repair is disabled; continuing with LLM validation/repair (issues=%d)", len(lastDetValidation.Issues))
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
			triage := deploy.TriageValidationForRepair(validation)
			if len(triage.LikelyNoise) > 0 || len(triage.ContextNeeded) > 0 {
				logf("[deploy] triage: hard=%d noise=%d context-needed=%d", len(triage.Hard.Issues), len(triage.LikelyNoise), len(triage.ContextNeeded))
			}
			if triage.Hard == nil || triage.Hard.IsValid || len(triage.Hard.Issues) == 0 {
				logf("[deploy] no hard-fixable issues after triage; skipping repair loop")
				goto finalReviewPass
			}

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
				LLMContext:          planningContext,
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
			currentValidation := triage.Hard
			currentPlanJSON := string(planJSON)
			for r := 1; r <= maxRepairRounds; r++ {
				baselinePlan := plan
				logf("[deploy] attempting plan repair (round %d/%d)...", r, maxRepairRounds)
				repairedRaw, rErr := repairAgent.Repair(ctx, currentPlanJSON, currentValidation, repairCtx)
				if rErr != nil {
					logf("[deploy] warning: repair failed (%v); continuing with current plan so execution/self-heal can proceed", rErr)
					break
				}
				repaired, pErr := maker.ParsePlan(repairedRaw)
				if pErr != nil {
					repaired, pErr = deploy.RepairPlanJSONWithLLM(ctx, aiClient.AskPrompt, aiClient.CleanJSONResponse, planningContext, projectSummaryForLLM, repairedRaw, currentPlanJSON, currentValidation.Issues, requiredLaunchOps, logf)
					if pErr == nil {
						logf("[deploy] repair round %d JSON auto-fixed via LLM", r)
					}
				}
				if pErr != nil {
					logf("[deploy] warning: repair output remained unparseable (%v); continuing with current plan so execution/self-heal can proceed", pErr)
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
				retentionContext := append([]string{}, currentValidation.Issues...)
				retentionContext = append(retentionContext, currentValidation.Fixes...)
				if retainErr := enforceStrictPlanRetention(baselinePlan, repaired, requiredLaunchOps, retentionContext); retainErr != nil {
					logf("[deploy] warning: retention guard rejected repair candidate; keeping previous plan: %v", retainErr)
					continue
				}
				// Keep the latest repaired candidate even if validator still has concerns.
				plan = repaired

				repairedJSON, _ := json.MarshalIndent(repaired, "", "  ")
				invariants := deploy.CheckBulkRepairInvariants(repaired, rp, intel.DeepAnalysis)
				if invariants != nil && !invariants.IsValid {
					logf("[deploy] bulk invariant check failed after repair round %d (issues=%d)", r, len(invariants.Issues))
					for i, issue := range invariants.Issues {
						if i >= 8 {
							break
						}
						logf("[deploy]   invariant: %s", strings.TrimSpace(issue))
					}
					currentValidation = invariants
					currentPlanJSON = string(repairedJSON)
					if r == maxRepairRounds {
						if applyMode {
							logf("[deploy] warning: invariants still failing after final repair round (issues=%d); continuing so execution/self-heal can proceed", len(invariants.Issues))
						} else {
							logf("[deploy] warning: invariants still failing after final repair round; continuing in plan-only mode")
						}
					}
					continue
				}
				repairedValidation, _, vErr := deploy.ValidatePlan(ctx,
					string(repairedJSON), rp, intel.DeepAnalysis,
					intel.Docker,
					false,
					aiClient.AskPrompt, aiClient.CleanJSONResponse, logf,
				)
				if vErr != nil {
					if applyMode {
						logf("[deploy] warning: validation failed after repair (%v); continuing with current plan so execution/self-heal can proceed", vErr)
					} else {
						logf("[deploy] warning: validation failed after repair in plan-only mode (%v); continuing with deterministically valid plan", vErr)
					}
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
					roundTriage := deploy.TriageValidationForRepair(repairedValidation)
					if len(roundTriage.LikelyNoise) > 0 || len(roundTriage.ContextNeeded) > 0 {
						logf("[deploy] triage (round %d): hard=%d noise=%d context-needed=%d", r, len(roundTriage.Hard.Issues), len(roundTriage.LikelyNoise), len(roundTriage.ContextNeeded))
					}
					currentValidation = roundTriage.Hard
					logf("[deploy] repair round %d still invalid (hard issues=%d)", r, len(currentValidation.Issues))
					for i, issue := range currentValidation.Issues {
						if i >= 12 {
							logf("[deploy]   issue: (and %d more)", len(currentValidation.Issues)-i)
							break
						}
						logf("[deploy]   issue: %s", strings.TrimSpace(issue))
					}
				}

				if r == maxRepairRounds {
					issueCount := 0
					if currentValidation != nil {
						issueCount = len(currentValidation.Issues)
					}
					if applyMode {
						logf("[deploy] warning: plan is still LLM-invalid after repair (issues=%d); continuing so execution/self-heal can proceed", issueCount)
					} else {
						logf("[deploy] warning: plan is still LLM-invalid after repair (issues=%d), but deterministic checks passed; returning plan in plan-only mode", issueCount)
					}
				}
			}
		}

		// Final non-blocking review pass: allow the reviewer agent to add missing
		// requirement commands to the latest plan (e.g. OpenClaw AWS CloudFront HTTPS).
	finalReviewPass:
		{
			reviewer := deploy.NewPlanReviewAgent(aiClient.AskPrompt, aiClient.CleanJSONResponse, logf)
			currentPlanJSON, _ := json.MarshalIndent(plan, "", "  ")
			isOpenClawRepo := deploy.IsOpenClawRepo(rp, intel.DeepAnalysis)
			openClawCloudFrontMissing := false
			if isOpenClawRepo {
				openClawCloudFrontMissing = !deploy.HasOpenClawCloudFront(string(currentPlanJSON))
			}
			reviewIssues := make([]string, 0, 24)
			reviewFixes := make([]string, 0, 24)
			reviewWarnings := make([]string, 0, 16)
			if det := deploy.DeterministicValidatePlan(string(currentPlanJSON), rp, intel.DeepAnalysis, intel.Docker); det != nil {
				reviewIssues = append(reviewIssues, det.Issues...)
				reviewFixes = append(reviewFixes, det.Fixes...)
				reviewWarnings = append(reviewWarnings, det.Warnings...)
			}
			if intel.Validation != nil {
				reviewIssues = append(reviewIssues, intel.Validation.Issues...)
				reviewFixes = append(reviewFixes, intel.Validation.Fixes...)
				reviewWarnings = append(reviewWarnings, intel.Validation.Warnings...)
			}
			dedupe := func(in []string, max int) []string {
				seen := make(map[string]struct{}, len(in))
				out := make([]string, 0, len(in))
				for _, raw := range in {
					v := strings.TrimSpace(raw)
					if v == "" {
						continue
					}
					if _, ok := seen[v]; ok {
						continue
					}
					seen[v] = struct{}{}
					out = append(out, v)
					if max > 0 && len(out) >= max {
						break
					}
				}
				return out
			}
			reviewIssues = dedupe(reviewIssues, 20)
			reviewFixes = dedupe(reviewFixes, 20)
			reviewWarnings = dedupe(reviewWarnings, 12)
			reviewTriage := deploy.TriageValidationForRepair(&deploy.PlanValidation{
				IsValid:  len(reviewIssues) == 0,
				Issues:   reviewIssues,
				Fixes:    reviewFixes,
				Warnings: reviewWarnings,
			})
			reviewIssues = dedupe(reviewTriage.Hard.Issues, 20)
			reviewFixes = dedupe(reviewTriage.Hard.Fixes, 20)
			reviewWarnings = dedupe(reviewTriage.Hard.Warnings, 12)
			if len(reviewTriage.LikelyNoise) > 0 || len(reviewTriage.ContextNeeded) > 0 {
				logf("[deploy] final review triage: hard=%d noise=%d context-needed=%d", len(reviewIssues), len(reviewTriage.LikelyNoise), len(reviewTriage.ContextNeeded))
			}

			projectSummary := rp.Summary
			projectCharacteristics := make([]string, 0, 12)
			if intel.DeepAnalysis != nil {
				if strings.TrimSpace(intel.DeepAnalysis.AppDescription) != "" {
					projectSummary = strings.TrimSpace(intel.DeepAnalysis.AppDescription)
				}
				if strings.TrimSpace(intel.DeepAnalysis.Complexity) != "" {
					projectCharacteristics = append(projectCharacteristics, "Complexity: "+strings.TrimSpace(intel.DeepAnalysis.Complexity))
				}
				if intel.DeepAnalysis.ListeningPort > 0 {
					projectCharacteristics = append(projectCharacteristics, fmt.Sprintf("Listening port: %d", intel.DeepAnalysis.ListeningPort))
				}
				if len(intel.DeepAnalysis.Services) > 0 {
					projectCharacteristics = append(projectCharacteristics, "Services: "+strings.Join(intel.DeepAnalysis.Services, ", "))
				}
				if len(intel.DeepAnalysis.ExternalDeps) > 0 {
					projectCharacteristics = append(projectCharacteristics, "External deps: "+strings.Join(intel.DeepAnalysis.ExternalDeps, ", "))
				}
			}
			if rp.HasDocker || (intel.Docker != nil && intel.Docker.HasCompose) {
				projectCharacteristics = append(projectCharacteristics, "Runtime: Docker/Compose")
			}
			if isOpenClawRepo {
				projectCharacteristics = append(projectCharacteristics, "OpenClaw pairing requires HTTPS URL")
			}
			projectCharacteristics = dedupe(projectCharacteristics, 12)

			reviewCtx := deploy.PlanReviewContext{
				Provider:                  intel.Architecture.Provider,
				Method:                    intel.Architecture.Method,
				RepoURL:                   rp.RepoURL,
				LLMContext:                planningContext,
				ProjectSummary:            projectSummary,
				ProjectCharacteristics:    projectCharacteristics,
				RequiredLaunchOps:         requiredLaunchOps,
				IsOpenClaw:                isOpenClawRepo,
				OpenClawCloudFrontMissing: openClawCloudFrontMissing,
				IsWordPress:               deploy.IsWordPressRepo(rp, intel.DeepAnalysis),
				Issues:                    reviewIssues,
				Fixes:                     reviewFixes,
				Warnings:                  reviewWarnings,
			}

			baselinePlan := plan
			reviewedRaw, reviewErr := reviewer.Review(ctx, string(currentPlanJSON), reviewCtx)
			if reviewErr != nil {
				logf("[deploy] warning: final plan review skipped (%v)", reviewErr)
			} else {
				reviewedPlan, parseErr := maker.ParsePlan(reviewedRaw)
				if parseErr != nil {
					reviewedPlan, parseErr = deploy.RepairPlanJSONWithLLM(ctx, aiClient.AskPrompt, aiClient.CleanJSONResponse, planningContext, projectSummaryForLLM, reviewedRaw, string(currentPlanJSON), reviewIssues, requiredLaunchOps, logf)
					if parseErr != nil {
						logf("[deploy] warning: final plan review produced unparseable plan (%v); keeping current plan", parseErr)
					} else {
						logf("[deploy] final review JSON auto-fixed via LLM")
					}
				}
				if reviewedPlan != nil && len(reviewedPlan.Commands) > 0 && parseErr == nil {
					reviewedPlan.Provider = intel.Architecture.Provider
					reviewedPlan.Question = fmt.Sprintf("Deploy %s to %s (%s)", rp.RepoURL, strings.ToLower(strings.TrimSpace(reviewedPlan.Provider)), intel.Architecture.Method)
					if reviewedPlan.CreatedAt.IsZero() {
						reviewedPlan.CreatedAt = time.Now().UTC()
					}
					if reviewedPlan.Version == 0 {
						reviewedPlan.Version = maker.CurrentPlanVersion
					}
					reviewedPlan = deploy.SanitizePlanConservative(reviewedPlan, rp, intel.DeepAnalysis, intel.Docker, logf)
					retentionContext := append([]string{}, reviewIssues...)
					retentionContext = append(retentionContext, reviewFixes...)
					if retainErr := enforceStrictPlanRetention(baselinePlan, reviewedPlan, requiredLaunchOps, retentionContext); retainErr != nil {
						logf("[deploy] warning: retention guard rejected final review candidate; keeping previous plan: %v", retainErr)
						goto skipFinalReviewApply
					}
					plan = reviewedPlan
					logf("[deploy] final plan review applied (commands=%d)", len(plan.Commands))
				}
			skipFinalReviewApply:
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

		if reviewedPlan, err := deploy.RunGenericPlanIntegrityPassWithLLM(
			ctx,
			aiClient.AskPrompt,
			aiClient.CleanJSONResponse,
			plan,
			planningContext,
			projectSummaryForLLM,
			requiredLaunchOps,
			logf,
		); err != nil {
			logf("[deploy] warning: generic integrity pass skipped (%v)", err)
		} else if reviewedPlan != nil {
			reviewedPlan = deploy.SanitizePlanConservative(reviewedPlan, rp, intel.DeepAnalysis, intel.Docker, logf)
			if retainErr := enforceStrictPlanRetention(plan, reviewedPlan, requiredLaunchOps, nil); retainErr != nil {
				logf("[deploy] warning: retention guard rejected integrity-pass candidate; keeping previous plan: %v", retainErr)
				goto skipIntegrityApply
			}
			plan = reviewedPlan
			logf("[deploy] generic integrity pass applied (commands=%d)", len(plan.Commands))
		}
	skipIntegrityApply:

		if patched := deploy.ApplyOpenClawPlanAutofix(plan, rp, intel.DeepAnalysis, logf); patched != nil {
			plan = patched
		}

		openClawUnresolvedApplyBlock := false
		openClawUnresolvedCritical := make([]string, 0, 12)
		if isOpenClawDeploy {
			if unresolved := deploy.GetUnresolvedPlaceholders(plan); len(unresolved) > 0 {
				if !deploy.AllPlaceholdersAreProduced(plan, unresolved) {
					openClawUnresolvedApplyBlock = true
					openClawUnresolvedCritical = append(openClawUnresolvedCritical, unresolved...)
					logf("[deploy] warning: openclaw plan has unresolved non-runtime placeholders (%d): %v", len(unresolved), unresolved)
				} else {
					logf("[deploy] openclaw placeholders are runtime-produced; continuing with non-deterministic plan")
				}
			}
		}

		// 8. Output plan JSON (or apply)
		normalized := normalizeShellStylePlaceholdersForExecution(plan)
		if normalized > 0 {
			logf("[deploy] normalized %d shell-style placeholder token(s) to angle format before execution", normalized)
		}
		if remaining := countShellStylePlaceholders(plan); remaining > 0 {
			logf("[deploy] warning: %d shell-style placeholder token(s) remain; continuing without hard fail so self-healing/runtime binding can resolve them", remaining)
		}

		planJSON, err = json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}

		if !applyMode {
			fmt.Println(string(planJSON))
			return nil
		}

		if isOpenClawDeploy && openClawUnresolvedApplyBlock {
			capped := openClawUnresolvedCritical
			if len(capped) > 12 {
				capped = capped[:12]
			}
			return fmt.Errorf("openclaw apply blocked: unresolved non-runtime placeholders remain (%d): %v", len(openClawUnresolvedCritical), capped)
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
				outputBindings[name] = value
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
		if isOpenClawDeploy {
			seedOpenClawRuntimeEnvBindings(outputBindings, userConfig)
			outputBindings["FORCE_IMAGE_DEPLOY"] = "true"
			fmt.Fprintf(os.Stderr, "[deploy] openclaw runtime: forcing ECR image deploy workflow\n")
		}
		if enforceImageDeploy {
			outputBindings["FORCE_IMAGE_DEPLOY"] = "true"
			fmt.Fprintf(os.Stderr, "[deploy] image deploy enforcement enabled (ECR image build/pull workflow)\n")
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
			fmt.Fprintf(os.Stderr, "[deploy] warning: openclaw HTTPS pairing URL missing (CloudFront output not available); continuing\n")
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

func jsonRootKind(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "empty"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	default:
		return "scalar"
	}
}

func compactOneLine(raw string, limit int) string {
	v := strings.TrimSpace(raw)
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\t", " ")
	for strings.Contains(v, "  ") {
		v = strings.ReplaceAll(v, "  ", " ")
	}
	if limit > 0 && len(v) > limit {
		return strings.TrimSpace(v[:limit]) + "…"
	}
	return strings.TrimSpace(v)
}

var shellStylePlaceholderRe = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

func withOneClickDeployContext(base, provider, method string, enforceImageDeploy bool) string {
	context := buildOneClickDeployObjective(provider, method, enforceImageDeploy)
	base = strings.TrimSpace(base)
	if base == "" {
		return context
	}
	return context + "\n\n" + base
}

func buildOneClickDeployObjective(provider, method string, enforceImageDeploy bool) string {
	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "" {
		prov = "aws"
	}
	m := strings.ToLower(strings.TrimSpace(method))
	if m == "" {
		m = "ec2"
	}
	if enforceImageDeploy {
		return fmt.Sprintf("[one-click deploy objective]\nGenerate command plan steps for one-click deploy. The runner executes plan.commands strictly in order, sequentially, to provision infrastructure and ship the app to production.\nUse provider=%s method=%s. Keep commands actionable/idempotent and preserve earlier produced bindings for later steps.\nImage deployment is enforced: do not rely on docker build on EC2 user-data. Ensure ECR image build/push + IMAGE_URI/ECR_URI bindings are preserved and workload launches by pulling that image.", prov, m)
	}
	return fmt.Sprintf("[one-click deploy objective]\nGenerate command plan steps for one-click deploy. The runner executes plan.commands strictly in order, sequentially, to provision infrastructure and ship the app to production.\nUse provider=%s method=%s. Keep commands actionable/idempotent and preserve earlier produced bindings for later steps.", prov, m)
}

func seedOpenClawRuntimeEnvBindings(bindings map[string]string, cfg *deploy.UserConfig) {
	if bindings == nil {
		return
	}

	lookup := func(key string) string {
		if cfg != nil {
			if v := strings.TrimSpace(cfg.EnvVars[key]); v != "" {
				return v
			}
		}
		return strings.TrimSpace(os.Getenv(key))
	}

	for _, key := range []string{
		"OPENCLAW_GATEWAY_TOKEN",
		"OPENCLAW_GATEWAY_PASSWORD",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GEMINI_API_KEY",
		"AI_GATEWAY_API_KEY",
		"DISCORD_BOT_TOKEN",
		"TELEGRAM_BOT_TOKEN",
		"OPENCLAW_CONFIG_DIR",
		"OPENCLAW_WORKSPACE_DIR",
	} {
		val := lookup(key)
		if val == "" {
			continue
		}
		if strings.TrimSpace(bindings["ENV_"+key]) == "" {
			bindings["ENV_"+key] = val
		}
		if strings.TrimSpace(bindings[key]) == "" {
			bindings[key] = val
		}
	}
}

func enforceStrictPlanRetention(baseline *maker.Plan, candidate *maker.Plan, requiredLaunchOps []string, issueTexts []string) error {
	if baseline == nil || len(baseline.Commands) == 0 {
		return nil
	}
	if candidate == nil || len(candidate.Commands) == 0 {
		return fmt.Errorf("candidate plan has no commands")
	}

	removedCount := len(baseline.Commands) - len(candidate.Commands)
	if removedCount > 0 {
		if !issuesAllowCommandRemoval(issueTexts) {
			return fmt.Errorf("candidate shrank command count from %d to %d without explicit removal intent in issues/fixes", len(baseline.Commands), len(candidate.Commands))
		}
		maxAllowedRemoval := len(baseline.Commands) / 4
		if maxAllowedRemoval < 2 {
			maxAllowedRemoval = 2
		}
		if removedCount > maxAllowedRemoval {
			return fmt.Errorf("candidate removed too many commands (%d); exceeds allowed focused-diff limit (%d)", removedCount, maxAllowedRemoval)
		}
	}

	basePairs := commandPairCounts(baseline)
	candPairs := commandPairCounts(candidate)
	for pair, baseCount := range basePairs {
		candCount := candPairs[pair]
		if candCount >= baseCount {
			continue
		}
		if !pairChangeMentionedInIssues(pair, issueTexts) {
			return fmt.Errorf("candidate removed %d command(s) for '%s' without issue-driven justification", baseCount-candCount, pair)
		}
	}

	if len(requiredLaunchOps) > 0 && !hasRequiredLaunchOp(candidate, requiredLaunchOps) {
		return fmt.Errorf("candidate removed required launch operation; expected one of: %s", strings.Join(requiredLaunchOps, " | "))
	}

	return nil
}

func issuesAllowCommandRemoval(issueTexts []string) bool {
	for _, issue := range issueTexts {
		line := strings.ToLower(strings.TrimSpace(issue))
		if line == "" {
			continue
		}
		if strings.Contains(line, "remove") ||
			strings.Contains(line, "delete") ||
			strings.Contains(line, "drop") ||
			strings.Contains(line, "orphan") ||
			strings.Contains(line, "unused") ||
			strings.Contains(line, "redundant") ||
			strings.Contains(line, "duplicate") ||
			strings.Contains(line, "not used") {
			return true
		}
	}
	return false
}

func commandPairCounts(plan *maker.Plan) map[string]int {
	out := make(map[string]int)
	if plan == nil {
		return out
	}
	for _, c := range plan.Commands {
		pair := commandPair(c.Args)
		if pair == "" {
			continue
		}
		out[pair]++
	}
	return out
}

func commandPair(args []string) string {
	if len(args) == 0 {
		return ""
	}
	first := strings.ToLower(strings.TrimSpace(args[0]))
	if first == "" {
		return ""
	}
	if len(args) == 1 {
		return first
	}
	second := strings.ToLower(strings.TrimSpace(args[1]))
	if second == "" {
		return first
	}
	return first + " " + second
}

func pairChangeMentionedInIssues(pair string, issueTexts []string) bool {
	pair = strings.ToLower(strings.TrimSpace(pair))
	if pair == "" {
		return false
	}
	parts := strings.Fields(pair)
	for _, issue := range issueTexts {
		line := strings.ToLower(strings.TrimSpace(issue))
		if line == "" {
			continue
		}
		if strings.Contains(line, pair) {
			return true
		}
		if len(parts) == 2 && strings.Contains(line, parts[0]) && strings.Contains(line, parts[1]) {
			return true
		}
	}
	return false
}

func hasRequiredLaunchOp(plan *maker.Plan, requiredLaunchOps []string) bool {
	if plan == nil || len(plan.Commands) == 0 || len(requiredLaunchOps) == 0 {
		return len(requiredLaunchOps) == 0
	}
	required := make(map[string]struct{}, len(requiredLaunchOps))
	for _, op := range requiredLaunchOps {
		tok := strings.ToLower(strings.TrimSpace(op))
		if tok == "" {
			continue
		}
		required[tok] = struct{}{}
	}
	if len(required) == 0 {
		return true
	}
	for _, c := range plan.Commands {
		pair := commandPair(c.Args)
		if pair == "" {
			continue
		}
		if _, ok := required[pair]; ok {
			return true
		}
	}
	return false
}

func normalizeShellStylePlaceholdersForExecution(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	changed := 0
	for ci := range plan.Commands {
		if len(plan.Commands[ci].Args) == 0 {
			continue
		}
		for ai, arg := range plan.Commands[ci].Args {
			v := strings.TrimSpace(arg)
			if v == "" || !strings.Contains(v, "${") {
				continue
			}
			if strings.Contains(v, "\n") || strings.HasPrefix(v, "#!") || strings.HasPrefix(strings.ToLower(v), "#cloud-config") {
				continue
			}
			n := shellStylePlaceholderRe.ReplaceAllString(v, "<$1>")
			if n != v {
				plan.Commands[ci].Args[ai] = n
				changed++
			}
		}
	}
	return changed
}

func countShellStylePlaceholders(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	total := 0
	for _, cmd := range plan.Commands {
		for _, arg := range cmd.Args {
			total += len(shellStylePlaceholderRe.FindAllString(arg, -1))
		}
	}
	return total
}

func compactPlanningContext(text, provider string) string {
	maxChars := maxPlanningPromptChars(provider)
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= maxChars {
		return trimmed
	}
	return summarizePlanningContext(trimmed, maxChars)
}

func maxPlanningPromptChars(provider string) int {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "gemini", "gemini-api":
		return 280000
	case "openai":
		return 230000
	case "anthropic":
		return 170000
	default:
		return 145000
	}
}

func summarizePlanningContext(text string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if len(text) <= maxChars {
		return text
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	keyed := make([]string, 0, len(lines))
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		ll := strings.ToLower(l)
		if strings.Contains(ll, "required") ||
			strings.Contains(ll, "must") ||
			strings.Contains(ll, "env") ||
			strings.Contains(ll, "port") ||
			strings.Contains(ll, "security") ||
			strings.Contains(ll, "iam") ||
			strings.Contains(ll, "ssm") ||
			strings.Contains(ll, "docker") ||
			strings.Contains(ll, "openclaw") {
			keyed = append(keyed, l)
		}
	}

	var b strings.Builder
	b.WriteString("[summarized planning context]\n")
	for _, line := range keyed {
		if b.Len()+len(line)+2 > maxChars-300 {
			break
		}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}

	headSize := maxChars / 3
	tailSize := maxChars / 4
	if headSize < 1000 {
		headSize = 1000
	}
	if tailSize < 1000 {
		tailSize = 1000
	}
	head := text
	if len(head) > headSize {
		head = head[:headSize]
	}
	tail := text
	if len(tail) > tailSize {
		tail = tail[len(tail)-tailSize:]
	}

	b.WriteString("\n[head]\n")
	b.WriteString(head)
	b.WriteString("\n\n[tail]\n")
	b.WriteString(tail)

	out := strings.TrimSpace(b.String())
	if len(out) > maxChars {
		out = strings.TrimSpace(out[:maxChars]) + "…"
	}
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
	deployCmd.Flags().Bool("enforce-image-deploy", false, "Force ECR image-based deploy path (avoid docker build-on-EC2 user-data)")
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
