package telemetry

import (
	"strings"
	"testing"
)

func TestGetGKECloudMonitoringHints(t *testing.T) {
	hints := GetGKECloudMonitoringHints()

	if len(hints) == 0 {
		t.Error("expected at least one Cloud Monitoring hint")
	}

	hintsText := strings.Join(hints, " ")

	expectedTopics := []string{
		"Cloud Monitoring",
		"metrics",
		"Console",
		"gcloud",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(hintsText, topic) {
			t.Errorf("Cloud Monitoring hints should mention %s", topic)
		}
	}
}

func TestGetGKEManagedPrometheusNotes(t *testing.T) {
	notes := GetGKEManagedPrometheusNotes()

	if len(notes) == 0 {
		t.Error("expected at least one Managed Prometheus note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Managed Prometheus",
		"GMP",
		"PodMonitoring",
		"Prometheus",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("Managed Prometheus notes should mention %s", topic)
		}
	}
}

func TestGKEMetricsGcloudCommand(t *testing.T) {
	cmd := GKEMetricsGcloudCommand("my-project", "my-cluster", string(GKEMetricContainerCPU))

	if !strings.Contains(cmd, "gcloud monitoring") {
		t.Error("expected gcloud monitoring command")
	}

	if !strings.Contains(cmd, "my-project") {
		t.Error("expected project in command")
	}

	if !strings.Contains(cmd, string(GKEMetricContainerCPU)) {
		t.Error("expected metric type in command")
	}
}

func TestGKEMetricsQueryCommand(t *testing.T) {
	cmd := GKEMetricsQueryCommand("my-project", string(GKEMetricContainerMemory), "1h")

	if !strings.Contains(cmd, "gcloud monitoring read") {
		t.Error("expected gcloud monitoring read command")
	}

	if !strings.Contains(cmd, "my-project") {
		t.Error("expected project in command")
	}

	if !strings.Contains(cmd, "1h") {
		t.Error("expected duration in command")
	}
}

func TestGKEDashboardURL(t *testing.T) {
	url := GKEDashboardURL("my-project", "my-cluster", "us-central1")

	expectedParts := []string{
		"console.cloud.google.com",
		"kubernetes",
		"observability",
		"my-project",
		"my-cluster",
		"us-central1",
	}

	for _, part := range expectedParts {
		if !strings.Contains(url, part) {
			t.Errorf("dashboard URL should contain %s, got %s", part, url)
		}
	}
}

func TestGKEMetricsExplorerURL(t *testing.T) {
	url := GKEMetricsExplorerURL("my-project")

	if !strings.Contains(url, "console.cloud.google.com") {
		t.Error("expected cloud console URL")
	}

	if !strings.Contains(url, "metrics-explorer") {
		t.Error("expected metrics explorer in URL")
	}

	if !strings.Contains(url, "my-project") {
		t.Error("expected project in URL")
	}
}

func TestGKEPrometheusQueries(t *testing.T) {
	queries := GKEPrometheusQueries()

	if len(queries) == 0 {
		t.Error("expected at least one Prometheus query")
	}

	expectedQueries := []string{
		"container_cpu_usage",
		"container_memory_usage",
		"pod_restart_rate",
		"node_cpu_utilization",
		"node_memory_utilization",
	}

	for _, queryName := range expectedQueries {
		if query, ok := queries[queryName]; !ok {
			t.Errorf("expected query %s to be defined", queryName)
		} else if query == "" {
			t.Errorf("query %s should not be empty", queryName)
		}
	}
}

func TestGKEAlertingPolicySuggestions(t *testing.T) {
	policies := GKEAlertingPolicySuggestions()

	if len(policies) == 0 {
		t.Error("expected at least one alerting policy suggestion")
	}

	for _, policy := range policies {
		if policy.Name == "" {
			t.Error("policy name should not be empty")
		}
		if policy.Metric == "" {
			t.Error("policy metric should not be empty")
		}
		if policy.Condition == "" {
			t.Error("policy condition should not be empty")
		}
		if policy.Severity == "" {
			t.Error("policy severity should not be empty")
		}
		if policy.Description == "" {
			t.Error("policy description should not be empty")
		}
	}

	// Verify severity values are valid
	validSeverities := map[string]bool{"warning": true, "critical": true, "info": true}
	for _, policy := range policies {
		if !validSeverities[policy.Severity] {
			t.Errorf("invalid severity %s for policy %s", policy.Severity, policy.Name)
		}
	}
}

func TestGetGKEMetricsRecommendation(t *testing.T) {
	tests := []struct {
		name                 string
		useCase              string
		wantSolutionContains string
	}{
		{
			name:                 "Production workload",
			useCase:              "production web service with SLA",
			wantSolutionContains: "Cloud Monitoring",
		},
		{
			name:                 "Cost-sensitive",
			useCase:              "budget-conscious deployment",
			wantSolutionContains: "metrics-server",
		},
		{
			name:                 "Prometheus user",
			useCase:              "existing prometheus dashboards",
			wantSolutionContains: "Prometheus",
		},
		{
			name:                 "Default case",
			useCase:              "general application",
			wantSolutionContains: "Cloud Monitoring",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetGKEMetricsRecommendation(tt.useCase)

			if !strings.Contains(rec.Solution, tt.wantSolutionContains) {
				t.Errorf("expected solution containing %s, got %s", tt.wantSolutionContains, rec.Solution)
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if len(rec.Components) == 0 {
				t.Error("recommendation should have components")
			}

			if len(rec.Notes) == 0 {
				t.Error("recommendation should have notes")
			}
		})
	}
}

func TestGKELoggingIntegration(t *testing.T) {
	logging := GKELoggingIntegration()

	if len(logging) == 0 {
		t.Error("expected logging integration information")
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
			t.Errorf("expected logging info key %s", key)
		}
	}

	if logging["default_sink"] != "Cloud Logging" {
		t.Errorf("expected default sink 'Cloud Logging', got %s", logging["default_sink"])
	}
}

func TestIsGKEMetricsSource(t *testing.T) {
	tests := []struct {
		source MetricsSource
		want   bool
	}{
		{SourceGKECloudMonitoring, true},
		{SourceGKEManagedPrometheus, true},
		{SourceMetricsServer, false},
		{SourceResourceSpecs, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			got := IsGKEMetricsSource(tt.source)
			if got != tt.want {
				t.Errorf("IsGKEMetricsSource(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestGetGKEMetricDescription(t *testing.T) {
	metricTypes := []GKEMetricType{
		GKEMetricContainerCPU,
		GKEMetricContainerMemory,
		GKEMetricContainerRestarts,
		GKEMetricPodNetworkReceived,
		GKEMetricNodeCPU,
		GKEMetricAPIServerRequestRate,
	}

	for _, metricType := range metricTypes {
		desc := GetGKEMetricDescription(metricType)

		if desc == "" {
			t.Errorf("expected description for metric type %s", metricType)
		}

		if desc == "Unknown metric type" {
			t.Errorf("expected specific description for %s, got unknown", metricType)
		}
	}

	// Test unknown metric
	unknownDesc := GetGKEMetricDescription("unknown.metric.type")
	if unknownDesc != "Unknown metric type" {
		t.Errorf("expected 'Unknown metric type' for unknown metric, got %s", unknownDesc)
	}
}

func TestGKETelemetryNotes(t *testing.T) {
	notes := GKETelemetryNotes()

	if len(notes) == 0 {
		t.Error("expected at least one telemetry note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Cloud Monitoring",
		"Managed Prometheus",
		"Cloud Logging",
		"Cloud Trace",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("telemetry notes should mention %s", topic)
		}
	}
}

func TestEKSTelemetryComparison(t *testing.T) {
	comparison := EKSTelemetryComparison()

	if len(comparison) == 0 {
		t.Error("expected telemetry comparison entries")
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_monitoring", "gke_prometheus", "gke_logging"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_monitoring", "eks_prometheus", "eks_logging"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestGKEMetricsConfig(t *testing.T) {
	config := GKEMetricsConfig{
		ProjectID:           "my-project",
		ClusterName:         "my-cluster",
		Location:            "us-central1",
		CloudMonitoring:     true,
		ManagedPrometheus:   true,
		SystemMetrics:       true,
		ControlPlaneMetrics: false,
	}

	if config.ProjectID != "my-project" {
		t.Errorf("expected project 'my-project', got %s", config.ProjectID)
	}

	if !config.CloudMonitoring {
		t.Error("expected Cloud Monitoring to be enabled")
	}

	if !config.ManagedPrometheus {
		t.Error("expected Managed Prometheus to be enabled")
	}

	if config.ControlPlaneMetrics {
		t.Error("expected Control Plane Metrics to be disabled")
	}
}

func TestGKEMetricTypeConstants(t *testing.T) {
	// Verify metric type constants are properly defined
	if !strings.HasPrefix(string(GKEMetricContainerCPU), "kubernetes.io/container/") {
		t.Errorf("GKEMetricContainerCPU should start with kubernetes.io/container/, got %s", GKEMetricContainerCPU)
	}

	if !strings.HasPrefix(string(GKEMetricPodNetworkReceived), "kubernetes.io/pod/") {
		t.Errorf("GKEMetricPodNetworkReceived should start with kubernetes.io/pod/, got %s", GKEMetricPodNetworkReceived)
	}

	if !strings.HasPrefix(string(GKEMetricNodeCPU), "kubernetes.io/node/") {
		t.Errorf("GKEMetricNodeCPU should start with kubernetes.io/node/, got %s", GKEMetricNodeCPU)
	}

	if !strings.HasPrefix(string(GKEMetricAPIServerRequestRate), "kubernetes.io/master/") {
		t.Errorf("GKEMetricAPIServerRequestRate should start with kubernetes.io/master/, got %s", GKEMetricAPIServerRequestRate)
	}
}

func TestGKEMetricsEndpointConstants(t *testing.T) {
	if GKEEndpointCloudMonitoring != "monitoring.googleapis.com" {
		t.Errorf("GKEEndpointCloudMonitoring = %s, want monitoring.googleapis.com", GKEEndpointCloudMonitoring)
	}

	if GKEEndpointManagedPrometheus != "prometheus.googleapis.com" {
		t.Errorf("GKEEndpointManagedPrometheus = %s, want prometheus.googleapis.com", GKEEndpointManagedPrometheus)
	}
}

func TestGKEMetricsSourceConstants(t *testing.T) {
	if SourceGKECloudMonitoring != "cloud-monitoring" {
		t.Errorf("SourceGKECloudMonitoring = %s, want cloud-monitoring", SourceGKECloudMonitoring)
	}

	if SourceGKEManagedPrometheus != "managed-prometheus" {
		t.Errorf("SourceGKEManagedPrometheus = %s, want managed-prometheus", SourceGKEManagedPrometheus)
	}
}

func TestGKEAlertPolicyStruct(t *testing.T) {
	policy := GKEAlertPolicy{
		Name:        "Test Alert",
		Metric:      "kubernetes.io/container/cpu/core_usage_time",
		Condition:   "above 80% for 5 minutes",
		Severity:    "warning",
		Description: "Test alert description",
	}

	if policy.Name != "Test Alert" {
		t.Errorf("expected name 'Test Alert', got %s", policy.Name)
	}

	if policy.Severity != "warning" {
		t.Errorf("expected severity 'warning', got %s", policy.Severity)
	}
}
