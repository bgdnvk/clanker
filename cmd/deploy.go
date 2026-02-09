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
  clanker deploy https://github.com/user/repo --provider cloudflare
  clanker deploy https://github.com/user/repo --profile prod`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoURL := args[0]
		ctx := context.Background()
		debug := viper.GetBool("debug")
		profile, _ := cmd.Flags().GetString("profile")
		applyMode, _ := cmd.Flags().GetBool("apply")
		aiProfile, _ := cmd.Flags().GetString("ai-profile")
		openaiKey, _ := cmd.Flags().GetString("openai-key")
		anthropicKey, _ := cmd.Flags().GetString("anthropic-key")
		geminiKey, _ := cmd.Flags().GetString("gemini-key")
		targetProvider, _ := cmd.Flags().GetString("provider")

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

		// 4. Run multi-phase intelligence pipeline (explore → deep analysis → infra scan → architecture)
		intel, err := deploy.RunIntelligence(ctx, rp,
			aiClient.AskPrompt,
			aiClient.CleanJSONResponse,
			debug, targetProvider, targetProfile, region, logf,
		)
		if err != nil {
			return fmt.Errorf("intelligence pipeline failed: %w", err)
		}

		enrichedQuestion := intel.EnrichedPrompt
		if debug {
			fmt.Fprintf(os.Stderr, "[deploy] enriched prompt:\n%s\n", enrichedQuestion)
		}

		// 4. Generate the maker plan via LLM
		fmt.Fprintf(os.Stderr, "[deploy] phase 3: generating execution plan with %s ...\n", provider)

		const maxValidationRounds = 2
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

		// 7. Output plan JSON (or apply)
		planJSON, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}

		if !applyMode {
			fmt.Println(string(planJSON))
			return nil
		}

		// apply mode: execute the plan
		fmt.Fprintf(os.Stderr, "[deploy] applying plan (%d commands)...\n", len(plan.Commands))
		execOpts := maker.ExecOptions{
			Profile:    targetProfile,
			Region:     region,
			Writer:     os.Stdout,
			Destroyer:  false,
			AIProvider: provider,
			AIAPIKey:   apiKey,
			AIProfile:  aiProfile,
			Debug:      debug,
		}
		if targetProvider == "cloudflare" {
			execOpts.Profile = ""
			execOpts.Region = ""
		}
		return maker.ExecutePlan(ctx, plan, execOpts)
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
}
