package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/cli"
	"github.com/bgdnvk/clanker/internal/k8s/cluster"
	"github.com/bgdnvk/clanker/internal/k8s/helm"
	"github.com/bgdnvk/clanker/internal/k8s/networking"
	"github.com/bgdnvk/clanker/internal/k8s/sre"
	"github.com/bgdnvk/clanker/internal/k8s/storage"
	"github.com/bgdnvk/clanker/internal/k8s/workloads"
)

// clientAdapter wraps Client to implement workloads.K8sClient interface
type clientAdapter struct {
	client *Client
}

func (a *clientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *clientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *clientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}

func (a *clientAdapter) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Describe(ctx, resourceType, name, namespace)
}

func (a *clientAdapter) Scale(ctx context.Context, resourceType, name, namespace string, replicas int) (string, error) {
	return a.client.Scale(ctx, resourceType, name, namespace, replicas)
}

func (a *clientAdapter) Rollout(ctx context.Context, action, resourceType, name, namespace string) (string, error) {
	return a.client.Rollout(ctx, action, resourceType, name, namespace)
}

func (a *clientAdapter) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Delete(ctx, resourceType, name, namespace)
}

func (a *clientAdapter) Logs(ctx context.Context, podName, namespace string, opts workloads.LogOptionsInternal) (string, error) {
	return a.client.Logs(ctx, podName, namespace, LogOptions{
		Container: opts.Container,
		Follow:    opts.Follow,
		Previous:  opts.Previous,
		TailLines: opts.TailLines,
		Since:     opts.Since,
	})
}

// networkingClientAdapter wraps Client to implement networking.K8sClient interface
type networkingClientAdapter struct {
	client *Client
}

func (a *networkingClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *networkingClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *networkingClientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}

func (a *networkingClientAdapter) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Describe(ctx, resourceType, name, namespace)
}

func (a *networkingClientAdapter) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Delete(ctx, resourceType, name, namespace)
}

func (a *networkingClientAdapter) Apply(ctx context.Context, manifest string) (string, error) {
	// Pass empty namespace - kubectl will use the namespace from the manifest
	return a.client.Apply(ctx, manifest, "")
}

// storageClientAdapter wraps Client to implement storage.K8sClient interface
type storageClientAdapter struct {
	client *Client
}

func (a *storageClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *storageClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *storageClientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}

func (a *storageClientAdapter) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Describe(ctx, resourceType, name, namespace)
}

func (a *storageClientAdapter) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Delete(ctx, resourceType, name, namespace)
}

func (a *storageClientAdapter) Apply(ctx context.Context, manifest string) (string, error) {
	// Pass empty namespace - kubectl will use the namespace from the manifest
	return a.client.Apply(ctx, manifest, "")
}

// helmClientAdapter wraps Client to implement helm.HelmClient interface
type helmClientAdapter struct {
	client *Client
}

func (a *helmClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.RunHelm(ctx, args...)
}

func (a *helmClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunHelmWithNamespace(ctx, namespace, args...)
}

// sreClientAdapter wraps Client to implement sre.K8sClient interface
type sreClientAdapter struct {
	client *Client
}

func (a *sreClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *sreClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *sreClientAdapter) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	return a.client.RunJSON(ctx, args...)
}

// Agent is the main K8s orchestrator that receives delegated queries from the main agent
type Agent struct {
	client       *Client
	clusterMgr   *cluster.Manager
	workloads    *workloads.SubAgent
	networking   *networking.SubAgent
	storage      *storage.SubAgent
	helm         *helm.SubAgent
	sre          *sre.SubAgent
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

// RegisterKubeadmProvider registers the kubeadm provider with the agent
func (a *Agent) RegisterKubeadmProvider(opts KubeadmProviderOptions) {
	a.clusterMgr.RegisterProvider(cluster.NewKubeadmProvider(cluster.KubeadmProviderOptions{
		AWSProfile:  opts.AWSProfile,
		Region:      opts.Region,
		VPCID:       opts.VPCID,
		SubnetID:    opts.SubnetID,
		KeyPairName: opts.KeyPairName,
		SSHKeyPath:  opts.SSHKeyPath,
		Debug:       a.debug,
	}))
}

// KubeadmProviderOptions contains options for registering a kubeadm provider
type KubeadmProviderOptions struct {
	AWSProfile  string
	Region      string
	VPCID       string
	SubnetID    string
	KeyPairName string
	SSHKeyPath  string
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

// GetClusterProvider returns a cluster provider by type
func (a *Agent) GetClusterProvider(clusterType ClusterType) (cluster.Provider, bool) {
	return a.clusterMgr.GetProvider(clusterType)
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

	// Initialize workloads sub-agent if needed
	if a.workloads == nil {
		a.workloads = workloads.NewSubAgent(&clientAdapter{client: a.client}, a.debug)
	}

	// Initialize networking sub-agent if needed
	if a.networking == nil {
		a.networking = networking.NewSubAgent(&networkingClientAdapter{client: a.client}, a.debug)
	}

	// Initialize storage sub-agent if needed
	if a.storage == nil {
		a.storage = storage.NewSubAgent(&storageClientAdapter{client: a.client}, a.debug)
	}

	// Initialize helm sub-agent if needed
	if a.helm == nil {
		a.helm = helm.NewSubAgent(&helmClientAdapter{client: a.client}, a.debug)
	}

	// Initialize sre sub-agent if needed
	if a.sre == nil {
		a.sre = sre.NewSubAgent(&sreClientAdapter{client: a.client}, a.debug)
	}

	// Analyze the query
	analysis := a.analyzeQuery(query)

	if a.debug {
		fmt.Printf("[k8s-agent] analysis: readonly=%v, category=%s, resources=%v\n",
			analysis.IsReadOnly, analysis.Category, analysis.Resources)
	}

	// Delegate workload queries to the workloads sub-agent
	if analysis.Category == "workloads" {
		return a.handleWorkloadQuery(ctx, query, analysis, opts)
	}

	// Delegate networking queries to the networking sub-agent
	if analysis.Category == "networking" {
		return a.handleNetworkingQuery(ctx, query, analysis, opts)
	}

	// Delegate storage queries to the storage sub-agent
	if analysis.Category == "storage" {
		return a.handleStorageQuery(ctx, query, analysis, opts)
	}

	// Delegate helm queries to the helm sub-agent
	if analysis.Category == "helm" {
		return a.handleHelmQuery(ctx, query, analysis, opts)
	}

	// Delegate sre queries to the sre sub-agent
	if analysis.Category == "sre" {
		return a.handleSREQuery(ctx, query, analysis, opts)
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
	if containsAny(query, []string{"service", "ingress", "loadbalancer", "network", "endpoint"}) {
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

	// SRE and Troubleshooting
	if containsAny(query, []string{"health", "healthy", "diagnose", "diagnostic", "troubleshoot",
		"error", "issue", "problem", "crash", "failing", "failed",
		"why is", "why are", "what is wrong", "what's wrong",
		"fix", "remediate", "analyze", "investigate"}) {
		return "sre"
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

// handleWorkloadQuery delegates workload queries to the workloads sub-agent
func (a *Agent) handleWorkloadQuery(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sResponse, error) {
	if a.debug {
		fmt.Printf("[k8s-agent] delegating to workloads sub-agent\n")
	}

	// Convert QueryOptions to workloads.QueryOptions
	workloadOpts := workloads.QueryOptions{
		Namespace:     opts.Namespace,
		AllNamespaces: analysis.ClusterScope,
	}

	response, err := a.workloads.HandleQuery(ctx, query, workloadOpts)
	if err != nil {
		return nil, err
	}

	// Convert workloads.Response to K8sResponse
	k8sResponse := &K8sResponse{
		NeedsApproval: false,
	}

	switch response.Type {
	case workloads.ResponseTypeResult:
		k8sResponse.Type = ResponseTypeResult
		if str, ok := response.Data.(string); ok {
			k8sResponse.Result = str
		} else {
			k8sResponse.Result = response.Message
		}
	case workloads.ResponseTypePlan:
		k8sResponse.Type = ResponseTypePlan
		k8sResponse.NeedsApproval = true
		k8sResponse.Summary = response.Message
		// Convert workloads.WorkloadPlan to K8sPlan
		if response.Plan != nil {
			k8sResponse.Plan = convertWorkloadPlanToK8sPlan(response.Plan)
		}
	}

	return k8sResponse, nil
}

// convertWorkloadPlanToK8sPlan converts a workloads plan to a K8s plan
func convertWorkloadPlanToK8sPlan(wp *workloads.WorkloadPlan) *K8sPlan {
	plan := &K8sPlan{
		Version:  wp.Version,
		Question: wp.Summary,
		Summary:  wp.Summary,
		Notes:    wp.Notes,
		Bindings: make(map[string]string),
	}

	// Convert steps to kubectl commands
	for _, step := range wp.Steps {
		if step.Command == "kubectl" {
			cmd := KubectlCmd{
				Args:   step.Args,
				Reason: step.Reason,
			}
			if step.WaitFor != nil {
				cmd.WaitFor = &WaitCondition{
					Resource:  step.WaitFor.Resource,
					Condition: step.WaitFor.Condition,
					Timeout:   step.WaitFor.Timeout,
				}
			}
			plan.KubectlCmds = append(plan.KubectlCmds, cmd)
		}
	}

	return plan
}

// handleNetworkingQuery delegates networking queries to the networking sub-agent
func (a *Agent) handleNetworkingQuery(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sResponse, error) {
	if a.debug {
		fmt.Printf("[k8s-agent] delegating to networking sub-agent\n")
	}

	// Convert QueryOptions to networking.QueryOptions
	networkingOpts := networking.QueryOptions{
		Namespace:     opts.Namespace,
		AllNamespaces: analysis.ClusterScope,
	}

	response, err := a.networking.HandleQuery(ctx, query, networkingOpts)
	if err != nil {
		return nil, err
	}

	// Convert networking.Response to K8sResponse
	k8sResponse := &K8sResponse{
		NeedsApproval: false,
	}

	switch response.Type {
	case networking.ResponseTypeResult:
		k8sResponse.Type = ResponseTypeResult
		if str, ok := response.Data.(string); ok {
			k8sResponse.Result = str
		} else if response.Data != nil {
			// Format structured data as readable output
			k8sResponse.Result = formatNetworkingData(response.Data)
		} else {
			k8sResponse.Result = response.Message
		}
	case networking.ResponseTypePlan:
		k8sResponse.Type = ResponseTypePlan
		k8sResponse.NeedsApproval = true
		k8sResponse.Summary = response.Message
		// Convert networking.NetworkingPlan to K8sPlan
		if response.Plan != nil {
			k8sResponse.Plan = convertNetworkingPlanToK8sPlan(response.Plan)
		}
	}

	return k8sResponse, nil
}

// convertNetworkingPlanToK8sPlan converts a networking plan to a K8s plan
func convertNetworkingPlanToK8sPlan(np *networking.NetworkingPlan) *K8sPlan {
	plan := &K8sPlan{
		Version:  np.Version,
		Question: np.Summary,
		Summary:  np.Summary,
		Notes:    np.Notes,
		Bindings: make(map[string]string),
	}

	// Convert steps to kubectl commands or manifests
	for _, step := range np.Steps {
		if step.Command == "kubectl" {
			cmd := KubectlCmd{
				Args:   step.Args,
				Reason: step.Reason,
			}
			if step.WaitFor != nil {
				cmd.WaitFor = &WaitCondition{
					Resource:  step.WaitFor.Resource,
					Condition: step.WaitFor.Condition,
					Timeout:   step.WaitFor.Timeout,
				}
			}
			plan.KubectlCmds = append(plan.KubectlCmds, cmd)
		}

		// Handle manifest-based steps
		if step.Manifest != "" {
			plan.Manifests = append(plan.Manifests, Manifest{
				Name:    step.ID,
				Content: step.Manifest,
				Reason:  step.Reason,
			})
		}
	}

	return plan
}

// handleStorageQuery delegates storage queries to the storage sub-agent
func (a *Agent) handleStorageQuery(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sResponse, error) {
	if a.debug {
		fmt.Printf("[k8s-agent] delegating to storage sub-agent\n")
	}

	// Convert QueryOptions to storage.QueryOptions
	storageOpts := storage.QueryOptions{
		Namespace:     opts.Namespace,
		AllNamespaces: analysis.ClusterScope,
	}

	response, err := a.storage.HandleQuery(ctx, query, storageOpts)
	if err != nil {
		return nil, err
	}

	// Convert storage.Response to K8sResponse
	k8sResponse := &K8sResponse{
		NeedsApproval: false,
	}

	switch response.Type {
	case storage.ResponseTypeResult:
		k8sResponse.Type = ResponseTypeResult
		if str, ok := response.Data.(string); ok {
			k8sResponse.Result = str
		} else if response.Data != nil {
			// Format structured data as readable output
			k8sResponse.Result = formatStorageData(response.Data)
		} else {
			k8sResponse.Result = response.Message
		}
	case storage.ResponseTypePlan:
		k8sResponse.Type = ResponseTypePlan
		k8sResponse.NeedsApproval = true
		k8sResponse.Summary = response.Message
		// Convert storage.StoragePlan to K8sPlan
		if response.Plan != nil {
			k8sResponse.Plan = convertStoragePlanToK8sPlan(response.Plan)
		}
	}

	return k8sResponse, nil
}

// convertStoragePlanToK8sPlan converts a storage plan to a K8s plan
func convertStoragePlanToK8sPlan(sp *storage.StoragePlan) *K8sPlan {
	plan := &K8sPlan{
		Version:  sp.Version,
		Question: sp.Summary,
		Summary:  sp.Summary,
		Notes:    sp.Notes,
		Bindings: make(map[string]string),
	}

	// Convert steps to kubectl commands or manifests
	for _, step := range sp.Steps {
		if step.Command == "kubectl" {
			cmd := KubectlCmd{
				Args:   step.Args,
				Reason: step.Reason,
			}
			if step.WaitFor != nil {
				cmd.WaitFor = &WaitCondition{
					Resource:  step.WaitFor.Resource,
					Condition: step.WaitFor.Condition,
					Timeout:   step.WaitFor.Timeout,
				}
			}
			plan.KubectlCmds = append(plan.KubectlCmds, cmd)
		}

		// Handle manifest-based steps
		if step.Manifest != "" {
			plan.Manifests = append(plan.Manifests, Manifest{
				Name:    step.ID,
				Content: step.Manifest,
				Reason:  step.Reason,
			})
		}
	}

	return plan
}

// handleHelmQuery delegates helm queries to the helm sub-agent
func (a *Agent) handleHelmQuery(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sResponse, error) {
	if a.debug {
		fmt.Printf("[k8s-agent] delegating to helm sub-agent\n")
	}

	// Convert QueryOptions to helm.QueryOptions
	helmOpts := helm.QueryOptions{
		Namespace:     opts.Namespace,
		AllNamespaces: analysis.ClusterScope,
	}

	response, err := a.helm.HandleQuery(ctx, query, helmOpts)
	if err != nil {
		return nil, err
	}

	// Convert helm.Response to K8sResponse
	k8sResponse := &K8sResponse{
		NeedsApproval: false,
	}

	switch response.Type {
	case helm.ResponseTypeResult:
		k8sResponse.Type = ResponseTypeResult
		if str, ok := response.Data.(string); ok {
			k8sResponse.Result = str
		} else if response.Data != nil {
			// Format structured data as readable output
			k8sResponse.Result = formatHelmData(response.Data)
		} else {
			k8sResponse.Result = response.Message
		}
	case helm.ResponseTypePlan:
		k8sResponse.Type = ResponseTypePlan
		k8sResponse.NeedsApproval = true
		k8sResponse.Summary = response.Message
		// Convert helm.HelmPlan to K8sPlan
		if response.Plan != nil {
			k8sResponse.Plan = convertHelmPlanToK8sPlan(response.Plan)
		}
	}

	return k8sResponse, nil
}

// convertHelmPlanToK8sPlan converts a helm plan to a K8s plan
func convertHelmPlanToK8sPlan(hp *helm.HelmPlan) *K8sPlan {
	plan := &K8sPlan{
		Version:  hp.Version,
		Question: hp.Summary,
		Summary:  hp.Summary,
		Notes:    hp.Notes,
		Bindings: make(map[string]string),
	}

	// Convert steps to helm commands
	for _, step := range hp.Steps {
		if step.Command == "helm" {
			action, release, chart, namespace := extractHelmArgsInfo(step.Args)
			cmd := HelmCmd{
				Action:    action,
				Release:   release,
				Chart:     chart,
				Namespace: namespace,
			}
			plan.HelmCmds = append(plan.HelmCmds, cmd)
		}
	}

	return plan
}

// extractHelmArgsInfo extracts action, release, chart and namespace from helm command args
func extractHelmArgsInfo(args []string) (action, release, chart, namespace string) {
	if len(args) == 0 {
		return
	}

	action = args[0]

	// Parse based on action
	switch action {
	case "install", "upgrade":
		// helm install <release> <chart> [-n namespace]
		if len(args) > 1 {
			release = args[1]
		}
		if len(args) > 2 {
			chart = args[2]
		}
	case "uninstall", "rollback", "status", "history":
		// helm uninstall <release> [-n namespace]
		if len(args) > 1 {
			release = args[1]
		}
	case "repo":
		// helm repo add/remove/update
		if len(args) > 2 {
			release = args[2] // Repo name is stored in release field for repo commands
		}
	}

	// Extract namespace from args
	for i, arg := range args {
		if (arg == "-n" || arg == "--namespace") && i+1 < len(args) {
			namespace = args[i+1]
			break
		}
	}

	return
}

// handleSREQuery delegates SRE queries to the sre sub-agent
func (a *Agent) handleSREQuery(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*K8sResponse, error) {
	if a.debug {
		fmt.Printf("[k8s-agent] delegating to sre sub-agent\n")
	}

	// Convert QueryOptions to sre.QueryOptions
	sreOpts := sre.QueryOptions{
		Namespace:     opts.Namespace,
		AllNamespaces: analysis.ClusterScope,
	}

	response, err := a.sre.HandleQuery(ctx, query, sreOpts)
	if err != nil {
		return nil, err
	}

	// Convert sre.Response to K8sResponse
	k8sResponse := &K8sResponse{
		NeedsApproval: false,
	}

	switch response.Type {
	case sre.ResponseTypeResult:
		k8sResponse.Type = ResponseTypeResult
		if str, ok := response.Data.(string); ok {
			k8sResponse.Result = str
		} else {
			k8sResponse.Result = response.Message
		}
	case sre.ResponseTypeReport:
		k8sResponse.Type = ResponseTypeResult
		k8sResponse.Result = response.Message
		// Include the diagnostic report as additional data
		if response.Report != nil {
			k8sResponse.Result = formatDiagnosticReport(response.Report)
		}
	case sre.ResponseTypePlan:
		k8sResponse.Type = ResponseTypePlan
		k8sResponse.NeedsApproval = true
		k8sResponse.Summary = response.Message
		// Convert sre.SREPlan to K8sPlan
		if response.Plan != nil {
			k8sResponse.Plan = convertSREPlanToK8sPlan(response.Plan)
		}
	}

	return k8sResponse, nil
}

// convertSREPlanToK8sPlan converts an SRE plan to a K8s plan
func convertSREPlanToK8sPlan(sp *sre.SREPlan) *K8sPlan {
	plan := &K8sPlan{
		Version:  sp.Version,
		Question: sp.Summary,
		Summary:  sp.Summary,
		Notes:    sp.Notes,
		Bindings: make(map[string]string),
	}

	// Convert remediation steps to kubectl commands
	for _, step := range sp.Steps {
		if step.Command == "kubectl" {
			cmd := KubectlCmd{
				Args:   step.Args,
				Reason: step.Description,
			}
			plan.KubectlCmds = append(plan.KubectlCmds, cmd)
		}
	}

	return plan
}

// formatDiagnosticReport formats a diagnostic report as a readable string
func formatDiagnosticReport(report *sre.DiagnosticReport) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Diagnostic Report: %s\n", report.Summary))
	sb.WriteString(fmt.Sprintf("Scope: %s\n", report.Scope))

	if report.ResourceType != "" {
		sb.WriteString(fmt.Sprintf("Resource: %s/%s\n", report.ResourceType, report.ResourceName))
	}
	if report.Namespace != "" {
		sb.WriteString(fmt.Sprintf("Namespace: %s\n", report.Namespace))
	}

	sb.WriteString(fmt.Sprintf("Generated: %s\n\n", report.GeneratedAt.Format("2006-01-02 15:04:05")))

	if len(report.Issues) > 0 {
		sb.WriteString("Issues Found:\n")
		for i, issue := range report.Issues {
			sb.WriteString(fmt.Sprintf("  %d. [%s] %s: %s\n", i+1, issue.Severity, issue.Category, issue.Message))
			if issue.Details != "" {
				sb.WriteString(fmt.Sprintf("     Details: %s\n", issue.Details))
			}
			if len(issue.Suggestions) > 0 {
				sb.WriteString("     Suggestions:\n")
				for _, s := range issue.Suggestions {
					sb.WriteString(fmt.Sprintf("       - %s\n", s))
				}
			}
		}
	} else {
		sb.WriteString("No issues found.\n")
	}

	if len(report.Events) > 0 {
		sb.WriteString("\nRecent Events:\n")
		for _, event := range report.Events {
			sb.WriteString(fmt.Sprintf("  [%s] %s: %s\n", event.Type, event.Reason, event.Message))
		}
	}

	if len(report.Remediation) > 0 {
		sb.WriteString("\nRemediation Steps:\n")
		for _, step := range report.Remediation {
			sb.WriteString(fmt.Sprintf("  %d. %s: %s\n", step.Order, step.Action, step.Description))
			if step.Command != "" {
				sb.WriteString(fmt.Sprintf("     Command: %s %s\n", step.Command, strings.Join(step.Args, " ")))
			}
		}
	}

	return sb.String()
}

// formatNetworkingData formats networking structured data for display
func formatNetworkingData(data interface{}) string {
	var sb strings.Builder

	switch v := data.(type) {
	case []networking.ServiceInfo:
		if len(v) == 0 {
			return "No services found"
		}
		sb.WriteString("Services:\n")
		sb.WriteString(fmt.Sprintf("%-30s %-15s %-15s %-20s %s\n", "NAME", "TYPE", "CLUSTER-IP", "EXTERNAL-IP", "PORTS"))
		for _, svc := range v {
			externalIP := svc.ExternalIP
			if externalIP == "" {
				externalIP = "<none>"
			}
			var ports []string
			for _, p := range svc.Ports {
				ports = append(ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
			}
			sb.WriteString(fmt.Sprintf("%-30s %-15s %-15s %-20s %s\n",
				svc.Name, svc.Type, svc.ClusterIP, externalIP, strings.Join(ports, ",")))
		}
	case []networking.IngressInfo:
		if len(v) == 0 {
			return "No ingresses found"
		}
		sb.WriteString("Ingresses:\n")
		sb.WriteString(fmt.Sprintf("%-30s %-30s %-15s %s\n", "NAME", "HOSTS", "ADDRESS", "PORTS"))
		for _, ing := range v {
			var hosts []string
			for _, rule := range ing.Rules {
				hosts = append(hosts, rule.Host)
			}
			sb.WriteString(fmt.Sprintf("%-30s %-30s %-15s %s\n",
				ing.Name, strings.Join(hosts, ","), ing.Address, "80, 443"))
		}
	case []networking.NetworkPolicyInfo:
		if len(v) == 0 {
			return "No network policies found"
		}
		sb.WriteString("Network Policies:\n")
		for _, np := range v {
			sb.WriteString(fmt.Sprintf("  %s (namespace: %s)\n", np.Name, np.Namespace))
		}
	case []networking.EndpointInfo:
		if len(v) == 0 {
			return "No endpoints found"
		}
		sb.WriteString("Endpoints:\n")
		for _, ep := range v {
			var addresses []string
			for _, subset := range ep.Subsets {
				for _, addr := range subset.Addresses {
					addresses = append(addresses, addr.IP)
				}
			}
			sb.WriteString(fmt.Sprintf("  %s: %v\n", ep.Name, addresses))
		}
	case map[string]interface{}:
		// Handle combined resources output
		if services, ok := v["services"].([]networking.ServiceInfo); ok && len(services) > 0 {
			sb.WriteString(formatNetworkingData(services))
			sb.WriteString("\n")
		}
		if ingresses, ok := v["ingresses"].([]networking.IngressInfo); ok && len(ingresses) > 0 {
			sb.WriteString(formatNetworkingData(ingresses))
			sb.WriteString("\n")
		}
		if policies, ok := v["networkPolicies"].([]networking.NetworkPolicyInfo); ok && len(policies) > 0 {
			sb.WriteString(formatNetworkingData(policies))
		}
	default:
		// Fallback to JSON representation
		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", data)
		}
		return string(jsonBytes)
	}

	return sb.String()
}

// formatStorageData formats storage structured data for display
func formatStorageData(data interface{}) string {
	var sb strings.Builder

	switch v := data.(type) {
	case []storage.PVInfo:
		if len(v) == 0 {
			return "No persistent volumes found"
		}
		sb.WriteString("Persistent Volumes:\n")
		sb.WriteString(fmt.Sprintf("%-30s %-10s %-15s %-10s %-20s %s\n", "NAME", "CAPACITY", "ACCESS MODES", "STATUS", "CLAIM", "STORAGECLASS"))
		for _, pv := range v {
			claim := pv.Claim
			if claim == "" {
				claim = "<none>"
			}
			sb.WriteString(fmt.Sprintf("%-30s %-10s %-15s %-10s %-20s %s\n",
				pv.Name, pv.Capacity, strings.Join(pv.AccessModes, ","), pv.Status, claim, pv.StorageClassName))
		}
	case []storage.PVCInfo:
		if len(v) == 0 {
			return "No persistent volume claims found"
		}
		sb.WriteString("Persistent Volume Claims:\n")
		sb.WriteString(fmt.Sprintf("%-30s %-15s %-10s %-15s %-15s %s\n", "NAME", "NAMESPACE", "STATUS", "VOLUME", "CAPACITY", "STORAGECLASS"))
		for _, pvc := range v {
			volume := pvc.Volume
			if volume == "" {
				volume = "<pending>"
			}
			capacity := pvc.Capacity
			if capacity == "" {
				capacity = pvc.RequestedStorage
			}
			sb.WriteString(fmt.Sprintf("%-30s %-15s %-10s %-15s %-15s %s\n",
				pvc.Name, pvc.Namespace, pvc.Status, volume, capacity, pvc.StorageClassName))
		}
	case []storage.StorageClassInfo:
		if len(v) == 0 {
			return "No storage classes found"
		}
		sb.WriteString("Storage Classes:\n")
		sb.WriteString(fmt.Sprintf("%-30s %-40s %-15s %-10s %s\n", "NAME", "PROVISIONER", "RECLAIM POLICY", "EXPAND", "DEFAULT"))
		for _, sc := range v {
			expand := "false"
			if sc.AllowVolumeExpansion {
				expand = "true"
			}
			isDefault := ""
			if sc.IsDefault {
				isDefault = "(default)"
			}
			sb.WriteString(fmt.Sprintf("%-30s %-40s %-15s %-10s %s\n",
				sc.Name, sc.Provisioner, sc.ReclaimPolicy, expand, isDefault))
		}
	case []storage.ConfigMapInfo:
		if len(v) == 0 {
			return "No configmaps found"
		}
		sb.WriteString("ConfigMaps:\n")
		sb.WriteString(fmt.Sprintf("%-40s %-15s %-10s %s\n", "NAME", "NAMESPACE", "DATA", "AGE"))
		for _, cm := range v {
			sb.WriteString(fmt.Sprintf("%-40s %-15s %-10d %s\n",
				cm.Name, cm.Namespace, cm.DataCount, cm.Age))
		}
	case []storage.SecretInfo:
		if len(v) == 0 {
			return "No secrets found"
		}
		sb.WriteString("Secrets:\n")
		sb.WriteString(fmt.Sprintf("%-40s %-15s %-25s %-10s %s\n", "NAME", "NAMESPACE", "TYPE", "DATA", "AGE"))
		for _, secret := range v {
			sb.WriteString(fmt.Sprintf("%-40s %-15s %-25s %-10d %s\n",
				secret.Name, secret.Namespace, secret.Type, secret.DataCount, secret.Age))
		}
	case map[string]interface{}:
		// Handle combined resources output
		if pvs, ok := v["persistentVolumes"].([]storage.PVInfo); ok && len(pvs) > 0 {
			sb.WriteString(formatStorageData(pvs))
			sb.WriteString("\n")
		}
		if pvcs, ok := v["persistentVolumeClaims"].([]storage.PVCInfo); ok && len(pvcs) > 0 {
			sb.WriteString(formatStorageData(pvcs))
			sb.WriteString("\n")
		}
		if scs, ok := v["storageClasses"].([]storage.StorageClassInfo); ok && len(scs) > 0 {
			sb.WriteString(formatStorageData(scs))
			sb.WriteString("\n")
		}
		if cms, ok := v["configMaps"].([]storage.ConfigMapInfo); ok && len(cms) > 0 {
			sb.WriteString(formatStorageData(cms))
			sb.WriteString("\n")
		}
		if secrets, ok := v["secrets"].([]storage.SecretInfo); ok && len(secrets) > 0 {
			sb.WriteString(formatStorageData(secrets))
		}
	default:
		// Fallback to JSON representation
		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", data)
		}
		return string(jsonBytes)
	}

	return sb.String()
}

// formatHelmData formats helm structured data for display
func formatHelmData(data interface{}) string {
	var sb strings.Builder

	switch v := data.(type) {
	case []helm.RepoInfo:
		if len(v) == 0 {
			return "No repositories configured"
		}
		sb.WriteString("Helm Repositories:\n")
		sb.WriteString(fmt.Sprintf("%-20s %s\n", "NAME", "URL"))
		for _, repo := range v {
			sb.WriteString(fmt.Sprintf("%-20s %s\n", repo.Name, repo.URL))
		}
	case []helm.ReleaseInfo:
		if len(v) == 0 {
			return "No releases found"
		}
		sb.WriteString("Helm Releases:\n")
		sb.WriteString(fmt.Sprintf("%-20s %-15s %-10s %-10s %-25s %s\n", "NAME", "NAMESPACE", "REVISION", "STATUS", "CHART", "APP VERSION"))
		for _, rel := range v {
			sb.WriteString(fmt.Sprintf("%-20s %-15s %-10d %-10s %-25s %s\n",
				rel.Name, rel.Namespace, rel.Revision, rel.Status, rel.Chart, rel.AppVersion))
		}
	case []helm.ChartInfo:
		if len(v) == 0 {
			return "No charts found"
		}
		sb.WriteString("Helm Charts:\n")
		sb.WriteString(fmt.Sprintf("%-30s %-15s %-15s %s\n", "NAME", "VERSION", "APP VERSION", "DESCRIPTION"))
		for _, chart := range v {
			desc := chart.Description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}
			sb.WriteString(fmt.Sprintf("%-30s %-15s %-15s %s\n",
				chart.Name, chart.Version, chart.AppVersion, desc))
		}
	case []helm.ReleaseHistoryEntry:
		if len(v) == 0 {
			return "No history found"
		}
		sb.WriteString("Release History:\n")
		sb.WriteString(fmt.Sprintf("%-10s %-10s %-25s %-15s %s\n", "REVISION", "STATUS", "CHART", "APP VERSION", "DESCRIPTION"))
		for _, entry := range v {
			sb.WriteString(fmt.Sprintf("%-10d %-10s %-25s %-15s %s\n",
				entry.Revision, entry.Status, entry.Chart, entry.AppVersion, entry.Description))
		}
	default:
		// Fallback to JSON representation
		jsonBytes, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", data)
		}
		return string(jsonBytes)
	}

	return sb.String()
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
