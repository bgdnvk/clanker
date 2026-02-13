package plan

import (
	"fmt"
	"time"
)

// EKSCreateOptions holds options for EKS cluster creation
type EKSCreateOptions struct {
	ClusterName       string
	Region            string
	Profile           string
	NodeCount         int
	NodeType          string
	KubernetesVersion string
}

// GKECreateOptions holds options for GKE cluster creation
type GKECreateOptions struct {
	ClusterName       string
	Project           string
	Region            string
	NodeCount         int
	NodeType          string
	KubernetesVersion string
	Preemptible       bool
}

// KubeadmCreateOptions holds options for kubeadm cluster creation
type KubeadmCreateOptions struct {
	ClusterName       string
	Region            string
	Profile           string
	WorkerCount       int
	NodeType          string
	ControlPlaneType  string
	KubernetesVersion string
	KeyPairName       string
	SSHKeyPath        string
	CNI               string // calico, flannel
}

// DeployOptions holds options for deploying an application
type DeployOptions struct {
	Name      string
	Image     string
	Port      int
	Replicas  int
	Namespace string
	Type      string // deployment, statefulset
}

// DeleteOptions holds options for cluster deletion
type DeleteOptions struct {
	ClusterType string
	ClusterName string
	Region      string
	Profile     string
}

// GenerateEKSCreatePlan generates a plan for creating an EKS cluster
func GenerateEKSCreatePlan(opts EKSCreateOptions) *K8sPlan {
	plan := &K8sPlan{
		Version:     CurrentPlanVersion,
		CreatedAt:   time.Now(),
		Operation:   "create-cluster",
		ClusterType: "eks",
		ClusterName: opts.ClusterName,
		Region:      opts.Region,
		Profile:     opts.Profile,
		Summary:     fmt.Sprintf("Create EKS cluster '%s' with %d worker nodes", opts.ClusterName, opts.NodeCount),
		Steps:       []Step{},
		Notes: []string{
			"Cluster creation typically takes 15-20 minutes",
			"Default addons (vpc-cni, coredns, kube-proxy) will be installed automatically",
			fmt.Sprintf("Worker nodes: %d x %s", opts.NodeCount, opts.NodeType),
		},
	}

	// Step 1: Create EKS cluster
	plan.Steps = append(plan.Steps, Step{
		ID:          "create-cluster",
		Description: "Create EKS cluster with eksctl",
		Command:     "eksctl",
		Args: []string{
			"create", "cluster",
			"--name", opts.ClusterName,
			"--version", opts.KubernetesVersion,
			"--nodes", fmt.Sprintf("%d", opts.NodeCount),
			"--node-type", opts.NodeType,
		},
		Reason: "eksctl handles VPC, subnets, IAM roles, and node groups automatically",
		Produces: map[string]string{
			"CLUSTER_ENDPOINT": "endpoint=",
		},
		ConfigChange: &ConfigChange{
			File:        "~/.kube/config",
			Description: "kubeconfig will be updated with new cluster context",
		},
	})

	// Step 2: Wait for cluster to be ready
	plan.Steps = append(plan.Steps, Step{
		ID:          "wait-cluster",
		Description: "Wait for cluster to be ACTIVE",
		Command:     "aws",
		Args: []string{
			"eks", "describe-cluster",
			"--name", opts.ClusterName,
			"--query", "cluster.status",
		},
		WaitFor: &WaitConfig{
			Type:        "cluster-ready",
			Resource:    opts.ClusterName,
			Timeout:     25 * time.Minute,
			Interval:    30 * time.Second,
			Description: "waiting for EKS cluster to be ACTIVE",
		},
	})

	// Step 3: Wait for nodes to be ready
	plan.Steps = append(plan.Steps, Step{
		ID:          "wait-nodes",
		Description: "Wait for worker nodes to be Ready",
		Command:     "kubectl",
		Args:        []string{"get", "nodes"},
		WaitFor: &WaitConfig{
			Type:        "node-ready",
			Timeout:     10 * time.Minute,
			Interval:    20 * time.Second,
			Description: "waiting for nodes to join and be Ready",
		},
	})

	// Step 4: Update kubeconfig
	plan.Steps = append(plan.Steps, Step{
		ID:          "update-kubeconfig",
		Description: "Update kubeconfig for cluster access",
		Command:     "aws",
		Args: []string{
			"eks", "update-kubeconfig",
			"--name", opts.ClusterName,
		},
		Produces: map[string]string{
			"KUBECONFIG": "kubeconfig",
		},
		ConfigChange: &ConfigChange{
			File:        "~/.kube/config",
			Description: fmt.Sprintf("Adding context for cluster %s", opts.ClusterName),
		},
	})

	// Connection info
	plan.Connection = &Connection{
		Kubeconfig: "~/.kube/config",
		Commands: []string{
			"kubectl get nodes",
			"kubectl get pods -A",
			fmt.Sprintf("kubectl config use-context arn:aws:eks:%s:*:cluster/%s", opts.Region, opts.ClusterName),
		},
	}

	return plan
}

// GenerateGKECreatePlan generates a plan for creating a GKE cluster
func GenerateGKECreatePlan(opts GKECreateOptions) *K8sPlan {
	plan := &K8sPlan{
		Version:     CurrentPlanVersion,
		CreatedAt:   time.Now(),
		Operation:   "create-cluster",
		ClusterType: "gke",
		ClusterName: opts.ClusterName,
		Region:      opts.Region,
		Profile:     opts.Project,
		Summary:     fmt.Sprintf("Create GKE cluster '%s' with %d worker nodes", opts.ClusterName, opts.NodeCount),
		Steps:       []Step{},
		Notes: []string{
			"Cluster creation typically takes 5-10 minutes",
			"Default addons will be installed automatically",
			fmt.Sprintf("Worker nodes: %d x %s", opts.NodeCount, opts.NodeType),
			fmt.Sprintf("Project: %s", opts.Project),
			fmt.Sprintf("Region: %s", opts.Region),
		},
	}

	if opts.Preemptible {
		plan.Notes = append(plan.Notes, "Using preemptible VMs (lower cost, may be terminated)")
	}

	// Build gcloud create command args
	createArgs := []string{
		"container", "clusters", "create", opts.ClusterName,
		"--project", opts.Project,
		"--region", opts.Region,
		"--num-nodes", fmt.Sprintf("%d", opts.NodeCount),
		"--machine-type", opts.NodeType,
	}

	if opts.KubernetesVersion != "" {
		createArgs = append(createArgs, "--cluster-version", opts.KubernetesVersion)
	}

	if opts.Preemptible {
		createArgs = append(createArgs, "--preemptible")
	}

	// Step 1: Create GKE cluster
	plan.Steps = append(plan.Steps, Step{
		ID:          "create-cluster",
		Description: "Create GKE cluster with gcloud",
		Command:     "gcloud",
		Args:        createArgs,
		Reason:      "gcloud handles VPC, subnets, node pools, and networking automatically",
		Produces: map[string]string{
			"CLUSTER_ENDPOINT": "endpoint",
		},
		ConfigChange: &ConfigChange{
			File:        "~/.kube/config",
			Description: "kubeconfig will be updated with new cluster context",
		},
	})

	// Step 2: Wait for cluster to be running
	plan.Steps = append(plan.Steps, Step{
		ID:          "wait-cluster",
		Description: "Wait for cluster to be RUNNING",
		Command:     "gcloud",
		Args: []string{
			"container", "clusters", "describe", opts.ClusterName,
			"--project", opts.Project,
			"--region", opts.Region,
			"--format", "value(status)",
		},
		WaitFor: &WaitConfig{
			Type:        "cluster-ready",
			Resource:    opts.ClusterName,
			Timeout:     15 * time.Minute,
			Interval:    30 * time.Second,
			Description: "waiting for GKE cluster to be RUNNING",
		},
	})

	// Step 3: Get credentials
	plan.Steps = append(plan.Steps, Step{
		ID:          "get-credentials",
		Description: "Get cluster credentials and update kubeconfig",
		Command:     "gcloud",
		Args: []string{
			"container", "clusters", "get-credentials", opts.ClusterName,
			"--project", opts.Project,
			"--region", opts.Region,
		},
		Produces: map[string]string{
			"KUBECONFIG": "kubeconfig",
		},
		ConfigChange: &ConfigChange{
			File:        "~/.kube/config",
			Description: fmt.Sprintf("Adding context for cluster %s", opts.ClusterName),
		},
	})

	// Step 4: Wait for nodes to be ready
	plan.Steps = append(plan.Steps, Step{
		ID:          "wait-nodes",
		Description: "Wait for worker nodes to be Ready",
		Command:     "kubectl",
		Args:        []string{"get", "nodes"},
		WaitFor: &WaitConfig{
			Type:        "node-ready",
			Timeout:     10 * time.Minute,
			Interval:    20 * time.Second,
			Description: "waiting for nodes to join and be Ready",
		},
	})

	// Connection info
	plan.Connection = &Connection{
		Kubeconfig: "~/.kube/config",
		Commands: []string{
			"kubectl get nodes",
			"kubectl get pods -A",
			fmt.Sprintf("gcloud container clusters get-credentials %s --project %s --region %s", opts.ClusterName, opts.Project, opts.Region),
		},
	}

	return plan
}

// GenerateKubeadmCreatePlan generates a plan for creating a kubeadm cluster
func GenerateKubeadmCreatePlan(opts KubeadmCreateOptions) *K8sPlan {
	plan := &K8sPlan{
		Version:     CurrentPlanVersion,
		CreatedAt:   time.Now(),
		Operation:   "create-cluster",
		ClusterType: "kubeadm",
		ClusterName: opts.ClusterName,
		Region:      opts.Region,
		Profile:     opts.Profile,
		Summary:     fmt.Sprintf("Create kubeadm cluster '%s' with 1 control plane and %d workers", opts.ClusterName, opts.WorkerCount),
		Steps:       []Step{},
		Notes: []string{
			"Cluster creation typically takes 10-15 minutes",
			"EC2 instances will be provisioned for control plane and workers",
			fmt.Sprintf("Control plane: 1 x %s", opts.ControlPlaneType),
			fmt.Sprintf("Workers: %d x %s", opts.WorkerCount, opts.NodeType),
			"Calico CNI will be installed for pod networking",
		},
	}

	cni := opts.CNI
	if cni == "" {
		cni = "calico"
	}

	// Step 1: Verify/create SSH key pair
	plan.Steps = append(plan.Steps, Step{
		ID:          "ensure-ssh-key",
		Description: "Verify SSH key pair exists in AWS",
		Command:     "aws",
		Args: []string{
			"ec2", "describe-key-pairs",
			"--key-names", opts.KeyPairName,
		},
		Reason: "SSH access is required for bootstrapping nodes",
	})

	// Step 2: Create security group
	plan.Steps = append(plan.Steps, Step{
		ID:          "create-security-group",
		Description: "Create security group for Kubernetes traffic",
		Command:     "aws",
		Args: []string{
			"ec2", "create-security-group",
			"--group-name", fmt.Sprintf("%s-k8s-sg", opts.ClusterName),
			"--description", fmt.Sprintf("Security group for kubeadm cluster %s", opts.ClusterName),
		},
		Reason: "Security group allows SSH, API server, etcd, kubelet, and NodePort traffic",
		Produces: map[string]string{
			"SG_ID": "GroupId",
		},
	})

	// Step 3: Launch control plane instance
	plan.Steps = append(plan.Steps, Step{
		ID:          "launch-control-plane",
		Description: "Launch control plane EC2 instance",
		Command:     "aws",
		Args: []string{
			"ec2", "run-instances",
			"--instance-type", opts.ControlPlaneType,
			"--key-name", opts.KeyPairName,
			"--security-group-ids", "<SG_ID>",
			"--tag-specifications", fmt.Sprintf("ResourceType=instance,Tags=[{Key=Name,Value=%s-control-plane}]", opts.ClusterName),
		},
		Produces: map[string]string{
			"CONTROL_PLANE_ID": "InstanceId",
			"CONTROL_PLANE_IP": "PublicIpAddress",
		},
		WaitFor: &WaitConfig{
			Type:        "instance-running",
			Resource:    "<CONTROL_PLANE_ID>",
			Timeout:     5 * time.Minute,
			Interval:    10 * time.Second,
			Description: "waiting for control plane instance to be running",
		},
	})

	// Step 4: Bootstrap control plane
	plan.Steps = append(plan.Steps, Step{
		ID:          "bootstrap-control-plane",
		Description: "Bootstrap control plane node (containerd, kubeadm, kubelet)",
		Command:     "ssh",
		SSHConfig: &SSHStepConfig{
			Host:       "<CONTROL_PLANE_IP>",
			User:       "ubuntu",
			KeyPath:    opts.SSHKeyPath,
			ScriptName: "bootstrap-k8s-node.sh",
			Script:     bootstrapNodeScript(opts.KubernetesVersion),
		},
		Reason: "Install container runtime and Kubernetes components",
	})

	// Step 5: Initialize control plane
	plan.Steps = append(plan.Steps, Step{
		ID:          "kubeadm-init",
		Description: "Initialize control plane with kubeadm",
		Command:     "ssh",
		SSHConfig: &SSHStepConfig{
			Host:       "<CONTROL_PLANE_IP>",
			User:       "ubuntu",
			KeyPath:    opts.SSHKeyPath,
			ScriptName: "kubeadm-init.sh",
			Script:     kubeadmInitScript(opts.KubernetesVersion),
		},
		Reason: "Initialize Kubernetes control plane",
		Produces: map[string]string{
			"JOIN_TOKEN":   "token",
			"CA_CERT_HASH": "sha256:",
		},
	})

	// Step 6: Install CNI
	plan.Steps = append(plan.Steps, Step{
		ID:          "install-cni",
		Description: fmt.Sprintf("Install %s CNI", cni),
		Command:     "ssh",
		SSHConfig: &SSHStepConfig{
			Host:       "<CONTROL_PLANE_IP>",
			User:       "ubuntu",
			KeyPath:    opts.SSHKeyPath,
			ScriptName: fmt.Sprintf("install-%s.sh", cni),
			Script:     installCNIScript(cni),
		},
		Reason: "Pod networking requires a CNI plugin",
	})

	// Step 7+: Launch and join workers
	for i := 0; i < opts.WorkerCount; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		workerName := fmt.Sprintf("%s-%s", opts.ClusterName, workerID)

		// Launch worker
		plan.Steps = append(plan.Steps, Step{
			ID:          fmt.Sprintf("launch-%s", workerID),
			Description: fmt.Sprintf("Launch worker %d EC2 instance", i),
			Command:     "aws",
			Args: []string{
				"ec2", "run-instances",
				"--instance-type", opts.NodeType,
				"--key-name", opts.KeyPairName,
				"--security-group-ids", "<SG_ID>",
				"--tag-specifications", fmt.Sprintf("ResourceType=instance,Tags=[{Key=Name,Value=%s}]", workerName),
			},
			Produces: map[string]string{
				fmt.Sprintf("WORKER_%d_ID", i): "InstanceId",
				fmt.Sprintf("WORKER_%d_IP", i): "PublicIpAddress",
			},
			WaitFor: &WaitConfig{
				Type:        "instance-running",
				Resource:    fmt.Sprintf("<WORKER_%d_ID>", i),
				Timeout:     5 * time.Minute,
				Interval:    10 * time.Second,
				Description: fmt.Sprintf("waiting for worker %d instance to be running", i),
			},
		})

		// Bootstrap worker
		plan.Steps = append(plan.Steps, Step{
			ID:          fmt.Sprintf("bootstrap-%s", workerID),
			Description: fmt.Sprintf("Bootstrap worker %d node", i),
			Command:     "ssh",
			SSHConfig: &SSHStepConfig{
				Host:       fmt.Sprintf("<WORKER_%d_IP>", i),
				User:       "ubuntu",
				KeyPath:    opts.SSHKeyPath,
				ScriptName: "bootstrap-k8s-node.sh",
				Script:     bootstrapNodeScript(opts.KubernetesVersion),
			},
		})

		// Join worker
		plan.Steps = append(plan.Steps, Step{
			ID:          fmt.Sprintf("join-%s", workerID),
			Description: fmt.Sprintf("Join worker %d to cluster", i),
			Command:     "ssh",
			SSHConfig: &SSHStepConfig{
				Host:       fmt.Sprintf("<WORKER_%d_IP>", i),
				User:       "ubuntu",
				KeyPath:    opts.SSHKeyPath,
				ScriptName: "kubeadm-join.sh",
				Script:     kubeadmJoinScript(),
			},
		})
	}

	// Final step: Wait for all nodes ready
	plan.Steps = append(plan.Steps, Step{
		ID:          "wait-nodes-ready",
		Description: "Wait for all nodes to be Ready",
		Command:     "ssh",
		SSHConfig: &SSHStepConfig{
			Host:       "<CONTROL_PLANE_IP>",
			User:       "ubuntu",
			KeyPath:    opts.SSHKeyPath,
			ScriptName: "check-nodes.sh",
			Script:     "kubectl get nodes",
		},
		WaitFor: &WaitConfig{
			Type:        "node-ready",
			Timeout:     10 * time.Minute,
			Interval:    20 * time.Second,
			Description: "waiting for all nodes to be Ready",
		},
	})

	// Save kubeconfig locally
	plan.Steps = append(plan.Steps, Step{
		ID:          "get-kubeconfig",
		Description: "Retrieve kubeconfig from control plane",
		Command:     "scp",
		Args: []string{
			"-o", "StrictHostKeyChecking=no",
			"-i", opts.SSHKeyPath,
			"ubuntu@<CONTROL_PLANE_IP>:.kube/config",
			fmt.Sprintf("~/.kube/config-%s", opts.ClusterName),
		},
		ConfigChange: &ConfigChange{
			File:        fmt.Sprintf("~/.kube/config-%s", opts.ClusterName),
			Description: "Saving kubeconfig for cluster access",
		},
	})

	// Connection info
	plan.Connection = &Connection{
		Kubeconfig: fmt.Sprintf("~/.kube/config-%s", opts.ClusterName),
		Commands: []string{
			fmt.Sprintf("export KUBECONFIG=~/.kube/config-%s", opts.ClusterName),
			"kubectl get nodes",
			"kubectl get pods -A",
		},
	}

	return plan
}

// GenerateDeployPlan generates a plan for deploying an application
func GenerateDeployPlan(opts DeployOptions) *K8sPlan {
	plan := &K8sPlan{
		Version:     CurrentPlanVersion,
		CreatedAt:   time.Now(),
		Operation:   "deploy",
		ClusterType: "",
		ClusterName: "",
		Summary:     fmt.Sprintf("Deploy %s with %d replicas", opts.Name, opts.Replicas),
		Steps:       []Step{},
		Notes: []string{
			fmt.Sprintf("Image: %s", opts.Image),
			fmt.Sprintf("Port: %d", opts.Port),
			fmt.Sprintf("Namespace: %s", opts.Namespace),
		},
	}

	// Step 1: Create deployment
	plan.Steps = append(plan.Steps, Step{
		ID:          "create-deployment",
		Description: fmt.Sprintf("Create deployment %s", opts.Name),
		Command:     "kubectl",
		Args: []string{
			"create", "deployment", opts.Name,
			"--image", opts.Image,
			"--replicas", fmt.Sprintf("%d", opts.Replicas),
			"-n", opts.Namespace,
		},
	})

	// Step 2: Expose service
	plan.Steps = append(plan.Steps, Step{
		ID:          "create-service",
		Description: fmt.Sprintf("Expose deployment %s as LoadBalancer", opts.Name),
		Command:     "kubectl",
		Args: []string{
			"expose", "deployment", opts.Name,
			"--port", fmt.Sprintf("%d", opts.Port),
			"--type", "LoadBalancer",
			"-n", opts.Namespace,
		},
	})

	// Step 3: Wait for pods ready
	plan.Steps = append(plan.Steps, Step{
		ID:          "wait-pods",
		Description: "Wait for pods to be running",
		Command:     "kubectl",
		Args:        []string{"get", "pods", "-l", fmt.Sprintf("app=%s", opts.Name), "-n", opts.Namespace},
		WaitFor: &WaitConfig{
			Type:        "pods-ready",
			Resource:    fmt.Sprintf("app=%s", opts.Name),
			Timeout:     5 * time.Minute,
			Interval:    10 * time.Second,
			Description: "waiting for pods to be Running",
		},
	})

	// Step 4: Get service endpoint
	plan.Steps = append(plan.Steps, Step{
		ID:          "get-endpoint",
		Description: "Get service endpoint",
		Command:     "kubectl",
		Args:        []string{"get", "svc", opts.Name, "-n", opts.Namespace},
		Produces: map[string]string{
			"SERVICE_ENDPOINT": "EXTERNAL-IP",
		},
	})

	// Connection info
	plan.Connection = &Connection{
		Commands: []string{
			fmt.Sprintf("kubectl get pods -l app=%s -n %s", opts.Name, opts.Namespace),
			fmt.Sprintf("kubectl get svc %s -n %s", opts.Name, opts.Namespace),
			fmt.Sprintf("kubectl logs -l app=%s -n %s", opts.Name, opts.Namespace),
		},
	}

	return plan
}

// GenerateDeletePlan generates a plan for deleting a cluster
func GenerateDeletePlan(opts DeleteOptions) *K8sPlan {
	plan := &K8sPlan{
		Version:     CurrentPlanVersion,
		CreatedAt:   time.Now(),
		Operation:   "delete-cluster",
		ClusterType: opts.ClusterType,
		ClusterName: opts.ClusterName,
		Region:      opts.Region,
		Profile:     opts.Profile,
		Summary:     fmt.Sprintf("Delete %s cluster '%s'", opts.ClusterType, opts.ClusterName),
		Steps:       []Step{},
		Notes: []string{
			"This action is irreversible",
			"All cluster resources will be deleted",
		},
	}

	switch opts.ClusterType {
	case "eks":
		plan.Steps = append(plan.Steps, Step{
			ID:          "delete-cluster",
			Description: "Delete EKS cluster with eksctl",
			Command:     "eksctl",
			Args: []string{
				"delete", "cluster",
				"--name", opts.ClusterName,
				"--wait",
			},
			Reason: "eksctl handles cleanup of VPC, subnets, IAM roles, and node groups",
		})

	case "gke":
		plan.Steps = append(plan.Steps, Step{
			ID:          "delete-cluster",
			Description: "Delete GKE cluster with gcloud",
			Command:     "gcloud",
			Args: []string{
				"container", "clusters", "delete", opts.ClusterName,
				"--project", opts.Profile,
				"--region", opts.Region,
				"--quiet",
			},
			Reason: "gcloud handles cleanup of VPC, subnets, node pools, and networking",
		})

	case "kubeadm":
		// Delete EC2 instances
		plan.Steps = append(plan.Steps, Step{
			ID:          "terminate-instances",
			Description: "Terminate EC2 instances",
			Command:     "aws",
			Args: []string{
				"ec2", "terminate-instances",
				"--instance-ids", fmt.Sprintf("<CLUSTER_%s_INSTANCES>", opts.ClusterName),
			},
		})

		// Delete security group
		plan.Steps = append(plan.Steps, Step{
			ID:          "delete-security-group",
			Description: "Delete security group",
			Command:     "aws",
			Args: []string{
				"ec2", "delete-security-group",
				"--group-name", fmt.Sprintf("%s-k8s-sg", opts.ClusterName),
			},
		})
	}

	return plan
}

// Bootstrap scripts

func bootstrapNodeScript(k8sVersion string) string {
	return fmt.Sprintf(`sudo bash -c '
# Load kernel modules
cat <<EOF | tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF
modprobe overlay
modprobe br_netfilter

# Sysctl settings
cat <<EOF | tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
sysctl --system

# Install containerd
apt-get update
apt-get install -y ca-certificates curl gnupg
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
apt-get update
apt-get install -y containerd.io
mkdir -p /etc/containerd
containerd config default | tee /etc/containerd/config.toml
sed -i "s/SystemdCgroup = false/SystemdCgroup = true/g" /etc/containerd/config.toml
systemctl restart containerd
systemctl enable containerd

# Disable swap
swapoff -a
sed -i "/ swap / s/^\\(.*\\)$/#\\1/g" /etc/fstab

# Install kubeadm, kubelet, kubectl
apt-get install -y apt-transport-https ca-certificates curl gpg
curl -fsSL https://pkgs.k8s.io/core:/stable:/v%s/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v%s/deb/ /" | tee /etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl
systemctl enable kubelet
'`, k8sVersion, k8sVersion)
}

func kubeadmInitScript(k8sVersion string) string {
	return fmt.Sprintf(`sudo kubeadm init \
  --pod-network-cidr=192.168.0.0/16 \
  --service-cidr=10.96.0.0/12 \
  --kubernetes-version=v%s.0 \
  --apiserver-cert-extra-sans=$(curl -s http://169.254.169.254/latest/meta-data/public-ipv4) \
  --upload-certs

# Setup kubeconfig for ubuntu user
mkdir -p $HOME/.kube
sudo cp -f /etc/kubernetes/admin.conf $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config

# Print join command for workers
echo "=== JOIN COMMAND ==="
kubeadm token create --print-join-command`, k8sVersion)
}

func kubeadmJoinScript() string {
	return `sudo kubeadm join <CONTROL_PLANE_PRIVATE_IP>:6443 \
  --token <JOIN_TOKEN> \
  --discovery-token-ca-cert-hash <CA_CERT_HASH>`
}

func installCNIScript(cni string) string {
	switch cni {
	case "flannel":
		return `kubectl apply -f https://raw.githubusercontent.com/flannel-io/flannel/master/Documentation/kube-flannel.yml`
	case "calico":
		fallthrough
	default:
		return `kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml`
	}
}
