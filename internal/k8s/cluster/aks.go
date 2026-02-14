package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AKSProvider manages Azure Kubernetes Service clusters
type AKSProvider struct {
	subscriptionID string
	resourceGroup  string
	region         string
	debug          bool
}

// AKSProviderOptions contains options for creating an AKS provider
type AKSProviderOptions struct {
	SubscriptionID string
	ResourceGroup  string
	Region         string
	Debug          bool
}

// NewAKSProvider creates a new AKS cluster provider
func NewAKSProvider(opts AKSProviderOptions) *AKSProvider {
	return &AKSProvider{
		subscriptionID: opts.SubscriptionID,
		resourceGroup:  opts.ResourceGroup,
		region:         opts.Region,
		debug:          opts.Debug,
	}
}

// Type returns the cluster type
func (p *AKSProvider) Type() ClusterType {
	return ClusterTypeAKS
}

// Create provisions a new AKS cluster
func (p *AKSProvider) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
	if opts.Name == "" {
		return nil, &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	region := opts.Region
	if region == "" {
		region = p.region
	}
	if region == "" {
		return nil, &ErrInvalidConfiguration{Message: "region is required"}
	}

	resourceGroup := opts.AzureResourceGroup
	if resourceGroup == "" {
		resourceGroup = p.resourceGroup
	}
	if resourceGroup == "" {
		return nil, &ErrInvalidConfiguration{Message: "Azure resource group is required"}
	}

	subscription := opts.AzureSubscription
	if subscription == "" {
		subscription = p.subscriptionID
	}

	// Check if cluster already exists
	existing, _ := p.GetCluster(ctx, opts.Name)
	if existing != nil {
		return nil, &ErrClusterExists{ClusterName: opts.Name}
	}

	args := []string{
		"aks", "create",
		"--name", opts.Name,
		"--resource-group", resourceGroup,
		"--location", region,
		"--generate-ssh-keys",
	}

	// Node configuration
	nodeCount := opts.WorkerCount
	if nodeCount <= 0 {
		nodeCount = 1
	}
	args = append(args, "--node-count", fmt.Sprintf("%d", nodeCount))

	if opts.WorkerType != "" {
		args = append(args, "--node-vm-size", opts.WorkerType)
	}

	if opts.KubernetesVersion != "" {
		args = append(args, "--kubernetes-version", opts.KubernetesVersion)
	}

	// Network configuration
	if opts.AzureVNetName != "" && opts.AzureSubnetName != "" {
		// Build the subnet ID for an existing VNet/subnet
		subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s/subnets/%s",
			subscription, resourceGroup, opts.AzureVNetName, opts.AzureSubnetName)
		args = append(args, "--vnet-subnet-id", subnetID)
	}

	// Enable cluster autoscaler if min/max specified
	if opts.WorkerMinCount > 0 && opts.WorkerMaxCount > 0 {
		args = append(args, "--enable-cluster-autoscaler")
		args = append(args, "--min-count", fmt.Sprintf("%d", opts.WorkerMinCount))
		args = append(args, "--max-count", fmt.Sprintf("%d", opts.WorkerMaxCount))
	}

	// Tags
	if len(opts.Tags) > 0 {
		var tags []string
		for k, v := range opts.Tags {
			tags = append(tags, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--tags", strings.Join(tags, " "))
	}

	if p.debug {
		fmt.Printf("[aks] creating cluster: az %s\n", strings.Join(args, " "))
	}

	_, err := p.runAzureCLI(ctx, subscription, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create AKS cluster: %w", err)
	}

	// Wait for cluster to become running
	if err := p.waitForClusterRunning(ctx, opts.Name, resourceGroup, subscription, opts.CreateTimeout); err != nil {
		return nil, err
	}

	return p.GetCluster(ctx, opts.Name)
}

// Delete removes an AKS cluster
func (p *AKSProvider) Delete(ctx context.Context, clusterName string) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return &ErrInvalidConfiguration{Message: "resource group is required for delete"}
	}

	args := []string{
		"aks", "delete",
		"--name", clusterName,
		"--resource-group", resourceGroup,
		"--yes",
		"--no-wait",
	}

	if p.debug {
		fmt.Printf("[aks] deleting cluster: az %s\n", strings.Join(args, " "))
	}

	_, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "ResourceNotFound") {
			return &ErrClusterNotFound{ClusterName: clusterName}
		}
		return fmt.Errorf("failed to delete AKS cluster: %w", err)
	}

	return nil
}

// Scale adjusts the node count in an AKS cluster
func (p *AKSProvider) Scale(ctx context.Context, clusterName string, opts ScaleOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return &ErrInvalidConfiguration{Message: "resource group is required for scale"}
	}

	var args []string

	if opts.NodeGroupName != "" {
		// Scale specific node pool
		args = []string{
			"aks", "nodepool", "scale",
			"--name", opts.NodeGroupName,
			"--cluster-name", clusterName,
			"--resource-group", resourceGroup,
			"--node-count", fmt.Sprintf("%d", opts.DesiredCount),
		}
	} else {
		// Scale the default node pool (agentpool)
		args = []string{
			"aks", "nodepool", "scale",
			"--name", "agentpool",
			"--cluster-name", clusterName,
			"--resource-group", resourceGroup,
			"--node-count", fmt.Sprintf("%d", opts.DesiredCount),
		}
	}

	if p.debug {
		fmt.Printf("[aks] scaling cluster: az %s\n", strings.Join(args, " "))
	}

	_, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	return err
}

// GetKubeconfig retrieves and updates kubeconfig for the cluster
func (p *AKSProvider) GetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	if clusterName == "" {
		return "", &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return "", &ErrInvalidConfiguration{Message: "resource group is required for kubeconfig"}
	}

	// Default kubeconfig path
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	kubeconfigPath := filepath.Join(home, ".kube", "config")

	// Check for custom KUBECONFIG
	if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		paths := strings.Split(envPath, string(os.PathListSeparator))
		if len(paths) > 0 {
			kubeconfigPath = paths[0]
		}
	}

	args := []string{
		"aks", "get-credentials",
		"--name", clusterName,
		"--resource-group", resourceGroup,
		"--overwrite-existing",
	}

	if p.debug {
		fmt.Printf("[aks] updating kubeconfig: az %s\n", strings.Join(args, " "))
	}

	_, err = p.runAzureCLI(ctx, p.subscriptionID, args...)
	if err != nil {
		return "", fmt.Errorf("failed to update kubeconfig: %w", err)
	}

	return kubeconfigPath, nil
}

// Health checks cluster health
func (p *AKSProvider) Health(ctx context.Context, clusterName string) (*HealthStatus, error) {
	status := &HealthStatus{
		Components:  make(map[string]string),
		NodeStatus:  make(map[string]string),
		LastChecked: time.Now(),
	}

	// Get cluster status from Azure
	cluster, err := p.describeCluster(ctx, clusterName)
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to describe cluster: %v", err)
		return status, nil
	}

	clusterStatus := cluster.ProvisioningState
	status.Components["cluster"] = clusterStatus
	status.Components["powerState"] = cluster.PowerState.Code

	// Check if cluster is running
	if clusterStatus != "Succeeded" {
		status.Healthy = false
		status.Message = fmt.Sprintf("cluster provisioning state is %s", clusterStatus)
		return status, nil
	}

	if cluster.PowerState.Code != "Running" {
		status.Healthy = false
		status.Message = fmt.Sprintf("cluster power state is %s", cluster.PowerState.Code)
		return status, nil
	}

	// Get node pool statuses
	nodePools, err := p.listNodePools(ctx, clusterName)
	if err == nil {
		for _, np := range nodePools {
			status.Components[fmt.Sprintf("nodepool-%s", np.Name)] = np.ProvisioningState
		}
	}

	// Update kubeconfig and check node status via kubectl
	_, err = p.GetKubeconfig(ctx, clusterName)
	if err == nil {
		nodes, err := p.getNodesViaKubectl(ctx)
		if err == nil {
			readyNodes := 0
			for _, node := range nodes {
				status.NodeStatus[node.Name] = node.Status
				if node.Status == "Ready" {
					readyNodes++
				}
			}

			if readyNodes == len(nodes) && len(nodes) > 0 {
				status.Healthy = true
				status.Message = fmt.Sprintf("cluster Running, %d/%d nodes ready", readyNodes, len(nodes))
			} else {
				status.Healthy = false
				status.Message = fmt.Sprintf("cluster Running, but only %d/%d nodes ready", readyNodes, len(nodes))
			}
		} else {
			status.Healthy = true
			status.Message = fmt.Sprintf("cluster Running, unable to check node status: %v", err)
		}
	} else {
		status.Healthy = true
		status.Message = "cluster Running, kubeconfig update failed"
	}

	return status, nil
}

// ListClusters returns all AKS clusters in the subscription/resource group
func (p *AKSProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	var args []string
	if p.resourceGroup != "" {
		args = []string{"aks", "list", "--resource-group", p.resourceGroup, "--output", "json"}
	} else {
		args = []string{"aks", "list", "--output", "json"}
	}

	output, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	var aksClusters []aksClusterInfo
	if err := json.Unmarshal([]byte(output), &aksClusters); err != nil {
		return nil, fmt.Errorf("failed to parse cluster list: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(aksClusters))
	for _, c := range aksClusters {
		info := ClusterInfo{
			Name:              c.Name,
			Type:              ClusterTypeAKS,
			Status:            c.ProvisioningState,
			KubernetesVersion: c.KubernetesVersion,
			Endpoint:          c.Fqdn,
			Region:            c.Location,
		}

		// Add worker nodes from agent pools
		for _, ap := range c.AgentPoolProfiles {
			for i := 0; i < ap.Count; i++ {
				info.WorkerNodes = append(info.WorkerNodes, NodeInfo{
					Name:   fmt.Sprintf("%s-node-%d", ap.Name, i),
					Role:   "worker",
					Status: ap.ProvisioningState,
				})
			}
		}

		clusters = append(clusters, info)
	}

	return clusters, nil
}

// GetCluster returns information about a specific cluster
func (p *AKSProvider) GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	cluster, err := p.describeCluster(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	info := &ClusterInfo{
		Name:              cluster.Name,
		Type:              ClusterTypeAKS,
		Status:            cluster.ProvisioningState,
		KubernetesVersion: cluster.KubernetesVersion,
		Endpoint:          cluster.Fqdn,
		Region:            cluster.Location,
	}

	// Add worker nodes from agent pools
	for _, ap := range cluster.AgentPoolProfiles {
		for i := 0; i < ap.Count; i++ {
			info.WorkerNodes = append(info.WorkerNodes, NodeInfo{
				Name:   fmt.Sprintf("%s-node-%d", ap.Name, i),
				Role:   "worker",
				Status: ap.ProvisioningState,
			})
		}
	}

	return info, nil
}

// AKS-specific node pool operations

// AKSNodePoolInfo contains information about an AKS node pool
type AKSNodePoolInfo struct {
	Name              string `json:"name"`
	ProvisioningState string `json:"provisioningState"`
	Count             int    `json:"count"`
	VMSize            string `json:"vmSize"`
	OsDiskSizeGB      int    `json:"osDiskSizeGB"`
	EnableAutoScaling bool   `json:"enableAutoScaling"`
	MinCount          int    `json:"minCount,omitempty"`
	MaxCount          int    `json:"maxCount,omitempty"`
	Mode              string `json:"mode"`
}

// ListNodePools returns all node pools for a cluster
func (p *AKSProvider) ListNodePools(ctx context.Context, clusterName string) ([]AKSNodePoolInfo, error) {
	return p.listNodePools(ctx, clusterName)
}

// CreateNodePool creates a new node pool for an AKS cluster
func (p *AKSProvider) CreateNodePool(ctx context.Context, clusterName string, opts NodeGroupOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}
	if opts.Name == "" {
		return &ErrInvalidConfiguration{Message: "node pool name is required"}
	}

	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return &ErrInvalidConfiguration{Message: "resource group is required"}
	}

	args := []string{
		"aks", "nodepool", "add",
		"--name", opts.Name,
		"--cluster-name", clusterName,
		"--resource-group", resourceGroup,
	}

	if opts.DesiredSize > 0 {
		args = append(args, "--node-count", fmt.Sprintf("%d", opts.DesiredSize))
	}

	if opts.InstanceType != "" {
		args = append(args, "--node-vm-size", opts.InstanceType)
	}

	if opts.DiskSize > 0 {
		args = append(args, "--node-osdisk-size", fmt.Sprintf("%d", opts.DiskSize))
	}

	// Labels
	if len(opts.Labels) > 0 {
		var labels []string
		for k, v := range opts.Labels {
			labels = append(labels, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--labels", strings.Join(labels, " "))
	}

	if p.debug {
		fmt.Printf("[aks] creating node pool: az %s\n", strings.Join(args, " "))
	}

	_, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	if err != nil {
		return fmt.Errorf("failed to create node pool: %w", err)
	}

	// Wait for node pool to become running
	return p.waitForNodePoolRunning(ctx, clusterName, opts.Name)
}

// DeleteNodePool deletes a node pool from a cluster
func (p *AKSProvider) DeleteNodePool(ctx context.Context, clusterName, nodePoolName string) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}
	if nodePoolName == "" {
		return &ErrInvalidConfiguration{Message: "node pool name is required"}
	}

	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return &ErrInvalidConfiguration{Message: "resource group is required"}
	}

	args := []string{
		"aks", "nodepool", "delete",
		"--name", nodePoolName,
		"--cluster-name", clusterName,
		"--resource-group", resourceGroup,
		"--yes",
	}

	if p.debug {
		fmt.Printf("[aks] deleting node pool: az %s\n", strings.Join(args, " "))
	}

	_, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	return err
}

// Internal types for Azure responses

type aksClusterInfo struct {
	Name              string `json:"name"`
	ProvisioningState string `json:"provisioningState"`
	KubernetesVersion string `json:"kubernetesVersion"`
	Fqdn              string `json:"fqdn"`
	Location          string `json:"location"`
	PowerState        struct {
		Code string `json:"code"`
	} `json:"powerState"`
	AgentPoolProfiles []struct {
		Name              string `json:"name"`
		Count             int    `json:"count"`
		VMSize            string `json:"vmSize"`
		ProvisioningState string `json:"provisioningState"`
	} `json:"agentPoolProfiles"`
}

type aksNodePoolInfo struct {
	Name              string `json:"name"`
	ProvisioningState string `json:"provisioningState"`
	Count             int    `json:"count"`
	VMSize            string `json:"vmSize"`
	OsDiskSizeGB      int    `json:"osDiskSizeGB"`
	EnableAutoScaling bool   `json:"enableAutoScaling"`
	MinCount          int    `json:"minCount,omitempty"`
	MaxCount          int    `json:"maxCount,omitempty"`
	Mode              string `json:"mode"`
}

// Internal methods

func (p *AKSProvider) describeCluster(ctx context.Context, clusterName string) (*aksClusterInfo, error) {
	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return nil, &ErrInvalidConfiguration{Message: "resource group is required"}
	}

	args := []string{
		"aks", "show",
		"--name", clusterName,
		"--resource-group", resourceGroup,
		"--output", "json",
	}

	output, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "ResourceNotFound") {
			return nil, &ErrClusterNotFound{ClusterName: clusterName}
		}
		return nil, err
	}

	var cluster aksClusterInfo
	if err := json.Unmarshal([]byte(output), &cluster); err != nil {
		return nil, fmt.Errorf("failed to parse cluster info: %w", err)
	}

	return &cluster, nil
}

func (p *AKSProvider) listNodePools(ctx context.Context, clusterName string) ([]AKSNodePoolInfo, error) {
	resourceGroup := p.resourceGroup
	if resourceGroup == "" {
		return nil, &ErrInvalidConfiguration{Message: "resource group is required"}
	}

	args := []string{
		"aks", "nodepool", "list",
		"--cluster-name", clusterName,
		"--resource-group", resourceGroup,
		"--output", "json",
	}

	output, err := p.runAzureCLI(ctx, p.subscriptionID, args...)
	if err != nil {
		return nil, err
	}

	var nodePools []AKSNodePoolInfo
	if err := json.Unmarshal([]byte(output), &nodePools); err != nil {
		return nil, err
	}

	return nodePools, nil
}

func (p *AKSProvider) waitForClusterRunning(ctx context.Context, clusterName, resourceGroup, subscription string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cluster, err := p.describeCluster(ctx, clusterName)
		if err != nil {
			time.Sleep(30 * time.Second)
			continue
		}

		if p.debug {
			fmt.Printf("[aks] cluster %s provisioning state: %s, power state: %s\n",
				clusterName, cluster.ProvisioningState, cluster.PowerState.Code)
		}

		if cluster.ProvisioningState == "Succeeded" && cluster.PowerState.Code == "Running" {
			return nil
		}
		if cluster.ProvisioningState == "Failed" {
			return fmt.Errorf("cluster creation failed with state: %s", cluster.ProvisioningState)
		}

		time.Sleep(30 * time.Second)
	}

	return fmt.Errorf("timeout waiting for cluster to become running")
}

func (p *AKSProvider) waitForNodePoolRunning(ctx context.Context, clusterName, nodePoolName string) error {
	timeout := 15 * time.Minute
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		nodePools, err := p.listNodePools(ctx, clusterName)
		if err != nil {
			time.Sleep(30 * time.Second)
			continue
		}

		for _, np := range nodePools {
			if np.Name == nodePoolName {
				if p.debug {
					fmt.Printf("[aks] node pool %s provisioning state: %s\n", nodePoolName, np.ProvisioningState)
				}

				if np.ProvisioningState == "Succeeded" {
					return nil
				}
				if np.ProvisioningState == "Failed" {
					return fmt.Errorf("node pool creation failed")
				}
				break
			}
		}

		time.Sleep(30 * time.Second)
	}

	return fmt.Errorf("timeout waiting for node pool to become running")
}

func (p *AKSProvider) getNodesViaKubectl(ctx context.Context) ([]NodeInfo, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", "json")
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl failed: %w, stderr: %s", err, stderr.String())
	}

	var nodeList struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &nodeList); err != nil {
		return nil, err
	}

	nodes := make([]NodeInfo, 0, len(nodeList.Items))
	for _, item := range nodeList.Items {
		node := NodeInfo{
			Name:   item.Metadata.Name,
			Labels: item.Metadata.Labels,
			Role:   "worker",
		}

		// Get addresses
		for _, addr := range item.Status.Addresses {
			switch addr.Type {
			case "InternalIP":
				node.InternalIP = addr.Address
			case "ExternalIP":
				node.ExternalIP = addr.Address
			}
		}

		// Get status
		for _, cond := range item.Status.Conditions {
			if cond.Type == "Ready" {
				if cond.Status == "True" {
					node.Status = "Ready"
				} else {
					node.Status = "NotReady"
				}
				break
			}
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

func (p *AKSProvider) runAzureCLI(ctx context.Context, subscription string, args ...string) (string, error) {
	if _, err := exec.LookPath("az"); err != nil {
		return "", fmt.Errorf("az not found in PATH (hint: install Azure CLI)")
	}

	// Add subscription flag if provided
	if subscription != "" {
		args = append(args, "--subscription", subscription)
	}

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt <= len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "az", args...)
		cmd.Env = os.Environ()

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			return stdout.String(), nil
		}

		lastErr = err
		lastStderr = strings.TrimSpace(stderr.String())

		if ctx.Err() != nil {
			break
		}

		if !p.isRetryableError(lastStderr) {
			break
		}

		if attempt < len(backoffs) {
			time.Sleep(backoffs[attempt])
		}
	}

	if lastErr == nil {
		return "", fmt.Errorf("az command failed")
	}

	return "", fmt.Errorf("az command failed: %w, stderr: %s%s", lastErr, lastStderr, p.errorHint(lastStderr))
}

func (p *AKSProvider) isRetryableError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "throttl") {
		return true
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") {
		return true
	}
	if strings.Contains(lower, "temporarily unavailable") || strings.Contains(lower, "internal error") {
		return true
	}
	if strings.Contains(lower, "service unavailable") {
		return true
	}
	return false
}

func (p *AKSProvider) errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "authorizationfailed") || strings.Contains(lower, "permission"):
		return " (hint: check Azure RBAC permissions or subscription access)"
	case strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "not found"):
		return " (hint: resource may not exist or check resource group/subscription)"
	case strings.Contains(lower, "invalidsubscriptionid"):
		return " (hint: run 'az account list' to verify subscription ID)"
	case strings.Contains(lower, "please run") && strings.Contains(lower, "az login"):
		return " (hint: run 'az login' to authenticate with Azure)"
	case strings.Contains(lower, "quota"):
		return " (hint: Azure quota exceeded, request increase or use different region)"
	case strings.Contains(lower, "resourcegroupnotfound"):
		return " (hint: resource group does not exist, create it first with 'az group create')"
	case strings.Contains(lower, "invalidresourcegroup"):
		return " (hint: invalid resource group name)"
	case strings.Contains(lower, "conflictingserveroperation") || strings.Contains(lower, "operationnotallowed"):
		return " (hint: another operation is in progress, wait and retry)"
	default:
		return ""
	}
}

// Note: NodeGroupOptions is defined in eks.go and shared across providers
