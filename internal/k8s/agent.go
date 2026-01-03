package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/cli"
	"github.com/bgdnvk/clanker/internal/k8s/cluster"
)

// Agent is the main K8s orchestrator that receives delegated queries from the main agent
type Agent struct {
	client       *Client
	clusterMgr   *cluster.Manager
	debug        bool
	aiDecisionFn AIDecisionFunc
}

// AgentOptions contains options for creating a K8s agent
type AgentOptions struct {
	Debug      bool
	AWSProfile string
	Region     string
	Kubeconfig string
}

// NewAgent creates a K8s agent for handling delegated K8s queries
func NewAgent(debug bool) *Agent {
	return NewAgentWithOptions(AgentOptions{Debug: debug})
}

// NewAgentWithOptions creates a K8s agent with full options
func NewAgentWithOptions(opts AgentOptions) *Agent {
	mgr := cluster.NewManager(opts.Debug)

	// Register the existing cluster provider by default
	mgr.RegisterProvider(cluster.NewExistingProvider(opts.Kubeconfig, opts.Debug))

	// Register EKS provider if AWS profile or region is specified
	if opts.AWSProfile != "" || opts.Region != "" {
		mgr.RegisterProvider(cluster.NewEKSProvider(cluster.EKSProviderOptions{
			AWSProfile: opts.AWSProfile,
			Region:     opts.Region,
			Debug:      opts.Debug,
		}))
	}

	return &Agent{
		clusterMgr: mgr,
		debug:      opts.Debug,
	}
}

// RegisterEKSProvider registers the EKS provider with the agent
func (a *Agent) RegisterEKSProvider(profile, region string) {
	a.clusterMgr.RegisterProvider(cluster.NewEKSProvider(cluster.EKSProviderOptions{
		AWSProfile: profile,
		Region:     region,
		Debug:      a.debug,
	}))
}

// SetAIDecisionFunction sets the function used for AI based decisions
func (a *Agent) SetAIDecisionFunction(fn AIDecisionFunc) {
	a.aiDecisionFn = fn
}

// SetClient sets the kubectl client
func (a *Agent) SetClient(client *Client) {
	a.client = client
}

// EnsureDependencies checks and optionally installs missing CLI tools
func (a *Agent) EnsureDependencies(ctx context.Context) error {
	checker := cli.NewDependencyChecker(a.debug)
	missing := checker.CheckMissing()

	if len(missing) == 0 {
		if a.debug {
			fmt.Println("[k8s-agent] All CLI dependencies are satisfied")
		}
		return nil
	}

	// Print current status
	cli.PrintDependencyStatus(checker.CheckAll())

	// Prompt user for permission to install
	install, err := cli.PromptForInstall(missing)
	if err != nil {
		return fmt.Errorf("failed to prompt for installation: %w", err)
	}

	if !install {
		// User declined, check if we can continue with what we have
		hasKubectl := true
		for _, dep := range missing {
			if dep.Name == "kubectl" && dep.Required {
				hasKubectl = false
				break
			}
		}

		if !hasKubectl {
			return fmt.Errorf("kubectl is required but not installed")
		}

		fmt.Println("\nProceeding with available tools. Some features may be limited.")
		return nil
	}

	// Install missing dependencies
	installer := cli.NewInstaller(a.debug)
	opts := cli.DefaultInstallOptions()

	for _, dep := range missing {
		cli.PrintInstallationStart(dep.Name)

		if err := installer.Install(ctx, dep.Name, opts); err != nil {
			cli.PrintInstallationError(dep.Name, err)
			if dep.Required {
				return fmt.Errorf("failed to install required dependency %s: %w", dep.Name, err)
			}
			// Continue with optional dependencies
			continue
		}

		cli.PrintInstallationSuccess(dep.Name)
	}

	// Verify installations
	fmt.Println("\nVerifying installations...")
	finalStatus := checker.CheckAll()
	cli.PrintDependencyStatus(finalStatus)

	return nil
}

// CheckDependencies returns the status of CLI dependencies without installing
func (a *Agent) CheckDependencies() []cli.DependencyStatus {
	checker := cli.NewDependencyChecker(a.debug)
	return checker.CheckAll()
}

// RegisterClusterProvider registers a cluster provider
func (a *Agent) RegisterClusterProvider(provider cluster.Provider) {
	a.clusterMgr.RegisterProvider(provider)
}

// HandleQuery processes a K8s related query delegated from the main agent
func (a *Agent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*K8sResponse, error) {
	if a.debug {
		fmt.Printf("[k8s-agent] handling query: %s\n", query)
	}

	// Initialize client if needed
	if a.client == nil {
		a.client = NewClient(opts.Kubeconfig, "", a.debug)
	}

	// Analyze the query
	analysis := a.analyzeQuery(query)

	if a.debug {
		fmt.Printf("[k8s-agent] analysis: readonly=%v, category=%s, resources=%v\n",
			analysis.IsReadOnly, analysis.Category, analysis.Resources)
	}

	// For read only operations, execute immediately
	if analysis.IsReadOnly {
		return a.executeReadOnly(ctx, query, analysis, opts)
	}

	// For modifications, generate a plan
	plan, err := a.generatePlan(ctx, query, analysis, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	return &K8sResponse{
		Type:          ResponseTypePlan,
		Plan:          plan,
		NeedsApproval: true,
		Summary:       plan.Summary,
	}, nil
}

// ApplyPlan executes an approved plan
func (a *Agent) ApplyPlan(ctx context.Context, plan *K8sPlan, opts ApplyOptions) error {
	if a.debug {
		fmt.Printf("[k8s-agent] applying plan: %s\n", plan.Summary)
	}

	// Execute infrastructure commands first
	for _, cmd := range plan.Infrastructure {
		if a.debug {
			fmt.Printf("[k8s-agent] infra: %s %s\n", cmd.Service, cmd.Operation)
		}
		// Infrastructure commands will be handled by the AWS client
		// This is a placeholder for the actual implementation
	}

	// Execute bootstrap commands
	for _, cmd := range plan.Bootstrap {
		if a.debug {
			fmt.Printf("[k8s-agent] bootstrap: %s %s\n", cmd.Type, cmd.Operation)
		}
		// Bootstrap commands require SSH or specific tools
		// This is a placeholder for the actual implementation
	}

	// Execute kubectl commands
	for _, cmd := range plan.KubectlCmds {
		if a.debug {
			fmt.Printf("[k8s-agent] kubectl: %v\n", cmd.Args)
		}

		if opts.DryRun {
			fmt.Printf("[dry-run] kubectl %s\n", strings.Join(cmd.Args, " "))
			continue
		}

		output, err := a.client.RunWithNamespace(ctx, cmd.Namespace, cmd.Args...)
		if err != nil {
			return fmt.Errorf("kubectl command failed: %w", err)
		}

		if a.debug {
			fmt.Printf("[k8s-agent] output: %s\n", output)
		}

		// Handle wait conditions
		if cmd.WaitFor != nil {
			if err := a.client.Wait(ctx, cmd.WaitFor.Resource, "", cmd.Namespace,
				cmd.WaitFor.Condition, cmd.WaitFor.Timeout); err != nil {
				return fmt.Errorf("wait condition failed: %w", err)
			}
		}
	}

	// Execute helm commands
	for _, cmd := range plan.HelmCmds {
		if a.debug {
			fmt.Printf("[k8s-agent] helm: %s %s\n", cmd.Action, cmd.Release)
		}
		// Helm commands will be handled by the helm sub-agent
		// This is a placeholder for the actual implementation
	}

	// Apply manifests
	for _, manifest := range plan.Manifests {
		if a.debug {
			fmt.Printf("[k8s-agent] applying manifest: %s/%s\n", manifest.Kind, manifest.Name)
		}

		if opts.DryRun {
			fmt.Printf("[dry-run] apply %s/%s\n", manifest.Kind, manifest.Name)
			continue
		}

		_, err := a.client.Apply(ctx, manifest.Content, manifest.Namespace)
		if err != nil {
			return fmt.Errorf("manifest apply failed for %s: %w", manifest.Name, err)
		}
	}

	// Run validations
	for _, validation := range plan.Validations {
		if a.debug {
			fmt.Printf("[k8s-agent] validating: %s\n", validation.Name)
		}
		// Validation logic will be implemented
	}

	return nil
}

// analyzeQuery determines the nature of a K8s query
func (a *Agent) analyzeQuery(query string) QueryAnalysis {
	queryLower := strings.ToLower(query)

	analysis := QueryAnalysis{
		Resources:  []string{},
		Operations: []string{},
	}

	// Check for read only patterns
	for _, pattern := range readOnlyPatterns {
		if strings.Contains(queryLower, pattern) {
			analysis.IsReadOnly = true
			analysis.Operations = append(analysis.Operations, pattern)
		}
	}

	// Check for modify patterns (these override read only)
	for _, pattern := range modifyPatterns {
		if strings.Contains(queryLower, pattern) {
			analysis.IsReadOnly = false
			analysis.Operations = append(analysis.Operations, pattern)
		}
	}

	// Detect resources mentioned
	for resource, keywords := range resourceKeywords {
		for _, keyword := range keywords {
			if strings.Contains(queryLower, keyword) {
				analysis.Resources = append(analysis.Resources, resource)
				break
			}
		}
	}

	// Determine category
	analysis.Category = a.categorizeQuery(queryLower, analysis)

	// Check for namespace hints
	if strings.Contains(queryLower, "kube-system") {
		analysis.NamespaceHint = "kube-system"
	} else if strings.Contains(queryLower, "default namespace") {
		analysis.NamespaceHint = "default"
	}

	// Check for cluster scope
	analysis.ClusterScope = strings.Contains(queryLower, "cluster") ||
		strings.Contains(queryLower, "node") ||
		strings.Contains(queryLower, "all namespace")

	return analysis
}

// categorizeQuery determines the category of the query
func (a *Agent) categorizeQuery(query string, analysis QueryAnalysis) string {
	// Cluster operations
	if strings.Contains(query, "cluster") && (strings.Contains(query, "create") ||
		strings.Contains(query, "provision") || strings.Contains(query, "setup")) {
		return "cluster_provisioning"
	}

	if strings.Contains(query, "node") && (strings.Contains(query, "add") ||
		strings.Contains(query, "remove") || strings.Contains(query, "scale")) {
		return "cluster_scaling"
	}

	// Workload operations
	if containsAny(query, []string{"deploy", "deployment", "pod", "replica", "statefulset", "daemonset"}) {
		return "workloads"
	}

	// Networking
	if containsAny(query, []string{"service", "ingress", "loadbalancer", "network"}) {
		return "networking"
	}

	// Storage
	if containsAny(query, []string{"pv", "pvc", "storage", "volume", "configmap", "secret"}) {
		return "storage"
	}

	// Helm
	if containsAny(query, []string{"helm", "chart", "release"}) {
		return "helm"
	}

	// Troubleshooting
	if containsAny(query, []string{"error", "issue", "debug", "logs", "troubleshoot", "problem"}) {
		return "troubleshooting"
	}

	return "general"
}

// executeReadOnly handles read only K8s operations
func (a *Agent) executeReadOnly(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sResponse, error) {
	var result strings.Builder

	queryLower := strings.ToLower(query)

	// Handle different read operations based on the query
	switch {
	case strings.Contains(queryLower, "pod"):
		pods, err := a.client.GetPods(ctx, opts.Namespace)
		if err != nil {
			return nil, err
		}
		result.WriteString("Pods:\n")
		result.WriteString(pods)

	case strings.Contains(queryLower, "deployment"):
		deployments, err := a.client.GetDeployments(ctx, opts.Namespace)
		if err != nil {
			return nil, err
		}
		result.WriteString("Deployments:\n")
		result.WriteString(deployments)

	case strings.Contains(queryLower, "service"):
		services, err := a.client.GetServices(ctx, opts.Namespace)
		if err != nil {
			return nil, err
		}
		result.WriteString("Services:\n")
		result.WriteString(services)

	case strings.Contains(queryLower, "node"):
		nodes, err := a.client.GetNodes(ctx)
		if err != nil {
			return nil, err
		}
		result.WriteString("Nodes:\n")
		for _, node := range nodes {
			result.WriteString(fmt.Sprintf("  %s (%s) - %s [%s/%s]\n",
				node.Name, node.Role, node.Status, node.InternalIP, node.ExternalIP))
		}

	case strings.Contains(queryLower, "namespace"):
		namespaces, err := a.client.GetNamespaces(ctx)
		if err != nil {
			return nil, err
		}
		result.WriteString("Namespaces:\n")
		for _, ns := range namespaces {
			result.WriteString(fmt.Sprintf("  %s\n", ns))
		}

	case strings.Contains(queryLower, "event"):
		events, err := a.client.GetEvents(ctx, opts.Namespace)
		if err != nil {
			return nil, err
		}
		result.WriteString("Events:\n")
		result.WriteString(events)

	case strings.Contains(queryLower, "log"):
		// Extract pod name from query if possible
		podName := extractPodName(query)
		if podName != "" {
			logs, err := a.client.Logs(ctx, podName, opts.Namespace, LogOptions{TailLines: 100})
			if err != nil {
				return nil, err
			}
			result.WriteString(fmt.Sprintf("Logs from %s:\n", podName))
			result.WriteString(logs)
		} else {
			result.WriteString("Please specify a pod name for logs")
		}

	case strings.Contains(queryLower, "cluster") && strings.Contains(queryLower, "info"):
		info, err := a.client.GetClusterInfo(ctx)
		if err != nil {
			return nil, err
		}
		result.WriteString("Cluster Info:\n")
		result.WriteString(info)

	case strings.Contains(queryLower, "context"):
		contexts, err := a.client.GetContexts(ctx)
		if err != nil {
			return nil, err
		}
		current, _ := a.client.GetCurrentContext(ctx)
		result.WriteString("Kubernetes Contexts:\n")
		for _, ctx := range contexts {
			if ctx == current {
				result.WriteString(fmt.Sprintf("  * %s (current)\n", ctx))
			} else {
				result.WriteString(fmt.Sprintf("    %s\n", ctx))
			}
		}

	case strings.Contains(queryLower, "eks") && (strings.Contains(queryLower, "list") || strings.Contains(queryLower, "cluster")):
		clusters, err := a.ListEKSClusters(ctx)
		if err != nil {
			return nil, err
		}
		result.WriteString("EKS Clusters:\n")
		if len(clusters) == 0 {
			result.WriteString("  No EKS clusters found in this region\n")
		} else {
			for _, c := range clusters {
				result.WriteString(fmt.Sprintf("  %s (%s) - %s [K8s %s]\n",
					c.Name, c.Status, c.Region, c.KubernetesVersion))
				if c.Endpoint != "" {
					result.WriteString(fmt.Sprintf("    Endpoint: %s\n", c.Endpoint))
				}
				if len(c.WorkerNodes) > 0 {
					result.WriteString(fmt.Sprintf("    Worker Nodes: %d\n", len(c.WorkerNodes)))
				}
			}
		}

	case strings.Contains(queryLower, "health") || strings.Contains(queryLower, "status"):
		health, err := a.clusterMgr.HealthCheck(ctx, opts.ClusterType, opts.ClusterName)
		if err != nil {
			return nil, err
		}
		result.WriteString("Cluster Health:\n")
		result.WriteString(fmt.Sprintf("  Healthy: %v\n", health.Healthy))
		result.WriteString(fmt.Sprintf("  Message: %s\n", health.Message))
		if len(health.NodeStatus) > 0 {
			result.WriteString("  Nodes:\n")
			for name, status := range health.NodeStatus {
				result.WriteString(fmt.Sprintf("    %s: %s\n", name, status))
			}
		}

	default:
		// General info
		result.WriteString("Available operations:\n")
		result.WriteString("  list pods, deployments, services, nodes, namespaces\n")
		result.WriteString("  show logs, events, cluster info\n")
		result.WriteString("  check health, status\n")
	}

	return &K8sResponse{
		Type:          ResponseTypeResult,
		Result:        result.String(),
		NeedsApproval: false,
	}, nil
}

// generatePlan creates a K8s execution plan
func (a *Agent) generatePlan(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sPlan, error) {
	plan := &K8sPlan{
		Version:     1,
		Question:    query,
		ClusterName: opts.ClusterName,
		ClusterType: opts.ClusterType,
		Bindings:    make(map[string]string),
	}

	// If AI decision function is available, use it to generate the plan
	if a.aiDecisionFn != nil {
		return a.generatePlanWithAI(ctx, query, analysis, opts, plan)
	}

	// Otherwise, generate a basic plan based on analysis
	return a.generateBasicPlan(query, analysis, opts, plan)
}

// generatePlanWithAI uses the AI to generate a detailed plan
func (a *Agent) generatePlanWithAI(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions, plan *K8sPlan) (*K8sPlan, error) {
	prompt := fmt.Sprintf(`Generate a Kubernetes execution plan for the following request.

Request: %s

Cluster Type: %s
Namespace: %s
Category: %s
Detected Resources: %v

Return a JSON object with the following structure:
{
  "summary": "Brief description of what the plan does",
  "kubectl_cmds": [
    {"args": ["kubectl", "arg1", "arg2"], "namespace": "default", "reason": "why this command"}
  ],
  "manifests": [
    {"kind": "Deployment", "api_version": "apps/v1", "name": "name", "namespace": "default", "content": "yaml content", "reason": "why"}
  ],
  "validations": [
    {"name": "check name", "command": "kubectl get ...", "expected": "expected output", "fail_action": "fail"}
  ],
  "notes": ["any important notes"],
  "warnings": ["any warnings"]
}

Only include the sections that are relevant. Return valid JSON only.`,
		query,
		opts.ClusterType,
		opts.Namespace,
		analysis.Category,
		analysis.Resources)

	response, err := a.aiDecisionFn(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI plan generation failed: %w", err)
	}

	// Parse the AI response into the plan
	if err := a.parsePlanResponse(response, plan); err != nil {
		// Fall back to basic plan generation
		if a.debug {
			fmt.Printf("[k8s-agent] AI plan parse failed, using basic plan: %v\n", err)
		}
		return a.generateBasicPlan(query, analysis, opts, plan)
	}

	return plan, nil
}

// parsePlanResponse parses AI generated plan JSON
func (a *Agent) parsePlanResponse(response string, plan *K8sPlan) error {
	// Find JSON in response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 || end <= start {
		return fmt.Errorf("no valid JSON found in response")
	}

	// This would use json.Unmarshal in a real implementation
	// For now, we extract the summary at minimum
	jsonStr := response[start : end+1]
	if strings.Contains(jsonStr, "summary") {
		// Extract summary
		summaryStart := strings.Index(jsonStr, `"summary"`)
		if summaryStart != -1 {
			valueStart := strings.Index(jsonStr[summaryStart:], `:`) + summaryStart + 1
			valueEnd := strings.Index(jsonStr[valueStart:], `"`)
			if valueEnd != -1 {
				quoteStart := valueStart + valueEnd + 1
				quoteEnd := strings.Index(jsonStr[quoteStart:], `"`)
				if quoteEnd != -1 {
					plan.Summary = jsonStr[quoteStart : quoteStart+quoteEnd]
				}
			}
		}
	}

	return nil
}

// generateBasicPlan creates a simple plan without AI
func (a *Agent) generateBasicPlan(query string, analysis QueryAnalysis, opts QueryOptions, plan *K8sPlan) (*K8sPlan, error) {
	queryLower := strings.ToLower(query)

	switch analysis.Category {
	case "workloads":
		if strings.Contains(queryLower, "scale") {
			plan.Summary = "Scale workload replicas"
			plan.Notes = append(plan.Notes, "Use kubectl scale command to adjust replica count")
		} else if strings.Contains(queryLower, "restart") {
			plan.Summary = "Restart workload pods"
			plan.KubectlCmds = append(plan.KubectlCmds, KubectlCmd{
				Args:      []string{"rollout", "restart", "deployment"},
				Namespace: opts.Namespace,
				Reason:    "Restart all pods in the deployment",
			})
		}

	case "cluster_provisioning":
		plan.Summary = "Provision new Kubernetes cluster"
		plan.Notes = append(plan.Notes,
			"Cluster provisioning requires additional infrastructure setup",
			"Please specify cluster type: eks, kubeadm, k3s, or kops")
		plan.Warnings = append(plan.Warnings,
			"Cluster provisioning is a complex operation that may take 10-30 minutes")

	default:
		plan.Summary = fmt.Sprintf("Execute K8s operation: %s", query)
		plan.Notes = append(plan.Notes, "This is a basic plan. Use AI mode for more detailed planning.")
	}

	return plan, nil
}

// Query patterns for classification

var readOnlyPatterns = []string{
	"list", "get", "describe", "show", "what", "which", "status", "health",
	"logs", "events", "check", "find", "search", "view", "display",
}

var modifyPatterns = []string{
	"create", "deploy", "install", "scale", "update", "upgrade", "delete",
	"remove", "add", "provision", "setup", "configure", "apply", "rollout",
	"restart", "rollback", "drain", "cordon", "taint", "patch", "edit",
}

var resourceKeywords = map[string][]string{
	"pod":         {"pod", "pods"},
	"deployment":  {"deployment", "deployments", "deploy"},
	"service":     {"service", "services", "svc"},
	"ingress":     {"ingress", "ingresses"},
	"node":        {"node", "nodes"},
	"namespace":   {"namespace", "namespaces", "ns"},
	"configmap":   {"configmap", "configmaps", "cm"},
	"secret":      {"secret", "secrets"},
	"pv":          {"pv", "persistentvolume"},
	"pvc":         {"pvc", "persistentvolumeclaim"},
	"statefulset": {"statefulset", "statefulsets", "sts"},
	"daemonset":   {"daemonset", "daemonsets", "ds"},
	"job":         {"job", "jobs"},
	"cronjob":     {"cronjob", "cronjobs"},
	"hpa":         {"hpa", "horizontalpodautoscaler"},
}

// Helper functions

func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func extractPodName(query string) string {
	// Simple extraction - look for quoted strings or specific patterns
	// This is a basic implementation; a more robust parser would be better
	words := strings.Fields(query)
	for i, word := range words {
		if (word == "pod" || word == "from") && i+1 < len(words) {
			return strings.Trim(words[i+1], `"'`)
		}
	}
	return ""
}

// ListEKSClusters lists all EKS clusters
func (a *Agent) ListEKSClusters(ctx context.Context) ([]ClusterInfo, error) {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return nil, fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.ListClusters(ctx)
}

// GetEKSCluster gets information about a specific EKS cluster
func (a *Agent) GetEKSCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return nil, fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.GetCluster(ctx, clusterName)
}

// CreateEKSCluster creates a new EKS cluster
func (a *Agent) CreateEKSCluster(ctx context.Context, opts cluster.CreateOptions) (*ClusterInfo, error) {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return nil, fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.Create(ctx, opts)
}

// DeleteEKSCluster deletes an EKS cluster
func (a *Agent) DeleteEKSCluster(ctx context.Context, clusterName string) error {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.Delete(ctx, clusterName)
}

// ScaleEKSCluster scales an EKS cluster node group
func (a *Agent) ScaleEKSCluster(ctx context.Context, clusterName string, opts cluster.ScaleOptions) error {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.Scale(ctx, clusterName, opts)
}

// GetEKSKubeconfig updates kubeconfig for an EKS cluster
func (a *Agent) GetEKSKubeconfig(ctx context.Context, clusterName string) (string, error) {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return "", fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.GetKubeconfig(ctx, clusterName)
}

// CheckEKSHealth checks the health of an EKS cluster
func (a *Agent) CheckEKSHealth(ctx context.Context, clusterName string) (*HealthStatus, error) {
	provider, ok := a.clusterMgr.GetProvider(ClusterTypeEKS)
	if !ok {
		return nil, fmt.Errorf("EKS provider not registered; call RegisterEKSProvider first")
	}
	return provider.Health(ctx, clusterName)
}

// IsK8sQuery determines if a query is K8s related
func IsK8sQuery(question string) bool {
	questionLower := strings.ToLower(question)

	k8sKeywords := []string{
		// Core K8s terms
		"kubernetes", "k8s", "kubectl", "kube",
		// Workloads
		"pod", "pods", "deployment", "deployments", "replicaset", "statefulset",
		"daemonset", "job", "cronjob",
		// Networking
		"service", "services", "ingress", "loadbalancer", "nodeport", "clusterip",
		"networkpolicy", "endpoint",
		// Storage
		"pv", "pvc", "persistentvolume", "storageclass", "configmap", "secret",
		// Cluster
		"node", "nodes", "namespace", "cluster", "kubeconfig", "context",
		// Tools
		"helm", "chart", "release", "tiller",
		// Providers
		"eks", "kubeadm", "kops", "k3s", "minikube",
		// Operations
		"rollout", "scale", "drain", "cordon", "taint",
	}

	for _, keyword := range k8sKeywords {
		if strings.Contains(questionLower, keyword) {
			return true
		}
	}

	return false
}
