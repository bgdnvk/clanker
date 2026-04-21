package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bgdnvk/clanker/internal/aws"
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/dbcontext"
	"github.com/bgdnvk/clanker/internal/digitalocean"
	"github.com/bgdnvk/clanker/internal/gcp"
	"github.com/bgdnvk/clanker/internal/hetzner"
	"github.com/bgdnvk/clanker/internal/k8s"
	tfclient "github.com/bgdnvk/clanker/internal/terraform"
	"github.com/bgdnvk/clanker/internal/vercel"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	defaultDeepResearchQuestion     = "Analyze the current infrastructure for bottlenecks, resilience risks, cost optimization opportunities, misconfigurations, stale or unused resources, and recent log signals. Prioritize cost and identify the clearest next actions."
	deepResearchResultMarker        = "::clanker-deep-research-result::"
	runtimeDeepResearchEstateEnv    = "CLANKER_RUNTIME_DEEP_RESEARCH_ESTATE_JSON"
	maxDeepResearchNarrativeBullets = 4
	maxDeepResearchFindings         = 14
)

type deepResearchEstateSnapshot struct {
	Resources   []deepResearchResource `json:"resources"`
	TotalCost   float64                `json:"totalCost"`
	LastUpdated string                 `json:"lastUpdated,omitempty"`
	TerraformOK bool                   `json:"terraformOk,omitempty"`
}

type deepResearchResource struct {
	ID                 string                           `json:"id"`
	Type               string                           `json:"type"`
	Name               string                           `json:"name"`
	Region             string                           `json:"region"`
	State              string                           `json:"state"`
	CreatedAt          string                           `json:"createdAt,omitempty"`
	Tags               map[string]string                `json:"tags,omitempty"`
	Attributes         map[string]interface{}           `json:"attributes,omitempty"`
	MonthlyPrice       float64                          `json:"monthlyPrice,omitempty"`
	IAMRole            string                           `json:"iamRole,omitempty"`
	IAMPolicies        []string                         `json:"iamPolicies,omitempty"`
	CanInvokeResources []string                         `json:"canInvokeResources,omitempty"`
	Connections        []string                         `json:"connections,omitempty"`
	TypedConnections   []deepResearchResourceConnection `json:"typedConnections,omitempty"`
}

type deepResearchResourceConnection struct {
	TargetID       string `json:"targetId"`
	ConnectionType string `json:"connectionType,omitempty"`
	Label          string `json:"label,omitempty"`
}

type deepResearchResult struct {
	Query       string                     `json:"query"`
	GeneratedAt string                     `json:"generatedAt"`
	Summary     deepResearchSummary        `json:"summary"`
	Findings    []deepResearchFinding      `json:"findings"`
	Providers   []deepResearchProviderRoll `json:"providers,omitempty"`
	Subagents   []deepResearchSubagentRun  `json:"subagents,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty"`
	Narrative   []string                   `json:"narrative,omitempty"`
}

type deepResearchSummary struct {
	TotalResources          int      `json:"totalResources"`
	TotalMonthlyCost        float64  `json:"totalMonthlyCost"`
	EstimatedMonthlySavings float64  `json:"estimatedMonthlySavings"`
	CriticalFindings        int      `json:"criticalFindings"`
	HighFindings            int      `json:"highFindings"`
	BottleneckFindings      int      `json:"bottleneckFindings"`
	CostFindings            int      `json:"costFindings"`
	PrimaryFocus            string   `json:"primaryFocus,omitempty"`
	Regions                 []string `json:"regions,omitempty"`
	TopRisks                []string `json:"topRisks,omitempty"`
}

type deepResearchEvidenceDetail struct {
	Detail   string `json:"detail"`
	Source   string `json:"source,omitempty"`
	Provider string `json:"provider,omitempty"`
	Section  string `json:"section,omitempty"`
}

type deepResearchFinding struct {
	ID                      string                       `json:"id"`
	Severity                string                       `json:"severity"`
	Category                string                       `json:"category"`
	Title                   string                       `json:"title"`
	Summary                 string                       `json:"summary"`
	ResourceID              string                       `json:"resourceId,omitempty"`
	ResourceName            string                       `json:"resourceName,omitempty"`
	ResourceType            string                       `json:"resourceType,omitempty"`
	Provider                string                       `json:"provider,omitempty"`
	Region                  string                       `json:"region,omitempty"`
	MonthlyCost             float64                      `json:"monthlyCost,omitempty"`
	EstimatedMonthlySavings float64                      `json:"estimatedMonthlySavings,omitempty"`
	Risk                    string                       `json:"risk,omitempty"`
	Score                   float64                      `json:"score,omitempty"`
	Evidence                []string                     `json:"evidence,omitempty"`
	EvidenceDetails         []deepResearchEvidenceDetail `json:"evidenceDetails,omitempty"`
	Questions               []string                     `json:"questions,omitempty"`
}

type deepResearchProviderRoll struct {
	Provider      string  `json:"provider"`
	ResourceCount int     `json:"resourceCount"`
	MonthlyCost   float64 `json:"monthlyCost"`
	ShareOfCost   float64 `json:"shareOfCost"`
}

type deepResearchSubagentRun struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

type deepResearchRunOptions struct {
	Debug               bool
	Profile             string
	GCPProject          string
	AzureSubscriptionID string
	TerraformWorkspace  string
}

type deepResearchQuestionPlan struct {
	FocusAreas         []string
	ProviderPriorities []string
	WantsLogs          bool
	WantsCost          bool
	WantsMisconfig     bool
	WantsHygiene       bool
	WantsResilience    bool
	WantsTopology      bool
	WantsDatabase      bool
	WantsCompute       bool
	WantsNetwork       bool
	WantsKubernetes    bool
	WantsTerraform     bool
	WantsCICD          bool
	DrilldownCount     int
}

type deepResearchFindingPatch struct {
	FindingID       string
	Evidence        []string
	EvidenceDetails []deepResearchEvidenceDetail
	Questions       []string
	Summary         string
	Risk            string
	ScoreDelta      float64
}

type deepResearchFindingDrilldown struct {
	Name string
	Run  func(context.Context) (deepResearchFindingPatch, deepResearchSubagentRun, []string)
}

type deepResearchPatchSubagent struct {
	Name string
	Run  func(context.Context) ([]deepResearchFindingPatch, deepResearchSubagentRun, []string)
}

type deepResearchProviderContext struct {
	Provider string
	Summary  string
	Details  []string
	Blob     string
}

type deepResearchKubernetesLogTarget struct {
	Name         string
	Namespace    string
	Phase        string
	Reason       string
	RestartCount int
	Score        int
}

type deepResearchSubagent struct {
	Name string
	Run  func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string)
}

var researchCmd = &cobra.Command{
	Use:   "research [question]",
	Short: "Run deep infrastructure research across the current estate",
	Long: `Run a multi-pass deep research analysis across the current infrastructure estate.

The command fans out across several analysis subagents, prioritizes cost findings,
and emits a final structured JSON payload that clanker-cloud can render into a report.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		question := defaultDeepResearchQuestion
		if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
			question = strings.TrimSpace(args[0])
		}

		profile, _ := cmd.Flags().GetString("profile")
		gcpProject, _ := cmd.Flags().GetString("gcp-project")
		azureSubscriptionID, _ := cmd.Flags().GetString("azure-subscription")
		workspace, _ := cmd.Flags().GetString("workspace")
		aiProfile, _ := cmd.Flags().GetString("ai-profile")
		openaiKey, _ := cmd.Flags().GetString("openai-key")
		localModelInferenceURL, _ := cmd.Flags().GetString("local-model-inference-url")
		anthropicKey, _ := cmd.Flags().GetString("anthropic-key")
		geminiKey, _ := cmd.Flags().GetString("gemini-key")
		deepseekKey, _ := cmd.Flags().GetString("deepseek-key")
		cohereKey, _ := cmd.Flags().GetString("cohere-key")
		minimaxKey, _ := cmd.Flags().GetString("minimax-key")
		openaiModel, _ := cmd.Flags().GetString("openai-model")
		anthropicModel, _ := cmd.Flags().GetString("anthropic-model")
		geminiModel, _ := cmd.Flags().GetString("gemini-model")
		deepseekModel, _ := cmd.Flags().GetString("deepseek-model")
		cohereModel, _ := cmd.Flags().GetString("cohere-model")
		minimaxModel, _ := cmd.Flags().GetString("minimax-model")
		githubModel, _ := cmd.Flags().GetString("github-model")
		debug := viper.GetBool("debug")

		if strings.TrimSpace(localModelInferenceURL) != "" {
			viper.Set("ai.providers.openai.local_model_inference_url", strings.TrimSpace(localModelInferenceURL))
		}
		applyCommandAIOverrides(aiProfile, openaiKey, anthropicKey, geminiKey, deepseekKey, cohereKey, minimaxKey, openaiModel, anthropicModel, geminiModel, deepseekModel, cohereModel, minimaxModel, githubModel)

		estate, warnings := loadDeepResearchEstateSnapshot()
		options := deepResearchRunOptions{
			Debug:               debug,
			Profile:             strings.TrimSpace(profile),
			GCPProject:          strings.TrimSpace(gcpProject),
			AzureSubscriptionID: strings.TrimSpace(azureSubscriptionID),
			TerraformWorkspace:  strings.TrimSpace(workspace),
		}

		fmt.Printf("[research] starting deep research query=%q resources=%d\n", question, len(estate.Resources))

		findings, subagents, subagentWarnings := runDeepResearchSubagents(context.Background(), question, estate, options)
		warnings = append(warnings, subagentWarnings...)

		providers := buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost)
		findings = sortAndCapDeepResearchFindings(findings)
		narrative := buildDeterministicNarrative(findings, providers)

		result := deepResearchResult{
			Query:       question,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Summary:     buildDeepResearchSummary(estate, findings),
			Findings:    findings,
			Providers:   providers,
			Subagents:   subagents,
			Warnings:    uniqueNonEmptyStrings(warnings),
			Narrative:   narrative,
		}

		if enriched, err := enrichDeepResearchNarrative(context.Background(), result, debug); err == nil && len(enriched) > 0 {
			result.Narrative = enriched
		}

		payload, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("marshal deep research result: %w", err)
		}

		fmt.Printf("%s%s\n", deepResearchResultMarker, string(payload))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(researchCmd)

	researchCmd.Flags().String("profile", "", "AWS profile to use for provider-side research helpers")
	researchCmd.Flags().String("gcp-project", "", "GCP project ID to use for provider-side research helpers")
	researchCmd.Flags().String("azure-subscription", "", "Azure subscription ID to use for provider-side research helpers")
	researchCmd.Flags().String("workspace", "", "Terraform workspace to use for provider-side research helpers")
	researchCmd.Flags().String("ai-profile", "", "AI profile to use for summary synthesis")
	researchCmd.Flags().String("openai-key", "", "OpenAI API key (overrides config)")
	researchCmd.Flags().String("local-model-inference-url", "", "Local model inference URL for OpenAI-compatible servers")
	researchCmd.Flags().String("anthropic-key", "", "Anthropic API key (overrides config)")
	researchCmd.Flags().String("gemini-key", "", "Gemini API key (overrides config and env vars)")
	researchCmd.Flags().String("deepseek-key", "", "DeepSeek API key (overrides config)")
	researchCmd.Flags().String("cohere-key", "", "Cohere API key (overrides config)")
	researchCmd.Flags().String("minimax-key", "", "MiniMax API key (overrides config)")
	researchCmd.Flags().String("openai-model", "", "OpenAI model to use (overrides config)")
	researchCmd.Flags().String("anthropic-model", "", "Anthropic model to use (overrides config)")
	researchCmd.Flags().String("gemini-model", "", "Gemini model to use (overrides config)")
	researchCmd.Flags().String("deepseek-model", "", "DeepSeek model to use (overrides config)")
	researchCmd.Flags().String("cohere-model", "", "Cohere model to use (overrides config)")
	researchCmd.Flags().String("minimax-model", "", "MiniMax model to use (overrides config)")
	researchCmd.Flags().String("github-model", "", "GitHub Models model to use (overrides config)")
}

func loadDeepResearchEstateSnapshot() (deepResearchEstateSnapshot, []string) {
	raw := strings.TrimSpace(os.Getenv(runtimeDeepResearchEstateEnv))
	if raw == "" {
		return deepResearchEstateSnapshot{}, []string{"No estate snapshot was provided by clanker-cloud; findings will rely on best-effort provider signals only."}
	}

	var estate deepResearchEstateSnapshot
	if err := json.Unmarshal([]byte(raw), &estate); err != nil {
		return deepResearchEstateSnapshot{}, []string{fmt.Sprintf("Failed to decode estate snapshot: %v", err)}
	}

	estate.Resources = normalizeDeepResearchResources(estate.Resources)
	if estate.TotalCost < 0 {
		estate.TotalCost = 0
	}
	if estate.TotalCost == 0 {
		for _, resource := range estate.Resources {
			estate.TotalCost += safeDeepResearchCost(resource.MonthlyPrice)
		}
	}
	return estate, nil
}

func normalizeDeepResearchResources(resources []deepResearchResource) []deepResearchResource {
	normalized := make([]deepResearchResource, 0, len(resources))
	for _, resource := range resources {
		resource.ID = strings.TrimSpace(resource.ID)
		if resource.ID == "" {
			continue
		}
		resource.Type = strings.TrimSpace(resource.Type)
		resource.Name = strings.TrimSpace(resource.Name)
		resource.Region = strings.TrimSpace(resource.Region)
		resource.State = strings.TrimSpace(resource.State)
		resource.CreatedAt = strings.TrimSpace(resource.CreatedAt)
		resource.MonthlyPrice = safeDeepResearchCost(resource.MonthlyPrice)
		if resource.Attributes == nil {
			resource.Attributes = map[string]interface{}{}
		}
		normalized = append(normalized, resource)
	}
	return normalized
}

func safeDeepResearchCost(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func runDeepResearchSubagents(ctx context.Context, question string, estate deepResearchEstateSnapshot, options deepResearchRunOptions) ([]deepResearchFinding, []deepResearchSubagentRun, []string) {
	plan := buildDeepResearchQuestionPlan(question, estate)
	subagents := []deepResearchSubagent{
		{
			Name: "query-planner",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				summary := fmt.Sprintf("Planned %d focus areas with %d prioritized drilldowns.", len(plan.FocusAreas), plan.DrilldownCount)
				if len(plan.ProviderPriorities) > 0 {
					summary = fmt.Sprintf("Planned %d focus areas with provider priority %s and %d prioritized drilldowns.", len(plan.FocusAreas), strings.Join(plan.ProviderPriorities, " -> "), plan.DrilldownCount)
				}
				return nil, deepResearchSubagentRun{Name: "query-planner", Status: "ok", Summary: summary, Details: buildDeepResearchPlanDetails(plan, estate)}, nil
			},
		},
		{
			Name: "estate-overview",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				providers := buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost)
				details := make([]string, 0, len(providers))
				for _, provider := range providers {
					details = append(details, fmt.Sprintf("%s: %d resources, $%.2f/mo", provider.Provider, provider.ResourceCount, provider.MonthlyCost))
				}
				status := "ok"
				summary := fmt.Sprintf("Loaded %d resources across %d provider groups.", len(estate.Resources), len(providers))
				if len(estate.Resources) == 0 {
					status = "warning"
					summary = "No estate resources were present in the runtime snapshot."
				}
				return nil, deepResearchSubagentRun{Name: "estate-overview", Status: status, Summary: summary, Details: details}, nil
			},
		},
		{
			Name: "cost-analyst",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				findings := buildCostFindings(estate.Resources, estate.TotalCost)
				savings := 0.0
				for _, finding := range findings {
					savings += finding.EstimatedMonthlySavings
				}
				summary := fmt.Sprintf("Identified %d cost findings with up to $%.2f/mo in estimated savings.", len(findings), savings)
				if len(findings) == 0 {
					summary = "No meaningful cost findings were detected in the current estate snapshot."
				}
				return findings, deepResearchSubagentRun{Name: "cost-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			Name: "topology-analyst",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				findings := buildTopologyFindings(estate.Resources)
				summary := fmt.Sprintf("Flagged %d potential bottlenecks or concentration points.", len(findings))
				if len(findings) == 0 {
					summary = "No major topology bottlenecks were inferred from the current connection graph."
				}
				return findings, deepResearchSubagentRun{Name: "topology-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			Name: "resilience-analyst",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				findings := buildResilienceFindings(estate.Resources)
				summary := fmt.Sprintf("Flagged %d resilience or operational risk findings.", len(findings))
				if len(findings) == 0 {
					summary = "No clear single-resource resilience risks were detected from the estate snapshot."
				}
				return findings, deepResearchSubagentRun{Name: "resilience-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			Name: "misconfig-analyst",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				findings := buildMisconfigurationFindings(estate.Resources)
				summary := fmt.Sprintf("Flagged %d configuration or exposure risks.", len(findings))
				if len(findings) == 0 {
					summary = "No obvious misconfiguration patterns were inferred from the current estate snapshot."
				}
				return findings, deepResearchSubagentRun{Name: "misconfig-analyst", Status: "ok", Summary: summary}, nil
			},
		},
		{
			Name: "stale-resource-analyst",
			Run: func(context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
				findings := buildStaleResourceFindings(estate.Resources)
				summary := fmt.Sprintf("Flagged %d stale or weakly-owned resource candidates.", len(findings))
				if len(findings) == 0 {
					summary = "No clear stale-resource cleanup candidates were inferred from the current snapshot."
				}
				return findings, deepResearchSubagentRun{Name: "stale-resource-analyst", Status: "ok", Summary: summary}, nil
			},
		},
	}

	findings, runs, warnings := executeDeepResearchSubagentBatch(ctx, subagents)
	findings = dedupeDeepResearchFindings(findings)
	findings = applyDeepResearchQuestionPlan(findings, plan)
	providerPatchSubagents := buildDeepResearchProviderPatchSubagents(question, plan, findings, estate, options)
	if len(providerPatchSubagents) > 0 {
		patches, patchRuns, patchWarnings := executeDeepResearchPatchSubagentBatch(ctx, providerPatchSubagents)
		findings = applyDeepResearchFindingPatches(findings, patches)
		runs = append(runs, patchRuns...)
		warnings = append(warnings, patchWarnings...)
	}

	prioritized := append([]deepResearchFinding(nil), findings...)
	prioritized = sortAndCapDeepResearchFindings(prioritized)
	drilldowns := buildDeepResearchDrilldownSubagents(question, plan, prioritized, options)
	if len(drilldowns) > 0 {
		patches, drilldownRuns, drilldownWarnings := executeDeepResearchDrilldownBatch(ctx, drilldowns)
		findings = applyDeepResearchFindingPatches(findings, patches)
		runs = append(runs, drilldownRuns...)
		warnings = append(warnings, drilldownWarnings...)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Name < runs[j].Name
	})
	return dedupeDeepResearchFindings(findings), runs, uniqueNonEmptyStrings(warnings)
}

func executeDeepResearchSubagentBatch(ctx context.Context, subagents []deepResearchSubagent) ([]deepResearchFinding, []deepResearchSubagentRun, []string) {
	var waitGroup sync.WaitGroup
	var mu sync.Mutex
	findings := make([]deepResearchFinding, 0, 16)
	runs := make([]deepResearchSubagentRun, 0, len(subagents))
	warnings := make([]string, 0, 8)

	for _, subagent := range subagents {
		current := subagent
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			fmt.Printf("[research][%s] starting\n", current.Name)
			resultFindings, run, runWarnings := current.Run(ctx)
			fmt.Printf("[research][%s] %s\n", current.Name, run.Summary)

			mu.Lock()
			findings = append(findings, resultFindings...)
			runs = append(runs, run)
			warnings = append(warnings, runWarnings...)
			mu.Unlock()
		}()
	}

	waitGroup.Wait()
	return findings, runs, uniqueNonEmptyStrings(warnings)
}

func executeDeepResearchPatchSubagentBatch(ctx context.Context, subagents []deepResearchPatchSubagent) ([]deepResearchFindingPatch, []deepResearchSubagentRun, []string) {
	var waitGroup sync.WaitGroup
	var mu sync.Mutex
	patches := make([]deepResearchFindingPatch, 0, len(subagents)*2)
	runs := make([]deepResearchSubagentRun, 0, len(subagents))
	warnings := make([]string, 0, len(subagents))

	for _, subagent := range subagents {
		current := subagent
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			fmt.Printf("[research][%s] starting\n", current.Name)
			resultPatches, run, runWarnings := current.Run(ctx)
			fmt.Printf("[research][%s] %s\n", current.Name, run.Summary)

			mu.Lock()
			patches = append(patches, resultPatches...)
			runs = append(runs, run)
			warnings = append(warnings, runWarnings...)
			mu.Unlock()
		}()
	}

	waitGroup.Wait()
	return patches, runs, uniqueNonEmptyStrings(warnings)
}

func buildDeepResearchProviderPatchSubagents(question string, plan deepResearchQuestionPlan, findings []deepResearchFinding, estate deepResearchEstateSnapshot, options deepResearchRunOptions) []deepResearchPatchSubagent {
	providerSet := make(map[string]struct{})
	for _, resource := range estate.Resources {
		providerSet[inferDeepResearchProvider(resource)] = struct{}{}
		if isDeepResearchKubernetesType(resource.Type) {
			providerSet["k8s"] = struct{}{}
		}
	}
	prioritized := append([]deepResearchFinding(nil), findings...)
	prioritized = sortAndCapDeepResearchFindings(prioritized)
	subagents := make([]deepResearchPatchSubagent, 0, 9)

	appendProvider := func(provider string, hasAccess bool) {
		if !shouldRunDeepResearchProvider(providerSet, provider, hasAccess) {
			return
		}
		providerName := provider
		targets := selectDeepResearchProviderCandidates(providerName, prioritized, 2)
		subagents = append(subagents, deepResearchPatchSubagent{
			Name: fmt.Sprintf("%s-scout", providerName),
			Run: func(ctx context.Context) ([]deepResearchFindingPatch, deepResearchSubagentRun, []string) {
				return runDeepResearchProviderPatchScout(ctx, providerName, question, plan, targets, options)
			},
		})
	}

	appendProvider("aws", hasAWSDomainAccess() || options.Profile != "")
	appendProvider("gcp", strings.TrimSpace(options.GCPProject) != "" || strings.TrimSpace(gcp.ResolveProjectID()) != "")
	appendProvider("azure", strings.TrimSpace(options.AzureSubscriptionID) != "" || strings.TrimSpace(azure.ResolveSubscriptionID()) != "")
	appendProvider("cloudflare", strings.TrimSpace(cloudflare.ResolveAPIToken()) != "")
	appendProvider("digitalocean", strings.TrimSpace(digitalocean.ResolveAPIToken()) != "")
	appendProvider("hetzner", strings.TrimSpace(hetzner.ResolveAPIToken()) != "")
	appendProvider("k8s", canUseDeepResearchKubernetes())
	appendProvider("supabase", hasDeepResearchSupabaseConnection())
	appendProvider("vercel", hasDeepResearchVercelAccess())
	appendProvider("terraform", strings.TrimSpace(options.TerraformWorkspace) != "" || estate.TerraformOK)

	return subagents
}

func selectDeepResearchProviderCandidates(provider string, findings []deepResearchFinding, maxCount int) []deepResearchFinding {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if maxCount <= 0 {
		return nil
	}
	targets := make([]deepResearchFinding, 0, maxCount)
	for _, finding := range findings {
		if provider == "k8s" {
			if !isDeepResearchKubernetesType(finding.ResourceType) {
				continue
			}
			targets = append(targets, finding)
			if len(targets) >= maxCount {
				break
			}
			continue
		}
		if strings.ToLower(strings.TrimSpace(finding.Provider)) != provider {
			continue
		}
		targets = append(targets, finding)
		if len(targets) >= maxCount {
			break
		}
	}
	return targets
}

func runDeepResearchProviderPatchScout(ctx context.Context, provider string, question string, plan deepResearchQuestionPlan, targets []deepResearchFinding, options deepResearchRunOptions) ([]deepResearchFindingPatch, deepResearchSubagentRun, []string) {
	prompt := buildDeepResearchProviderScoutPrompt(provider, question, plan, targets)
	contextResult, err := collectDeepResearchProviderContext(ctx, provider, prompt, options)
	name := fmt.Sprintf("%s-scout", provider)
	if err != nil {
		return nil, deepResearchSubagentRun{Name: name, Status: "warning", Summary: fmt.Sprintf("%s scout failed: %v", deepResearchProviderDisplayName(provider), err)}, nil
	}
	patches := buildDeepResearchProviderFindingPatches(provider, plan, targets, contextResult)
	summary := contextResult.Summary
	if len(targets) > 0 && len(patches) > 0 {
		summary = fmt.Sprintf("%s Enriched %d findings with %s live context.", contextResult.Summary, len(patches), deepResearchProviderDisplayName(provider))
	} else if len(targets) == 0 {
		summary = fmt.Sprintf("%s No current findings were targeted for direct %s evidence patches.", contextResult.Summary, deepResearchProviderDisplayName(provider))
	}
	return patches, deepResearchSubagentRun{Name: name, Status: "ok", Summary: summary, Details: contextResult.Details}, nil
}

func buildLiveProviderSubagents(estate deepResearchEstateSnapshot, options deepResearchRunOptions) []deepResearchSubagent {
	providerSet := make(map[string]struct{})
	for _, resource := range estate.Resources {
		providerSet[inferDeepResearchProvider(resource)] = struct{}{}
	}

	subagents := make([]deepResearchSubagent, 0, 6)
	if shouldRunDeepResearchProvider(providerSet, "aws", hasAWSDomainAccess() || options.Profile != "") {
		subagents = append(subagents, deepResearchSubagent{Name: "aws-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runAWSDeepResearchScout(ctx, options), nil
		}})
		subagents = append(subagents, deepResearchSubagent{Name: "aws-log-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runAWSDeepResearchLogScout(ctx, options), nil
		}})
	}
	if shouldRunDeepResearchProvider(providerSet, "gcp", strings.TrimSpace(options.GCPProject) != "" || strings.TrimSpace(gcp.ResolveProjectID()) != "") {
		subagents = append(subagents, deepResearchSubagent{Name: "gcp-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runGCPDeepResearchScout(ctx, options), nil
		}})
		subagents = append(subagents, deepResearchSubagent{Name: "gcp-log-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runGCPDeepResearchLogScout(ctx, options), nil
		}})
	}
	if shouldRunDeepResearchProvider(providerSet, "azure", strings.TrimSpace(options.AzureSubscriptionID) != "" || strings.TrimSpace(azure.ResolveSubscriptionID()) != "") {
		subagents = append(subagents, deepResearchSubagent{Name: "azure-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runAzureDeepResearchScout(ctx, options), nil
		}})
		subagents = append(subagents, deepResearchSubagent{Name: "azure-log-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runAzureDeepResearchLogScout(ctx, options), nil
		}})
	}
	if shouldRunDeepResearchProvider(providerSet, "cloudflare", strings.TrimSpace(cloudflare.ResolveAPIToken()) != "") {
		subagents = append(subagents, deepResearchSubagent{Name: "cloudflare-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runCloudflareDeepResearchScout(ctx, options), nil
		}})
	}
	if shouldRunDeepResearchProvider(providerSet, "digitalocean", strings.TrimSpace(digitalocean.ResolveAPIToken()) != "") {
		subagents = append(subagents, deepResearchSubagent{Name: "digitalocean-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runDigitalOceanDeepResearchScout(ctx, options), nil
		}})
	}
	if shouldRunDeepResearchProvider(providerSet, "hetzner", strings.TrimSpace(hetzner.ResolveAPIToken()) != "") {
		subagents = append(subagents, deepResearchSubagent{Name: "hetzner-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runHetznerDeepResearchScout(ctx, options), nil
		}})
	}
	if strings.TrimSpace(options.TerraformWorkspace) != "" || estate.TerraformOK {
		subagents = append(subagents, deepResearchSubagent{Name: "terraform-scout", Run: func(ctx context.Context) ([]deepResearchFinding, deepResearchSubagentRun, []string) {
			return nil, runTerraformDeepResearchScout(ctx, options), nil
		}})
	}
	return subagents
}

func shouldRunDeepResearchProvider(providerSet map[string]struct{}, provider string, hasAccess bool) bool {
	if hasAccess {
		return true
	}
	_, ok := providerSet[provider]
	return ok
}

func runAWSDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	profile := resolveAWSProfile(options.Profile)
	region := resolveAWSRegion(ctx, profile)
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := aws.NewClientWithProfileAndDebug(timeoutCtx, profile, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "aws-scout", Status: "warning", Summary: fmt.Sprintf("AWS scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "cost anomalies ec2 instances lambda functions rds databases s3 buckets ecs containers iam roles cloudwatch alarms logs errors misconfigurations unused stale resources infrastructure hotspots")
	if err != nil {
		return deepResearchSubagentRun{Name: "aws-scout", Status: "warning", Summary: fmt.Sprintf("AWS scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "aws-scout", Status: "ok", Summary: fmt.Sprintf("Collected AWS live context for profile %s in %s.", profile, region), Details: summarizeDeepResearchLines(info, 4)}
}

func runAWSDeepResearchLogScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	profile := resolveAWSProfile(options.Profile)
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := aws.NewClientWithProfileAndDebug(timeoutCtx, profile, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "aws-log-scout", Status: "warning", Summary: fmt.Sprintf("AWS log scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "cloudwatch logs recent error logs log stream last 10 error logs last 10 api logs last 10 lambda logs alarms alerts recent incidents")
	if err != nil {
		return deepResearchSubagentRun{Name: "aws-log-scout", Status: "warning", Summary: fmt.Sprintf("AWS log scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "aws-log-scout", Status: "ok", Summary: "Reviewed AWS log groups, recent errors, and alarm context.", Details: summarizeDeepResearchLines(info, 4)}
}

func runGCPDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	projectID := strings.TrimSpace(options.GCPProject)
	if projectID == "" {
		projectID = strings.TrimSpace(gcp.ResolveProjectID())
	}
	if projectID == "" {
		return deepResearchSubagentRun{Name: "gcp-scout", Status: "warning", Summary: "GCP scout skipped: no project ID is configured."}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := gcp.NewClient(projectID, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "gcp-scout", Status: "warning", Summary: fmt.Sprintf("GCP scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "cost bottlenecks compute instances cloud run jobs gke cloud sql load balancer pubsub queues cloud functions cloud storage service accounts secret manager dns scheduler eventarc artifact registry firestore bigquery spanner bigtable memorystore kms cloud build deploy monitoring logging misconfigurations unused stale resources infrastructure hotspots")
	if err != nil {
		return deepResearchSubagentRun{Name: "gcp-scout", Status: "warning", Summary: fmt.Sprintf("GCP scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "gcp-scout", Status: "ok", Summary: fmt.Sprintf("Collected GCP live context for project %s.", projectID), Details: summarizeDeepResearchLines(info, 4)}
}

func runGCPDeepResearchLogScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	projectID := strings.TrimSpace(options.GCPProject)
	if projectID == "" {
		projectID = strings.TrimSpace(gcp.ResolveProjectID())
	}
	if projectID == "" {
		return deepResearchSubagentRun{Name: "gcp-log-scout", Status: "warning", Summary: "GCP log scout skipped: no project ID is configured."}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := gcp.NewClient(projectID, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "gcp-log-scout", Status: "warning", Summary: fmt.Sprintf("GCP log scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "recent error logs cloud logging errors incidents monitoring alerts cloud run logs gke logs cloud functions logs")
	if err != nil {
		return deepResearchSubagentRun{Name: "gcp-log-scout", Status: "warning", Summary: fmt.Sprintf("GCP log scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "gcp-log-scout", Status: "ok", Summary: fmt.Sprintf("Reviewed GCP logging and alert context for project %s.", projectID), Details: summarizeDeepResearchLines(info, 4)}
}

func runAzureDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	subscriptionID := strings.TrimSpace(options.AzureSubscriptionID)
	if subscriptionID == "" {
		subscriptionID = strings.TrimSpace(azure.ResolveSubscriptionID())
	}
	client := azure.NewClientWithOptionalSubscription(subscriptionID, options.Debug)
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	info, err := client.GetRelevantContext(timeoutCtx, "cost bottlenecks resource groups virtual machines aks app service functions storage key vault cosmos azure sql postgres mysql redis load balancer azure devops inventory logs alerts misconfigurations unused stale resources infrastructure hotspots")
	if err != nil {
		return deepResearchSubagentRun{Name: "azure-scout", Status: "warning", Summary: fmt.Sprintf("Azure scout failed: %v", err)}
	}
	label := subscriptionID
	if label == "" {
		label = "default subscription"
	}
	return deepResearchSubagentRun{Name: "azure-scout", Status: "ok", Summary: fmt.Sprintf("Collected Azure live context for %s.", label), Details: summarizeDeepResearchLines(info, 4)}
}

func runAzureDeepResearchLogScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	subscriptionID := strings.TrimSpace(options.AzureSubscriptionID)
	if subscriptionID == "" {
		subscriptionID = strings.TrimSpace(azure.ResolveSubscriptionID())
	}
	client := azure.NewClientWithOptionalSubscription(subscriptionID, options.Debug)
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	info, err := client.GetRelevantContext(timeoutCtx, "activity logs recent incidents monitor alerts errors")
	if err != nil {
		return deepResearchSubagentRun{Name: "azure-log-scout", Status: "warning", Summary: fmt.Sprintf("Azure log scout failed: %v", err)}
	}
	label := subscriptionID
	if label == "" {
		label = "default subscription"
	}
	return deepResearchSubagentRun{Name: "azure-log-scout", Status: "ok", Summary: fmt.Sprintf("Reviewed Azure activity logs and alert context for %s.", label), Details: summarizeDeepResearchLines(info, 4)}
}

func runCloudflareDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	apiToken := strings.TrimSpace(cloudflare.ResolveAPIToken())
	if apiToken == "" {
		return deepResearchSubagentRun{Name: "cloudflare-scout", Status: "warning", Summary: "Cloudflare scout skipped: no API token is configured."}
	}
	accountID := strings.TrimSpace(cloudflare.ResolveAccountID())
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := cloudflare.NewClient(accountID, apiToken, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "cloudflare-scout", Status: "warning", Summary: fmt.Sprintf("Cloudflare scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "cost bottlenecks workers kv d1 r2 pages cache edge traffic hotspots")
	if err != nil {
		return deepResearchSubagentRun{Name: "cloudflare-scout", Status: "warning", Summary: fmt.Sprintf("Cloudflare scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "cloudflare-scout", Status: "ok", Summary: "Collected Cloudflare live context.", Details: summarizeDeepResearchLines(info, 4)}
}

func runDigitalOceanDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	apiToken := strings.TrimSpace(digitalocean.ResolveAPIToken())
	if apiToken == "" {
		return deepResearchSubagentRun{Name: "digitalocean-scout", Status: "warning", Summary: "DigitalOcean scout skipped: no API token is configured."}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := digitalocean.NewClient(apiToken, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "digitalocean-scout", Status: "warning", Summary: fmt.Sprintf("DigitalOcean scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "cost bottlenecks droplets kubernetes doks databases spaces apps load balancers volumes vpcs domains firewalls hotspots")
	if err != nil {
		return deepResearchSubagentRun{Name: "digitalocean-scout", Status: "warning", Summary: fmt.Sprintf("DigitalOcean scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "digitalocean-scout", Status: "ok", Summary: "Collected DigitalOcean live context.", Details: summarizeDeepResearchLines(info, 4)}
}

func runHetznerDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	apiToken, err := resolveHetznerToken(timeoutCtx, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "hetzner-scout", Status: "warning", Summary: fmt.Sprintf("Hetzner scout unavailable: %v", err)}
	}
	client, err := hetzner.NewClient(apiToken, options.Debug)
	if err != nil {
		return deepResearchSubagentRun{Name: "hetzner-scout", Status: "warning", Summary: fmt.Sprintf("Hetzner scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "cost bottlenecks servers load balancers volumes networks firewalls floating ips primary ips ssh keys images certificates kubernetes hotspots")
	if err != nil {
		return deepResearchSubagentRun{Name: "hetzner-scout", Status: "warning", Summary: fmt.Sprintf("Hetzner scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "hetzner-scout", Status: "ok", Summary: "Collected Hetzner live context.", Details: summarizeDeepResearchLines(info, 4)}
}

func runTerraformDeepResearchScout(ctx context.Context, options deepResearchRunOptions) deepResearchSubagentRun {
	workspace := strings.TrimSpace(options.TerraformWorkspace)
	if workspace == "" {
		workspace = strings.TrimSpace(viper.GetString("terraform.workspace"))
	}
	if workspace == "" {
		return deepResearchSubagentRun{Name: "terraform-scout", Status: "warning", Summary: "Terraform scout skipped: no workspace is configured."}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := tfclient.NewClient(workspace)
	if err != nil {
		return deepResearchSubagentRun{Name: "terraform-scout", Status: "warning", Summary: fmt.Sprintf("Terraform scout unavailable: %v", err)}
	}
	info, err := client.GetRelevantContext(timeoutCtx, "terraform state summary plan changes diff outputs resources infrastructure cost hotspots critical resources drift")
	if err != nil {
		return deepResearchSubagentRun{Name: "terraform-scout", Status: "warning", Summary: fmt.Sprintf("Terraform scout failed: %v", err)}
	}
	return deepResearchSubagentRun{Name: "terraform-scout", Status: "ok", Summary: fmt.Sprintf("Collected Terraform context for workspace %s.", workspace), Details: summarizeDeepResearchLines(info, 4)}
}

func executeDeepResearchDrilldownBatch(ctx context.Context, drilldowns []deepResearchFindingDrilldown) ([]deepResearchFindingPatch, []deepResearchSubagentRun, []string) {
	var waitGroup sync.WaitGroup
	var mu sync.Mutex
	patches := make([]deepResearchFindingPatch, 0, len(drilldowns))
	runs := make([]deepResearchSubagentRun, 0, len(drilldowns))
	warnings := make([]string, 0, len(drilldowns))

	for _, drilldown := range drilldowns {
		current := drilldown
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			fmt.Printf("[research][%s] starting\n", current.Name)
			patch, run, runWarnings := current.Run(ctx)
			fmt.Printf("[research][%s] %s\n", current.Name, run.Summary)

			mu.Lock()
			if strings.TrimSpace(patch.FindingID) != "" {
				patches = append(patches, patch)
			}
			runs = append(runs, run)
			warnings = append(warnings, runWarnings...)
			mu.Unlock()
		}()
	}

	waitGroup.Wait()
	return patches, runs, uniqueNonEmptyStrings(warnings)
}

func buildDeepResearchDrilldownSubagents(question string, plan deepResearchQuestionPlan, findings []deepResearchFinding, options deepResearchRunOptions) []deepResearchFindingDrilldown {
	if len(findings) == 0 || plan.DrilldownCount <= 0 {
		return nil
	}

	candidates := append([]deepResearchFinding(nil), findings...)
	sort.SliceStable(candidates, func(i, j int) bool {
		leftRank := deepResearchFindingPriorityRank(candidates[i], plan.ProviderPriorities)
		rightRank := deepResearchFindingPriorityRank(candidates[j], plan.ProviderPriorities)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftSeverity := deepResearchSeverityRank(candidates[i].Severity)
		rightSeverity := deepResearchSeverityRank(candidates[j].Severity)
		if leftSeverity != rightSeverity {
			return leftSeverity > rightSeverity
		}
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Title < candidates[j].Title
	})

	drilldowns := make([]deepResearchFindingDrilldown, 0, plan.DrilldownCount)
	seen := make(map[string]struct{}, plan.DrilldownCount)
	for _, finding := range candidates {
		if len(drilldowns) >= plan.DrilldownCount {
			break
		}
		provider := deepResearchFindingLiveProvider(finding, options)
		if provider == "" || !canRunDeepResearchProviderDrilldown(provider, options) {
			continue
		}
		if strings.TrimSpace(finding.ID) == "" {
			continue
		}
		if _, exists := seen[finding.ID]; exists {
			continue
		}
		seen[finding.ID] = struct{}{}
		currentFinding := finding
		label := deepResearchCompactLabel(currentFinding.ResourceName, currentFinding.Title)
		drilldowns = append(drilldowns, deepResearchFindingDrilldown{
			Name: fmt.Sprintf("%s-drilldown-%s", provider, label),
			Run: func(ctx context.Context) (deepResearchFindingPatch, deepResearchSubagentRun, []string) {
				return runDeepResearchFindingDrilldown(ctx, provider, question, plan, currentFinding, options)
			},
		})
	}

	return drilldowns
}

func canRunDeepResearchProviderDrilldown(provider string, options deepResearchRunOptions) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws":
		return hasAWSDomainAccess() || strings.TrimSpace(options.Profile) != ""
	case "gcp":
		return strings.TrimSpace(options.GCPProject) != "" || strings.TrimSpace(gcp.ResolveProjectID()) != ""
	case "azure":
		return strings.TrimSpace(options.AzureSubscriptionID) != "" || strings.TrimSpace(azure.ResolveSubscriptionID()) != ""
	case "cloudflare":
		return strings.TrimSpace(cloudflare.ResolveAPIToken()) != ""
	case "digitalocean":
		return strings.TrimSpace(digitalocean.ResolveAPIToken()) != ""
	case "hetzner":
		return strings.TrimSpace(hetzner.ResolveAPIToken()) != ""
	case "k8s":
		return canUseDeepResearchKubernetes()
	case "supabase":
		return hasDeepResearchSupabaseConnection()
	case "vercel":
		return hasDeepResearchVercelAccess()
	case "terraform":
		return strings.TrimSpace(options.TerraformWorkspace) != "" || strings.TrimSpace(viper.GetString("terraform.workspace")) != ""
	default:
		return false
	}
}

func runDeepResearchFindingDrilldown(ctx context.Context, provider string, question string, plan deepResearchQuestionPlan, finding deepResearchFinding, options deepResearchRunOptions) (deepResearchFindingPatch, deepResearchSubagentRun, []string) {
	patch := deepResearchFindingPatch{FindingID: finding.ID}
	label := deepResearchResourceLabelFromFinding(finding)
	name := fmt.Sprintf("%s-drilldown-%s", provider, deepResearchCompactLabel(finding.ResourceName, finding.Title))
	prompt := buildDeepResearchProviderFindingPrompt(provider, question, plan, finding)
	contextResult, err := collectDeepResearchProviderContext(ctx, provider, prompt, options)
	if err != nil {
		return patch, deepResearchSubagentRun{Name: name, Status: "warning", Summary: fmt.Sprintf("%s drilldown failed for %s: %v", deepResearchProviderDisplayName(provider), label, err)}, nil
	}
	evidenceDetails := deepResearchBuildProviderEvidenceDetails(provider, finding, plan, contextResult, "provider-drilldown", 6)
	if len(evidenceDetails) == 0 {
		return patch, deepResearchSubagentRun{Name: name, Status: "warning", Summary: fmt.Sprintf("%s drilldown found no additional live evidence for %s.", deepResearchProviderDisplayName(provider), label)}, nil
	}
	patchEvidence := append([]deepResearchEvidenceDetail{{
		Detail:   fmt.Sprintf("%s live drilldown pulled %s for %s (%s).", deepResearchProviderDisplayName(provider), deepResearchProviderEvidenceFocusLabel(provider, finding, evidenceDetails), label, deepResearchNonEmpty(finding.ResourceType, "resource")),
		Source:   "provider-drilldown",
		Provider: provider,
	}}, evidenceDetails...)
	patch.Evidence = deepResearchEvidenceStrings(patchEvidence)
	patch.EvidenceDetails = patchEvidence
	patch.Questions = buildDeepResearchProviderDrilldownQuestions(provider, finding, plan)
	patch.ScoreDelta = 12
	return patch, deepResearchSubagentRun{Name: name, Status: "ok", Summary: fmt.Sprintf("Drilled into %s with %s live context.", label, deepResearchProviderDisplayName(provider)), Details: deepResearchEvidenceStrings(evidenceDetails)}, nil
}

func canUseDeepResearchKubernetes() bool {
	return k8s.IsKubectlAvailable()
}

func hasDeepResearchSupabaseConnection() bool {
	connections, _, err := dbcontext.ListConnections()
	if err != nil {
		return false
	}
	for _, connection := range connections {
		if strings.EqualFold(strings.TrimSpace(connection.Vendor), "supabase") {
			return true
		}
	}
	return false
}

func hasDeepResearchVercelAccess() bool {
	return strings.TrimSpace(vercel.ResolveAPIToken()) != ""
}

func resolveDeepResearchSupabaseConnectionName() string {
	connections, defaultName, err := dbcontext.ListConnections()
	if err != nil || len(connections) == 0 {
		return ""
	}
	for _, connection := range connections {
		if connection.Name == defaultName && strings.EqualFold(strings.TrimSpace(connection.Vendor), "supabase") {
			return connection.Name
		}
	}
	for _, connection := range connections {
		if strings.EqualFold(strings.TrimSpace(connection.Vendor), "supabase") {
			return connection.Name
		}
	}
	return ""
}

func collectDeepResearchKubernetesContext(ctx context.Context, debug bool) (deepResearchProviderContext, error) {
	if !k8s.IsKubectlAvailable() {
		return deepResearchProviderContext{}, fmt.Errorf("kubectl is not installed")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()

	client := k8s.NewClient("", "", debug)
	if err := client.CheckConnection(timeoutCtx); err != nil {
		return deepResearchProviderContext{}, err
	}

	sections := make([]string, 0, 5)
	details := make([]string, 0, 4)
	clusterLines := make([]string, 0, 3)

	currentContext, err := client.GetCurrentContext(timeoutCtx)
	if err == nil && strings.TrimSpace(currentContext) != "" {
		clusterLines = append(clusterLines, fmt.Sprintf("Kubectl context: %s", strings.TrimSpace(currentContext)))
		details = append(details, fmt.Sprintf("Kubectl context: %s", strings.TrimSpace(currentContext)))
	}
	if clusterInfo, err := client.GetClusterInfo(timeoutCtx); err == nil {
		clusterLines = append(clusterLines, summarizeDeepResearchLines(clusterInfo, 2)...)
	}
	if len(clusterLines) > 0 {
		sections = append(sections, "Cluster:\n"+strings.Join(uniqueNonEmptyStrings(clusterLines), "\n"))
	}

	if topPods, err := client.Run(timeoutCtx, "top", "pods", "-A"); err == nil {
		topLines := summarizeDeepResearchLines(topPods, 4)
		if len(topLines) > 0 {
			sections = append(sections, "Top Pods:\n"+strings.Join(topLines, "\n"))
			details = append(details, "Reviewed current pod CPU and memory hotspots.")
		}
	}

	if events, err := client.Run(timeoutCtx, "get", "events", "-A", "--sort-by=.metadata.creationTimestamp"); err == nil {
		eventLines := summarizeDeepResearchTailLines(events, 4)
		if len(eventLines) > 0 {
			sections = append(sections, "Recent Events:\n"+strings.Join(eventLines, "\n"))
			details = append(details, "Captured the latest multi-namespace cluster events.")
		}
	}

	if podsJSON, err := client.RunJSON(timeoutCtx, "get", "pods", "-A"); err == nil {
		logTargets := selectDeepResearchKubernetesLogTargets(podsJSON, 2)
		if len(logTargets) > 0 {
			details = append(details, fmt.Sprintf("Sampled latest logs from %d unhealthy or restarting pods.", len(logTargets)))
		}
		for _, target := range logTargets {
			logCtx, cancelLog := context.WithTimeout(timeoutCtx, 4*time.Second)
			logs, logErr := client.Logs(logCtx, target.Name, target.Namespace, k8s.LogOptions{TailLines: 40, Since: "90m"})
			cancelLog()
			if logErr != nil {
				continue
			}
			logLines := summarizeDeepResearchTailLines(logs, 3)
			if len(logLines) == 0 {
				continue
			}
			logSectionLines := []string{
				fmt.Sprintf("Phase=%s Restarts=%d Reason=%s", deepResearchNonEmpty(target.Phase, "unknown"), target.RestartCount, deepResearchNonEmpty(target.Reason, "n/a")),
			}
			logSectionLines = append(logSectionLines, logLines...)
			sections = append(sections, fmt.Sprintf("Recent Logs %s/%s:\n%s", target.Namespace, target.Name, strings.Join(uniqueNonEmptyStrings(logSectionLines), "\n")))
		}
	}

	blob := strings.Join(sections, "\n\n")
	if strings.TrimSpace(blob) == "" {
		return deepResearchProviderContext{}, fmt.Errorf("no Kubernetes context could be collected from the current kubectl cluster")
	}

	summary := "Collected Kubernetes live context from the current kubectl cluster."
	if strings.TrimSpace(currentContext) != "" {
		summary = fmt.Sprintf("Collected Kubernetes live context from kubectl context %s.", strings.TrimSpace(currentContext))
	}

	return deepResearchProviderContext{
		Provider: "k8s",
		Summary:  summary,
		Details:  deepResearchLimitStrings(uniqueNonEmptyStrings(details), 4),
		Blob:     blob,
	}, nil
}

func collectDeepResearchSupabaseContext(ctx context.Context, prompt string) (deepResearchProviderContext, error) {
	connectionName := resolveDeepResearchSupabaseConnectionName()
	if connectionName == "" {
		return deepResearchProviderContext{}, fmt.Errorf("no Supabase database connection is configured")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	info, err := dbcontext.BuildRelevantContext(timeoutCtx, "supabase "+strings.TrimSpace(prompt), connectionName)
	if err != nil {
		return deepResearchProviderContext{}, err
	}

	return deepResearchProviderContext{
		Provider: "supabase",
		Summary:  fmt.Sprintf("Collected Supabase database context for connection %s.", connectionName),
		Details:  summarizeDeepResearchLines(info, 4),
		Blob:     info,
	}, nil
}

func selectDeepResearchKubernetesLogTargets(podsJSON []byte, maxCount int) []deepResearchKubernetesLogTarget {
	if maxCount <= 0 || len(podsJSON) == 0 {
		return nil
	}

	var payload struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
					State        struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
						Terminated *struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsJSON, &payload); err != nil {
		return nil
	}

	targets := make([]deepResearchKubernetesLogTarget, 0, maxCount)
	for _, item := range payload.Items {
		restartCount := 0
		notReadyCount := 0
		reason := ""
		for _, status := range item.Status.ContainerStatuses {
			restartCount += status.RestartCount
			if !status.Ready {
				notReadyCount++
			}
			if reason == "" && status.State.Waiting != nil {
				reason = strings.TrimSpace(status.State.Waiting.Reason)
			}
			if reason == "" && status.State.Terminated != nil {
				reason = strings.TrimSpace(status.State.Terminated.Reason)
			}
		}

		score := restartCount * 10
		phase := strings.TrimSpace(item.Status.Phase)
		if !strings.EqualFold(phase, "Running") {
			score += 30
		}
		if notReadyCount > 0 {
			score += 18
		}
		if deepResearchContainsAny(strings.ToLower(reason), "crashloop", "backoff", "oom", "error") {
			score += 20
		}
		if score <= 0 {
			continue
		}

		targets = append(targets, deepResearchKubernetesLogTarget{
			Name:         strings.TrimSpace(item.Metadata.Name),
			Namespace:    strings.TrimSpace(item.Metadata.Namespace),
			Phase:        phase,
			Reason:       reason,
			RestartCount: restartCount,
			Score:        score,
		})
	}

	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Score != targets[j].Score {
			return targets[i].Score > targets[j].Score
		}
		if targets[i].RestartCount != targets[j].RestartCount {
			return targets[i].RestartCount > targets[j].RestartCount
		}
		if targets[i].Namespace != targets[j].Namespace {
			return targets[i].Namespace < targets[j].Namespace
		}
		return targets[i].Name < targets[j].Name
	})

	if len(targets) > maxCount {
		return append([]deepResearchKubernetesLogTarget(nil), targets[:maxCount]...)
	}
	return targets
}

func collectDeepResearchProviderContext(ctx context.Context, provider string, prompt string, options deepResearchRunOptions) (deepResearchProviderContext, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "aws":
		profile := resolveAWSProfile(options.Profile)
		region := resolveAWSRegion(ctx, profile)
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		client, err := aws.NewClientWithProfileAndDebug(timeoutCtx, profile, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: fmt.Sprintf("Collected AWS live context for profile %s in %s.", profile, region), Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "gcp":
		projectID := strings.TrimSpace(options.GCPProject)
		if projectID == "" {
			projectID = strings.TrimSpace(gcp.ResolveProjectID())
		}
		if projectID == "" {
			return deepResearchProviderContext{}, fmt.Errorf("no project ID is configured")
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		client, err := gcp.NewClient(projectID, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: fmt.Sprintf("Collected GCP live context for project %s.", projectID), Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "azure":
		subscriptionID := strings.TrimSpace(options.AzureSubscriptionID)
		if subscriptionID == "" {
			subscriptionID = strings.TrimSpace(azure.ResolveSubscriptionID())
		}
		label := subscriptionID
		if label == "" {
			label = "default subscription"
		}
		client := azure.NewClientWithOptionalSubscription(subscriptionID, options.Debug)
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: fmt.Sprintf("Collected Azure live context for %s.", label), Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "cloudflare":
		apiToken := strings.TrimSpace(cloudflare.ResolveAPIToken())
		if apiToken == "" {
			return deepResearchProviderContext{}, fmt.Errorf("no API token is configured")
		}
		accountID := strings.TrimSpace(cloudflare.ResolveAccountID())
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		client, err := cloudflare.NewClient(accountID, apiToken, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: "Collected Cloudflare live context.", Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "digitalocean":
		apiToken := strings.TrimSpace(digitalocean.ResolveAPIToken())
		if apiToken == "" {
			return deepResearchProviderContext{}, fmt.Errorf("no API token is configured")
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		client, err := digitalocean.NewClient(apiToken, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: "Collected DigitalOcean live context.", Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "hetzner":
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		apiToken, err := resolveHetznerToken(timeoutCtx, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		client, err := hetzner.NewClient(apiToken, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: "Collected Hetzner live context.", Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "k8s":
		return collectDeepResearchKubernetesContext(ctx, options.Debug)
	case "supabase":
		return collectDeepResearchSupabaseContext(ctx, prompt)
	case "vercel":
		apiToken := strings.TrimSpace(vercel.ResolveAPIToken())
		if apiToken == "" {
			return deepResearchProviderContext{}, fmt.Errorf("no API token is configured")
		}
		teamID := strings.TrimSpace(vercel.ResolveTeamID())
		label := "personal account"
		if teamID != "" {
			label = "team " + teamID
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		client, err := vercel.NewClient(apiToken, teamID, options.Debug)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: fmt.Sprintf("Collected Vercel live context for %s.", label), Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	case "terraform":
		workspace := strings.TrimSpace(options.TerraformWorkspace)
		if workspace == "" {
			workspace = strings.TrimSpace(viper.GetString("terraform.workspace"))
		}
		if workspace == "" {
			return deepResearchProviderContext{}, fmt.Errorf("no workspace is configured")
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		client, err := tfclient.NewClient(workspace)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		info, err := client.GetRelevantContext(timeoutCtx, prompt)
		if err != nil {
			return deepResearchProviderContext{}, err
		}
		return deepResearchProviderContext{Provider: provider, Summary: fmt.Sprintf("Collected Terraform context for workspace %s.", workspace), Details: summarizeDeepResearchLines(info, 4), Blob: info}, nil
	default:
		return deepResearchProviderContext{}, fmt.Errorf("unsupported provider %q", provider)
	}
}

func buildDeepResearchProviderFindingPatches(provider string, plan deepResearchQuestionPlan, targets []deepResearchFinding, contextResult deepResearchProviderContext) []deepResearchFindingPatch {
	if len(targets) == 0 {
		return nil
	}
	patches := make([]deepResearchFindingPatch, 0, len(targets))
	providerLabel := deepResearchProviderDisplayName(provider)
	for _, finding := range targets {
		evidenceDetails := deepResearchBuildProviderEvidenceDetails(provider, finding, plan, contextResult, "provider-scout", 5)
		if len(evidenceDetails) == 0 {
			continue
		}
		patchEvidence := append([]deepResearchEvidenceDetail{{
			Detail:   fmt.Sprintf("%s live scout reviewed %s for %s.", providerLabel, deepResearchProviderEvidenceFocusLabel(provider, finding, evidenceDetails), deepResearchResourceLabelFromFinding(finding)),
			Source:   "provider-scout",
			Provider: provider,
		}}, evidenceDetails...)
		patch := deepResearchFindingPatch{
			FindingID:       finding.ID,
			Evidence:        deepResearchEvidenceStrings(patchEvidence),
			EvidenceDetails: patchEvidence,
			Questions:       buildDeepResearchProviderScoutQuestions(provider, finding, plan),
			ScoreDelta:      8,
		}
		if strings.EqualFold(finding.Category, "resilience") && plan.WantsLogs {
			patch.ScoreDelta += 4
		}
		patches = append(patches, patch)
	}
	return patches
}

func buildDeepResearchProviderScoutPrompt(provider string, question string, plan deepResearchQuestionPlan, targets []deepResearchFinding) string {
	parts := append([]string{strings.TrimSpace(question)}, deepResearchProviderPromptTerms(provider, plan, targets)...)
	for _, finding := range targets {
		parts = append(parts,
			deepResearchProviderServiceLabel(provider, finding),
			strings.TrimSpace(finding.Title),
			strings.TrimSpace(finding.Summary),
			strings.TrimSpace(finding.ResourceName),
			strings.TrimSpace(finding.ResourceID),
			strings.TrimSpace(finding.ResourceType),
			strings.TrimSpace(finding.Region),
		)
	}
	return strings.Join(uniqueNonEmptyStrings(parts), " ")
}

func buildDeepResearchProviderFindingPrompt(provider string, question string, plan deepResearchQuestionPlan, finding deepResearchFinding) string {
	parts := []string{
		strings.TrimSpace(question),
		fmt.Sprintf("Investigate %s %s in %s.", deepResearchProviderServiceLabel(provider, finding), deepResearchResourceLabelFromFinding(finding), deepResearchProviderDisplayName(provider)),
		strings.TrimSpace(finding.Title),
		strings.TrimSpace(finding.Summary),
	}
	parts = append(parts, deepResearchProviderPromptTerms(provider, plan, []deepResearchFinding{finding})...)
	return strings.Join(uniqueNonEmptyStrings(parts), " ")
}

func deepResearchProviderPromptTerms(provider string, plan deepResearchQuestionPlan, targets []deepResearchFinding) []string {
	terms := []string{provider}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws":
		terms = append(terms, "ec2", "lambda", "rds", "s3", "ecs", "iam", "cloudwatch", "logs", "alarms", "vpc", "security group")
	case "gcp":
		terms = append(terms, "compute", "cloud run", "gke", "cloud sql", "pubsub", "storage", "service account", "logging", "alerts", "firewall")
	case "azure":
		terms = append(terms, "vm", "aks", "webapp", "functionapp", "storage", "key vault", "cosmos", "sql", "postgres", "redis", "activity log", "alerts")
	case "k8s":
		terms = append(terms, "kubernetes", "pods", "deployments", "services", "events", "logs", "restarts", "crashloop", "metrics", "kubectl")
	case "cloudflare":
		terms = append(terms, "zones", "dns", "workers", "kv", "d1", "r2", "pages", "cache")
	case "digitalocean":
		terms = append(terms, "droplet", "doks", "database", "spaces", "apps", "load balancer", "volume", "vpc", "firewall")
	case "hetzner":
		terms = append(terms, "server", "load balancer", "volume", "network", "firewall", "floating ip", "primary ip")
	case "supabase":
		terms = append(terms, "postgres", "database", "schemas", "tables", "auth", "storage", "realtime", "connections")
	case "vercel":
		terms = append(terms, "projects", "deployments", "domains", "preview", "production", "build", "edge", "functions", "analytics", "usage", "vercel.app")
	case "terraform":
		terms = append(terms, "terraform", "state", "outputs", "plan", "diff", "resource", "drift")
	}
	if plan.WantsCost {
		terms = append(terms, "cost", "utilization", "savings")
	}
	if plan.WantsLogs {
		terms = append(terms, "logs", "errors", "alerts", "incident")
	}
	if plan.WantsMisconfig {
		terms = append(terms, "public", "exposure", "encryption", "backup", "permission")
	}
	if plan.WantsHygiene {
		terms = append(terms, "unused", "stale", "cleanup", "orphan")
	}
	if plan.WantsTopology {
		terms = append(terms, "dependency", "bottleneck", "hotspot", "scaling")
	}
	for _, finding := range targets {
		terms = append(terms, deepResearchProviderFocusTerms(provider, finding, plan)...)
	}
	return uniqueNonEmptyStrings(terms)
}

func buildDeepResearchProviderScoutQuestions(provider string, finding deepResearchFinding, plan deepResearchQuestionPlan) []string {
	label := deepResearchResourceLabelFromFinding(finding)
	providerLabel := deepResearchProviderDisplayName(provider)
	serviceLabel := strings.ToLower(deepResearchProviderServiceLabel(provider, finding))
	questions := make([]string, 0, 4)
	if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
		questions = append(questions, fmt.Sprintf("What recent %s signals matter most for %s on this %s?", providerLabel, label, serviceLabel))
	}
	if plan.WantsCost || strings.EqualFold(finding.Category, "cost") {
		questions = append(questions, fmt.Sprintf("Which %s usage pattern is driving cost on %s?", serviceLabel, label))
	}
	if plan.WantsMisconfig || strings.EqualFold(finding.Category, "misconfiguration") {
		questions = append(questions, fmt.Sprintf("Which %s configuration should I harden first on %s in %s?", serviceLabel, label, providerLabel))
	}
	if plan.WantsHygiene || strings.EqualFold(finding.Category, "hygiene") {
		questions = append(questions, fmt.Sprintf("What proves %s is still active as a %s in %s?", label, serviceLabel, providerLabel))
	}
	if len(questions) == 0 {
		questions = append(questions, fmt.Sprintf("What should I inspect next for %s as a %s in %s?", label, serviceLabel, providerLabel))
	}
	return uniqueNonEmptyStrings(questions)
}

func buildDeepResearchProviderDrilldownQuestions(provider string, finding deepResearchFinding, plan deepResearchQuestionPlan) []string {
	label := deepResearchResourceLabelFromFinding(finding)
	providerLabel := deepResearchProviderDisplayName(provider)
	serviceLabel := strings.ToLower(deepResearchProviderServiceLabel(provider, finding))
	questions := make([]string, 0, 4)
	if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
		questions = append(questions, fmt.Sprintf("What do the recent %s logs, events, or alerts say about %s on this %s?", providerLabel, label, serviceLabel))
	}
	if plan.WantsCost || strings.EqualFold(finding.Category, "cost") {
		questions = append(questions, fmt.Sprintf("Which %s setting or behavior is driving spend on %s?", serviceLabel, label))
	}
	if plan.WantsMisconfig || strings.EqualFold(finding.Category, "misconfiguration") {
		questions = append(questions, fmt.Sprintf("Which %s configuration change should I make first on %s in %s?", serviceLabel, label, providerLabel))
	}
	if plan.WantsTopology || strings.EqualFold(finding.Category, "bottleneck") {
		questions = append(questions, fmt.Sprintf("What is the main dependency or throughput choke point around %s in this %s?", label, serviceLabel))
	}
	if plan.WantsHygiene || strings.EqualFold(finding.Category, "hygiene") {
		questions = append(questions, fmt.Sprintf("What evidence shows whether %s is still needed as a %s in %s?", label, serviceLabel, providerLabel))
	}
	if len(questions) == 0 {
		questions = append(questions, fmt.Sprintf("What should I inspect next for %s as a %s in %s?", label, serviceLabel, providerLabel))
	}
	return uniqueNonEmptyStrings(questions)
}

func deepResearchProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws":
		return "AWS"
	case "gcp":
		return "GCP"
	case "azure":
		return "Azure"
	case "k8s":
		return "Kubernetes"
	case "cloudflare":
		return "Cloudflare"
	case "digitalocean":
		return "DigitalOcean"
	case "hetzner":
		return "Hetzner"
	case "supabase":
		return "Supabase"
	case "vercel":
		return "Vercel"
	case "terraform":
		return "Terraform"
	default:
		return strings.Title(strings.ToLower(strings.TrimSpace(provider)))
	}
}

func deepResearchLimitStrings(values []string, maxCount int) []string {
	if maxCount <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) <= maxCount {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[:maxCount]...)
}

func runAWSDeepResearchFindingDrilldown(ctx context.Context, question string, plan deepResearchQuestionPlan, finding deepResearchFinding, options deepResearchRunOptions) (deepResearchFindingPatch, deepResearchSubagentRun, []string) {
	patch := deepResearchFindingPatch{FindingID: finding.ID}
	profile := resolveAWSProfile(options.Profile)
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := aws.NewClientWithProfileAndDebug(timeoutCtx, profile, options.Debug)
	if err != nil {
		return patch, deepResearchSubagentRun{Name: fmt.Sprintf("aws-drilldown-%s", deepResearchCompactLabel(finding.ResourceName, finding.Title)), Status: "warning", Summary: fmt.Sprintf("AWS drilldown unavailable for %s: %v", deepResearchResourceLabelFromFinding(finding), err)}, nil
	}

	prompt := buildAWSDeepResearchFindingPrompt(question, plan, finding)
	info, err := client.GetRelevantContext(timeoutCtx, prompt)
	if err != nil {
		return patch, deepResearchSubagentRun{Name: fmt.Sprintf("aws-drilldown-%s", deepResearchCompactLabel(finding.ResourceName, finding.Title)), Status: "warning", Summary: fmt.Sprintf("AWS drilldown failed for %s: %v", deepResearchResourceLabelFromFinding(finding), err)}, nil
	}

	evidence := summarizeDeepResearchEvidenceLines(info, 6)
	if len(evidence) == 0 {
		return patch, deepResearchSubagentRun{Name: fmt.Sprintf("aws-drilldown-%s", deepResearchCompactLabel(finding.ResourceName, finding.Title)), Status: "warning", Summary: fmt.Sprintf("AWS drilldown found no additional live evidence for %s.", deepResearchResourceLabelFromFinding(finding))}, nil
	}

	patch.Evidence = append([]string{fmt.Sprintf("AWS live drilldown for %s (%s).", deepResearchResourceLabelFromFinding(finding), deepResearchNonEmpty(finding.ResourceType, "resource"))}, evidence...)
	patch.Questions = buildDeepResearchDrilldownQuestions(finding, plan)
	patch.ScoreDelta = 12
	return patch, deepResearchSubagentRun{
		Name:    fmt.Sprintf("aws-drilldown-%s", deepResearchCompactLabel(finding.ResourceName, finding.Title)),
		Status:  "ok",
		Summary: fmt.Sprintf("Drilled into %s with AWS live context.", deepResearchResourceLabelFromFinding(finding)),
		Details: evidence,
	}, nil
}

func buildAWSDeepResearchFindingPrompt(question string, plan deepResearchQuestionPlan, finding deepResearchFinding) string {
	serviceHint := deepResearchAWSServiceHint(finding.ResourceType)
	focusTerms := make([]string, 0, 8)
	if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
		focusTerms = append(focusTerms, "logs", "error", "alarm", "alert", "cloudwatch")
	}
	if plan.WantsCost || strings.EqualFold(finding.Category, "cost") {
		focusTerms = append(focusTerms, "cost", "utilization")
	}
	if plan.WantsMisconfig || strings.EqualFold(finding.Category, "misconfiguration") {
		focusTerms = append(focusTerms, "public exposure", "backup", "encryption", "iam role")
	}
	if plan.WantsTopology || strings.EqualFold(finding.Category, "bottleneck") {
		focusTerms = append(focusTerms, "dependency", "scaling", "bottleneck")
	}
	if plan.WantsDatabase && isDeepResearchDatabaseType(finding.ResourceType) {
		focusTerms = append(focusTerms, "database")
	}
	if plan.WantsCompute && isDeepResearchComputeType(finding.ResourceType) {
		focusTerms = append(focusTerms, "instance", "function", "container")
	}
	if len(focusTerms) == 0 {
		focusTerms = append(focusTerms, "logs", "error", "alarm", "cost", "utilization")
	}

	resourceLabel := deepResearchResourceLabelFromFinding(finding)
	region := deepResearchNonEmpty(finding.Region, "default-region")
	return fmt.Sprintf("%s %s. Investigate AWS resource %s type %s in %s. Finding: %s. Summary: %s. Original question: %s.", serviceHint, strings.Join(uniqueNonEmptyStrings(focusTerms), " "), resourceLabel, deepResearchNonEmpty(finding.ResourceType, "resource"), region, finding.Title, finding.Summary, strings.TrimSpace(question))
}

func buildCostFindings(resources []deepResearchResource, totalCost float64) []deepResearchFinding {
	findings := make([]deepResearchFinding, 0, 8)
	if len(resources) == 0 {
		return findings
	}

	byCost := append([]deepResearchResource(nil), resources...)
	sort.Slice(byCost, func(i, j int) bool {
		return byCost[i].MonthlyPrice > byCost[j].MonthlyPrice
	})

	maxTopCostFindings := 4
	for _, resource := range byCost {
		if len(findings) >= maxTopCostFindings {
			break
		}
		if resource.MonthlyPrice <= 0 {
			continue
		}
		share := 0.0
		if totalCost > 0 {
			share = (resource.MonthlyPrice / totalCost) * 100
		}
		if share < 8 && resource.MonthlyPrice < 50 {
			continue
		}
		severity := "medium"
		if share >= 25 || resource.MonthlyPrice >= 500 {
			severity = "critical"
		} else if share >= 15 || resource.MonthlyPrice >= 200 {
			severity = "high"
		}
		findings = append(findings, deepResearchFinding{
			ID:           buildDeepResearchFindingID("cost-driver", resource.ID),
			Severity:     severity,
			Category:     "cost",
			Title:        fmt.Sprintf("%s is a top cost driver", deepResearchResourceLabel(resource)),
			Summary:      fmt.Sprintf("This resource accounts for $%.2f/mo and %.1f%% of the observed estate cost, making it a first-pass target for rightsizing or architecture review.", resource.MonthlyPrice, share),
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			MonthlyCost:  resource.MonthlyPrice,
			Risk:         "High recurring spend concentrated in a single asset.",
			Score:        resource.MonthlyPrice,
			Evidence: []string{
				fmt.Sprintf("Observed monthly price: $%.2f", resource.MonthlyPrice),
				fmt.Sprintf("Estimated share of estate cost: %.1f%%", share),
			},
			Questions: buildDeepResearchQuestions("cost-driver", resource),
		})
	}

	for _, resource := range byCost {
		if resource.MonthlyPrice <= 0 || !isDeepResearchIdleState(resource.State) {
			continue
		}
		findings = append(findings, deepResearchFinding{
			ID:                      buildDeepResearchFindingID("idle-cost", resource.ID),
			Severity:                deepResearchSeverityForCost(resource.MonthlyPrice),
			Category:                "cost",
			Title:                   fmt.Sprintf("%s looks idle but still costs money", deepResearchResourceLabel(resource)),
			Summary:                 fmt.Sprintf("The resource state is %q while the observed monthly price is still $%.2f. This is a direct savings candidate if the state reflects real inactivity.", resource.State, resource.MonthlyPrice),
			ResourceID:              resource.ID,
			ResourceName:            resource.Name,
			ResourceType:            resource.Type,
			Provider:                inferDeepResearchProvider(resource),
			Region:                  resource.Region,
			MonthlyCost:             resource.MonthlyPrice,
			EstimatedMonthlySavings: resource.MonthlyPrice,
			Risk:                    "Idle or parked resources silently accumulate spend.",
			Score:                   resource.MonthlyPrice + 25,
			Evidence: []string{
				fmt.Sprintf("State: %s", resource.State),
				fmt.Sprintf("Observed monthly price: $%.2f", resource.MonthlyPrice),
			},
			Questions: buildDeepResearchQuestions("idle-cost", resource),
		})
	}

	for _, resource := range byCost {
		if resource.MonthlyPrice < 75 || len(resource.Tags) > 0 {
			continue
		}
		findings = append(findings, deepResearchFinding{
			ID:           buildDeepResearchFindingID("untagged-cost", resource.ID),
			Severity:     "medium",
			Category:     "cost",
			Title:        fmt.Sprintf("%s is expensive and untagged", deepResearchResourceLabel(resource)),
			Summary:      fmt.Sprintf("The resource carries $%.2f/mo without ownership or environment tags. That slows cost attribution and makes cleanup decisions harder.", resource.MonthlyPrice),
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			MonthlyCost:  resource.MonthlyPrice,
			Risk:         "Missing ownership and lifecycle metadata creates sustained spend drift.",
			Score:        resource.MonthlyPrice,
			Evidence: []string{
				fmt.Sprintf("Observed monthly price: $%.2f", resource.MonthlyPrice),
				"No tags were present in the estate snapshot.",
			},
			Questions: buildDeepResearchQuestions("untagged-cost", resource),
		})
	}

	providerRolls := buildDeepResearchProviderRollup(resources, totalCost)
	if len(providerRolls) > 0 && providerRolls[0].ShareOfCost >= 70 {
		primary := providerRolls[0]
		findings = append(findings, deepResearchFinding{
			ID:          buildDeepResearchFindingID("provider-concentration", primary.Provider),
			Severity:    "medium",
			Category:    "cost",
			Title:       fmt.Sprintf("%s dominates the current spend profile", strings.ToUpper(primary.Provider)),
			Summary:     fmt.Sprintf("%s currently represents %.1f%% of the observed monthly cost. That makes cost optimization heavily dependent on a small set of services and pricing decisions in one provider.", strings.ToUpper(primary.Provider), primary.ShareOfCost),
			Provider:    primary.Provider,
			MonthlyCost: primary.MonthlyCost,
			Risk:        "Provider-heavy spend concentration reduces optimization flexibility.",
			Score:       primary.ShareOfCost,
			Evidence: []string{
				fmt.Sprintf("Provider cost share: %.1f%%", primary.ShareOfCost),
				fmt.Sprintf("Observed provider cost: $%.2f/mo", primary.MonthlyCost),
			},
			Questions: []string{
				fmt.Sprintf("Which %s services are driving most of the spend?", strings.ToUpper(primary.Provider)),
				fmt.Sprintf("Where can I reduce %s cost without changing user-facing behavior?", strings.ToUpper(primary.Provider)),
			},
		})
	}

	return findings
}

func buildTopologyFindings(resources []deepResearchResource) []deepResearchFinding {
	inbound := make(map[string]int)
	resourceByID := make(map[string]deepResearchResource, len(resources))
	for _, resource := range resources {
		resourceByID[resource.ID] = resource
		seenTargets := make(map[string]struct{})
		for _, targetID := range resource.Connections {
			targetID = strings.TrimSpace(targetID)
			if targetID == "" {
				continue
			}
			if _, exists := seenTargets[targetID]; exists {
				continue
			}
			seenTargets[targetID] = struct{}{}
			inbound[targetID]++
		}
		for _, connection := range resource.TypedConnections {
			targetID := strings.TrimSpace(connection.TargetID)
			if targetID == "" {
				continue
			}
			if _, exists := seenTargets[targetID]; exists {
				continue
			}
			seenTargets[targetID] = struct{}{}
			inbound[targetID]++
		}
	}

	findings := make([]deepResearchFinding, 0, 6)
	for resourceID, count := range inbound {
		resource, ok := resourceByID[resourceID]
		if !ok || count < 3 || !isDeepResearchBottleneckType(resource.Type) {
			continue
		}
		severity := "medium"
		if count >= 8 {
			severity = "critical"
		} else if count >= 5 {
			severity = "high"
		}
		findings = append(findings, deepResearchFinding{
			ID:           buildDeepResearchFindingID("topology-bottleneck", resource.ID),
			Severity:     severity,
			Category:     "bottleneck",
			Title:        fmt.Sprintf("%s is a concentration point", deepResearchResourceLabel(resource)),
			Summary:      fmt.Sprintf("The current topology shows %d upstream dependencies feeding this resource. If it slows down or saturates, multiple downstream paths are likely to degrade together.", count),
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			MonthlyCost:  resource.MonthlyPrice,
			Risk:         "High dependency fan-in creates an operational choke point.",
			Score:        float64(count)*20 + resource.MonthlyPrice,
			Evidence: []string{
				fmt.Sprintf("Inbound dependency count: %d", count),
				fmt.Sprintf("Resource type: %s", resource.Type),
			},
			Questions: buildDeepResearchQuestions("topology-bottleneck", resource),
		})
	}

	regions := make(map[string]int)
	for _, resource := range resources {
		region := strings.TrimSpace(resource.Region)
		if region == "" {
			continue
		}
		regions[region]++
	}
	if len(regions) >= 5 {
		regionNames := make([]string, 0, len(regions))
		for region := range regions {
			regionNames = append(regionNames, region)
		}
		sort.Strings(regionNames)
		findings = append(findings, deepResearchFinding{
			ID:       buildDeepResearchFindingID("region-sprawl", strings.Join(regionNames, ",")),
			Severity: "medium",
			Category: "bottleneck",
			Title:    "Estate is spread across many regions",
			Summary:  fmt.Sprintf("Resources were observed in %d regions. That can raise cross-region latency, egress, and operational complexity if the spread is accidental rather than deliberate.", len(regionNames)),
			Risk:     "Operational sprawl can hide latency and cost drag across service boundaries.",
			Score:    float64(len(regionNames)) * 10,
			Evidence: []string{
				fmt.Sprintf("Regions: %s", strings.Join(regionNames, ", ")),
			},
			Questions: []string{
				"Which regions are actually serving production traffic?",
				"Do I have cross-region calls or replication paths that are adding cost or latency?",
			},
		})
	}

	for _, resource := range resources {
		if resource.MonthlyPrice < 100 {
			continue
		}
		connectionCount := len(resource.Connections) + len(resource.TypedConnections)
		if connectionCount != 0 {
			continue
		}
		findings = append(findings, deepResearchFinding{
			ID:                      buildDeepResearchFindingID("orphan-cost", resource.ID),
			Severity:                "high",
			Category:                "bottleneck",
			Title:                   fmt.Sprintf("%s is expensive with no visible topology links", deepResearchResourceLabel(resource)),
			Summary:                 fmt.Sprintf("The resource carries $%.2f/mo but has no modeled connections in the estate snapshot. That often means an orphaned cost center, hidden traffic path, or incomplete dependency mapping.", resource.MonthlyPrice),
			ResourceID:              resource.ID,
			ResourceName:            resource.Name,
			ResourceType:            resource.Type,
			Provider:                inferDeepResearchProvider(resource),
			Region:                  resource.Region,
			MonthlyCost:             resource.MonthlyPrice,
			EstimatedMonthlySavings: resource.MonthlyPrice * 0.5,
			Risk:                    "Unmapped expensive resources are common waste and hidden-dependency sources.",
			Score:                   resource.MonthlyPrice + 60,
			Evidence: []string{
				fmt.Sprintf("Observed monthly price: $%.2f", resource.MonthlyPrice),
				"Connection count in estate snapshot: 0",
			},
			Questions: buildDeepResearchQuestions("orphan-cost", resource),
		})
	}

	return findings
}

func buildResilienceFindings(resources []deepResearchResource) []deepResearchFinding {
	byCriticalType := make(map[string][]deepResearchResource)
	for _, resource := range resources {
		criticalType := deepResearchCriticalType(resource.Type)
		if criticalType == "" {
			continue
		}
		byCriticalType[criticalType] = append(byCriticalType[criticalType], resource)
	}

	findings := make([]deepResearchFinding, 0, 6)
	for criticalType, members := range byCriticalType {
		if len(members) != 1 {
			continue
		}
		resource := members[0]
		findings = append(findings, deepResearchFinding{
			ID:           buildDeepResearchFindingID("single-point", criticalType),
			Severity:     "high",
			Category:     "resilience",
			Title:        fmt.Sprintf("Only one %s resource is visible", criticalType),
			Summary:      fmt.Sprintf("The estate snapshot shows a single %s resource (%s). If it backs a critical path, this is a clear single point of failure or scale ceiling.", criticalType, deepResearchResourceLabel(resource)),
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			MonthlyCost:  resource.MonthlyPrice,
			Risk:         "Single-instance critical services compress both redundancy and burst headroom.",
			Score:        resource.MonthlyPrice + 80,
			Evidence: []string{
				fmt.Sprintf("Critical type: %s", criticalType),
				"Observed count: 1",
			},
			Questions: buildDeepResearchQuestions("single-point", resource),
		})
	}

	for _, resource := range resources {
		state := strings.ToLower(strings.TrimSpace(resource.State))
		if state == "" || state == "running" || state == "active" || state == "available" || state == "healthy" {
			continue
		}
		if safeDeepResearchCost(resource.MonthlyPrice) <= 0 && len(resource.Connections) == 0 && len(resource.TypedConnections) == 0 {
			continue
		}
		findings = append(findings, deepResearchFinding{
			ID:           buildDeepResearchFindingID("degraded-state", resource.ID),
			Severity:     "medium",
			Category:     "resilience",
			Title:        fmt.Sprintf("%s is not in a healthy steady state", deepResearchResourceLabel(resource)),
			Summary:      fmt.Sprintf("The estate snapshot reports state %q. That is worth checking before the next cost or scaling review, especially if this resource is on a primary request path.", resource.State),
			ResourceID:   resource.ID,
			ResourceName: resource.Name,
			ResourceType: resource.Type,
			Provider:     inferDeepResearchProvider(resource),
			Region:       resource.Region,
			MonthlyCost:  resource.MonthlyPrice,
			Risk:         "Degraded infrastructure can turn cost hotspots into availability incidents.",
			Score:        resource.MonthlyPrice + 30,
			Evidence: []string{
				fmt.Sprintf("Observed state: %s", resource.State),
			},
			Questions: buildDeepResearchQuestions("degraded-state", resource),
		})
	}

	return findings
}

func buildMisconfigurationFindings(resources []deepResearchResource) []deepResearchFinding {
	findings := make([]deepResearchFinding, 0, 8)
	for _, resource := range resources {
		if isDeepResearchDatabaseType(resource.Type) {
			if publicAddress := deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "ipAddress"); publicAddress != "" && deepResearchFirstNonEmptyAttr(resource.Attributes, "privateNetwork") == "" {
				findings = append(findings, deepResearchFinding{
					ID:           buildDeepResearchFindingID("public-database", resource.ID),
					Severity:     deepResearchSeverityForPublicExposure(resource.MonthlyPrice),
					Category:     "misconfiguration",
					Title:        fmt.Sprintf("%s appears publicly reachable", deepResearchResourceLabel(resource)),
					Summary:      fmt.Sprintf("The resource exposes a public address (%s). For a database tier, that is a configuration risk unless it is tightly fronted and intentionally public.", publicAddress),
					ResourceID:   resource.ID,
					ResourceName: resource.Name,
					ResourceType: resource.Type,
					Provider:     inferDeepResearchProvider(resource),
					Region:       resource.Region,
					MonthlyCost:  resource.MonthlyPrice,
					Risk:         "Publicly reachable data planes expand blast radius fast.",
					Score:        resource.MonthlyPrice + 220,
					Evidence: []string{
						fmt.Sprintf("Public address: %s", publicAddress),
						"No private network marker was found in the snapshot.",
					},
					Questions: buildDeepResearchQuestions("public-database", resource),
				})
			}

			if backupRetention, ok := deepResearchIntAttr(resource.Attributes, "backupRetentionPeriod"); ok && backupRetention == 0 {
				findings = append(findings, deepResearchFinding{
					ID:           buildDeepResearchFindingID("disabled-backups", resource.ID),
					Severity:     "high",
					Category:     "misconfiguration",
					Title:        fmt.Sprintf("%s has no backup retention", deepResearchResourceLabel(resource)),
					Summary:      "The snapshot shows zero backup retention. That leaves the recovery path weaker than the rest of the estate and turns operator mistakes into durability incidents.",
					ResourceID:   resource.ID,
					ResourceName: resource.Name,
					ResourceType: resource.Type,
					Provider:     inferDeepResearchProvider(resource),
					Region:       resource.Region,
					MonthlyCost:  resource.MonthlyPrice,
					Risk:         "Missing backups convert routine failures into irreversible loss.",
					Score:        resource.MonthlyPrice + 175,
					Evidence: []string{
						"Backup retention period: 0",
					},
					Questions: buildDeepResearchQuestions("disabled-backups", resource),
				})
			}

			if encrypted, ok := deepResearchBoolAttr(resource.Attributes, "storageEncrypted"); ok && !encrypted {
				findings = append(findings, deepResearchFinding{
					ID:           buildDeepResearchFindingID("unencrypted-data", resource.ID),
					Severity:     "critical",
					Category:     "misconfiguration",
					Title:        fmt.Sprintf("%s is not encrypted at rest", deepResearchResourceLabel(resource)),
					Summary:      "The estate snapshot indicates storage encryption is disabled. That is a straight configuration gap and usually one of the first hardening moves to make.",
					ResourceID:   resource.ID,
					ResourceName: resource.Name,
					ResourceType: resource.Type,
					Provider:     inferDeepResearchProvider(resource),
					Region:       resource.Region,
					MonthlyCost:  resource.MonthlyPrice,
					Risk:         "Unencrypted persistent storage widens compliance and breach exposure.",
					Score:        resource.MonthlyPrice + 240,
					Evidence: []string{
						"storageEncrypted=false",
					},
					Questions: buildDeepResearchQuestions("unencrypted-data", resource),
				})
			}
		}

		if isDeepResearchComputeType(resource.Type) && deepResearchHasExternalAddress(resource) && !deepResearchHasOwnershipTags(resource.Tags) {
			findings = append(findings, deepResearchFinding{
				ID:           buildDeepResearchFindingID("public-compute", resource.ID),
				Severity:     "medium",
				Category:     "misconfiguration",
				Title:        fmt.Sprintf("%s is public and lightly owned", deepResearchResourceLabel(resource)),
				Summary:      "The resource appears publicly reachable but the snapshot does not show clear ownership tags. That makes exposure drift harder to review over time.",
				ResourceID:   resource.ID,
				ResourceName: resource.Name,
				ResourceType: resource.Type,
				Provider:     inferDeepResearchProvider(resource),
				Region:       resource.Region,
				MonthlyCost:  resource.MonthlyPrice,
				Risk:         "Public compute with weak ownership tends to linger beyond its intended purpose.",
				Score:        resource.MonthlyPrice + 90,
				Evidence: []string{
					"Public network address detected.",
					"Owner or environment tags were not found.",
				},
				Questions: buildDeepResearchQuestions("public-compute", resource),
			})
		}
	}
	return findings
}

func buildStaleResourceFindings(resources []deepResearchResource) []deepResearchFinding {
	findings := make([]deepResearchFinding, 0, 8)
	now := time.Now().UTC()
	for _, resource := range resources {
		createdAt, ok := deepResearchParseTime(resource.CreatedAt)
		if !ok {
			continue
		}
		ageDays := int(now.Sub(createdAt).Hours() / 24)
		if ageDays < 180 {
			continue
		}
		connectionCount := deepResearchConnectionCount(resource)
		if !isDeepResearchIdleState(resource.State) && connectionCount > 0 {
			continue
		}
		if deepResearchHasOwnershipTags(resource.Tags) && !isDeepResearchIdleState(resource.State) && connectionCount > 0 {
			continue
		}
		severity := "medium"
		if ageDays >= 365 || resource.MonthlyPrice >= 100 {
			severity = "high"
		}
		estimatedSavings := 0.0
		if resource.MonthlyPrice > 0 {
			estimatedSavings = resource.MonthlyPrice * 0.75
		}
		findings = append(findings, deepResearchFinding{
			ID:                      buildDeepResearchFindingID("stale-resource", resource.ID),
			Severity:                severity,
			Category:                "hygiene",
			Title:                   fmt.Sprintf("%s looks stale or forgotten", deepResearchResourceLabel(resource)),
			Summary:                 fmt.Sprintf("The resource is about %d days old, has weak topology signals, and looks like cleanup debt rather than an actively managed asset.", ageDays),
			ResourceID:              resource.ID,
			ResourceName:            resource.Name,
			ResourceType:            resource.Type,
			Provider:                inferDeepResearchProvider(resource),
			Region:                  resource.Region,
			MonthlyCost:             resource.MonthlyPrice,
			EstimatedMonthlySavings: estimatedSavings,
			Risk:                    "Stale infrastructure quietly accumulates spend and attack surface.",
			Score:                   float64(ageDays) + resource.MonthlyPrice + 70,
			Evidence: []string{
				fmt.Sprintf("Approximate age: %d days", ageDays),
				fmt.Sprintf("Connection count: %d", connectionCount),
				fmt.Sprintf("Observed state: %s", resource.State),
			},
			Questions: buildDeepResearchQuestions("stale-resource", resource),
		})
	}
	return findings
}

func buildDeepResearchProviderRollup(resources []deepResearchResource, totalCost float64) []deepResearchProviderRoll {
	byProvider := make(map[string]*deepResearchProviderRoll)
	for _, resource := range resources {
		provider := inferDeepResearchProvider(resource)
		roll, ok := byProvider[provider]
		if !ok {
			roll = &deepResearchProviderRoll{Provider: provider}
			byProvider[provider] = roll
		}
		roll.ResourceCount++
		roll.MonthlyCost += safeDeepResearchCost(resource.MonthlyPrice)
	}
	providers := make([]deepResearchProviderRoll, 0, len(byProvider))
	for _, roll := range byProvider {
		if totalCost > 0 {
			roll.ShareOfCost = (roll.MonthlyCost / totalCost) * 100
		}
		providers = append(providers, *roll)
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].MonthlyCost == providers[j].MonthlyCost {
			return providers[i].Provider < providers[j].Provider
		}
		return providers[i].MonthlyCost > providers[j].MonthlyCost
	})
	return providers
}

func buildDeepResearchSummary(estate deepResearchEstateSnapshot, findings []deepResearchFinding) deepResearchSummary {
	summary := deepResearchSummary{
		TotalResources:          len(estate.Resources),
		TotalMonthlyCost:        estate.TotalCost,
		EstimatedMonthlySavings: 0,
		Regions:                 deepResearchRegions(estate.Resources),
	}
	for _, finding := range findings {
		summary.EstimatedMonthlySavings += finding.EstimatedMonthlySavings
		switch strings.ToLower(strings.TrimSpace(finding.Severity)) {
		case "critical":
			summary.CriticalFindings++
		case "high":
			summary.HighFindings++
		}
		switch strings.ToLower(strings.TrimSpace(finding.Category)) {
		case "bottleneck":
			summary.BottleneckFindings++
		case "cost":
			summary.CostFindings++
		}
		if len(summary.TopRisks) < 3 {
			summary.TopRisks = append(summary.TopRisks, finding.Title)
		}
	}
	if summary.CostFindings > 0 {
		summary.PrimaryFocus = "cost"
	} else if summary.BottleneckFindings > 0 {
		summary.PrimaryFocus = "bottleneck"
	} else if deepResearchHasCategory(findings, "misconfiguration") {
		summary.PrimaryFocus = "misconfiguration"
	} else if deepResearchHasCategory(findings, "hygiene") {
		summary.PrimaryFocus = "hygiene"
	} else {
		summary.PrimaryFocus = "estate-overview"
	}
	return summary
}

func enrichDeepResearchNarrative(ctx context.Context, result deepResearchResult, debug bool) ([]string, error) {
	if len(result.Findings) == 0 {
		return result.Narrative, nil
	}
	aiClient := newConfiguredAIClient(debug)
	prompt := buildDeepResearchNarrativePrompt(result)
	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return nil, err
	}
	cleaned := aiClient.CleanJSONResponse(response)
	var payload struct {
		Narrative []string `json:"narrative"`
	}
	if err := json.Unmarshal([]byte(cleaned), &payload); err != nil {
		return nil, err
	}
	return limitDeepResearchNarrative(payload.Narrative), nil
}

func buildDeepResearchNarrativePrompt(result deepResearchResult) string {
	topFindings := result.Findings
	if len(topFindings) > 6 {
		topFindings = topFindings[:6]
	}
	payload, _ := json.Marshal(struct {
		Summary   deepResearchSummary        `json:"summary"`
		Providers []deepResearchProviderRoll `json:"providers"`
		Findings  []deepResearchFinding      `json:"findings"`
	}{
		Summary:   result.Summary,
		Providers: result.Providers,
		Findings:  topFindings,
	})
	return fmt.Sprintf(`You are Clanker's deep research synthesis agent.

Summarize the infrastructure research results into 2 to 4 concise bullets.
Put cost first, then bottlenecks/resilience, then the sharpest next action.

Return strict JSON only in this shape:
{"narrative":["bullet 1","bullet 2"]}

Input:
%s`, string(payload))
}

func buildDeterministicNarrative(findings []deepResearchFinding, providers []deepResearchProviderRoll) []string {
	lines := make([]string, 0, maxDeepResearchNarrativeBullets)
	if len(providers) > 0 {
		primary := providers[0]
		lines = append(lines, fmt.Sprintf("%s currently drives the largest observed cost share at %.1f%% of the estate.", strings.ToUpper(primary.Provider), primary.ShareOfCost))
	}
	for _, finding := range findings {
		if len(lines) >= maxDeepResearchNarrativeBullets {
			break
		}
		lines = append(lines, finding.Title)
	}
	return lines
}

func limitDeepResearchNarrative(lines []string) []string {
	trimmed := make([]string, 0, maxDeepResearchNarrativeBullets)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed = append(trimmed, line)
		if len(trimmed) >= maxDeepResearchNarrativeBullets {
			break
		}
	}
	return trimmed
}

func sortAndCapDeepResearchFindings(findings []deepResearchFinding) []deepResearchFinding {
	sort.Slice(findings, func(i, j int) bool {
		leftSeverity := deepResearchSeverityRank(findings[i].Severity)
		rightSeverity := deepResearchSeverityRank(findings[j].Severity)
		if leftSeverity != rightSeverity {
			return leftSeverity > rightSeverity
		}
		if findings[i].Score != findings[j].Score {
			return findings[i].Score > findings[j].Score
		}
		return findings[i].Title < findings[j].Title
	})
	if len(findings) > maxDeepResearchFindings {
		return findings[:maxDeepResearchFindings]
	}
	return findings
}

func buildDeepResearchQuestionPlan(question string, estate deepResearchEstateSnapshot) deepResearchQuestionPlan {
	questionLower := strings.ToLower(strings.TrimSpace(question))
	plan := deepResearchQuestionPlan{
		ProviderPriorities: deepResearchProvidersMentionedInQuestion(questionLower),
		WantsLogs:          deepResearchQuestionContains(questionLower, "log", "logs", "error", "errors", "incident", "incidents", "alert", "alerts", "alarm", "alarms", "event", "events"),
		WantsCost:          deepResearchQuestionContains(questionLower, "cost", "costs", "spend", "savings", "optimiz", "rightsiz", "cheaper"),
		WantsMisconfig:     deepResearchQuestionContains(questionLower, "misconfig", "security", "public", "exposure", "encrypt", "backup", "permission", "private networking"),
		WantsHygiene:       deepResearchQuestionContains(questionLower, "stale", "old resource", "old resources", "unused", "orphan", "cleanup", "retire"),
		WantsResilience:    deepResearchQuestionContains(questionLower, "resilien", "reliab", "outage", "failover", "availability", "recovery", "degraded"),
		WantsTopology:      deepResearchQuestionContains(questionLower, "bottleneck", "latency", "scal", "throughput", "dependency", "topology", "hotspot"),
		WantsDatabase:      deepResearchQuestionContains(questionLower, "database", "databases", "rds", "postgres", "mysql", "sql", "redis", "cache", "supabase"),
		WantsCompute:       deepResearchQuestionContains(questionLower, "ec2", "instance", "instances", "lambda", "function", "functions", "ecs", "container", "compute", "vm"),
		WantsNetwork:       deepResearchQuestionContains(questionLower, "network", "ingress", "egress", "gateway", "load balancer", "loadbalancer", "vpc", "security group", "firewall", "dns"),
		WantsKubernetes:    deepResearchQuestionContains(questionLower, "kubernetes", "k8s", "eks", "gke", "cluster", "clusters"),
		WantsTerraform:     deepResearchQuestionContains(questionLower, "terraform", "drift", "tfstate", "plan", "apply", "destroy"),
		WantsCICD:          deepResearchQuestionContains(questionLower, "cicd", "ci/cd", "pipeline", "pipelines", "workflow", "workflows", "github actions", "deploy"),
		DrilldownCount:     3,
	}

	if !(plan.WantsCost || plan.WantsMisconfig || plan.WantsHygiene || plan.WantsResilience || plan.WantsTopology || plan.WantsLogs) {
		plan.WantsCost = true
		plan.WantsMisconfig = true
		plan.WantsHygiene = true
		plan.WantsResilience = true
		plan.WantsTopology = true
	}
	if plan.WantsLogs {
		plan.WantsResilience = true
	}

	if len(plan.ProviderPriorities) == 0 {
		for _, provider := range buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost) {
			plan.ProviderPriorities = append(plan.ProviderPriorities, provider.Provider)
			if len(plan.ProviderPriorities) >= 3 {
				break
			}
		}
	}

	plan.FocusAreas = buildDeepResearchPlanFocusAreas(plan)
	if plan.WantsLogs || plan.WantsMisconfig {
		plan.DrilldownCount++
	}
	if len(plan.ProviderPriorities) > 0 && plan.ProviderPriorities[0] == "aws" {
		plan.DrilldownCount++
	}
	if plan.DrilldownCount > 5 {
		plan.DrilldownCount = 5
	}
	return plan
}

func buildDeepResearchPlanDetails(plan deepResearchQuestionPlan, estate deepResearchEstateSnapshot) []string {
	details := []string{
		fmt.Sprintf("Focus areas: %s", strings.Join(buildDeepResearchPlanFocusAreas(plan), ", ")),
		fmt.Sprintf("Drilldown budget: %d live follow-ups", plan.DrilldownCount),
		fmt.Sprintf("Estate size: %d resources across %d regions", len(estate.Resources), len(deepResearchRegions(estate.Resources))),
	}
	if len(plan.ProviderPriorities) > 0 {
		details = append(details, fmt.Sprintf("Provider priorities: %s", strings.Join(plan.ProviderPriorities, " -> ")))
	}
	return details
}

func buildDeepResearchPlanFocusAreas(plan deepResearchQuestionPlan) []string {
	focuses := make([]string, 0, 8)
	if plan.WantsCost {
		focuses = append(focuses, "cost")
	}
	if plan.WantsTopology {
		focuses = append(focuses, "topology")
	}
	if plan.WantsResilience {
		focuses = append(focuses, "resilience")
	}
	if plan.WantsMisconfig {
		focuses = append(focuses, "misconfiguration")
	}
	if plan.WantsHygiene {
		focuses = append(focuses, "cleanup")
	}
	if plan.WantsLogs {
		focuses = append(focuses, "logs")
	}
	if plan.WantsDatabase {
		focuses = append(focuses, "databases")
	}
	if plan.WantsCompute {
		focuses = append(focuses, "compute")
	}
	if plan.WantsNetwork {
		focuses = append(focuses, "network")
	}
	if plan.WantsKubernetes {
		focuses = append(focuses, "kubernetes")
	}
	if plan.WantsTerraform {
		focuses = append(focuses, "terraform")
	}
	if plan.WantsCICD {
		focuses = append(focuses, "cicd")
	}
	return uniqueNonEmptyStrings(focuses)
}

func deepResearchProvidersMentionedInQuestion(questionLower string) []string {
	providers := make([]string, 0, 4)
	if deepResearchQuestionContains(questionLower, "aws", "amazon web services") {
		providers = append(providers, "aws")
	}
	if deepResearchQuestionContains(questionLower, "gcp", "google cloud") {
		providers = append(providers, "gcp")
	}
	if deepResearchQuestionContains(questionLower, "azure") {
		providers = append(providers, "azure")
	}
	if deepResearchQuestionContains(questionLower, "kubernetes", "k8s", "kubectl", "kubeconfig", "pod", "pods", "deployment", "deployments") {
		providers = append(providers, "k8s")
	}
	if deepResearchQuestionContains(questionLower, "cloudflare") {
		providers = append(providers, "cloudflare")
	}
	if deepResearchQuestionContains(questionLower, "digitalocean", "digital ocean") {
		providers = append(providers, "digitalocean")
	}
	if deepResearchQuestionContains(questionLower, "hetzner") {
		providers = append(providers, "hetzner")
	}
	if deepResearchQuestionContains(questionLower, "supabase") {
		providers = append(providers, "supabase")
	}
	if deepResearchQuestionContains(questionLower, "vercel", "vercel.app") {
		providers = append(providers, "vercel")
	}
	if deepResearchQuestionContains(questionLower, "terraform") {
		providers = append(providers, "terraform")
	}
	return uniqueNonEmptyStrings(providers)
}

func deepResearchQuestionContains(questionLower string, tokens ...string) bool {
	for _, token := range tokens {
		token = strings.ToLower(strings.TrimSpace(token))
		if token != "" && strings.Contains(questionLower, token) {
			return true
		}
	}
	return false
}

func applyDeepResearchQuestionPlan(findings []deepResearchFinding, plan deepResearchQuestionPlan) []deepResearchFinding {
	adjusted := append([]deepResearchFinding(nil), findings...)
	for i := range adjusted {
		boost := 0.0
		switch strings.ToLower(strings.TrimSpace(adjusted[i].Category)) {
		case "cost":
			if plan.WantsCost {
				boost += 38
			}
		case "bottleneck":
			if plan.WantsTopology {
				boost += 34
			}
		case "resilience":
			if plan.WantsResilience {
				boost += 34
			}
		case "misconfiguration":
			if plan.WantsMisconfig {
				boost += 40
			}
		case "hygiene":
			if plan.WantsHygiene {
				boost += 30
			}
		}

		if plan.WantsLogs && strings.ToLower(strings.TrimSpace(adjusted[i].Category)) == "resilience" {
			boost += 18
		}
		if plan.WantsDatabase && isDeepResearchDatabaseType(adjusted[i].ResourceType) {
			boost += 18
		}
		if plan.WantsCompute && isDeepResearchComputeType(adjusted[i].ResourceType) {
			boost += 14
		}
		if plan.WantsNetwork && isDeepResearchNetworkType(adjusted[i].ResourceType) {
			boost += 16
		}
		if plan.WantsKubernetes && isDeepResearchKubernetesType(adjusted[i].ResourceType) {
			boost += 20
		}
		if plan.WantsTerraform && strings.EqualFold(adjusted[i].Provider, "terraform") {
			boost += 24
		}
		if plan.WantsCICD && deepResearchQuestionContains(strings.ToLower(strings.TrimSpace(adjusted[i].ResourceType)), "github", "pipeline", "workflow", "deploy") {
			boost += 16
		}

		rank := deepResearchFindingPriorityRank(adjusted[i], plan.ProviderPriorities)
		switch rank {
		case 0:
			boost += 35
		case 1:
			boost += 20
		case 2:
			boost += 10
		}

		adjusted[i].Score += boost
	}
	return adjusted
}

func deepResearchProviderPriorityRank(provider string, priorities []string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for index, candidate := range priorities {
		if provider == strings.ToLower(strings.TrimSpace(candidate)) {
			return index
		}
	}
	return len(priorities) + 1
}

func deepResearchFindingProviderAliases(finding deepResearchFinding) []string {
	aliases := []string{strings.ToLower(strings.TrimSpace(finding.Provider))}
	if isDeepResearchKubernetesType(finding.ResourceType) {
		aliases = append(aliases, "k8s")
	}
	if deepResearchContainsAny(strings.ToLower(strings.TrimSpace(finding.ResourceType)), "supabase") {
		aliases = append(aliases, "supabase")
	}
	return uniqueNonEmptyStrings(aliases)
}

func deepResearchFindingPriorityRank(finding deepResearchFinding, priorities []string) int {
	bestRank := len(priorities) + 1
	for _, provider := range deepResearchFindingProviderAliases(finding) {
		rank := deepResearchProviderPriorityRank(provider, priorities)
		if rank < bestRank {
			bestRank = rank
		}
	}
	return bestRank
}

func deepResearchFindingLiveProvider(finding deepResearchFinding, options deepResearchRunOptions) string {
	if isDeepResearchKubernetesType(finding.ResourceType) && canRunDeepResearchProviderDrilldown("k8s", options) {
		return "k8s"
	}
	return strings.ToLower(strings.TrimSpace(finding.Provider))
}

func applyDeepResearchFindingPatches(findings []deepResearchFinding, patches []deepResearchFindingPatch) []deepResearchFinding {
	if len(findings) == 0 || len(patches) == 0 {
		return findings
	}
	byFindingID := make(map[string]deepResearchFindingPatch, len(patches))
	for _, patch := range patches {
		if strings.TrimSpace(patch.FindingID) == "" {
			continue
		}
		existing := byFindingID[patch.FindingID]
		existing.FindingID = patch.FindingID
		existing.Evidence = append(existing.Evidence, patch.Evidence...)
		existing.EvidenceDetails = append(existing.EvidenceDetails, patch.EvidenceDetails...)
		existing.Questions = append(existing.Questions, patch.Questions...)
		if strings.TrimSpace(patch.Summary) != "" {
			existing.Summary = strings.TrimSpace(patch.Summary)
		}
		if strings.TrimSpace(patch.Risk) != "" {
			existing.Risk = strings.TrimSpace(patch.Risk)
		}
		existing.ScoreDelta += patch.ScoreDelta
		byFindingID[patch.FindingID] = existing
	}

	updated := append([]deepResearchFinding(nil), findings...)
	for index := range updated {
		patch, ok := byFindingID[updated[index].ID]
		if !ok {
			continue
		}
		updated[index].Evidence = uniqueNonEmptyStrings(append(updated[index].Evidence, patch.Evidence...))
		updated[index].EvidenceDetails = deepResearchNormalizeEvidenceDetails(append(updated[index].EvidenceDetails, patch.EvidenceDetails...), updated[index].Evidence, "heuristic", updated[index].Provider)
		updated[index].Questions = uniqueNonEmptyStrings(append(updated[index].Questions, patch.Questions...))
		if patch.Summary != "" {
			updated[index].Summary = patch.Summary
		}
		if patch.Risk != "" {
			updated[index].Risk = patch.Risk
		}
		updated[index].Score += patch.ScoreDelta
	}
	return updated
}

func dedupeDeepResearchFindings(findings []deepResearchFinding) []deepResearchFinding {
	seen := make(map[string]struct{}, len(findings))
	deduped := make([]deepResearchFinding, 0, len(findings))
	for _, finding := range findings {
		key := strings.TrimSpace(finding.ID)
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(finding.Category + ":" + finding.Title + ":" + finding.ResourceID))
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		finding.Evidence = uniqueNonEmptyStrings(finding.Evidence)
		finding.EvidenceDetails = deepResearchNormalizeEvidenceDetails(finding.EvidenceDetails, finding.Evidence, "heuristic", finding.Provider)
		finding.Questions = uniqueNonEmptyStrings(finding.Questions)
		deduped = append(deduped, finding)
	}
	return deduped
}

func buildDeepResearchFindingID(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		part = strings.ReplaceAll(part, " ", "-")
		part = strings.ReplaceAll(part, "/", "-")
		part = strings.ReplaceAll(part, ":", "-")
		if part == "" {
			continue
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, "-")
}

func deepResearchSeverityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func deepResearchSeverityForCost(cost float64) string {
	switch {
	case cost >= 500:
		return "critical"
	case cost >= 200:
		return "high"
	case cost >= 75:
		return "medium"
	default:
		return "low"
	}
}

func inferDeepResearchProvider(resource deepResearchResource) string {
	if provider, ok := resource.Attributes["provider"].(string); ok {
		normalized := strings.ToLower(strings.TrimSpace(provider))
		if normalized != "" {
			return normalized
		}
	}
	typeLower := strings.ToLower(strings.TrimSpace(resource.Type))
	switch {
	case strings.Contains(typeLower, "azure") || strings.HasPrefix(typeLower, "azure_"):
		return "azure"
	case strings.Contains(typeLower, "gcp") || strings.Contains(typeLower, "google"):
		return "gcp"
	case strings.Contains(typeLower, "kubernetes") || strings.Contains(typeLower, "k8s"):
		return "k8s"
	case strings.Contains(typeLower, "cloudflare") || strings.HasPrefix(typeLower, "cf_"):
		return "cloudflare"
	case strings.Contains(typeLower, "digitalocean") || strings.HasPrefix(typeLower, "do_"):
		return "digitalocean"
	case strings.Contains(typeLower, "github"):
		return "github"
	case strings.Contains(typeLower, "hetzner") || strings.HasPrefix(typeLower, "hz_"):
		return "hetzner"
	case strings.Contains(typeLower, "supabase"):
		return "supabase"
	case strings.Contains(typeLower, "verda"):
		return "verda"
	case strings.Contains(typeLower, "vercel"):
		return "vercel"
	default:
		return "aws"
	}
}

func deepResearchResourceLabel(resource deepResearchResource) string {
	if strings.TrimSpace(resource.Name) != "" {
		return resource.Name
	}
	if strings.TrimSpace(resource.ID) != "" {
		return resource.ID
	}
	return strings.TrimSpace(resource.Type)
}

func isDeepResearchIdleState(state string) bool {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return false
	}
	idleMarkers := []string{"stopped", "inactive", "parked", "suspended", "off", "standby", "deallocated"}
	for _, marker := range idleMarkers {
		if strings.Contains(state, marker) {
			return true
		}
	}
	return false
}

func isDeepResearchBottleneckType(resourceType string) bool {
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	keywords := []string{"db", "sql", "redis", "cache", "queue", "broker", "gateway", "load_balancer", "loadbalancer", "ingress", "rds", "dynamodb", "cloudsql", "cosmos", "kafka", "stream", "search"}
	for _, keyword := range keywords {
		if strings.Contains(resourceType, keyword) {
			return true
		}
	}
	return false
}

func deepResearchCriticalType(resourceType string) string {
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	switch {
	case strings.Contains(resourceType, "db") || strings.Contains(resourceType, "sql") || strings.Contains(resourceType, "rds") || strings.Contains(resourceType, "cloudsql"):
		return "database"
	case strings.Contains(resourceType, "redis") || strings.Contains(resourceType, "cache"):
		return "cache"
	case strings.Contains(resourceType, "queue") || strings.Contains(resourceType, "broker") || strings.Contains(resourceType, "kafka") || strings.Contains(resourceType, "pubsub"):
		return "queue"
	case strings.Contains(resourceType, "load_balancer") || strings.Contains(resourceType, "loadbalancer") || strings.Contains(resourceType, "gateway") || strings.Contains(resourceType, "ingress"):
		return "edge"
	default:
		return ""
	}
}

func deepResearchRegions(resources []deepResearchResource) []string {
	seen := map[string]struct{}{}
	regions := make([]string, 0, len(resources))
	for _, resource := range resources {
		region := strings.TrimSpace(resource.Region)
		if region == "" {
			continue
		}
		if _, exists := seen[region]; exists {
			continue
		}
		seen[region] = struct{}{}
		regions = append(regions, region)
	}
	sort.Strings(regions)
	return regions
}

func summarizeDeepResearchLines(blob string, maxLines int) []string {
	lines := make([]string, 0, maxLines)
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func summarizeDeepResearchTailLines(blob string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	trimmed := make([]string, 0, maxLines)
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed = append(trimmed, line)
	}
	if len(trimmed) <= maxLines {
		return trimmed
	}
	return append([]string(nil), trimmed[len(trimmed)-maxLines:]...)
}

func buildDeepResearchQuestions(kind string, resource deepResearchResource) []string {
	label := deepResearchResourceLabel(resource)
	switch kind {
	case "cost-driver":
		return []string{
			fmt.Sprintf("Why does %s cost so much right now?", label),
			fmt.Sprintf("How can I reduce the monthly cost of %s safely?", label),
		}
	case "idle-cost":
		return []string{
			fmt.Sprintf("Can I delete or hibernate %s without breaking anything?", label),
			fmt.Sprintf("What still depends on %s?", label),
		}
	case "untagged-cost":
		return []string{
			fmt.Sprintf("Who owns %s and which environment is it part of?", label),
			fmt.Sprintf("What tags should I add to %s for cost attribution?", label),
		}
	case "topology-bottleneck":
		return []string{
			fmt.Sprintf("How risky is %s as a bottleneck on the current request path?", label),
			fmt.Sprintf("What scaling or redundancy changes would reduce risk around %s?", label),
		}
	case "orphan-cost":
		return []string{
			fmt.Sprintf("Is %s still serving production traffic?", label),
			fmt.Sprintf("Why does %s have no visible connections in the estate graph?", label),
		}
	case "single-point":
		return []string{
			fmt.Sprintf("How do I remove %s as a single point of failure?", label),
			fmt.Sprintf("What is the blast radius if %s fails?", label),
		}
	case "degraded-state":
		return []string{
			fmt.Sprintf("Why is %s currently %s?", label, strings.TrimSpace(resource.State)),
			fmt.Sprintf("What should I inspect first on %s?", label),
		}
	case "public-database":
		return []string{
			fmt.Sprintf("Why does %s still have a public database address?", label),
			fmt.Sprintf("How do I move %s onto private networking without breaking clients?", label),
		}
	case "disabled-backups":
		return []string{
			fmt.Sprintf("What is the backup and restore path for %s today?", label),
			fmt.Sprintf("How do I enable backups on %s safely?", label),
		}
	case "unencrypted-data":
		return []string{
			fmt.Sprintf("How do I enable encryption at rest on %s?", label),
			fmt.Sprintf("What migration or downtime risk comes with encrypting %s?", label),
		}
	case "public-compute":
		return []string{
			fmt.Sprintf("Should %s really be reachable from the public internet?", label),
			fmt.Sprintf("What is the safest edge pattern for %s?", label),
		}
	case "stale-resource":
		return []string{
			fmt.Sprintf("Is %s still serving anything real?", label),
			fmt.Sprintf("Who owns %s and can I retire it safely?", label),
		}
	default:
		return []string{fmt.Sprintf("Explain the issue around %s in more detail.", label)}
	}
}

func deepResearchHasCategory(findings []deepResearchFinding, category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	for _, finding := range findings {
		if strings.ToLower(strings.TrimSpace(finding.Category)) == category {
			return true
		}
	}
	return false
}

func deepResearchFirstNonEmptyAttr(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := attrs[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func deepResearchBoolAttr(attrs map[string]interface{}, key string) (bool, bool) {
	value, ok := attrs[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		if trimmed == "true" {
			return true, true
		}
		if trimmed == "false" {
			return false, true
		}
	}
	return false, false
}

func deepResearchIntAttr(attrs map[string]interface{}, key string) (int64, bool) {
	value, ok := attrs[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed, true
		}
	case string:
		parsed, err := json.Number(strings.TrimSpace(typed)).Int64()
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func deepResearchHasExternalAddress(resource deepResearchResource) bool {
	return deepResearchFirstNonEmptyAttr(resource.Attributes, "publicIp", "publicIpAddress", "natIP", "ipAddress") != ""
}

func isDeepResearchNetworkType(resourceType string) bool {
	lower := strings.ToLower(strings.TrimSpace(resourceType))
	keywords := []string{"load", "balancer", "gateway", "ingress", "vpc", "subnet", "securitygroup", "security_group", "firewall", "dns", "route53", "cloudfront", "edge"}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func isDeepResearchKubernetesType(resourceType string) bool {
	lower := strings.ToLower(strings.TrimSpace(resourceType))
	return strings.Contains(lower, "k8s") || strings.Contains(lower, "kubernetes") || strings.Contains(lower, "eks") || strings.Contains(lower, "gke") || strings.Contains(lower, "cluster")
}

func deepResearchHasOwnershipTags(tags map[string]string) bool {
	for _, key := range []string{"owner", "team", "service", "project", "environment", "env"} {
		if value := strings.TrimSpace(tags[key]); value != "" {
			return true
		}
	}
	return false
}

func isDeepResearchDatabaseType(resourceType string) bool {
	lower := strings.ToLower(strings.TrimSpace(resourceType))
	return strings.Contains(lower, "db") || strings.Contains(lower, "sql") || strings.Contains(lower, "rds") || strings.Contains(lower, "cloudsql") || strings.Contains(lower, "postgres") || strings.Contains(lower, "mysql") || strings.Contains(lower, "redis") || strings.Contains(lower, "cosmos")
}

func isDeepResearchComputeType(resourceType string) bool {
	lower := strings.ToLower(strings.TrimSpace(resourceType))
	return strings.Contains(lower, "ec2") || strings.Contains(lower, "instance") || strings.Contains(lower, "vm") || strings.Contains(lower, "droplet") || strings.Contains(lower, "server") || strings.Contains(lower, "compute") || strings.Contains(lower, "lambda") || strings.Contains(lower, "cloudrun") || strings.Contains(lower, "function")
}

func deepResearchSeverityForPublicExposure(monthlyCost float64) string {
	if monthlyCost >= 200 {
		return "critical"
	}
	return "high"
}

func deepResearchConnectionCount(resource deepResearchResource) int {
	return len(resource.Connections) + len(resource.TypedConnections)
}

func deepResearchParseTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func summarizeDeepResearchEvidenceLines(blob string, maxLines int) []string {
	lines := make([]string, 0, maxLines)
	fallback := ""
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "(") && fallback == "" {
			fallback = line
			continue
		}
		lines = append(lines, line)
		if len(lines) >= maxLines {
			break
		}
	}
	if len(lines) == 0 && fallback != "" {
		return []string{fallback}
	}
	return lines
}

type deepResearchContextSection struct {
	Name  string
	Lines []string
}

type deepResearchEvidenceCandidate struct {
	Detail  string
	Section string
	Score   int
	Order   int
}

func deepResearchBuildProviderEvidenceDetails(provider string, finding deepResearchFinding, plan deepResearchQuestionPlan, contextResult deepResearchProviderContext, source string, maxLines int) []deepResearchEvidenceDetail {
	sections := deepResearchSplitContextSections(contextResult.Blob)
	sectionHints := deepResearchProviderSectionHints(provider, finding, plan)
	lineHints := deepResearchProviderFocusTerms(provider, finding, plan)
	matchTerms := deepResearchFindingMatchTerms(provider, finding)
	candidates := make([]deepResearchEvidenceCandidate, 0, maxLines*4)
	order := 0
	for _, section := range sections {
		sectionName := strings.TrimSpace(section.Name)
		sectionLower := strings.ToLower(sectionName)
		sectionScore := 0
		if deepResearchContainsAny(sectionLower, sectionHints...) {
			sectionScore += 30
		}
		if deepResearchContainsAny(sectionLower, lineHints...) {
			sectionScore += 12
		}
		for _, rawLine := range section.Lines {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			lineLower := strings.ToLower(line)
			score := sectionScore
			if deepResearchContainsAny(lineLower, matchTerms...) {
				score += 38
			}
			if deepResearchContainsAny(lineLower, lineHints...) {
				score += 18
			}
			if strings.TrimSpace(finding.Region) != "" && strings.Contains(lineLower, strings.ToLower(strings.TrimSpace(finding.Region))) {
				score += 8
			}
			if score <= 0 {
				if len(matchTerms) == 0 && sectionScore > 0 {
					score = sectionScore
				} else {
					continue
				}
			}
			candidates = append(candidates, deepResearchEvidenceCandidate{Detail: line, Section: sectionName, Score: score, Order: order})
			order++
		}
	}
	if len(candidates) == 0 {
		return deepResearchBuildEvidenceDetailsFromLines(summarizeDeepResearchEvidenceLines(contextResult.Blob, maxLines), source, provider, "")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Order < candidates[j].Order
	})
	details := make([]deepResearchEvidenceDetail, 0, maxLines)
	seen := make(map[string]struct{}, maxLines)
	for _, candidate := range candidates {
		if _, exists := seen[candidate.Detail]; exists {
			continue
		}
		seen[candidate.Detail] = struct{}{}
		details = append(details, deepResearchEvidenceDetail{
			Detail:   candidate.Detail,
			Source:   source,
			Provider: provider,
			Section:  candidate.Section,
		})
		if len(details) >= maxLines {
			break
		}
	}
	if len(details) == 0 {
		return deepResearchBuildEvidenceDetailsFromLines(summarizeDeepResearchEvidenceLines(contextResult.Blob, maxLines), source, provider, "")
	}
	return details
}

func deepResearchProviderSectionHints(provider string, finding deepResearchFinding, plan deepResearchQuestionPlan) []string {
	lowerProvider := strings.ToLower(strings.TrimSpace(provider))
	lowerType := strings.ToLower(strings.TrimSpace(finding.ResourceType))
	hints := make([]string, 0, 12)
	switch lowerProvider {
	case "aws":
		switch {
		case isDeepResearchDatabaseType(lowerType):
			hints = append(hints, "rds instances")
		case strings.Contains(lowerType, "lambda") || strings.Contains(lowerType, "function"):
			hints = append(hints, "lambda functions")
		case strings.Contains(lowerType, "s3") || strings.Contains(lowerType, "bucket"):
			hints = append(hints, "s3 buckets")
		case strings.Contains(lowerType, "ecs") || strings.Contains(lowerType, "container") || isDeepResearchKubernetesType(lowerType):
			hints = append(hints, "ecs services")
		case strings.Contains(lowerType, "iam") || strings.Contains(lowerType, "role"):
			hints = append(hints, "iam roles")
		default:
			hints = append(hints, "ec2 instances")
		}
		if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
			hints = append(hints, "cloudwatch log groups", "recent error logs", "cloudwatch alarms")
		}
	case "gcp":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			hints = append(hints, "gke clusters")
		case strings.Contains(lowerType, "cloudrun") || strings.Contains(lowerType, "cloud run") || strings.Contains(lowerType, "run"):
			hints = append(hints, "cloud run services", "cloud run jobs")
		case strings.Contains(lowerType, "function"):
			hints = append(hints, "cloud functions", "cloud functions gen2")
		case strings.Contains(lowerType, "pubsub"):
			hints = append(hints, "pub/sub topics", "pub/sub subscriptions")
		case strings.Contains(lowerType, "firestore"):
			hints = append(hints, "firestore databases")
		case strings.Contains(lowerType, "bigquery"):
			hints = append(hints, "bigquery datasets")
		case strings.Contains(lowerType, "spanner"):
			hints = append(hints, "cloud spanner instances")
		case strings.Contains(lowerType, "bigtable"):
			hints = append(hints, "bigtable instances")
		case strings.Contains(lowerType, "memorystore") || strings.Contains(lowerType, "redis"):
			hints = append(hints, "memorystore redis")
		case isDeepResearchDatabaseType(lowerType):
			hints = append(hints, "cloud sql instances")
		case isDeepResearchNetworkType(lowerType):
			hints = append(hints, "load balancers", "firewall rules", "vpc networks", "subnets", "cloud dns zones", "cloud armor policies")
		default:
			hints = append(hints, "compute instances")
		}
		if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
			hints = append(hints, "recent error logs", "monitoring alert policies")
		}
	case "azure":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			hints = append(hints, "aks clusters")
		case strings.Contains(lowerType, "webapp") || strings.Contains(lowerType, "appservice"):
			hints = append(hints, "app services")
		case strings.Contains(lowerType, "function"):
			hints = append(hints, "function apps")
		case strings.Contains(lowerType, "cosmos"):
			hints = append(hints, "cosmos db")
		case strings.Contains(lowerType, "postgres"):
			hints = append(hints, "azure postgresql flexible servers")
		case strings.Contains(lowerType, "mysql"):
			hints = append(hints, "azure mysql flexible servers")
		case strings.Contains(lowerType, "redis"):
			hints = append(hints, "azure cache for redis")
		case isDeepResearchDatabaseType(lowerType):
			hints = append(hints, "azure sql servers", "azure sql databases")
		default:
			hints = append(hints, "virtual machines")
		}
		if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
			hints = append(hints, "activity logs", "alert rules")
		}
	case "k8s":
		hints = append(hints, "cluster", "recent events", "top pods")
		if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
			hints = append(hints, "recent logs", "crashloop", "restarts")
		}
	case "cloudflare":
		hints = append(hints, "zones", "account details")
	case "digitalocean":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			hints = append(hints, "kubernetes clusters")
		case isDeepResearchDatabaseType(lowerType):
			hints = append(hints, "databases")
		case strings.Contains(lowerType, "space") || strings.Contains(lowerType, "bucket") || strings.Contains(lowerType, "storage"):
			hints = append(hints, "spaces")
		case strings.Contains(lowerType, "app"):
			hints = append(hints, "apps")
		case isDeepResearchNetworkType(lowerType):
			hints = append(hints, "load balancers", "vpcs", "firewalls", "domains")
		default:
			hints = append(hints, "droplets")
		}
	case "hetzner":
		switch {
		case strings.Contains(lowerType, "volume") || strings.Contains(lowerType, "disk"):
			hints = append(hints, "volumes")
		case isDeepResearchNetworkType(lowerType):
			hints = append(hints, "load balancers", "networks", "firewalls", "floating ips", "primary ips")
		default:
			hints = append(hints, "servers")
		}
	case "supabase":
		hints = append(hints, "configured database connections", "focused database", "top schemas", "largest tables", "objects")
	case "vercel":
		switch {
		case strings.Contains(lowerType, "deployment"):
			hints = append(hints, "recent deployments", "preview deployments", "production deployments")
		case strings.Contains(lowerType, "domain"):
			hints = append(hints, "domains", "aliases", "dns")
		default:
			hints = append(hints, "projects", "deployments", "domains", "usage")
		}
		if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
			hints = append(hints, "recent build failures", "deployment status")
		}
	case "terraform":
		hints = append(hints, "terraform workspace info", "terraform state", "terraform outputs")
		if plan.WantsTerraform || strings.EqualFold(finding.Category, "misconfiguration") || strings.EqualFold(finding.Category, "hygiene") {
			hints = append(hints, "terraform plan")
		}
	}
	return uniqueNonEmptyStrings(hints)
}

func deepResearchProviderFocusTerms(provider string, finding deepResearchFinding, plan deepResearchQuestionPlan) []string {
	terms := []string{
		deepResearchProviderServiceLabel(provider, finding),
		strings.TrimSpace(finding.ResourceType),
		strings.TrimSpace(finding.ResourceName),
		strings.TrimSpace(finding.ResourceID),
		strings.TrimSpace(finding.Region),
	}
	terms = append(terms, deepResearchProviderSectionHints(provider, finding, plan)...)
	if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
		terms = append(terms, "logs", "errors", "alerts", "incident")
	}
	if plan.WantsCost || strings.EqualFold(finding.Category, "cost") {
		terms = append(terms, "cost", "utilization", "savings")
	}
	if plan.WantsMisconfig || strings.EqualFold(finding.Category, "misconfiguration") {
		terms = append(terms, "public exposure", "encryption", "backup", "permission", "firewall")
	}
	if plan.WantsHygiene || strings.EqualFold(finding.Category, "hygiene") {
		terms = append(terms, "unused", "stale", "orphan", "cleanup")
	}
	if plan.WantsTopology || strings.EqualFold(finding.Category, "bottleneck") {
		terms = append(terms, "dependency", "bottleneck", "throughput", "scaling")
	}
	return uniqueNonEmptyStrings(terms)
}

func deepResearchProviderServiceLabel(provider string, finding deepResearchFinding) string {
	lowerProvider := strings.ToLower(strings.TrimSpace(provider))
	lowerType := strings.ToLower(strings.TrimSpace(finding.ResourceType))
	switch lowerProvider {
	case "aws":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			return "EKS cluster"
		case strings.Contains(lowerType, "ecs") || strings.Contains(lowerType, "container"):
			return "ECS service"
		case strings.Contains(lowerType, "lambda") || strings.Contains(lowerType, "function"):
			return "Lambda function"
		case isDeepResearchDatabaseType(lowerType):
			return "RDS database"
		case strings.Contains(lowerType, "s3") || strings.Contains(lowerType, "bucket"):
			return "S3 bucket"
		case strings.Contains(lowerType, "iam") || strings.Contains(lowerType, "role"):
			return "IAM role"
		case isDeepResearchNetworkType(lowerType):
			return "AWS network edge"
		default:
			return "EC2 instance"
		}
	case "gcp":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			return "GKE cluster"
		case strings.Contains(lowerType, "cloudrun") || strings.Contains(lowerType, "cloud run") || strings.Contains(lowerType, "run"):
			return "Cloud Run service"
		case strings.Contains(lowerType, "function"):
			return "Cloud Function"
		case strings.Contains(lowerType, "pubsub"):
			return "Pub/Sub service"
		case strings.Contains(lowerType, "bigquery"):
			return "BigQuery dataset"
		case strings.Contains(lowerType, "firestore"):
			return "Firestore database"
		case strings.Contains(lowerType, "memorystore") || strings.Contains(lowerType, "redis"):
			return "Memorystore instance"
		case isDeepResearchDatabaseType(lowerType):
			return "Cloud SQL instance"
		case isDeepResearchNetworkType(lowerType):
			return "GCP network edge"
		default:
			return "Compute Engine instance"
		}
	case "azure":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			return "AKS cluster"
		case strings.Contains(lowerType, "webapp") || strings.Contains(lowerType, "appservice"):
			return "App Service"
		case strings.Contains(lowerType, "function"):
			return "Function App"
		case strings.Contains(lowerType, "cosmos"):
			return "Cosmos DB account"
		case strings.Contains(lowerType, "postgres"):
			return "Azure PostgreSQL server"
		case strings.Contains(lowerType, "mysql"):
			return "Azure MySQL server"
		case strings.Contains(lowerType, "redis"):
			return "Azure Cache for Redis"
		case isDeepResearchDatabaseType(lowerType):
			return "Azure SQL service"
		case isDeepResearchNetworkType(lowerType):
			return "Azure network edge"
		default:
			return "Azure VM"
		}
	case "k8s":
		switch {
		case strings.Contains(lowerType, "service"):
			return "Kubernetes service"
		case strings.Contains(lowerType, "pod"):
			return "Kubernetes pod"
		case strings.Contains(lowerType, "deploy"):
			return "Kubernetes deployment"
		case strings.Contains(lowerType, "node"):
			return "Kubernetes node"
		default:
			return "Kubernetes cluster"
		}
	case "cloudflare":
		if strings.Contains(lowerType, "worker") {
			return "Cloudflare Worker"
		}
		return "Cloudflare zone"
	case "digitalocean":
		switch {
		case isDeepResearchKubernetesType(lowerType):
			return "DOKS cluster"
		case isDeepResearchDatabaseType(lowerType):
			return "DigitalOcean database"
		case strings.Contains(lowerType, "app"):
			return "App Platform service"
		case strings.Contains(lowerType, "space") || strings.Contains(lowerType, "bucket") || strings.Contains(lowerType, "storage"):
			return "Spaces bucket"
		case isDeepResearchNetworkType(lowerType):
			return "DigitalOcean network edge"
		default:
			return "Droplet"
		}
	case "hetzner":
		switch {
		case strings.Contains(lowerType, "volume") || strings.Contains(lowerType, "disk"):
			return "Hetzner volume"
		case isDeepResearchNetworkType(lowerType):
			return "Hetzner network edge"
		default:
			return "Hetzner server"
		}
	case "supabase":
		return "Supabase database"
	case "vercel":
		switch {
		case strings.Contains(lowerType, "deployment"):
			return "Vercel deployment"
		case strings.Contains(lowerType, "domain"):
			return "Vercel domain"
		default:
			return "Vercel project"
		}
	case "terraform":
		return "Terraform managed resource"
	default:
		if strings.TrimSpace(finding.ResourceType) != "" {
			return strings.TrimSpace(finding.ResourceType)
		}
		if strings.TrimSpace(provider) != "" {
			return strings.TrimSpace(provider) + " resource"
		}
		return "resource"
	}
}

func deepResearchFindingMatchTerms(provider string, finding deepResearchFinding) []string {
	terms := []string{
		strings.TrimSpace(finding.ResourceName),
		strings.TrimSpace(finding.ResourceID),
		strings.TrimSpace(finding.ResourceType),
		deepResearchProviderServiceLabel(provider, finding),
		strings.TrimSpace(finding.Region),
	}
	terms = append(terms, deepResearchTextTokens(terms...)...)
	return uniqueNonEmptyStrings(terms)
}

func deepResearchTextTokens(values ...string) []string {
	tokens := make([]string, 0, len(values)*2)
	replacer := strings.NewReplacer("-", " ", "_", " ", "/", " ", ":", " ", ".", " ", ",", " ")
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		tokens = append(tokens, value)
		for _, token := range strings.Fields(replacer.Replace(value)) {
			token = strings.TrimSpace(token)
			if len(token) < 4 {
				continue
			}
			tokens = append(tokens, token)
		}
	}
	return uniqueNonEmptyStrings(tokens)
}

func deepResearchContainsAny(candidate string, hints ...string) bool {
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	if candidate == "" {
		return false
	}
	for _, hint := range hints {
		hint = strings.ToLower(strings.TrimSpace(hint))
		if hint != "" && strings.Contains(candidate, hint) {
			return true
		}
	}
	return false
}

func deepResearchSplitContextSections(blob string) []deepResearchContextSection {
	sections := make([]deepResearchContextSection, 0, 8)
	current := deepResearchContextSection{}
	haveCurrent := false
	for _, rawLine := range strings.Split(blob, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ":") {
			if haveCurrent && (strings.TrimSpace(current.Name) != "" || len(current.Lines) > 0) {
				sections = append(sections, current)
			}
			current = deepResearchContextSection{Name: strings.TrimSuffix(line, ":")}
			haveCurrent = true
			continue
		}
		if !haveCurrent {
			current = deepResearchContextSection{Name: "General"}
			haveCurrent = true
		}
		current.Lines = append(current.Lines, line)
	}
	if haveCurrent && (strings.TrimSpace(current.Name) != "" || len(current.Lines) > 0) {
		sections = append(sections, current)
	}
	return sections
}

func deepResearchBuildEvidenceDetailsFromLines(lines []string, source string, provider string, section string) []deepResearchEvidenceDetail {
	details := make([]deepResearchEvidenceDetail, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		details = append(details, deepResearchEvidenceDetail{
			Detail:   line,
			Source:   strings.TrimSpace(source),
			Provider: strings.TrimSpace(provider),
			Section:  strings.TrimSpace(section),
		})
	}
	return details
}

func deepResearchEvidenceStrings(details []deepResearchEvidenceDetail) []string {
	values := make([]string, 0, len(details))
	for _, detail := range details {
		values = append(values, strings.TrimSpace(detail.Detail))
	}
	return uniqueNonEmptyStrings(values)
}

func deepResearchNormalizeEvidenceDetails(details []deepResearchEvidenceDetail, evidence []string, defaultSource string, provider string) []deepResearchEvidenceDetail {
	normalized := make([]deepResearchEvidenceDetail, 0, len(details)+len(evidence))
	seenLines := make(map[string]struct{}, len(details)+len(evidence))
	for _, detail := range details {
		detail.Detail = strings.TrimSpace(detail.Detail)
		if detail.Detail == "" {
			continue
		}
		detail.Source = deepResearchNonEmpty(detail.Source, defaultSource)
		detail.Provider = deepResearchNonEmpty(detail.Provider, provider)
		detail.Section = strings.TrimSpace(detail.Section)
		normalized = append(normalized, detail)
		seenLines[detail.Detail] = struct{}{}
	}
	for _, line := range evidence {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, exists := seenLines[line]; exists {
			continue
		}
		normalized = append(normalized, deepResearchEvidenceDetail{
			Detail:   line,
			Source:   deepResearchNonEmpty(defaultSource, "heuristic"),
			Provider: strings.TrimSpace(provider),
		})
	}
	return deepResearchDedupeEvidenceDetails(normalized)
}

func deepResearchDedupeEvidenceDetails(details []deepResearchEvidenceDetail) []deepResearchEvidenceDetail {
	seen := make(map[string]struct{}, len(details))
	unique := make([]deepResearchEvidenceDetail, 0, len(details))
	for _, detail := range details {
		key := strings.ToLower(strings.TrimSpace(detail.Source)) + "|" + strings.ToLower(strings.TrimSpace(detail.Provider)) + "|" + strings.ToLower(strings.TrimSpace(detail.Section)) + "|" + strings.TrimSpace(detail.Detail)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, detail)
	}
	return unique
}

func deepResearchProviderEvidenceFocusLabel(provider string, finding deepResearchFinding, details []deepResearchEvidenceDetail) string {
	sections := make([]string, 0, len(details))
	for _, detail := range details {
		if strings.TrimSpace(detail.Section) != "" {
			sections = append(sections, strings.TrimSpace(detail.Section))
		}
	}
	sections = deepResearchLimitStrings(uniqueNonEmptyStrings(sections), 2)
	if len(sections) > 0 {
		return deepResearchHumanList(sections)
	}
	return deepResearchProviderServiceLabel(provider, finding)
}

func deepResearchHumanList(values []string) string {
	values = uniqueNonEmptyStrings(values)
	switch len(values) {
	case 0:
		return "current provider state"
	case 1:
		return values[0]
	case 2:
		return values[0] + " and " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", and " + values[len(values)-1]
	}
}

func buildDeepResearchDrilldownQuestions(finding deepResearchFinding, plan deepResearchQuestionPlan) []string {
	label := deepResearchResourceLabelFromFinding(finding)
	questions := make([]string, 0, 4)
	if plan.WantsLogs || strings.EqualFold(finding.Category, "resilience") {
		questions = append(questions, fmt.Sprintf("What do the recent AWS logs and alarms say about %s?", label))
	}
	if plan.WantsCost || strings.EqualFold(finding.Category, "cost") {
		questions = append(questions, fmt.Sprintf("Which AWS setting or usage pattern is driving cost on %s?", label))
	}
	if plan.WantsMisconfig || strings.EqualFold(finding.Category, "misconfiguration") {
		questions = append(questions, fmt.Sprintf("Which AWS configuration change should I make first on %s?", label))
	}
	if plan.WantsTopology || strings.EqualFold(finding.Category, "bottleneck") {
		questions = append(questions, fmt.Sprintf("What is the first scaling or dependency choke point to inspect on %s?", label))
	}
	if plan.WantsHygiene || strings.EqualFold(finding.Category, "hygiene") {
		questions = append(questions, fmt.Sprintf("What evidence proves %s is still in active use?", label))
	}
	if len(questions) == 0 {
		questions = append(questions, fmt.Sprintf("What should I inspect next for %s in AWS?", label))
	}
	return uniqueNonEmptyStrings(questions)
}

func deepResearchAWSServiceHint(resourceType string) string {
	lower := strings.ToLower(strings.TrimSpace(resourceType))
	switch {
	case strings.Contains(lower, "rds") || strings.Contains(lower, "sql") || strings.Contains(lower, "db"):
		return "rds database"
	case strings.Contains(lower, "lambda") || strings.Contains(lower, "function"):
		return "lambda function"
	case strings.Contains(lower, "s3") || strings.Contains(lower, "bucket"):
		return "s3 bucket"
	case strings.Contains(lower, "ecs") || strings.Contains(lower, "container"):
		return "ecs container"
	case strings.Contains(lower, "iam") || strings.Contains(lower, "role"):
		return "iam role"
	default:
		return "ec2 instance"
	}
}

func deepResearchCompactLabel(primary string, fallback string) string {
	label := strings.ToLower(strings.TrimSpace(primary))
	if label == "" {
		label = strings.ToLower(strings.TrimSpace(fallback))
	}
	label = strings.ReplaceAll(label, " ", "-")
	label = strings.ReplaceAll(label, "/", "-")
	label = strings.ReplaceAll(label, ":", "-")
	for strings.Contains(label, "--") {
		label = strings.ReplaceAll(label, "--", "-")
	}
	label = strings.Trim(label, "-")
	if label == "" {
		return "finding"
	}
	if len(label) > 40 {
		return strings.Trim(label[:40], "-")
	}
	return label
}

func deepResearchResourceLabelFromFinding(finding deepResearchFinding) string {
	if strings.TrimSpace(finding.ResourceName) != "" {
		return strings.TrimSpace(finding.ResourceName)
	}
	if strings.TrimSpace(finding.ResourceID) != "" {
		return strings.TrimSpace(finding.ResourceID)
	}
	if strings.TrimSpace(finding.Title) != "" {
		return strings.TrimSpace(finding.Title)
	}
	return "resource"
}

func deepResearchNonEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
