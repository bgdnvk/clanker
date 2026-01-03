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

// KubeadmProvider manages kubeadm-based Kubernetes clusters on EC2
type KubeadmProvider struct {
	awsProfile  string
	region      string
	vpcID       string
	subnetID    string
	keyPairName string
	sshKeyPath  string
	debug       bool
}

// KubeadmProviderOptions contains options for creating a kubeadm provider
type KubeadmProviderOptions struct {
	AWSProfile  string
	Region      string
	VPCID       string
	SubnetID    string
	KeyPairName string
	SSHKeyPath  string
	Debug       bool
}

// NewKubeadmProvider creates a new kubeadm cluster provider
func NewKubeadmProvider(opts KubeadmProviderOptions) *KubeadmProvider {
	sshKeyPath := opts.SSHKeyPath
	if sshKeyPath == "" {
		home, _ := os.UserHomeDir()
		sshKeyPath = filepath.Join(home, ".ssh", "id_rsa")
	}

	return &KubeadmProvider{
		awsProfile:  opts.AWSProfile,
		region:      opts.Region,
		vpcID:       opts.VPCID,
		subnetID:    opts.SubnetID,
		keyPairName: opts.KeyPairName,
		sshKeyPath:  sshKeyPath,
		debug:       opts.Debug,
	}
}

// Type returns the cluster type
func (p *KubeadmProvider) Type() ClusterType {
	return ClusterTypeKubeadm
}

// Create provisions a new kubeadm cluster on EC2
func (p *KubeadmProvider) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
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

	if p.keyPairName == "" {
		return nil, &ErrInvalidConfiguration{Message: "SSH key pair name is required for kubeadm clusters"}
	}

	// Check if cluster already exists
	existing, _ := p.GetCluster(ctx, opts.Name)
	if existing != nil {
		return nil, &ErrClusterExists{ClusterName: opts.Name}
	}

	if p.debug {
		fmt.Printf("[kubeadm] creating cluster %s in %s\n", opts.Name, region)
	}

	// Step 1: Create security group
	sgID, err := p.createSecurityGroup(ctx, opts.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create security group: %w", err)
	}

	if p.debug {
		fmt.Printf("[kubeadm] created security group: %s\n", sgID)
	}

	// Step 2: Launch control plane instance
	cpInstanceType := opts.ControlPlaneType
	if cpInstanceType == "" {
		cpInstanceType = "t3.medium"
	}

	cpInstance, err := p.launchInstance(ctx, opts.Name, "control-plane", cpInstanceType, sgID, opts.Tags)
	if err != nil {
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("failed to launch control plane: %w", err)
	}

	if p.debug {
		fmt.Printf("[kubeadm] launched control plane instance: %s (%s)\n", cpInstance.InstanceID, cpInstance.PublicIP)
	}

	// Wait for SSH to be available
	if err := WaitForSSH(ctx, cpInstance.PublicIP, 22, 5*time.Minute); err != nil {
		p.terminateInstance(ctx, cpInstance.InstanceID)
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("control plane SSH not available: %w", err)
	}

	// Step 3: Bootstrap control plane
	ssh, err := NewSSHClient(SSHClientOptions{
		Host:           cpInstance.PublicIP,
		User:           "ubuntu",
		PrivateKeyPath: p.sshKeyPath,
		Debug:          p.debug,
	})
	if err != nil {
		p.terminateInstance(ctx, cpInstance.InstanceID)
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("failed to create SSH client: %w", err)
	}

	if err := ssh.Connect(ctx); err != nil {
		p.terminateInstance(ctx, cpInstance.InstanceID)
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("failed to connect to control plane: %w", err)
	}
	defer ssh.Close()

	// Bootstrap the node
	bootstrapConfig := DefaultBootstrapConfig()
	bootstrapConfig.ClusterName = opts.Name
	if opts.KubernetesVersion != "" {
		bootstrapConfig.KubernetesVersion = opts.KubernetesVersion
	}

	if p.debug {
		fmt.Println("[kubeadm] bootstrapping control plane node...")
	}

	if err := BootstrapNode(ctx, ssh, bootstrapConfig); err != nil {
		p.terminateInstance(ctx, cpInstance.InstanceID)
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("failed to bootstrap control plane: %w", err)
	}

	// Initialize the control plane
	if p.debug {
		fmt.Println("[kubeadm] initializing control plane...")
	}

	initOutput, err := InitializeControlPlane(ctx, ssh, bootstrapConfig)
	if err != nil {
		p.terminateInstance(ctx, cpInstance.InstanceID)
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("kubeadm init failed: %w", err)
	}

	// Install CNI
	if p.debug {
		fmt.Println("[kubeadm] installing CNI (Calico)...")
	}

	if err := InstallCNI(ctx, ssh, bootstrapConfig.CNI); err != nil {
		p.terminateInstance(ctx, cpInstance.InstanceID)
		p.deleteSecurityGroup(ctx, sgID)
		return nil, fmt.Errorf("failed to install CNI: %w", err)
	}

	// Step 4: Launch worker nodes
	workerCount := opts.WorkerCount
	if workerCount <= 0 {
		workerCount = 2
	}

	workerType := opts.WorkerType
	if workerType == "" {
		workerType = "t3.medium"
	}

	var workerNodes []NodeInfo

	for i := 0; i < workerCount; i++ {
		workerInstance, err := p.launchInstance(ctx, opts.Name, fmt.Sprintf("worker-%d", i), workerType, sgID, opts.Tags)
		if err != nil {
			// Clean up on failure
			for _, w := range workerNodes {
				p.terminateInstance(ctx, w.InternalIP) // Using InternalIP to store instance ID temporarily
			}
			p.terminateInstance(ctx, cpInstance.InstanceID)
			p.deleteSecurityGroup(ctx, sgID)
			return nil, fmt.Errorf("failed to launch worker %d: %w", i, err)
		}

		if p.debug {
			fmt.Printf("[kubeadm] launched worker instance %d: %s (%s)\n", i, workerInstance.InstanceID, workerInstance.PublicIP)
		}

		// Wait for SSH
		if err := WaitForSSH(ctx, workerInstance.PublicIP, 22, 5*time.Minute); err != nil {
			// Continue anyway, will fail on bootstrap
			if p.debug {
				fmt.Printf("[kubeadm] warning: worker %d SSH not available: %v\n", i, err)
			}
		}

		// Bootstrap and join worker
		workerSSH, err := NewSSHClient(SSHClientOptions{
			Host:           workerInstance.PublicIP,
			User:           "ubuntu",
			PrivateKeyPath: p.sshKeyPath,
			Debug:          p.debug,
		})
		if err != nil {
			continue
		}

		if err := workerSSH.Connect(ctx); err != nil {
			workerSSH.Close()
			continue
		}

		if p.debug {
			fmt.Printf("[kubeadm] bootstrapping worker %d...\n", i)
		}

		if err := BootstrapNode(ctx, workerSSH, bootstrapConfig); err != nil {
			workerSSH.Close()
			if p.debug {
				fmt.Printf("[kubeadm] warning: failed to bootstrap worker %d: %v\n", i, err)
			}
			continue
		}

		// Join the worker to the cluster
		joinConfig := bootstrapConfig
		joinConfig.ControlPlaneIP = cpInstance.PrivateIP
		joinConfig.JoinToken = initOutput.Token
		joinConfig.CACertHash = initOutput.CACertHash
		joinConfig.IsControlPlane = false

		if p.debug {
			fmt.Printf("[kubeadm] joining worker %d to cluster...\n", i)
		}

		if err := JoinWorker(ctx, workerSSH, joinConfig); err != nil {
			workerSSH.Close()
			if p.debug {
				fmt.Printf("[kubeadm] warning: failed to join worker %d: %v\n", i, err)
			}
			continue
		}

		workerSSH.Close()

		workerNodes = append(workerNodes, NodeInfo{
			Name:       fmt.Sprintf("%s-worker-%d", opts.Name, i),
			Role:       "worker",
			Status:     "Ready",
			InternalIP: workerInstance.PrivateIP,
			ExternalIP: workerInstance.PublicIP,
		})
	}

	// Wait for all nodes to be ready
	if p.debug {
		fmt.Println("[kubeadm] waiting for nodes to be ready...")
	}

	if err := WaitForNodeReady(ctx, ssh, 5*time.Minute); err != nil {
		if p.debug {
			fmt.Printf("[kubeadm] warning: not all nodes ready: %v\n", err)
		}
	}

	// Build cluster info
	info := &ClusterInfo{
		Name:              opts.Name,
		Type:              ClusterTypeKubeadm,
		Status:            "ACTIVE",
		KubernetesVersion: bootstrapConfig.KubernetesVersion,
		Endpoint:          fmt.Sprintf("https://%s:6443", cpInstance.PublicIP),
		Region:            region,
		ControlPlaneNodes: []NodeInfo{
			{
				Name:       fmt.Sprintf("%s-control-plane", opts.Name),
				Role:       "control-plane",
				Status:     "Ready",
				InternalIP: cpInstance.PrivateIP,
				ExternalIP: cpInstance.PublicIP,
			},
		},
		WorkerNodes: workerNodes,
		CreatedAt:   time.Now(),
	}

	if p.debug {
		fmt.Printf("[kubeadm] cluster %s created successfully\n", opts.Name)
	}

	return info, nil
}

// Delete removes a kubeadm cluster
func (p *KubeadmProvider) Delete(ctx context.Context, clusterName string) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	if p.debug {
		fmt.Printf("[kubeadm] deleting cluster %s\n", clusterName)
	}

	// Find all instances with the cluster tag
	instances, err := p.findClusterInstances(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("failed to find cluster instances: %w", err)
	}

	// Terminate all instances
	for _, instanceID := range instances {
		if p.debug {
			fmt.Printf("[kubeadm] terminating instance %s\n", instanceID)
		}
		if err := p.terminateInstance(ctx, instanceID); err != nil {
			if p.debug {
				fmt.Printf("[kubeadm] warning: failed to terminate %s: %v\n", instanceID, err)
			}
		}
	}

	// Wait for instances to terminate
	time.Sleep(30 * time.Second)

	// Delete security group
	sgID, err := p.findSecurityGroup(ctx, clusterName)
	if err == nil && sgID != "" {
		if p.debug {
			fmt.Printf("[kubeadm] deleting security group %s\n", sgID)
		}
		p.deleteSecurityGroup(ctx, sgID)
	}

	if p.debug {
		fmt.Printf("[kubeadm] cluster %s deleted\n", clusterName)
	}

	return nil
}

// Scale adjusts the worker node count
func (p *KubeadmProvider) Scale(ctx context.Context, clusterName string, opts ScaleOptions) error {
	if clusterName == "" {
		return &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	cluster, err := p.GetCluster(ctx, clusterName)
	if err != nil {
		return err
	}

	currentCount := len(cluster.WorkerNodes)
	desiredCount := opts.DesiredCount

	if p.debug {
		fmt.Printf("[kubeadm] scaling cluster %s from %d to %d workers\n", clusterName, currentCount, desiredCount)
	}

	if desiredCount > currentCount {
		// Scale up: add new workers
		return p.scaleUp(ctx, cluster, desiredCount-currentCount, opts)
	} else if desiredCount < currentCount {
		// Scale down: remove workers
		return p.scaleDown(ctx, cluster, currentCount-desiredCount)
	}

	return nil
}

// GetKubeconfig retrieves the kubeconfig for the cluster
func (p *KubeadmProvider) GetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	if clusterName == "" {
		return "", &ErrInvalidConfiguration{Message: "cluster name is required"}
	}

	cluster, err := p.GetCluster(ctx, clusterName)
	if err != nil {
		return "", err
	}

	if len(cluster.ControlPlaneNodes) == 0 {
		return "", fmt.Errorf("no control plane nodes found")
	}

	cpIP := cluster.ControlPlaneNodes[0].ExternalIP
	if cpIP == "" {
		cpIP = cluster.ControlPlaneNodes[0].InternalIP
	}

	// Connect to control plane and get kubeconfig
	ssh, err := NewSSHClient(SSHClientOptions{
		Host:           cpIP,
		User:           "ubuntu",
		PrivateKeyPath: p.sshKeyPath,
		Debug:          p.debug,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create SSH client: %w", err)
	}

	if err := ssh.Connect(ctx); err != nil {
		return "", fmt.Errorf("failed to connect to control plane: %w", err)
	}
	defer ssh.Close()

	kubeconfig, err := GetKubeconfig(ctx, ssh)
	if err != nil {
		return "", fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Save kubeconfig to local file
	home, _ := os.UserHomeDir()
	kubeconfigPath := filepath.Join(home, ".kube", fmt.Sprintf("kubeadm-%s", clusterName))

	if err := os.MkdirAll(filepath.Dir(kubeconfigPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create .kube directory: %w", err)
	}

	if err := os.WriteFile(kubeconfigPath, kubeconfig, 0600); err != nil {
		return "", fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	return kubeconfigPath, nil
}

// Health checks cluster health
func (p *KubeadmProvider) Health(ctx context.Context, clusterName string) (*HealthStatus, error) {
	status := &HealthStatus{
		Components:  make(map[string]string),
		NodeStatus:  make(map[string]string),
		LastChecked: time.Now(),
	}

	cluster, err := p.GetCluster(ctx, clusterName)
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to get cluster: %v", err)
		return status, nil
	}

	if len(cluster.ControlPlaneNodes) == 0 {
		status.Healthy = false
		status.Message = "no control plane nodes found"
		return status, nil
	}

	cpIP := cluster.ControlPlaneNodes[0].ExternalIP
	if cpIP == "" {
		cpIP = cluster.ControlPlaneNodes[0].InternalIP
	}

	// Connect and check nodes
	ssh, err := NewSSHClient(SSHClientOptions{
		Host:           cpIP,
		User:           "ubuntu",
		PrivateKeyPath: p.sshKeyPath,
		Debug:          p.debug,
	})
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to create SSH client: %v", err)
		return status, nil
	}

	if err := ssh.Connect(ctx); err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to connect: %v", err)
		return status, nil
	}
	defer ssh.Close()

	// Get node status
	output, err := ssh.Run(ctx, "kubectl get nodes -o json")
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to get nodes: %v", err)
		return status, nil
	}

	var nodeList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal([]byte(output), &nodeList); err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("failed to parse nodes: %v", err)
		return status, nil
	}

	readyCount := 0
	for _, node := range nodeList.Items {
		nodeStatus := "NotReady"
		for _, cond := range node.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				nodeStatus = "Ready"
				readyCount++
				break
			}
		}
		status.NodeStatus[node.Metadata.Name] = nodeStatus
	}

	totalNodes := len(nodeList.Items)
	if readyCount == totalNodes && totalNodes > 0 {
		status.Healthy = true
		status.Message = fmt.Sprintf("all %d nodes ready", totalNodes)
	} else {
		status.Healthy = false
		status.Message = fmt.Sprintf("%d/%d nodes ready", readyCount, totalNodes)
	}

	status.Components["control-plane"] = "ACTIVE"

	return status, nil
}

// ListClusters returns all kubeadm clusters in the region
func (p *KubeadmProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	// Find all clusters by looking for security groups with our tag pattern
	output, err := p.runAWS(ctx, "ec2", "describe-security-groups",
		"--filters", "Name=tag-key,Values=kubernetes.io/cluster/*",
		"--query", "SecurityGroups[*].Tags[?Key=='kubernetes.io/cluster'].Value",
		"--output", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	var tagValues [][]string
	if err := json.Unmarshal([]byte(output), &tagValues); err != nil {
		return nil, fmt.Errorf("failed to parse cluster list: %w", err)
	}

	// Extract unique cluster names
	clusterNames := make(map[string]bool)
	for _, tags := range tagValues {
		for _, tag := range tags {
			if tag != "" {
				clusterNames[tag] = true
			}
		}
	}

	// Get details for each cluster
	var clusters []ClusterInfo
	for name := range clusterNames {
		cluster, err := p.GetCluster(ctx, name)
		if err != nil {
			continue
		}
		clusters = append(clusters, *cluster)
	}

	return clusters, nil
}

// GetCluster returns information about a specific cluster
func (p *KubeadmProvider) GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	instances, err := p.findClusterInstances(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	if len(instances) == 0 {
		return nil, &ErrClusterNotFound{ClusterName: clusterName}
	}

	// Get instance details
	output, err := p.runAWS(ctx, "ec2", "describe-instances",
		"--instance-ids", strings.Join(instances, " "),
		"--output", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to describe instances: %w", err)
	}

	var result struct {
		Reservations []struct {
			Instances []struct {
				InstanceID       string `json:"InstanceId"`
				PrivateIPAddress string `json:"PrivateIpAddress"`
				PublicIPAddress  string `json:"PublicIpAddress"`
				State            struct {
					Name string `json:"Name"`
				} `json:"State"`
				Tags []struct {
					Key   string `json:"Key"`
					Value string `json:"Value"`
				} `json:"Tags"`
			} `json:"Instances"`
		} `json:"Reservations"`
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse instances: %w", err)
	}

	info := &ClusterInfo{
		Name:   clusterName,
		Type:   ClusterTypeKubeadm,
		Status: "ACTIVE",
		Region: p.region,
	}

	for _, res := range result.Reservations {
		for _, inst := range res.Instances {
			if inst.State.Name != "running" {
				continue
			}

			node := NodeInfo{
				InternalIP: inst.PrivateIPAddress,
				ExternalIP: inst.PublicIPAddress,
				Status:     "Ready",
			}

			// Get role from tags
			for _, tag := range inst.Tags {
				if tag.Key == "kubernetes.io/role" {
					node.Role = tag.Value
				}
				if tag.Key == "Name" {
					node.Name = tag.Value
				}
			}

			if node.Role == "control-plane" {
				info.ControlPlaneNodes = append(info.ControlPlaneNodes, node)
				info.Endpoint = fmt.Sprintf("https://%s:6443", node.ExternalIP)
			} else {
				info.WorkerNodes = append(info.WorkerNodes, node)
			}
		}
	}

	return info, nil
}

// Internal helper methods

type ec2Instance struct {
	InstanceID string
	PublicIP   string
	PrivateIP  string
}

func (p *KubeadmProvider) launchInstance(ctx context.Context, clusterName, role, instanceType, sgID string, extraTags map[string]string) (*ec2Instance, error) {
	// Get the latest Ubuntu 22.04 AMI
	amiOutput, err := p.runAWS(ctx, "ec2", "describe-images",
		"--owners", "099720109477",
		"--filters",
		"Name=name,Values=ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*",
		"Name=state,Values=available",
		"--query", "sort_by(Images, &CreationDate)[-1].ImageId",
		"--output", "text")
	if err != nil {
		return nil, fmt.Errorf("failed to find Ubuntu AMI: %w", err)
	}

	amiID := strings.TrimSpace(amiOutput)

	// Build tags
	tags := []string{
		fmt.Sprintf("Key=Name,Value=%s-%s", clusterName, role),
		fmt.Sprintf("Key=kubernetes.io/cluster/%s,Value=owned", clusterName),
		fmt.Sprintf("Key=kubernetes.io/role,Value=%s", role),
	}
	for k, v := range extraTags {
		tags = append(tags, fmt.Sprintf("Key=%s,Value=%s", k, v))
	}

	tagSpec := fmt.Sprintf("ResourceType=instance,Tags=[{%s}]", strings.Join(tags, "},{"))

	args := []string{
		"ec2", "run-instances",
		"--image-id", amiID,
		"--instance-type", instanceType,
		"--key-name", p.keyPairName,
		"--security-group-ids", sgID,
		"--tag-specifications", tagSpec,
		"--associate-public-ip-address",
		"--output", "json",
	}

	if p.subnetID != "" {
		args = append(args, "--subnet-id", p.subnetID)
	}

	output, err := p.runAWS(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to launch instance: %w", err)
	}

	var result struct {
		Instances []struct {
			InstanceID       string `json:"InstanceId"`
			PrivateIPAddress string `json:"PrivateIpAddress"`
		} `json:"Instances"`
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse instance: %w", err)
	}

	if len(result.Instances) == 0 {
		return nil, fmt.Errorf("no instance created")
	}

	instanceID := result.Instances[0].InstanceID

	// Wait for instance to be running and get public IP
	if p.debug {
		fmt.Printf("[kubeadm] waiting for instance %s to be running...\n", instanceID)
	}

	_, err = p.runAWS(ctx, "ec2", "wait", "instance-running", "--instance-ids", instanceID)
	if err != nil {
		return nil, fmt.Errorf("instance did not start: %w", err)
	}

	// Get public IP
	ipOutput, err := p.runAWS(ctx, "ec2", "describe-instances",
		"--instance-ids", instanceID,
		"--query", "Reservations[0].Instances[0].[PublicIpAddress,PrivateIpAddress]",
		"--output", "text")
	if err != nil {
		return nil, fmt.Errorf("failed to get instance IPs: %w", err)
	}

	ips := strings.Fields(ipOutput)
	publicIP := ""
	privateIP := result.Instances[0].PrivateIPAddress

	if len(ips) >= 1 && ips[0] != "None" {
		publicIP = ips[0]
	}
	if len(ips) >= 2 {
		privateIP = ips[1]
	}

	return &ec2Instance{
		InstanceID: instanceID,
		PublicIP:   publicIP,
		PrivateIP:  privateIP,
	}, nil
}

func (p *KubeadmProvider) terminateInstance(ctx context.Context, instanceID string) error {
	_, err := p.runAWS(ctx, "ec2", "terminate-instances", "--instance-ids", instanceID)
	return err
}

func (p *KubeadmProvider) createSecurityGroup(ctx context.Context, clusterName string) (string, error) {
	sgName := fmt.Sprintf("%s-k8s-sg", clusterName)

	// Create security group
	args := []string{
		"ec2", "create-security-group",
		"--group-name", sgName,
		"--description", fmt.Sprintf("Security group for kubeadm cluster %s", clusterName),
		"--output", "json",
	}

	if p.vpcID != "" {
		args = append(args, "--vpc-id", p.vpcID)
	}

	output, err := p.runAWS(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %w", err)
	}

	var result struct {
		GroupID string `json:"GroupId"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", fmt.Errorf("failed to parse security group: %w", err)
	}

	sgID := result.GroupID

	// Tag the security group
	_, _ = p.runAWS(ctx, "ec2", "create-tags",
		"--resources", sgID,
		"--tags",
		fmt.Sprintf("Key=Name,Value=%s", sgName),
		fmt.Sprintf("Key=kubernetes.io/cluster/%s,Value=owned", clusterName))

	// Add ingress rules
	rules := []struct {
		port     string
		protocol string
		desc     string
	}{
		{"22", "tcp", "SSH"},
		{"6443", "tcp", "Kubernetes API"},
		{"2379-2380", "tcp", "etcd"},
		{"10250-10252", "tcp", "Kubelet"},
		{"30000-32767", "tcp", "NodePort Services"},
	}

	for _, rule := range rules {
		_, _ = p.runAWS(ctx, "ec2", "authorize-security-group-ingress",
			"--group-id", sgID,
			"--protocol", rule.protocol,
			"--port", rule.port,
			"--cidr", "0.0.0.0/0")
	}

	// Allow all traffic within the security group
	_, _ = p.runAWS(ctx, "ec2", "authorize-security-group-ingress",
		"--group-id", sgID,
		"--protocol", "-1",
		"--source-group", sgID)

	return sgID, nil
}

func (p *KubeadmProvider) deleteSecurityGroup(ctx context.Context, sgID string) error {
	_, err := p.runAWS(ctx, "ec2", "delete-security-group", "--group-id", sgID)
	return err
}

func (p *KubeadmProvider) findSecurityGroup(ctx context.Context, clusterName string) (string, error) {
	output, err := p.runAWS(ctx, "ec2", "describe-security-groups",
		"--filters",
		fmt.Sprintf("Name=tag:kubernetes.io/cluster/%s,Values=owned", clusterName),
		"--query", "SecurityGroups[0].GroupId",
		"--output", "text")
	if err != nil {
		return "", err
	}

	sgID := strings.TrimSpace(output)
	if sgID == "" || sgID == "None" {
		return "", fmt.Errorf("security group not found")
	}

	return sgID, nil
}

func (p *KubeadmProvider) findClusterInstances(ctx context.Context, clusterName string) ([]string, error) {
	output, err := p.runAWS(ctx, "ec2", "describe-instances",
		"--filters",
		fmt.Sprintf("Name=tag:kubernetes.io/cluster/%s,Values=owned", clusterName),
		"Name=instance-state-name,Values=running,pending",
		"--query", "Reservations[*].Instances[*].InstanceId",
		"--output", "json")
	if err != nil {
		return nil, err
	}

	var result [][]string
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, err
	}

	var instances []string
	for _, res := range result {
		instances = append(instances, res...)
	}

	return instances, nil
}

func (p *KubeadmProvider) scaleUp(ctx context.Context, cluster *ClusterInfo, count int, opts ScaleOptions) error {
	// Get security group
	sgID, err := p.findSecurityGroup(ctx, cluster.Name)
	if err != nil {
		return fmt.Errorf("failed to find security group: %w", err)
	}

	// Get join token from control plane
	if len(cluster.ControlPlaneNodes) == 0 {
		return fmt.Errorf("no control plane nodes found")
	}

	cpIP := cluster.ControlPlaneNodes[0].ExternalIP
	if cpIP == "" {
		cpIP = cluster.ControlPlaneNodes[0].InternalIP
	}

	ssh, err := NewSSHClient(SSHClientOptions{
		Host:           cpIP,
		User:           "ubuntu",
		PrivateKeyPath: p.sshKeyPath,
		Debug:          p.debug,
	})
	if err != nil {
		return fmt.Errorf("failed to create SSH client: %w", err)
	}

	if err := ssh.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to control plane: %w", err)
	}
	defer ssh.Close()

	joinOutput, err := GetJoinToken(ctx, ssh)
	if err != nil {
		return fmt.Errorf("failed to get join token: %w", err)
	}

	// Launch new workers
	currentCount := len(cluster.WorkerNodes)
	instanceType := "t3.medium"

	for i := 0; i < count; i++ {
		workerIndex := currentCount + i

		instance, err := p.launchInstance(ctx, cluster.Name, fmt.Sprintf("worker-%d", workerIndex), instanceType, sgID, nil)
		if err != nil {
			return fmt.Errorf("failed to launch worker %d: %w", workerIndex, err)
		}

		// Wait for SSH
		if err := WaitForSSH(ctx, instance.PublicIP, 22, 5*time.Minute); err != nil {
			continue
		}

		// Bootstrap and join
		workerSSH, err := NewSSHClient(SSHClientOptions{
			Host:           instance.PublicIP,
			User:           "ubuntu",
			PrivateKeyPath: p.sshKeyPath,
			Debug:          p.debug,
		})
		if err != nil {
			continue
		}

		if err := workerSSH.Connect(ctx); err != nil {
			workerSSH.Close()
			continue
		}

		bootstrapConfig := DefaultBootstrapConfig()
		if err := BootstrapNode(ctx, workerSSH, bootstrapConfig); err != nil {
			workerSSH.Close()
			continue
		}

		joinConfig := bootstrapConfig
		joinConfig.ControlPlaneIP = cluster.ControlPlaneNodes[0].InternalIP
		joinConfig.JoinToken = joinOutput.Token
		joinConfig.CACertHash = joinOutput.CACertHash

		if err := JoinWorker(ctx, workerSSH, joinConfig); err != nil {
			workerSSH.Close()
			continue
		}

		workerSSH.Close()
	}

	return nil
}

func (p *KubeadmProvider) scaleDown(ctx context.Context, cluster *ClusterInfo, count int) error {
	// Drain and remove workers (starting from the last ones)
	if len(cluster.ControlPlaneNodes) == 0 {
		return fmt.Errorf("no control plane nodes found")
	}

	cpIP := cluster.ControlPlaneNodes[0].ExternalIP
	if cpIP == "" {
		cpIP = cluster.ControlPlaneNodes[0].InternalIP
	}

	ssh, err := NewSSHClient(SSHClientOptions{
		Host:           cpIP,
		User:           "ubuntu",
		PrivateKeyPath: p.sshKeyPath,
		Debug:          p.debug,
	})
	if err != nil {
		return fmt.Errorf("failed to create SSH client: %w", err)
	}

	if err := ssh.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to control plane: %w", err)
	}
	defer ssh.Close()

	workersToRemove := cluster.WorkerNodes[len(cluster.WorkerNodes)-count:]

	for _, worker := range workersToRemove {
		// Drain the node
		_, _ = ssh.Run(ctx, fmt.Sprintf("kubectl drain %s --ignore-daemonsets --delete-emptydir-data --force", worker.Name))

		// Delete the node
		_, _ = ssh.Run(ctx, fmt.Sprintf("kubectl delete node %s", worker.Name))

		// Find and terminate the instance
		instances, err := p.findClusterInstances(ctx, cluster.Name)
		if err != nil {
			continue
		}

		for _, instanceID := range instances {
			// Check if this is the worker we want to remove
			output, _ := p.runAWS(ctx, "ec2", "describe-instances",
				"--instance-ids", instanceID,
				"--query", "Reservations[0].Instances[0].PrivateIpAddress",
				"--output", "text")

			if strings.TrimSpace(output) == worker.InternalIP {
				p.terminateInstance(ctx, instanceID)
				break
			}
		}
	}

	return nil
}

func (p *KubeadmProvider) runAWS(ctx context.Context, args ...string) (string, error) {
	args = append(args, "--no-cli-pager")

	if p.awsProfile != "" {
		args = append(args, "--profile", p.awsProfile)
	}
	if p.region != "" {
		args = append(args, "--region", p.region)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if p.debug {
		fmt.Printf("[aws] %s\n", strings.Join(args, " "))
	}

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("aws command failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
