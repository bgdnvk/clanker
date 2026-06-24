package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
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
	defaultDeepResearchQuestion     = "Analyze the current infrastructure from the perspective of an experienced systems architect and cloud architect. Assess system status, architecture, provider coverage, bottlenecks, reliability, security, operational hygiene, optimization opportunities, and cost efficiency. Recommend concrete next actions for resources and systems."
	deepResearchResultMarker        = "::clanker-deep-research-result::"
	runtimeDeepResearchEstateEnv    = "CLANKER_RUNTIME_DEEP_RESEARCH_ESTATE_JSON"
	maxDeepResearchNarrativeBullets = 4
	maxDeepResearchFindings         = 14
	maxDeepResearchSystemRecs       = 6
)

type deepResearchEstateSnapshot struct {
	Resources   []deepResearchResource   `json:"resources"`
	TotalCost   float64                  `json:"totalCost"`
	LastUpdated string                   `json:"lastUpdated,omitempty"`
	TerraformOK bool                     `json:"terraformOk,omitempty"`
	CostSummary *deepResearchCostSummary `json:"costSummary,omitempty"`
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

type deepResearchCostSummary struct {
	TotalCost     float64                    `json:"totalCost,omitempty"`
	LastMonthCost float64                    `json:"lastMonthCost,omitempty"`
	Currency      string                     `json:"currency,omitempty"`
	ProviderCosts []deepResearchProviderCost `json:"providerCosts,omitempty"`
	TopServices   []deepResearchServiceCost  `json:"topServices,omitempty"`
	LastUpdated   string                     `json:"lastUpdated,omitempty"`
}

type deepResearchProviderCost struct {
	Provider         string                    `json:"provider"`
	TotalCost        float64                   `json:"totalCost"`
	Currency         string                    `json:"currency,omitempty"`
	ServiceBreakdown []deepResearchServiceCost `json:"serviceBreakdown,omitempty"`
	Change           float64                   `json:"change,omitempty"`
}

type deepResearchServiceCost struct {
	Service       string  `json:"service"`
	Cost          float64 `json:"cost"`
	UsageQuantity float64 `json:"usageQuantity,omitempty"`
	UsageUnit     string  `json:"usageUnit,omitempty"`
	ResourceCount int     `json:"resourceCount,omitempty"`
}

type deepResearchResult struct {
	Query             string                         `json:"query"`
	GeneratedAt       string                         `json:"generatedAt"`
	Summary           deepResearchSummary            `json:"summary"`
	Findings          []deepResearchFinding          `json:"findings"`
	Providers         []deepResearchProviderRoll     `json:"providers,omitempty"`
	Subagents         []deepResearchSubagentRun      `json:"subagents,omitempty"`
	Warnings          []string                       `json:"warnings,omitempty"`
	Narrative         []string                       `json:"narrative,omitempty"`
	SystemImprovement *deepResearchSystemImprovement `json:"systemImprovement,omitempty"`
	ResearchQuality   *deepResearchQuality           `json:"researchQuality,omitempty"`
	AdvisorBenchmarks *deepResearchAdvisorBenchmarks `json:"advisorBenchmarks,omitempty"`
	ExpertTeam        *deepResearchExpertTeam        `json:"expertTeam,omitempty"`
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

type deepResearchSystemImprovement struct {
	Overview        string                             `json:"overview,omitempty"`
	Traffic         deepResearchSystemImprovementBlock `json:"traffic"`
	Codebase        deepResearchSystemImprovementBlock `json:"codebase"`
	Architecture    deepResearchSystemImprovementBlock `json:"architecture"`
	Recommendations []deepResearchSystemRecommendation `json:"recommendations,omitempty"`
}

type deepResearchSystemImprovementBlock struct {
	Status   string   `json:"status,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Gaps     []string `json:"gaps,omitempty"`
}

type deepResearchSystemRecommendation struct {
	ID       string   `json:"id"`
	Priority string   `json:"priority"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Impact   string   `json:"impact,omitempty"`
	Effort   string   `json:"effort,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Actions  []string `json:"actions,omitempty"`
}

type deepResearchQuality struct {
	Score             int                        `json:"score"`
	Confidence        string                     `json:"confidence"`
	Summary           string                     `json:"summary,omitempty"`
	EvidenceMix       []deepResearchEvidenceMix  `json:"evidenceMix,omitempty"`
	Checks            []deepResearchQualityCheck `json:"checks,omitempty"`
	ContextGaps       []string                   `json:"contextGaps,omitempty"`
	NextDataToCollect []string                   `json:"nextDataToCollect,omitempty"`
}

type deepResearchEvidenceMix struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	Count  int    `json:"count"`
}

type deepResearchQualityCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type deepResearchAdvisorBenchmarks struct {
	Summary  string                            `json:"summary,omitempty"`
	Pillars  []deepResearchAdvisorPillar       `json:"pillars,omitempty"`
	Lessons  []deepResearchAdvisorLesson       `json:"lessons,omitempty"`
	Workflow []deepResearchAdvisorWorkflowStep `json:"workflow,omitempty"`
}

type deepResearchAdvisorPillar struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	Status              string   `json:"status"`
	Score               int      `json:"score"`
	Summary             string   `json:"summary"`
	FindingCount        int      `json:"findingCount"`
	RecommendationCount int      `json:"recommendationCount"`
	Evidence            []string `json:"evidence,omitempty"`
	NextAction          string   `json:"nextAction,omitempty"`
}

type deepResearchAdvisorLesson struct {
	Product   string `json:"product"`
	Lesson    string `json:"lesson"`
	AppliedAs string `json:"appliedAs"`
}

type deepResearchAdvisorWorkflowStep struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Summary string   `json:"summary"`
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
}

type deepResearchExpertTeam struct {
	Status      string                       `json:"status"`
	Summary     string                       `json:"summary,omitempty"`
	Personas    []deepResearchExpertPersona  `json:"personas,omitempty"`
	Conclusions []deepResearchTeamConclusion `json:"conclusions,omitempty"`
	AgentRuns   []deepResearchExpertAgentRun `json:"agentRuns,omitempty"`
	Dialogues   []deepResearchTeamDialogue   `json:"dialogues,omitempty"`
	Consensus   []string                     `json:"consensus,omitempty"`
}

type deepResearchExpertPersona struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Discipline      string   `json:"discipline,omitempty"`
	Status          string   `json:"status"`
	Summary         string   `json:"summary,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	Concerns        []string `json:"concerns,omitempty"`
	Recommendations []string `json:"recommendations,omitempty"`
}

type deepResearchTeamConclusion struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Summary     string   `json:"summary,omitempty"`
	Owners      []string `json:"owners,omitempty"`
	NextActions []string `json:"nextActions,omitempty"`
}

type deepResearchExpertAgentRun struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  string   `json:"status"`
	Summary string   `json:"summary,omitempty"`
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
}

type deepResearchTeamDialogue struct {
	ID       string   `json:"id"`
	Topic    string   `json:"topic"`
	Agents   []string `json:"agents,omitempty"`
	Exchange string   `json:"exchange,omitempty"`
	Decision string   `json:"decision,omitempty"`
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

The command fans out across several analysis subagents, weighs architecture,
reliability, security, operations, provider coverage, and cost signals, then
emits a final structured JSON payload that clanker-cloud can render into a report.`,
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
		fmt.Printf("[research][coverage] reconciling %d resources, %d billing provider groups, and $%.2f/mo observed spend\n", len(estate.Resources), len(deepResearchBillingProviderNames(estate)), estate.TotalCost)

		findings, subagents, subagentWarnings := runDeepResearchSubagents(context.Background(), question, estate, options)
		warnings = append(warnings, subagentWarnings...)

		providers := buildDeepResearchProviderRollupFromEstate(estate)
		findings = sortAndCapDeepResearchFindings(findings)
		narrative := buildDeterministicNarrative(findings, providers)
		systemImprovement := buildDeepResearchSystemImprovement(estate, findings, providers, buildDeepResearchQuestionPlan(question, estate))
		researchQuality := buildDeepResearchQuality(estate, findings, providers, systemImprovement)
		advisorBenchmarks := buildDeepResearchAdvisorBenchmarks(estate, findings, providers, systemImprovement, researchQuality)
		fmt.Printf("[research][expert-team] synthesizing systems, cloud, SRE, DevOps, FinOps, security, data, and product reviews\n")
		expertTeam := buildDeepResearchExpertTeam(estate, findings, providers, systemImprovement, researchQuality, advisorBenchmarks)
		subagents = append(subagents, buildDeepResearchExpertSubagentRuns(expertTeam)...)

		result := deepResearchResult{
			Query:             question,
			GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
			Summary:           buildDeepResearchSummary(estate, findings),
			Findings:          findings,
			Providers:         providers,
			Subagents:         subagents,
			Warnings:          uniqueNonEmptyStrings(warnings),
			Narrative:         narrative,
			SystemImprovement: systemImprovement,
			ResearchQuality:   researchQuality,
			AdvisorBenchmarks: advisorBenchmarks,
			ExpertTeam:        expertTeam,
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
	estate.CostSummary = normalizeDeepResearchCostSummary(estate.CostSummary)
	if estate.TotalCost < 0 {
		estate.TotalCost = 0
	}
	resourceTotal := deepResearchResourceMonthlyTotal(estate.Resources)
	billingTotal := deepResearchBillingTotal(estate.CostSummary)
	switch {
	case billingTotal > 0 && billingTotal > estate.TotalCost:
		estate.TotalCost = billingTotal
	case estate.TotalCost == 0:
		estate.TotalCost = resourceTotal
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

func normalizeDeepResearchCostSummary(summary *deepResearchCostSummary) *deepResearchCostSummary {
	if summary == nil {
		return nil
	}
	summary.TotalCost = safeDeepResearchCost(summary.TotalCost)
	summary.LastMonthCost = safeDeepResearchCost(summary.LastMonthCost)
	summary.Currency = strings.TrimSpace(summary.Currency)
	summary.LastUpdated = strings.TrimSpace(summary.LastUpdated)
	providers := make([]deepResearchProviderCost, 0, len(summary.ProviderCosts))
	for _, provider := range summary.ProviderCosts {
		provider.Provider = normalizeDeepResearchProvider(provider.Provider)
		if provider.Provider == "" {
			provider.Provider = "unknown"
		}
		provider.TotalCost = safeDeepResearchCost(provider.TotalCost)
		provider.Currency = strings.TrimSpace(provider.Currency)
		provider.ServiceBreakdown = normalizeDeepResearchServiceCosts(provider.ServiceBreakdown)
		if provider.TotalCost == 0 {
			for _, service := range provider.ServiceBreakdown {
				provider.TotalCost += safeDeepResearchCost(service.Cost)
			}
		}
		if provider.TotalCost > 0 || len(provider.ServiceBreakdown) > 0 {
			providers = append(providers, provider)
		}
	}
	summary.ProviderCosts = providers
	summary.TopServices = normalizeDeepResearchServiceCosts(summary.TopServices)
	if summary.TotalCost == 0 {
		for _, provider := range summary.ProviderCosts {
			summary.TotalCost += safeDeepResearchCost(provider.TotalCost)
		}
	}
	if summary.TotalCost == 0 && len(summary.TopServices) > 0 {
		for _, service := range summary.TopServices {
			summary.TotalCost += safeDeepResearchCost(service.Cost)
		}
	}
	return summary
}

func normalizeDeepResearchServiceCosts(services []deepResearchServiceCost) []deepResearchServiceCost {
	normalized := make([]deepResearchServiceCost, 0, len(services))
	for _, service := range services {
		service.Service = strings.TrimSpace(service.Service)
		service.Cost = safeDeepResearchCost(service.Cost)
		if service.UsageQuantity < 0 {
			service.UsageQuantity = 0
		}
		service.UsageUnit = strings.TrimSpace(service.UsageUnit)
		if service.ResourceCount < 0 {
			service.ResourceCount = 0
		}
		if service.Service != "" || service.Cost > 0 {
			normalized = append(normalized, service)
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Cost != normalized[j].Cost {
			return normalized[i].Cost > normalized[j].Cost
		}
		return normalized[i].Service < normalized[j].Service
	})
	return normalized
}

func deepResearchResourceMonthlyTotal(resources []deepResearchResource) float64 {
	total := 0.0
	for _, resource := range resources {
		total += safeDeepResearchCost(resource.MonthlyPrice)
	}
	return total
}

func deepResearchBillingTotal(summary *deepResearchCostSummary) float64 {
	if summary == nil {
		return 0
	}
	if summary.TotalCost > 0 {
		return safeDeepResearchCost(summary.TotalCost)
	}
	total := 0.0
	for _, provider := range summary.ProviderCosts {
		total += safeDeepResearchCost(provider.TotalCost)
	}
	return total
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
				providers := buildDeepResearchProviderRollupFromEstate(estate)
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
				findings := buildCostFindings(estate, buildDeepResearchProviderRollupFromEstate(estate))
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

	verifierPatches, verifierRun := buildDeepResearchVerifierPatches(estate, findings, plan)
	if len(verifierPatches) > 0 {
		findings = applyDeepResearchFindingPatches(findings, verifierPatches)
		runs = append(runs, verifierRun)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Name < runs[j].Name
	})
	return dedupeDeepResearchFindings(findings), runs, uniqueNonEmptyStrings(warnings)
}

func buildDeepResearchVerifierPatches(estate deepResearchEstateSnapshot, findings []deepResearchFinding, plan deepResearchQuestionPlan) ([]deepResearchFindingPatch, deepResearchSubagentRun) {
	if len(findings) == 0 {
		return nil, deepResearchSubagentRun{
			Name:    "finding-verifier",
			Status:  "warning",
			Summary: "No findings were available for verifier review.",
		}
	}

	resourcesByID := make(map[string]deepResearchResource, len(estate.Resources))
	for _, resource := range estate.Resources {
		if strings.TrimSpace(resource.ID) != "" {
			resourcesByID[resource.ID] = resource
		}
	}

	patches := make([]deepResearchFindingPatch, 0, len(findings))
	lensCounts := make(map[string]int)
	missingResource := 0
	for _, finding := range findings {
		patch, foundResource, lens := buildDeepResearchVerifierPatch(estate, finding, resourcesByID, plan)
		if len(patch.EvidenceDetails) == 0 && len(patch.Questions) == 0 {
			continue
		}
		patches = append(patches, patch)
		lensCounts[lens]++
		if !foundResource && strings.TrimSpace(finding.ResourceID) != "" {
			missingResource++
		}
	}

	status := "ok"
	if len(patches) == 0 || missingResource > 0 {
		status = "warning"
	}
	details := []string{
		fmt.Sprintf("Verified %d findings against SRE, security, FinOps, DevOps, and architecture review lenses.", len(patches)),
	}
	if len(lensCounts) > 0 {
		lenses := make([]string, 0, len(lensCounts))
		for lens, count := range lensCounts {
			lenses = append(lenses, fmt.Sprintf("%s: %d", lens, count))
		}
		sort.Strings(lenses)
		details = append(details, "Lens coverage: "+strings.Join(lenses, ", "))
	}
	if missingResource > 0 {
		details = append(details, fmt.Sprintf("%d findings referenced resources that were not present in the estate snapshot.", missingResource))
	}
	if plan.WantsLogs {
		details = append(details, "Verifier emphasized live logs, alerts, latency, traffic, errors, and saturation because the query requested operational evidence.")
	}

	return patches, deepResearchSubagentRun{
		Name:    "finding-verifier",
		Status:  status,
		Summary: fmt.Sprintf("Verifier attached first-principles evidence to %d findings.", len(patches)),
		Details: uniqueNonEmptyStrings(details),
	}
}

func buildDeepResearchVerifierPatch(estate deepResearchEstateSnapshot, finding deepResearchFinding, resourcesByID map[string]deepResearchResource, plan deepResearchQuestionPlan) (deepResearchFindingPatch, bool, string) {
	lens := deepResearchVerifierLens(finding)
	provider := strings.TrimSpace(finding.Provider)
	resource, foundResource := resourcesByID[strings.TrimSpace(finding.ResourceID)]
	if foundResource && provider == "" {
		provider = inferDeepResearchProvider(resource)
	}

	details := make([]deepResearchEvidenceDetail, 0, 8)
	addDetail := func(section string, detail string) {
		detail = strings.TrimSpace(detail)
		if detail == "" {
			return
		}
		details = append(details, deepResearchEvidenceDetail{
			Detail:   detail,
			Source:   "verifier",
			Provider: provider,
			Section:  section,
		})
	}

	addDetail("Review lens", deepResearchVerifierLensDetail(lens, finding))
	if foundResource {
		inbound, outbound := deepResearchResourceDependencyCounts(resource, estate.Resources)
		trafficSignals := deepResearchTrafficSignalLabels(resource)
		ownership := "missing"
		if deepResearchHasOwnershipTags(resource.Tags) {
			ownership = "present"
		}
		addDetail("Resource facts", fmt.Sprintf("%s is %s in %s/%s, state=%s, cost=$%.2f/mo, dependencies=%d inbound/%d outbound, ownership tags=%s.",
			deepResearchResourceLabel(resource),
			deepResearchNonEmpty(resource.Type, "unknown type"),
			deepResearchNonEmpty(provider, "unknown provider"),
			deepResearchNonEmpty(resource.Region, "unknown region"),
			deepResearchNonEmpty(resource.State, "unknown"),
			resource.MonthlyPrice,
			inbound,
			outbound,
			ownership,
		))
		if len(trafficSignals) > 0 {
			addDetail("SRE signals", fmt.Sprintf("Visible golden-signal inputs: %s.", strings.Join(trafficSignals, "; ")))
		} else if finding.Category == "cost" || finding.Category == "bottleneck" || finding.Category == "resilience" {
			addDetail("SRE gap", "Latency, traffic, error-rate, and saturation metrics are not attached to this resource, so scaling and rightsizing need another validation pass.")
		}
		if metric, value, ok := deepResearchPrimaryTrafficMetric(resource); ok {
			addDetail("Unit economics", fmt.Sprintf("Primary demand metric candidate for cost/performance review: %s=%.2f.", metric, value))
		}
		if !deepResearchHasOwnershipTags(resource.Tags) {
			addDetail("Ownership gap", "Owner, service, team, project, or environment tags are missing, which weakens repo-to-infra accountability and cleanup approval.")
		}
		addDetail("Security posture", deepResearchSecurityVerifierDetail(resource))
		addDetail("Delivery posture", deepResearchDeliveryVerifierDetail(resource))
	} else {
		addDetail("Scope", "Verifier treated this as an estate-level issue because no single resource record was attached.")
		if len(estate.Resources) > 0 {
			addDetail("Estate facts", fmt.Sprintf("Estate context includes %d resources across %d regions and %d provider groups.",
				len(estate.Resources),
				len(deepResearchRegions(estate.Resources)),
				len(buildDeepResearchProviderRollupFromEstate(estate)),
			))
		}
	}
	addDetail("Required proof", deepResearchVerifierRequiredProof(finding, plan))

	return deepResearchFindingPatch{
		FindingID:       finding.ID,
		Evidence:        deepResearchEvidenceStrings(details),
		EvidenceDetails: deepResearchLimitEvidenceDetails(uniqueDeepResearchEvidenceDetails(details), 7),
		Questions:       buildDeepResearchVerifierQuestions(finding, foundResource, resource),
		ScoreDelta:      deepResearchVerifierScoreDelta(finding, foundResource),
	}, foundResource, lens
}

func deepResearchVerifierLens(finding deepResearchFinding) string {
	switch strings.ToLower(strings.TrimSpace(finding.Category)) {
	case "cost":
		return "FinOps"
	case "misconfiguration":
		return "Security"
	case "resilience":
		return "Reliability"
	case "bottleneck":
		return "SRE"
	case "hygiene":
		return "DevOps"
	default:
		return "Architecture"
	}
}

func deepResearchVerifierLensDetail(lens string, finding deepResearchFinding) string {
	switch lens {
	case "FinOps":
		return "FinOps lens: tie spend to owner, service, demand metric, utilization, and rollback criteria before rightsizing."
	case "Security":
		return "Security lens: validate threat boundary, public exposure, identity permissions, encryption, backup posture, and least-privilege controls."
	case "Reliability":
		return "Reliability lens: prove blast radius, failover, backup/restore, degraded-state impact, and recovery objective before accepting risk."
	case "SRE":
		return "SRE lens: evaluate latency, traffic, errors, saturation, dependency fan-in, and capacity ceiling before recommending scale changes."
	case "DevOps":
		return "DevOps lens: confirm owner, repo/deploy source, recent activity, runbook, and rollback path before cleanup or migration."
	default:
		return "Architecture lens: map provider, region, dependency path, shared state, bottleneck, and future growth risk before changing the system."
	}
}

func deepResearchVerifierRequiredProof(finding deepResearchFinding, plan deepResearchQuestionPlan) string {
	switch strings.ToLower(strings.TrimSpace(finding.Category)) {
	case "cost":
		return "Required proof: show the cost driver, traffic or utilization denominator, owner approval, expected unit-cost impact, and rollback plan."
	case "misconfiguration":
		return "Required proof: show the exposed control, affected data or identity boundary, compensating control, remediation command/change, and validation step."
	case "resilience":
		return "Required proof: show current redundancy, backup/restore path, failover behavior, alert coverage, and user-facing blast radius."
	case "bottleneck":
		return "Required proof: show request path, fan-in/fan-out, latency, error-rate, saturation, and projected growth before scaling or decomposing."
	case "hygiene":
		return "Required proof: show last activity, owner, dependencies, access path, cost/security impact, and a reversible retirement plan."
	default:
		if plan.WantsTopology {
			return "Required proof: show dependency path, owner, provider data, traffic evidence, failure mode, and the exact next architecture change."
		}
		return "Required proof: show resource facts, owner, dependency path, operational impact, and a validation step for the recommendation."
	}
}

func deepResearchSecurityVerifierDetail(resource deepResearchResource) string {
	signals := make([]string, 0, 4)
	if deepResearchHasExternalAddress(resource) {
		signals = append(signals, "public network address present")
	}
	if encrypted, ok := deepResearchBoolAttr(resource.Attributes, "storageEncrypted"); ok {
		if encrypted {
			signals = append(signals, "storageEncrypted=true")
		} else {
			signals = append(signals, "storageEncrypted=false")
		}
	}
	if backupRetention, ok := deepResearchIntAttr(resource.Attributes, "backupRetentionPeriod"); ok {
		signals = append(signals, fmt.Sprintf("backupRetentionPeriod=%d", backupRetention))
	}
	if len(resource.IAMPolicies) > 0 {
		signals = append(signals, fmt.Sprintf("%d IAM policies attached", len(resource.IAMPolicies)))
	}
	if len(signals) == 0 {
		return "No explicit public exposure, encryption, backup, or IAM policy fields were present in the snapshot for this resource."
	}
	return "Security-relevant fields: " + strings.Join(signals, "; ") + "."
}

func deepResearchDeliveryVerifierDetail(resource deepResearchResource) string {
	signals := make([]string, 0, 4)
	for _, key := range []string{"repository", "repo", "githubRepo", "repoFullName", "sourceRepo", "gitRepository"} {
		if value := deepResearchFirstNonEmptyAttr(resource.Attributes, key); value != "" {
			signals = append(signals, fmt.Sprintf("%s=%s", key, value))
			break
		}
	}
	for _, key := range []string{"deployment", "deploymentId", "release", "branch", "commit", "commitSha", "gitCommit", "deploymentCommit"} {
		if value := deepResearchFirstNonEmptyAttr(resource.Attributes, key); value != "" {
			signals = append(signals, fmt.Sprintf("%s=%s", key, value))
			if len(signals) >= 4 {
				break
			}
		}
	}
	if len(signals) == 0 {
		return "No repo, deployment, branch, or commit signal was visible, so change ownership and rollback readiness remain unverified."
	}
	return "Delivery linkage fields: " + strings.Join(signals, "; ") + "."
}

func buildDeepResearchVerifierQuestions(finding deepResearchFinding, foundResource bool, resource deepResearchResource) []string {
	label := deepResearchResourceLabelFromFinding(finding)
	if foundResource {
		label = deepResearchResourceLabel(resource)
	}
	questions := []string{
		fmt.Sprintf("What exact evidence proves the recommendation for %s is safe to apply?", label),
	}
	switch strings.ToLower(strings.TrimSpace(finding.Category)) {
	case "cost":
		questions = append(questions,
			fmt.Sprintf("What traffic, utilization, or unit metric should gate any cost change on %s?", label),
			fmt.Sprintf("Who owns %s and what rollback would restore capacity if demand spikes?", label),
		)
	case "misconfiguration":
		questions = append(questions,
			fmt.Sprintf("What boundary, data class, or identity path is exposed by %s?", label),
			fmt.Sprintf("How do I verify %s is hardened after the change?", label),
		)
	case "resilience":
		questions = append(questions,
			fmt.Sprintf("What is the user-facing blast radius if %s fails?", label),
			fmt.Sprintf("Which restore, failover, or rollback test should run for %s?", label),
		)
	case "bottleneck":
		questions = append(questions,
			fmt.Sprintf("Which requests flow through %s and what are their latency/error/saturation limits?", label),
			fmt.Sprintf("What future traffic level makes %s a scaling incident?", label),
		)
	case "hygiene":
		questions = append(questions,
			fmt.Sprintf("What last-activity signal proves %s is unused?", label),
			fmt.Sprintf("Which dependency or access path would break if %s is retired?", label),
		)
	default:
		questions = append(questions,
			fmt.Sprintf("Which architecture path, owner, and provider signal explain %s?", label),
			fmt.Sprintf("What future scaling or operational problem does %s create if left unchanged?", label),
		)
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(questions), 4)
}

func deepResearchVerifierScoreDelta(finding deepResearchFinding, foundResource bool) float64 {
	delta := 5.0
	if foundResource {
		delta += 4
	}
	switch strings.ToLower(strings.TrimSpace(finding.Severity)) {
	case "critical":
		delta += 10
	case "high":
		delta += 7
	case "medium":
		delta += 4
	}
	switch strings.ToLower(strings.TrimSpace(finding.Category)) {
	case "misconfiguration", "resilience", "bottleneck":
		delta += 3
	}
	return delta
}

func deepResearchResourceDependencyCounts(resource deepResearchResource, resources []deepResearchResource) (int, int) {
	outboundSeen := make(map[string]struct{})
	for _, target := range resource.Connections {
		target = strings.TrimSpace(target)
		if target != "" {
			outboundSeen[target] = struct{}{}
		}
	}
	for _, connection := range resource.TypedConnections {
		target := strings.TrimSpace(connection.TargetID)
		if target != "" {
			outboundSeen[target] = struct{}{}
		}
	}

	inboundSeen := make(map[string]struct{})
	for _, candidate := range resources {
		if strings.TrimSpace(candidate.ID) == "" || candidate.ID == resource.ID {
			continue
		}
		targets := make(map[string]struct{})
		for _, target := range candidate.Connections {
			target = strings.TrimSpace(target)
			if target != "" {
				targets[target] = struct{}{}
			}
		}
		for _, connection := range candidate.TypedConnections {
			target := strings.TrimSpace(connection.TargetID)
			if target != "" {
				targets[target] = struct{}{}
			}
		}
		if _, ok := targets[resource.ID]; ok {
			inboundSeen[candidate.ID] = struct{}{}
		}
	}
	return len(inboundSeen), len(outboundSeen)
}

func uniqueDeepResearchEvidenceDetails(details []deepResearchEvidenceDetail) []deepResearchEvidenceDetail {
	seen := make(map[string]struct{}, len(details))
	unique := make([]deepResearchEvidenceDetail, 0, len(details))
	for _, detail := range details {
		detail.Detail = strings.TrimSpace(detail.Detail)
		detail.Source = strings.TrimSpace(detail.Source)
		detail.Provider = strings.TrimSpace(detail.Provider)
		detail.Section = strings.TrimSpace(detail.Section)
		if detail.Detail == "" {
			continue
		}
		key := strings.Join([]string{strings.ToLower(detail.Source), strings.ToLower(detail.Provider), strings.ToLower(detail.Section), detail.Detail}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, detail)
	}
	return unique
}

func deepResearchLimitEvidenceDetails(details []deepResearchEvidenceDetail, maxCount int) []deepResearchEvidenceDetail {
	if maxCount <= 0 || len(details) <= maxCount {
		return details
	}
	return append([]deepResearchEvidenceDetail(nil), details[:maxCount]...)
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
	providerSet := buildDeepResearchProviderSet(estate)
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
	providerSet := buildDeepResearchProviderSet(estate)

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

func buildDeepResearchProviderSet(estate deepResearchEstateSnapshot) map[string]struct{} {
	providerSet := make(map[string]struct{})
	for _, resource := range estate.Resources {
		if provider := inferDeepResearchProvider(resource); provider != "" {
			providerSet[provider] = struct{}{}
		}
		if isDeepResearchKubernetesType(resource.Type) {
			providerSet["k8s"] = struct{}{}
		}
	}
	for _, provider := range deepResearchBillingProviderNames(estate) {
		providerSet[provider] = struct{}{}
	}
	return providerSet
}

func deepResearchBillingProviderNames(estate deepResearchEstateSnapshot) []string {
	if estate.CostSummary == nil {
		return nil
	}
	providers := make([]string, 0, len(estate.CostSummary.ProviderCosts))
	seen := make(map[string]struct{})
	for _, providerCost := range estate.CostSummary.ProviderCosts {
		provider := normalizeDeepResearchProvider(providerCost.Provider)
		if provider == "" {
			provider = "unknown"
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
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
	if !digitalocean.CanUseLiveContext(ctx) {
		return deepResearchSubagentRun{Name: "digitalocean-scout", Status: "warning", Summary: "DigitalOcean scout skipped: no API token or authenticated doctl context is configured."}
	}
	apiToken := strings.TrimSpace(digitalocean.ResolveAPIToken())
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
		return digitalocean.CanUseLiveContext(context.Background())
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
		if !digitalocean.CanUseLiveContext(ctx) {
			return deepResearchProviderContext{}, fmt.Errorf("no API token or authenticated doctl context is configured")
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
	terms = append(terms, deepResearchFirstPrinciplesPromptTerms(plan, targets)...)
	for _, finding := range targets {
		terms = append(terms, deepResearchProviderFocusTerms(provider, finding, plan)...)
	}
	return uniqueNonEmptyStrings(terms)
}

func deepResearchFirstPrinciplesPromptTerms(plan deepResearchQuestionPlan, targets []deepResearchFinding) []string {
	terms := []string{
		"systems architect",
		"cloud architect",
		"first principles",
		"verify evidence",
		"critical bugs",
		"misconfiguration",
		"future scaling risk",
		"sre golden signals latency traffic errors saturation",
		"blast radius failover backup restore rollback",
		"security threat model public exposure identity permissions encryption secrets data protection least privilege",
		"finops unit economics owner allocation utilization rightsizing idle waste anomaly budget",
		"devops ownership deployment frequency lead time change failure rate time to restore runbook rollback",
		"software architecture dependencies critical path queue cache database rate limit capacity ceiling",
	}
	if plan.WantsCICD {
		terms = append(terms, "ci cd pipeline deploy provenance release health")
	}
	if plan.WantsDatabase {
		terms = append(terms, "database durability backups encryption private networking connection saturation")
	}
	if plan.WantsNetwork {
		terms = append(terms, "ingress egress dns load balancer firewall private network")
	}
	if plan.WantsKubernetes {
		terms = append(terms, "kubernetes requests limits hpa restarts events node saturation")
	}
	if plan.WantsTerraform {
		terms = append(terms, "terraform drift plan state ownership lifecycle")
	}
	for _, finding := range targets {
		switch strings.ToLower(strings.TrimSpace(finding.Category)) {
		case "cost":
			terms = append(terms, "cost driver demand denominator unit metric utilization commitment savings risk")
		case "misconfiguration":
			terms = append(terms, "attack surface exposure encryption backup iam firewall secrets compliance")
		case "resilience":
			terms = append(terms, "single point of failure redundancy restore objective failover incident response")
		case "bottleneck":
			terms = append(terms, "fan in fan out throughput saturation latency critical path scale ceiling")
		case "hygiene":
			terms = append(terms, "stale unused ownership last activity dependency cleanup reversible retirement")
		}
	}
	return terms
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

func buildCostFindings(estate deepResearchEstateSnapshot, providerRolls []deepResearchProviderRoll) []deepResearchFinding {
	resources := estate.Resources
	totalCost := estate.TotalCost
	findings := make([]deepResearchFinding, 0, 8)
	if len(resources) == 0 && estate.CostSummary == nil {
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

	findings = append(findings, buildDeepResearchBillingFindings(estate, providerRolls)...)

	return findings
}

func buildDeepResearchBillingFindings(estate deepResearchEstateSnapshot, providerRolls []deepResearchProviderRoll) []deepResearchFinding {
	if estate.CostSummary == nil {
		return nil
	}
	findings := make([]deepResearchFinding, 0, 4)
	resourceTotal := deepResearchResourceMonthlyTotal(estate.Resources)
	billingTotal := deepResearchBillingTotal(estate.CostSummary)
	if billingTotal > 0 && resourceTotal > 0 && billingTotal > resourceTotal*1.15 {
		delta := billingTotal - resourceTotal
		findings = append(findings, deepResearchFinding{
			ID:          buildDeepResearchFindingID("billing-coverage-gap", "resource-prices"),
			Severity:    "high",
			Category:    "cost",
			Title:       "Billing telemetry is higher than resource inventory pricing",
			Summary:     fmt.Sprintf("The billing view reports $%.2f/mo while scanned resources add up to $%.2f/mo. Treat billing as authoritative, then use the delta to find unpriced services, usage fees, marketplace spend, support, taxes, or missing per-resource prices.", billingTotal, resourceTotal),
			MonthlyCost: delta,
			Risk:        "Inventory-only pricing can understate the real operating cost and mis-rank optimization work.",
			Score:       delta + 180,
			Evidence: []string{
				fmt.Sprintf("Billing total: $%.2f/mo", billingTotal),
				fmt.Sprintf("Resource inventory total: $%.2f/mo", resourceTotal),
				fmt.Sprintf("Unallocated billing delta: $%.2f/mo", delta),
			},
			Questions: []string{
				"Which provider services explain the gap between billing and resource inventory?",
				"Which resources or usage dimensions are missing monthly prices in the scan?",
			},
		})
	}

	for _, provider := range providerRolls {
		if provider.MonthlyCost < 50 {
			continue
		}
		resourceProviderTotal := 0.0
		for _, resource := range estate.Resources {
			if inferDeepResearchProvider(resource) == provider.Provider {
				resourceProviderTotal += safeDeepResearchCost(resource.MonthlyPrice)
			}
		}
		if resourceProviderTotal == 0 || provider.MonthlyCost <= resourceProviderTotal*1.25 {
			continue
		}
		delta := provider.MonthlyCost - resourceProviderTotal
		findings = append(findings, deepResearchFinding{
			ID:          buildDeepResearchFindingID("provider-billing-gap", provider.Provider),
			Severity:    "medium",
			Category:    "cost",
			Title:       fmt.Sprintf("%s billing is not fully tied to priced resources", deepResearchProviderDisplayName(provider.Provider)),
			Summary:     fmt.Sprintf("%s billing is $%.2f/mo while priced resources from that provider add up to $%.2f/mo. Attach billing dimensions and service costs to resources before treating the visible inventory as complete.", deepResearchProviderDisplayName(provider.Provider), provider.MonthlyCost, resourceProviderTotal),
			Provider:    provider.Provider,
			MonthlyCost: delta,
			Risk:        "Provider spend can be hidden in usage, managed services, support, or resources without pricing metadata.",
			Score:       delta + provider.ShareOfCost,
			Evidence: []string{
				fmt.Sprintf("%s billing total: $%.2f/mo", deepResearchProviderDisplayName(provider.Provider), provider.MonthlyCost),
				fmt.Sprintf("%s priced resource total: $%.2f/mo", deepResearchProviderDisplayName(provider.Provider), resourceProviderTotal),
			},
			Questions: []string{
				fmt.Sprintf("Which %s services or usage dimensions are not mapped to resources?", deepResearchProviderDisplayName(provider.Provider)),
				"Which tags or provider APIs can connect billing line items back to owners and systems?",
			},
		})
	}

	for _, service := range deepResearchTopBillingServices(estate.CostSummary, 3) {
		if service.provider == "" || service.cost < 50 {
			continue
		}
		findings = append(findings, deepResearchFinding{
			ID:          buildDeepResearchFindingID("billing-service-driver", service.provider, service.service),
			Severity:    deepResearchSeverityForCost(service.cost),
			Category:    "cost",
			Title:       fmt.Sprintf("%s spend is concentrated in %s", deepResearchProviderDisplayName(service.provider), service.service),
			Summary:     fmt.Sprintf("Billing telemetry shows $%.2f/mo in %s for %s. Review the service owner, usage driver, traffic relationship, and resource mapping before making capacity changes.", service.cost, service.service, deepResearchProviderDisplayName(service.provider)),
			Provider:    service.provider,
			MonthlyCost: service.cost,
			Risk:        "Service-level cost concentration can hide optimization targets that are not visible as individual resource prices.",
			Score:       service.cost + 80,
			Evidence: []string{
				fmt.Sprintf("Billing service: %s", service.service),
				fmt.Sprintf("Service cost: $%.2f/mo", service.cost),
			},
			Questions: []string{
				fmt.Sprintf("Which systems or resources are generating %s spend?", service.service),
				"Is this service spend tied to user traffic, background work, storage growth, or idle capacity?",
			},
		})
	}
	return findings
}

type deepResearchBillingService struct {
	provider string
	service  string
	cost     float64
}

func deepResearchTopBillingServices(summary *deepResearchCostSummary, maxCount int) []deepResearchBillingService {
	if summary == nil || maxCount <= 0 {
		return nil
	}
	services := make([]deepResearchBillingService, 0)
	for _, provider := range summary.ProviderCosts {
		for _, service := range provider.ServiceBreakdown {
			name := strings.TrimSpace(service.Service)
			if name == "" || service.Cost <= 0 {
				continue
			}
			services = append(services, deepResearchBillingService{provider: provider.Provider, service: name, cost: safeDeepResearchCost(service.Cost)})
		}
	}
	if len(services) == 0 {
		for _, service := range summary.TopServices {
			name := strings.TrimSpace(service.Service)
			if name == "" || service.Cost <= 0 {
				continue
			}
			services = append(services, deepResearchBillingService{provider: "unknown", service: name, cost: safeDeepResearchCost(service.Cost)})
		}
	}
	sort.SliceStable(services, func(i, j int) bool {
		if services[i].cost != services[j].cost {
			return services[i].cost > services[j].cost
		}
		if services[i].provider != services[j].provider {
			return services[i].provider < services[j].provider
		}
		return services[i].service < services[j].service
	})
	if len(services) > maxCount {
		return append([]deepResearchBillingService(nil), services[:maxCount]...)
	}
	return append([]deepResearchBillingService(nil), services...)
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

func buildDeepResearchProviderRollupFromEstate(estate deepResearchEstateSnapshot) []deepResearchProviderRoll {
	resourceRolls := buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost)
	if estate.CostSummary == nil || len(estate.CostSummary.ProviderCosts) == 0 {
		return resourceRolls
	}

	byProvider := make(map[string]*deepResearchProviderRoll)
	for _, roll := range resourceRolls {
		current := roll
		byProvider[current.Provider] = &current
	}
	for _, providerCost := range estate.CostSummary.ProviderCosts {
		provider := normalizeDeepResearchProvider(providerCost.Provider)
		if provider == "" {
			provider = "unknown"
		}
		roll, ok := byProvider[provider]
		if !ok {
			roll = &deepResearchProviderRoll{Provider: provider}
			byProvider[provider] = roll
		}
		roll.MonthlyCost = safeDeepResearchCost(providerCost.TotalCost)
		if roll.ResourceCount == 0 {
			for _, service := range providerCost.ServiceBreakdown {
				roll.ResourceCount += service.ResourceCount
			}
		}
	}
	totalCost := estate.TotalCost
	if billingTotal := deepResearchBillingTotal(estate.CostSummary); billingTotal > totalCost {
		totalCost = billingTotal
	}
	if totalCost == 0 {
		for _, roll := range byProvider {
			totalCost += safeDeepResearchCost(roll.MonthlyCost)
		}
	}

	providers := make([]deepResearchProviderRoll, 0, len(byProvider))
	for _, roll := range byProvider {
		if totalCost > 0 {
			roll.ShareOfCost = (safeDeepResearchCost(roll.MonthlyCost) / totalCost) * 100
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

func buildDeepResearchSystemImprovement(estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, plan deepResearchQuestionPlan) *deepResearchSystemImprovement {
	traffic := buildDeepResearchTrafficAssessment(estate.Resources, findings, plan)
	codebase := buildDeepResearchCodebaseAssessment(estate.Resources)
	architecture := buildDeepResearchArchitectureAssessment(estate, findings, providers)
	recommendations := buildDeepResearchSystemRecommendations(estate, findings, providers, traffic, codebase, architecture)

	overviewParts := []string{
		fmt.Sprintf("Reviewed %d resources", len(estate.Resources)),
		fmt.Sprintf("%d provider groups", len(providers)),
		fmt.Sprintf("%d regions", len(deepResearchRegions(estate.Resources))),
	}
	if len(recommendations) > 0 {
		overviewParts = append(overviewParts, fmt.Sprintf("%d improvement tracks", len(recommendations)))
	}

	return &deepResearchSystemImprovement{
		Overview:        strings.Join(overviewParts, ", "),
		Traffic:         traffic,
		Codebase:        codebase,
		Architecture:    architecture,
		Recommendations: recommendations,
	}
}

func buildDeepResearchTrafficAssessment(resources []deepResearchResource, findings []deepResearchFinding, plan deepResearchQuestionPlan) deepResearchSystemImprovementBlock {
	edges, typedEdges, entrypoints := deepResearchTopologyCounts(resources)
	allTrafficSignals := collectDeepResearchTrafficSignals(resources, 0)
	trafficSignals := deepResearchLimitStrings(allTrafficSignals, 8)
	hotspots := collectDeepResearchTrafficHotspots(resources, 6)
	allCostTraffic := collectDeepResearchCostTrafficSignals(resources, 0)
	costTraffic := deepResearchLimitStrings(allCostTraffic, 6)
	bottlenecks := deepResearchFindingsByCategory(findings, "bottleneck")
	trafficResourceCount := 0
	for _, resource := range resources {
		if len(deepResearchTrafficSignalLabels(resource)) > 0 {
			trafficResourceCount++
		}
	}

	evidence := []string{
		fmt.Sprintf("%d resources expose %d traffic, latency, error, saturation, throughput, or bandwidth fields.", trafficResourceCount, len(allTrafficSignals)),
		fmt.Sprintf("Topology model contains %d dependency edges, including %d typed edges.", edges, typedEdges),
		fmt.Sprintf("Detected %d likely ingress, edge, or entrypoint resources.", entrypoints),
		fmt.Sprintf("%d topology bottleneck findings are ranked in the current report.", len(bottlenecks)),
	}
	if len(allCostTraffic) > 0 {
		evidence = append(evidence, fmt.Sprintf("%d resources have both cost and demand metrics for unit-economics review.", len(allCostTraffic)))
	}
	evidence = append(evidence, trafficSignals...)
	evidence = append(evidence, hotspots...)
	evidence = append(evidence, costTraffic...)
	evidence = uniqueNonEmptyStrings(evidence)

	gaps := make([]string, 0, 3)
	status := "ok"
	if len(allTrafficSignals) == 0 {
		status = "gap"
		gaps = append(gaps, "No direct request, latency, throughput, error-rate, or bandwidth metrics were present in the estate snapshot.")
	}
	if typedEdges == 0 && edges > 0 {
		gaps = append(gaps, "Connections exist, but typed connection labels are missing, which weakens traffic-path analysis.")
	}
	if entrypoints == 0 {
		gaps = append(gaps, "No clear edge or ingress resources were identified, so production traffic entrypoints should be marked explicitly.")
	}
	if len(bottlenecks) > 0 && status == "ok" {
		status = "warning"
	}

	summary := fmt.Sprintf("Traffic review found %d measured signal fields across %d resources, %d entrypoints, and %d topology hotspot candidates.", len(allTrafficSignals), trafficResourceCount, entrypoints, len(hotspots))
	if len(allTrafficSignals) == 0 {
		summary = "Traffic analysis is mostly inferential because the snapshot does not include live request or latency metrics."
	} else if len(bottlenecks) > 0 {
		summary = fmt.Sprintf("Traffic review found %d measured signal fields, %d topology bottleneck candidates, and %d entrypoints.", len(allTrafficSignals), len(bottlenecks), entrypoints)
	} else if len(costTraffic) > 0 {
		summary = fmt.Sprintf("Traffic review found %d measured signal fields and %d cost-versus-traffic correlation candidates.", len(allTrafficSignals), len(allCostTraffic))
	}
	if plan.WantsLogs {
		summary += " Recent logs and alerts were prioritized for live provider drilldowns."
	}

	return deepResearchSystemImprovementBlock{
		Status:   status,
		Summary:  summary,
		Evidence: deepResearchLimitStrings(evidence, 12),
		Gaps:     uniqueNonEmptyStrings(gaps),
	}
}

func buildDeepResearchCodebaseAssessment(resources []deepResearchResource) deepResearchSystemImprovementBlock {
	trackedRepos := loadDeepResearchTrackedRepos()
	allCodeSignals := collectDeepResearchCodebaseSignals(resources, 0)
	codeSignals := deepResearchLimitStrings(allCodeSignals, 8)
	allDeploySignals := collectDeepResearchDeploySignals(resources, 0)
	deploySignals := deepResearchLimitStrings(allDeploySignals, 6)
	untagged := 0
	owned := 0
	for _, resource := range resources {
		if !deepResearchHasOwnershipTags(resource.Tags) {
			untagged++
		} else {
			owned++
		}
	}

	evidence := make([]string, 0, 12)
	evidence = append(evidence, fmt.Sprintf("Ownership coverage: %d/%d resources expose owner, team, service, project, environment, or env tags.", owned, len(resources)))
	evidence = append(evidence, fmt.Sprintf("Code visibility: %d tracked repos, %d repo-linked resources, and %d deploy/runtime signals.", len(trackedRepos), len(allCodeSignals), len(allDeploySignals)))
	if len(trackedRepos) > 0 {
		for _, repo := range deepResearchLimitStrings(trackedRepos, 4) {
			evidence = append(evidence, "Tracked repo: "+repo)
		}
	}
	evidence = append(evidence, codeSignals...)
	evidence = append(evidence, deploySignals...)
	if untagged > 0 {
		evidence = append(evidence, fmt.Sprintf("%d resources do not expose owner or environment tags.", untagged))
	}

	gaps := make([]string, 0, 3)
	status := "ok"
	if len(trackedRepos) == 0 && len(allCodeSignals) == 0 {
		status = "gap"
		gaps = append(gaps, "No tracked repositories or repository-linked resources were visible to this run.")
	}
	if len(allDeploySignals) == 0 {
		gaps = append(gaps, "No deployment pipeline, build, or release resources were visible in the snapshot.")
	}
	if untagged > len(resources)/3 && len(resources) > 0 {
		gaps = append(gaps, "Ownership tags are sparse enough that code-to-infra accountability will be weak.")
		if status == "ok" {
			status = "warning"
		}
	}

	summary := fmt.Sprintf("Codebase review mapped %d tracked repos, %d repo-linked resources, %d deploy/runtime signals, and %d unowned resources.", len(trackedRepos), len(allCodeSignals), len(allDeploySignals), untagged)
	if status == "gap" {
		summary = "Codebase understanding is limited because no repo or deployment-system context was available in this run."
	} else if len(trackedRepos) > 0 {
		summary = fmt.Sprintf("Codebase review includes %d tracked repos plus %d resource-level code or deploy signals; %d resources still lack ownership tags.", len(trackedRepos), len(allCodeSignals)+len(allDeploySignals), untagged)
	}

	return deepResearchSystemImprovementBlock{
		Status:   status,
		Summary:  summary,
		Evidence: deepResearchLimitStrings(uniqueNonEmptyStrings(evidence), 12),
		Gaps:     uniqueNonEmptyStrings(gaps),
	}
}

func buildDeepResearchArchitectureAssessment(estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll) deepResearchSystemImprovementBlock {
	edges, typedEdges, entrypoints := deepResearchTopologyCounts(estate.Resources)
	regions := deepResearchRegions(estate.Resources)
	criticalPaths := collectDeepResearchCriticalPaths(estate.Resources, 4)
	resilienceFindings := deepResearchFindingsByCategory(findings, "resilience")
	misconfigFindings := deepResearchFindingsByCategory(findings, "misconfiguration")
	costFindings := deepResearchFindingsByCategory(findings, "cost")
	bottleneckFindings := deepResearchFindingsByCategory(findings, "bottleneck")
	hygieneFindings := deepResearchFindingsByCategory(findings, "hygiene")

	evidence := []string{
		fmt.Sprintf("%d resources across %d regions and %d provider groups.", len(estate.Resources), len(regions), len(providers)),
		fmt.Sprintf("%d dependency edges, %d typed edges, %d likely entrypoints.", edges, typedEdges, entrypoints),
		fmt.Sprintf("Finding mix: %d cost, %d bottleneck, %d resilience, %d misconfiguration, and %d hygiene findings.", len(costFindings), len(bottleneckFindings), len(resilienceFindings), len(misconfigFindings), len(hygieneFindings)),
	}
	if len(providers) > 0 {
		evidence = append(evidence, fmt.Sprintf("%s is the largest observed provider with %d resources, $%.2f/mo, and %.1f%% of observed spend.", strings.ToUpper(providers[0].Provider), providers[0].ResourceCount, providers[0].MonthlyCost, providers[0].ShareOfCost))
	}
	evidence = append(evidence, criticalPaths...)
	if len(resilienceFindings) > 0 {
		evidence = append(evidence, fmt.Sprintf("%d resilience findings indicate single points, degraded state, or limited redundancy.", len(resilienceFindings)))
	}
	if len(misconfigFindings) > 0 {
		evidence = append(evidence, fmt.Sprintf("%d misconfiguration findings affect architecture hardening.", len(misconfigFindings)))
	}
	if len(costFindings) > 0 {
		evidence = append(evidence, fmt.Sprintf("%d cost findings should shape the architecture review order.", len(costFindings)))
	}

	gaps := make([]string, 0, 4)
	status := "ok"
	if edges == 0 {
		status = "gap"
		gaps = append(gaps, "No dependency graph edges were present, so architecture analysis cannot distinguish active paths from isolated inventory.")
	}
	if typedEdges == 0 && edges > 0 {
		gaps = append(gaps, "Dependency edges are untyped, so request, data, auth, and deploy relationships cannot be separated yet.")
	}
	if len(criticalPaths) == 0 && entrypoints > 0 {
		gaps = append(gaps, "Entrypoints exist, but no multi-hop service or data paths could be reconstructed from the graph.")
	}
	if len(regions) >= 5 {
		if status == "ok" {
			status = "warning"
		}
		gaps = append(gaps, "Region spread is large enough to deserve a latency, egress, and failover review.")
	}
	if len(resilienceFindings) > 0 && status == "ok" {
		status = "warning"
	}

	summary := fmt.Sprintf("Architecture review mapped %d resources, %d regions, %d provider groups, %d typed dependency edges, and %d likely entrypoints.", len(estate.Resources), len(regions), len(providers), typedEdges, entrypoints)
	if status == "gap" {
		summary = "Architecture review is inventory-heavy because dependency relationships are missing from the snapshot."
	} else if len(resilienceFindings) > 0 || len(costFindings) > 0 {
		summary = fmt.Sprintf("Architecture review found %d resilience, %d bottleneck, %d security/configuration, and %d cost signals that should drive the roadmap.", len(resilienceFindings), len(bottleneckFindings), len(misconfigFindings), len(costFindings))
	}

	return deepResearchSystemImprovementBlock{
		Status:   status,
		Summary:  summary,
		Evidence: deepResearchLimitStrings(uniqueNonEmptyStrings(evidence), 12),
		Gaps:     uniqueNonEmptyStrings(gaps),
	}
}

func buildDeepResearchSystemRecommendations(estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, traffic deepResearchSystemImprovementBlock, codebase deepResearchSystemImprovementBlock, architecture deepResearchSystemImprovementBlock) []deepResearchSystemRecommendation {
	recommendations := make([]deepResearchSystemRecommendation, 0, maxDeepResearchSystemRecs)
	appendRecommendation := func(rec deepResearchSystemRecommendation) {
		if strings.TrimSpace(rec.ID) == "" || strings.TrimSpace(rec.Title) == "" {
			return
		}
		for _, existing := range recommendations {
			if existing.ID == rec.ID {
				return
			}
		}
		rec.Evidence = deepResearchLimitStrings(uniqueNonEmptyStrings(rec.Evidence), 4)
		rec.Actions = deepResearchLimitStrings(uniqueNonEmptyStrings(rec.Actions), 4)
		recommendations = append(recommendations, rec)
	}

	bottlenecks := deepResearchFindingsByCategory(findings, "bottleneck")
	resilience := deepResearchFindingsByCategory(findings, "resilience")
	costs := deepResearchFindingsByCategory(findings, "cost")
	misconfigs := deepResearchFindingsByCategory(findings, "misconfiguration")
	trafficGap := traffic.Status == "gap"
	codebaseGap := codebase.Status == "gap"
	architectureGap := architecture.Status == "gap"

	if trafficGap || len(bottlenecks) > 0 {
		priority := "high"
		if !trafficGap && len(bottlenecks) == 0 {
			priority = "medium"
		}
		appendRecommendation(deepResearchSystemRecommendation{
			ID:       "traffic-observability",
			Priority: priority,
			Title:    "Make traffic and saturation measurable",
			Summary:  "Instrument request volume, latency percentiles, errors, queue depth, and capacity pressure on every ingress and concentration point before the next optimization pass.",
			Impact:   "Turns inferred bottlenecks into measured targets and prevents cost changes from hurting production paths.",
			Effort:   "medium",
			Evidence: append(traffic.Evidence, traffic.Gaps...),
			Actions: []string{
				"Attach request-rate, p95/p99 latency, error-rate, and saturation metrics to edge, gateway, queue, database, and cache resources.",
				"Mark production entrypoints explicitly in resource attributes or tags.",
				"Run the next deep research pass after metrics are visible so traffic-heavy resources outrank idle inventory.",
			},
		})
	}

	if codebaseGap || len(codebase.Gaps) > 0 {
		appendRecommendation(deepResearchSystemRecommendation{
			ID:       "code-to-infra-ownership",
			Priority: "high",
			Title:    "Connect code ownership to infrastructure ownership",
			Summary:  "Map repositories, deployment pipelines, services, and owner tags so the report can recommend the responsible code path, not only the cloud resource.",
			Impact:   "Shortens remediation handoff and makes architecture recommendations executable by the right team.",
			Effort:   "small",
			Evidence: append(codebase.Evidence, codebase.Gaps...),
			Actions: []string{
				"Track GitHub repos used by the estate and include deploy resources in scans.",
				"Add owner, environment, service, repo, and criticality tags to long-lived resources.",
				"Link Vercel, Railway, Cloudflare Workers, Kubernetes workloads, and cloud resources back to repository names where possible.",
			},
		})
	}

	if architectureGap || len(bottlenecks) > 0 {
		appendRecommendation(deepResearchSystemRecommendation{
			ID:       "dependency-map",
			Priority: "high",
			Title:    "Tighten the dependency graph before redesign work",
			Summary:  "Add typed edges for request, data, auth, event, deploy, and monitoring relationships so architecture recommendations can distinguish critical paths from incidental inventory.",
			Impact:   "Improves blast-radius analysis, capacity planning, and change sequencing.",
			Effort:   "medium",
			Evidence: append(architecture.Evidence, architecture.Gaps...),
			Actions: []string{
				"Prefer typed connections over untyped adjacency when merging provider resources.",
				"Flag edge services, databases, queues, caches, and deployment pipelines as architecture-critical.",
				"Use dependency fan-in plus live traffic to decide which components need redundancy or decomposition first.",
			},
		})
	}

	if len(resilience) > 0 || len(misconfigs) > 0 {
		appendRecommendation(deepResearchSystemRecommendation{
			ID:       "resilience-hardening",
			Priority: "high",
			Title:    "Harden the critical path before aggressive optimization",
			Summary:  "Fix single points, degraded resources, public data planes, missing backups, and encryption gaps before reducing capacity or removing resources.",
			Impact:   "Keeps savings work from increasing outage or exposure risk.",
			Effort:   "medium",
			Evidence: deepResearchTopFindingTitles(append(resilience, misconfigs...), 4),
			Actions: []string{
				"Validate backups, restore paths, encryption, and private networking on data stores.",
				"Add redundancy or failover for single visible databases, caches, queues, and edges that serve production traffic.",
				"Use recent logs and alerts to decide which degraded resources block optimization work.",
			},
		})
	}

	if len(costs) > 0 || (len(providers) > 0 && providers[0].ShareOfCost >= 60) {
		appendRecommendation(deepResearchSystemRecommendation{
			ID:       "cost-and-capacity-loop",
			Priority: "medium",
			Title:    "Run cost optimization as a measured capacity loop",
			Summary:  "Pair the top cost drivers with utilization and traffic data, then right-size only after demand, redundancy, and owner intent are known.",
			Impact:   "Avoids blind cuts while still prioritizing high-return savings.",
			Effort:   "medium",
			Evidence: append(deepResearchTopFindingTitles(costs, 4), architecture.Evidence...),
			Actions: []string{
				"Group cost drivers by provider, service, owner, and traffic path.",
				"Prioritize idle and orphaned resources first, then right-size hot paths with utilization evidence.",
				"Record accepted savings as follow-up tickets with rollback criteria.",
			},
		})
	}

	if len(recommendations) == 0 {
		appendRecommendation(deepResearchSystemRecommendation{
			ID:       "baseline-improvement-loop",
			Priority: "medium",
			Title:    "Create a repeatable improvement baseline",
			Summary:  "The current snapshot has no dominant critical theme, so improve the next scan by adding traffic metrics, typed dependencies, owner tags, and repo links.",
			Impact:   "Makes future research more precise and cheaper to act on.",
			Effort:   "small",
			Evidence: []string{
				fmt.Sprintf("%d resources reviewed.", len(estate.Resources)),
				fmt.Sprintf("%d ranked findings generated.", len(findings)),
			},
			Actions: []string{
				"Tag owners and criticality for production resources.",
				"Add typed connections for user-facing and data paths.",
				"Connect deploy sources and rerun deep research after the next scan.",
			},
		})
	}

	if len(recommendations) > maxDeepResearchSystemRecs {
		return append([]deepResearchSystemRecommendation(nil), recommendations[:maxDeepResearchSystemRecs]...)
	}
	return recommendations
}

func buildDeepResearchQuality(estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, systemImprovement *deepResearchSystemImprovement) *deepResearchQuality {
	edges, typedEdges, entrypoints := deepResearchTopologyCounts(estate.Resources)
	trafficSignals := collectDeepResearchTrafficSignals(estate.Resources, 0)
	costTrafficSignals := collectDeepResearchCostTrafficSignals(estate.Resources, 0)
	trackedRepos := loadDeepResearchTrackedRepos()
	codeSignals := collectDeepResearchCodebaseSignals(estate.Resources, 0)
	deploySignals := collectDeepResearchDeploySignals(estate.Resources, 0)
	evidenceMix := buildDeepResearchEvidenceMix(findings)
	sourceCounts := make(map[string]int, len(evidenceMix))
	for _, item := range evidenceMix {
		sourceCounts[item.Source] = item.Count
	}

	untagged := 0
	for _, resource := range estate.Resources {
		if !deepResearchHasOwnershipTags(resource.Tags) {
			untagged++
		}
	}

	score := 30
	checks := make([]deepResearchQualityCheck, 0, 7)
	addCheck := func(name string, status string, summary string, scoreDelta int) {
		checks = append(checks, deepResearchQualityCheck{Name: name, Status: status, Summary: summary})
		score += scoreDelta
	}

	switch {
	case len(estate.Resources) >= 25:
		addCheck("Estate context", "ok", fmt.Sprintf("%d resources were available for estate-wide ranking.", len(estate.Resources)), 12)
	case len(estate.Resources) > 0:
		addCheck("Estate context", "warning", fmt.Sprintf("Only %d resources were available; conclusions may be narrow.", len(estate.Resources)), 4)
	default:
		addCheck("Estate context", "gap", "No estate resources were available to deep research.", -18)
	}

	switch {
	case len(trafficSignals) > 0 && len(costTrafficSignals) > 0:
		addCheck("Traffic telemetry", "ok", fmt.Sprintf("%d traffic signals and %d cost-traffic candidates were present.", len(trafficSignals), len(costTrafficSignals)), 14)
	case len(trafficSignals) > 0:
		addCheck("Traffic telemetry", "warning", fmt.Sprintf("%d traffic signals were present, but cost correlation is limited.", len(trafficSignals)), 8)
	default:
		addCheck("Traffic telemetry", "gap", "No direct request, latency, throughput, or bandwidth metrics were present.", -10)
	}

	switch {
	case typedEdges > 0:
		addCheck("Service topology", "ok", fmt.Sprintf("%d dependency edges include %d typed relationships.", edges, typedEdges), 12)
	case edges > 0:
		addCheck("Service topology", "warning", fmt.Sprintf("%d dependency edges exist, but none are typed.", edges), 4)
	default:
		addCheck("Service topology", "gap", "No dependency edges were available for path analysis.", -10)
	}

	switch {
	case sourceCounts["provider-drilldown"] > 0:
		addCheck("Provider evidence", "ok", fmt.Sprintf("%d live drilldown evidence items were attached to findings.", sourceCounts["provider-drilldown"]), 14)
	case sourceCounts["provider-scout"] > 0:
		addCheck("Provider evidence", "warning", fmt.Sprintf("%d provider scout evidence items were attached, but no drilldowns landed.", sourceCounts["provider-scout"]), 8)
	case len(findings) > 0:
		addCheck("Provider evidence", "gap", "Findings are mostly heuristic; provider scouts did not attach live evidence.", -6)
	default:
		addCheck("Provider evidence", "gap", "No findings were available for evidence enrichment.", -8)
	}

	switch {
	case len(trackedRepos) > 0 || len(codeSignals) > 0:
		addCheck("Code context", "ok", fmt.Sprintf("%d tracked repos and %d resource code signals were visible.", len(trackedRepos), len(codeSignals)), 10)
	case len(deploySignals) > 0:
		addCheck("Code context", "warning", fmt.Sprintf("%d deployment signals were visible, but repository links were missing.", len(deploySignals)), 5)
	default:
		addCheck("Code context", "gap", "No repository or code ownership context was visible.", -8)
	}

	if estate.TotalCost > 0 && len(costTrafficSignals) > 0 {
		addCheck("Cost-performance correlation", "ok", "Cost data and traffic signals can be compared in this run.", 10)
	} else if estate.TotalCost > 0 {
		addCheck("Cost-performance correlation", "warning", "Cost data is present, but traffic signals are missing or too sparse for validation.", 4)
	} else {
		addCheck("Cost-performance correlation", "gap", "No estate cost total was available for efficiency analysis.", -6)
	}

	if len(estate.Resources) > 0 && untagged <= len(estate.Resources)/3 {
		addCheck("Ownership metadata", "ok", fmt.Sprintf("%d of %d resources lack owner or environment tags.", untagged, len(estate.Resources)), 8)
	} else if len(estate.Resources) > 0 {
		addCheck("Ownership metadata", "warning", fmt.Sprintf("%d of %d resources lack owner or environment tags.", untagged, len(estate.Resources)), 2)
	} else {
		addCheck("Ownership metadata", "gap", "No resources were available for ownership metadata checks.", -6)
	}

	contextGaps := collectDeepResearchQualityGaps(checks, systemImprovement)
	nextData := buildDeepResearchNextDataToCollect(checks, systemImprovement, entrypoints)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	confidence := "low"
	switch {
	case score >= 78:
		confidence = "high"
	case score >= 55:
		confidence = "medium"
	}

	return &deepResearchQuality{
		Score:             score,
		Confidence:        confidence,
		Summary:           fmt.Sprintf("%s confidence with %d quality checks, %d findings, and %d evidence items.", strings.Title(confidence), len(checks), len(findings), deepResearchEvidenceMixTotal(evidenceMix)),
		EvidenceMix:       evidenceMix,
		Checks:            checks,
		ContextGaps:       contextGaps,
		NextDataToCollect: nextData,
	}
}

func buildDeepResearchAdvisorBenchmarks(estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) *deepResearchAdvisorBenchmarks {
	pillars := buildDeepResearchAdvisorPillars(estate, findings, systemImprovement, quality)
	lessons := []deepResearchAdvisorLesson{
		{
			Product:   "AWS Trusted Advisor, Azure Advisor, and Google Active Assist",
			Lesson:    "Mature cloud advisors organize recommendations by durable operational pillars and make the remediation path reviewable.",
			AppliedAs: "Adds advisor pillars with scores, statuses, evidence, and next actions for cost, performance, reliability, security, and operations.",
		},
		{
			Product:   "Google Active Assist",
			Lesson:    "Recommendations need human assessment before apply or dismiss decisions because changes can affect performance, reliability, and permissions.",
			AppliedAs: "Keeps the workflow human-in-the-loop and separates evidence review from remediation and completion.",
		},
		{
			Product:   "Datadog Bits AI SRE, Dynatrace Davis, and New Relic iRCA",
			Lesson:    "RCA products earn trust by grounding conclusions in topology, causal paths, source evidence, and visible investigation traces.",
			AppliedAs: "Carries critical path candidates, evidence mix, research confidence, and live progress notes into the report.",
		},
		{
			Product:   "CloudZero and Harness Cloud Cost Management",
			Lesson:    "Cost optimization gets safer when spend is tied to unit metrics, business dimensions, anomalies, budgets, and owners.",
			AppliedAs: "Highlights cost-to-traffic candidates and missing unit-economics data before recommending rightsizing.",
		},
	}
	workflow := buildDeepResearchAdvisorWorkflow(pillars, systemImprovement, quality)

	summary := "Advisor benchmark output compares this scan against cloud-advisor, observability-RCA, and FinOps product patterns."
	if len(pillars) > 0 {
		top := deepResearchAdvisorPriorityLabels(pillars, 2)
		if len(top) > 0 {
			summary = fmt.Sprintf("Advisor benchmark output prioritizes %s using cloud-advisor pillars, topology evidence, and FinOps-style unit metrics.", strings.Join(top, " and "))
		}
	}
	if len(providers) > 0 {
		summary = fmt.Sprintf("%s Largest observed provider by spend is %s at %.1f%%.", summary, strings.ToUpper(providers[0].Provider), providers[0].ShareOfCost)
	}

	return &deepResearchAdvisorBenchmarks{
		Summary:  summary,
		Pillars:  pillars,
		Lessons:  lessons,
		Workflow: workflow,
	}
}

func buildDeepResearchAdvisorPillars(estate deepResearchEstateSnapshot, findings []deepResearchFinding, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) []deepResearchAdvisorPillar {
	type pillarSpec struct {
		id                string
		label             string
		categories        []string
		recommendationIDs []string
		fallbackAction    string
	}
	specs := []pillarSpec{
		{
			id:                "cost-efficiency",
			label:             "Cost efficiency",
			categories:        []string{"cost", "hygiene"},
			recommendationIDs: []string{"cost-and-capacity-loop"},
			fallbackAction:    "Collect unit metrics and validate top spend before rightsizing.",
		},
		{
			id:                "performance",
			label:             "Performance",
			categories:        []string{"bottleneck"},
			recommendationIDs: []string{"traffic-observability", "dependency-map"},
			fallbackAction:    "Measure latency, request volume, saturation, and path fan-in on entrypoints.",
		},
		{
			id:                "reliability",
			label:             "Reliability",
			categories:        []string{"resilience"},
			recommendationIDs: []string{"resilience-hardening", "dependency-map"},
			fallbackAction:    "Validate redundancy, backups, restore paths, and failover on critical paths.",
		},
		{
			id:                "security",
			label:             "Security",
			categories:        []string{"misconfiguration"},
			recommendationIDs: []string{"resilience-hardening"},
			fallbackAction:    "Review public exposure, encryption, IAM, and private networking gaps.",
		},
		{
			id:                "operational-excellence",
			label:             "Operational excellence",
			recommendationIDs: []string{"code-to-infra-ownership", "dependency-map", "baseline-improvement-loop", "traffic-observability"},
			fallbackAction:    "Route recommendations to owners and track review, ticket, validation, and completion states.",
		},
	}

	pillars := make([]deepResearchAdvisorPillar, 0, len(specs))
	for _, spec := range specs {
		matchedFindings := deepResearchFindingsByCategories(findings, spec.categories...)
		recommendations := deepResearchRecommendationsByIDs(systemImprovement, spec.recommendationIDs...)
		gapCount := deepResearchAdvisorGapCount(spec.id, estate, systemImprovement, quality)
		score := deepResearchAdvisorPillarScore(matchedFindings, len(recommendations), gapCount)
		status := deepResearchAdvisorPillarStatus(score, matchedFindings, gapCount)
		evidence := deepResearchAdvisorPillarEvidence(spec.id, estate, matchedFindings, recommendations, systemImprovement, quality)
		nextAction := deepResearchAdvisorNextAction(recommendations, quality, spec.fallbackAction)
		pillars = append(pillars, deepResearchAdvisorPillar{
			ID:                  spec.id,
			Label:               spec.label,
			Status:              status,
			Score:               score,
			Summary:             deepResearchAdvisorPillarSummary(spec.label, status, matchedFindings, recommendations, gapCount),
			FindingCount:        len(matchedFindings),
			RecommendationCount: len(recommendations),
			Evidence:            evidence,
			NextAction:          nextAction,
		})
	}
	sort.SliceStable(pillars, func(i, j int) bool {
		if pillars[i].Score != pillars[j].Score {
			return pillars[i].Score < pillars[j].Score
		}
		return pillars[i].Label < pillars[j].Label
	})
	return pillars
}

func buildDeepResearchAdvisorWorkflow(pillars []deepResearchAdvisorPillar, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) []deepResearchAdvisorWorkflowStep {
	topPillars := deepResearchAdvisorPriorityLabels(pillars, 3)
	nextData := []string(nil)
	if quality != nil {
		nextData = append(nextData, quality.NextDataToCollect...)
	}
	recommendationTitles := []string(nil)
	if systemImprovement != nil {
		for _, recommendation := range systemImprovement.Recommendations {
			recommendationTitles = append(recommendationTitles, recommendation.Title)
		}
	}
	return []deepResearchAdvisorWorkflowStep{
		{
			ID:      "review",
			Label:   "Review and accept",
			Summary: "Review pillar status, evidence mix, and confidence before accepting or dismissing recommendations.",
			Inputs:  deepResearchLimitStrings(append(topPillars, deepResearchAdvisorQualityLabel(quality)), 4),
			Outputs: []string{"Accepted recommendations", "Dismissed recommendations with rationale"},
		},
		{
			ID:      "instrument",
			Label:   "Fill evidence gaps",
			Summary: "Collect missing telemetry, topology, ownership, and unit-economics inputs before risky optimization.",
			Inputs:  deepResearchLimitStrings(uniqueNonEmptyStrings(nextData), 4),
			Outputs: []string{"Request and latency metrics", "Typed dependencies", "Owner and repo tags"},
		},
		{
			ID:      "triage",
			Label:   "Route work",
			Summary: "Convert accepted advisor items into tickets or runbook tasks owned by platform, app, security, or finance teams.",
			Inputs:  deepResearchLimitStrings(uniqueNonEmptyStrings(recommendationTitles), 4),
			Outputs: []string{"Owner-assigned tickets", "Runbook tasks", "Rollback criteria"},
		},
		{
			ID:      "validate",
			Label:   "Validate and complete",
			Summary: "Rerun deep research after remediation and mark items complete only when telemetry or provider checks confirm the outcome.",
			Inputs:  []string{"Post-change scan", "Provider evidence", "Cost and traffic deltas"},
			Outputs: []string{"Completed recommendations", "Updated baseline score"},
		},
	}
}

func buildDeepResearchExpertTeam(estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality, advisorBenchmarks *deepResearchAdvisorBenchmarks) *deepResearchExpertTeam {
	costFindings := deepResearchFindingsByCategory(findings, "cost")
	bottleneckFindings := deepResearchFindingsByCategory(findings, "bottleneck")
	resilienceFindings := deepResearchFindingsByCategory(findings, "resilience")
	misconfigFindings := deepResearchFindingsByCategory(findings, "misconfiguration")
	hygieneFindings := deepResearchFindingsByCategory(findings, "hygiene")
	databaseFindings := deepResearchDatabaseFindings(findings)
	status := deepResearchExpertTeamStatus(findings, systemImprovement, quality, advisorBenchmarks)

	traffic := deepResearchSystemBlock(systemImprovement, "traffic")
	codebase := deepResearchSystemBlock(systemImprovement, "codebase")
	architecture := deepResearchSystemBlock(systemImprovement, "architecture")
	providerEvidence := deepResearchProviderRollupEvidence(providers, 5)
	topRecommendations := deepResearchSystemRecommendationLines(systemImprovement, 5)
	criticalPaths := collectDeepResearchCriticalPaths(estate.Resources, 3)

	personas := []deepResearchExpertPersona{
		{
			ID:         "systems-architect",
			Title:      "Systems Architect",
			Discipline: "Architecture and dependency design",
			Status:     deepResearchExpertPersonaStatus(append(append([]deepResearchFinding(nil), bottleneckFindings...), resilienceFindings...), architecture.Status, len(architecture.Gaps)),
			Summary:    fmt.Sprintf("Reviewed topology, dependency fan-in, critical paths, region spread, and system-level recommendations across %d resources.", len(estate.Resources)),
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(architecture.Evidence, criticalPaths...)), 5),
			Concerns:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(architecture.Gaps, deepResearchTopFindingTitles(append(bottleneckFindings, resilienceFindings...), 4)...)), 5),
			Recommendations: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "dependency-map", "resilience-hardening", "baseline-improvement-loop"),
				"Use typed dependency edges plus traffic to separate critical request paths from incidental inventory.",
			)), 4),
		},
		{
			ID:         "cloud-architect",
			Title:      "Cloud Architect",
			Discipline: "Provider coverage and platform design",
			Status:     deepResearchCloudArchitectStatus(providers, architecture, quality),
			Summary:    fmt.Sprintf("Reviewed %d provider groups, %d regions, and whether each platform has enough evidence to support architectural recommendations.", len(providers), len(deepResearchRegions(estate.Resources))),
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(providerEvidence, architecture.Evidence...)), 5),
			Concerns:   deepResearchCloudArchitectConcerns(providers, architecture, quality),
			Recommendations: []string{
				"Keep every visible provider in the report, even when one platform dominates spend.",
				"Label unknown provider buckets instead of folding them into AWS or another default provider.",
				"Review regional placement, egress, service ownership, and failover boundaries together.",
			},
		},
		{
			ID:         "site-reliability-engineer",
			Title:      "Site Reliability Engineer",
			Discipline: "Reliability, traffic, incidents, and safe change",
			Status:     deepResearchExpertPersonaStatus(append(append([]deepResearchFinding(nil), bottleneckFindings...), resilienceFindings...), traffic.Status, len(traffic.Gaps)),
			Summary:    "Reviewed traffic telemetry, topology hotspots, degraded-state findings, and what must be measured before risky changes.",
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(traffic.Evidence, criticalPaths...)), 5),
			Concerns:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(append(traffic.Gaps, deepResearchTopFindingTitles(bottleneckFindings, 3)...), deepResearchTopFindingTitles(resilienceFindings, 3)...)), 5),
			Recommendations: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "traffic-observability", "resilience-hardening"),
				"Validate latency, error rate, saturation, and rollback criteria before rightsizing production paths.",
			)), 4),
		},
		{
			ID:         "devops-engineer",
			Title:      "DevOps Engineer",
			Discipline: "Delivery, ownership, and remediation flow",
			Status:     deepResearchExpertPersonaStatus(hygieneFindings, codebase.Status, len(codebase.Gaps)),
			Summary:    "Reviewed repo links, deployment signals, ownership metadata, and whether findings can be routed to the right team.",
			Evidence:   deepResearchLimitStrings(codebase.Evidence, 5),
			Concerns:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(codebase.Gaps, deepResearchTopFindingTitles(hygieneFindings, 4)...)), 5),
			Recommendations: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "code-to-infra-ownership", "baseline-improvement-loop"),
				"Turn accepted findings into owner-assigned tickets with validation and rollback notes.",
			)), 4),
		},
		{
			ID:         "finops-analyst",
			Title:      "FinOps Analyst",
			Discipline: "Cost, utilization, unit metrics, and accountability",
			Status:     deepResearchFinOpsStatus(costFindings, providers, quality),
			Summary:    fmt.Sprintf("Reviewed %.2f monthly observed spend, provider concentration, cost findings, and whether savings can be validated against demand.", estate.TotalCost),
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(append(providerEvidence, deepResearchTopFindingTitles(costFindings, 4)...), collectDeepResearchCostTrafficSignals(estate.Resources, 3)...)), 5),
			Concerns:   deepResearchFinOpsConcerns(costFindings, providers, quality),
			Recommendations: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "cost-and-capacity-loop"),
				"Handle idle and orphaned resources first; right-size hot paths only after traffic and utilization support the change.",
			)), 4),
		},
		{
			ID:         "security-engineer",
			Title:      "Security Engineer",
			Discipline: "Exposure, data protection, and hardening",
			Status:     deepResearchExpertPersonaStatus(misconfigFindings, architecture.Status, len(misconfigFindings)),
			Summary:    "Reviewed public exposure, encryption, backups, IAM-adjacent signals, and security-sensitive architecture gaps.",
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(deepResearchTopFindingTitles(misconfigFindings, 5), architecture.Evidence...)), 5),
			Concerns:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(architecture.Gaps, deepResearchTopFindingTitles(misconfigFindings, 5)...)), 5),
			Recommendations: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "resilience-hardening"),
				"Prioritize public data planes, missing backups, missing encryption, and private-networking gaps before capacity cuts.",
			)), 4),
		},
		{
			ID:         "data-platform-engineer",
			Title:      "Data Platform Engineer",
			Discipline: "Databases, stateful systems, and recovery",
			Status:     deepResearchExpertPersonaStatus(databaseFindings, architecture.Status, len(databaseFindings)),
			Summary:    fmt.Sprintf("Reviewed %d stateful or database findings plus backup, encryption, public exposure, and single-instance risk.", len(databaseFindings)),
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(deepResearchTopFindingTitles(databaseFindings, 5), collectDeepResearchCriticalPaths(estate.Resources, 2)...)), 5),
			Concerns:   deepResearchLimitStrings(deepResearchTopFindingTitles(databaseFindings, 5), 5),
			Recommendations: []string{
				"Verify backup retention, restore time, encryption, and private access for every production datastore.",
				"Treat databases, queues, caches, and stateful clusters as critical-path resources in the next scan.",
				"Add redundancy or failover before removing spare capacity from stateful paths.",
			},
		},
		{
			ID:         "product-manager",
			Title:      "Product Manager",
			Discipline: "User impact, prioritization, and delivery sequencing",
			Status:     deepResearchProductStatus(findings, quality),
			Summary:    "Reviewed which findings can affect users, which evidence gaps block confident decisions, and how to sequence work into an executable roadmap.",
			Evidence:   deepResearchLimitStrings(uniqueNonEmptyStrings(append(deepResearchTopFindingTitles(findings, 4), topRecommendations...)), 5),
			Concerns:   deepResearchLimitStrings(deepResearchProductConcerns(findings, quality), 5),
			Recommendations: []string{
				"Lead the roadmap with user-facing reliability, exposure, and bottleneck risk before pure savings work.",
				"Make each accepted finding ownable: expected impact, validation signal, owner, due date, and rollback condition.",
				"Rerun deep research after remediation so the report becomes a closed-loop operating artifact.",
			},
		},
	}

	conclusions := buildDeepResearchTeamConclusions(status, estate, findings, providers, systemImprovement, quality, personas)
	agentRuns := buildDeepResearchExpertAgentRuns(personas, conclusions, systemImprovement, quality)
	dialogues := buildDeepResearchTeamDialogues(conclusions, personas, providers, quality)
	consensus := buildDeepResearchExpertConsensus(status, findings, providers, systemImprovement, quality)

	return &deepResearchExpertTeam{
		Status:      status,
		Summary:     buildDeepResearchExpertTeamSummary(status, estate, findings, providers, quality),
		Personas:    personas,
		Conclusions: conclusions,
		AgentRuns:   agentRuns,
		Dialogues:   dialogues,
		Consensus:   consensus,
	}
}

func buildDeepResearchExpertAgentRuns(personas []deepResearchExpertPersona, conclusions []deepResearchTeamConclusion, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) []deepResearchExpertAgentRun {
	runs := make([]deepResearchExpertAgentRun, 0, len(personas))
	qualityLabel := deepResearchAdvisorQualityLabel(quality)
	for _, persona := range personas {
		inputs := []string{persona.Discipline}
		inputs = append(inputs, persona.Evidence...)
		if qualityLabel != "" {
			inputs = append(inputs, qualityLabel)
		}
		outputs := append([]string(nil), persona.Concerns...)
		outputs = append(outputs, persona.Recommendations...)
		if len(outputs) == 0 {
			for _, conclusion := range conclusions {
				if deepResearchStringSliceContains(conclusion.Owners, persona.Title) {
					outputs = append(outputs, conclusion.Title+": "+conclusion.Summary)
				}
			}
		}
		if len(outputs) == 0 && systemImprovement != nil {
			outputs = deepResearchSystemRecommendationLines(systemImprovement, 2)
		}
		runs = append(runs, deepResearchExpertAgentRun{
			ID:      "expert-agent-" + persona.ID,
			Title:   persona.Title,
			Status:  persona.Status,
			Summary: fmt.Sprintf("%s reviewed the estate independently and contributed to the shared conclusions.", persona.Title),
			Inputs:  deepResearchLimitStrings(uniqueNonEmptyStrings(inputs), 5),
			Outputs: deepResearchLimitStrings(uniqueNonEmptyStrings(outputs), 5),
		})
	}
	return runs
}

func buildDeepResearchTeamDialogues(conclusions []deepResearchTeamConclusion, personas []deepResearchExpertPersona, providers []deepResearchProviderRoll, quality *deepResearchQuality) []deepResearchTeamDialogue {
	lookupTitle := func(id string) string {
		for _, persona := range personas {
			if persona.ID == id {
				return persona.Title
			}
		}
		return id
	}
	providerContext := "provider coverage"
	if len(providers) > 0 {
		providerContext = fmt.Sprintf("%s leads observed spend at %.1f%%", deepResearchProviderDisplayName(providers[0].Provider), providers[0].ShareOfCost)
	}
	qualityContext := "confidence was not scored"
	if quality != nil {
		qualityContext = fmt.Sprintf("research confidence is %s (%d/100)", quality.Confidence, quality.Score)
	}

	dialogues := []deepResearchTeamDialogue{
		{
			ID:       "traffic-before-rightsizing",
			Topic:    "Safe optimization order",
			Agents:   []string{lookupTitle("site-reliability-engineer"), lookupTitle("finops-analyst"), lookupTitle("systems-architect")},
			Exchange: "SRE and Systems Architecture compare traffic/topology evidence against FinOps cost candidates before approving capacity changes.",
			Decision: "Right-size only after request volume, latency, error rate, saturation, owner intent, and rollback criteria are visible.",
		},
		{
			ID:       "provider-coverage",
			Topic:    "Provider and billing coverage",
			Agents:   []string{lookupTitle("cloud-architect"), lookupTitle("finops-analyst"), lookupTitle("devops-engineer")},
			Exchange: fmt.Sprintf("Cloud Architecture checks every provider group while FinOps reconciles billing totals and DevOps checks whether owners/repos are attached; %s.", providerContext),
			Decision: "Treat billing totals as authoritative, keep unknown providers explicit, and add ownership/repo links where spend is not resource-mapped.",
		},
		{
			ID:       "risk-before-cost",
			Topic:    "Risk versus savings",
			Agents:   []string{lookupTitle("security-engineer"), lookupTitle("data-platform-engineer"), lookupTitle("product-manager")},
			Exchange: fmt.Sprintf("Security, Data, and Product weigh public exposure, backups, encryption, user impact, and confidence before prioritizing savings; %s.", qualityContext),
			Decision: "Reliability, data protection, and user-facing risk can outrank pure savings even when cost findings are numerous.",
		},
	}
	for _, conclusion := range conclusions {
		if conclusion.ID == "system-status" || conclusion.ID == "what-is-wrong" {
			dialogues = append(dialogues, deepResearchTeamDialogue{
				ID:       "conclusion-" + conclusion.ID,
				Topic:    conclusion.Title,
				Agents:   deepResearchLimitStrings(conclusion.Owners, 4),
				Exchange: conclusion.Summary,
				Decision: strings.Join(deepResearchLimitStrings(conclusion.NextActions, 2), " "),
			})
		}
	}
	return deepResearchLimitDialogues(dialogues, 5)
}

func buildDeepResearchExpertSubagentRuns(team *deepResearchExpertTeam) []deepResearchSubagentRun {
	if team == nil {
		return nil
	}
	runs := make([]deepResearchSubagentRun, 0, len(team.AgentRuns))
	for _, agentRun := range team.AgentRuns {
		details := append([]string(nil), agentRun.Inputs...)
		for _, output := range agentRun.Outputs {
			details = append(details, "Output: "+output)
		}
		runs = append(runs, deepResearchSubagentRun{
			Name:    agentRun.ID,
			Status:  agentRun.Status,
			Summary: agentRun.Summary,
			Details: deepResearchLimitStrings(uniqueNonEmptyStrings(details), 6),
		})
	}
	for _, dialogue := range team.Dialogues {
		runs = append(runs, deepResearchSubagentRun{
			Name:    "expert-dialogue-" + dialogue.ID,
			Status:  "ok",
			Summary: fmt.Sprintf("%s: %s", dialogue.Topic, dialogue.Decision),
			Details: uniqueNonEmptyStrings([]string{
				"Agents: " + strings.Join(dialogue.Agents, ", "),
				dialogue.Exchange,
			}),
		})
	}
	return runs
}

func deepResearchLimitDialogues(dialogues []deepResearchTeamDialogue, maxCount int) []deepResearchTeamDialogue {
	if maxCount <= 0 || len(dialogues) <= maxCount {
		return append([]deepResearchTeamDialogue(nil), dialogues...)
	}
	return append([]deepResearchTeamDialogue(nil), dialogues[:maxCount]...)
}

func deepResearchStringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func deepResearchSystemBlock(systemImprovement *deepResearchSystemImprovement, id string) deepResearchSystemImprovementBlock {
	if systemImprovement == nil {
		return deepResearchSystemImprovementBlock{Status: "gap", Summary: "No system improvement analysis was available."}
	}
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "traffic":
		return systemImprovement.Traffic
	case "codebase":
		return systemImprovement.Codebase
	case "architecture":
		return systemImprovement.Architecture
	default:
		return deepResearchSystemImprovementBlock{Status: "gap"}
	}
}

func deepResearchExpertTeamStatus(findings []deepResearchFinding, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality, advisorBenchmarks *deepResearchAdvisorBenchmarks) string {
	critical := 0
	high := 0
	for _, finding := range findings {
		switch deepResearchSeverityRank(finding.Severity) {
		case 4:
			critical++
		case 3:
			high++
		}
	}
	if critical > 0 || deepResearchHasAdvisorStatus(advisorBenchmarks, "critical") {
		return "critical"
	}
	if high > 0 || deepResearchHasSystemBlockStatus(systemImprovement, "gap") || (quality != nil && quality.Score < 55) {
		return "action"
	}
	if len(findings) > 0 || deepResearchHasSystemBlockStatus(systemImprovement, "warning") || (quality != nil && quality.Score < 78) {
		return "watch"
	}
	return "ok"
}

func deepResearchExpertPersonaStatus(findings []deepResearchFinding, blockStatus string, gapCount int) string {
	for _, finding := range findings {
		if deepResearchSeverityRank(finding.Severity) >= 4 {
			return "critical"
		}
	}
	switch strings.ToLower(strings.TrimSpace(blockStatus)) {
	case "gap":
		return "gap"
	case "critical", "error":
		return "critical"
	case "warning", "action":
		return "action"
	}
	for _, finding := range findings {
		if deepResearchSeverityRank(finding.Severity) >= 3 {
			return "action"
		}
	}
	if gapCount > 0 {
		return "action"
	}
	if len(findings) > 0 {
		return "watch"
	}
	return "ok"
}

func deepResearchCloudArchitectStatus(providers []deepResearchProviderRoll, architecture deepResearchSystemImprovementBlock, quality *deepResearchQuality) string {
	if architecture.Status == "gap" {
		return "gap"
	}
	if len(providers) == 0 || deepResearchProviderRollupHasUnknown(providers) {
		return "action"
	}
	if len(providers) > 0 && providers[0].ShareOfCost >= 70 {
		return "watch"
	}
	if quality != nil && quality.Score < 55 {
		return "action"
	}
	return deepResearchExpertPersonaStatus(nil, architecture.Status, len(architecture.Gaps))
}

func deepResearchFinOpsStatus(costFindings []deepResearchFinding, providers []deepResearchProviderRoll, quality *deepResearchQuality) string {
	if status := deepResearchExpertPersonaStatus(costFindings, "", 0); status == "critical" || status == "action" {
		return status
	}
	if len(providers) > 0 && providers[0].ShareOfCost >= 70 {
		return "watch"
	}
	if quality != nil {
		for _, gap := range quality.NextDataToCollect {
			if strings.Contains(strings.ToLower(gap), "cost") || strings.Contains(strings.ToLower(gap), "traffic") || strings.Contains(strings.ToLower(gap), "utilization") {
				return "watch"
			}
		}
	}
	if len(costFindings) > 0 {
		return "watch"
	}
	return "ok"
}

func deepResearchProductStatus(findings []deepResearchFinding, quality *deepResearchQuality) string {
	if status := deepResearchExpertPersonaStatus(findings, "", 0); status == "critical" || status == "action" {
		return status
	}
	if quality != nil && quality.Score < 78 {
		return "watch"
	}
	if len(findings) > 0 {
		return "watch"
	}
	return "ok"
}

func deepResearchHasAdvisorStatus(advisorBenchmarks *deepResearchAdvisorBenchmarks, status string) bool {
	if advisorBenchmarks == nil {
		return false
	}
	for _, pillar := range advisorBenchmarks.Pillars {
		if strings.EqualFold(strings.TrimSpace(pillar.Status), status) {
			return true
		}
	}
	return false
}

func deepResearchHasSystemBlockStatus(systemImprovement *deepResearchSystemImprovement, status string) bool {
	if systemImprovement == nil {
		return false
	}
	status = strings.ToLower(strings.TrimSpace(status))
	return strings.ToLower(strings.TrimSpace(systemImprovement.Traffic.Status)) == status ||
		strings.ToLower(strings.TrimSpace(systemImprovement.Codebase.Status)) == status ||
		strings.ToLower(strings.TrimSpace(systemImprovement.Architecture.Status)) == status
}

func deepResearchProviderRollupEvidence(providers []deepResearchProviderRoll, maxCount int) []string {
	evidence := make([]string, 0, maxCount)
	for _, provider := range providers {
		label := deepResearchProviderDisplayName(provider.Provider)
		if label == "" {
			label = "Unknown"
		}
		evidence = append(evidence, fmt.Sprintf("%s has %d resources, $%.2f/mo observed spend, and %.1f%% of observed cost.", label, provider.ResourceCount, provider.MonthlyCost, provider.ShareOfCost))
		if maxCount > 0 && len(evidence) >= maxCount {
			break
		}
	}
	return evidence
}

func deepResearchProviderRollupHasUnknown(providers []deepResearchProviderRoll) bool {
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider.Provider), "unknown") {
			return true
		}
	}
	return false
}

func deepResearchCloudArchitectConcerns(providers []deepResearchProviderRoll, architecture deepResearchSystemImprovementBlock, quality *deepResearchQuality) []string {
	concerns := append([]string(nil), architecture.Gaps...)
	if len(providers) == 0 {
		concerns = append(concerns, "No provider rollup was available, so platform coverage cannot be verified.")
	} else if providers[0].ShareOfCost >= 70 {
		concerns = append(concerns, fmt.Sprintf("%s carries %.1f%% of observed spend, so provider concentration deserves architecture review.", deepResearchProviderDisplayName(providers[0].Provider), providers[0].ShareOfCost))
	}
	if deepResearchProviderRollupHasUnknown(providers) {
		concerns = append(concerns, "Some resources are in an unknown provider bucket and should be classified before provider-specific recommendations are trusted.")
	}
	if quality != nil {
		for _, gap := range quality.ContextGaps {
			if strings.Contains(strings.ToLower(gap), "provider") {
				concerns = append(concerns, gap)
			}
		}
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(concerns), 5)
}

func deepResearchFinOpsConcerns(costFindings []deepResearchFinding, providers []deepResearchProviderRoll, quality *deepResearchQuality) []string {
	concerns := deepResearchTopFindingTitles(costFindings, 4)
	if len(providers) > 0 && providers[0].ShareOfCost >= 60 {
		concerns = append(concerns, fmt.Sprintf("%s represents %.1f%% of observed spend.", deepResearchProviderDisplayName(providers[0].Provider), providers[0].ShareOfCost))
	}
	if quality != nil {
		for _, gap := range quality.NextDataToCollect {
			lower := strings.ToLower(gap)
			if strings.Contains(lower, "cost") || strings.Contains(lower, "traffic") || strings.Contains(lower, "utilization") || strings.Contains(lower, "unit") {
				concerns = append(concerns, gap)
			}
		}
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(concerns), 5)
}

func deepResearchProductConcerns(findings []deepResearchFinding, quality *deepResearchQuality) []string {
	concerns := deepResearchTopFindingTitles(findings, 4)
	if quality != nil && quality.Confidence != "high" {
		concerns = append(concerns, fmt.Sprintf("Research confidence is %s (%d/100), so roadmap commitments should call out missing evidence.", quality.Confidence, quality.Score))
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(concerns), 5)
}

func deepResearchRecommendationSummaries(systemImprovement *deepResearchSystemImprovement, ids ...string) []string {
	recommendations := deepResearchRecommendationsByIDs(systemImprovement, ids...)
	lines := make([]string, 0, len(recommendations))
	for _, recommendation := range recommendations {
		line := strings.TrimSpace(recommendation.Title)
		if len(recommendation.Actions) > 0 && strings.TrimSpace(recommendation.Actions[0]) != "" {
			line = fmt.Sprintf("%s: %s", line, strings.TrimSpace(recommendation.Actions[0]))
		} else if strings.TrimSpace(recommendation.Summary) != "" {
			line = fmt.Sprintf("%s: %s", line, strings.TrimSpace(recommendation.Summary))
		}
		lines = append(lines, line)
	}
	return uniqueNonEmptyStrings(lines)
}

func deepResearchSystemRecommendationLines(systemImprovement *deepResearchSystemImprovement, maxCount int) []string {
	if systemImprovement == nil {
		return nil
	}
	lines := make([]string, 0, maxCount)
	for _, recommendation := range systemImprovement.Recommendations {
		line := strings.TrimSpace(recommendation.Title)
		if strings.TrimSpace(recommendation.Priority) != "" {
			line = fmt.Sprintf("%s priority: %s", strings.Title(strings.TrimSpace(recommendation.Priority)), line)
		}
		lines = append(lines, line)
		if maxCount > 0 && len(lines) >= maxCount {
			break
		}
	}
	return uniqueNonEmptyStrings(lines)
}

func deepResearchDatabaseFindings(findings []deepResearchFinding) []deepResearchFinding {
	out := make([]deepResearchFinding, 0)
	for _, finding := range findings {
		lower := strings.ToLower(strings.Join([]string{finding.ID, finding.Category, finding.ResourceType, finding.Title, finding.Summary}, " "))
		if deepResearchContainsAny(lower, "database", "db", "rds", "postgres", "mysql", "redis", "cache", "dynamodb", "aurora", "supabase", "backup", "encrypt", "data plane") {
			out = append(out, finding)
		}
	}
	return out
}

func buildDeepResearchTeamConclusions(status string, estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality, personas []deepResearchExpertPersona) []deepResearchTeamConclusion {
	criticalCount := deepResearchSeverityCount(findings, "critical")
	highCount := deepResearchSeverityCount(findings, "high")
	topRisks := deepResearchTopFindingTitles(findings, 3)
	systemActions := deepResearchSystemRecommendationLines(systemImprovement, 4)
	costFindings := deepResearchFindingsByCategory(findings, "cost")
	nonCostFindings := deepResearchNonCostFindings(findings)
	ownersByPersona := func(ids ...string) []string {
		wanted := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			wanted[id] = struct{}{}
		}
		owners := make([]string, 0, len(ids))
		for _, persona := range personas {
			if _, ok := wanted[persona.ID]; ok {
				owners = append(owners, persona.Title)
			}
		}
		return owners
	}

	providerLine := "No provider mix was available."
	if len(providers) > 0 {
		providerLine = fmt.Sprintf("Largest observed provider is %s at %.1f%% of observed spend.", deepResearchProviderDisplayName(providers[0].Provider), providers[0].ShareOfCost)
	}
	qualityLine := "Research confidence was not scored."
	if quality != nil {
		qualityLine = fmt.Sprintf("Research confidence is %s at %d/100.", quality.Confidence, quality.Score)
	}

	conclusions := []deepResearchTeamConclusion{
		{
			ID:      "system-status",
			Title:   "System status",
			Status:  status,
			Summary: fmt.Sprintf("The team reviewed %d resources across %d provider groups and found %d critical plus %d high findings. %s %s", len(estate.Resources), len(providers), criticalCount, highCount, providerLine, qualityLine),
			Owners:  ownersByPersona("systems-architect", "cloud-architect", "site-reliability-engineer"),
			NextActions: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				[]string{"Review the critical and high findings before accepting optimization work."},
				systemActions...,
			)), 4),
		},
		{
			ID:      "what-is-wrong",
			Title:   "What is wrong",
			Status:  deepResearchWrongnessStatus(nonCostFindings, systemImprovement, quality),
			Summary: deepResearchWrongnessSummary(nonCostFindings, topRisks, systemImprovement, quality),
			Owners:  ownersByPersona("systems-architect", "site-reliability-engineer", "security-engineer", "data-platform-engineer"),
			NextActions: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "dependency-map", "resilience-hardening", "traffic-observability"),
				topRisks...,
			)), 4),
		},
		{
			ID:      "system-optimization",
			Title:   "How to optimize the system",
			Status:  deepResearchExpertTeamStatus(nonCostFindings, systemImprovement, quality, nil),
			Summary: "Optimize architecture by tightening dependency maps, adding traffic and saturation metrics, assigning code ownership, and hardening critical paths before changing capacity.",
			Owners:  ownersByPersona("systems-architect", "site-reliability-engineer", "devops-engineer"),
			NextActions: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "traffic-observability", "dependency-map", "code-to-infra-ownership", "resilience-hardening"),
				"Run another scan after typed topology, ownership, and metrics are present.",
			)), 5),
		},
		{
			ID:      "cost-optimization",
			Title:   "How to optimize costs",
			Status:  deepResearchFinOpsStatus(costFindings, providers, quality),
			Summary: "Treat cost as a measured capacity loop: remove idle or orphaned resources first, then right-size active paths only after utilization, traffic, owner intent, and rollback criteria are clear.",
			Owners:  ownersByPersona("finops-analyst", "site-reliability-engineer", "product-manager"),
			NextActions: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				deepResearchRecommendationSummaries(systemImprovement, "cost-and-capacity-loop"),
				deepResearchTopFindingTitles(costFindings, 3)...,
			)), 4),
		},
		{
			ID:      "resource-and-architecture-recommendations",
			Title:   "Resource and architecture recommendations",
			Status:  status,
			Summary: "Prioritize resources on user-facing request paths, data stores, edge services, queues, caches, deployment pipelines, and any high-spend resource whose owner or demand signal is missing.",
			Owners:  ownersByPersona("cloud-architect", "data-platform-engineer", "devops-engineer", "security-engineer"),
			NextActions: deepResearchLimitStrings(uniqueNonEmptyStrings(append(
				systemActions,
				"Classify unknown providers and add owner, service, repo, environment, and criticality tags.",
			)), 5),
		},
	}

	return conclusions
}

func deepResearchWrongnessStatus(nonCostFindings []deepResearchFinding, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) string {
	if len(nonCostFindings) > 0 {
		return deepResearchExpertPersonaStatus(nonCostFindings, "", 0)
	}
	if systemImprovement != nil {
		gapCount := len(systemImprovement.Traffic.Gaps) + len(systemImprovement.Codebase.Gaps) + len(systemImprovement.Architecture.Gaps)
		if gapCount > 0 || deepResearchHasSystemBlockStatus(systemImprovement, "gap") {
			return "gap"
		}
		if deepResearchHasSystemBlockStatus(systemImprovement, "warning") {
			return "watch"
		}
	}
	if quality != nil && len(quality.ContextGaps) > 0 {
		return "gap"
	}
	return "ok"
}

func deepResearchWrongnessSummary(nonCostFindings []deepResearchFinding, topRisks []string, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) string {
	if len(nonCostFindings) > 0 {
		return fmt.Sprintf("The strongest non-cost problems are architecture, reliability, security, hygiene, or bottleneck risks: %s.", strings.Join(deepResearchLimitStrings(deepResearchTopFindingTitles(nonCostFindings, 3), 3), "; "))
	}
	if systemImprovement != nil {
		gaps := uniqueNonEmptyStrings(append(append(systemImprovement.Traffic.Gaps, systemImprovement.Codebase.Gaps...), systemImprovement.Architecture.Gaps...))
		if len(gaps) > 0 {
			return fmt.Sprintf("The main problem is incomplete evidence: %s", strings.Join(deepResearchLimitStrings(gaps, 3), "; "))
		}
	}
	if quality != nil && len(quality.ContextGaps) > 0 {
		return fmt.Sprintf("The main problem is that confidence is limited by missing context: %s", strings.Join(deepResearchLimitStrings(quality.ContextGaps, 3), "; "))
	}
	if len(topRisks) > 0 {
		return fmt.Sprintf("The main problems are the top ranked risks: %s.", strings.Join(topRisks, "; "))
	}
	return "No dominant fault pattern was detected; keep improving telemetry, topology, ownership, and provider evidence so the next review is sharper."
}

func deepResearchSeverityCount(findings []deepResearchFinding, severity string) int {
	count := 0
	for _, finding := range findings {
		if strings.EqualFold(strings.TrimSpace(finding.Severity), severity) {
			count++
		}
	}
	return count
}

func deepResearchNonCostFindings(findings []deepResearchFinding) []deepResearchFinding {
	out := make([]deepResearchFinding, 0, len(findings))
	for _, finding := range findings {
		if !strings.EqualFold(strings.TrimSpace(finding.Category), "cost") {
			out = append(out, finding)
		}
	}
	return out
}

func buildDeepResearchExpertConsensus(status string, findings []deepResearchFinding, providers []deepResearchProviderRoll, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) []string {
	consensus := []string{
		"Use the report as an architecture and operations review first; cost is one dimension of system health, not the whole objective.",
		"Do not optimize capacity on production paths until traffic, latency, error, saturation, owner, and rollback evidence exists.",
	}
	if len(providers) > 1 {
		consensus = append(consensus, "Cross-provider coverage matters; smaller providers should stay visible when they own edge, deploy, data, or auth surfaces.")
	}
	if deepResearchProviderRollupHasUnknown(providers) {
		consensus = append(consensus, "Unknown provider buckets must stay explicit until classified; silently defaulting them to a major cloud would hide risk.")
	}
	if systemImprovement != nil && len(systemImprovement.Recommendations) > 0 {
		consensus = append(consensus, fmt.Sprintf("The immediate improvement loop is %s.", strings.Join(deepResearchLimitStrings(deepResearchSystemRecommendationLines(systemImprovement, 3), 3), "; ")))
	}
	if quality != nil && quality.Confidence != "high" {
		consensus = append(consensus, fmt.Sprintf("Confidence is %s, so recommendations should include evidence gaps and validation steps.", quality.Confidence))
	}
	if status == "critical" || deepResearchSeverityCount(findings, "critical") > 0 {
		consensus = append(consensus, "Critical findings should block broad cost-cutting until reliability, security, and data-plane risk are reviewed.")
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(consensus), 6)
}

func buildDeepResearchExpertTeamSummary(status string, estate deepResearchEstateSnapshot, findings []deepResearchFinding, providers []deepResearchProviderRoll, quality *deepResearchQuality) string {
	qualityLabel := "unscored confidence"
	if quality != nil {
		qualityLabel = fmt.Sprintf("%s confidence (%d/100)", quality.Confidence, quality.Score)
	}
	return fmt.Sprintf("A simulated senior software and cloud team reviewed %d resources across %d provider groups with %s. Overall status is %s with %d ranked findings.", len(estate.Resources), len(providers), qualityLabel, status, len(findings))
}

func deepResearchFindingsByCategories(findings []deepResearchFinding, categories ...string) []deepResearchFinding {
	wanted := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		category = strings.ToLower(strings.TrimSpace(category))
		if category != "" {
			wanted[category] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	matched := make([]deepResearchFinding, 0)
	for _, finding := range findings {
		category := strings.ToLower(strings.TrimSpace(finding.Category))
		if _, ok := wanted[category]; ok {
			matched = append(matched, finding)
		}
	}
	return matched
}

func deepResearchRecommendationsByIDs(systemImprovement *deepResearchSystemImprovement, ids ...string) []deepResearchSystemRecommendation {
	if systemImprovement == nil || len(ids) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" {
			wanted[id] = struct{}{}
		}
	}
	matched := make([]deepResearchSystemRecommendation, 0)
	for _, recommendation := range systemImprovement.Recommendations {
		id := strings.ToLower(strings.TrimSpace(recommendation.ID))
		if _, ok := wanted[id]; ok {
			matched = append(matched, recommendation)
		}
	}
	return matched
}

func deepResearchAdvisorPillarScore(findings []deepResearchFinding, recommendationCount int, gapCount int) int {
	score := 100 - recommendationCount*6 - gapCount*5
	for _, finding := range findings {
		switch deepResearchSeverityRank(finding.Severity) {
		case 4:
			score -= 28
		case 3:
			score -= 18
		case 2:
			score -= 10
		case 1:
			score -= 5
		default:
			score -= 4
		}
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func deepResearchAdvisorPillarStatus(score int, findings []deepResearchFinding, gapCount int) string {
	for _, finding := range findings {
		if deepResearchSeverityRank(finding.Severity) >= 4 {
			return "critical"
		}
	}
	switch {
	case score < 50:
		return "critical"
	case score < 75:
		return "action"
	case score < 92 || gapCount > 0:
		return "watch"
	default:
		return "ok"
	}
}

func deepResearchAdvisorPillarSummary(label string, status string, findings []deepResearchFinding, recommendations []deepResearchSystemRecommendation, gapCount int) string {
	switch {
	case len(findings) > 0:
		return fmt.Sprintf("%s has %d ranked findings, %d advisor actions, and %d evidence gaps.", label, len(findings), len(recommendations), gapCount)
	case len(recommendations) > 0:
		return fmt.Sprintf("%s has %d advisor actions driven by context gaps rather than direct findings.", label, len(recommendations))
	case status == "ok":
		return fmt.Sprintf("%s has no direct findings in the current scan; keep it in the baseline review loop.", label)
	default:
		return fmt.Sprintf("%s needs more evidence before it can be marked healthy.", label)
	}
}

func deepResearchAdvisorPillarEvidence(id string, estate deepResearchEstateSnapshot, findings []deepResearchFinding, recommendations []deepResearchSystemRecommendation, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) []string {
	evidence := deepResearchTopFindingTitles(findings, 3)
	for _, recommendation := range recommendations {
		evidence = append(evidence, recommendation.Title)
	}
	switch id {
	case "cost-efficiency":
		evidence = append(evidence, collectDeepResearchCostTrafficSignals(estate.Resources, 2)...)
	case "performance":
		if systemImprovement != nil {
			evidence = append(evidence, systemImprovement.Traffic.Evidence...)
		}
		evidence = append(evidence, collectDeepResearchTrafficHotspots(estate.Resources, 2)...)
	case "reliability":
		evidence = append(evidence, collectDeepResearchCriticalPaths(estate.Resources, 2)...)
	case "security":
		if systemImprovement != nil {
			evidence = append(evidence, systemImprovement.Architecture.Gaps...)
		}
	case "operational-excellence":
		if quality != nil {
			evidence = append(evidence, quality.ContextGaps...)
		}
		if systemImprovement != nil {
			evidence = append(evidence, systemImprovement.Codebase.Evidence...)
		}
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(evidence), 5)
}

func deepResearchAdvisorGapCount(id string, estate deepResearchEstateSnapshot, systemImprovement *deepResearchSystemImprovement, quality *deepResearchQuality) int {
	count := 0
	if systemImprovement != nil {
		switch id {
		case "cost-efficiency":
			if estate.TotalCost > 0 && len(collectDeepResearchCostTrafficSignals(estate.Resources, 1)) == 0 {
				count++
			}
		case "performance":
			count += len(systemImprovement.Traffic.Gaps)
		case "reliability", "security":
			count += len(systemImprovement.Architecture.Gaps)
		case "operational-excellence":
			count += len(systemImprovement.Codebase.Gaps)
		}
	}
	if quality != nil && id == "operational-excellence" {
		for _, check := range quality.Checks {
			status := strings.ToLower(strings.TrimSpace(check.Status))
			if status == "gap" || status == "warning" {
				count++
			}
		}
	}
	return count
}

func deepResearchAdvisorNextAction(recommendations []deepResearchSystemRecommendation, quality *deepResearchQuality, fallback string) string {
	for _, recommendation := range recommendations {
		if len(recommendation.Actions) > 0 {
			return recommendation.Actions[0]
		}
		if strings.TrimSpace(recommendation.Summary) != "" {
			return recommendation.Summary
		}
	}
	if quality != nil && len(quality.NextDataToCollect) > 0 {
		return quality.NextDataToCollect[0]
	}
	return fallback
}

func deepResearchAdvisorPriorityLabels(pillars []deepResearchAdvisorPillar, maxCount int) []string {
	labels := make([]string, 0, maxCount)
	for _, pillar := range pillars {
		if pillar.Status == "ok" {
			continue
		}
		labels = append(labels, strings.ToLower(pillar.Label))
		if len(labels) >= maxCount {
			break
		}
	}
	if len(labels) == 0 {
		for _, pillar := range pillars {
			labels = append(labels, strings.ToLower(pillar.Label))
			if len(labels) >= maxCount {
				break
			}
		}
	}
	return labels
}

func deepResearchAdvisorQualityLabel(quality *deepResearchQuality) string {
	if quality == nil {
		return ""
	}
	return fmt.Sprintf("Research confidence: %s (%d/100)", quality.Confidence, quality.Score)
}

func deepResearchTopologyCounts(resources []deepResearchResource) (edges int, typedEdges int, entrypoints int) {
	for _, resource := range resources {
		seen := make(map[string]struct{})
		for _, target := range resource.Connections {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			seen[target] = struct{}{}
		}
		for _, connection := range resource.TypedConnections {
			target := strings.TrimSpace(connection.TargetID)
			if target == "" {
				continue
			}
			seen[target] = struct{}{}
			typedEdges++
		}
		edges += len(seen)
		if isDeepResearchEntryPointType(resource.Type) || deepResearchHasTrafficRole(resource) {
			entrypoints++
		}
	}
	return edges, typedEdges, entrypoints
}

func collectDeepResearchTrafficSignals(resources []deepResearchResource, maxCount int) []string {
	signals := make([]string, 0)
	for _, resource := range resources {
		for _, signal := range deepResearchTrafficSignalLabels(resource) {
			signals = append(signals, fmt.Sprintf("%s traffic signal: %s", deepResearchResourceLabel(resource), signal))
			if maxCount > 0 && len(signals) >= maxCount {
				return signals
			}
		}
	}
	return signals
}

func collectDeepResearchTrafficHotspots(resources []deepResearchResource, maxCount int) []string {
	if maxCount <= 0 {
		return nil
	}
	inbound := make(map[string]int)
	outbound := make(map[string]int)
	for _, resource := range resources {
		seen := make(map[string]struct{})
		for _, target := range resource.Connections {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			seen[target] = struct{}{}
		}
		for _, connection := range resource.TypedConnections {
			target := strings.TrimSpace(connection.TargetID)
			if target == "" {
				continue
			}
			seen[target] = struct{}{}
		}
		outbound[resource.ID] = len(seen)
		for target := range seen {
			inbound[target]++
		}
	}

	type candidate struct {
		resource deepResearchResource
		inbound  int
		outbound int
		score    int
	}
	candidates := make([]candidate, 0, len(resources))
	for _, resource := range resources {
		score := inbound[resource.ID]*3 + outbound[resource.ID]
		if score < 3 && !isDeepResearchEntryPointType(resource.Type) {
			continue
		}
		candidates = append(candidates, candidate{resource: resource, inbound: inbound[resource.ID], outbound: outbound[resource.ID], score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return deepResearchResourceLabel(candidates[i].resource) < deepResearchResourceLabel(candidates[j].resource)
	})

	hotspots := make([]string, 0, maxCount)
	for _, candidate := range candidates {
		hotspots = append(hotspots, fmt.Sprintf("%s has %d inbound and %d outbound modeled dependencies.", deepResearchResourceLabel(candidate.resource), candidate.inbound, candidate.outbound))
		if len(hotspots) >= maxCount {
			break
		}
	}
	return hotspots
}

func collectDeepResearchCostTrafficSignals(resources []deepResearchResource, maxCount int) []string {
	type candidate struct {
		resource deepResearchResource
		key      string
		value    float64
		score    float64
	}
	candidates := make([]candidate, 0, len(resources))
	for _, resource := range resources {
		if resource.MonthlyPrice <= 0 {
			continue
		}
		key, value, ok := deepResearchPrimaryTrafficMetric(resource)
		if !ok || value <= 0 {
			continue
		}
		score := resource.MonthlyPrice / value
		if value >= 1000 {
			score = resource.MonthlyPrice / (value / 1000)
		}
		candidates = append(candidates, candidate{resource: resource, key: key, value: value, score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].resource.MonthlyPrice > candidates[j].resource.MonthlyPrice
	})
	if maxCount <= 0 {
		maxCount = len(candidates)
	}
	signals := make([]string, 0, minInt(maxCount, len(candidates)))
	for _, candidate := range candidates {
		signals = append(signals, fmt.Sprintf("%s costs $%.2f/mo with traffic metric %s=%.2f.", deepResearchResourceLabel(candidate.resource), candidate.resource.MonthlyPrice, candidate.key, candidate.value))
		if len(signals) >= maxCount {
			break
		}
	}
	return signals
}

func collectDeepResearchCriticalPaths(resources []deepResearchResource, maxCount int) []string {
	if maxCount <= 0 || len(resources) == 0 {
		return nil
	}
	byID := make(map[string]deepResearchResource, len(resources))
	adjacency := make(map[string][]string, len(resources))
	entrypoints := make([]deepResearchResource, 0)
	for _, resource := range resources {
		byID[resource.ID] = resource
	}
	for _, resource := range resources {
		seen := make(map[string]struct{})
		for _, target := range resource.Connections {
			target = strings.TrimSpace(target)
			if target != "" {
				seen[target] = struct{}{}
			}
		}
		for _, connection := range resource.TypedConnections {
			target := strings.TrimSpace(connection.TargetID)
			if target != "" {
				seen[target] = struct{}{}
			}
		}
		for target := range seen {
			if _, ok := byID[target]; ok {
				adjacency[resource.ID] = append(adjacency[resource.ID], target)
			}
		}
		if isDeepResearchEntryPointType(resource.Type) || deepResearchHasTrafficRole(resource) {
			entrypoints = append(entrypoints, resource)
		}
	}
	sort.SliceStable(entrypoints, func(i, j int) bool {
		if entrypoints[i].MonthlyPrice != entrypoints[j].MonthlyPrice {
			return entrypoints[i].MonthlyPrice > entrypoints[j].MonthlyPrice
		}
		return deepResearchResourceLabel(entrypoints[i]) < deepResearchResourceLabel(entrypoints[j])
	})

	type pathCandidate struct {
		ids   []string
		score float64
	}
	candidates := make([]pathCandidate, 0, maxCount)
	var walk func(start string, current string, visited map[string]struct{}, path []string, depth int)
	walk = func(start string, current string, visited map[string]struct{}, path []string, depth int) {
		if depth >= 4 || len(candidates) >= maxCount*4 {
			return
		}
		for _, target := range adjacency[current] {
			if _, exists := visited[target]; exists {
				continue
			}
			targetResource, ok := byID[target]
			if !ok {
				continue
			}
			nextPath := append(append([]string(nil), path...), target)
			score := float64(len(nextPath))*4 + targetResource.MonthlyPrice
			if isDeepResearchBottleneckType(targetResource.Type) || deepResearchCriticalType(targetResource.Type) != "" {
				score += 120
			}
			if len(nextPath) >= 2 && score >= 30 {
				candidates = append(candidates, pathCandidate{ids: nextPath, score: score})
			}
			nextVisited := make(map[string]struct{}, len(visited)+1)
			for id := range visited {
				nextVisited[id] = struct{}{}
			}
			nextVisited[target] = struct{}{}
			walk(start, target, nextVisited, nextPath, depth+1)
		}
		_ = start
	}
	for _, entrypoint := range entrypoints {
		walk(entrypoint.ID, entrypoint.ID, map[string]struct{}{entrypoint.ID: {}}, []string{entrypoint.ID}, 0)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return strings.Join(candidates[i].ids, ">") < strings.Join(candidates[j].ids, ">")
	})

	paths := make([]string, 0, maxCount)
	seen := make(map[string]struct{}, maxCount)
	for _, candidate := range candidates {
		labels := make([]string, 0, len(candidate.ids))
		for _, id := range candidate.ids {
			resource := byID[id]
			labels = append(labels, deepResearchResourceLabel(resource))
		}
		path := strings.Join(labels, " -> ")
		if _, ok := seen[path]; ok || path == "" {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, "Critical path candidate: "+path)
		if len(paths) >= maxCount {
			break
		}
	}
	return paths
}

func collectDeepResearchCodebaseSignals(resources []deepResearchResource, maxCount int) []string {
	signals := make([]string, 0)
	for _, resource := range resources {
		if !deepResearchHasCodebaseSignal(resource) {
			continue
		}
		label := deepResearchResourceLabel(resource)
		repo := deepResearchFirstNonEmptyAttr(resource.Attributes, "repository", "repo", "githubRepo", "repoFullName", "sourceRepo", "gitRepository")
		if repo != "" {
			signals = append(signals, fmt.Sprintf("%s links to repo %s.", label, repo))
		} else {
			signals = append(signals, fmt.Sprintf("%s is a code or repository-linked resource.", label))
		}
		if maxCount > 0 && len(signals) >= maxCount {
			break
		}
	}
	return signals
}

func collectDeepResearchDeploySignals(resources []deepResearchResource, maxCount int) []string {
	signals := make([]string, 0)
	for _, resource := range resources {
		if !deepResearchHasDeploySignal(resource) {
			continue
		}
		label := deepResearchResourceLabel(resource)
		branch := deepResearchFirstNonEmptyAttr(resource.Attributes, "branch", "gitBranch", "productionBranch")
		commit := deepResearchFirstNonEmptyAttr(resource.Attributes, "commit", "commitSha", "gitCommit", "deploymentCommit")
		detail := "deployment or runtime service"
		if branch != "" || commit != "" {
			parts := make([]string, 0, 2)
			if branch != "" {
				parts = append(parts, "branch "+branch)
			}
			if commit != "" {
				parts = append(parts, "commit "+commit)
			}
			detail = strings.Join(parts, ", ")
		}
		signals = append(signals, fmt.Sprintf("%s exposes %s.", label, detail))
		if maxCount > 0 && len(signals) >= maxCount {
			break
		}
	}
	return signals
}

func buildDeepResearchEvidenceMix(findings []deepResearchFinding) []deepResearchEvidenceMix {
	counts := map[string]int{}
	for _, finding := range findings {
		if len(finding.EvidenceDetails) == 0 {
			if len(finding.Evidence) > 0 {
				counts["heuristic"] += len(finding.Evidence)
			}
			continue
		}
		for _, evidence := range finding.EvidenceDetails {
			source := strings.ToLower(strings.TrimSpace(evidence.Source))
			if source == "" {
				source = "heuristic"
			}
			counts[source]++
		}
	}
	order := []string{"provider-drilldown", "provider-scout", "verifier", "heuristic"}
	mix := make([]deepResearchEvidenceMix, 0, len(counts))
	for _, source := range order {
		if counts[source] == 0 {
			continue
		}
		mix = append(mix, deepResearchEvidenceMix{Source: source, Label: deepResearchEvidenceSourceLabel(source), Count: counts[source]})
		delete(counts, source)
	}
	remaining := make([]string, 0, len(counts))
	for source := range counts {
		remaining = append(remaining, source)
	}
	sort.Strings(remaining)
	for _, source := range remaining {
		mix = append(mix, deepResearchEvidenceMix{Source: source, Label: deepResearchEvidenceSourceLabel(source), Count: counts[source]})
	}
	return mix
}

func deepResearchEvidenceSourceLabel(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "provider-drilldown":
		return "Live drilldown"
	case "provider-scout":
		return "Provider scout"
	case "verifier":
		return "Verifier"
	case "heuristic":
		return "Heuristic"
	default:
		return strings.Title(strings.ReplaceAll(strings.TrimSpace(source), "-", " "))
	}
}

func deepResearchEvidenceMixTotal(mix []deepResearchEvidenceMix) int {
	total := 0
	for _, item := range mix {
		total += item.Count
	}
	return total
}

func collectDeepResearchQualityGaps(checks []deepResearchQualityCheck, systemImprovement *deepResearchSystemImprovement) []string {
	gaps := make([]string, 0, 10)
	for _, check := range checks {
		switch strings.ToLower(strings.TrimSpace(check.Status)) {
		case "gap", "warning":
			gaps = append(gaps, check.Summary)
		}
	}
	if systemImprovement != nil {
		gaps = append(gaps, systemImprovement.Traffic.Gaps...)
		gaps = append(gaps, systemImprovement.Codebase.Gaps...)
		gaps = append(gaps, systemImprovement.Architecture.Gaps...)
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(gaps), 8)
}

func buildDeepResearchNextDataToCollect(checks []deepResearchQualityCheck, systemImprovement *deepResearchSystemImprovement, entrypoints int) []string {
	actions := make([]string, 0, 8)
	for _, check := range checks {
		name := strings.ToLower(strings.TrimSpace(check.Name))
		status := strings.ToLower(strings.TrimSpace(check.Status))
		if status == "ok" {
			continue
		}
		switch name {
		case "traffic telemetry":
			actions = append(actions, "Add request-rate, latency percentile, error-rate, throughput, and bandwidth metrics to entrypoints and bottlenecks.")
		case "service topology":
			actions = append(actions, "Emit typed dependency edges for request, data, auth, event, deploy, and monitoring paths.")
		case "provider evidence":
			actions = append(actions, "Enable provider credentials or scopes that allow live scouts and drilldowns to attach direct evidence.")
		case "code context":
			actions = append(actions, "Link scanned services to GitHub repos, deployment pipelines, owners, and production branches.")
		case "cost-performance correlation":
			actions = append(actions, "Put cost, utilization, and traffic metrics on the same resources so savings can be validated safely.")
		case "ownership metadata":
			actions = append(actions, "Add owner, service, environment, criticality, and repo tags to long-lived resources.")
		}
	}
	if entrypoints == 0 {
		actions = append(actions, "Mark production ingress, edge, and public API resources explicitly.")
	}
	if systemImprovement != nil && len(systemImprovement.Recommendations) > 0 {
		for _, recommendation := range systemImprovement.Recommendations {
			if len(recommendation.Actions) == 0 {
				continue
			}
			actions = append(actions, recommendation.Actions[0])
			if len(actions) >= 8 {
				break
			}
		}
	}
	return deepResearchLimitStrings(uniqueNonEmptyStrings(actions), 8)
}

func deepResearchTrafficSignalLabels(resource deepResearchResource) []string {
	if len(resource.Attributes) == 0 {
		return nil
	}
	keywords := []string{"request", "traffic", "throughput", "rps", "qps", "latency", "duration", "p95", "p99", "error", "bandwidth", "bytes", "networkin", "networkout", "invocation", "hit", "miss", "connection"}
	signals := make([]string, 0, 4)
	keys := make([]string, 0, len(resource.Attributes))
	for key := range resource.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		normalizedKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
		if !deepResearchContainsAny(normalizedKey, keywords...) {
			continue
		}
		value := deepResearchFormatAttributeValue(resource.Attributes[key])
		if value == "" {
			continue
		}
		signals = append(signals, fmt.Sprintf("%s=%s", key, value))
		if len(signals) >= 4 {
			break
		}
	}
	return signals
}

func deepResearchPrimaryTrafficMetric(resource deepResearchResource) (string, float64, bool) {
	if len(resource.Attributes) == 0 {
		return "", 0, false
	}
	preferred := []string{
		"requestsPerSecond",
		"rps",
		"qps",
		"requestRate",
		"totalRequests",
		"requests",
		"invocations",
		"invocationCount",
		"throughput",
		"bytesOut",
		"bandwidth",
	}
	for _, key := range preferred {
		if value, ok := deepResearchFloatAttr(resource.Attributes, key); ok && value > 0 {
			return key, value, true
		}
	}
	keys := make([]string, 0, len(resource.Attributes))
	for key := range resource.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		normalizedKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
		if !deepResearchContainsAny(normalizedKey, "request", "traffic", "throughput", "rps", "qps", "invocation", "bandwidth", "bytes") {
			continue
		}
		if value, ok := deepResearchFloatAttr(resource.Attributes, key); ok && value > 0 {
			return key, value, true
		}
	}
	return "", 0, false
}

func deepResearchHasTrafficRole(resource deepResearchResource) bool {
	if role := strings.ToLower(deepResearchFirstNonEmptyAttr(resource.Attributes, "role", "trafficRole", "tier", "serviceRole")); role != "" {
		return deepResearchContainsAny(role, "edge", "ingress", "api", "gateway", "frontend", "public")
	}
	return false
}

func deepResearchHasCodebaseSignal(resource deepResearchResource) bool {
	typeLower := strings.ToLower(strings.TrimSpace(resource.Type))
	if deepResearchContainsAny(typeLower, "github", "repository", "repo", "workflow", "action", "vercel", "railway", "cloudflare_worker", "pages", "deployment") {
		return true
	}
	return deepResearchFirstNonEmptyAttr(resource.Attributes, "repository", "repo", "githubRepo", "repoFullName", "sourceRepo", "gitRepository", "commit", "branch") != ""
}

func deepResearchHasDeploySignal(resource deepResearchResource) bool {
	typeLower := strings.ToLower(strings.TrimSpace(resource.Type))
	if deepResearchContainsAny(typeLower, "deploy", "deployment", "workflow", "action", "pipeline", "build", "release", "vercel", "railway", "fly", "cloudflare_worker", "pages", "service") {
		return true
	}
	return deepResearchFirstNonEmptyAttr(resource.Attributes, "deploymentId", "deployId", "buildId", "workflow", "pipeline", "commit", "branch", "productionBranch") != ""
}

func isDeepResearchEntryPointType(resourceType string) bool {
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	return deepResearchContainsAny(resourceType, "load_balancer", "loadbalancer", "alb", "elb", "nlb", "gateway", "ingress", "cloudfront", "cdn", "route53", "dns", "api_gateway", "apigateway", "cloudflare", "worker", "pages", "vercel", "railway", "fly", "frontend")
}

func deepResearchFindingsByCategory(findings []deepResearchFinding, category string) []deepResearchFinding {
	category = strings.ToLower(strings.TrimSpace(category))
	out := make([]deepResearchFinding, 0)
	for _, finding := range findings {
		if strings.EqualFold(strings.TrimSpace(finding.Category), category) {
			out = append(out, finding)
		}
	}
	return out
}

func deepResearchTopFindingTitles(findings []deepResearchFinding, maxCount int) []string {
	titles := make([]string, 0, maxCount)
	for _, finding := range findings {
		title := strings.TrimSpace(finding.Title)
		if title == "" {
			continue
		}
		titles = append(titles, title)
		if len(titles) >= maxCount {
			break
		}
	}
	return titles
}

func loadDeepResearchTrackedRepos() []string {
	raw := strings.TrimSpace(os.Getenv(runtimeGitHubTrackedReposEnv))
	if raw == "" {
		return nil
	}
	var repos []string
	if err := json.Unmarshal([]byte(raw), &repos); err != nil {
		return nil
	}
	normalized := make([]string, 0, len(repos))
	seen := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		normalized = append(normalized, repo)
	}
	sort.Strings(normalized)
	return normalized
}

func deepResearchFormatAttributeValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return ""
		}
		if len(trimmed) > 80 {
			return trimmed[:77] + "..."
		}
		return trimmed
	case float64:
		return fmt.Sprintf("%.2f", typed)
	case float32:
		return fmt.Sprintf("%.2f", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case int32:
		return fmt.Sprintf("%d", typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
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
	summary.PrimaryFocus = deepResearchPrimaryFocus(findings)
	return summary
}

func deepResearchPrimaryFocus(findings []deepResearchFinding) string {
	if len(findings) == 0 {
		return "estate-overview"
	}

	categoryScores := make(map[string]float64)
	severeNonCost := 0
	highSignalCategories := make(map[string]struct{})
	for _, finding := range findings {
		category := strings.ToLower(strings.TrimSpace(finding.Category))
		if category == "" {
			category = "estate-overview"
		}
		severity := deepResearchSeverityRank(finding.Severity)
		categoryScores[category] += deepResearchCategoryArchitectureWeight(category) + float64(severity*12) + math.Min(finding.Score/100, 8)
		if category != "cost" && severity >= 3 {
			severeNonCost++
			highSignalCategories[category] = struct{}{}
		}
	}

	bestCategory := "estate-overview"
	bestScore := -1.0
	for category, score := range categoryScores {
		if score > bestScore || (score == bestScore && category < bestCategory) {
			bestCategory = category
			bestScore = score
		}
	}
	if len(highSignalCategories) >= 3 {
		return "system-architecture"
	}
	if bestCategory == "cost" && severeNonCost > 0 {
		nonCostScore := 0.0
		for category, score := range categoryScores {
			if category != "cost" {
				nonCostScore += score
			}
		}
		if len(highSignalCategories) >= 2 || nonCostScore >= categoryScores["cost"]*0.72 {
			return "system-architecture"
		}
	}
	return bestCategory
}

func deepResearchCategoryArchitectureWeight(category string) float64 {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "misconfiguration":
		return 18
	case "resilience":
		return 16
	case "bottleneck":
		return 14
	case "cost":
		return 8
	case "hygiene":
		return 6
	default:
		return 4
	}
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
Write from the point of view of an experienced systems architect and cloud architect.
Lead with system status, architecture risk, reliability/security/performance posture, provider coverage, and the sharpest next action.
Include cost optimization only as one operating dimension, not as the default headline unless it is clearly the dominant risk.

Return strict JSON only in this shape:
{"narrative":["bullet 1","bullet 2"]}

Input:
%s`, string(payload))
}

func buildDeterministicNarrative(findings []deepResearchFinding, providers []deepResearchProviderRoll) []string {
	lines := make([]string, 0, maxDeepResearchNarrativeBullets)
	if len(providers) > 0 {
		providerLabels := make([]string, 0, minInt(3, len(providers)))
		for _, provider := range providers {
			providerLabels = append(providerLabels, fmt.Sprintf("%s (%d resources)", strings.ToUpper(provider.Provider), provider.ResourceCount))
			if len(providerLabels) >= 3 {
				break
			}
		}
		lines = append(lines, fmt.Sprintf("Reviewed %d provider groups across the estate: %s.", len(providers), strings.Join(providerLabels, ", ")))
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
		for _, provider := range buildDeepResearchProviderRollupFromEstate(estate) {
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
	for _, provider := range []string{
		deepResearchFirstNonEmptyAttr(resource.Attributes, "provider", "cloudProvider", "platform", "sourceProvider", "serviceProvider"),
		deepResearchTagValue(resource.Tags, "provider", "cloudProvider", "platform", "sourceProvider"),
	} {
		if normalized := normalizeDeepResearchProvider(provider); normalized != "" {
			return normalized
		}
	}
	typeLower := strings.ToLower(strings.TrimSpace(resource.Type))
	haystack := strings.ToLower(strings.Join([]string{
		resource.Type,
		resource.ID,
		resource.Name,
		resource.Region,
		deepResearchFirstNonEmptyAttr(resource.Attributes, "arn", "resourceArn", "providerId", "resourceId", "selfLink", "url", "account", "project", "subscription"),
	}, " "))
	switch {
	case strings.Contains(haystack, "arn:aws:") || deepResearchAWSServiceType(typeLower):
		return "aws"
	case strings.Contains(typeLower, "azure") || strings.Contains(haystack, "microsoft.compute") || strings.Contains(haystack, "microsoft.") || strings.HasPrefix(typeLower, "azure_"):
		return "azure"
	case strings.Contains(typeLower, "gcp") || strings.Contains(typeLower, "google") || strings.Contains(haystack, "googleapis.com") || strings.Contains(haystack, "projects/"):
		return "gcp"
	case strings.Contains(typeLower, "kubernetes") || strings.Contains(typeLower, "k8s"):
		return "k8s"
	case strings.Contains(typeLower, "cloudflare") || strings.HasPrefix(typeLower, "cf_"):
		return "cloudflare"
	case strings.Contains(typeLower, "digitalocean") || strings.HasPrefix(typeLower, "do_") || strings.Contains(typeLower, "droplet"):
		return "digitalocean"
	case strings.Contains(typeLower, "github"):
		return "github"
	case strings.Contains(typeLower, "hetzner") || strings.Contains(typeLower, "hcloud") || strings.HasPrefix(typeLower, "hz_"):
		return "hetzner"
	case strings.Contains(typeLower, "flyio") || strings.Contains(typeLower, "fly.io") || strings.HasPrefix(typeLower, "fly_"):
		return "flyio"
	case strings.Contains(typeLower, "railway"):
		return "railway"
	case strings.Contains(typeLower, "supabase"):
		return "supabase"
	case strings.Contains(typeLower, "tencent"):
		return "tencent"
	case strings.Contains(typeLower, "verda"):
		return "verda"
	case strings.Contains(typeLower, "vercel"):
		return "vercel"
	case strings.Contains(typeLower, "terraform"):
		return "terraform"
	case strings.Contains(typeLower, "sentry"):
		return "sentry"
	case strings.Contains(typeLower, "linear"):
		return "linear"
	case strings.Contains(typeLower, "notion"):
		return "notion"
	default:
		return "unknown"
	}
}

func normalizeDeepResearchProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "", "<nil>":
		return ""
	case "amazon", "amazon web services", "aws", "ec2", "amazon ec2", "linux/unix", "linux/unix usage", "linux unix":
		return "aws"
	case "google", "google cloud", "google cloud platform", "gcp":
		return "gcp"
	case "microsoft", "microsoft azure", "azure":
		return "azure"
	case "kubernetes", "k8s":
		return "k8s"
	case "cloudflare", "digitalocean", "hetzner", "flyio", "fly.io", "railway", "supabase", "vercel", "verda", "tencent", "terraform", "github", "sentry", "linear", "notion":
		return strings.ReplaceAll(provider, "fly.io", "flyio")
	default:
		return provider
	}
}

func deepResearchAWSServiceType(resourceType string) bool {
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	switch resourceType {
	case "apigateway", "apigateway_vpc_link", "apprunner", "appsync", "athena", "backup", "batch", "bedrock", "cloudfront", "cloudtrail", "cloudwatch", "codebuild", "codecommit", "codepipeline", "config", "documentdb", "dynamodb", "ebs", "ec2", "ecr", "ecs", "eks", "elasticache", "elb", "emr", "eventbridge", "fsx", "glue", "iam_policy", "iam_role", "kinesis", "kms", "lambda", "memorydb", "neptune", "opensearch", "pipes", "rds", "route53", "s3", "scheduler", "secretsmanager", "security-group", "securitygroup", "securitylake", "sfn", "sns", "sqs", "stepfunctions", "timestream", "verifiedpermissions", "vpc", "waf":
		return true
	default:
		return strings.HasPrefix(resourceType, "aws_") || strings.HasPrefix(resourceType, "aws-")
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
			fmt.Sprintf("Which traffic, utilization, or unit metric explains the spend on %s?", label),
		}
	case "idle-cost":
		return []string{
			fmt.Sprintf("Can I delete or hibernate %s without breaking anything?", label),
			fmt.Sprintf("What still depends on %s?", label),
			fmt.Sprintf("What last activity, owner approval, and rollback plan prove %s is safe to retire?", label),
		}
	case "untagged-cost":
		return []string{
			fmt.Sprintf("Who owns %s and which environment is it part of?", label),
			fmt.Sprintf("What tags should I add to %s for cost attribution?", label),
			fmt.Sprintf("Which repo, deploy pipeline, or service map should own %s?", label),
		}
	case "topology-bottleneck":
		return []string{
			fmt.Sprintf("How risky is %s as a bottleneck on the current request path?", label),
			fmt.Sprintf("What scaling or redundancy changes would reduce risk around %s?", label),
			fmt.Sprintf("What are latency, traffic, error-rate, and saturation values around %s?", label),
		}
	case "orphan-cost":
		return []string{
			fmt.Sprintf("Is %s still serving production traffic?", label),
			fmt.Sprintf("Why does %s have no visible connections in the estate graph?", label),
			fmt.Sprintf("Which owner, logs, or access events prove whether %s is real workload or waste?", label),
		}
	case "single-point":
		return []string{
			fmt.Sprintf("How do I remove %s as a single point of failure?", label),
			fmt.Sprintf("What is the blast radius if %s fails?", label),
			fmt.Sprintf("What backup, failover, restore, and alert evidence exists for %s?", label),
		}
	case "degraded-state":
		return []string{
			fmt.Sprintf("Why is %s currently %s?", label, strings.TrimSpace(resource.State)),
			fmt.Sprintf("What should I inspect first on %s?", label),
			fmt.Sprintf("Which recent logs, events, deploys, or saturation changes explain %s?", label),
		}
	case "public-database":
		return []string{
			fmt.Sprintf("Why does %s still have a public database address?", label),
			fmt.Sprintf("How do I move %s onto private networking without breaking clients?", label),
			fmt.Sprintf("Which clients, security groups, IAM identities, and backup paths touch %s?", label),
		}
	case "disabled-backups":
		return []string{
			fmt.Sprintf("What is the backup and restore path for %s today?", label),
			fmt.Sprintf("How do I enable backups on %s safely?", label),
			fmt.Sprintf("What restore test and recovery objective should %s satisfy?", label),
		}
	case "unencrypted-data":
		return []string{
			fmt.Sprintf("How do I enable encryption at rest on %s?", label),
			fmt.Sprintf("What migration or downtime risk comes with encrypting %s?", label),
			fmt.Sprintf("Which data class, key policy, and verification step apply to %s?", label),
		}
	case "public-compute":
		return []string{
			fmt.Sprintf("Should %s really be reachable from the public internet?", label),
			fmt.Sprintf("What is the safest edge pattern for %s?", label),
			fmt.Sprintf("Which firewall, identity, logging, and owner controls prove %s is intentionally public?", label),
		}
	case "stale-resource":
		return []string{
			fmt.Sprintf("Is %s still serving anything real?", label),
			fmt.Sprintf("Who owns %s and can I retire it safely?", label),
			fmt.Sprintf("What dependency, access, billing, or deploy signal would block removing %s?", label),
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

func deepResearchTagValue(tags map[string]string, keys ...string) string {
	if len(tags) == 0 {
		return ""
	}
	for _, key := range keys {
		for tagKey, value := range tags {
			if strings.EqualFold(strings.TrimSpace(tagKey), strings.TrimSpace(key)) {
				return strings.TrimSpace(value)
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

func deepResearchFloatAttr(attrs map[string]interface{}, key string) (float64, bool) {
	value, ok := attrs[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		trimmed := strings.TrimSpace(strings.TrimSuffix(typed, "%"))
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
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
