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

// GKEProvider manages Google Kubernetes Engine clusters
type GKEProvider struct {
	projectID string
	region    string
	debug     bool
}

// GKEProviderOptions contains options for creating a GKE provider
type GKEProviderOptions struct {
	ProjectID string
	Region    string
	Debug     bool
}

// NewGKEProvider creates a new GKE cluster provider
func NewGKEProvider(opts GKEProviderOptions) *GKEProvider {
	return &GKEProvider{
		projectID: opts.ProjectID,
		region:    opts.Region,
		debug:     opts.Debug,
	}
}

// Type returns the cluster type
func (p *GKEProvider) Type() ClusterType {
	return ClusterTypeGKE
}

// Create provisions a new GKE cluster
func (p *GKEProvider) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
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

	project := opts.GCPProject
	if project == "" {
		project = p.projectID
	}
	if project == "" {
		return nil, &ErrInvalidConfiguration{Message: "GCP project is required"}
	}

	// Check if cluster already exists
	existing, _ := p.GetCluster(ctx, opts.Name)
	if existing != nil {
		return nil, &ErrClusterExists{ClusterName: opts.Name}
	}

	args := []string{
		"container", "clusters", "create", opts.Name,
		"--region", region,
	}

	// Node configuration
	nodeCount := opts.WorkerCount
	if nodeCount <= 0 {
		nodeCount = 1
	}
	args = append(args, "--num-nodes", fmt.Sprintf("%d", nodeCount))

	if opts.WorkerType != "" {
		args = append(args, "--machine-type", opts.WorkerType)
	}

	if opts.KubernetesVersion != "" {
		args = append(args, "--cluster-version", opts.KubernetesVersion)
	}

	// Network configuration
	if opts.GCPNetwork != "" {
		args = append(args, "--network", opts.GCPNetwork)
	}
	if opts.GCPSubnetwork != "" {
		args = append(args, "--subnetwork", opts.GCPSubnetwork)
	}

	// Preemptible nodes for cost savings
	if opts.Preemptible {
		args = append(args, "--preemptible")
	}

	// Tags/Labels
	if len(opts.Tags) > 0 {
		var labels []string
		for k, v := range opts.Tags {
			labels = append(labels, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	if p.debug {
		fmt.Printf("[gke] creating cluster: gcloud %s --project %s\n", strings.Join(args, " "), project)
	}

	_, err := p.runGcloud(ctx, project, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GKE cluster: %w", err)
	}

	// Wait for cluster to become running
	if err := p.waitForClusterRunning(ctx, opts.Name, project, region, opts.CreateTimeout); err != nil {
		return nil, err
	}

	return p.GetCluster(ctx, opts.Name)
}

// Delete removes a GKE cluster
func (p *GKEProvider) Delete(ctx context.Context, clusterName string) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	region := p.region
	if region == "" {
		return &ErrInvalidConfiguration{Message: "region is required for delete"}
	}

	args := []string{
		"container", "clusters", "delete", clusterName,
		"--region", region,
		"--quiet",
	}

	if p.debug {
		fmt.Printf("[gke] deleting cluster: gcloud %s --project %s\n", strings.Join(args, " "), p.projectID)
	}

	_, err := p.runGcloud(ctx, p.projectID, args...)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NOT_FOUND") {
			return &ErrClusterNotFound{ClusterName: clusterName}
		}
		return fmt.Errorf("failed to delete GKE cluster: %w", err)
	}

	return nil
}

// Scale adjusts the node count in a GKE cluster
func (p *GKEProvider) Scale(ctx context.Context, clusterName string, opts ScaleOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	region := p.region
	if region == "" {
		return &ErrInvalidConfiguration{Message: "region is required for scale"}
	}

	var args []string

	if opts.NodeGroupName != "" {
		// Scale specific node pool
		args = []string{
			"container", "node-pools", "update", opts.NodeGroupName,
			"--cluster", clusterName,
			"--region", region,
			"--num-nodes", fmt.Sprintf("%d", opts.DesiredCount),
			"--quiet",
		}
	} else {
		// Resize cluster (affects default pool)
		args = []string{
			"container", "clusters", "resize", clusterName,
			"--region", region,
			"--num-nodes", fmt.Sprintf("%d", opts.DesiredCount),
			"--quiet",
		}
	}

	if p.debug {
		fmt.Printf("[gke] scaling cluster: gcloud %s --project %s\n", strings.Join(args, " "), p.projectID)
	}

	_, err := p.runGcloud(ctx, p.projectID, args...)
	return err
}

// GetKubeconfig retrieves and updates kubeconfig for the cluster
func (p *GKEProvider) GetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	if clusterName == "" {
		return "", &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	region := p.region
	if region == "" {
		return "", &ErrInvalidConfiguration{Message: "region is required for kubeconfig"}
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
		"container", "clusters", "get-credentials", clusterName,
		"--region", region,
	}

	if p.debug {
		fmt.Printf("[gke] updating kubeconfig: gcloud %s --project %s\n", strings.Join(args, " "), p.projectID)
	}

	_, err = p.runGcloud(ctx, p.projectID, args...)
	if err != nil {
		return "", fmt.Errorf("failed to update kubeconfig: %w", err)
	}

	return kubeconfigPath, nil
}

// Health checks cluster health
func (p *GKEProvider) Health(ctx context.Context, clusterName string) (*HealthStatus, error) {
	status := &HealthStatus{
		Components:  make(map[string]string),
		NodeStatus:  make(map[string]string),
		LastChecked: time.Now(),
	}

	// Get cluster status from GCP
	cluster, err := p.describeCluster(ctx, clusterName)
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to describe cluster: %v", err)
		return status, nil
	}

	clusterStatus := cluster.Status
	status.Components["cluster"] = clusterStatus

	// Check if cluster is running
	if clusterStatus != "RUNNING" {
		status.Healthy = false
		status.Message = fmt.Sprintf("cluster status is %s", clusterStatus)
		return status, nil
	}

	// Get node pool statuses
	nodePools, err := p.listNodePools(ctx, clusterName)
	if err == nil {
		for _, np := range nodePools {
			status.Components[fmt.Sprintf("nodepool-%s", np.Name)] = np.Status
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
				status.Message = fmt.Sprintf("cluster RUNNING, %d/%d nodes ready", readyNodes, len(nodes))
			} else {
				status.Healthy = false
				status.Message = fmt.Sprintf("cluster RUNNING, but only %d/%d nodes ready", readyNodes, len(nodes))
			}
		} else {
			status.Healthy = true
			status.Message = fmt.Sprintf("cluster RUNNING, unable to check node status: %v", err)
		}
	} else {
		status.Healthy = true
		status.Message = "cluster RUNNING, kubeconfig update failed"
	}

	return status, nil
}

// ListClusters returns all GKE clusters in the project
func (p *GKEProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	args := []string{"container", "clusters", "list", "--format=json"}

	output, err := p.runGcloud(ctx, p.projectID, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	var gkeClusters []gkeClusterInfo
	if err := json.Unmarshal([]byte(output), &gkeClusters); err != nil {
		return nil, fmt.Errorf("failed to parse cluster list: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(gkeClusters))
	for _, c := range gkeClusters {
		createdAt, _ := time.Parse(time.RFC3339, c.CreateTime)

		info := ClusterInfo{
			Name:              c.Name,
			Type:              ClusterTypeGKE,
			Status:            c.Status,
			KubernetesVersion: c.CurrentMasterVersion,
			Endpoint:          c.Endpoint,
			Region:            c.Location,
			CreatedAt:         createdAt,
		}

		// Add worker nodes from node pools
		for _, np := range c.NodePools {
			for i := 0; i < np.InitialNodeCount; i++ {
				info.WorkerNodes = append(info.WorkerNodes, NodeInfo{
					Name:   fmt.Sprintf("%s-node-%d", np.Name, i),
					Role:   "worker",
					Status: np.Status,
				})
			}
		}

		clusters = append(clusters, info)
	}

	return clusters, nil
}

// GetCluster returns information about a specific cluster
func (p *GKEProvider) GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	cluster, err := p.describeCluster(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	createdAt, _ := time.Parse(time.RFC3339, cluster.CreateTime)

	info := &ClusterInfo{
		Name:              cluster.Name,
		Type:              ClusterTypeGKE,
		Status:            cluster.Status,
		KubernetesVersion: cluster.CurrentMasterVersion,
		Endpoint:          cluster.Endpoint,
		Region:            cluster.Location,
		CreatedAt:         createdAt,
	}

	// Add worker nodes from node pools
	for _, np := range cluster.NodePools {
		for i := 0; i < np.InitialNodeCount; i++ {
			info.WorkerNodes = append(info.WorkerNodes, NodeInfo{
				Name:   fmt.Sprintf("%s-node-%d", np.Name, i),
				Role:   "worker",
				Status: np.Status,
			})
		}
	}

	return info, nil
}

// GKE-specific node pool operations

// GKENodePoolInfo contains information about a GKE node pool
type GKENodePoolInfo struct {
	Name             string `json:"name"`
	Status           string `json:"status"`
	InitialNodeCount int    `json:"initialNodeCount"`
	Config           struct {
		MachineType string `json:"machineType"`
		DiskSizeGb  int    `json:"diskSizeGb"`
	} `json:"config"`
	Autoscaling *struct {
		Enabled      bool `json:"enabled"`
		MinNodeCount int  `json:"minNodeCount"`
		MaxNodeCount int  `json:"maxNodeCount"`
	} `json:"autoscaling,omitempty"`
}

// ListNodePools returns all node pools for a cluster
func (p *GKEProvider) ListNodePools(ctx context.Context, clusterName string) ([]GKENodePoolInfo, error) {
	return p.listNodePools(ctx, clusterName)
}

// CreateNodePool creates a new node pool for a GKE cluster
func (p *GKEProvider) CreateNodePool(ctx context.Context, clusterName string, opts NodeGroupOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}
	if opts.Name == "" {
		return &ErrInvalidConfiguration{Message: "node pool name is required"}
	}

	region := p.region
	if region == "" {
		return &ErrInvalidConfiguration{Message: "region is required"}
	}

	args := []string{
		"container", "node-pools", "create", opts.Name,
		"--cluster", clusterName,
		"--region", region,
	}

	if opts.DesiredSize > 0 {
		args = append(args, "--num-nodes", fmt.Sprintf("%d", opts.DesiredSize))
	}

	if opts.InstanceType != "" {
		args = append(args, "--machine-type", opts.InstanceType)
	}

	if opts.DiskSize > 0 {
		args = append(args, "--disk-size", fmt.Sprintf("%d", opts.DiskSize))
	}

	// Labels
	if len(opts.Labels) > 0 {
		var labels []string
		for k, v := range opts.Labels {
			labels = append(labels, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--node-labels", strings.Join(labels, ","))
	}

	if p.debug {
		fmt.Printf("[gke] creating node pool: gcloud %s --project %s\n", strings.Join(args, " "), p.projectID)
	}

	_, err := p.runGcloud(ctx, p.projectID, args...)
	if err != nil {
		return fmt.Errorf("failed to create node pool: %w", err)
	}

	// Wait for node pool to become running
	return p.waitForNodePoolRunning(ctx, clusterName, opts.Name)
}

// DeleteNodePool deletes a node pool from a cluster
func (p *GKEProvider) DeleteNodePool(ctx context.Context, clusterName, nodePoolName string) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}
	if nodePoolName == "" {
		return &ErrInvalidConfiguration{Message: "node pool name is required"}
	}

	region := p.region
	if region == "" {
		return &ErrInvalidConfiguration{Message: "region is required"}
	}

	args := []string{
		"container", "node-pools", "delete", nodePoolName,
		"--cluster", clusterName,
		"--region", region,
		"--quiet",
	}

	if p.debug {
		fmt.Printf("[gke] deleting node pool: gcloud %s --project %s\n", strings.Join(args, " "), p.projectID)
	}

	_, err := p.runGcloud(ctx, p.projectID, args...)
	return err
}

// Internal types for GCP responses

type gkeClusterInfo struct {
	Name                 string            `json:"name"`
	Status               string            `json:"status"`
	CurrentMasterVersion string            `json:"currentMasterVersion"`
	Endpoint             string            `json:"endpoint"`
	Location             string            `json:"location"`
	Network              string            `json:"network"`
	Subnetwork           string            `json:"subnetwork"`
	CreateTime           string            `json:"createTime"`
	NodePools            []gkeNodePoolInfo `json:"nodePools"`
}

type gkeNodePoolInfo struct {
	Name             string `json:"name"`
	Status           string `json:"status"`
	InitialNodeCount int    `json:"initialNodeCount"`
	Config           struct {
		MachineType string `json:"machineType"`
		DiskSizeGb  int    `json:"diskSizeGb"`
	} `json:"config"`
	Autoscaling *struct {
		Enabled      bool `json:"enabled"`
		MinNodeCount int  `json:"minNodeCount"`
		MaxNodeCount int  `json:"maxNodeCount"`
	} `json:"autoscaling,omitempty"`
}

// Internal methods

func (p *GKEProvider) describeCluster(ctx context.Context, clusterName string) (*gkeClusterInfo, error) {
	region := p.region
	if region == "" {
		return nil, &ErrInvalidConfiguration{Message: "region is required"}
	}

	args := []string{
		"container", "clusters", "describe", clusterName,
		"--region", region,
		"--format=json",
	}

	output, err := p.runGcloud(ctx, p.projectID, args...)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NOT_FOUND") {
			return nil, &ErrClusterNotFound{ClusterName: clusterName}
		}
		return nil, err
	}

	var cluster gkeClusterInfo
	if err := json.Unmarshal([]byte(output), &cluster); err != nil {
		return nil, fmt.Errorf("failed to parse cluster info: %w", err)
	}

	return &cluster, nil
}

func (p *GKEProvider) listNodePools(ctx context.Context, clusterName string) ([]GKENodePoolInfo, error) {
	region := p.region
	if region == "" {
		return nil, &ErrInvalidConfiguration{Message: "region is required"}
	}

	args := []string{
		"container", "node-pools", "list",
		"--cluster", clusterName,
		"--region", region,
		"--format=json",
	}

	output, err := p.runGcloud(ctx, p.projectID, args...)
	if err != nil {
		return nil, err
	}

	var nodePools []GKENodePoolInfo
	if err := json.Unmarshal([]byte(output), &nodePools); err != nil {
		return nil, err
	}

	return nodePools, nil
}

func (p *GKEProvider) waitForClusterRunning(ctx context.Context, clusterName, project, region string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultClusterCreateTimeout
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cluster, err := p.describeCluster(ctx, clusterName)
		if err != nil {
			time.Sleep(DefaultPollInterval)
			continue
		}

		if p.debug {
			fmt.Printf("[gke] cluster %s status: %s\n", clusterName, cluster.Status)
		}

		if cluster.Status == "RUNNING" {
			return nil
		}
		if cluster.Status == "ERROR" || cluster.Status == "DEGRADED" {
			return fmt.Errorf("cluster creation failed with status: %s", cluster.Status)
		}

		time.Sleep(DefaultPollInterval)
	}

	return fmt.Errorf("timeout waiting for cluster to become running")
}

func (p *GKEProvider) waitForNodePoolRunning(ctx context.Context, clusterName, nodePoolName string) error {
	deadline := time.Now().Add(DefaultNodeGroupCreateTimeout)

	for time.Now().Before(deadline) {
		nodePools, err := p.listNodePools(ctx, clusterName)
		if err != nil {
			time.Sleep(DefaultPollInterval)
			continue
		}

		for _, np := range nodePools {
			if np.Name == nodePoolName {
				if p.debug {
					fmt.Printf("[gke] node pool %s status: %s\n", nodePoolName, np.Status)
				}

				if np.Status == "RUNNING" {
					return nil
				}
				if np.Status == "ERROR" {
					return fmt.Errorf("node pool creation failed")
				}
				break
			}
		}

		time.Sleep(DefaultPollInterval)
	}

	return fmt.Errorf("timeout waiting for node pool to become running")
}

func (p *GKEProvider) getNodesViaKubectl(ctx context.Context) ([]NodeInfo, error) {
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

func (p *GKEProvider) runGcloud(ctx context.Context, project string, args ...string) (string, error) {
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", fmt.Errorf("gcloud not found in PATH (hint: install Google Cloud SDK)")
	}

	// Add project flag
	args = append(args, "--project", project)

	backoffs := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1200 * time.Millisecond}
	var lastErr error
	var lastStderr string

	for attempt := 0; attempt <= len(backoffs); attempt++ {
		cmd := exec.CommandContext(ctx, "gcloud", args...)
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
		return "", fmt.Errorf("gcloud command failed")
	}

	return "", fmt.Errorf("gcloud command failed: %w, stderr: %s%s", lastErr, lastStderr, p.errorHint(lastStderr))
}

func (p *GKEProvider) isRetryableError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "resource_exhausted") {
		return true
	}
	if strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") {
		return true
	}
	if strings.Contains(lower, "temporarily unavailable") || strings.Contains(lower, "internal error") {
		return true
	}
	return false
}

func (p *GKEProvider) errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "permission") || strings.Contains(lower, "denied"):
		return " (hint: missing IAM permissions or project access)"
	case strings.Contains(lower, "not found") && strings.Contains(lower, "project"):
		return " (hint: project_id may be incorrect)"
	case strings.Contains(lower, "api") && strings.Contains(lower, "not enabled"):
		return " (hint: enable the Kubernetes Engine API: gcloud services enable container.googleapis.com)"
	case strings.Contains(lower, "login") || strings.Contains(lower, "auth"):
		return " (hint: run 'gcloud auth login' or set GOOGLE_APPLICATION_CREDENTIALS)"
	case strings.Contains(lower, "quota"):
		return " (hint: GCP quota exceeded, request increase or use different zone)"
	default:
		return ""
	}
}
