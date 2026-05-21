package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	// shared connection flags for the node lifecycle subcommands
	k8sNodeContext    string
	k8sNodeKubeconfig string
	k8sNodeDebug      bool

	// drain flags (kubectl semantics)
	k8sDrainForce              bool
	k8sDrainIgnoreDaemonSets   bool
	k8sDrainDeleteEmptyDirData bool
	k8sDrainGracePeriod        int
	k8sDrainTimeout            string
	k8sDrainPodSelector        string
	k8sDrainDisableEviction    bool
)

var k8sNodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Manage node lifecycle (cordon, drain, uncordon)",
	Long: `Manage Kubernetes node lifecycle on the active kubeconfig context:
cordon a node (mark unschedulable), drain it (evict its workloads), or
uncordon it (return it to the scheduler).`,
}

var k8sNodeCordonCmd = &cobra.Command{
	Use:   "cordon [node]",
	Short: "Mark a node as unschedulable",
	Long: `Mark a node as unschedulable. New pods will not be placed on it; existing
pods are left alone (use 'drain' to evict).

Example:
  clanker k8s node cordon ip-10-0-1-23.ec2.internal`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sNodeCordon,
}

var k8sNodeUncordonCmd = &cobra.Command{
	Use:   "uncordon [node]",
	Short: "Mark a node as schedulable",
	Args:  cobra.ExactArgs(1),
	RunE:  runK8sNodeUncordon,
}

var k8sNodeDrainCmd = &cobra.Command{
	Use:   "drain [node]",
	Short: "Drain workloads from a node",
	Long: `Cordon a node and evict its pods so it can be safely removed or rebooted.
Honours the same flags as 'kubectl drain' — daemonsets are skipped by
default (use --ignore-daemonsets=false to require none exist).

Example:
  clanker k8s node drain ip-10-0-1-23.ec2.internal --ignore-daemonsets --delete-emptydir-data
  clanker k8s node drain my-node --grace-period 60 --timeout 5m`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sNodeDrain,
}

func init() {
	k8sCmd.AddCommand(k8sNodeCmd)
	k8sNodeCmd.AddCommand(k8sNodeCordonCmd)
	k8sNodeCmd.AddCommand(k8sNodeUncordonCmd)
	k8sNodeCmd.AddCommand(k8sNodeDrainCmd)

	for _, cmd := range []*cobra.Command{k8sNodeCordonCmd, k8sNodeUncordonCmd, k8sNodeDrainCmd} {
		cmd.Flags().StringVar(&k8sNodeContext, "context", "", "kubectl context to use")
		cmd.Flags().StringVar(&k8sNodeKubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
		cmd.Flags().BoolVar(&k8sNodeDebug, "debug", false, "Enable debug output")
	}

	// Drain-specific flags mirror kubectl's surface.
	k8sNodeDrainCmd.Flags().BoolVar(&k8sDrainForce, "force", false, "Force eviction of pods not managed by a controller")
	k8sNodeDrainCmd.Flags().BoolVar(&k8sDrainIgnoreDaemonSets, "ignore-daemonsets", true, "Ignore DaemonSet-managed pods")
	k8sNodeDrainCmd.Flags().BoolVar(&k8sDrainDeleteEmptyDirData, "delete-emptydir-data", false, "Allow eviction of pods using emptyDir volumes (data is lost)")
	k8sNodeDrainCmd.Flags().IntVar(&k8sDrainGracePeriod, "grace-period", -1, "Grace period in seconds (-1 = use the pod's terminationGracePeriodSeconds)")
	k8sNodeDrainCmd.Flags().StringVar(&k8sDrainTimeout, "timeout", "0s", "Time to wait before giving up (e.g., 5m0s, 0s = no timeout)")
	k8sNodeDrainCmd.Flags().StringVar(&k8sDrainPodSelector, "pod-selector", "", "Label selector to filter which pods to drain")
	k8sNodeDrainCmd.Flags().BoolVar(&k8sDrainDisableEviction, "disable-eviction", false, "Bypass the eviction API and delete pods directly")
}

func buildK8sNodeClient() *k8s.Client {
	debug := k8sNodeDebug || viper.GetBool("debug")
	kubeconfig := k8sNodeKubeconfig
	if kubeconfig == "" {
		kubeconfig = getKubeconfigPath()
	}
	// Node ops are cluster-scoped — leave the client's default namespace
	// unset so buildArgs doesn't inject a stray '-n' for `kubectl cordon`.
	client := k8s.NewClient(kubeconfig, k8sNodeContext, debug)
	client.SetNamespace("all")
	return client
}

func printAndNewline(s string) {
	fmt.Print(s)
	if !strings.HasSuffix(s, "\n") {
		fmt.Println()
	}
}

func runK8sNodeCordon(cmd *cobra.Command, args []string) error {
	node := args[0]
	ctx := context.Background()
	client := buildK8sNodeClient()

	out, err := client.Run(ctx, "cordon", node)
	if err != nil {
		return fmt.Errorf("cordon node %q failed: %w", node, err)
	}
	printAndNewline(out)
	return nil
}

func runK8sNodeUncordon(cmd *cobra.Command, args []string) error {
	node := args[0]
	ctx := context.Background()
	client := buildK8sNodeClient()

	out, err := client.Run(ctx, "uncordon", node)
	if err != nil {
		return fmt.Errorf("uncordon node %q failed: %w", node, err)
	}
	printAndNewline(out)
	return nil
}

func runK8sNodeDrain(cmd *cobra.Command, args []string) error {
	node := args[0]
	ctx := context.Background()
	client := buildK8sNodeClient()

	drainArgs := []string{"drain", node}
	if k8sDrainForce {
		drainArgs = append(drainArgs, "--force")
	}
	if k8sDrainIgnoreDaemonSets {
		drainArgs = append(drainArgs, "--ignore-daemonsets")
	}
	if k8sDrainDeleteEmptyDirData {
		drainArgs = append(drainArgs, "--delete-emptydir-data")
	}
	if k8sDrainGracePeriod >= 0 {
		drainArgs = append(drainArgs, fmt.Sprintf("--grace-period=%d", k8sDrainGracePeriod))
	}
	if k8sDrainTimeout != "" && k8sDrainTimeout != "0s" {
		drainArgs = append(drainArgs, "--timeout", k8sDrainTimeout)
	}
	if k8sDrainPodSelector != "" {
		drainArgs = append(drainArgs, "--pod-selector", k8sDrainPodSelector)
	}
	if k8sDrainDisableEviction {
		drainArgs = append(drainArgs, "--disable-eviction")
	}

	out, err := client.Run(ctx, drainArgs...)
	if err != nil {
		return fmt.Errorf("drain node %q failed: %w", node, err)
	}
	printAndNewline(out)
	return nil
}
