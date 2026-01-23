package k8s

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// K8sOperation represents a K8s operation requested by the LLM
type K8sOperation struct {
	Operation  string                 `json:"operation"`
	Reason     string                 `json:"reason"`
	Parameters map[string]interface{} `json:"parameters"`
}

// K8sAnalysis represents the LLM's analysis of what K8s operations are needed
type K8sAnalysis struct {
	Operations []K8sOperation `json:"operations"`
	Analysis   string         `json:"analysis"`
}

// K8sOperationResult represents the result of a K8s operation
type K8sOperationResult struct {
	Operation string
	Result    string
	Error     error
	Index     int
}

// ExecuteOperations executes K8s operations in parallel and returns combined results
func (c *Client) ExecuteOperations(ctx context.Context, operations []K8sOperation) (string, error) {
	if len(operations) == 0 {
		return "", nil
	}

	verbose := viper.GetBool("debug")
	localMode := viper.GetBool("local_mode")
	delayMs := viper.GetInt("local_delay_ms")

	// Default to local mode with rate limiting
	if !viper.IsSet("local_mode") {
		localMode = true
	}
	if localMode && delayMs == 0 {
		delayMs = 100
	}

	resultChan := make(chan K8sOperationResult, len(operations))
	var wg sync.WaitGroup

	for i, op := range operations {
		wg.Add(1)

		// Rate limiting for local mode
		if localMode && i > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		go func(index int, operation K8sOperation) {
			defer wg.Done()

			if verbose {
				fmt.Printf("[k8s] Starting operation %d: %s\n", index+1, operation.Operation)
			}

			start := time.Now()
			result, err := c.executeK8sOperation(ctx, operation)
			duration := time.Since(start)

			if verbose {
				if err != nil {
					fmt.Printf("[k8s] Operation %d failed (%v): %s - %v\n", index+1, duration, operation.Operation, err)
				} else {
					fmt.Printf("[k8s] Operation %d completed (%v): %s\n", index+1, duration, operation.Operation)
				}
			}

			resultChan <- K8sOperationResult{
				Operation: operation.Operation,
				Result:    result,
				Error:     err,
				Index:     index,
			}
		}(i, op)
	}

	// Wait for all operations and close channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results in order
	results := make([]K8sOperationResult, len(operations))
	for result := range resultChan {
		results[result.Index] = result
	}

	// Build combined results string
	var k8sResults strings.Builder
	for _, result := range results {
		if result.Error != nil {
			k8sResults.WriteString(fmt.Sprintf("[%s] Error: %v\n\n", result.Operation, result.Error))
		} else if result.Result != "" {
			k8sResults.WriteString(fmt.Sprintf("[%s]:\n%s\n\n", result.Operation, result.Result))
		}
	}

	return k8sResults.String(), nil
}

// executeK8sOperation executes a single K8s operation based on its type
func (c *Client) executeK8sOperation(ctx context.Context, op K8sOperation) (string, error) {
	// Extract common parameters
	namespace := c.getStringParam(op.Parameters, "namespace", "")
	allNamespaces := c.getBoolParam(op.Parameters, "all_namespaces", false)
	name := c.getStringParam(op.Parameters, "name", "")
	labelSelector := c.getStringParam(op.Parameters, "label_selector", "")

	switch op.Operation {
	// CLUSTER INFORMATION
	case "get_cluster_info":
		return c.GetClusterInfo(ctx)

	case "get_nodes":
		nodes, err := c.GetNodes(ctx)
		if err != nil {
			return "", err
		}
		return formatNodeList(nodes), nil

	case "get_node_details":
		if name == "" {
			return "", fmt.Errorf("node name required")
		}
		return c.Describe(ctx, "node", name, "")

	case "get_namespaces":
		namespaces, err := c.GetNamespaces(ctx)
		if err != nil {
			return "", err
		}
		return strings.Join(namespaces, "\n"), nil

	case "get_cluster_version":
		return c.GetVersion(ctx)

	case "get_contexts":
		return c.Run(ctx, "config", "get-contexts")

	case "get_current_context":
		return c.GetCurrentContext(ctx)

	// WORKLOADS
	case "list_pods":
		args := []string{"get", "pods", "-o", "wide"}
		if labelSelector != "" {
			args = append(args, "-l", labelSelector)
		}
		if allNamespaces {
			args = append(args, "-A")
			return c.Run(ctx, args...)
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, args...)

	case "get_pod_details":
		if name == "" {
			return "", fmt.Errorf("pod name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.Describe(ctx, "pod", name, namespace)

	case "list_deployments":
		if allNamespaces {
			return c.Run(ctx, "get", "deployments", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.GetDeployments(ctx, namespace)

	case "get_deployment_details":
		if name == "" {
			return "", fmt.Errorf("deployment name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.Describe(ctx, "deployment", name, namespace)

	case "list_statefulsets":
		if allNamespaces {
			return c.Run(ctx, "get", "statefulsets", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "statefulsets", "-o", "wide")

	case "list_daemonsets":
		if allNamespaces {
			return c.Run(ctx, "get", "daemonsets", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "daemonsets", "-o", "wide")

	case "list_replicasets":
		if allNamespaces {
			return c.Run(ctx, "get", "replicasets", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "replicasets", "-o", "wide")

	case "list_jobs":
		if allNamespaces {
			return c.Run(ctx, "get", "jobs", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "jobs", "-o", "wide")

	case "list_cronjobs":
		if allNamespaces {
			return c.Run(ctx, "get", "cronjobs", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "cronjobs", "-o", "wide")

	// NETWORKING
	case "list_services":
		if allNamespaces {
			return c.Run(ctx, "get", "services", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.GetServices(ctx, namespace)

	case "get_service_details":
		if name == "" {
			return "", fmt.Errorf("service name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.Describe(ctx, "service", name, namespace)

	case "list_ingresses":
		if allNamespaces {
			return c.Run(ctx, "get", "ingresses", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "ingresses", "-o", "wide")

	case "get_ingress_details":
		if name == "" {
			return "", fmt.Errorf("ingress name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.Describe(ctx, "ingress", name, namespace)

	case "list_endpoints":
		if allNamespaces {
			return c.Run(ctx, "get", "endpoints", "-A")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "endpoints")

	case "list_network_policies":
		if allNamespaces {
			return c.Run(ctx, "get", "networkpolicies", "-A")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "networkpolicies")

	// STORAGE
	case "list_pvs":
		return c.Run(ctx, "get", "pv", "-o", "wide")

	case "list_pvcs":
		if allNamespaces {
			return c.Run(ctx, "get", "pvc", "-A", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "pvc", "-o", "wide")

	case "list_storage_classes":
		return c.Run(ctx, "get", "storageclass")

	case "list_configmaps":
		if allNamespaces {
			return c.Run(ctx, "get", "configmaps", "-A")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "configmaps")

	case "get_configmap_details":
		if name == "" {
			return "", fmt.Errorf("configmap name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.Describe(ctx, "configmap", name, namespace)

	case "list_secrets":
		if allNamespaces {
			return c.Run(ctx, "get", "secrets", "-A")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "secrets")

	// LOGS AND EVENTS
	case "get_pod_logs":
		if name == "" {
			return "", fmt.Errorf("pod name required")
		}
		if namespace == "" {
			namespace = "default"
		}

		tailLines := c.getIntParam(op.Parameters, "tail_lines", 100)
		since := c.getStringParam(op.Parameters, "since", "")
		container := c.getStringParam(op.Parameters, "container", "")

		return c.Logs(ctx, name, namespace, LogOptions{
			TailLines: tailLines,
			Since:     since,
			Container: container,
		})

	case "get_events":
		if namespace == "" {
			namespace = "default"
		}
		return c.GetEvents(ctx, namespace)

	case "get_recent_events":
		if allNamespaces {
			return c.Run(ctx, "get", "events", "-A", "--sort-by=.lastTimestamp")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "events", "--sort-by=.lastTimestamp")

	case "get_warning_events":
		if allNamespaces {
			return c.Run(ctx, "get", "events", "-A", "--field-selector=type=Warning", "--sort-by=.lastTimestamp")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "events", "--field-selector=type=Warning", "--sort-by=.lastTimestamp")

	// METRICS
	case "get_node_metrics", "get_top_nodes":
		return c.TopNodesWithHeaders(ctx)

	case "get_pod_metrics", "get_top_pods":
		if allNamespaces {
			return c.TopPodsWithHeaders(ctx, "", true)
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.TopPodsWithHeaders(ctx, namespace, false)

	// HELM
	case "list_helm_releases":
		if allNamespaces {
			return c.RunHelm(ctx, "list", "-A")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunHelmWithNamespace(ctx, namespace, "list")

	case "get_release_details":
		releaseName := c.getStringParam(op.Parameters, "release", name)
		if releaseName == "" {
			return "", fmt.Errorf("helm release name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunHelmWithNamespace(ctx, namespace, "status", releaseName)

	case "list_helm_repos":
		return c.RunHelm(ctx, "repo", "list")

	// TROUBLESHOOTING
	case "describe_resource":
		resourceType := c.getStringParam(op.Parameters, "resource_type", "pod")
		if name == "" {
			return "", fmt.Errorf("resource name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.Describe(ctx, resourceType, name, namespace)

	case "get_pod_containers":
		if name == "" {
			return "", fmt.Errorf("pod name required")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "pod", name, "-o", "jsonpath={.status.containerStatuses[*].name}")

	case "check_pod_errors":
		if allNamespaces {
			return c.Run(ctx, "get", "pods", "-A", "--field-selector=status.phase!=Running,status.phase!=Succeeded", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "pods", "--field-selector=status.phase!=Running,status.phase!=Succeeded", "-o", "wide")

	case "get_unhealthy_pods":
		if allNamespaces {
			return c.Run(ctx, "get", "pods", "-A", "--field-selector=status.phase!=Running,status.phase!=Succeeded", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "pods", "--field-selector=status.phase!=Running,status.phase!=Succeeded", "-o", "wide")

	case "get_pending_pods":
		if allNamespaces {
			return c.Run(ctx, "get", "pods", "-A", "--field-selector=status.phase=Pending", "-o", "wide")
		}
		if namespace == "" {
			namespace = "default"
		}
		return c.RunWithNamespace(ctx, namespace, "get", "pods", "--field-selector=status.phase=Pending", "-o", "wide")

	default:
		return "", fmt.Errorf("unknown operation: %s", op.Operation)
	}
}

// Helper functions to extract parameters with defaults

func (c *Client) getStringParam(params map[string]interface{}, key, defaultVal string) string {
	if params == nil {
		return defaultVal
	}
	if val, ok := params[key].(string); ok && val != "" {
		return val
	}
	return defaultVal
}

func (c *Client) getBoolParam(params map[string]interface{}, key string, defaultVal bool) bool {
	if params == nil {
		return defaultVal
	}
	if val, ok := params[key].(bool); ok {
		return val
	}
	return defaultVal
}

func (c *Client) getIntParam(params map[string]interface{}, key string, defaultVal int) int {
	if params == nil {
		return defaultVal
	}
	// JSON numbers come as float64
	if val, ok := params[key].(float64); ok {
		return int(val)
	}
	if val, ok := params[key].(int); ok {
		return val
	}
	return defaultVal
}

// formatNodeList formats a list of nodes for display
func formatNodeList(nodes []NodeInfo) string {
	if len(nodes) == 0 {
		return "No nodes found"
	}

	var sb strings.Builder
	sb.WriteString("NAME\tROLE\tSTATUS\tINTERNAL-IP\tEXTERNAL-IP\n")
	for _, n := range nodes {
		externalIP := n.ExternalIP
		if externalIP == "" {
			externalIP = "<none>"
		}
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n",
			n.Name, n.Role, n.Status, n.InternalIP, externalIP))
	}
	return sb.String()
}
