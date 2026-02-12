package telemetry

import (
	"fmt"
	"strings"
)

// GKE Metrics sources
const (
	// SourceGKECloudMonitoring indicates metrics from GCP Cloud Monitoring
	SourceGKECloudMonitoring MetricsSource = "cloud-monitoring"
	// SourceGKEManagedPrometheus indicates metrics from GKE Managed Prometheus
	SourceGKEManagedPrometheus MetricsSource = "managed-prometheus"
)

// GKEMetricsEndpoint represents GKE-specific metrics endpoint types
type GKEMetricsEndpoint string

const (
	// GKEEndpointCloudMonitoring is the Cloud Monitoring API
	GKEEndpointCloudMonitoring GKEMetricsEndpoint = "monitoring.googleapis.com"
	// GKEEndpointManagedPrometheus is the GKE Managed Prometheus API
	GKEEndpointManagedPrometheus GKEMetricsEndpoint = "prometheus.googleapis.com"
)

// GKEMetricsConfig represents configuration for GKE metrics collection
type GKEMetricsConfig struct {
	ProjectID          string `json:"projectId"`
	ClusterName        string `json:"clusterName"`
	Location           string `json:"location"`
	CloudMonitoring    bool   `json:"cloudMonitoringEnabled"`
	ManagedPrometheus  bool   `json:"managedPrometheusEnabled"`
	SystemMetrics      bool   `json:"systemMetricsEnabled"`
	ControlPlaneMetrics bool  `json:"controlPlaneMetricsEnabled"`
}

// GKEMetricType represents GKE-specific metric types
type GKEMetricType string

const (
	// GKE container metrics
	GKEMetricContainerCPU           GKEMetricType = "kubernetes.io/container/cpu/core_usage_time"
	GKEMetricContainerMemory        GKEMetricType = "kubernetes.io/container/memory/used_bytes"
	GKEMetricContainerRestarts      GKEMetricType = "kubernetes.io/container/restart_count"
	GKEMetricContainerUptime        GKEMetricType = "kubernetes.io/container/uptime"

	// GKE pod metrics
	GKEMetricPodNetworkReceived     GKEMetricType = "kubernetes.io/pod/network/received_bytes_count"
	GKEMetricPodNetworkSent         GKEMetricType = "kubernetes.io/pod/network/sent_bytes_count"
	GKEMetricPodVolumeTotalBytes    GKEMetricType = "kubernetes.io/pod/volume/total_bytes"
	GKEMetricPodVolumeUsedBytes     GKEMetricType = "kubernetes.io/pod/volume/used_bytes"

	// GKE node metrics
	GKEMetricNodeCPU                GKEMetricType = "kubernetes.io/node/cpu/core_usage_time"
	GKEMetricNodeMemory             GKEMetricType = "kubernetes.io/node/memory/used_bytes"
	GKEMetricNodePIDUsed            GKEMetricType = "kubernetes.io/node/pid_used"

	// GKE control plane metrics
	GKEMetricAPIServerRequestRate   GKEMetricType = "kubernetes.io/master/api_server/requests"
	GKEMetricAPIServerLatency       GKEMetricType = "kubernetes.io/master/api_server/request_latencies"
	GKEMetricSchedulerLatency       GKEMetricType = "kubernetes.io/master/scheduler/schedule_attempts"
)

// GetGKECloudMonitoringHints returns guidance for Cloud Monitoring integration
func GetGKECloudMonitoringHints() []string {
	return []string{
		"GKE clusters have Cloud Monitoring enabled by default",
		"Metrics are available in the Cloud Console under Kubernetes Engine > Clusters > cluster-name > Observability",
		"Use gcloud monitoring to query metrics programmatically",
		"Cloud Monitoring provides automatic alerting policies for GKE",
		"System metrics include container, pod, and node-level data",
		"Control plane metrics available for GKE Enterprise clusters",
	}
}

// GetGKEManagedPrometheusNotes returns notes about GKE Managed Prometheus
func GetGKEManagedPrometheusNotes() []string {
	return []string{
		"GKE Managed Prometheus (GMP) provides fully managed Prometheus",
		"Enable GMP via: gcloud container clusters update CLUSTER --enable-managed-prometheus",
		"GMP is compatible with existing Prometheus queries and dashboards",
		"Metrics are stored in Cloud Monitoring with Prometheus query interface",
		"Use PodMonitoring CRD to configure scrape targets",
		"GMP supports global and cluster-scoped metrics collection",
		"Prometheus UI available via port-forward to the managed collector",
	}
}

// GKEMetricsGcloudCommand returns a gcloud command for querying GKE metrics
func GKEMetricsGcloudCommand(project, cluster, metricType string) string {
	return fmt.Sprintf(`gcloud monitoring metrics list \
  --filter="metric.type='%s'" \
  --project=%s`, metricType, project)
}

// GKEMetricsQueryCommand returns a gcloud command for reading metric data
func GKEMetricsQueryCommand(project, metricType, duration string) string {
	return fmt.Sprintf(`gcloud monitoring read \
  --project=%s \
  "fetch kubernetes_container | metric '%s' | within %s"`,
		project, metricType, duration)
}

// GKEDashboardURL returns the Cloud Console URL for GKE monitoring
func GKEDashboardURL(project, cluster, location string) string {
	return fmt.Sprintf(
		"https://console.cloud.google.com/kubernetes/clusters/details/%s/%s/observability?project=%s",
		location, cluster, project)
}

// GKEMetricsExplorerURL returns the Cloud Metrics Explorer URL
func GKEMetricsExplorerURL(project string) string {
	return fmt.Sprintf(
		"https://console.cloud.google.com/monitoring/metrics-explorer?project=%s",
		project)
}

// GKEPrometheusQueries returns common Prometheus queries for GKE
func GKEPrometheusQueries() map[string]string {
	return map[string]string{
		"container_cpu_usage": `sum(rate(container_cpu_usage_seconds_total{container!="POD",container!=""}[5m])) by (namespace, pod, container)`,
		"container_memory_usage": `sum(container_memory_usage_bytes{container!="POD",container!=""}) by (namespace, pod, container)`,
		"pod_restart_rate": `sum(rate(kube_pod_container_status_restarts_total[1h])) by (namespace, pod)`,
		"node_cpu_utilization": `100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) by (instance))`,
		"node_memory_utilization": `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`,
		"network_receive_bytes": `sum(rate(container_network_receive_bytes_total[5m])) by (namespace, pod)`,
		"network_transmit_bytes": `sum(rate(container_network_transmit_bytes_total[5m])) by (namespace, pod)`,
		"persistent_volume_usage": `kubelet_volume_stats_used_bytes / kubelet_volume_stats_capacity_bytes * 100`,
	}
}

// GKEAlertingPolicySuggestions returns suggested alerting policies for GKE
func GKEAlertingPolicySuggestions() []GKEAlertPolicy {
	return []GKEAlertPolicy{
		{
			Name:        "High Container CPU",
			Metric:      "kubernetes.io/container/cpu/core_usage_time",
			Condition:   "above 80% of limit for 5 minutes",
			Severity:    "warning",
			Description: "Alert when container CPU usage exceeds 80% of its limit",
		},
		{
			Name:        "High Container Memory",
			Metric:      "kubernetes.io/container/memory/used_bytes",
			Condition:   "above 90% of limit for 5 minutes",
			Severity:    "critical",
			Description: "Alert when container memory usage exceeds 90% of its limit",
		},
		{
			Name:        "Pod Restart Rate",
			Metric:      "kubernetes.io/container/restart_count",
			Condition:   "increase of 3 or more in 10 minutes",
			Severity:    "warning",
			Description: "Alert when a pod restarts frequently",
		},
		{
			Name:        "Node Not Ready",
			Metric:      "kubernetes.io/node/status/condition",
			Condition:   "Ready condition is False for 5 minutes",
			Severity:    "critical",
			Description: "Alert when a node enters NotReady state",
		},
		{
			Name:        "High Node CPU",
			Metric:      "kubernetes.io/node/cpu/core_usage_time",
			Condition:   "above 85% for 10 minutes",
			Severity:    "warning",
			Description: "Alert when node CPU utilization is high",
		},
		{
			Name:        "Persistent Volume Full",
			Metric:      "kubernetes.io/pod/volume/used_bytes",
			Condition:   "above 90% of total_bytes",
			Severity:    "critical",
			Description: "Alert when persistent volume is nearly full",
		},
	}
}

// GKEAlertPolicy represents a suggested GKE alerting policy
type GKEAlertPolicy struct {
	Name        string `json:"name"`
	Metric      string `json:"metric"`
	Condition   string `json:"condition"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// GetGKEMetricsRecommendation returns telemetry recommendations for a use case
func GetGKEMetricsRecommendation(useCase string) GKEMetricsRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// High-fidelity production monitoring
	if containsAny(useCaseLower, []string{"production", "critical", "sla", "slo"}) {
		return GKEMetricsRecommendation{
			Solution:      "Cloud Monitoring with Managed Prometheus",
			Reason:        "Production workloads need reliable, managed monitoring with SLA support",
			Components:    []string{"Cloud Monitoring", "Managed Prometheus", "Cloud Alerting"},
			Configuration: "Enable both system metrics and managed Prometheus for comprehensive coverage",
			Notes: []string{
				"Set up SLO monitoring using Cloud Monitoring SLIs",
				"Configure alerting policies for critical metrics",
				"Use Cloud Trace for distributed tracing",
				"Enable audit logging for compliance",
			},
		}
	}

	// Cost-sensitive monitoring
	if containsAny(useCaseLower, []string{"cost", "budget", "cheap", "minimal"}) {
		return GKEMetricsRecommendation{
			Solution:      "Built-in metrics-server only",
			Reason:        "Minimal cost with basic monitoring via kubectl top",
			Components:    []string{"metrics-server"},
			Configuration: "Use default metrics-server, disable additional monitoring features",
			Notes: []string{
				"kubectl top provides basic CPU and memory metrics",
				"No historical data retention",
				"Consider Cloud Monitoring free tier for basic alerting",
				"Self-managed Prometheus for more detailed metrics if needed",
			},
		}
	}

	// Prometheus-native workloads
	if containsAny(useCaseLower, []string{"prometheus", "grafana", "promql", "existing"}) {
		return GKEMetricsRecommendation{
			Solution:      "GKE Managed Prometheus",
			Reason:        "Managed Prometheus provides familiar PromQL interface with GCP integration",
			Components:    []string{"Managed Prometheus", "PodMonitoring CRD"},
			Configuration: "Enable managed Prometheus and configure PodMonitoring resources",
			Notes: []string{
				"Compatible with existing Prometheus queries and dashboards",
				"Use PodMonitoring CRD for custom metric scraping",
				"Grafana Cloud or self-hosted Grafana for visualization",
				"Metrics stored in Cloud Monitoring backend",
			},
		}
	}

	// Default recommendation
	return GKEMetricsRecommendation{
		Solution:      "Cloud Monitoring with system metrics",
		Reason:        "Default GKE monitoring provides good balance of features and simplicity",
		Components:    []string{"Cloud Monitoring", "System metrics"},
		Configuration: "Use default Cloud Monitoring integration enabled on cluster creation",
		Notes: []string{
			"System metrics collected automatically",
			"Dashboards available in GCP Console",
			"Consider Managed Prometheus for custom application metrics",
			"Enable control plane metrics for deeper cluster insights",
		},
	}
}

// GKEMetricsRecommendation represents a metrics/telemetry recommendation
type GKEMetricsRecommendation struct {
	Solution      string   `json:"solution"`
	Reason        string   `json:"reason"`
	Components    []string `json:"components"`
	Configuration string   `json:"configuration"`
	Notes         []string `json:"notes"`
}

// GKELoggingIntegration returns information about GKE logging integration
func GKELoggingIntegration() map[string]string {
	return map[string]string{
		"default_sink":    "Cloud Logging",
		"log_types":       "container, pod, node, audit, control plane",
		"retention":       "30 days default, configurable up to 3650 days",
		"query_language":  "Cloud Logging query language",
		"export_options":  "BigQuery, Cloud Storage, Pub/Sub",
		"log_router_url":  "https://console.cloud.google.com/logs/router",
	}
}

// IsGKEMetricsSource checks if a metrics source is GKE-specific
func IsGKEMetricsSource(source MetricsSource) bool {
	return source == SourceGKECloudMonitoring || source == SourceGKEManagedPrometheus
}

// GetGKEMetricDescription returns a description for a GKE metric type
func GetGKEMetricDescription(metricType GKEMetricType) string {
	descriptions := map[GKEMetricType]string{
		GKEMetricContainerCPU:         "Cumulative CPU usage time in seconds",
		GKEMetricContainerMemory:      "Current memory usage in bytes",
		GKEMetricContainerRestarts:    "Number of container restarts",
		GKEMetricContainerUptime:      "Time since container started in seconds",
		GKEMetricPodNetworkReceived:   "Cumulative bytes received over network",
		GKEMetricPodNetworkSent:       "Cumulative bytes sent over network",
		GKEMetricPodVolumeTotalBytes:  "Total volume capacity in bytes",
		GKEMetricPodVolumeUsedBytes:   "Volume space used in bytes",
		GKEMetricNodeCPU:              "Cumulative node CPU usage time",
		GKEMetricNodeMemory:           "Current node memory usage",
		GKEMetricNodePIDUsed:          "Number of process IDs in use",
		GKEMetricAPIServerRequestRate: "API server request rate",
		GKEMetricAPIServerLatency:     "API server request latency distribution",
		GKEMetricSchedulerLatency:     "Scheduler scheduling attempt latency",
	}

	if desc, ok := descriptions[metricType]; ok {
		return desc
	}
	return "Unknown metric type"
}

// GKETelemetryNotes returns important notes about GKE telemetry
func GKETelemetryNotes() []string {
	return []string{
		"Cloud Monitoring is enabled by default on GKE clusters",
		"Managed Prometheus provides PromQL-compatible managed metrics",
		"System metrics include container, pod, node, and cluster-level data",
		"Control plane metrics require GKE Enterprise or explicit enablement",
		"Cloud Logging collects container logs automatically",
		"Use Log Router to export logs to BigQuery for analysis",
		"Custom metrics require Managed Prometheus or self-hosted Prometheus",
		"Cloud Trace provides distributed tracing for microservices",
	}
}

// EKSTelemetryComparison returns comparison notes between GKE and EKS telemetry
func EKSTelemetryComparison() map[string]string {
	return map[string]string{
		"gke_monitoring":        "Cloud Monitoring (built-in)",
		"eks_monitoring":        "CloudWatch Container Insights (opt-in)",
		"gke_prometheus":        "Managed Prometheus (GMP)",
		"eks_prometheus":        "Amazon Managed Prometheus (AMP)",
		"gke_logging":           "Cloud Logging (built-in)",
		"eks_logging":           "CloudWatch Logs (opt-in)",
		"gke_dashboard":         "GKE Observability Dashboard",
		"eks_dashboard":         "CloudWatch Container Insights Dashboard",
		"gke_tracing":           "Cloud Trace",
		"eks_tracing":           "AWS X-Ray",
	}
}
