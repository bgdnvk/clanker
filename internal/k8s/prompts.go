package k8s

import "fmt"

// GetLLMAnalysisPrompt returns the prompt for LLM to analyze what K8s operations are needed
func GetLLMAnalysisPrompt(question string, clusterContext string) string {
	return fmt.Sprintf(`Analyze this user query about a Kubernetes cluster and determine what kubectl operations would be needed to answer it accurately.

User Query: "%s"

Current Cluster Context:
%s

Available Kubernetes READ-ONLY operations (all are safe and never modify or delete anything):

CLUSTER INFORMATION:
- get_cluster_info: Get cluster API server information and status
- get_nodes: List all nodes with status, roles, and resource capacity
- get_node_details: Get detailed information about a specific node (requires name parameter)
- get_namespaces: List all namespaces in the cluster
- get_cluster_version: Get Kubernetes version information
- get_contexts: List available kubectl contexts
- get_current_context: Get the current kubectl context

WORKLOADS:
- list_pods: List pods (supports namespace, all_namespaces, label_selector parameters)
- get_pod_details: Get detailed information about a specific pod (requires name parameter)
- list_deployments: List deployments with replica status
- get_deployment_details: Get deployment configuration and rollout status (requires name parameter)
- list_statefulsets: List StatefulSets with replica status
- list_daemonsets: List DaemonSets with node coverage
- list_replicasets: List ReplicaSets
- list_jobs: List Jobs with completion status
- list_cronjobs: List CronJobs with schedule information

NETWORKING:
- list_services: List services with type, cluster IP, and ports
- get_service_details: Get service endpoints and configuration (requires name parameter)
- list_ingresses: List ingresses with hosts and paths
- get_ingress_details: Get ingress rules and backend configuration (requires name parameter)
- list_endpoints: List endpoints for services
- list_network_policies: List network policies

STORAGE:
- list_pvs: List PersistentVolumes with capacity and status
- list_pvcs: List PersistentVolumeClaims with binding status
- list_storage_classes: List available storage classes
- list_configmaps: List ConfigMaps (names only)
- get_configmap_details: Get ConfigMap data (requires name parameter)
- list_secrets: List Secrets (names only, no sensitive values)

LOGS AND EVENTS:
- get_pod_logs: Get logs from a pod (requires name, supports container, tail_lines, since parameters)
- get_events: Get events in a namespace
- get_recent_events: Get recent events sorted by timestamp
- get_warning_events: Get warning events only

RESOURCE METRICS (requires metrics-server):
- get_node_metrics: Get CPU and memory usage for nodes
- get_pod_metrics: Get CPU and memory usage for pods
- get_top_nodes: Show resource consumption by nodes
- get_top_pods: Show resource consumption by pods

HELM (if available):
- list_helm_releases: List Helm releases across namespaces
- get_release_details: Get Helm release details and values (requires release name)
- list_helm_repos: List configured Helm repositories

TROUBLESHOOTING:
- describe_resource: Describe any K8s resource showing events and conditions (requires resource_type and name)
- get_pod_containers: Get container statuses in a pod (requires name)
- check_pod_errors: Check for CrashLoopBackOff, ImagePullBackOff, and other error states
- get_unhealthy_pods: List pods that are not in Running or Succeeded state
- get_pending_pods: List pods stuck in Pending state

Respond with ONLY a JSON object in this format:
{
  "operations": [
    {
      "operation": "operation_name",
      "reason": "why this operation is needed to answer the question",
      "parameters": {
        "namespace": "optional namespace (omit for all namespaces)",
        "name": "optional resource name",
        "label_selector": "optional label selector like app=nginx",
        "all_namespaces": true,
        "tail_lines": 100,
        "since": "1h",
        "container": "optional container name"
      }
    }
  ],
  "analysis": "brief explanation of what the user wants to know"
}

Important guidelines:
- Only include operations that are necessary to answer the question
- Use all_namespaces: true when the user does not specify a namespace
- For log queries, default tail_lines to 100 unless user specifies otherwise
- For error or troubleshooting queries, include check_pod_errors and get_warning_events
- If no K8s operations are needed, return: {"operations": [], "analysis": "explanation"}`, question, clusterContext)
}

// GetFinalResponsePrompt returns the prompt for generating the final user-facing response
func GetFinalResponsePrompt(question, k8sData, conversationContext string) string {
	prompt := `Based on the Kubernetes cluster data below, please answer the user's question comprehensively.

`
	if conversationContext != "" {
		prompt += fmt.Sprintf(`Previous conversation context (for follow-up questions):
%s

`, conversationContext)
	}

	prompt += fmt.Sprintf(`Current Question: "%s"

Kubernetes Cluster Data:
%s

Instructions:
- Provide a clear, well-formatted markdown response
- Include specific details like pod names, counts, and status information
- Use tables for listing multiple resources when appropriate
- Highlight any issues, warnings, or errors found
- If the data shows problems, suggest potential causes or next steps
- Keep the response concise but complete
- Do not include raw JSON or kubectl output unless specifically asked`, question, k8sData)

	return prompt
}

// GetClusterStatusSummary returns a formatted string of cluster status for context
func GetClusterStatusSummary(nodeCount, podCount, namespaceCount int, version, context string) string {
	return fmt.Sprintf(`Cluster Overview:
- Current Context: %s
- Kubernetes Version: %s
- Total Nodes: %d
- Total Pods: %d
- Total Namespaces: %d`, context, version, nodeCount, podCount, namespaceCount)
}
