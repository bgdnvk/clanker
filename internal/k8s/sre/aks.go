package sre

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AKSHealthCheck contains AKS-specific health check methods
type AKSHealthCheck struct {
	client K8sClient
	debug  bool
}

// NewAKSHealthCheck creates a new AKS health checker
func NewAKSHealthCheck(client K8sClient, debug bool) *AKSHealthCheck {
	return &AKSHealthCheck{
		client: client,
		debug:  debug,
	}
}

// AKSClusterHealth represents AKS-specific cluster health information
type AKSClusterHealth struct {
	ControlPlaneHealthy   bool               `json:"control_plane_healthy"`
	NodePoolsHealthy      bool               `json:"node_pools_healthy"`
	ManagedIdentityOK     bool               `json:"managed_identity_ok"`
	NetworkPolicyStatus   string             `json:"network_policy_status"`
	VirtualNodeStatus     string             `json:"virtual_node_status"`
	NodePoolStatuses      []AKSNodePoolStatus `json:"node_pool_statuses,omitempty"`
	AKSSpecificIssues     []Issue            `json:"aks_specific_issues,omitempty"`
	Recommendations       []string           `json:"recommendations,omitempty"`
}

// AKSNodePoolStatus represents the status of an AKS node pool
type AKSNodePoolStatus struct {
	Name              string `json:"name"`
	Status            string `json:"status"`
	NodeCount         int    `json:"node_count"`
	ReadyCount        int    `json:"ready_count"`
	Mode              string `json:"mode"` // System or User
	VMSize            string `json:"vm_size,omitempty"`
	Spot              bool   `json:"spot"`
	PowerState        string `json:"power_state,omitempty"`
	ProvisioningState string `json:"provisioning_state,omitempty"`
}

// CheckAKSClusterHealth performs AKS-specific health checks
func (a *AKSHealthCheck) CheckAKSClusterHealth(ctx context.Context) (*AKSClusterHealth, error) {
	health := &AKSClusterHealth{
		ControlPlaneHealthy: true,
		NodePoolsHealthy:    true,
		ManagedIdentityOK:   true,
	}

	// Check node pool health by examining nodes
	nodePoolHealth, issues := a.checkNodePoolHealth(ctx)
	health.NodePoolStatuses = nodePoolHealth
	health.AKSSpecificIssues = append(health.AKSSpecificIssues, issues...)

	// Check for Spot VM evictions
	evictionIssues := a.checkSpotEvictions(ctx)
	health.AKSSpecificIssues = append(health.AKSSpecificIssues, evictionIssues...)

	// Check Managed Identity configuration
	miIssues := a.checkManagedIdentity(ctx)
	if len(miIssues) > 0 {
		health.ManagedIdentityOK = false
		health.AKSSpecificIssues = append(health.AKSSpecificIssues, miIssues...)
	}

	// Check Virtual Nodes status
	vnIssues := a.checkVirtualNodes(ctx)
	health.AKSSpecificIssues = append(health.AKSSpecificIssues, vnIssues...)

	// Determine overall health
	for _, issue := range health.AKSSpecificIssues {
		if issue.Severity == SeverityCritical {
			if issue.Category == CategoryAKSNodePool {
				health.NodePoolsHealthy = false
			}
		}
	}

	// Add recommendations
	health.Recommendations = a.generateRecommendations(health)

	return health, nil
}

// checkNodePoolHealth checks the health of AKS node pools
func (a *AKSHealthCheck) checkNodePoolHealth(ctx context.Context) ([]AKSNodePoolStatus, []Issue) {
	var poolStatuses []AKSNodePoolStatus
	var issues []Issue

	// Get nodes with AKS labels
	output, err := a.client.Run(ctx, "get", "nodes", "-o",
		"jsonpath={range .items[*]}{.metadata.name},{.metadata.labels.agentpool},{.status.conditions[?(@.type==\"Ready\")].status},{.metadata.labels.kubernetes\\.azure\\.com/scalesetpriority},{.metadata.labels.kubernetes\\.azure\\.com/mode},{.metadata.labels.node\\.kubernetes\\.io/instance-type}\n{end}")
	if err != nil {
		return poolStatuses, issues
	}

	// Parse node information and group by pool
	poolNodes := make(map[string][]struct {
		name   string
		ready  bool
		spot   bool
		mode   string
		vmSize string
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 6 {
			continue
		}

		nodeName := parts[0]
		poolName := parts[1]
		ready := parts[2] == "True"
		spot := parts[3] == "spot"
		mode := parts[4]
		vmSize := parts[5]

		if poolName == "" {
			poolName = "nodepool1"
		}

		poolNodes[poolName] = append(poolNodes[poolName], struct {
			name   string
			ready  bool
			spot   bool
			mode   string
			vmSize string
		}{nodeName, ready, spot, mode, vmSize})
	}

	// Build pool statuses
	for poolName, nodes := range poolNodes {
		status := AKSNodePoolStatus{
			Name:      poolName,
			NodeCount: len(nodes),
			Status:    "healthy",
		}

		readyCount := 0
		hasSpot := false
		mode := ""
		vmSize := ""

		for _, node := range nodes {
			if node.ready {
				readyCount++
			}
			if node.spot {
				hasSpot = true
			}
			if node.mode != "" && mode == "" {
				mode = node.mode
			}
			if node.vmSize != "" && vmSize == "" {
				vmSize = node.vmSize
			}
		}

		status.ReadyCount = readyCount
		status.Spot = hasSpot
		status.Mode = mode
		status.VMSize = vmSize

		// Check for issues
		if readyCount < len(nodes) {
			notReadyCount := len(nodes) - readyCount
			status.Status = "degraded"

			issue := Issue{
				ID:           fmt.Sprintf("aks-nodepool-%s-notready", poolName),
				Severity:     SeverityWarning,
				Category:     CategoryAKSNodePool,
				ResourceType: ResourceNode,
				ResourceName: poolName,
				Message:      fmt.Sprintf("Node pool %s has %d/%d nodes not ready", poolName, notReadyCount, len(nodes)),
				Timestamp:    time.Now(),
				Suggestions: []string{
					"Check node conditions for specific issues",
					"Review node events for errors",
					"Use az aks nodepool show to check pool status",
					"Consider node pool scale-up or repair",
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

// checkSpotEvictions checks for Spot VM eviction events
func (a *AKSHealthCheck) checkSpotEvictions(ctx context.Context) []Issue {
	var issues []Issue

	// Check for eviction events
	output, err := a.client.Run(ctx, "get", "events", "--all-namespaces",
		"--field-selector=reason=Evicted,reason=Preempted",
		"-o", "jsonpath={range .items[*]}{.involvedObject.name},{.reason},{.message}\n{end}")
	if err != nil {
		return issues
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	evictionCount := 0

	for _, line := range lines {
		if line != "" {
			evictionCount++
		}
	}

	if evictionCount > 0 {
		issues = append(issues, Issue{
			ID:           "aks-spot-eviction-events",
			Severity:     SeverityInfo,
			Category:     CategoryAKSSpotEviction,
			ResourceType: ResourceNode,
			Message:      fmt.Sprintf("Found %d eviction events (may include Spot VM evictions)", evictionCount),
			Timestamp:    time.Now(),
			Suggestions: []string{
				"Spot VM eviction is expected when Azure needs capacity",
				"Ensure workloads are fault-tolerant",
				"Consider PodDisruptionBudgets for availability",
				"Use multiple node pools for redundancy",
			},
		})
	}

	return issues
}

// checkManagedIdentity checks Managed Identity configuration
func (a *AKSHealthCheck) checkManagedIdentity(ctx context.Context) []Issue {
	var issues []Issue

	// Check for service accounts with Azure Workload Identity annotation
	output, err := a.client.Run(ctx, "get", "serviceaccounts", "--all-namespaces",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace},{.metadata.name},{.metadata.annotations.azure\\.workload\\.identity/client-id}\n{end}")
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
		clientID := parts[2]

		if clientID != "" {
			annotatedSAs++
			// Check if client ID looks like a valid GUID
			if len(clientID) != 36 || strings.Count(clientID, "-") != 4 {
				misconfiguredSAs = append(misconfiguredSAs, fmt.Sprintf("%s/%s", namespace, saName))
			}
		}
	}

	if len(misconfiguredSAs) > 0 {
		issues = append(issues, Issue{
			ID:           "aks-managed-identity-misconfigured",
			Severity:     SeverityWarning,
			Category:     CategoryAKSManagedIdentity,
			Message:      fmt.Sprintf("Found %d service accounts with potentially misconfigured Workload Identity", len(misconfiguredSAs)),
			Details:      strings.Join(misconfiguredSAs, ", "),
			Timestamp:    time.Now(),
			Suggestions: []string{
				"Verify client ID is a valid Azure AD application ID",
				"Ensure federated identity credential is configured",
				"Check Azure RBAC assignments for the managed identity",
			},
		})
	}

	return issues
}

// checkVirtualNodes checks Virtual Nodes status
func (a *AKSHealthCheck) checkVirtualNodes(ctx context.Context) []Issue {
	var issues []Issue

	// Check for virtual-kubelet nodes
	output, err := a.client.Run(ctx, "get", "nodes", "-l", "type=virtual-kubelet",
		"-o", "jsonpath={range .items[*]}{.metadata.name},{.status.conditions[?(@.type==\"Ready\")].status}\n{end}")
	if err != nil {
		return issues
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	notReadyVN := []string{}

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}

		nodeName := parts[0]
		ready := parts[1] == "True"

		if !ready {
			notReadyVN = append(notReadyVN, nodeName)
		}
	}

	if len(notReadyVN) > 0 {
		issues = append(issues, Issue{
			ID:           "aks-virtual-nodes-notready",
			Severity:     SeverityWarning,
			Category:     CategoryAKSVirtualNode,
			ResourceType: ResourceNode,
			Message:      fmt.Sprintf("Found %d Virtual Nodes not ready", len(notReadyVN)),
			Details:      strings.Join(notReadyVN, ", "),
			Timestamp:    time.Now(),
			Suggestions: []string{
				"Check ACI connector pod in kube-system namespace",
				"Verify Virtual Nodes addon is enabled",
				"Check ACI quota in the Azure region",
				"Review ACI connector logs for errors",
			},
		})
	}

	return issues
}

// generateRecommendations generates AKS-specific recommendations
func (a *AKSHealthCheck) generateRecommendations(health *AKSClusterHealth) []string {
	var recommendations []string

	// Check for Spot usage
	hasSpot := false
	for _, pool := range health.NodePoolStatuses {
		if pool.Spot {
			hasSpot = true
			break
		}
	}

	if hasSpot {
		recommendations = append(recommendations,
			"Using Spot VMs: ensure workloads have PodDisruptionBudgets configured")
	}

	if !health.ManagedIdentityOK {
		recommendations = append(recommendations,
			"Review Workload Identity configuration for proper Azure API access")
	}

	// Default recommendations
	if len(recommendations) == 0 {
		recommendations = append(recommendations, "AKS cluster appears healthy")
	}

	return recommendations
}

// AKSRemediationSteps returns AKS-specific remediation steps for an issue
func AKSRemediationSteps(issue Issue, resourceName, namespace string) []RemediationStep {
	var steps []RemediationStep

	switch issue.Category {
	case CategoryAKSNodePool:
		steps = append(steps, RemediationStep{
			Order:       1,
			Action:      "Check node pool status",
			Description: "Verify node pool health via Azure CLI",
			Command:     "az",
			Args:        []string{"aks", "nodepool", "show", "--resource-group", "<RESOURCE_GROUP>", "--cluster-name", "<CLUSTER_NAME>", "--name", resourceName},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Order:       2,
			Action:      "Scale node pool",
			Description: "Scale up node pool if capacity is needed",
			Command:     "az",
			Args:        []string{"aks", "nodepool", "scale", "--resource-group", "<RESOURCE_GROUP>", "--cluster-name", "<CLUSTER_NAME>", "--name", resourceName, "--node-count", "<COUNT>"},
			Risk:        "medium",
			Automated:   false,
		})

	case CategoryAKSManagedIdentity:
		steps = append(steps, RemediationStep{
			Order:       1,
			Action:      "Verify federated identity credential",
			Description: "Check federated identity configuration on managed identity",
			Command:     "az",
			Args:        []string{"identity", "federated-credential", "list", "--resource-group", "<RESOURCE_GROUP>", "--identity-name", "<IDENTITY_NAME>"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Order:       2,
			Action:      "Create federated credential",
			Description: "Create federated credential for Workload Identity",
			Command:     "az",
			Args: []string{"identity", "federated-credential", "create",
				"--name", "<CREDENTIAL_NAME>",
				"--identity-name", "<IDENTITY_NAME>",
				"--resource-group", "<RESOURCE_GROUP>",
				"--issuer", "<OIDC_ISSUER>",
				"--subject", "system:serviceaccount:<NAMESPACE>:<SA_NAME>"},
			Risk:        "medium",
			Automated:   false,
		})

	case CategoryAKSSpotEviction:
		steps = append(steps, RemediationStep{
			Order:       1,
			Action:      "Review evicted workloads",
			Description: "Check which workloads were affected by eviction",
			Command:     "kubectl",
			Args:        []string{"get", "events", "--all-namespaces", "--field-selector=reason=Evicted"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Order:       2,
			Action:      "Configure PodDisruptionBudget",
			Description: "Add PDB to ensure availability during evictions",
			Risk:        "low",
			Automated:   false,
		})

	case CategoryAKSQuotaExceeded:
		steps = append(steps, RemediationStep{
			Order:       1,
			Action:      "Check Azure quotas",
			Description: "Review subscription quotas in Azure Portal or CLI",
			Command:     "az",
			Args:        []string{"vm", "list-usage", "--location", "<LOCATION>", "-o", "table"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Order:       2,
			Action:      "Request quota increase",
			Description: "Submit quota increase request via Azure Portal",
			Risk:        "low",
			Automated:   false,
		})

	case CategoryAKSVirtualNode:
		steps = append(steps, RemediationStep{
			Order:       1,
			Action:      "Check ACI connector",
			Description: "Verify the ACI connector pod is running",
			Command:     "kubectl",
			Args:        []string{"get", "pods", "-n", "kube-system", "-l", "app=aci-connector-linux"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Order:       2,
			Action:      "Restart ACI connector",
			Description: "Restart the ACI connector deployment",
			Command:     "kubectl",
			Args:        []string{"rollout", "restart", "deployment", "aci-connector-linux", "-n", "kube-system"},
			Risk:        "medium",
			Automated:   false,
		})

	case CategoryAKSAutoscaling:
		steps = append(steps, RemediationStep{
			Order:       1,
			Action:      "Check autoscaler status",
			Description: "Review cluster autoscaler events",
			Command:     "kubectl",
			Args:        []string{"get", "events", "-n", "kube-system", "--field-selector=source=cluster-autoscaler"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Order:       2,
			Action:      "Update autoscaler settings",
			Description: "Adjust autoscaler min/max settings if needed",
			Command:     "az",
			Args:        []string{"aks", "nodepool", "update", "--resource-group", "<RESOURCE_GROUP>", "--cluster-name", "<CLUSTER_NAME>", "--name", resourceName, "--enable-cluster-autoscaler", "--min-count", "<MIN>", "--max-count", "<MAX>"},
			Risk:        "medium",
			Automated:   false,
		})
	}

	return steps
}

// AKSDiagnosticChecks returns AKS-specific diagnostic checks to perform
func AKSDiagnosticChecks() []string {
	return []string{
		"Check node pool status via az aks nodepool list",
		"Verify Workload Identity configuration on service accounts",
		"Check for Spot VM eviction events",
		"Review cluster autoscaler status and events",
		"Verify network policy enforcement if enabled",
		"Check for AKS-specific annotations on services and ingresses",
		"Review Azure Monitor and Container Insights integration",
		"Check Virtual Nodes (ACI) connector status if enabled",
	}
}

// AKSHealthNotes returns important notes about AKS health monitoring
func AKSHealthNotes() []string {
	return []string{
		"AKS control plane is managed by Azure and automatically monitored",
		"Use az aks show for detailed cluster status",
		"Node pool health can be checked via Azure Portal or az CLI",
		"Workload Identity replaces pod identity for Azure API access",
		"Spot VM eviction is expected behavior when Azure needs capacity",
		"Enable Azure Monitor for Containers for detailed metrics",
		"Consider enabling Azure Policy for AKS for compliance",
		"Use AKS Diagnostics in Azure Portal for troubleshooting",
	}
}

// IsAKSIssueCategory checks if an issue category is AKS-specific
func IsAKSIssueCategory(category IssueCategory) bool {
	switch category {
	case CategoryAKSNodePool, CategoryAKSManagedIdentity, CategoryAKSSpotEviction,
		CategoryAKSQuotaExceeded, CategoryAKSNetworkPolicy, CategoryAKSVirtualNode,
		CategoryAKSAutoscaling:
		return true
	}
	return false
}

// GetAKSIssueGuidance returns AKS-specific guidance for an issue category
func GetAKSIssueGuidance(category IssueCategory) string {
	guidance := map[IssueCategory]string{
		CategoryAKSNodePool:        "Node pool issues often require intervention via az CLI or Azure Portal. Check node pool status and consider scale or repair if needed.",
		CategoryAKSManagedIdentity: "Managed Identity issues usually stem from federated credential misconfiguration. Verify the binding between K8s SA and Azure AD application.",
		CategoryAKSSpotEviction:    "Spot eviction is expected when Azure needs capacity. Ensure workloads are fault-tolerant with proper PDBs.",
		CategoryAKSQuotaExceeded:   "Quota issues require requesting increased quotas via Azure Portal or reducing resource usage.",
		CategoryAKSNetworkPolicy:   "Network policy issues require checking the network policy provider (Azure NPM or Calico) and policy definitions.",
		CategoryAKSVirtualNode:     "Virtual Node issues may indicate ACI connector problems. Check connector pod status and ACI regional availability.",
		CategoryAKSAutoscaling:     "Autoscaling issues may be due to quota limits, node pool constraints, or pod scheduling requirements.",
	}

	if g, ok := guidance[category]; ok {
		return g
	}
	return "No specific guidance available for this issue category."
}

// AKSManagedIdentityChecks returns checks for Managed Identity configuration
func AKSManagedIdentityChecks() []string {
	return []string{
		"Verify azure.workload.identity/client-id annotation on service account",
		"Check federated identity credential exists on managed identity",
		"Verify OIDC issuer URL matches cluster configuration",
		"Confirm Azure RBAC role assignments for managed identity",
		"Ensure azure.workload.identity/use: true label is set on pods",
		"Check managed identity has necessary API permissions",
	}
}

// GKESREComparison returns comparison notes between AKS and GKE SRE
func GKESREComparison() map[string]string {
	return map[string]string{
		"aks_identity":         "Workload Identity (federated credentials)",
		"gke_identity":         "Workload Identity (IAM binding)",
		"eks_identity":         "IRSA (IAM Roles for Service Accounts)",
		"aks_node_pool_check":  "az aks nodepool show",
		"gke_node_pool_check":  "gcloud container node-pools describe",
		"eks_node_pool_check":  "aws eks describe-nodegroup",
		"aks_spot_eviction":    "Azure Spot VM eviction",
		"gke_preemption":       "GKE preemptible/spot preemption",
		"eks_spot_eviction":    "EC2 Spot interruption",
		"aks_diagnostics":      "AKS Diagnostics (Azure Portal)",
		"gke_diagnostics":      "GKE Dashboard",
		"eks_diagnostics":      "CloudWatch Container Insights",
	}
}
