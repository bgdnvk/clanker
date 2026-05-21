package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/workloads"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// scalableKinds lists the kubectl kinds that accept `kubectl scale`. Keep this
// minimal: every entry here must be safe to pass straight through to
// (*workloads.K8sClient).Scale without translation.
var scalableKinds = map[string]string{
	"deployment":   "deployment",
	"deployments":  "deployment",
	"deploy":       "deployment",
	"statefulset":  "statefulset",
	"statefulsets": "statefulset",
	"sts":          "statefulset",
	"replicaset":   "replicaset",
	"replicasets":  "replicaset",
	"rs":           "replicaset",
}

// restartableKinds lists the kinds that accept `kubectl rollout restart`.
var restartableKinds = map[string]string{
	"deployment":  "deployment",
	"deployments": "deployment",
	"deploy":      "deployment",
	"statefulset": "statefulset",
	"sts":         "statefulset",
	"daemonset":   "daemonset",
	"daemonsets":  "daemonset",
	"ds":          "daemonset",
}

// rolloutKinds lists kinds that work with `kubectl rollout {status,undo,history}`.
var rolloutKinds = map[string]string{
	"deployment":  "deployment",
	"deployments": "deployment",
	"deploy":      "deployment",
	"statefulset": "statefulset",
	"sts":         "statefulset",
	"daemonset":   "daemonset",
	"ds":          "daemonset",
}

var (
	k8sWorkloadReplicas   int
	k8sWorkloadNamespace  string
	k8sWorkloadContext    string
	k8sWorkloadKubeconfig string
	k8sWorkloadDebug      bool
	k8sRolloutRevision    int
	k8sRmIgnoreNotFound   bool
	k8sRmForce            bool
	k8sRmGracePeriod      int
)

var k8sScaleCmd = &cobra.Command{
	Use:   "scale [kind] [name]",
	Short: "Scale a workload to a target replica count",
	Long: `Scale a deployment, statefulset, or replicaset to the requested replica count
on the active kubeconfig context.

Example:
  clanker k8s scale deployment my-app --replicas 5
  clanker k8s scale statefulset web --replicas 3 -n prod`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sScale,
}

var k8sRestartCmd = &cobra.Command{
	Use:   "restart [kind] [name]",
	Short: "Trigger a rolling restart of a workload",
	Long: `Trigger a rolling restart of a deployment, statefulset, or daemonset.

Example:
  clanker k8s restart deployment my-app
  clanker k8s restart sts web -n prod`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sRestart,
}

var k8sRolloutCmd = &cobra.Command{
	Use:   "rollout",
	Short: "Inspect or roll back workload rollouts",
	Long:  `Inspect rollout status / history, undo a rollout, pause or resume one.`,
}

var k8sRolloutStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show rollout status for a deployment (default kind)",
	Long: `Show rollout status for a deployment, statefulset, or daemonset.

Example:
  clanker k8s rollout status my-app
  clanker k8s rollout status web --kind statefulset -n prod`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sRolloutStatus,
}

var k8sRolloutUndoCmd = &cobra.Command{
	Use:   "undo [name]",
	Short: "Roll back a deployment to the previous revision",
	Long: `Roll back a deployment to the previous (or a specific) revision.

Example:
  clanker k8s rollout undo my-app
  clanker k8s rollout undo my-app --to-revision 3 -n prod`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sRolloutUndo,
}

var k8sRolloutHistoryCmd = &cobra.Command{
	Use:   "history [name]",
	Short: "Show the rollout history of a deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runK8sRolloutHistory,
}

var k8sRmCmd = &cobra.Command{
	Use:   "rm [kind] [name]",
	Short: "Delete a Kubernetes resource on the active context",
	Long: `Delete a Kubernetes resource (pod, deployment, service, etc.) on the
active kubeconfig context.

The cluster-lifecycle 'clanker k8s delete <type> <name>' command is unchanged
and is used for tearing down whole clusters (EKS/GKE/AKS/kubeadm). Use 'rm'
for in-cluster resources.

Example:
  clanker k8s rm pod my-pod
  clanker k8s rm deployment my-app -n prod
  clanker k8s rm svc my-svc --grace-period 0 --force`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sRm,
}

// rolloutKindFlag controls the rollout subcommand kind when the user doesn't
// pass `<kind>` (we default to deployment to match kubectl behaviour).
var k8sRolloutKind string

func init() {
	k8sCmd.AddCommand(k8sScaleCmd)
	k8sCmd.AddCommand(k8sRestartCmd)
	k8sCmd.AddCommand(k8sRolloutCmd)
	k8sCmd.AddCommand(k8sRmCmd)

	k8sRolloutCmd.AddCommand(k8sRolloutStatusCmd)
	k8sRolloutCmd.AddCommand(k8sRolloutUndoCmd)
	k8sRolloutCmd.AddCommand(k8sRolloutHistoryCmd)

	// Shared connection flags. We don't reuse k8sAsk* because those mark
	// 'namespace' with shorthand 'n' already on k8sLogsCmd; here we want a
	// dedicated set so flags from other subcommands don't bleed in. The
	// EKS/GKE/AKS --cluster auto-kubeconfig flow lives on `k8s ask` only
	// for now; here you point at the cluster via --context (or the
	// active kubeconfig).
	for _, cmd := range []*cobra.Command{k8sScaleCmd, k8sRestartCmd, k8sRolloutStatusCmd, k8sRolloutUndoCmd, k8sRolloutHistoryCmd, k8sRmCmd} {
		cmd.Flags().StringVarP(&k8sWorkloadNamespace, "namespace", "n", "default", "Kubernetes namespace")
		cmd.Flags().StringVar(&k8sWorkloadContext, "context", "", "kubectl context to use (defaults to active context)")
		cmd.Flags().StringVar(&k8sWorkloadKubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
		cmd.Flags().BoolVar(&k8sWorkloadDebug, "debug", false, "Enable debug output")
	}

	k8sScaleCmd.Flags().IntVar(&k8sWorkloadReplicas, "replicas", 0, "Target replica count (required)")
	_ = k8sScaleCmd.MarkFlagRequired("replicas")

	k8sRolloutUndoCmd.Flags().IntVar(&k8sRolloutRevision, "to-revision", 0, "Revision to roll back to (0 = previous)")
	k8sRolloutStatusCmd.Flags().StringVar(&k8sRolloutKind, "kind", "deployment", "Workload kind (deployment, statefulset, daemonset)")
	k8sRolloutUndoCmd.Flags().StringVar(&k8sRolloutKind, "kind", "deployment", "Workload kind (deployment, statefulset, daemonset)")
	k8sRolloutHistoryCmd.Flags().StringVar(&k8sRolloutKind, "kind", "deployment", "Workload kind (deployment, statefulset, daemonset)")

	k8sRmCmd.Flags().BoolVar(&k8sRmIgnoreNotFound, "ignore-not-found", false, "Treat missing resources as success")
	k8sRmCmd.Flags().BoolVar(&k8sRmForce, "force", false, "Force immediate deletion (pass --force to kubectl)")
	k8sRmCmd.Flags().IntVar(&k8sRmGracePeriod, "grace-period", -1, "Grace period in seconds (-1 = use kubectl default)")
}

// buildK8sWorkloadClient constructs a kubectl-backed client honouring the
// shared workload flags. It does not perform a connectivity check — callers
// should run k8s.Client.CheckConnection if they want eager validation.
func buildK8sWorkloadClient() *k8s.Client {
	debug := k8sWorkloadDebug || viper.GetBool("debug")
	kubeconfig := k8sWorkloadKubeconfig
	if kubeconfig == "" {
		kubeconfig = getKubeconfigPath()
	}
	client := k8s.NewClient(kubeconfig, k8sWorkloadContext, debug)
	if k8sWorkloadNamespace != "" {
		client.SetNamespace(k8sWorkloadNamespace)
	}
	return client
}

// resolveKindFromTable maps a user-supplied kind (singular, plural, or
// short alias) to the canonical singular form, or returns an error if the
// kind isn't in the table.
func resolveKindFromTable(table map[string]string, raw, op string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	canonical, ok := table[key]
	if !ok {
		valid := make([]string, 0, len(table))
		seen := make(map[string]struct{})
		for _, v := range table {
			if _, already := seen[v]; already {
				continue
			}
			seen[v] = struct{}{}
			valid = append(valid, v)
		}
		return "", fmt.Errorf("unsupported kind %q for %s; supported: %s", raw, op, strings.Join(valid, ", "))
	}
	return canonical, nil
}

func runK8sScale(cmd *cobra.Command, args []string) error {
	kind, err := resolveKindFromTable(scalableKinds, args[0], "scale")
	if err != nil {
		return err
	}
	name := args[1]
	if k8sWorkloadReplicas < 0 {
		return fmt.Errorf("--replicas must be >= 0, got %d", k8sWorkloadReplicas)
	}

	ctx := context.Background()
	client := buildK8sWorkloadClient()

	output, err := client.Scale(ctx, kind, name, k8sWorkloadNamespace, k8sWorkloadReplicas)
	if err != nil {
		return fmt.Errorf("scale %s/%s failed: %w", kind, name, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sRestart(cmd *cobra.Command, args []string) error {
	kind, err := resolveKindFromTable(restartableKinds, args[0], "restart")
	if err != nil {
		return err
	}
	name := args[1]

	ctx := context.Background()
	client := buildK8sWorkloadClient()

	// Dispatch on the resolved kind. We used to delegate to
	// DeploymentManager.RolloutRestart and fall back on error, but
	// RolloutRestart hardcodes 'deployment' internally — when a
	// Deployment AND a StatefulSet share a name, the deployment call
	// silently succeeded against the wrong target.
	output, err := client.Rollout(ctx, "restart", kind, name, k8sWorkloadNamespace)
	if err != nil {
		return fmt.Errorf("restart %s/%s failed: %w", kind, name, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sRolloutStatus(cmd *cobra.Command, args []string) error {
	kind, err := resolveKindFromTable(rolloutKinds, k8sRolloutKind, "rollout status")
	if err != nil {
		return err
	}
	name := args[0]

	ctx := context.Background()
	client := buildK8sWorkloadClient()

	output, err := client.Rollout(ctx, "status", kind, name, k8sWorkloadNamespace)
	if err != nil {
		return fmt.Errorf("rollout status %s/%s failed: %w", kind, name, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sRolloutUndo(cmd *cobra.Command, args []string) error {
	kind, err := resolveKindFromTable(rolloutKinds, k8sRolloutKind, "rollout undo")
	if err != nil {
		return err
	}
	name := args[0]

	ctx := context.Background()
	client := buildK8sWorkloadClient()

	// Use DeploymentManager.RolloutUndo for deployments so we get the
	// --to-revision plumbing; for statefulset/daemonset call kubectl directly.
	if kind == "deployment" {
		mgr := workloads.NewDeploymentManager(k8s.NewWorkloadsAdapter(client), k8sWorkloadDebug)
		output, err := mgr.RolloutUndo(ctx, name, k8sWorkloadNamespace, k8sRolloutRevision)
		if err != nil {
			return fmt.Errorf("rollout undo deployment/%s failed: %w", name, err)
		}
		fmt.Print(output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Println()
		}
		return nil
	}

	rolloutArgs := []string{"rollout", "undo", kind, name}
	if k8sRolloutRevision > 0 {
		rolloutArgs = append(rolloutArgs, "--to-revision", fmt.Sprintf("%d", k8sRolloutRevision))
	}
	output, err := client.RunWithNamespace(ctx, k8sWorkloadNamespace, rolloutArgs...)
	if err != nil {
		return fmt.Errorf("rollout undo %s/%s failed: %w", kind, name, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sRolloutHistory(cmd *cobra.Command, args []string) error {
	kind, err := resolveKindFromTable(rolloutKinds, k8sRolloutKind, "rollout history")
	if err != nil {
		return err
	}
	name := args[0]

	ctx := context.Background()
	client := buildK8sWorkloadClient()

	output, err := client.Rollout(ctx, "history", kind, name, k8sWorkloadNamespace)
	if err != nil {
		return fmt.Errorf("rollout history %s/%s failed: %w", kind, name, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sRm(cmd *cobra.Command, args []string) error {
	kind := strings.TrimSpace(args[0])
	name := strings.TrimSpace(args[1])
	if kind == "" || name == "" {
		return fmt.Errorf("kind and name are required")
	}

	ctx := context.Background()
	client := buildK8sWorkloadClient()

	deleteArgs := []string{"delete", kind, name}
	if k8sRmIgnoreNotFound {
		deleteArgs = append(deleteArgs, "--ignore-not-found")
	}
	if k8sRmForce {
		deleteArgs = append(deleteArgs, "--force")
	}
	if k8sRmGracePeriod >= 0 {
		deleteArgs = append(deleteArgs, fmt.Sprintf("--grace-period=%d", k8sRmGracePeriod))
	}

	output, err := client.RunWithNamespace(ctx, k8sWorkloadNamespace, deleteArgs...)
	if err != nil {
		return fmt.Errorf("delete %s/%s failed: %w", kind, name, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}
