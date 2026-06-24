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
