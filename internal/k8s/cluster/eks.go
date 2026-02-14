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

// EKSProvider manages AWS EKS clusters
type EKSProvider struct {
	awsProfile string
	region     string
	debug      bool
}

// EKSProviderOptions contains options for creating an EKS provider
type EKSProviderOptions struct {
	AWSProfile string
	Region     string
	Debug      bool
}

// NewEKSProvider creates a new EKS cluster provider
func NewEKSProvider(opts EKSProviderOptions) *EKSProvider {
	return &EKSProvider{
		awsProfile: opts.AWSProfile,
		region:     opts.Region,
		debug:      opts.Debug,
	}
}

// Type returns the cluster type
func (p *EKSProvider) Type() ClusterType {
	return ClusterTypeEKS
}

// Create provisions a new EKS cluster
func (p *EKSProvider) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
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

	profile := opts.AWSProfile
	if profile == "" {
		profile = p.awsProfile
	}

	// Check if cluster already exists
	existing, _ := p.GetCluster(ctx, opts.Name)
	if existing != nil {
		return nil, &ErrClusterExists{ClusterName: opts.Name}
	}

	// Determine creation method: eksctl or AWS CLI
	if p.hasEksctl() {
		return p.createWithEksctl(ctx, opts, profile, region)
	}

	return p.createWithAWSCLI(ctx, opts, profile, region)
}

// createWithEksctl creates a cluster using eksctl
func (p *EKSProvider) createWithEksctl(ctx context.Context, opts CreateOptions, profile, region string) (*ClusterInfo, error) {
	args := []string{
		"create", "cluster",
		"--name", opts.Name,
		"--region", region,
	}

	if profile != "" {
		args = append(args, "--profile", profile)
	}

	if opts.KubernetesVersion != "" {
		args = append(args, "--version", opts.KubernetesVersion)
	}

	// Node configuration
	nodeCount := opts.WorkerCount
	if nodeCount <= 0 {
		nodeCount = 2
	}
	args = append(args, "--nodes", fmt.Sprintf("%d", nodeCount))

	if opts.WorkerMinCount > 0 {
		args = append(args, "--nodes-min", fmt.Sprintf("%d", opts.WorkerMinCount))
	}

	if opts.WorkerMaxCount > 0 {
		args = append(args, "--nodes-max", fmt.Sprintf("%d", opts.WorkerMaxCount))
	}

	if opts.WorkerType != "" {
		args = append(args, "--node-type", opts.WorkerType)
	}

	// VPC configuration
	if opts.VPCId != "" {
		args = append(args, "--vpc-private-subnets", strings.Join(opts.SubnetIds, ","))
	}

	// Tags
	if len(opts.Tags) > 0 {
		var tags []string
		for k, v := range opts.Tags {
			tags = append(tags, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--tags", strings.Join(tags, ","))
	}

	if p.debug {
		fmt.Printf("[eksctl] creating cluster: eksctl %s\n", strings.Join(args, " "))
	}

	output, err := p.runEksctl(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create EKS cluster: %w", err)
	}

	if p.debug {
		fmt.Printf("[eksctl] output: %s\n", output)
	}

	// Retrieve cluster info after creation
	return p.GetCluster(ctx, opts.Name)
}

// createWithAWSCLI creates a cluster using AWS CLI directly
func (p *EKSProvider) createWithAWSCLI(ctx context.Context, opts CreateOptions, profile, region string) (*ClusterInfo, error) {
	// Step 1: Create the EKS cluster
	args := []string{
		"eks", "create-cluster",
		"--name", opts.Name,
		"--region", region,
	}

	if profile != "" {
		args = append(args, "--profile", profile)
	}

	if opts.KubernetesVersion != "" {
		args = append(args, "--kubernetes-version", opts.KubernetesVersion)
	}

	// Role ARN is required for AWS CLI method
	if roleARN, ok := opts.Tags["eks-role-arn"]; ok {
		args = append(args, "--role-arn", roleARN)
	} else {
		return nil, &ErrInvalidConfiguration{Message: "eks-role-arn tag is required when using AWS CLI method; consider installing eksctl for easier cluster creation"}
	}

	// VPC configuration
	if len(opts.SubnetIds) > 0 {
		args = append(args, "--resources-vpc-config", fmt.Sprintf("subnetIds=%s", strings.Join(opts.SubnetIds, ",")))
	} else {
		return nil, &ErrInvalidConfiguration{Message: "subnet IDs are required"}
	}

	if p.debug {
		fmt.Printf("[aws] creating cluster: aws %s\n", strings.Join(args, " "))
	}

	_, err := p.runAWS(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create EKS cluster: %w", err)
	}

	// Wait for cluster to become active
	if err := p.waitForClusterActive(ctx, opts.Name, profile, region, opts.CreateTimeout); err != nil {
		return nil, err
	}

	return p.GetCluster(ctx, opts.Name)
}

// Delete removes an EKS cluster
func (p *EKSProvider) Delete(ctx context.Context, clusterName string) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	// Try eksctl first, fall back to AWS CLI
	if p.hasEksctl() {
		return p.deleteWithEksctl(ctx, clusterName)
	}

	return p.deleteWithAWSCLI(ctx, clusterName)
}

// deleteWithEksctl deletes a cluster using eksctl
func (p *EKSProvider) deleteWithEksctl(ctx context.Context, clusterName string) error {
	args := []string{"delete", "cluster", "--name", clusterName}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	args = append(args, "--wait")

	if p.debug {
		fmt.Printf("[eksctl] deleting cluster: eksctl %s\n", strings.Join(args, " "))
	}

	_, err := p.runEksctl(ctx, args...)
	return err
}

// deleteWithAWSCLI deletes a cluster using AWS CLI
func (p *EKSProvider) deleteWithAWSCLI(ctx context.Context, clusterName string) error {
	// First, delete all node groups
	nodeGroups, err := p.listNodeGroups(ctx, clusterName)
	if err == nil {
		for _, ng := range nodeGroups {
			if err := p.deleteNodeGroup(ctx, clusterName, ng); err != nil {
				return fmt.Errorf("failed to delete node group %s: %w", ng, err)
			}
		}
	}

	// Then delete the cluster
	args := []string{"eks", "delete-cluster", "--name", clusterName}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	if p.debug {
		fmt.Printf("[aws] deleting cluster: aws %s\n", strings.Join(args, " "))
	}

	_, err = p.runAWS(ctx, args...)
	return err
}

// Scale adjusts the node count in a node group
func (p *EKSProvider) Scale(ctx context.Context, clusterName string, opts ScaleOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	nodeGroupName := opts.NodeGroupName
	if nodeGroupName == "" {
		// Get the first node group if not specified
		nodeGroups, err := p.listNodeGroups(ctx, clusterName)
		if err != nil {
			return fmt.Errorf("failed to list node groups: %w", err)
		}
		if len(nodeGroups) == 0 {
			return fmt.Errorf("no node groups found in cluster %s", clusterName)
		}
		nodeGroupName = nodeGroups[0]
	}

	// Use eksctl if available
	if p.hasEksctl() {
		return p.scaleWithEksctl(ctx, clusterName, nodeGroupName, opts)
	}

	return p.scaleWithAWSCLI(ctx, clusterName, nodeGroupName, opts)
}

// scaleWithEksctl scales a node group using eksctl
func (p *EKSProvider) scaleWithEksctl(ctx context.Context, clusterName, nodeGroupName string, opts ScaleOptions) error {
	args := []string{
		"scale", "nodegroup",
		"--cluster", clusterName,
		"--name", nodeGroupName,
		"--nodes", fmt.Sprintf("%d", opts.DesiredCount),
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	if opts.MinCount > 0 {
		args = append(args, "--nodes-min", fmt.Sprintf("%d", opts.MinCount))
	}
	if opts.MaxCount > 0 {
		args = append(args, "--nodes-max", fmt.Sprintf("%d", opts.MaxCount))
	}

	if p.debug {
		fmt.Printf("[eksctl] scaling node group: eksctl %s\n", strings.Join(args, " "))
	}

	_, err := p.runEksctl(ctx, args...)
	return err
}

// scaleWithAWSCLI scales a node group using AWS CLI
func (p *EKSProvider) scaleWithAWSCLI(ctx context.Context, clusterName, nodeGroupName string, opts ScaleOptions) error {
	scalingConfig := fmt.Sprintf("desiredSize=%d", opts.DesiredCount)
	if opts.MinCount > 0 {
		scalingConfig += fmt.Sprintf(",minSize=%d", opts.MinCount)
	}
	if opts.MaxCount > 0 {
		scalingConfig += fmt.Sprintf(",maxSize=%d", opts.MaxCount)
	}

	args := []string{
		"eks", "update-nodegroup-config",
		"--cluster-name", clusterName,
		"--nodegroup-name", nodeGroupName,
		"--scaling-config", scalingConfig,
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	if p.debug {
		fmt.Printf("[aws] scaling node group: aws %s\n", strings.Join(args, " "))
	}

	_, err := p.runAWS(ctx, args...)
	return err
}

// GetKubeconfig retrieves and updates kubeconfig for the cluster
func (p *EKSProvider) GetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	if clusterName == "" {
		return "", &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	// Default kubeconfig path
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	kubeconfigPath := filepath.Join(home, ".kube", "config")

	// Update kubeconfig using AWS CLI
	args := []string{
		"eks", "update-kubeconfig",
		"--name", clusterName,
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	if p.debug {
		fmt.Printf("[aws] updating kubeconfig: aws %s\n", strings.Join(args, " "))
	}

	_, err = p.runAWS(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("failed to update kubeconfig: %w", err)
	}

	return kubeconfigPath, nil
}

// Health checks cluster health
func (p *EKSProvider) Health(ctx context.Context, clusterName string) (*HealthStatus, error) {
	status := &HealthStatus{
		Components:  make(map[string]string),
		NodeStatus:  make(map[string]string),
		LastChecked: time.Now(),
	}

	// Get cluster status from AWS
	cluster, err := p.describeCluster(ctx, clusterName)
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to describe cluster: %v", err)
		return status, nil
	}

	clusterStatus := cluster.Status
	status.Components["cluster"] = clusterStatus

	// Check if cluster is active
	if clusterStatus != "ACTIVE" {
		status.Healthy = false
		status.Message = fmt.Sprintf("cluster status is %s", clusterStatus)
		return status, nil
	}

	// Get node groups and their status
	nodeGroups, err := p.listNodeGroups(ctx, clusterName)
	if err == nil {
		for _, ng := range nodeGroups {
			ngStatus, err := p.describeNodeGroup(ctx, clusterName, ng)
			if err == nil {
				status.Components[fmt.Sprintf("nodegroup-%s", ng)] = ngStatus.Status
			}
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
				status.Message = fmt.Sprintf("cluster ACTIVE, %d/%d nodes ready", readyNodes, len(nodes))
			} else {
				status.Healthy = false
				status.Message = fmt.Sprintf("cluster ACTIVE, but only %d/%d nodes ready", readyNodes, len(nodes))
			}
		} else {
			status.Healthy = true
			status.Message = fmt.Sprintf("cluster ACTIVE, unable to check node status: %v", err)
		}
	} else {
		status.Healthy = true
		status.Message = "cluster ACTIVE, kubeconfig update failed"
	}

	return status, nil
}

// ListClusters returns all EKS clusters in the region
func (p *EKSProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	args := []string{"eks", "list-clusters", "--output", "json"}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	output, err := p.runAWS(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	var result struct {
		Clusters []string `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse cluster list: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(result.Clusters))
	for _, name := range result.Clusters {
		cluster, err := p.GetCluster(ctx, name)
		if err != nil {
			// Include basic info even if full details fail
			clusters = append(clusters, ClusterInfo{
				Name:   name,
				Type:   ClusterTypeEKS,
				Status: "unknown",
				Region: p.region,
			})
			continue
		}
		clusters = append(clusters, *cluster)
	}

	return clusters, nil
}

// GetCluster returns information about a specific cluster
func (p *EKSProvider) GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	cluster, err := p.describeCluster(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	info := &ClusterInfo{
		Name:              cluster.Name,
		Type:              ClusterTypeEKS,
		Status:            cluster.Status,
		KubernetesVersion: cluster.Version,
		Endpoint:          cluster.Endpoint,
		Region:            p.region,
		VPCID:             cluster.VpcId,
		CreatedAt:         cluster.CreatedAt,
	}

	// Get node information from node groups
	nodeGroups, err := p.listNodeGroups(ctx, clusterName)
	if err == nil {
		for _, ng := range nodeGroups {
			ngInfo, err := p.describeNodeGroup(ctx, clusterName, ng)
			if err != nil {
				continue
			}

			for i := 0; i < ngInfo.DesiredSize; i++ {
				info.WorkerNodes = append(info.WorkerNodes, NodeInfo{
					Name:   fmt.Sprintf("%s-node-%d", ng, i),
					Role:   "worker",
					Status: ngInfo.Status,
				})
			}
		}
	}

	return info, nil
}

// CreateNodeGroup creates a new node group for an EKS cluster
func (p *EKSProvider) CreateNodeGroup(ctx context.Context, clusterName string, opts NodeGroupOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}
	if opts.Name == "" {
		return &ErrInvalidConfiguration{Message: "node group name is required"}
	}

	// Use eksctl if available
	if p.hasEksctl() {
		return p.createNodeGroupWithEksctl(ctx, clusterName, opts)
	}

	return p.createNodeGroupWithAWSCLI(ctx, clusterName, opts)
}

// NodeGroupOptions contains options for creating a node group
type NodeGroupOptions struct {
	Name         string
	InstanceType string
	DesiredSize  int
	MinSize      int
	MaxSize      int
	DiskSize     int
	SubnetIds    []string
	Labels       map[string]string
	Taints       []NodeTaint
	AMIType      string
	SSHKeyName   string
}

// NodeTaint represents a Kubernetes node taint
type NodeTaint struct {
	Key    string
	Value  string
	Effect string
}

func (p *EKSProvider) createNodeGroupWithEksctl(ctx context.Context, clusterName string, opts NodeGroupOptions) error {
	args := []string{
		"create", "nodegroup",
		"--cluster", clusterName,
		"--name", opts.Name,
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	if opts.InstanceType != "" {
		args = append(args, "--node-type", opts.InstanceType)
	}

	if opts.DesiredSize > 0 {
		args = append(args, "--nodes", fmt.Sprintf("%d", opts.DesiredSize))
	}
	if opts.MinSize > 0 {
		args = append(args, "--nodes-min", fmt.Sprintf("%d", opts.MinSize))
	}
	if opts.MaxSize > 0 {
		args = append(args, "--nodes-max", fmt.Sprintf("%d", opts.MaxSize))
	}

	if opts.DiskSize > 0 {
		args = append(args, "--node-volume-size", fmt.Sprintf("%d", opts.DiskSize))
	}

	if opts.SSHKeyName != "" {
		args = append(args, "--ssh-access", "--ssh-public-key", opts.SSHKeyName)
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
		fmt.Printf("[eksctl] creating node group: eksctl %s\n", strings.Join(args, " "))
	}

	_, err := p.runEksctl(ctx, args...)
	return err
}

func (p *EKSProvider) createNodeGroupWithAWSCLI(ctx context.Context, clusterName string, opts NodeGroupOptions) error {
	args := []string{
		"eks", "create-nodegroup",
		"--cluster-name", clusterName,
		"--nodegroup-name", opts.Name,
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	// Scaling config
	scalingConfig := fmt.Sprintf("desiredSize=%d", opts.DesiredSize)
	if opts.MinSize > 0 {
		scalingConfig += fmt.Sprintf(",minSize=%d", opts.MinSize)
	} else {
		scalingConfig += fmt.Sprintf(",minSize=%d", opts.DesiredSize)
	}
	if opts.MaxSize > 0 {
		scalingConfig += fmt.Sprintf(",maxSize=%d", opts.MaxSize)
	} else {
		scalingConfig += fmt.Sprintf(",maxSize=%d", opts.DesiredSize)
	}
	args = append(args, "--scaling-config", scalingConfig)

	if len(opts.SubnetIds) > 0 {
		args = append(args, "--subnets", strings.Join(opts.SubnetIds, ","))
	}

	if opts.InstanceType != "" {
		args = append(args, "--instance-types", opts.InstanceType)
	}

	if opts.DiskSize > 0 {
		args = append(args, "--disk-size", fmt.Sprintf("%d", opts.DiskSize))
	}

	if opts.AMIType != "" {
		args = append(args, "--ami-type", opts.AMIType)
	}

	// Labels
	if len(opts.Labels) > 0 {
		var labels []string
		for k, v := range opts.Labels {
			labels = append(labels, fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Node role is required for AWS CLI method
	// This would typically come from configuration or be created beforehand

	if p.debug {
		fmt.Printf("[aws] creating node group: aws %s\n", strings.Join(args, " "))
	}

	_, err := p.runAWS(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to create node group: %w", err)
	}

	// Wait for node group to become active
	return p.waitForNodeGroupActive(ctx, clusterName, opts.Name)
}

func (p *EKSProvider) waitForNodeGroupActive(ctx context.Context, clusterName, nodeGroupName string) error {
	deadline := time.Now().Add(DefaultNodeGroupCreateTimeout)

	for time.Now().Before(deadline) {
		ng, err := p.describeNodeGroup(ctx, clusterName, nodeGroupName)
		if err != nil {
			time.Sleep(DefaultPollInterval)
			continue
		}

		if p.debug {
			fmt.Printf("[aws] node group %s status: %s\n", nodeGroupName, ng.Status)
		}

		if ng.Status == "ACTIVE" {
			return nil
		}
		if ng.Status == "CREATE_FAILED" {
			return fmt.Errorf("node group creation failed")
		}

		time.Sleep(DefaultPollInterval)
	}

	return fmt.Errorf("timeout waiting for node group to become active")
}

// ListNodeGroups returns all node groups for a cluster
func (p *EKSProvider) ListNodeGroups(ctx context.Context, clusterName string) ([]eksNodeGroupInfo, error) {
	nodeGroupNames, err := p.listNodeGroups(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	nodeGroups := make([]eksNodeGroupInfo, 0, len(nodeGroupNames))
	for _, name := range nodeGroupNames {
		ng, err := p.describeNodeGroup(ctx, clusterName, name)
		if err != nil {
			continue
		}
		nodeGroups = append(nodeGroups, *ng)
	}

	return nodeGroups, nil
}

// DeleteNodeGroup deletes a node group from a cluster
func (p *EKSProvider) DeleteNodeGroup(ctx context.Context, clusterName, nodeGroupName string) error {
	return p.deleteNodeGroup(ctx, clusterName, nodeGroupName)
}

// Internal types for AWS responses

type eksClusterInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	Endpoint  string    `json:"endpoint"`
	VpcId     string    `json:"vpcId"`
	CreatedAt time.Time `json:"createdAt"`
}

type eksNodeGroupInfo struct {
	NodegroupName string `json:"nodegroupName"`
	Status        string `json:"status"`
	DesiredSize   int    `json:"desiredSize"`
	MinSize       int    `json:"minSize"`
	MaxSize       int    `json:"maxSize"`
}

// Internal methods

func (p *EKSProvider) describeCluster(ctx context.Context, clusterName string) (*eksClusterInfo, error) {
	args := []string{"eks", "describe-cluster", "--name", clusterName, "--output", "json"}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	output, err := p.runAWS(ctx, args...)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return nil, &ErrClusterNotFound{ClusterName: clusterName}
		}
		return nil, err
	}

	var result struct {
		Cluster struct {
			Name               string `json:"name"`
			Status             string `json:"status"`
			Version            string `json:"version"`
			Endpoint           string `json:"endpoint"`
			ResourcesVpcConfig struct {
				VpcId string `json:"vpcId"`
			} `json:"resourcesVpcConfig"`
			CreatedAt string `json:"createdAt"`
		} `json:"cluster"`
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse cluster info: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, result.Cluster.CreatedAt)

	return &eksClusterInfo{
		Name:      result.Cluster.Name,
		Status:    result.Cluster.Status,
		Version:   result.Cluster.Version,
		Endpoint:  result.Cluster.Endpoint,
		VpcId:     result.Cluster.ResourcesVpcConfig.VpcId,
		CreatedAt: createdAt,
	}, nil
}

func (p *EKSProvider) listNodeGroups(ctx context.Context, clusterName string) ([]string, error) {
	args := []string{"eks", "list-nodegroups", "--cluster-name", clusterName, "--output", "json"}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	output, err := p.runAWS(ctx, args...)
	if err != nil {
		return nil, err
	}

	var result struct {
		Nodegroups []string `json:"nodegroups"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, err
	}

	return result.Nodegroups, nil
}

func (p *EKSProvider) describeNodeGroup(ctx context.Context, clusterName, nodeGroupName string) (*eksNodeGroupInfo, error) {
	args := []string{
		"eks", "describe-nodegroup",
		"--cluster-name", clusterName,
		"--nodegroup-name", nodeGroupName,
		"--output", "json",
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	output, err := p.runAWS(ctx, args...)
	if err != nil {
		return nil, err
	}

	var result struct {
		Nodegroup struct {
			NodegroupName string `json:"nodegroupName"`
			Status        string `json:"status"`
			ScalingConfig struct {
				DesiredSize int `json:"desiredSize"`
				MinSize     int `json:"minSize"`
				MaxSize     int `json:"maxSize"`
			} `json:"scalingConfig"`
		} `json:"nodegroup"`
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, err
	}

	return &eksNodeGroupInfo{
		NodegroupName: result.Nodegroup.NodegroupName,
		Status:        result.Nodegroup.Status,
		DesiredSize:   result.Nodegroup.ScalingConfig.DesiredSize,
		MinSize:       result.Nodegroup.ScalingConfig.MinSize,
		MaxSize:       result.Nodegroup.ScalingConfig.MaxSize,
	}, nil
}

func (p *EKSProvider) deleteNodeGroup(ctx context.Context, clusterName, nodeGroupName string) error {
	args := []string{
		"eks", "delete-nodegroup",
		"--cluster-name", clusterName,
		"--nodegroup-name", nodeGroupName,
	}

	if p.region != "" {
		args = append(args, "--region", p.region)
	}
	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}

	if p.debug {
		fmt.Printf("[aws] deleting node group: aws %s\n", strings.Join(args, " "))
	}

	_, err := p.runAWS(ctx, args...)
	if err != nil {
		return err
	}

	// Wait for node group deletion
	return p.waitForNodeGroupDeleted(ctx, clusterName, nodeGroupName)
}

func (p *EKSProvider) waitForClusterActive(ctx context.Context, clusterName, profile, region string, timeout time.Duration) error {
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
			fmt.Printf("[aws] cluster %s status: %s\n", clusterName, cluster.Status)
		}

		if cluster.Status == "ACTIVE" {
			return nil
		}
		if cluster.Status == "FAILED" {
			return fmt.Errorf("cluster creation failed")
		}

		time.Sleep(DefaultPollInterval)
	}

	return fmt.Errorf("timeout waiting for cluster to become active")
}

func (p *EKSProvider) waitForNodeGroupDeleted(ctx context.Context, clusterName, nodeGroupName string) error {
	deadline := time.Now().Add(DefaultNodeGroupDeleteTimeout)

	for time.Now().Before(deadline) {
		_, err := p.describeNodeGroup(ctx, clusterName, nodeGroupName)
		if err != nil {
			// Node group not found means it's deleted
			return nil
		}

		if p.debug {
			fmt.Printf("[aws] waiting for node group %s deletion\n", nodeGroupName)
		}

		time.Sleep(DefaultPollInterval)
	}

	return fmt.Errorf("timeout waiting for node group deletion")
}

func (p *EKSProvider) getNodesViaKubectl(ctx context.Context) ([]NodeInfo, error) {
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

func (p *EKSProvider) hasEksctl() bool {
	_, err := exec.LookPath("eksctl")
	return err == nil
}

func (p *EKSProvider) runEksctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "eksctl", args...)
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("eksctl command failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

func (p *EKSProvider) runAWS(ctx context.Context, args ...string) (string, error) {
	// Add no-cli-pager to prevent interactive output
	args = append(args, "--no-cli-pager")

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		stderrStr := stderr.String()
		return "", fmt.Errorf("aws command failed: %w, stderr: %s%s", err, stderrStr, p.errorHint(stderrStr))
	}

	return stdout.String(), nil
}

// errorHint returns helpful hints for common AWS CLI errors
func (p *EKSProvider) errorHint(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "accessdenied") || strings.Contains(lower, "access denied"):
		return " (hint: check IAM permissions for EKS operations)"
	case strings.Contains(lower, "authorizationerror") || strings.Contains(lower, "not authorized"):
		return " (hint: IAM user/role lacks required permissions)"
	case strings.Contains(lower, "resourcenotfoundexception"):
		return " (hint: cluster or resource does not exist)"
	case strings.Contains(lower, "invalidparameterexception"):
		return " (hint: check parameter values, e.g., region, cluster name)"
	case strings.Contains(lower, "resourceinuseexception"):
		return " (hint: resource is currently in use or being modified)"
	case strings.Contains(lower, "clusteralreadyexists"):
		return " (hint: cluster with this name already exists)"
	case strings.Contains(lower, "limitexceeded") || strings.Contains(lower, "service quota"):
		return " (hint: AWS service quota exceeded, request increase)"
	case strings.Contains(lower, "unable to locate credentials") || strings.Contains(lower, "no credentials"):
		return " (hint: run 'aws configure' or set AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY)"
	case strings.Contains(lower, "expired token") || strings.Contains(lower, "security token"):
		return " (hint: AWS session token expired, refresh credentials)"
	case strings.Contains(lower, "invalid region"):
		return " (hint: check region name, e.g., us-west-2, eu-west-1)"
	case strings.Contains(lower, "vpc") && strings.Contains(lower, "not found"):
		return " (hint: VPC does not exist in this region)"
	case strings.Contains(lower, "subnet") && strings.Contains(lower, "not found"):
		return " (hint: subnet does not exist or is in a different VPC)"
	case strings.Contains(lower, "security group") && strings.Contains(lower, "not found"):
		return " (hint: security group does not exist or is in a different VPC)"
	case strings.Contains(lower, "role") && (strings.Contains(lower, "not found") || strings.Contains(lower, "invalid")):
		return " (hint: IAM role ARN is invalid or does not exist)"
	case strings.Contains(lower, "throttling") || strings.Contains(lower, "rate exceeded"):
		return " (hint: API rate limit exceeded, retry after a moment)"
	default:
		return ""
	}
}
