package telemetry

import (
	"strings"
	"testing"
)

func TestGetAKSAzureMonitorHints(t *testing.T) {
	hints := GetAKSAzureMonitorHints()

	if len(hints) == 0 {
		t.Error("expected at least one Azure Monitor hint")
	}

	hintsText := strings.Join(hints, " ")

	expectedTopics := []string{
		"Azure Monitor",
		"Container Insights",
		"Azure Portal",
		"alerting",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(hintsText, topic) {
			t.Errorf("Azure Monitor hints should mention %s", topic)
		}
	}
}

func TestGetAKSContainerInsightsNotes(t *testing.T) {
	notes := GetAKSContainerInsightsNotes()

	if len(notes) == 0 {
		t.Error("expected at least one Container Insights note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Container Insights",
		"az aks",
		"Log Analytics",
		"workbooks",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("Container Insights notes should mention %s", topic)
		}
	}
}

func TestGetAKSManagedPrometheusNotes(t *testing.T) {
	notes := GetAKSManagedPrometheusNotes()

	if len(notes) == 0 {
		t.Error("expected at least one Managed Prometheus note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Prometheus",
		"Grafana",
		"PromQL",
		"PodMonitor",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("Managed Prometheus notes should mention %s", topic)
		}
	}
}

func TestAKSMetricsAzCommand(t *testing.T) {
	cmd := AKSMetricsAzCommand("sub-id", "my-rg", "my-cluster", "cpuUsageMillicores")

	expectedContents := []string{
		"az monitor metrics list",
		"sub-id",
		"my-rg",
		"my-cluster",
		"cpuUsageMillicores",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(cmd, expected) {
			t.Errorf("az command should contain %s", expected)
		}
	}
}

func TestAKSDashboardURL(t *testing.T) {
	url := AKSDashboardURL("sub-id", "my-rg", "my-cluster")

	if !strings.Contains(url, "portal.azure.com") {
		t.Error("dashboard URL should contain portal.azure.com")
	}

	if !strings.Contains(url, "sub-id") {
		t.Error("dashboard URL should contain subscription ID")
	}

	if !strings.Contains(url, "my-rg") {
		t.Error("dashboard URL should contain resource group")
	}

	if !strings.Contains(url, "my-cluster") {
		t.Error("dashboard URL should contain cluster name")
	}
}

func TestAKSMetricsExplorerURL(t *testing.T) {
	url := AKSMetricsExplorerURL("sub-id", "my-rg")

	if !strings.Contains(url, "portal.azure.com") {
		t.Error("metrics explorer URL should contain portal.azure.com")
	}

	if !strings.Contains(url, "metrics") {
		t.Error("metrics explorer URL should contain 'metrics'")
	}
}

func TestAKSLogAnalyticsQueries(t *testing.T) {
	queries := AKSLogAnalyticsQueries()

	if len(queries) == 0 {
		t.Error("expected at least one Log Analytics query")
	}

	expectedQueries := []string{
		"container_cpu",
		"container_memory",
		"pod_restarts",
		"node_cpu",
		"node_memory",
		"container_logs",
		"failed_pods",
		"node_conditions",
	}

	for _, query := range expectedQueries {
		if _, ok := queries[query]; !ok {
			t.Errorf("expected query %s not found", query)
		}
	}

	// Verify queries contain KQL elements
	for name, query := range queries {
		if !strings.Contains(query, "|") {
			t.Errorf("query %s should contain KQL pipe operator", name)
		}
	}
}

func TestAKSAlertingPolicySuggestions(t *testing.T) {
	policies := AKSAlertingPolicySuggestions()

	if len(policies) == 0 {
		t.Error("expected at least one alerting policy")
	}

	hasCPUAlert := false
	hasMemoryAlert := false
	hasNodeAlert := false

	for _, policy := range policies {
		if strings.Contains(policy.Name, "CPU") {
			hasCPUAlert = true
		}
		if strings.Contains(policy.Name, "Memory") {
			hasMemoryAlert = true
		}
		if strings.Contains(policy.Name, "Node") {
			hasNodeAlert = true
		}

		if policy.Metric == "" {
			t.Errorf("policy %s should have a metric", policy.Name)
		}
		if policy.Condition == "" {
			t.Errorf("policy %s should have a condition", policy.Name)
		}
		if policy.Severity == "" {
			t.Errorf("policy %s should have a severity", policy.Name)
		}
	}

	if !hasCPUAlert {
		t.Error("expected CPU alerting policy")
	}
	if !hasMemoryAlert {
		t.Error("expected memory alerting policy")
	}
	if !hasNodeAlert {
		t.Error("expected node alerting policy")
	}
}

func TestGetAKSMetricsRecommendation(t *testing.T) {
	tests := []struct {
		name           string
		useCase        string
		wantSolution   string
		wantComponents int
	}{
		{
			name:           "Production",
			useCase:        "production critical workload",
			wantSolution:   "Container Insights with Azure Managed Prometheus",
			wantComponents: 3,
		},
		{
			name:           "Cost-sensitive",
			useCase:        "budget cost minimal",
			wantSolution:   "Built-in metrics-server only",
			wantComponents: 1,
		},
		{
			name:           "Prometheus",
			useCase:        "existing prometheus grafana setup",
			wantSolution:   "Azure Managed Prometheus",
			wantComponents: 3,
		},
		{
			name:           "Default",
			useCase:        "general application",
			wantSolution:   "Container Insights",
			wantComponents: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetAKSMetricsRecommendation(tt.useCase)

			if rec.Solution != tt.wantSolution {
				t.Errorf("Solution = %s, want %s", rec.Solution, tt.wantSolution)
			}

			if len(rec.Components) < tt.wantComponents {
				t.Errorf("expected at least %d components, got %d", tt.wantComponents, len(rec.Components))
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if len(rec.Notes) == 0 {
				t.Error("recommendation should have notes")
			}
		})
	}
}

func TestAKSLoggingIntegration(t *testing.T) {
	logging := AKSLoggingIntegration()

	if len(logging) == 0 {
		t.Error("expected logging integration entries")
	}

	expectedKeys := []string{
		"default_sink",
		"log_types",
		"retention",
		"query_language",
		"export_options",
	}

	for _, key := range expectedKeys {
		if _, ok := logging[key]; !ok {
			t.Errorf("expected logging key %s", key)
		}
	}

	if logging["query_language"] != "Kusto Query Language (KQL)" {
		t.Errorf("expected KQL query language, got %s", logging["query_language"])
	}
}

func TestIsAKSMetricsSource(t *testing.T) {
	tests := []struct {
		source MetricsSource
		want   bool
	}{
		{SourceAKSAzureMonitor, true},
		{SourceAKSContainerInsights, true},
		{SourceAKSManagedPrometheus, true},
		{SourceGKECloudMonitoring, false},
		{SourceGKEManagedPrometheus, false},
		{SourceMetricsServer, false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			got := IsAKSMetricsSource(tt.source)
			if got != tt.want {
				t.Errorf("IsAKSMetricsSource(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestGetAKSMetricDescription(t *testing.T) {
	metricTypes := []AKSMetricType{
		AKSMetricContainerCPU,
		AKSMetricContainerMemory,
		AKSMetricContainerRestarts,
		AKSMetricNodeCPUPercent,
		AKSMetricNodeMemoryPercent,
		AKSMetricClusterPodCount,
	}

	for _, metricType := range metricTypes {
		desc := GetAKSMetricDescription(metricType)

		if desc == "" {
			t.Errorf("expected description for metric type %s", metricType)
		}

		if desc == "Unknown metric type" {
			t.Errorf("expected specific description for %s, got unknown", metricType)
		}
	}

	// Test unknown metric type
	unknownDesc := GetAKSMetricDescription("unknown-metric")
	if unknownDesc != "Unknown metric type" {
		t.Errorf("expected 'Unknown metric type' for unknown metric, got %s", unknownDesc)
	}
}

func TestAKSTelemetryNotes(t *testing.T) {
	notes := AKSTelemetryNotes()

	if len(notes) == 0 {
		t.Error("expected at least one telemetry note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Container Insights",
		"Prometheus",
		"Log Analytics",
		"Azure Monitor",
		"Grafana",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("telemetry notes should mention %s", topic)
		}
	}
}

func TestGKETelemetryComparisonWithAKS(t *testing.T) {
	comparison := GKETelemetryComparisonWithAKS()

	if len(comparison) == 0 {
		t.Error("expected telemetry comparison entries")
	}

	// Verify AKS entries
	aksKeys := []string{"aks_monitoring", "aks_prometheus", "aks_logging", "aks_dashboard", "aks_grafana"}
	for _, key := range aksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_monitoring", "gke_prometheus", "gke_logging", "gke_dashboard", "gke_grafana"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_monitoring", "eks_prometheus", "eks_logging", "eks_dashboard", "eks_grafana"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestAKSMetricsSourceConstants(t *testing.T) {
	if SourceAKSAzureMonitor != "azure-monitor" {
		t.Errorf("SourceAKSAzureMonitor = %s, want azure-monitor", SourceAKSAzureMonitor)
	}

	if SourceAKSContainerInsights != "container-insights" {
		t.Errorf("SourceAKSContainerInsights = %s, want container-insights", SourceAKSContainerInsights)
	}

	if SourceAKSManagedPrometheus != "azure-managed-prometheus" {
		t.Errorf("SourceAKSManagedPrometheus = %s, want azure-managed-prometheus", SourceAKSManagedPrometheus)
	}
}

func TestAKSEndpointConstants(t *testing.T) {
	if AKSEndpointAzureMonitor != "monitor.azure.com" {
		t.Errorf("AKSEndpointAzureMonitor = %s, want monitor.azure.com", AKSEndpointAzureMonitor)
	}

	if AKSEndpointManagedPrometheus != "prometheus.monitor.azure.com" {
		t.Errorf("AKSEndpointManagedPrometheus = %s, want prometheus.monitor.azure.com", AKSEndpointManagedPrometheus)
	}
}

func TestAKSMetricsConfigStruct(t *testing.T) {
	config := AKSMetricsConfig{
		SubscriptionID:     "sub-123",
		ResourceGroup:      "my-rg",
		ClusterName:        "my-cluster",
		ContainerInsights:  true,
		ManagedPrometheus:  true,
		AzureMonitor:       true,
		DiagnosticSettings: true,
	}

	if config.SubscriptionID != "sub-123" {
		t.Errorf("expected SubscriptionID 'sub-123', got %s", config.SubscriptionID)
	}

	if !config.ContainerInsights {
		t.Error("expected ContainerInsights to be true")
	}

	if !config.ManagedPrometheus {
		t.Error("expected ManagedPrometheus to be true")
	}
}
