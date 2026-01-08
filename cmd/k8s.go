package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/cluster"
	"github.com/bgdnvk/clanker/internal/k8s/plan"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var k8sCmd = &cobra.Command{
	Use:   "k8s",
	Short: "Kubernetes cluster management",
	Long:  `Manage Kubernetes clusters (EKS, kubeadm, k3s) and workloads.`,
}

var k8sCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Kubernetes cluster",
	Long:  `Create a new Kubernetes cluster using EKS, kubeadm, or k3s.`,
}

var k8sCreateEKSCmd = &cobra.Command{
	Use:   "eks [cluster-name]",
	Short: "Create an EKS cluster",
	Long: `Create a new Amazon EKS cluster.

Example:
  clanker k8s create eks my-cluster --nodes 2 --node-type t3.small
  clanker k8s create eks my-cluster --plan  # Show plan only`,
	Args: cobra.ExactArgs(1),
	RunE: runCreateEKS,
}

var k8sCreateKubeadmCmd = &cobra.Command{
	Use:   "kubeadm [cluster-name]",
	Short: "Create a kubeadm cluster on EC2",
	Long: `Create a new kubeadm-based Kubernetes cluster on EC2 instances.

Example:
  clanker k8s create kubeadm my-cluster --workers 1 --key-pair my-key
  clanker k8s create kubeadm my-cluster --plan  # Show plan only`,
	Args: cobra.ExactArgs(1),
	RunE: runCreateKubeadm,
}

var k8sDeleteCmd = &cobra.Command{
	Use:   "delete [cluster-type] [cluster-name]",
	Short: "Delete a Kubernetes cluster",
	Long: `Delete an existing Kubernetes cluster.

Example:
  clanker k8s delete eks my-cluster
  clanker k8s delete kubeadm my-cluster`,
	Args: cobra.ExactArgs(2),
	RunE: runDeleteCluster,
}

var k8sListCmd = &cobra.Command{
	Use:   "list [cluster-type]",
	Short: "List Kubernetes clusters",
	Long: `List Kubernetes clusters of a specific type.

Example:
  clanker k8s list eks
  clanker k8s list kubeadm`,
	Args: cobra.MaximumNArgs(1),
	RunE: runListClusters,
}

var k8sDeployCmd = &cobra.Command{
	Use:   "deploy [image]",
	Short: "Deploy an application to the cluster",
	Long: `Deploy an application (container image) to the current Kubernetes cluster.

Example:
  clanker k8s deploy nginx --name my-nginx --port 80
  clanker k8s deploy nginx --plan  # Show plan only`,
	Args: cobra.ExactArgs(1),
	RunE: runDeploy,
}

var k8sGetKubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig [cluster-type] [cluster-name]",
	Short: "Get kubeconfig for a cluster",
	Long: `Retrieve and configure kubeconfig for a cluster.

Example:
  clanker k8s kubeconfig eks my-cluster
  clanker k8s kubeconfig kubeadm my-cluster`,
	Args: cobra.ExactArgs(2),
	RunE: runGetKubeconfig,
}

var k8sResourcesCmd = &cobra.Command{
	Use:   "resources",
	Short: "Get all Kubernetes resources from a cluster",
	Long: `Fetch all Kubernetes resources (nodes, pods, services, PVs, ConfigMaps) for visualization.

Example:
  clanker k8s resources --cluster my-cluster
  clanker k8s resources --cluster my-cluster --output json`,
	RunE: runGetResources,
}

// Flags
var (
	k8sNodes         int
	k8sNodeType      string
	k8sWorkers       int
	k8sKeyPair       string
	k8sSSHKeyPath    string
	k8sK8sVersion    string
	k8sPlanOnly      bool
	k8sApply         bool
	k8sDeployName    string
	k8sDeployPort    int
	k8sReplicas      int
	k8sNamespace     string
	k8sClusterName   string
	k8sOutputFormat  string
)

func init() {
	rootCmd.AddCommand(k8sCmd)

	// Add subcommands
	k8sCmd.AddCommand(k8sCreateCmd)
	k8sCmd.AddCommand(k8sDeleteCmd)
	k8sCmd.AddCommand(k8sListCmd)
	k8sCmd.AddCommand(k8sDeployCmd)
	k8sCmd.AddCommand(k8sGetKubeconfigCmd)
	k8sCmd.AddCommand(k8sResourcesCmd)

	k8sCreateCmd.AddCommand(k8sCreateEKSCmd)
	k8sCreateCmd.AddCommand(k8sCreateKubeadmCmd)

	// EKS create flags
	k8sCreateEKSCmd.Flags().IntVar(&k8sNodes, "nodes", 1, "Number of worker nodes")
	k8sCreateEKSCmd.Flags().StringVar(&k8sNodeType, "node-type", "t3.small", "EC2 instance type for nodes")
	k8sCreateEKSCmd.Flags().StringVar(&k8sK8sVersion, "version", "1.29", "Kubernetes version")
	k8sCreateEKSCmd.Flags().BoolVar(&k8sPlanOnly, "plan", false, "Show plan without applying")
	k8sCreateEKSCmd.Flags().BoolVar(&k8sApply, "apply", false, "Apply the plan (default prompts for confirmation)")

	// Kubeadm create flags
	k8sCreateKubeadmCmd.Flags().IntVar(&k8sWorkers, "workers", 1, "Number of worker nodes")
	k8sCreateKubeadmCmd.Flags().StringVar(&k8sNodeType, "node-type", "t3.small", "EC2 instance type for nodes")
	k8sCreateKubeadmCmd.Flags().StringVar(&k8sKeyPair, "key-pair", "", "AWS key pair name for SSH access (auto-creates if not exists)")
	k8sCreateKubeadmCmd.Flags().StringVar(&k8sSSHKeyPath, "ssh-key", "", "Path to SSH private key (default: ~/.ssh/<key-pair>)")
	k8sCreateKubeadmCmd.Flags().StringVar(&k8sK8sVersion, "version", "1.29", "Kubernetes version")
	k8sCreateKubeadmCmd.Flags().BoolVar(&k8sPlanOnly, "plan", false, "Show plan without applying")
	k8sCreateKubeadmCmd.Flags().BoolVar(&k8sApply, "apply", false, "Apply the plan (default prompts for confirmation)")

	// Deploy flags
	k8sDeployCmd.Flags().StringVar(&k8sDeployName, "name", "", "Deployment name (default: image name)")
	k8sDeployCmd.Flags().IntVar(&k8sDeployPort, "port", 80, "Container port to expose")
	k8sDeployCmd.Flags().IntVar(&k8sReplicas, "replicas", 1, "Number of replicas")
	k8sDeployCmd.Flags().StringVar(&k8sNamespace, "namespace", "default", "Kubernetes namespace")
	k8sDeployCmd.Flags().BoolVar(&k8sPlanOnly, "plan", false, "Show plan without applying")
	k8sDeployCmd.Flags().BoolVar(&k8sApply, "apply", false, "Apply the plan (default prompts for confirmation)")

	// Resources flags
	k8sResourcesCmd.Flags().StringVar(&k8sClusterName, "cluster", "", "Cluster name (optional, uses current context if not specified)")
	k8sResourcesCmd.Flags().StringVarP(&k8sOutputFormat, "output", "o", "json", "Output format (json or yaml)")
}

func getK8sAgent() (*k8s.Agent, string, string) {
	debug := viper.GetBool("debug")

	// Resolve AWS profile
	awsProfile := ""
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	awsProfile = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
	if awsProfile == "" {
		awsProfile = viper.GetString("aws.default_profile")
	}
	if awsProfile == "" {
		awsProfile = "default"
	}

	// Resolve region
	awsRegion := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", defaultEnv))
	if awsRegion == "" {
		awsRegion = viper.GetString("aws.default_region")
	}
	if awsRegion == "" {
		awsRegion = "us-east-1"
	}

	agent := k8s.NewAgentWithOptions(k8s.AgentOptions{
		Debug:      debug,
		AWSProfile: awsProfile,
		Region:     awsRegion,
	})

	return agent, awsProfile, awsRegion
}

func runCreateEKS(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	ctx := context.Background()
	debug := viper.GetBool("debug")

	agent, awsProfile, awsRegion := getK8sAgent()

	// Generate the plan
	k8sPlan := plan.GenerateEKSCreatePlan(plan.EKSCreateOptions{
		ClusterName:       clusterName,
		Region:            awsRegion,
		Profile:           awsProfile,
		NodeCount:         k8sNodes,
		NodeType:          k8sNodeType,
		KubernetesVersion: k8sK8sVersion,
	})

	// Display the plan
	plan.DisplayPlan(os.Stdout, k8sPlan, plan.PlanDisplayOptions{
		ShowCommands: debug,
		Verbose:      debug,
	})

	// If --plan flag, show JSON and exit
	if k8sPlanOnly {
		fmt.Println()
		fmt.Println("Plan JSON:")
		planJSON, _ := json.MarshalIndent(k8sPlan, "", "  ")
		fmt.Println(string(planJSON))
		return nil
	}

	// Confirm unless --apply
	if !k8sApply {
		fmt.Print("Do you want to create this cluster? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Println()

	// Execute using existing agent (which has streaming output)
	opts := cluster.CreateOptions{
		Name:              clusterName,
		Region:            awsRegion,
		WorkerCount:       k8sNodes,
		WorkerType:        k8sNodeType,
		KubernetesVersion: k8sK8sVersion,
	}

	info, err := agent.CreateEKSCluster(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to create EKS cluster: %w", err)
	}

	// Build result
	result := &plan.ExecResult{
		Success: true,
		Connection: &plan.Connection{
			Kubeconfig: "~/.kube/config",
			Endpoint:   info.Endpoint,
			Commands: []string{
				"kubectl get nodes",
				"kubectl get pods -A",
			},
		},
	}

	// Get kubeconfig
	kubeconfigPath, err := agent.GetEKSKubeconfig(ctx, clusterName)
	if err != nil {
		if debug {
			fmt.Printf("[k8s] warning: failed to update kubeconfig: %v\n", err)
		}
	} else {
		result.Connection.Kubeconfig = kubeconfigPath
	}

	// Display result
	plan.DisplayResult(os.Stdout, k8sPlan, result)

	return nil
}

func runCreateKubeadm(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	ctx := context.Background()
	debug := viper.GetBool("debug")

	_, awsProfile, awsRegion := getK8sAgent()

	// Default key pair name if not provided
	keyPairName := k8sKeyPair
	if keyPairName == "" {
		keyPairName = fmt.Sprintf("clanker-%s-key", clusterName)
	}

	// Ensure SSH key exists
	fmt.Println("[k8s] checking SSH key configuration...")
	sshKeyInfo, err := plan.EnsureSSHKey(ctx, keyPairName, awsRegion, awsProfile, os.Stdout)
	if err != nil {
		return fmt.Errorf("failed to ensure SSH key: %w", err)
	}

	sshKeyPath := k8sSSHKeyPath
	if sshKeyPath == "" {
		sshKeyPath = sshKeyInfo.PrivateKeyPath
	}

	// Generate the plan
	k8sPlan := plan.GenerateKubeadmCreatePlan(plan.KubeadmCreateOptions{
		ClusterName:       clusterName,
		Region:            awsRegion,
		Profile:           awsProfile,
		WorkerCount:       k8sWorkers,
		NodeType:          k8sNodeType,
		ControlPlaneType:  k8sNodeType,
		KubernetesVersion: k8sK8sVersion,
		KeyPairName:       keyPairName,
		SSHKeyPath:        sshKeyPath,
		CNI:               "calico",
	})

	// Display the plan
	plan.DisplayPlan(os.Stdout, k8sPlan, plan.PlanDisplayOptions{
		ShowCommands: debug,
		ShowSSH:      debug,
		Verbose:      debug,
	})

	// If --plan flag, show JSON and exit
	if k8sPlanOnly {
		fmt.Println()
		fmt.Println("Plan JSON:")
		planJSON, _ := json.MarshalIndent(k8sPlan, "", "  ")
		fmt.Println(string(planJSON))
		return nil
	}

	// Confirm unless --apply
	if !k8sApply {
		fmt.Print("Do you want to create this cluster? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Println()

	// Execute using existing kubeadm provider (which has streaming output)
	agent, _, _ := getK8sAgent()
	agent.RegisterKubeadmProvider(k8s.KubeadmProviderOptions{
		AWSProfile:  awsProfile,
		Region:      awsRegion,
		KeyPairName: keyPairName,
		SSHKeyPath:  sshKeyPath,
	})

	provider, ok := agent.GetClusterProvider(k8s.ClusterTypeKubeadm)
	if !ok {
		return fmt.Errorf("kubeadm provider not available")
	}

	opts := cluster.CreateOptions{
		Name:              clusterName,
		Region:            awsRegion,
		WorkerCount:       k8sWorkers,
		WorkerType:        k8sNodeType,
		ControlPlaneType:  k8sNodeType,
		KubernetesVersion: k8sK8sVersion,
	}

	info, err := provider.Create(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to create kubeadm cluster: %w", err)
	}

	// Build result
	result := &plan.ExecResult{
		Success: true,
		Connection: &plan.Connection{
			Endpoint: info.Endpoint,
			Commands: []string{
				"kubectl get nodes",
				"kubectl get pods -A",
			},
		},
	}

	// Get kubeconfig
	kubeconfigPath, err := provider.GetKubeconfig(ctx, clusterName)
	if err != nil {
		if debug {
			fmt.Printf("[k8s] warning: failed to get kubeconfig: %v\n", err)
		}
	} else {
		result.Connection.Kubeconfig = kubeconfigPath
		result.Connection.Commands = append(result.Connection.Commands,
			fmt.Sprintf("export KUBECONFIG=%s", kubeconfigPath))
	}

	// Display result
	fmt.Println()
	fmt.Println("=== Cluster Created Successfully ===")
	fmt.Println()
	fmt.Printf("Name:       %s\n", info.Name)
	fmt.Printf("Status:     %s\n", info.Status)
	fmt.Printf("Endpoint:   %s\n", info.Endpoint)
	fmt.Printf("Version:    %s\n", info.KubernetesVersion)
	fmt.Println()
	fmt.Println("Control Plane:")
	for _, node := range info.ControlPlaneNodes {
		fmt.Printf("  %s: %s (Public: %s)\n", node.Name, node.InternalIP, node.ExternalIP)
	}
	fmt.Println()
	fmt.Println("Workers:")
	for _, node := range info.WorkerNodes {
		fmt.Printf("  %s: %s (Public: %s)\n", node.Name, node.InternalIP, node.ExternalIP)
	}

	plan.DisplayConnection(os.Stdout, result.Connection)

	return nil
}

func runDeleteCluster(cmd *cobra.Command, args []string) error {
	clusterType := args[0]
	clusterName := args[1]
	ctx := context.Background()

	agent, awsProfile, awsRegion := getK8sAgent()

	// Generate delete plan
	deletePlan := plan.GenerateDeletePlan(plan.DeleteOptions{
		ClusterType: clusterType,
		ClusterName: clusterName,
		Region:      awsRegion,
		Profile:     awsProfile,
	})

	// Display the plan
	plan.DisplayPlan(os.Stdout, deletePlan, plan.PlanDisplayOptions{})

	// Confirm
	fmt.Print("Are you sure you want to delete this cluster? [y/N]: ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	fmt.Println()
	fmt.Printf("[k8s] deleting %s cluster '%s'...\n", clusterType, clusterName)

	var err error
	switch clusterType {
	case "eks":
		err = agent.DeleteEKSCluster(ctx, clusterName)
	case "kubeadm":
		agent.RegisterKubeadmProvider(k8s.KubeadmProviderOptions{
			AWSProfile: awsProfile,
			Region:     awsRegion,
		})
		provider, ok := agent.GetClusterProvider(k8s.ClusterTypeKubeadm)
		if !ok {
			return fmt.Errorf("kubeadm provider not available")
		}
		err = provider.Delete(ctx, clusterName)
	default:
		return fmt.Errorf("unsupported cluster type: %s (use 'eks' or 'kubeadm')", clusterType)
	}

	if err != nil {
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	fmt.Printf("[k8s] cluster '%s' deleted successfully.\n", clusterName)
	return nil
}

func runListClusters(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	agent, awsProfile, awsRegion := getK8sAgent()

	clusterType := "eks"
	if len(args) > 0 {
		clusterType = args[0]
	}

	var clusters []k8s.ClusterInfo
	var err error

	switch clusterType {
	case "eks":
		clusters, err = agent.ListEKSClusters(ctx)
	case "kubeadm":
		agent.RegisterKubeadmProvider(k8s.KubeadmProviderOptions{
			AWSProfile: awsProfile,
			Region:     awsRegion,
		})
		provider, ok := agent.GetClusterProvider(k8s.ClusterTypeKubeadm)
		if !ok {
			return fmt.Errorf("kubeadm provider not available")
		}
		clusters, err = provider.ListClusters(ctx)
	default:
		return fmt.Errorf("unsupported cluster type: %s (use 'eks' or 'kubeadm')", clusterType)
	}

	if err != nil {
		return fmt.Errorf("failed to list clusters: %w", err)
	}

	if len(clusters) == 0 {
		fmt.Printf("No %s clusters found.\n", clusterType)
		return nil
	}

	fmt.Printf("=== %s Clusters ===\n\n", strings.ToUpper(clusterType))
	for _, c := range clusters {
		fmt.Printf("Name:     %s\n", c.Name)
		fmt.Printf("Status:   %s\n", c.Status)
		fmt.Printf("Region:   %s\n", c.Region)
		fmt.Printf("Version:  %s\n", c.KubernetesVersion)
		fmt.Printf("Endpoint: %s\n", c.Endpoint)
		if len(c.WorkerNodes) > 0 {
			fmt.Printf("Workers:  %d\n", len(c.WorkerNodes))
		}
		fmt.Println()
	}

	return nil
}

func runDeploy(cmd *cobra.Command, args []string) error {
	image := args[0]
	ctx := context.Background()

	deployName := k8sDeployName
	if deployName == "" {
		// Extract name from image
		parts := strings.Split(image, "/")
		deployName = parts[len(parts)-1]
		if idx := strings.Index(deployName, ":"); idx > 0 {
			deployName = deployName[:idx]
		}
	}

	// Generate deploy plan
	deployPlan := plan.GenerateDeployPlan(plan.DeployOptions{
		Name:      deployName,
		Image:     image,
		Port:      k8sDeployPort,
		Replicas:  k8sReplicas,
		Namespace: k8sNamespace,
		Type:      "deployment",
	})

	// Display the plan
	plan.DisplayPlan(os.Stdout, deployPlan, plan.PlanDisplayOptions{})

	if k8sPlanOnly {
		fmt.Println()
		fmt.Println("Plan JSON:")
		planJSON, _ := json.MarshalIndent(deployPlan, "", "  ")
		fmt.Println(string(planJSON))
		return nil
	}

	// Confirm
	if !k8sApply {
		fmt.Print("Do you want to deploy this application? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Println()
	fmt.Println("[k8s] deploying application...")

	// Build deployment manifest
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: %d
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: %s
        image: %s
        ports:
        - containerPort: %d
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: %s
  ports:
  - port: %d
    targetPort: %d
  type: LoadBalancer
`, deployName, k8sNamespace, k8sReplicas, deployName, deployName, deployName, image, k8sDeployPort, deployName, k8sNamespace, deployName, k8sDeployPort, k8sDeployPort)

	// Apply using kubectl
	client := k8s.NewClient("", "", viper.GetBool("debug"))

	output, err := client.Apply(ctx, manifest, k8sNamespace)
	if err != nil {
		return fmt.Errorf("failed to deploy: %w", err)
	}

	fmt.Println(output)
	fmt.Println()
	fmt.Println("=== Deployment Successful ===")

	plan.DisplayConnection(os.Stdout, deployPlan.Connection)

	return nil
}

func runGetKubeconfig(cmd *cobra.Command, args []string) error {
	clusterType := args[0]
	clusterName := args[1]
	ctx := context.Background()

	agent, awsProfile, awsRegion := getK8sAgent()

	var kubeconfigPath string
	var err error

	switch clusterType {
	case "eks":
		kubeconfigPath, err = agent.GetEKSKubeconfig(ctx, clusterName)
	case "kubeadm":
		agent.RegisterKubeadmProvider(k8s.KubeadmProviderOptions{
			AWSProfile: awsProfile,
			Region:     awsRegion,
		})
		provider, ok := agent.GetClusterProvider(k8s.ClusterTypeKubeadm)
		if !ok {
			return fmt.Errorf("kubeadm provider not available")
		}
		kubeconfigPath, err = provider.GetKubeconfig(ctx, clusterName)
	default:
		return fmt.Errorf("unsupported cluster type: %s (use 'eks' or 'kubeadm')", clusterType)
	}

	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	fmt.Printf("Kubeconfig saved to: %s\n", kubeconfigPath)
	fmt.Println()
	fmt.Println("To use this kubeconfig:")
	fmt.Printf("  export KUBECONFIG=%s\n", kubeconfigPath)
	fmt.Println("or")
	fmt.Printf("  kubectl --kubeconfig %s get nodes\n", kubeconfigPath)

	return nil
}

func runGetResources(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	agent := k8s.NewAgent(debug)

	clusterName := k8sClusterName
	if clusterName == "" {
		clusterName = "current-context"
	}

	opts := k8s.QueryOptions{
		ClusterName: clusterName,
	}

	resources, err := agent.GetClusterResources(ctx, clusterName, opts)
	if err != nil {
		return fmt.Errorf("failed to get cluster resources: %w", err)
	}

	var output []byte
	if k8sOutputFormat == "yaml" {
		output, err = yaml.Marshal(resources)
	} else {
		output, err = json.MarshalIndent(resources, "", "  ")
	}

	if err != nil {
		return fmt.Errorf("failed to marshal resources: %w", err)
	}

	fmt.Println(string(output))
	return nil
}
