package telemetry

import (
	"fmt"
	"strings"
)

// AKSMetricsConfig represents configuration for AKS metrics collection
type AKSMetricsConfig struct {
	SubscriptionID     string `json:"subscriptionId"`
	ResourceGroup      string `json:"resourceGroup"`
	ClusterName        string `json:"clusterName"`
	ContainerInsights  bool   `json:"containerInsightsEnabled"`
	ManagedPrometheus  bool   `json:"managedPrometheusEnabled"`
	AzureMonitor       bool   `json:"azureMonitorEnabled"`
	DiagnosticSettings bool   `json:"diagnosticSettingsEnabled"`
}

// AKSMetricType represents AKS-specific metric types
type AKSMetricType string

const (
	// AKS container metrics (Container Insights)
	AKSMetricContainerCPU          AKSMetricType = "cpuUsageMillicores"
	AKSMetricContainerMemory       AKSMetricType = "memoryWorkingSetBytes"
	AKSMetricContainerMemoryRss    AKSMetricType = "memoryRssBytes"
	AKSMetricContainerRestarts     AKSMetricType = "restartCount"

	// AKS node metrics
	AKSMetricNodeCPUPercent        AKSMetricType = "cpuUsagePercentage"
	AKSMetricNodeMemoryPercent     AKSMetricType = "memoryWorkingSetPercentage"
	AKSMetricNodeDiskUsedPercent   AKSMetricType = "diskUsedPercentage"
	AKSMetricNodeNetworkIn         AKSMetricType = "networkRxBytes"
	AKSMetricNodeNetworkOut        AKSMetricType = "networkTxBytes"

	// AKS cluster metrics
	AKSMetricClusterPodCount       AKSMetricType = "podCount"
	AKSMetricClusterNodeCount      AKSMetricType = "nodeCount"
	AKSMetricClusterCPUPercent     AKSMetricType = "clusterCpuUsagePercentage"
	AKSMetricClusterMemoryPercent  AKSMetricType = "clusterMemoryWorkingSetPercentage"
)

// GetAKSAzureMonitorHints returns guidance for Azure Monitor integration
func GetAKSAzureMonitorHints() []string {
	return []string{
		"Azure Monitor is the unified monitoring platform for Azure resources",
		"Enable Container Insights for AKS-specific metrics and logs",
		"Metrics are available in Azure Portal under AKS cluster > Insights",
		"Use Azure CLI or REST API to query metrics programmatically",
		"Azure Monitor provides automatic alerting with Action Groups",
		"Diagnostic settings allow export to Log Analytics, Storage, or Event Hubs",
	}
}

// GetAKSContainerInsightsNotes returns notes about Container Insights
func GetAKSContainerInsightsNotes() []string {
	return []string{
		"Container Insights provides detailed monitoring for AKS clusters",
		"Enable via: az aks enable-addons --addon monitoring -n CLUSTER -g RESOURCE_GROUP",
		"Requires Log Analytics workspace for data storage",
		"Provides pre-built workbooks for cluster analysis",
		"Collects container logs (stdout/stderr) automatically",
		"Live metrics view available for real-time monitoring",
		"Cost scales with data ingestion volume",
	}
}

// GetAKSManagedPrometheusNotes returns notes about Azure Managed Prometheus
func GetAKSManagedPrometheusNotes() []string {
	return []string{
		"Azure Monitor managed service for Prometheus provides fully managed Prometheus",
		"Enable during cluster creation or update existing cluster",
		"Compatible with existing Prometheus queries and dashboards",
		"Integrated with Azure Managed Grafana for visualization",
		"Use PodMonitor and ServiceMonitor CRDs for custom scrape targets",
		"Prometheus data stored in Azure Monitor workspace",
		"PromQL queries available through Azure Monitor workspace",
	}
}

// AKSMetricsAzCommand returns az cli command for querying AKS metrics
func AKSMetricsAzCommand(subscription, resourceGroup, clusterName, metricName string) string {
	return fmt.Sprintf(`az monitor metrics list \
  --resource /subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s \
  --metric %s \
  --interval PT1M`,
		subscription, resourceGroup, clusterName, metricName)
}

// AKSDashboardURL returns the Azure Portal URL for AKS monitoring
func AKSDashboardURL(subscription, resourceGroup, clusterName string) string {
	return fmt.Sprintf(
		"https://portal.azure.com/#@/resource/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s/infrainsights",
		subscription, resourceGroup, clusterName)
}

// AKSMetricsExplorerURL returns the Azure Metrics Explorer URL
func AKSMetricsExplorerURL(subscription, resourceGroup string) string {
	return fmt.Sprintf(
		"https://portal.azure.com/#@/resource/subscriptions/%s/resourceGroups/%s/metrics",
		subscription, resourceGroup)
}

// AKSLogAnalyticsQueries returns common Log Analytics queries for AKS
func AKSLogAnalyticsQueries() map[string]string {
	return map[string]string{
		"container_cpu": `Perf
| where ObjectName == "K8SContainer" and CounterName == "cpuUsageMillicores"
| summarize avg(CounterValue) by bin(TimeGenerated, 5m), InstanceName
| order by TimeGenerated desc`,

		"container_memory": `Perf
| where ObjectName == "K8SContainer" and CounterName == "memoryWorkingSetBytes"
| summarize avg(CounterValue) by bin(TimeGenerated, 5m), InstanceName
| order by TimeGenerated desc`,

		"pod_restarts": `KubePodInventory
| where ClusterName == "CLUSTER_NAME"
| summarize RestartCount = max(PodRestartCount) by PodName, TimeGenerated
| where RestartCount > 0
| order by RestartCount desc`,

		"node_cpu": `Perf
| where ObjectName == "K8SNode" and CounterName == "cpuUsagePercentage"
| summarize avg(CounterValue) by bin(TimeGenerated, 5m), Computer
| order by TimeGenerated desc`,

		"node_memory": `Perf
| where ObjectName == "K8SNode" and CounterName == "memoryWorkingSetPercentage"
| summarize avg(CounterValue) by bin(TimeGenerated, 5m), Computer
| order by TimeGenerated desc`,

		"container_logs": `ContainerLog
| where ClusterName == "CLUSTER_NAME"
| where LogEntry contains "error" or LogEntry contains "exception"
| project TimeGenerated, ContainerID, LogEntry
| order by TimeGenerated desc
| limit 100`,

		"failed_pods": `KubePodInventory
| where ClusterName == "CLUSTER_NAME" and PodStatus == "Failed"
| project TimeGenerated, Namespace, Name, PodStatus
| order by TimeGenerated desc`,

		"node_conditions": `KubeNodeInventory
| where ClusterName == "CLUSTER_NAME"
| extend Ready = iff(NodeConditionReady == "true", 1, 0)
| summarize ReadyNodes = sum(Ready), TotalNodes = count() by bin(TimeGenerated, 5m)
| order by TimeGenerated desc`,
	}
}

// AKSAlertPolicy represents a suggested AKS alerting policy
type AKSAlertPolicy struct {
	Name        string `json:"name"`
	Metric      string `json:"metric"`
	Condition   string `json:"condition"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// AKSAlertingPolicySuggestions returns suggested alerting policies for AKS
func AKSAlertingPolicySuggestions() []AKSAlertPolicy {
	return []AKSAlertPolicy{
		{
			Name:        "High Container CPU",
			Metric:      "cpuUsageMillicores",
			Condition:   "Average above 80% of limit for 5 minutes",
			Severity:    "Warning",
			Description: "Alert when container CPU usage exceeds 80% of its limit",
		},
		{
			Name:        "High Container Memory",
			Metric:      "memoryWorkingSetBytes",
			Condition:   "Average above 90% of limit for 5 minutes",
			Severity:    "Critical",
			Description: "Alert when container memory usage exceeds 90% of its limit",
		},
		{
			Name:        "Pod Restart Rate",
			Metric:      "PodRestartCount",
			Condition:   "Increase of 3 or more in 10 minutes",
			Severity:    "Warning",
			Description: "Alert when a pod restarts frequently",
		},
		{
			Name:        "Node Not Ready",
			Metric:      "NodeConditionReady",
			Condition:   "Ready condition is False for 5 minutes",
			Severity:    "Critical",
			Description: "Alert when a node enters NotReady state",
		},
		{
			Name:        "High Node CPU",
			Metric:      "cpuUsagePercentage",
			Condition:   "Average above 85% for 10 minutes",
			Severity:    "Warning",
			Description: "Alert when node CPU utilization is high",
		},
		{
			Name:        "High Node Disk Usage",
			Metric:      "diskUsedPercentage",
			Condition:   "Average above 85% for 15 minutes",
			Severity:    "Warning",
			Description: "Alert when node disk usage is high",
		},
		{
			Name:        "Cluster Unschedulable Pods",
			Metric:      "podCount",
			Condition:   "Pending pods count above 0 for 15 minutes",
			Severity:    "Warning",
			Description: "Alert when pods cannot be scheduled",
		},
	}
}

// AKSMetricsRecommendation represents a metrics/telemetry recommendation
type AKSMetricsRecommendation struct {
	Solution      string   `json:"solution"`
	Reason        string   `json:"reason"`
	Components    []string `json:"components"`
	Configuration string   `json:"configuration"`
	Notes         []string `json:"notes"`
}

// GetAKSMetricsRecommendation returns telemetry recommendations for a use case
func GetAKSMetricsRecommendation(useCase string) AKSMetricsRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// High-fidelity production monitoring
	if containsAny(useCaseLower, []string{"production", "critical", "sla", "slo"}) {
		return AKSMetricsRecommendation{
			Solution:      "Container Insights with Azure Managed Prometheus",
			Reason:        "Production workloads need reliable, managed monitoring with Azure integration",
			Components:    []string{"Container Insights", "Azure Managed Prometheus", "Azure Alerts"},
			Configuration: "Enable Container Insights addon and Azure Managed Prometheus for comprehensive coverage",
			Notes: []string{
				"Configure Azure Monitor alerts with Action Groups",
				"Use Azure Managed Grafana for visualization",
				"Enable diagnostic settings for audit logging",
				"Consider Azure Monitor Workbooks for custom dashboards",
			},
		}
	}

	// Cost-sensitive monitoring
	if containsAny(useCaseLower, []string{"cost", "budget", "cheap", "minimal"}) {
		return AKSMetricsRecommendation{
			Solution:      "Built-in metrics-server only",
			Reason:        "Minimal cost with basic monitoring via kubectl top",
			Components:    []string{"metrics-server"},
			Configuration: "Use default metrics-server, disable Container Insights",
			Notes: []string{
				"kubectl top provides basic CPU and memory metrics",
				"No historical data retention",
				"Consider enabling Container Insights only for critical namespaces",
				"Use Azure Monitor free tier quotas where possible",
			},
		}
	}

	// Prometheus-native workloads
	if containsAny(useCaseLower, []string{"prometheus", "grafana", "promql", "existing"}) {
		return AKSMetricsRecommendation{
			Solution:      "Azure Managed Prometheus",
			Reason:        "Managed Prometheus provides familiar PromQL interface with Azure integration",
			Components:    []string{"Azure Managed Prometheus", "Azure Managed Grafana", "PodMonitor CRD"},
			Configuration: "Enable Azure Managed Prometheus and configure PodMonitor/ServiceMonitor resources",
			Notes: []string{
				"Compatible with existing Prometheus queries and dashboards",
				"Use PodMonitor CRD for custom metric scraping",
				"Azure Managed Grafana provides managed visualization",
				"PromQL queries available through Azure Monitor workspace",
			},
		}
	}

	// Default recommendation
	return AKSMetricsRecommendation{
		Solution:      "Container Insights",
		Reason:        "Container Insights provides good balance of features and Azure integration",
		Components:    []string{"Container Insights", "Log Analytics"},
		Configuration: "Enable Container Insights addon on the cluster",
		Notes: []string{
			"Container and node metrics collected automatically",
			"Pre-built dashboards available in Azure Portal",
			"Consider Azure Managed Prometheus for custom application metrics",
			"Enable Prometheus metrics collection for additional coverage",
		},
	}
}

// AKSLoggingIntegration returns information about AKS logging integration
func AKSLoggingIntegration() map[string]string {
	return map[string]string{
		"default_sink":    "Log Analytics workspace",
		"log_types":       "container, pod, node, audit, Kubernetes events",
		"retention":       "30 days default, configurable up to 730 days",
		"query_language":  "Kusto Query Language (KQL)",
		"export_options":  "Storage Account, Event Hubs, Azure Sentinel",
		"dashboard_url":   "https://portal.azure.com/#blade/Microsoft_Azure_Monitoring_Logs/LogsBlade",
	}
}

// IsAKSMetricsSource checks if a metrics source is AKS-specific
func IsAKSMetricsSource(source MetricsSource) bool {
	return source == SourceAKSAzureMonitor ||
		source == SourceAKSContainerInsights ||
		source == SourceAKSManagedPrometheus
}

// GetAKSMetricDescription returns a description for an AKS metric type
func GetAKSMetricDescription(metricType AKSMetricType) string {
	descriptions := map[AKSMetricType]string{
		AKSMetricContainerCPU:         "Container CPU usage in millicores",
		AKSMetricContainerMemory:      "Container memory working set in bytes",
		AKSMetricContainerMemoryRss:   "Container RSS memory in bytes",
		AKSMetricContainerRestarts:    "Number of container restarts",
		AKSMetricNodeCPUPercent:       "Node CPU usage percentage",
		AKSMetricNodeMemoryPercent:    "Node memory usage percentage",
		AKSMetricNodeDiskUsedPercent:  "Node disk usage percentage",
		AKSMetricNodeNetworkIn:        "Node network received bytes",
		AKSMetricNodeNetworkOut:       "Node network transmitted bytes",
		AKSMetricClusterPodCount:      "Total pod count in cluster",
		AKSMetricClusterNodeCount:     "Total node count in cluster",
		AKSMetricClusterCPUPercent:    "Cluster-wide CPU usage percentage",
		AKSMetricClusterMemoryPercent: "Cluster-wide memory usage percentage",
	}

	if desc, ok := descriptions[metricType]; ok {
		return desc
	}
	return "Unknown metric type"
}

// AKSTelemetryNotes returns important notes about AKS telemetry
func AKSTelemetryNotes() []string {
	return []string{
		"Container Insights is the recommended monitoring solution for AKS",
		"Azure Managed Prometheus provides PromQL-compatible managed metrics",
		"Log Analytics workspace is required for Container Insights",
		"Azure Monitor alerts can trigger Action Groups for notifications",
		"Diagnostic settings enable export to external systems",
		"Azure Managed Grafana integrates with Azure Managed Prometheus",
		"Container logs are stored in ContainerLog table in Log Analytics",
		"Use Azure Monitor Workbooks for custom visualization",
	}
}

// GKETelemetryComparisonWithAKS returns comparison notes between AKS and GKE telemetry
func GKETelemetryComparisonWithAKS() map[string]string {
	return map[string]string{
		"aks_monitoring":  "Azure Monitor / Container Insights",
		"gke_monitoring":  "Cloud Monitoring (built-in)",
		"eks_monitoring":  "CloudWatch Container Insights (opt-in)",
		"aks_prometheus":  "Azure Managed Prometheus",
		"gke_prometheus":  "GKE Managed Prometheus (GMP)",
		"eks_prometheus":  "Amazon Managed Prometheus (AMP)",
		"aks_logging":     "Log Analytics (Container Insights)",
		"gke_logging":     "Cloud Logging (built-in)",
		"eks_logging":     "CloudWatch Logs (opt-in)",
		"aks_dashboard":   "Azure Portal / Azure Workbooks",
		"gke_dashboard":   "GKE Observability Dashboard",
		"eks_dashboard":   "CloudWatch Container Insights Dashboard",
		"aks_grafana":     "Azure Managed Grafana",
		"gke_grafana":     "Self-managed or Cloud Marketplace",
		"eks_grafana":     "Amazon Managed Grafana",
	}
}

