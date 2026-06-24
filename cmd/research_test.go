package cmd

import (
	"strings"
	"testing"
)

func TestBuildDeepResearchSystemImprovementUsesTrafficAndCodeSignals(t *testing.T) {
	t.Setenv(runtimeGitHubTrackedReposEnv, `["nov/clanker-cloud","nov/api"]`)

	estate := deepResearchEstateSnapshot{
		Resources: []deepResearchResource{
			{
				ID:           "edge",
				Type:         "aws_api_gateway",
				Name:         "public-api",
				Region:       "us-east-1",
				MonthlyPrice: 120,
				Attributes: map[string]interface{}{
					"requestsPerSecond": float64(140),
					"p95LatencyMs":      float64(320),
					"role":              "edge",
				},
				Tags:             map[string]string{"owner": "platform", "env": "prod"},
				Connections:      []string{"service"},
				TypedConnections: []deepResearchResourceConnection{{TargetID: "service", ConnectionType: "request", Label: "HTTP"}},
			},
			{
				ID:           "service",
				Type:         "vercel_deployment",
				Name:         "web-service",
				Region:       "us-east-1",
				MonthlyPrice: 80,
				Attributes: map[string]interface{}{
					"repository": "nov/clanker-cloud",
					"branch":     "main",
				},
				Tags:        map[string]string{"service": "web"},
				Connections: []string{"db"},
			},
			{
				ID:           "db",
				Type:         "rds_instance",
				Name:         "primary-db",
				Region:       "us-east-1",
				MonthlyPrice: 300,
				Tags:         map[string]string{"owner": "data"},
			},
		},
		TotalCost: 500,
	}
	findings := []deepResearchFinding{
		{ID: "topology-bottleneck-db", Category: "bottleneck", Title: "primary-db is a concentration point"},
		{ID: "single-point-database", Category: "resilience", Title: "Only one database resource is visible"},
	}

	section := buildDeepResearchSystemImprovement(estate, findings, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), buildDeepResearchQuestionPlan("optimize traffic and architecture", estate))
	if section == nil {
		t.Fatal("expected system improvement section")
	}
	if section.Traffic.Status == "gap" {
		t.Fatalf("traffic should not be a gap when request metrics exist: %+v", section.Traffic)
	}
	if !containsAnyJoined(section.Traffic.Evidence, "requestsPerSecond") {
		t.Fatalf("traffic evidence missing request metric: %+v", section.Traffic.Evidence)
	}
	if section.Codebase.Status == "gap" {
		t.Fatalf("codebase should not be a gap with tracked repos and repo attributes: %+v", section.Codebase)
	}
	if !containsRecommendation(section.Recommendations, "dependency-map") {
		t.Fatalf("expected dependency-map recommendation, got %+v", section.Recommendations)
	}
	paths := collectDeepResearchCriticalPaths(estate.Resources, 3)
	if !containsAnyJoined(paths, "public-api -> web-service -> primary-db") {
		t.Fatalf("expected critical path through API, service, and database, got %+v", paths)
	}
	costTraffic := collectDeepResearchCostTrafficSignals(estate.Resources, 3)
	if !containsAnyJoined(costTraffic, "requestsPerSecond") {
		t.Fatalf("expected cost-traffic correlation signal, got %+v", costTraffic)
	}

	quality := buildDeepResearchQuality(estate, []deepResearchFinding{{
		ID:       "cost-driver-db",
		Category: "cost",
		Title:    "primary-db is a top cost driver",
		EvidenceDetails: []deepResearchEvidenceDetail{{
			Detail:   "AWS live scout reviewed utilization.",
			Source:   "provider-scout",
			Provider: "aws",
		}},
	}}, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), section)
	if quality == nil {
		t.Fatal("expected research quality")
	}
	if quality.Score <= 50 {
		t.Fatalf("expected medium-or-better quality score, got %+v", quality)
	}
	if !containsAnyJoined(quality.NextDataToCollect, "drilldowns") {
		t.Fatalf("expected next data to include live drilldown guidance, got %+v", quality.NextDataToCollect)
	}

	benchmarks := buildDeepResearchAdvisorBenchmarks(estate, []deepResearchFinding{{
		ID:       "topology-bottleneck-db",
		Category: "bottleneck",
		Severity: "high",
		Title:    "primary-db is a concentration point",
	}}, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), section, quality)
	if benchmarks == nil {
		t.Fatal("expected advisor benchmarks")
	}
	if !containsAdvisorPillar(benchmarks.Pillars, "performance") {
		t.Fatalf("expected performance advisor pillar, got %+v", benchmarks.Pillars)
	}
	if !containsAnyJoined(benchmarks.Pillars[0].Evidence, "public-api") && !containsAnyJoined(benchmarks.Pillars[0].Evidence, "primary-db") {
		t.Fatalf("expected advisor evidence to include topology or traffic context, got %+v", benchmarks.Pillars[0])
	}
	if len(benchmarks.Lessons) == 0 || len(benchmarks.Workflow) == 0 {
		t.Fatalf("expected product lessons and workflow, got %+v", benchmarks)
	}

	expertTeam := buildDeepResearchExpertTeam(estate, []deepResearchFinding{
		{
			ID:       "topology-bottleneck-db",
			Category: "bottleneck",
			Severity: "high",
			Title:    "primary-db is a concentration point",
		},
		{
			ID:       "public-database-db",
			Category: "misconfiguration",
			Severity: "critical",
			Title:    "primary-db is publicly reachable",
		},
	}, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), section, quality, benchmarks)
	if expertTeam == nil {
		t.Fatal("expected expert team synthesis")
	}
	if !containsExpertPersona(expertTeam.Personas, "systems-architect") {
		t.Fatalf("expected systems architect persona, got %+v", expertTeam.Personas)
	}
	if !containsExpertPersona(expertTeam.Personas, "site-reliability-engineer") {
		t.Fatalf("expected SRE persona, got %+v", expertTeam.Personas)
	}
	if !containsExpertPersona(expertTeam.Personas, "finops-analyst") {
		t.Fatalf("expected FinOps persona, got %+v", expertTeam.Personas)
	}
	if !containsTeamConclusion(expertTeam.Conclusions, "system-optimization") {
		t.Fatalf("expected system optimization conclusion, got %+v", expertTeam.Conclusions)
	}
	if !containsTeamConclusion(expertTeam.Conclusions, "cost-optimization") {
		t.Fatalf("expected cost optimization conclusion, got %+v", expertTeam.Conclusions)
	}
	if !containsAnyJoined(expertTeam.Consensus, "cost is one dimension") {
		t.Fatalf("expected consensus to avoid cost-only framing, got %+v", expertTeam.Consensus)
	}
	if len(expertTeam.AgentRuns) < len(expertTeam.Personas) {
		t.Fatalf("expected each expert persona to produce an agent run, got personas=%d runs=%d", len(expertTeam.Personas), len(expertTeam.AgentRuns))
	}
	if !containsExpertAgentRun(expertTeam.AgentRuns, "expert-agent-systems-architect") {
		t.Fatalf("expected systems architect agent run, got %+v", expertTeam.AgentRuns)
	}
	if !containsTeamDialogue(expertTeam.Dialogues, "provider-coverage") {
		t.Fatalf("expected provider coverage team dialogue, got %+v", expertTeam.Dialogues)
	}
	subagentRuns := buildDeepResearchExpertSubagentRuns(expertTeam)
	if !containsSubagentRun(subagentRuns, "expert-dialogue-provider-coverage") {
		t.Fatalf("expected expert dialogue to be represented as a subagent run, got %+v", subagentRuns)
	}
}

func TestBuildDeepResearchSystemImprovementSurfacesTelemetryAndCodebaseGaps(t *testing.T) {
	t.Setenv(runtimeGitHubTrackedReposEnv, "")

	estate := deepResearchEstateSnapshot{
		Resources: []deepResearchResource{
			{
				ID:           "db",
				Type:         "rds_instance",
				Name:         "primary-db",
				Region:       "us-east-1",
				MonthlyPrice: 240,
				Attributes:   map[string]interface{}{},
			},
		},
		TotalCost: 240,
	}

	section := buildDeepResearchSystemImprovement(estate, nil, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), buildDeepResearchQuestionPlan("research everything", estate))
	if section == nil {
		t.Fatal("expected system improvement section")
	}
	if section.Traffic.Status != "gap" {
		t.Fatalf("traffic should be a gap without metrics or topology, got %+v", section.Traffic)
	}
	if !containsAnyJoined(section.Traffic.Gaps, "No direct request") {
		t.Fatalf("traffic gaps should mention missing request metrics: %+v", section.Traffic.Gaps)
	}
	if section.Codebase.Status != "gap" {
		t.Fatalf("codebase should be a gap without repo signals, got %+v", section.Codebase)
	}
	if !containsRecommendation(section.Recommendations, "traffic-observability") {
		t.Fatalf("expected traffic-observability recommendation, got %+v", section.Recommendations)
	}
	if !containsRecommendation(section.Recommendations, "code-to-infra-ownership") {
		t.Fatalf("expected code-to-infra-ownership recommendation, got %+v", section.Recommendations)
	}

	quality := buildDeepResearchQuality(estate, nil, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), section)
	if quality == nil {
		t.Fatal("expected research quality")
	}
	if quality.Confidence != "low" {
		t.Fatalf("expected low confidence with missing telemetry and repo context, got %+v", quality)
	}
	if !containsAnyJoined(quality.ContextGaps, "No direct request") {
		t.Fatalf("expected quality gaps to include missing telemetry, got %+v", quality.ContextGaps)
	}

	benchmarks := buildDeepResearchAdvisorBenchmarks(estate, nil, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), section, quality)
	if benchmarks == nil {
		t.Fatal("expected advisor benchmarks")
	}
	if !containsAdvisorPillar(benchmarks.Pillars, "operational-excellence") {
		t.Fatalf("expected operational excellence pillar, got %+v", benchmarks.Pillars)
	}
	if !containsAnyJoined(benchmarks.Workflow[1].Inputs, "request-rate") {
		t.Fatalf("expected workflow to carry missing telemetry guidance, got %+v", benchmarks.Workflow)
	}

	expertTeam := buildDeepResearchExpertTeam(estate, nil, buildDeepResearchProviderRollup(estate.Resources, estate.TotalCost), section, quality, benchmarks)
	if expertTeam == nil {
		t.Fatal("expected expert team synthesis")
	}
	if !containsExpertPersona(expertTeam.Personas, "cloud-architect") {
		t.Fatalf("expected cloud architect persona, got %+v", expertTeam.Personas)
	}
	if !containsTeamConclusion(expertTeam.Conclusions, "what-is-wrong") {
		t.Fatalf("expected what-is-wrong conclusion, got %+v", expertTeam.Conclusions)
	}
	if expertTeam.Conclusions[1].Status == "ok" {
		t.Fatalf("expected evidence gaps to keep what-is-wrong out of ok status, got %+v", expertTeam.Conclusions[1])
	}
	if !containsAnyJoined(expertTeam.Conclusions[1].NextActions, "request-rate") && !strings.Contains(expertTeam.Conclusions[1].Summary, "incomplete evidence") {
		t.Fatalf("expected team conclusion to surface evidence gaps, got %+v", expertTeam.Conclusions[1])
	}
}

func TestDeepResearchProviderInferenceDoesNotDefaultToAWS(t *testing.T) {
	resources := []deepResearchResource{
		{ID: "gcp-run", Type: "gcp_cloudrun_service", MonthlyPrice: 40},
		{ID: "cf-worker", Type: "cf_worker", MonthlyPrice: 20},
		{ID: "railway-api", Type: "railway_service", MonthlyPrice: 15},
		{ID: "generic-service", Type: "service", MonthlyPrice: 10},
		{ID: "arn:aws:rds:us-east-1:123:db:prod", Type: "database", MonthlyPrice: 30},
	}

	if provider := inferDeepResearchProvider(resources[0]); provider != "gcp" {
		t.Fatalf("expected gcp provider, got %q", provider)
	}
	if provider := inferDeepResearchProvider(resources[1]); provider != "cloudflare" {
		t.Fatalf("expected cloudflare provider, got %q", provider)
	}
	if provider := inferDeepResearchProvider(resources[2]); provider != "railway" {
		t.Fatalf("expected railway provider, got %q", provider)
	}
	if provider := inferDeepResearchProvider(resources[3]); provider != "unknown" {
		t.Fatalf("generic resource should not default to aws, got %q", provider)
	}
	if provider := inferDeepResearchProvider(resources[4]); provider != "aws" {
		t.Fatalf("arn-backed resource should infer aws, got %q", provider)
	}

	rollup := buildDeepResearchProviderRollup(resources, 115)
	if !containsProviderRollup(rollup, "unknown") {
		t.Fatalf("expected unknown provider bucket for generic resources, got %+v", rollup)
	}
}

func TestDeepResearchBillingSummaryOverridesResourcePricingAndNormalizesProviders(t *testing.T) {
	t.Setenv(runtimeDeepResearchEstateEnv, `{
		"resources": [
			{"id":"i-123","type":"ec2","name":"eval-dev","region":"us-east-1","monthlyPrice":121.47},
			{"id":"eks-main","type":"eks","name":"clanker-cluster","region":"us-east-1","monthlyPrice":73.00},
			{"id":"api","type":"service","name":"api","region":"us-east-1","monthlyPrice":75.63}
		],
		"totalCost": 270.10,
		"costSummary": {
			"totalCost": 579.12,
			"providerCosts": [
				{
					"provider": "Linux/Unix",
					"totalCost": 579.12,
					"serviceBreakdown": [
						{"service":"Amazon Elastic Compute Cloud - Compute","cost":421.12,"resourceCount":2},
						{"service":"Amazon Relational Database Service","cost":158.00,"resourceCount":1}
					]
				}
			]
		}
	}`)

	estate, warnings := loadDeepResearchEstateSnapshot()
	if len(warnings) > 0 {
		t.Fatalf("did not expect warnings, got %+v", warnings)
	}
	if estate.TotalCost != 579.12 {
		t.Fatalf("expected billing total to override resource total, got %.2f", estate.TotalCost)
	}
	providers := buildDeepResearchProviderRollupFromEstate(estate)
	if !containsProviderRollup(providers, "aws") {
		t.Fatalf("expected Linux/Unix billing provider to normalize to aws, got %+v", providers)
	}
	if containsProviderRollup(providers, "linux/unix") {
		t.Fatalf("did not expect raw Linux/Unix provider bucket, got %+v", providers)
	}
	if len(providers) == 0 || providers[0].MonthlyCost != 579.12 {
		t.Fatalf("expected provider cost from billing summary, got %+v", providers)
	}
	if _, ok := buildDeepResearchProviderSet(estate)["aws"]; !ok {
		t.Fatalf("expected billing provider to be available for scout selection")
	}
	findings := buildCostFindings(estate, providers)
	if !containsFindingWithPrefix(findings, "billing-coverage-gap") {
		t.Fatalf("expected billing coverage gap finding, got %+v", findings)
	}
	if !containsFindingWithPrefix(findings, "billing-service-driver") {
		t.Fatalf("expected billing service driver finding, got %+v", findings)
	}
}

func TestDeepResearchPrimaryFocusBalancesArchitectureRisks(t *testing.T) {
	findings := []deepResearchFinding{
		{Category: "cost", Severity: "critical", Score: 400, Title: "Large instance is expensive"},
		{Category: "misconfiguration", Severity: "critical", Score: 260, Title: "Database is public"},
		{Category: "resilience", Severity: "high", Score: 220, Title: "Only one database is visible"},
		{Category: "bottleneck", Severity: "high", Score: 190, Title: "API gateway is saturated"},
	}

	focus := deepResearchPrimaryFocus(findings)
	if focus != "system-architecture" {
		t.Fatalf("expected mixed high-risk estate to produce system-architecture focus, got %q", focus)
	}
}

func containsAnyJoined(values []string, needle string) bool {
	return strings.Contains(strings.Join(values, "\n"), needle)
}

func containsRecommendation(recommendations []deepResearchSystemRecommendation, id string) bool {
	for _, recommendation := range recommendations {
		if recommendation.ID == id {
			return true
		}
	}
	return false
}

func containsAdvisorPillar(pillars []deepResearchAdvisorPillar, id string) bool {
	for _, pillar := range pillars {
		if pillar.ID == id {
			return true
		}
	}
	return false
}

func containsProviderRollup(providers []deepResearchProviderRoll, provider string) bool {
	for _, roll := range providers {
		if roll.Provider == provider {
			return true
		}
	}
	return false
}

func containsExpertPersona(personas []deepResearchExpertPersona, id string) bool {
	for _, persona := range personas {
		if persona.ID == id {
			return true
		}
	}
	return false
}

func containsTeamConclusion(conclusions []deepResearchTeamConclusion, id string) bool {
	for _, conclusion := range conclusions {
		if conclusion.ID == id {
			return true
		}
	}
	return false
}

func containsExpertAgentRun(runs []deepResearchExpertAgentRun, id string) bool {
	for _, run := range runs {
		if run.ID == id {
			return true
		}
	}
	return false
}

func containsTeamDialogue(dialogues []deepResearchTeamDialogue, id string) bool {
	for _, dialogue := range dialogues {
		if dialogue.ID == id {
			return true
		}
	}
	return false
}

func containsSubagentRun(runs []deepResearchSubagentRun, name string) bool {
	for _, run := range runs {
		if run.Name == name {
			return true
		}
	}
	return false
}

func containsFindingWithPrefix(findings []deepResearchFinding, prefix string) bool {
	for _, finding := range findings {
		if strings.HasPrefix(finding.ID, prefix) {
			return true
		}
	}
	return false
}
