package sre

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// HealthChecker performs health checks on cluster resources
type HealthChecker struct {
	client      K8sClient
	diagnostics *DiagnosticsManager
	debug       bool
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(client K8sClient, debug bool) *HealthChecker {
	return &HealthChecker{
		client:      client,
		diagnostics: NewDiagnosticsManager(client, debug),
		debug:       debug,
	}
}

// CheckCluster performs a cluster-wide health check
func (h *HealthChecker) CheckCluster(ctx context.Context) (*ClusterHealthSummary, error) {
	summary := &ClusterHealthSummary{
		Score: 100,
	}

	// Check nodes
	nodeHealth, nodeIssues, err := h.checkNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check nodes: %w", err)
	}
	summary.NodeHealth = nodeHealth

	// Check workloads
	workloadHealth, workloadIssues, podCounts, err := h.checkWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check workloads: %w", err)
	}
	summary.WorkloadHealth = workloadHealth
	summary.TotalPods = podCounts.total
	summary.RunningPods = podCounts.running
	summary.PendingPods = podCounts.pending
	summary.FailedPods = podCounts.failed

	// Check storage (basic check)
	storageHealth, err := h.checkStorage(ctx)
	if err == nil {
		summary.StorageHealth = storageHealth
	} else {
		summary.StorageHealth = ComponentHealth{Status: "unknown", Score: 50}
	}

	// Check network (basic check)
	networkHealth, err := h.checkNetwork(ctx)
	if err == nil {
		summary.NetworkHealth = networkHealth
	} else {
		summary.NetworkHealth = ComponentHealth{Status: "unknown", Score: 50}
	}

	// Count issues by severity
	allIssues := append(nodeIssues, workloadIssues...)
	for _, issue := range allIssues {
		switch issue.Severity {
		case SeverityCritical:
			summary.CriticalIssues++
		case SeverityWarning:
			summary.WarningIssues++
		}
	}

	// Calculate overall score
	summary.Score = h.calculateOverallScore(summary)

	// Determine overall health status
	if summary.CriticalIssues > 0 {
		summary.OverallHealth = "critical"
	} else if summary.WarningIssues > 0 || summary.Score < 80 {
		summary.OverallHealth = "degraded"
	} else {
		summary.OverallHealth = "healthy"
	}

	return summary, nil
}

// CheckNamespace performs a health check on a specific namespace
func (h *HealthChecker) CheckNamespace(ctx context.Context, namespace string) (*HealthCheckResult, error) {
	result := &HealthCheckResult{
		Healthy:   true,
		Score:     100,
		CheckedAt: time.Now(),
	}

	// Get issues in namespace
	issues, err := h.diagnostics.DetectIssuesInNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	result.Issues = issues

	// Calculate score based on issues
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityCritical:
			result.Score -= 25
			result.Healthy = false
		case SeverityWarning:
			result.Score -= 10
		case SeverityInfo:
			result.Score -= 2
		}
	}

	if result.Score < 0 {
		result.Score = 0
	}

	// Generate summary
	if len(issues) == 0 {
		result.Summary = fmt.Sprintf("Namespace %s is healthy", namespace)
	} else {
		criticalCount := 0
		warningCount := 0
		for _, issue := range issues {
			if issue.Severity == SeverityCritical {
				criticalCount++
			} else if issue.Severity == SeverityWarning {
				warningCount++
			}
		}
		result.Summary = fmt.Sprintf("Namespace %s has %d issues (%d critical, %d warnings)",
			namespace, len(issues), criticalCount, warningCount)
	}

	// Generate suggestions
	result.Suggestions = h.generateSuggestionsFromIssues(issues)

	return result, nil
}

// CheckResource performs a health check on a specific resource
func (h *HealthChecker) CheckResource(ctx context.Context, resourceType, name, namespace string) (*HealthCheckResult, error) {
	result := &HealthCheckResult{
		Healthy:   true,
		Score:     100,
		CheckedAt: time.Now(),
	}

	// Get diagnostic report for resource
	report, err := h.diagnostics.DiagnoseResource(ctx, resourceType, name, namespace)
	if err != nil {
		return nil, err
	}

	result.Issues = report.Issues

	// Calculate score based on issues
	for _, issue := range report.Issues {
		switch issue.Severity {
		case SeverityCritical:
			result.Score -= 30
			result.Healthy = false
		case SeverityWarning:
			result.Score -= 15
		case SeverityInfo:
			result.Score -= 5
		}
	}

	if result.Score < 0 {
		result.Score = 0
	}

	result.Summary = report.Summary
	result.Suggestions = h.generateSuggestionsFromIssues(report.Issues)

	return result, nil
}

// podCounts holds pod count statistics
type podCounts struct {
	total   int
	running int
	pending int
	failed  int
}

// checkNodes checks the health of all nodes
func (h *HealthChecker) checkNodes(ctx context.Context) (ComponentHealth, []Issue, error) {
	health := ComponentHealth{
		Status: "healthy",
		Score:  100,
	}

	output, err := h.client.RunJSON(ctx, "get", "nodes")
	if err != nil {
		return health, nil, err
	}

	var nodeList struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(output, &nodeList); err != nil {
		return health, nil, err
	}

	totalNodes := len(nodeList.Items)
	readyNodes := 0
	var issues []Issue

	for _, item := range nodeList.Items {
		status, err := h.diagnostics.parseNodeJSON(item)
		if err != nil {
			continue
		}

		if status.Ready {
			readyNodes++
		}

		nodeIssues := h.diagnostics.detectNodeIssues(status)
		issues = append(issues, nodeIssues...)
	}

	// Calculate score
	if totalNodes > 0 {
		health.Score = (readyNodes * 100) / totalNodes
	}

	// Adjust for issues
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityCritical:
			health.Score -= 20
		case SeverityWarning:
			health.Score -= 5
		}
	}

	if health.Score < 0 {
		health.Score = 0
	}

	// Determine status
	if health.Score < 50 {
		health.Status = "critical"
	} else if health.Score < 80 {
		health.Status = "degraded"
	}

	health.Details = fmt.Sprintf("%d/%d nodes ready", readyNodes, totalNodes)

	return health, issues, nil
}

// checkWorkloads checks the health of workloads
func (h *HealthChecker) checkWorkloads(ctx context.Context) (ComponentHealth, []Issue, podCounts, error) {
	health := ComponentHealth{
		Status: "healthy",
		Score:  100,
	}
	counts := podCounts{}
	var issues []Issue

	// Get all pods
	output, err := h.client.RunJSON(ctx, "get", "pods", "-A")
	if err != nil {
		return health, nil, counts, err
	}

	var podList struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(output, &podList); err != nil {
		return health, nil, counts, err
	}

	counts.total = len(podList.Items)

	for _, item := range podList.Items {
		status, err := h.diagnostics.parsePodJSON(item)
		if err != nil {
			continue
		}

		switch status.Phase {
		case "Running":
			if status.Ready {
				counts.running++
			}
		case "Pending":
			counts.pending++
		case "Failed":
			counts.failed++
		}

		podIssues := h.diagnostics.detectPodIssues(status)
		issues = append(issues, podIssues...)
	}

	// Calculate score based on pod status
	if counts.total > 0 {
		healthyRatio := float64(counts.running) / float64(counts.total)
		health.Score = int(healthyRatio * 100)
	}

	// Adjust for critical issues
	criticalCount := 0
	warningCount := 0
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityCritical:
			criticalCount++
			health.Score -= 10
		case SeverityWarning:
			warningCount++
			health.Score -= 3
		}
	}

	if health.Score < 0 {
		health.Score = 0
	}

	// Determine status
	if health.Score < 50 || criticalCount > 5 {
		health.Status = "critical"
	} else if health.Score < 80 || criticalCount > 0 {
		health.Status = "degraded"
	}

	health.Details = fmt.Sprintf("%d running, %d pending, %d failed pods",
		counts.running, counts.pending, counts.failed)

	return health, issues, counts, nil
}

// checkStorage checks the health of storage resources
func (h *HealthChecker) checkStorage(ctx context.Context) (ComponentHealth, error) {
	health := ComponentHealth{
		Status: "healthy",
		Score:  100,
	}

	// Get PVCs
	output, err := h.client.RunJSON(ctx, "get", "pvc", "-A")
	if err != nil {
		return health, err
	}

	var pvcList struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(output, &pvcList); err != nil {
		return health, err
	}

	totalPVCs := len(pvcList.Items)
	boundPVCs := 0
	pendingPVCs := 0

	for _, pvc := range pvcList.Items {
		switch pvc.Status.Phase {
		case "Bound":
			boundPVCs++
		case "Pending":
			pendingPVCs++
		}
	}

	// Calculate score
	if totalPVCs > 0 {
		health.Score = (boundPVCs * 100) / totalPVCs
	}

	// Determine status
	if pendingPVCs > 0 {
		health.Status = "degraded"
		health.Score -= pendingPVCs * 10
	}

	if health.Score < 0 {
		health.Score = 0
	}

	if health.Score < 50 {
		health.Status = "critical"
	} else if health.Score < 80 {
		health.Status = "degraded"
	}

	health.Details = fmt.Sprintf("%d/%d PVCs bound, %d pending", boundPVCs, totalPVCs, pendingPVCs)

	return health, nil
}

// checkNetwork checks the health of network resources
func (h *HealthChecker) checkNetwork(ctx context.Context) (ComponentHealth, error) {
	health := ComponentHealth{
		Status: "healthy",
		Score:  100,
	}

	// Get services
	output, err := h.client.RunJSON(ctx, "get", "services", "-A")
	if err != nil {
		return health, err
	}

	var svcList struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Type      string `json:"type"`
				ClusterIP string `json:"clusterIP"`
			} `json:"spec"`
			Status struct {
				LoadBalancer struct {
					Ingress []struct {
						IP       string `json:"ip"`
						Hostname string `json:"hostname"`
					} `json:"ingress"`
				} `json:"loadBalancer"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(output, &svcList); err != nil {
		return health, err
	}

	totalServices := len(svcList.Items)
	healthyServices := 0
	pendingLBs := 0

	for _, svc := range svcList.Items {
		if svc.Spec.Type == "LoadBalancer" {
			if len(svc.Status.LoadBalancer.Ingress) > 0 {
				healthyServices++
			} else {
				pendingLBs++
			}
		} else if svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != "None" {
			healthyServices++
		} else {
			healthyServices++ // Headless services are fine
		}
	}

	// Calculate score
	if totalServices > 0 {
		health.Score = (healthyServices * 100) / totalServices
	}

	// Adjust for pending load balancers
	if pendingLBs > 0 {
		health.Status = "degraded"
		health.Score -= pendingLBs * 5
	}

	if health.Score < 0 {
		health.Score = 0
	}

	if health.Score < 50 {
		health.Status = "critical"
	} else if health.Score < 80 {
		health.Status = "degraded"
	}

	health.Details = fmt.Sprintf("%d services, %d pending LoadBalancers", totalServices, pendingLBs)

	return health, nil
}

// calculateOverallScore calculates the overall cluster health score
func (h *HealthChecker) calculateOverallScore(summary *ClusterHealthSummary) int {
	// Weight the component scores
	nodeWeight := 0.30
	workloadWeight := 0.40
	storageWeight := 0.15
	networkWeight := 0.15

	score := float64(summary.NodeHealth.Score)*nodeWeight +
		float64(summary.WorkloadHealth.Score)*workloadWeight +
		float64(summary.StorageHealth.Score)*storageWeight +
		float64(summary.NetworkHealth.Score)*networkWeight

	// Penalize for critical issues
	score -= float64(summary.CriticalIssues) * 5
	// Penalize for warnings
	score -= float64(summary.WarningIssues) * 1

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return int(score)
}

// generateSuggestionsFromIssues generates suggestions based on detected issues
func (h *HealthChecker) generateSuggestionsFromIssues(issues []Issue) []string {
	suggestions := make(map[string]bool)

	for _, issue := range issues {
		for _, s := range issue.Suggestions {
			suggestions[s] = true
		}

		// Add category-specific suggestions
		switch issue.Category {
		case CategoryCrash:
			suggestions["Review pod logs for crash details"] = true
			suggestions["Check resource limits and requests"] = true
		case CategoryImagePull:
			suggestions["Verify image name and tag are correct"] = true
			suggestions["Check image pull secrets"] = true
		case CategoryResourceLimit:
			suggestions["Consider increasing resource limits"] = true
			suggestions["Review application memory usage"] = true
		case CategoryPending:
			suggestions["Check cluster capacity"] = true
			suggestions["Review pod scheduling constraints"] = true
		case CategoryNodePressure:
			suggestions["Consider adding more nodes"] = true
			suggestions["Review workload distribution"] = true
		case CategoryStorage:
			suggestions["Check PVC status and storage class"] = true
			suggestions["Verify storage provisioner is working"] = true
		}
	}

	var result []string
	for s := range suggestions {
		result = append(result, s)
	}

	return result
}
