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
	k8sHelmNamespace       string
	k8sHelmContext         string
	k8sHelmKubeconfig      string
	k8sHelmDebug           bool
	k8sHelmAllNamespaces   bool
	k8sHelmVersion         string
	k8sHelmValuesFiles     []string
	k8sHelmSetValues       []string
	k8sHelmCreateNamespace bool
	k8sHelmWait            bool
	k8sHelmTimeout         string
	k8sHelmDryRun          bool
	k8sHelmDescription     string
	k8sHelmReuseValues     bool
	k8sHelmResetValues     bool
	k8sHelmForce           bool
	k8sHelmInstall         bool
	k8sHelmKeepHistory     bool
	k8sHelmRollbackRev     int
	k8sHelmOutputFormat    string
)

var k8sHelmCmd = &cobra.Command{
	Use:   "helm",
	Short: "Manage Helm releases on the active cluster",
	Long: `Manage Helm releases on the active kubeconfig context: install, upgrade,
list, uninstall, status, history, rollback, and values.

These wrap the local 'helm' binary; you must have helm installed and the
kubeconfig context pointed at the target cluster (use 'clanker k8s
kubeconfig ...' if you need to bring one in first).`,
}

var k8sHelmInstallCmd = &cobra.Command{
	Use:   "install [release] [chart]",
	Short: "Install a Helm chart as a new release",
	Long: `Install a Helm chart as a new release.

Example:
  clanker k8s helm install my-nginx bitnami/nginx -n web --create-namespace
  clanker k8s helm install my-app ./chart --set image.tag=v1.2.3
  clanker k8s helm install my-app oci://registry/chart --version 1.0.0`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sHelmInstall,
}

var k8sHelmUpgradeCmd = &cobra.Command{
	Use:   "upgrade [release] [chart]",
	Short: "Upgrade an existing Helm release",
	Long: `Upgrade an existing Helm release. With --install, will install the
release if it does not already exist (helm upgrade --install).

Example:
  clanker k8s helm upgrade my-nginx bitnami/nginx --install -n web
  clanker k8s helm upgrade my-app ./chart --set image.tag=v1.2.4 --reuse-values`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sHelmUpgrade,
}

var k8sHelmListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List Helm releases",
	Aliases: []string{"ls"},
	Long: `List Helm releases in the current namespace or all namespaces.

Example:
  clanker k8s helm list
  clanker k8s helm list -A
  clanker k8s helm list -n web -o json`,
	RunE: runK8sHelmList,
}

var k8sHelmUninstallCmd = &cobra.Command{
	Use:     "uninstall [release]",
	Short:   "Uninstall a Helm release",
	Aliases: []string{"delete", "remove"},
	Long: `Uninstall a Helm release (removes all resources created by the release).

Example:
  clanker k8s helm uninstall my-nginx -n web
  clanker k8s helm uninstall my-app --keep-history`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sHelmUninstall,
}

var k8sHelmStatusCmd = &cobra.Command{
	Use:   "status [release]",
	Short: "Show the status of a Helm release",
	Long: `Show the status of a Helm release (deployed, failed, pending, etc).

Example:
  clanker k8s helm status my-nginx -n web`,
	Args: cobra.ExactArgs(1),
	RunE: runK8sHelmStatus,
}

var k8sHelmHistoryCmd = &cobra.Command{
	Use:   "history [release]",
	Short: "Show the revision history of a Helm release",
	Args:  cobra.ExactArgs(1),
	RunE:  runK8sHelmHistory,
}

var k8sHelmRollbackCmd = &cobra.Command{
	Use:   "rollback [release] [revision]",
	Short: "Roll a Helm release back to a previous revision",
	Long: `Roll a Helm release back to a previous revision (0 = previous).

Example:
  clanker k8s helm rollback my-nginx 3 -n web
  clanker k8s helm rollback my-app 0`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sHelmRollback,
}

var k8sHelmValuesCmd = &cobra.Command{
	Use:   "values [release]",
	Short: "Show the values applied to a Helm release",
	Args:  cobra.ExactArgs(1),
	RunE:  runK8sHelmValues,
}

func init() {
	k8sCmd.AddCommand(k8sHelmCmd)
	k8sHelmCmd.AddCommand(k8sHelmInstallCmd)
	k8sHelmCmd.AddCommand(k8sHelmUpgradeCmd)
	k8sHelmCmd.AddCommand(k8sHelmListCmd)
	k8sHelmCmd.AddCommand(k8sHelmUninstallCmd)
	k8sHelmCmd.AddCommand(k8sHelmStatusCmd)
	k8sHelmCmd.AddCommand(k8sHelmHistoryCmd)
	k8sHelmCmd.AddCommand(k8sHelmRollbackCmd)
	k8sHelmCmd.AddCommand(k8sHelmValuesCmd)

	// Shared connection / namespace flags on every subcommand.
	for _, cmd := range []*cobra.Command{
		k8sHelmInstallCmd, k8sHelmUpgradeCmd, k8sHelmListCmd, k8sHelmUninstallCmd,
		k8sHelmStatusCmd, k8sHelmHistoryCmd, k8sHelmRollbackCmd, k8sHelmValuesCmd,
	} {
		cmd.Flags().StringVarP(&k8sHelmNamespace, "namespace", "n", "default", "Kubernetes namespace")
		cmd.Flags().StringVar(&k8sHelmContext, "context", "", "kubectl context to use")
		cmd.Flags().StringVar(&k8sHelmKubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
		cmd.Flags().BoolVar(&k8sHelmDebug, "debug", false, "Enable debug output")
	}

	// install/upgrade shared flags
	for _, cmd := range []*cobra.Command{k8sHelmInstallCmd, k8sHelmUpgradeCmd} {
		cmd.Flags().StringVar(&k8sHelmVersion, "version", "", "Chart version to install")
		cmd.Flags().StringArrayVarP(&k8sHelmValuesFiles, "values", "f", nil, "Values file(s) to use (can be repeated)")
		cmd.Flags().StringArrayVar(&k8sHelmSetValues, "set", nil, "Set values on the command line (key=val, can be repeated)")
		cmd.Flags().BoolVar(&k8sHelmWait, "wait", false, "Wait until all resources are in ready state")
		cmd.Flags().StringVar(&k8sHelmTimeout, "timeout", "", "Time to wait for any individual k8s operation (e.g., 5m0s)")
		cmd.Flags().BoolVar(&k8sHelmDryRun, "dry-run", false, "Simulate without actually installing/upgrading")
		cmd.Flags().StringVar(&k8sHelmDescription, "description", "", "Add a custom description")
	}

	k8sHelmInstallCmd.Flags().BoolVar(&k8sHelmCreateNamespace, "create-namespace", false, "Create the namespace if it does not exist")

	k8sHelmUpgradeCmd.Flags().BoolVar(&k8sHelmReuseValues, "reuse-values", false, "Reuse values from the last release")
	k8sHelmUpgradeCmd.Flags().BoolVar(&k8sHelmResetValues, "reset-values", false, "Reset values to those from chart defaults")
	k8sHelmUpgradeCmd.Flags().BoolVar(&k8sHelmForce, "force", false, "Force resource updates through delete/recreate")
	k8sHelmUpgradeCmd.Flags().BoolVar(&k8sHelmInstall, "install", false, "Install the release if it does not already exist")

	// list flags
	k8sHelmListCmd.Flags().BoolVarP(&k8sHelmAllNamespaces, "all-namespaces", "A", false, "List releases across all namespaces")
	k8sHelmListCmd.Flags().StringVarP(&k8sHelmOutputFormat, "output", "o", "table", "Output format (table, json, yaml)")

	// uninstall flags
	k8sHelmUninstallCmd.Flags().BoolVar(&k8sHelmKeepHistory, "keep-history", false, "Retain release history")
	k8sHelmUninstallCmd.Flags().BoolVar(&k8sHelmDryRun, "dry-run", false, "Simulate without actually uninstalling")
	k8sHelmUninstallCmd.Flags().StringVar(&k8sHelmTimeout, "timeout", "", "Time to wait for any individual k8s operation")

	// status / values output format
	for _, cmd := range []*cobra.Command{k8sHelmStatusCmd, k8sHelmValuesCmd} {
		cmd.Flags().StringVarP(&k8sHelmOutputFormat, "output", "o", "", "Output format (json, yaml, table)")
	}
}

// buildK8sHelmClient returns a Client whose default namespace is aligned with
// the active --namespace flag so buildHelmArgs only injects '-n' once.
// Callers should NOT also append '-n ...' into their args.
func buildK8sHelmClient() *k8s.Client {
	debug := k8sHelmDebug || viper.GetBool("debug")
	kubeconfig := k8sHelmKubeconfig
	if kubeconfig == "" {
		kubeconfig = getKubeconfigPath()
	}
	client := k8s.NewClient(kubeconfig, k8sHelmContext, debug)
	if k8sHelmAllNamespaces {
		// 'all' tells buildHelmArgs to skip namespace injection — required
		// for 'helm list -A' which uses --all-namespaces instead.
		client.SetNamespace("all")
	} else if k8sHelmNamespace != "" {
		client.SetNamespace(k8sHelmNamespace)
	}
	return client
}

// appendIf adds flag to args when value is non-empty.
func appendIf(args []string, flag, value string) []string {
	if value == "" {
		return args
	}
	return append(args, flag, value)
}

// appendBoolIf adds the flag (no value) when v is true.
func appendBoolIf(args []string, flag string, v bool) []string {
	if !v {
		return args
	}
	return append(args, flag)
}

// runHelmAndPrint executes helm with the given args (helm subcommand is the
// first arg, e.g., "install"). It uses RunHelm so the namespace flag is
// passed via args explicitly to avoid double-injection from the client default.
func runHelmAndPrint(ctx context.Context, client *k8s.Client, args []string) error {
	output, err := client.RunHelm(ctx, args...)
	if err != nil {
		return err
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sHelmInstall(cmd *cobra.Command, args []string) error {
	release := args[0]
	chart := args[1]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"install", release, chart}
	helmArgs = appendIf(helmArgs, "--version", k8sHelmVersion)
	helmArgs = appendBoolIf(helmArgs, "--create-namespace", k8sHelmCreateNamespace)
	helmArgs = appendBoolIf(helmArgs, "--wait", k8sHelmWait)
	helmArgs = appendIf(helmArgs, "--timeout", k8sHelmTimeout)
	helmArgs = appendBoolIf(helmArgs, "--dry-run", k8sHelmDryRun)
	helmArgs = appendIf(helmArgs, "--description", k8sHelmDescription)
	for _, f := range k8sHelmValuesFiles {
		helmArgs = append(helmArgs, "-f", f)
	}
	for _, s := range k8sHelmSetValues {
		helmArgs = append(helmArgs, "--set", s)
	}

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm install %s failed: %w", release, err)
	}
	return nil
}

func runK8sHelmUpgrade(cmd *cobra.Command, args []string) error {
	release := args[0]
	chart := args[1]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"upgrade", release, chart}
	helmArgs = appendBoolIf(helmArgs, "--install", k8sHelmInstall)
	helmArgs = appendIf(helmArgs, "--version", k8sHelmVersion)
	helmArgs = appendBoolIf(helmArgs, "--wait", k8sHelmWait)
	helmArgs = appendIf(helmArgs, "--timeout", k8sHelmTimeout)
	helmArgs = appendBoolIf(helmArgs, "--dry-run", k8sHelmDryRun)
	helmArgs = appendBoolIf(helmArgs, "--reuse-values", k8sHelmReuseValues)
	helmArgs = appendBoolIf(helmArgs, "--reset-values", k8sHelmResetValues)
	helmArgs = appendBoolIf(helmArgs, "--force", k8sHelmForce)
	helmArgs = appendIf(helmArgs, "--description", k8sHelmDescription)
	for _, f := range k8sHelmValuesFiles {
		helmArgs = append(helmArgs, "-f", f)
	}
	for _, s := range k8sHelmSetValues {
		helmArgs = append(helmArgs, "--set", s)
	}

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm upgrade %s failed: %w", release, err)
	}
	return nil
}

func runK8sHelmList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"list"}
	if k8sHelmAllNamespaces {
		helmArgs = append(helmArgs, "-A")
	}
	if k8sHelmOutputFormat != "" && k8sHelmOutputFormat != "table" {
		helmArgs = append(helmArgs, "-o", k8sHelmOutputFormat)
	}

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm list failed: %w", err)
	}
	return nil
}

func runK8sHelmUninstall(cmd *cobra.Command, args []string) error {
	release := args[0]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"uninstall", release}
	helmArgs = appendBoolIf(helmArgs, "--keep-history", k8sHelmKeepHistory)
	helmArgs = appendBoolIf(helmArgs, "--dry-run", k8sHelmDryRun)
	helmArgs = appendIf(helmArgs, "--timeout", k8sHelmTimeout)

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm uninstall %s failed: %w", release, err)
	}
	return nil
}

func runK8sHelmStatus(cmd *cobra.Command, args []string) error {
	release := args[0]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"status", release}
	if k8sHelmOutputFormat != "" {
		helmArgs = append(helmArgs, "-o", k8sHelmOutputFormat)
	}

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm status %s failed: %w", release, err)
	}
	return nil
}

func runK8sHelmHistory(cmd *cobra.Command, args []string) error {
	release := args[0]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"history", release}
	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm history %s failed: %w", release, err)
	}
	return nil
}

func runK8sHelmRollback(cmd *cobra.Command, args []string) error {
	release := args[0]
	revision := args[1]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"rollback", release, revision}
	helmArgs = appendBoolIf(helmArgs, "--wait", k8sHelmWait)
	helmArgs = appendIf(helmArgs, "--timeout", k8sHelmTimeout)
	helmArgs = appendBoolIf(helmArgs, "--dry-run", k8sHelmDryRun)
	helmArgs = appendBoolIf(helmArgs, "--force", k8sHelmForce)

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm rollback %s %s failed: %w", release, revision, err)
	}
	return nil
}

func runK8sHelmValues(cmd *cobra.Command, args []string) error {
	release := args[0]
	ctx := context.Background()
	client := buildK8sHelmClient()

	helmArgs := []string{"get", "values", release}
	if k8sHelmOutputFormat != "" {
		helmArgs = append(helmArgs, "-o", k8sHelmOutputFormat)
	}

	if err := runHelmAndPrint(ctx, client, helmArgs); err != nil {
		return fmt.Errorf("helm get values %s failed: %w", release, err)
	}
	return nil
}
