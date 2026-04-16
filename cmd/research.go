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
	"github.com/bgdnvk/clanker/internal/digitalocean"
	"github.com/bgdnvk/clanker/internal/gcp"
	"github.com/bgdnvk/clanker/internal/hetzner"
	tfclient "github.com/bgdnvk/clanker/internal/terraform"
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
	ID               string                           `json:"id"`
	Type             string                           `json:"type"`
	Name             string                           `json:"name"`
	Region           string                           `json:"region"`
	State            string                           `json:"state"`
	CreatedAt        string                           `json:"createdAt,omitempty"`
	Tags             map[string]string                `json:"tags,omitempty"`
	Attributes       map[string]interface{}           `json:"attributes,omitempty"`
	MonthlyPrice     float64                          `json:"monthlyPrice,omitempty"`
	Connections      []string                         `json:"connections,omitempty"`
	TypedConnections []deepResearchResourceConnection `json:"typedConnections,omitempty"`
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

type deepResearchFinding struct {
	ID                      string   `json:"id"`
	Severity                string   `json:"severity"`
	Category                string   `json:"category"`
	Title                   string   `json:"title"`
	Summary                 string   `json:"summary"`
	ResourceID              string   `json:"resourceId,omitempty"`
	ResourceName            string   `json:"resourceName,omitempty"`
	ResourceType            string   `json:"resourceType,omitempty"`
	Provider                string   `json:"provider,omitempty"`
	Region                  string   `json:"region,omitempty"`
	MonthlyCost             float64  `json:"monthlyCost,omitempty"`
	EstimatedMonthlySavings float64  `json:"estimatedMonthlySavings,omitempty"`
	Risk                    string   `json:"risk,omitempty"`
	Score                   float64  `json:"score,omitempty"`
	Evidence                []string `json:"evidence,omitempty"`
	Questions               []string `json:"questions,omitempty"`
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
	subagents := []deepResearchSubagent{
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

	subagents = append(subagents, buildLiveProviderSubagents(estate, options)...)

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
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Name < runs[j].Name
	})
	return dedupeDeepResearchFindings(findings), runs, uniqueNonEmptyStrings(warnings)
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
	case strings.Contains(typeLower, "cloudflare") || strings.HasPrefix(typeLower, "cf_"):
		return "cloudflare"
	case strings.Contains(typeLower, "digitalocean") || strings.HasPrefix(typeLower, "do_"):
		return "digitalocean"
	case strings.Contains(typeLower, "hetzner") || strings.HasPrefix(typeLower, "hz_"):
		return "hetzner"
	case strings.Contains(typeLower, "supabase"):
		return "supabase"
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
