package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
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

var k8sLogsCmd = &cobra.Command{
	Use:   "logs [pod-name]",
	Short: "Get logs from a pod",
	Long: `Retrieve logs from a pod or container.

Example:
  clanker k8s logs my-pod
  clanker k8s logs my-pod -c my-container
  clanker k8s logs my-pod --tail 100
  clanker k8s logs my-pod -f`,
	Args: cobra.ExactArgs(1),
	RunE: runGetLogs,
}

var k8sStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Get resource metrics and statistics",
	Long:  `Get CPU and memory metrics for nodes, pods, and containers.`,
}

var k8sStatsNodesCmd = &cobra.Command{
	Use:   "nodes",
	Short: "Get node metrics",
	Long: `Get CPU and memory metrics for all cluster nodes.

Example:
  clanker k8s stats nodes
  clanker k8s stats nodes --sort-by cpu
  clanker k8s stats nodes -o json`,
	RunE: runStatsNodes,
}

var k8sStatsPodsCmd = &cobra.Command{
	Use:   "pods",
	Short: "Get pod metrics",
	Long: `Get CPU and memory metrics for pods.

Example:
  clanker k8s stats pods
  clanker k8s stats pods -n kube-system
  clanker k8s stats pods -A
  clanker k8s stats pods --sort-by memory`,
	RunE: runStatsPods,
}

var k8sStatsPodCmd = &cobra.Command{
	Use:   "pod [pod-name]",
	Short: "Get metrics for a specific pod",
	Long: `Get CPU and memory metrics for a specific pod and its containers.

Example:
  clanker k8s stats pod my-pod
  clanker k8s stats pod my-pod -n kube-system
  clanker k8s stats pod my-pod --containers`,
	Args: cobra.ExactArgs(1),
	RunE: runStatsPod,
}

var k8sStatsClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Get cluster-wide metrics",
	Long: `Get aggregated CPU and memory metrics for the entire cluster.

Example:
  clanker k8s stats cluster
  clanker k8s stats cluster -o json`,
	RunE: runStatsCluster,
}

var k8sAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural language questions about your Kubernetes cluster",
	Long: `Ask natural language questions about your Kubernetes cluster using AI.

The AI will analyze your question, determine what kubectl operations are needed,
execute them, and provide a comprehensive markdown-formatted response.

Conversation history is maintained per cluster for follow-up questions.

Examples:
  clanker k8s ask "how many pods are running"
  clanker k8s ask --cluster test-cluster --profile myaws "show me all deployments"
  clanker k8s ask --cluster prod "give me error logs for nginx pod"
  clanker k8s ask "which pods are using the most memory"
  clanker k8s ask "why is my pod crashing"
  clanker k8s ask "tell me the health of my cluster"`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sAsk,
}

// Flags
var (
	k8sNodes        int
	k8sNodeType     string
	k8sWorkers      int
	k8sKeyPair      string
	k8sSSHKeyPath   string
	k8sK8sVersion   string
	k8sPlanOnly     bool
	k8sApply        bool
	k8sDeployName   string
	k8sDeployPort   int
	k8sReplicas     int
	k8sNamespace    string
	k8sClusterName  string
	k8sOutputFormat string
	// Logs flags
	k8sLogContainer     string
	k8sLogFollow        bool
	k8sLogPrevious      bool
	k8sLogTail          int
	k8sLogSince         string
	k8sLogTimestamps    bool
	k8sLogAllContainers bool
	// Stats flags
	k8sStatsSortBy     string
	k8sStatsContainers bool
	k8sStatsAllNS      bool
	// Ask flags
	k8sAskCluster    string
	k8sAskProfile    string
	k8sAskKubeconfig string
	k8sAskContext    string
	k8sAskAIProfile  string
	k8sAskDebug      bool
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

	// Add logs and stats commands
	k8sCmd.AddCommand(k8sLogsCmd)
	k8sCmd.AddCommand(k8sStatsCmd)
	k8sStatsCmd.AddCommand(k8sStatsNodesCmd)
	k8sStatsCmd.AddCommand(k8sStatsPodsCmd)
	k8sStatsCmd.AddCommand(k8sStatsPodCmd)
	k8sStatsCmd.AddCommand(k8sStatsClusterCmd)

	// Logs flags
	k8sLogsCmd.Flags().StringVarP(&k8sLogContainer, "container", "c", "", "Container name")
	k8sLogsCmd.Flags().BoolVarP(&k8sLogFollow, "follow", "f", false, "Follow log output")
	k8sLogsCmd.Flags().BoolVarP(&k8sLogPrevious, "previous", "p", false, "Previous terminated container logs")
	k8sLogsCmd.Flags().IntVar(&k8sLogTail, "tail", 100, "Lines from end of logs")
	k8sLogsCmd.Flags().StringVar(&k8sLogSince, "since", "", "Show logs since duration (e.g., 1h, 30m)")
	k8sLogsCmd.Flags().BoolVar(&k8sLogTimestamps, "timestamps", false, "Include timestamps")
	k8sLogsCmd.Flags().BoolVar(&k8sLogAllContainers, "all-containers", false, "All containers in pod")
	k8sLogsCmd.Flags().StringVarP(&k8sNamespace, "namespace", "n", "default", "Namespace")

	// Stats nodes flags
	k8sStatsNodesCmd.Flags().StringVar(&k8sStatsSortBy, "sort-by", "", "Sort by (cpu or memory)")
	k8sStatsNodesCmd.Flags().StringVarP(&k8sOutputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	// Stats pods flags
	k8sStatsPodsCmd.Flags().StringVarP(&k8sNamespace, "namespace", "n", "default", "Namespace")
	k8sStatsPodsCmd.Flags().BoolVarP(&k8sStatsAllNS, "all-namespaces", "A", false, "All namespaces")
	k8sStatsPodsCmd.Flags().StringVar(&k8sStatsSortBy, "sort-by", "", "Sort by (cpu or memory)")
	k8sStatsPodsCmd.Flags().StringVarP(&k8sOutputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	// Stats pod flags
	k8sStatsPodCmd.Flags().StringVarP(&k8sNamespace, "namespace", "n", "default", "Namespace")
	k8sStatsPodCmd.Flags().BoolVar(&k8sStatsContainers, "containers", false, "Show container metrics")
	k8sStatsPodCmd.Flags().StringVarP(&k8sOutputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	// Stats cluster flags
	k8sStatsClusterCmd.Flags().StringVarP(&k8sOutputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	// Add ask command
	k8sCmd.AddCommand(k8sAskCmd)

	// Ask command flags
	k8sAskCmd.Flags().StringVar(&k8sAskCluster, "cluster", "", "Kubernetes cluster name (EKS cluster name)")
	k8sAskCmd.Flags().StringVar(&k8sAskProfile, "profile", "", "AWS profile for EKS clusters")
	k8sAskCmd.Flags().StringVar(&k8sAskKubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
	k8sAskCmd.Flags().StringVar(&k8sAskContext, "context", "", "kubectl context to use (overrides --cluster)")
	k8sAskCmd.Flags().StringVarP(&k8sNamespace, "namespace", "n", "", "Default namespace for queries (default: all namespaces)")
	k8sAskCmd.Flags().StringVar(&k8sAskAIProfile, "ai-profile", "", "AI profile to use for LLM queries")
	k8sAskCmd.Flags().BoolVar(&k8sAskDebug, "debug", false, "Enable debug output")
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

	agent, awsProfile, awsRegion := getK8sAgent()

	// If cluster name is specified, get resources for that cluster only
	if k8sClusterName != "" {
		// First verify the cluster exists
		clusterExists, err := verifyEKSClusterExists(ctx, k8sClusterName, awsProfile, awsRegion)
		if err != nil {
			return fmt.Errorf("failed to verify cluster: %w", err)
		}
		if !clusterExists {
			return fmt.Errorf("EKS cluster '%s' not found in region %s", k8sClusterName, awsRegion)
		}

		// Validate and fix kubeconfig if needed
		if err := ensureValidKubeconfig(ctx, k8sClusterName, awsProfile, awsRegion, debug); err != nil {
			return fmt.Errorf("failed to configure kubeconfig: %w", err)
		}

		// Create fresh agent after kubeconfig update
		agent = k8s.NewAgent(debug)

		opts := k8s.QueryOptions{
			ClusterName: k8sClusterName,
		}

		resources, err := agent.GetClusterResources(ctx, k8sClusterName, opts)
		if err != nil {
			return fmt.Errorf("failed to get cluster resources: %w", err)
		}

		// Validate we got actual data
		if len(resources.Nodes) == 0 && len(resources.Pods) == 0 {
			// Try to fix kubeconfig and retry
			if debug {
				fmt.Fprintf(os.Stderr, "[k8s] no resources found, attempting kubeconfig refresh...\n")
			}
			if err := forceUpdateKubeconfig(ctx, k8sClusterName, awsProfile, awsRegion); err != nil {
				return fmt.Errorf("failed to refresh kubeconfig: %w", err)
			}

			// Retry with fresh agent
			agent = k8s.NewAgent(debug)
			resources, err = agent.GetClusterResources(ctx, k8sClusterName, opts)
			if err != nil {
				return fmt.Errorf("failed to get cluster resources after kubeconfig refresh: %w", err)
			}

			if len(resources.Nodes) == 0 && len(resources.Pods) == 0 {
				return fmt.Errorf("unable to fetch resources from cluster '%s' - check cluster status and permissions", k8sClusterName)
			}
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

	// No cluster specified - get resources from all EKS clusters
	clusters, err := agent.ListEKSClusters(ctx)
	if err != nil {
		return fmt.Errorf("failed to list EKS clusters: %w", err)
	}

	if len(clusters) == 0 {
		return fmt.Errorf("no EKS clusters found")
	}

	// Backup kubeconfig and save original context before multi-cluster operations
	backupPath, err := backupKubeconfig(debug)
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] warning: failed to backup kubeconfig: %v\n", err)
		}
	}

	originalContext := getCurrentContext(ctx)
	if debug && originalContext != "" {
		fmt.Fprintf(os.Stderr, "[k8s] saved original context: %s\n", originalContext)
	}

	multiResources := k8s.MultiClusterResources{
		Clusters: make([]k8s.ClusterResources, 0, len(clusters)),
	}

	for _, cluster := range clusters {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] switching to cluster: %s\n", cluster.Name)
		}

		// Update kubeconfig and switch context for this cluster
		if err := switchToCluster(ctx, cluster.Name, awsProfile, awsRegion, debug); err != nil {
			if debug {
				fmt.Fprintf(os.Stderr, "[k8s] warning: failed to switch to cluster %s: %v\n", cluster.Name, err)
			}
			continue
		}

		// Verify the context switch was successful
		if !verifyContextSwitch(ctx, cluster.Name, debug) {
			if debug {
				fmt.Fprintf(os.Stderr, "[k8s] warning: context switch verification failed for %s\n", cluster.Name)
			}
			continue
		}

		opts := k8s.QueryOptions{
			ClusterName: cluster.Name,
		}

		// Create a new agent with fresh client for this cluster
		clusterAgent := k8s.NewAgent(debug)
		resources, err := clusterAgent.GetClusterResources(ctx, cluster.Name, opts)
		if err != nil {
			if debug {
				fmt.Fprintf(os.Stderr, "[k8s] warning: failed to get resources for %s: %v\n", cluster.Name, err)
			}
			continue
		}

		// Add cluster metadata
		resources.Region = cluster.Region
		resources.Status = cluster.Status

		multiResources.Clusters = append(multiResources.Clusters, *resources)

		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] successfully fetched resources from %s (%d nodes, %d pods)\n",
				cluster.Name, len(resources.Nodes), len(resources.Pods))
		}
	}

	// Restore original context if we had one
	if originalContext != "" {
		if err := restoreContext(ctx, originalContext, debug); err != nil {
			if debug {
				fmt.Fprintf(os.Stderr, "[k8s] warning: failed to restore original context: %v\n", err)
			}
		}
	}

	// Log backup location
	if backupPath != "" && debug {
		fmt.Fprintf(os.Stderr, "[k8s] kubeconfig backup saved at: %s\n", backupPath)
	}

	var output []byte
	if k8sOutputFormat == "yaml" {
		output, err = yaml.Marshal(multiResources)
	} else {
		output, err = json.MarshalIndent(multiResources, "", "  ")
	}

	if err != nil {
		return fmt.Errorf("failed to marshal resources: %w", err)
	}

	fmt.Println(string(output))
	return nil
}

// verifyEKSClusterExists checks if an EKS cluster exists in the specified region
func verifyEKSClusterExists(ctx context.Context, clusterName, awsProfile, awsRegion string) (bool, error) {
	cmd := exec.CommandContext(ctx, "aws", "eks", "describe-cluster",
		"--name", clusterName,
		"--profile", awsProfile,
		"--region", awsRegion,
		"--query", "cluster.status",
		"--output", "text")

	output, err := cmd.Output()
	if err != nil {
		// Check if it's a "not found" error
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "ResourceNotFoundException") ||
				strings.Contains(stderr, "not found") {
				return false, nil
			}
		}
		return false, fmt.Errorf("failed to describe cluster: %w", err)
	}

	status := strings.TrimSpace(string(output))
	return status != "", nil
}

// ensureValidKubeconfig validates the kubeconfig and updates it if needed
func ensureValidKubeconfig(ctx context.Context, clusterName, awsProfile, awsRegion string, debug bool) error {
	// Check if current context points to the right cluster
	contextValid := checkKubeconfigContext(ctx, clusterName, debug)

	if !contextValid {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] kubeconfig context invalid or missing, updating...\n")
		}
		// Backup before making changes
		if backupPath, err := backupKubeconfig(debug); err == nil && backupPath != "" && debug {
			fmt.Fprintf(os.Stderr, "[k8s] kubeconfig backed up to: %s\n", backupPath)
		}
		return forceUpdateKubeconfig(ctx, clusterName, awsProfile, awsRegion)
	}

	// Verify kubectl can actually connect
	if !verifyKubectlConnection(ctx, debug) {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] kubectl connection failed, refreshing kubeconfig...\n")
		}
		// Backup before making changes
		if backupPath, err := backupKubeconfig(debug); err == nil && backupPath != "" && debug {
			fmt.Fprintf(os.Stderr, "[k8s] kubeconfig backed up to: %s\n", backupPath)
		}
		return forceUpdateKubeconfig(ctx, clusterName, awsProfile, awsRegion)
	}

	return nil
}

// checkKubeconfigContext checks if the current kubeconfig context is valid for the cluster
func checkKubeconfigContext(ctx context.Context, clusterName string, debug bool) bool {
	// Get current context
	cmd := exec.CommandContext(ctx, "kubectl", "config", "current-context")
	output, err := cmd.Output()
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] no current context set: %v\n", err)
		}
		return false
	}

	currentContext := strings.TrimSpace(string(output))

	// Check if context contains the cluster name (EKS contexts usually contain the cluster name)
	if !strings.Contains(currentContext, clusterName) {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] current context '%s' does not match cluster '%s'\n", currentContext, clusterName)
		}
		return false
	}

	return true
}

// verifyKubectlConnection verifies that kubectl can connect to the cluster
func verifyKubectlConnection(ctx context.Context, debug bool) bool {
	// Try a simple cluster-info command with timeout
	cmd := exec.CommandContext(ctx, "kubectl", "cluster-info", "--request-timeout=5s")
	err := cmd.Run()
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] kubectl connection test failed: %v\n", err)
		}
		return false
	}
	return true
}

// forceUpdateKubeconfig forces an update of the kubeconfig for the specified cluster
func forceUpdateKubeconfig(ctx context.Context, clusterName, awsProfile, awsRegion string) error {
	cmd := exec.CommandContext(ctx, "aws", "eks", "update-kubeconfig",
		"--name", clusterName,
		"--profile", awsProfile,
		"--region", awsRegion)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update kubeconfig: %w - %s", err, string(output))
	}

	return nil
}

// backupKubeconfig creates a backup of the current kubeconfig file
func backupKubeconfig(debug bool) (string, error) {
	kubeconfigPath := getKubeconfigPath()
	if kubeconfigPath == "" {
		return "", fmt.Errorf("could not determine kubeconfig path")
	}

	// Check if kubeconfig exists
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] no existing kubeconfig to backup\n")
		}
		return "", nil
	}

	// Create backup directory
	backupDir := filepath.Join(filepath.Dir(kubeconfigPath), "kubeconfig-backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Generate backup filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	backupPath := filepath.Join(backupDir, fmt.Sprintf("config.backup.%s", timestamp))

	// Read original file
	content, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	// Write backup
	if err := os.WriteFile(backupPath, content, 0600); err != nil {
		return "", fmt.Errorf("failed to write backup: %w", err)
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] created kubeconfig backup: %s\n", backupPath)
	}

	return backupPath, nil
}

// getKubeconfigPath returns the path to the kubeconfig file
func getKubeconfigPath() string {
	// Check KUBECONFIG env var first
	if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
		// If multiple paths, use the first one
		paths := strings.Split(kubeconfigEnv, string(os.PathListSeparator))
		if len(paths) > 0 {
			return paths[0]
		}
	}

	// Default to ~/.kube/config
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".kube", "config")
}

// getCurrentContext returns the current kubectl context name
func getCurrentContext(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "kubectl", "config", "current-context")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// switchToCluster updates kubeconfig and switches context to the specified cluster
func switchToCluster(ctx context.Context, clusterName, awsProfile, awsRegion string, debug bool) error {
	// First, update kubeconfig for this cluster (this also sets the context)
	cmd := exec.CommandContext(ctx, "aws", "eks", "update-kubeconfig",
		"--name", clusterName,
		"--profile", awsProfile,
		"--region", awsRegion)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update kubeconfig: %w - %s", err, string(output))
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] updated kubeconfig for cluster: %s\n", clusterName)
	}

	return nil
}

// verifyContextSwitch verifies that the current context is pointing to the expected cluster
func verifyContextSwitch(ctx context.Context, clusterName string, debug bool) bool {
	currentContext := getCurrentContext(ctx)
	if currentContext == "" {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] no context set after switch\n")
		}
		return false
	}

	// EKS contexts typically contain the cluster name
	if !strings.Contains(currentContext, clusterName) {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] context '%s' does not contain cluster name '%s'\n", currentContext, clusterName)
		}
		return false
	}

	// Quick connection test
	testCmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "--request-timeout=10s", "-o", "name")
	if err := testCmd.Run(); err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[k8s] connection test failed for %s: %v\n", clusterName, err)
		}
		return false
	}

	return true
}

// restoreContext restores the kubectl context to the specified context name
func restoreContext(ctx context.Context, contextName string, debug bool) error {
	cmd := exec.CommandContext(ctx, "kubectl", "config", "use-context", contextName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restore context: %w - %s", err, string(output))
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] restored original context: %s\n", contextName)
	}

	return nil
}

// runGetLogs retrieves logs from a pod
func runGetLogs(cmd *cobra.Command, args []string) error {
	podName := args[0]
	ctx := context.Background()
	debug := viper.GetBool("debug")

	// Build kubectl logs command
	kubectlArgs := []string{"logs", podName, "-n", k8sNamespace}

	if k8sLogContainer != "" {
		kubectlArgs = append(kubectlArgs, "-c", k8sLogContainer)
	}
	if k8sLogFollow {
		kubectlArgs = append(kubectlArgs, "-f")
	}
	if k8sLogPrevious {
		kubectlArgs = append(kubectlArgs, "-p")
	}
	if k8sLogTail > 0 {
		kubectlArgs = append(kubectlArgs, "--tail", fmt.Sprintf("%d", k8sLogTail))
	}
	if k8sLogSince != "" {
		kubectlArgs = append(kubectlArgs, "--since", k8sLogSince)
	}
	if k8sLogTimestamps {
		kubectlArgs = append(kubectlArgs, "--timestamps")
	}
	if k8sLogAllContainers {
		kubectlArgs = append(kubectlArgs, "--all-containers")
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] executing: kubectl %s\n", strings.Join(kubectlArgs, " "))
	}

	kubectlCmd := exec.CommandContext(ctx, "kubectl", kubectlArgs...)
	kubectlCmd.Stdout = os.Stdout
	kubectlCmd.Stderr = os.Stderr

	return kubectlCmd.Run()
}

// runStatsNodes gets node metrics
func runStatsNodes(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] getting node metrics\n")
	}

	// Run kubectl top nodes
	kubectlArgs := []string{"top", "nodes"}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] executing: kubectl %s\n", strings.Join(kubectlArgs, " "))
	}

	kubectlCmd := exec.CommandContext(ctx, "kubectl", kubectlArgs...)
	output, err := kubectlCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get node metrics: %w\n%s", err, string(output))
	}

	if k8sOutputFormat == "json" || k8sOutputFormat == "yaml" {
		// Parse output and convert to structured format
		metrics := parseNodeMetricsOutput(string(output))
		var formatted []byte
		if k8sOutputFormat == "yaml" {
			formatted, err = yaml.Marshal(metrics)
		} else {
			formatted, err = json.MarshalIndent(metrics, "", "  ")
		}
		if err != nil {
			return fmt.Errorf("failed to format output: %w", err)
		}
		fmt.Println(string(formatted))
	} else {
		// Table output
		fmt.Print(string(output))
	}

	return nil
}

// runStatsPods gets pod metrics
func runStatsPods(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	kubectlArgs := []string{"top", "pods"}

	if k8sStatsAllNS {
		kubectlArgs = append(kubectlArgs, "--all-namespaces")
	} else {
		kubectlArgs = append(kubectlArgs, "-n", k8sNamespace)
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] executing: kubectl %s\n", strings.Join(kubectlArgs, " "))
	}

	kubectlCmd := exec.CommandContext(ctx, "kubectl", kubectlArgs...)
	output, err := kubectlCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get pod metrics: %w\n%s", err, string(output))
	}

	if k8sOutputFormat == "json" || k8sOutputFormat == "yaml" {
		// Parse output and convert to structured format
		metrics := parsePodMetricsOutput(string(output), k8sStatsAllNS)
		var formatted []byte
		if k8sOutputFormat == "yaml" {
			formatted, err = yaml.Marshal(metrics)
		} else {
			formatted, err = json.MarshalIndent(metrics, "", "  ")
		}
		if err != nil {
			return fmt.Errorf("failed to format output: %w", err)
		}
		fmt.Println(string(formatted))
	} else {
		// Table output
		fmt.Print(string(output))
	}

	return nil
}

// runStatsPod gets metrics for a specific pod
func runStatsPod(cmd *cobra.Command, args []string) error {
	podName := args[0]
	ctx := context.Background()
	debug := viper.GetBool("debug")

	kubectlArgs := []string{"top", "pod", podName, "-n", k8sNamespace}

	if k8sStatsContainers {
		kubectlArgs = append(kubectlArgs, "--containers")
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] executing: kubectl %s\n", strings.Join(kubectlArgs, " "))
	}

	kubectlCmd := exec.CommandContext(ctx, "kubectl", kubectlArgs...)
	output, err := kubectlCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get pod metrics: %w\n%s", err, string(output))
	}

	if k8sOutputFormat == "json" || k8sOutputFormat == "yaml" {
		var metrics interface{}
		if k8sStatsContainers {
			metrics = parseContainerMetricsOutput(string(output))
		} else {
			pods := parsePodMetricsOutput(string(output), false)
			if len(pods) > 0 {
				metrics = pods[0]
			}
		}
		var formatted []byte
		if k8sOutputFormat == "yaml" {
			formatted, err = yaml.Marshal(metrics)
		} else {
			formatted, err = json.MarshalIndent(metrics, "", "  ")
		}
		if err != nil {
			return fmt.Errorf("failed to format output: %w", err)
		}
		fmt.Println(string(formatted))
	} else {
		// Table output
		fmt.Print(string(output))
	}

	return nil
}

// runStatsCluster gets cluster-wide metrics
func runStatsCluster(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	if debug {
		fmt.Fprintf(os.Stderr, "[k8s] getting cluster metrics\n")
	}

	// Get node metrics
	nodeCmd := exec.CommandContext(ctx, "kubectl", "top", "nodes", "--no-headers")
	nodeOutput, err := nodeCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get node metrics: %w", err)
	}

	nodes := parseNodeMetricsOutput(string(nodeOutput))

	// Calculate cluster totals
	clusterMetrics := map[string]interface{}{
		"nodeCount":     len(nodes),
		"nodes":         nodes,
		"totalCPU":      "0m",
		"totalMemory":   "0Mi",
		"avgCPUPercent": 0.0,
		"avgMemPercent": 0.0,
	}

	var totalCPUPct, totalMemPct float64
	for _, node := range nodes {
		totalCPUPct += node["cpuPercent"].(float64)
		totalMemPct += node["memPercent"].(float64)
	}

	if len(nodes) > 0 {
		clusterMetrics["avgCPUPercent"] = totalCPUPct / float64(len(nodes))
		clusterMetrics["avgMemPercent"] = totalMemPct / float64(len(nodes))
	}

	if k8sOutputFormat == "json" || k8sOutputFormat == "yaml" {
		var formatted []byte
		if k8sOutputFormat == "yaml" {
			formatted, err = yaml.Marshal(clusterMetrics)
		} else {
			formatted, err = json.MarshalIndent(clusterMetrics, "", "  ")
		}
		if err != nil {
			return fmt.Errorf("failed to format output: %w", err)
		}
		fmt.Println(string(formatted))
	} else {
		// Table output
		fmt.Printf("Cluster Metrics\n")
		fmt.Printf("===============\n")
		fmt.Printf("Nodes: %d\n", len(nodes))
		fmt.Printf("Avg CPU Usage: %.1f%%\n", clusterMetrics["avgCPUPercent"])
		fmt.Printf("Avg Memory Usage: %.1f%%\n", clusterMetrics["avgMemPercent"])
		fmt.Println()
		fmt.Printf("%-30s %-12s %-8s %-12s %-8s\n", "NODE", "CPU", "CPU%", "MEMORY", "MEM%")
		for _, node := range nodes {
			cpuPct := fmt.Sprintf("%.1f%%", node["cpuPercent"])
			memPct := fmt.Sprintf("%.1f%%", node["memPercent"])
			fmt.Printf("%-30s %-12s %-8s %-12s %-8s\n",
				node["name"], node["cpu"], cpuPct,
				node["memory"], memPct)
		}
	}

	return nil
}

// parseNodeMetricsOutput parses kubectl top nodes output
func parseNodeMetricsOutput(output string) []map[string]interface{} {
	var nodes []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		cpuPct := strings.TrimSuffix(fields[2], "%")
		cpuPctFloat := 0.0
		fmt.Sscanf(cpuPct, "%f", &cpuPctFloat)

		memPct := strings.TrimSuffix(fields[4], "%")
		memPctFloat := 0.0
		fmt.Sscanf(memPct, "%f", &memPctFloat)

		nodes = append(nodes, map[string]interface{}{
			"name":       fields[0],
			"cpu":        fields[1],
			"cpuPercent": cpuPctFloat,
			"memory":     fields[3],
			"memPercent": memPctFloat,
		})
	}

	return nodes
}

// parsePodMetricsOutput parses kubectl top pods output
func parsePodMetricsOutput(output string, allNamespaces bool) []map[string]interface{} {
	var pods []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "NAMESPACE") {
			continue
		}

		fields := strings.Fields(line)

		var pod map[string]interface{}
		if allNamespaces && len(fields) >= 4 {
			pod = map[string]interface{}{
				"namespace": fields[0],
				"name":      fields[1],
				"cpu":       fields[2],
				"memory":    fields[3],
			}
		} else if len(fields) >= 3 {
			pod = map[string]interface{}{
				"name":   fields[0],
				"cpu":    fields[1],
				"memory": fields[2],
			}
		} else {
			continue
		}

		pods = append(pods, pod)
	}

	return pods
}

// parseContainerMetricsOutput parses kubectl top pods --containers output
func parseContainerMetricsOutput(output string) []map[string]interface{} {
	var containers []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "POD") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 4 {
			containers = append(containers, map[string]interface{}{
				"pod":       fields[0],
				"container": fields[1],
				"cpu":       fields[2],
				"memory":    fields[3],
			})
		}
	}

	return containers
}

// runK8sAsk implements the k8s ask command using a three-stage LLM pipeline
func runK8sAsk(cmd *cobra.Command, args []string) error {
	question := args[0]
	ctx := context.Background()

	// Get debug flag (from command or viper)
	debug := k8sAskDebug || viper.GetBool("debug")

	if debug {
		fmt.Println("[k8s ask] Starting LLM-powered query pipeline...")
	}

	// Resolve AWS profile
	awsProfile := k8sAskProfile
	if awsProfile == "" {
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
	}

	// Resolve AWS region
	awsRegion := ""
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	awsRegion = viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", defaultEnv))
	if awsRegion == "" {
		awsRegion = viper.GetString("aws.default_region")
	}
	if awsRegion == "" {
		awsRegion = "us-east-1"
	}

	// If cluster is specified, update kubeconfig for EKS
	if k8sAskCluster != "" && k8sAskContext == "" {
		if debug {
			fmt.Printf("[k8s ask] Updating kubeconfig for EKS cluster: %s\n", k8sAskCluster)
		}
		if err := updateKubeconfigForEKS(ctx, k8sAskCluster, awsProfile, awsRegion, debug); err != nil {
			return fmt.Errorf("failed to update kubeconfig for cluster %s: %w", k8sAskCluster, err)
		}
	}

	// Create K8s client
	k8sClient := k8s.NewClient(k8sAskKubeconfig, k8sAskContext, debug)
	if k8sNamespace != "" {
		k8sClient.SetNamespace(k8sNamespace)
	}

	// Verify cluster connection
	if err := k8sClient.CheckConnection(ctx); err != nil {
		return fmt.Errorf("cannot connect to Kubernetes cluster: %w\nTry running: aws eks update-kubeconfig --name <cluster-name> --profile %s", err, awsProfile)
	}

	// Determine cluster name for conversation history
	clusterName := k8sAskCluster
	if clusterName == "" {
		currentCtx, err := k8sClient.GetCurrentContext(ctx)
		if err == nil {
			clusterName = currentCtx
		} else {
			clusterName = "default"
		}
	}

	if debug {
		fmt.Printf("[k8s ask] Using cluster context: %s\n", clusterName)
	}

	// Load conversation history
	history := k8s.NewConversationHistory(clusterName)
	if err := history.Load(); err != nil && debug {
		fmt.Printf("[k8s ask] Warning: could not load conversation history: %v\n", err)
	}

	// Gather cluster status for context
	if debug {
		fmt.Println("[k8s ask] Gathering cluster status...")
	}
	clusterStatus, err := k8s.GatherClusterStatus(ctx, k8sClient)
	if err != nil && debug {
		fmt.Printf("[k8s ask] Warning: could not gather cluster status: %v\n", err)
	}
	if clusterStatus != nil {
		history.UpdateClusterStatus(clusterStatus)
	}

	// Create AI client
	aiClient, err := createAIClient(debug)
	if err != nil {
		return fmt.Errorf("failed to create AI client: %w", err)
	}

	// Stage 1: LLM analyzes query and determines K8s operations
	if debug {
		fmt.Println("[k8s ask] Stage 1: Analyzing query with LLM...")
	}

	clusterContext := history.GetClusterStatusContext()
	conversationContext := history.GetRecentContext(5)

	analysisPrompt := k8s.GetLLMAnalysisPrompt(question, clusterContext)
	analysisResponse, err := aiClient.AskPrompt(ctx, analysisPrompt)
	if err != nil {
		return fmt.Errorf("failed to analyze query: %w", err)
	}

	// Parse the analysis response
	var analysis k8s.K8sAnalysis
	cleanedResponse := aiClient.CleanJSONResponse(analysisResponse)
	if err := json.Unmarshal([]byte(cleanedResponse), &analysis); err != nil {
		if debug {
			fmt.Printf("[k8s ask] Warning: Failed to parse LLM analysis, raw response:\n%s\n", analysisResponse)
		}
		// Continue with empty operations, the LLM might give a direct response
		analysis = k8s.K8sAnalysis{
			Operations: []k8s.K8sOperation{},
			Analysis:   "Could not parse LLM response",
		}
	}

	if debug {
		fmt.Printf("[k8s ask] Stage 1 complete: %d operations identified\n", len(analysis.Operations))
		for i, op := range analysis.Operations {
			fmt.Printf("  %d. %s - %s\n", i+1, op.Operation, op.Reason)
		}
	}

	// Stage 2: Execute K8s operations
	if debug {
		fmt.Println("[k8s ask] Stage 2: Executing K8s operations...")
	}

	var k8sResults string
	if len(analysis.Operations) > 0 {
		k8sResults, err = k8sClient.ExecuteOperations(ctx, analysis.Operations)
		if err != nil && debug {
			fmt.Printf("[k8s ask] Warning: Some operations failed: %v\n", err)
		}
	}

	if debug {
		fmt.Printf("[k8s ask] Stage 2 complete: %d chars of results\n", len(k8sResults))
	}

	// Stage 3: Build final context and get LLM response
	if debug {
		fmt.Println("[k8s ask] Stage 3: Generating final response...")
	}

	var finalContext strings.Builder
	finalContext.WriteString(clusterContext)
	finalContext.WriteString("\n\n")
	if k8sResults != "" {
		finalContext.WriteString("Kubernetes Data:\n")
		finalContext.WriteString(k8sResults)
	}

	finalPrompt := k8s.GetFinalResponsePrompt(question, finalContext.String(), conversationContext)
	response, err := aiClient.AskPrompt(ctx, finalPrompt)
	if err != nil {
		return fmt.Errorf("failed to generate response: %w", err)
	}

	// Save conversation history
	history.AddEntry(question, response, clusterName)
	if err := history.Save(); err != nil && debug {
		fmt.Printf("[k8s ask] Warning: could not save conversation history: %v\n", err)
	}

	// Output the response
	fmt.Println(response)
	return nil
}

// updateKubeconfigForEKS updates kubeconfig for an EKS cluster
func updateKubeconfigForEKS(ctx context.Context, clusterName, awsProfile, awsRegion string, debug bool) error {
	args := []string{
		"eks", "update-kubeconfig",
		"--name", clusterName,
		"--profile", awsProfile,
		"--region", awsRegion,
	}

	if debug {
		fmt.Printf("[k8s ask] Running: aws %s\n", strings.Join(args, " "))
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aws eks update-kubeconfig failed: %w\nOutput: %s", err, string(output))
	}

	if debug {
		fmt.Printf("[k8s ask] Kubeconfig updated: %s\n", strings.TrimSpace(string(output)))
	}

	return nil
}

// createAIClient creates an AI client based on configuration
func createAIClient(debug bool) (*ai.Client, error) {
	// Resolve AI provider
	provider := k8sAskAIProfile
	if provider == "" {
		provider = viper.GetString("ai.default_provider")
	}
	if provider == "" {
		provider = "bedrock"
	}

	// Resolve API key based on provider
	var apiKey string
	switch provider {
	case "bedrock", "claude":
		// Bedrock uses AWS credentials, no API key needed
		apiKey = ""
	case "gemini":
		// Gemini ADC, no API key needed
		apiKey = ""
	case "gemini-api":
		apiKey = viper.GetString("ai.providers.gemini-api.api_key")
		if apiKey == "" {
			if envName := viper.GetString("ai.providers.gemini-api.api_key_env"); envName != "" {
				apiKey = os.Getenv(envName)
			}
		}
		if apiKey == "" {
			apiKey = os.Getenv("GEMINI_API_KEY")
		}
	case "openai":
		apiKey = viper.GetString("ai.providers.openai.api_key")
		if apiKey == "" {
			if envName := viper.GetString("ai.providers.openai.api_key_env"); envName != "" {
				apiKey = os.Getenv(envName)
			}
		}
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
	case "anthropic":
		apiKey = viper.GetString("ai.providers.anthropic.api_key")
		if apiKey == "" {
			if envName := viper.GetString("ai.providers.anthropic.api_key_env"); envName != "" {
				apiKey = os.Getenv(envName)
			}
		}
	}

	if debug {
		fmt.Printf("[k8s ask] Using AI provider: %s\n", provider)
	}

	return ai.NewClient(provider, apiKey, debug, k8sAskAIProfile), nil
}
