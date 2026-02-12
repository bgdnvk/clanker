package sre

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// GKE-specific issue categories
const (
	CategoryGKENodePool       IssueCategory = "gke_node_pool"
	CategoryGKEWorkloadID     IssueCategory = "gke_workload_identity"
	CategoryGKEAutoscaling    IssueCategory = "gke_autoscaling"
	CategoryGKENetworkPolicy  IssueCategory = "gke_network_policy"
	CategoryGKEPreemption     IssueCategory = "gke_preemption"
	CategoryGKEQuotaExceeded  IssueCategory = "gke_quota_exceeded"
	CategoryGKEAutopilot      IssueCategory = "gke_autopilot"
)

// GKEHealthCheck contains GKE-specific health check methods
type GKEHealthCheck struct {
	client K8sClient
	debug  bool
}

// NewGKEHealthCheck creates a new GKE health checker
func NewGKEHealthCheck(client K8sClient, debug bool) *GKEHealthCheck {
	return &GKEHealthCheck{
		client: client,
		debug:  debug,
	}
}

// GKEClusterHealth represents GKE-specific cluster health information
type GKEClusterHealth struct {
	ControlPlaneHealthy   bool              `json:"control_plane_healthy"`
	NodePoolsHealthy      bool              `json:"node_pools_healthy"`
	WorkloadIdentityOK    bool              `json:"workload_identity_ok"`
	AutoscalingStatus     string            `json:"autoscaling_status"`
	NetworkPolicyEnabled  bool              `json:"network_policy_enabled"`
	NodePoolStatuses      []NodePoolStatus  `json:"node_pool_statuses,omitempty"`
	GKESpecificIssues     []Issue           `json:"gke_specific_issues,omitempty"`
	Recommendations       []string          `json:"recommendations,omitempty"`
}

// NodePoolStatus represents the status of a GKE node pool
type NodePoolStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	NodeCount     int    `json:"node_count"`
	ReadyCount    int    `json:"ready_count"`
	Preemptible   bool   `json:"preemptible"`
	Spot          bool   `json:"spot"`
	AutoscaleMin  int    `json:"autoscale_min,omitempty"`
	AutoscaleMax  int    `json:"autoscale_max,omitempty"`
	MachineType   string `json:"machine_type,omitempty"`
}

// CheckGKEClusterHealth performs GKE-specific health checks
func (g *GKEHealthCheck) CheckGKEClusterHealth(ctx context.Context) (*GKEClusterHealth, error) {
	health := &GKEClusterHealth{
		ControlPlaneHealthy: true,
		NodePoolsHealthy:    true,
		WorkloadIdentityOK:  true,
	}

	// Check node pool health by examining nodes
	nodePoolHealth, issues := g.checkNodePoolHealth(ctx)
	health.NodePoolStatuses = nodePoolHealth
	health.GKESpecificIssues = append(health.GKESpecificIssues, issues...)

	// Check for preempted nodes
	preemptionIssues := g.checkPreemptionStatus(ctx)
	health.GKESpecificIssues = append(health.GKESpecificIssues, preemptionIssues...)

	// Check Workload Identity configuration
	wiIssues := g.checkWorkloadIdentity(ctx)
	if len(wiIssues) > 0 {
		health.WorkloadIdentityOK = false
		health.GKESpecificIssues = append(health.GKESpecificIssues, wiIssues...)
	}

	// Determine overall health
	for _, issue := range health.GKESpecificIssues {
		if issue.Severity == SeverityCritical {
			if issue.Category == CategoryGKENodePool {
				health.NodePoolsHealthy = false
			}
		}
	}

	// Add recommendations
	health.Recommendations = g.generateRecommendations(health)

	return health, nil
}

// checkNodePoolHealth checks the health of GKE node pools
func (g *GKEHealthCheck) checkNodePoolHealth(ctx context.Context) ([]NodePoolStatus, []Issue) {
	var poolStatuses []NodePoolStatus
	var issues []Issue

	// Get nodes with GKE labels
	output, err := g.client.Run(ctx, "get", "nodes", "-o",
		"jsonpath={range .items[*]}{.metadata.name},{.metadata.labels.cloud\\.google\\.com/gke-nodepool},{.status.conditions[?(@.type==\"Ready\")].status},{.metadata.labels.cloud\\.google\\.com/gke-preemptible},{.metadata.labels.cloud\\.google\\.com/gke-spot}\n{end}")
	if err != nil {
		return poolStatuses, issues
	}

	// Parse node information and group by pool
	poolNodes := make(map[string][]struct {
		name        string
		ready       bool
		preemptible bool
		spot        bool
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 5 {
			continue
		}

		nodeName := parts[0]
		poolName := parts[1]
		ready := parts[2] == "True"
		preemptible := parts[3] == "true"
		spot := parts[4] == "true"

		if poolName == "" {
			poolName = "default-pool"
		}

		poolNodes[poolName] = append(poolNodes[poolName], struct {
			name        string
			ready       bool
			preemptible bool
			spot        bool
		}{nodeName, ready, preemptible, spot})
	}

	// Build pool statuses
	for poolName, nodes := range poolNodes {
		status := NodePoolStatus{
			Name:      poolName,
			NodeCount: len(nodes),
			Status:    "healthy",
		}

		readyCount := 0
		hasPreemptible := false
		hasSpot := false

		for _, node := range nodes {
			if node.ready {
				readyCount++
			}
			if node.preemptible {
				hasPreemptible = true
			}
			if node.spot {
				hasSpot = true
			}
		}

		status.ReadyCount = readyCount
		status.Preemptible = hasPreemptible
		status.Spot = hasSpot

		// Check for issues
		if readyCount < len(nodes) {
			notReadyCount := len(nodes) - readyCount
			status.Status = "degraded"

			issue := Issue{
				ID:           fmt.Sprintf("gke-nodepool-%s-notready", poolName),
				Severity:     SeverityWarning,
				Category:     CategoryGKENodePool,
				ResourceType: ResourceNode,
				ResourceName: poolName,
				Message:      fmt.Sprintf("Node pool %s has %d/%d nodes not ready", poolName, notReadyCount, len(nodes)),
				Timestamp:    time.Now(),
				Suggestions: []string{
					"Check node conditions for specific issues",
					"Review node events for errors",
					"Consider node pool repair or recreation",
				},
			}

			if readyCount == 0 {
				issue.Severity = SeverityCritical
				status.Status = "critical"
			}

			issues = append(issues, issue)
		}

		poolStatuses = append(poolStatuses, status)
	}

	return poolStatuses, issues
}

// checkPreemptionStatus checks for recently preempted nodes
func (g *GKEHealthCheck) checkPreemptionStatus(ctx context.Context) []Issue {
	var issues []Issue

	// Check for preemption events
	output, err := g.client.Run(ctx, "get", "events", "--all-namespaces",
		"--field-selector=reason=PreemptScheduled,reason=Preempted",
		"-o", "jsonpath={range .items[*]}{.involvedObject.name},{.reason},{.message}\n{end}")
	if err != nil {
		return issues
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	preemptionCount := 0

	for _, line := range lines {
		if line != "" {
			preemptionCount++
		}
	}

	if preemptionCount > 0 {
		issues = append(issues, Issue{
			ID:           "gke-preemption-events",
			Severity:     SeverityInfo,
			Category:     CategoryGKEPreemption,
			ResourceType: ResourceNode,
			Message:      fmt.Sprintf("Found %d preemption events for preemptible/spot nodes", preemptionCount),
			Timestamp:    time.Now(),
			Suggestions: []string{
				"Preemption is expected for preemptible/spot VMs",
				"Ensure workloads are fault-tolerant",
				"Consider PodDisruptionBudgets for availability",
			},
		})
	}

	return issues
}

// checkWorkloadIdentity checks Workload Identity configuration
func (g *GKEHealthCheck) checkWorkloadIdentity(ctx context.Context) []Issue {
	var issues []Issue

	// Check for service accounts with Workload Identity annotation
	output, err := g.client.Run(ctx, "get", "serviceaccounts", "--all-namespaces",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace},{.metadata.name},{.metadata.annotations.iam\\.gke\\.io/gcp-service-account}\n{end}")
	if err != nil {
		return issues
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	annotatedSAs := 0
	misconfiguredSAs := []string{}

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}

		namespace := parts[0]
		saName := parts[1]
		gcpSA := parts[2]

		if gcpSA != "" {
			annotatedSAs++
			// Check if GCP SA annotation looks valid
			if !strings.Contains(gcpSA, "@") || !strings.Contains(gcpSA, ".iam.gserviceaccount.com") {
				misconfiguredSAs = append(misconfiguredSAs, fmt.Sprintf("%s/%s", namespace, saName))
			}
		}
	}

	if len(misconfiguredSAs) > 0 {
		issues = append(issues, Issue{
			ID:           "gke-workload-identity-misconfigured",
			Severity:     SeverityWarning,
			Category:     CategoryGKEWorkloadID,
			Message:      fmt.Sprintf("Found %d service accounts with potentially misconfigured Workload Identity", len(misconfiguredSAs)),
			Details:      strings.Join(misconfiguredSAs, ", "),
			Timestamp:    time.Now(),
			Suggestions: []string{
				"Verify GCP service account email format",
				"Ensure IAM binding exists between K8s SA and GCP SA",
				"Check nodepool has Workload Identity enabled",
			},
		})
	}

	return issues
}

// generateRecommendations generates GKE-specific recommendations
func (g *GKEHealthCheck) generateRecommendations(health *GKEClusterHealth) []string {
	var recommendations []string

	// Check for preemptible/spot usage
	hasPreemptible := false
	hasSpot := false
	for _, pool := range health.NodePoolStatuses {
		if pool.Preemptible {
			hasPreemptible = true
		}
		if pool.Spot {
			hasSpot = true
		}
	}

	if hasPreemptible || hasSpot {
		recommendations = append(recommendations,
			"Using preemptible/spot nodes: ensure workloads have PodDisruptionBudgets configured")
	}

	if !health.WorkloadIdentityOK {
		recommendations = append(recommendations,
			"Review Workload Identity configuration for proper GCP API access")
	}

	// Default recommendations
	if len(recommendations) == 0 {
		recommendations = append(recommendations, "GKE cluster appears healthy")
	}

	return recommendations
}

// GKERemediationSteps returns GKE-specific remediation steps for an issue
func GKERemediationSteps(issue Issue, resourceName, namespace string) []RemediationStep {
	var steps []RemediationStep

	switch issue.Category {
	case CategoryGKENodePool:
		steps = append(steps, RemediationStep{
			Action:      "Check node pool status",
			Description: "Verify node pool health in GCP Console or via gcloud",
			Command:     "gcloud",
			Args:        []string{"container", "node-pools", "describe", resourceName, "--cluster", "<CLUSTER_NAME>", "--region", "<REGION>"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Repair node pool",
			Description: "Trigger node pool repair if nodes are in bad state",
			Command:     "gcloud",
			Args:        []string{"container", "node-pools", "update", resourceName, "--cluster", "<CLUSTER_NAME>", "--region", "<REGION>", "--node-locations", "<ZONES>"},
			Risk:        "medium",
			Automated:   false,
		})

	case CategoryGKEWorkloadID:
		steps = append(steps, RemediationStep{
			Action:      "Verify Workload Identity binding",
			Description: "Check IAM binding between Kubernetes and GCP service accounts",
			Command:     "gcloud",
			Args:        []string{"iam", "service-accounts", "get-iam-policy", "<GCP_SA_EMAIL>"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Create IAM binding",
			Description: "Bind Kubernetes SA to GCP SA for Workload Identity",
			Command:     "gcloud",
			Args: []string{"iam", "service-accounts", "add-iam-policy-binding",
				"<GCP_SA_EMAIL>",
				"--role", "roles/iam.workloadIdentityUser",
				"--member", "serviceAccount:<PROJECT>.svc.id.goog[<NAMESPACE>/<K8S_SA>]"},
			Risk:        "medium",
			Automated:   false,
		})

	case CategoryGKEPreemption:
		steps = append(steps, RemediationStep{
			Action:      "Review preempted workloads",
			Description: "Check which workloads were affected by preemption",
			Command:     "kubectl",
			Args:        []string{"get", "events", "--all-namespaces", "--field-selector=reason=PreemptScheduled"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Configure PodDisruptionBudget",
			Description: "Add PDB to ensure availability during preemption",
			Risk:        "low",
			Automated:   false,
		})

	case CategoryGKEQuotaExceeded:
		steps = append(steps, RemediationStep{
			Action:      "Check GCP quotas",
			Description: "Review project quotas in GCP Console",
			Command:     "gcloud",
			Args:        []string{"compute", "project-info", "describe"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Request quota increase",
			Description: "Submit quota increase request via GCP Console",
			Risk:        "low",
			Automated:   false,
		})

	case CategoryGKEAutopilot:
		steps = append(steps, RemediationStep{
			Action:      "Check Autopilot constraints",
			Description: "Review Autopilot limitations for your workload",
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Adjust resource requests",
			Description: "Ensure pods have appropriate resource requests for Autopilot",
			Risk:        "low",
			Automated:   false,
		})
	}

	return steps
}

// GKEDiagnosticChecks returns GKE-specific diagnostic checks to perform
func GKEDiagnosticChecks() []string {
	return []string{
		"Check node pool status via gcloud container node-pools list",
		"Verify Workload Identity configuration on service accounts",
		"Check for preemption events on preemptible/spot nodes",
		"Review cluster autoscaler status and events",
		"Verify network policy enforcement if enabled",
		"Check for GKE-specific annotations on services and ingresses",
		"Review Cloud Operations (Monitoring/Logging) integration",
	}
}

// GKEHealthNotes returns important notes about GKE health monitoring
func GKEHealthNotes() []string {
	return []string{
		"GKE control plane is managed by Google and automatically monitored",
		"Use gcloud container clusters describe for detailed cluster status",
		"Node pool health can be checked via GCP Console or gcloud",
		"Workload Identity replaces service account key management",
		"Preemptible/Spot node preemption is expected behavior",
		"Enable GKE usage metering for detailed resource tracking",
		"Consider enabling GKE Enterprise for advanced monitoring",
	}
}

// IsGKEIssueCategory checks if an issue category is GKE-specific
func IsGKEIssueCategory(category IssueCategory) bool {
	switch category {
	case CategoryGKENodePool, CategoryGKEWorkloadID, CategoryGKEAutoscaling,
		CategoryGKENetworkPolicy, CategoryGKEPreemption, CategoryGKEQuotaExceeded,
		CategoryGKEAutopilot:
		return true
	}
	return false
}

// GetGKEIssueGuidance returns guidance for GKE-specific issues
func GetGKEIssueGuidance(category IssueCategory) string {
	guidance := map[IssueCategory]string{
		CategoryGKENodePool:      "Node pool issues often require intervention via gcloud or GCP Console. Check node pool status and consider repair or recreation if needed.",
		CategoryGKEWorkloadID:    "Workload Identity issues usually stem from IAM misconfiguration. Verify the binding between Kubernetes SA and GCP SA.",
		CategoryGKEAutoscaling:   "Autoscaling issues may be due to quota limits, node pool constraints, or pod scheduling requirements.",
		CategoryGKENetworkPolicy: "Network policy issues require checking the network policy controller and policy definitions.",
		CategoryGKEPreemption:    "Preemption is expected for preemptible/spot nodes. Ensure workloads are fault-tolerant with proper PDBs.",
		CategoryGKEQuotaExceeded: "Quota issues require requesting increased quotas via GCP Console or reducing resource usage.",
		CategoryGKEAutopilot:     "Autopilot mode has specific constraints. Review workload configuration for compatibility.",
	}

	if g, ok := guidance[category]; ok {
		return g
	}
	return "No specific guidance available for this issue category."
}
