package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	// shared connection flags for apply / exec / port-forward
	k8sCOpsNamespace  string
	k8sCOpsContext    string
	k8sCOpsKubeconfig string
	k8sCOpsDebug      bool

	// apply
	k8sApplyFile      string
	k8sApplyManifest  string
	k8sApplyStdin     bool
	k8sApplyServerDry bool

	// exec
	k8sExecContainer string

	// port-forward
	k8sPFAddresses string
	k8sPFKind      string
)

var k8sApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a manifest to the active cluster",
	Long: `Apply a Kubernetes manifest to the active kubeconfig context. The
manifest can be read from a file (-f), from stdin (--stdin), or supplied
inline via --manifest.

Example:
  clanker k8s apply -f ./deployment.yaml
  cat deployment.yaml | clanker k8s apply --stdin
  clanker k8s apply --manifest "$(cat deployment.yaml)" -n prod
  clanker k8s apply -f ./deploy.yaml --server-dry-run`,
	RunE: runK8sApply,
}

var k8sExecCmd = &cobra.Command{
	Use:   "exec [pod] -- [command...]",
	Short: "Execute a command in a pod (one-shot, non-interactive)",
	Long: `Execute a command in a pod. This is one-shot only (stdout/stderr is
captured and printed); interactive TTY sessions will land in the cloud
backend as a WebSocket endpoint in a follow-up phase.

Use '--' to separate the pod name from the command and its arguments.

Example:
  clanker k8s exec my-pod -- env
  clanker k8s exec my-pod -c sidecar -- ls /var/log
  clanker k8s exec my-pod -n prod -- /bin/sh -c "ps aux | head"`,
	Args: cobra.MinimumNArgs(2),
	RunE: runK8sExec,
}

var k8sPortForwardCmd = &cobra.Command{
	Use:     "port-forward [pod-or-svc] [local:remote]",
	Short:   "Forward a local port to a pod or service in the cluster",
	Aliases: []string{"pf"},
	Long: `Forward a local port to a pod or service running on the cluster.
The command blocks until you press Ctrl-C.

Example:
  clanker k8s port-forward my-pod 8080:80
  clanker k8s port-forward svc/my-svc 5432:5432 -n db
  clanker k8s port-forward my-svc 5432:5432 --kind svc -n db
  clanker k8s port-forward my-pod 0:80    # let kubectl pick the local port`,
	Args: cobra.ExactArgs(2),
	RunE: runK8sPortForward,
}

func init() {
	k8sCmd.AddCommand(k8sApplyCmd)
	k8sCmd.AddCommand(k8sExecCmd)
	k8sCmd.AddCommand(k8sPortForwardCmd)

	for _, cmd := range []*cobra.Command{k8sApplyCmd, k8sExecCmd, k8sPortForwardCmd} {
		cmd.Flags().StringVarP(&k8sCOpsNamespace, "namespace", "n", "default", "Kubernetes namespace")
		cmd.Flags().StringVar(&k8sCOpsContext, "context", "", "kubectl context to use")
		cmd.Flags().StringVar(&k8sCOpsKubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
		cmd.Flags().BoolVar(&k8sCOpsDebug, "debug", false, "Enable debug output")
	}

	k8sApplyCmd.Flags().StringVarP(&k8sApplyFile, "file", "f", "", "Path to manifest file")
	k8sApplyCmd.Flags().StringVar(&k8sApplyManifest, "manifest", "", "Inline manifest YAML")
	k8sApplyCmd.Flags().BoolVar(&k8sApplyStdin, "stdin", false, "Read manifest from stdin")
	k8sApplyCmd.Flags().BoolVar(&k8sApplyServerDry, "server-dry-run", false, "Submit server-side dry-run (kubectl apply --dry-run=server)")

	k8sExecCmd.Flags().StringVarP(&k8sExecContainer, "container", "c", "", "Container name (if pod has multiple)")
	// '-i' and '-t' are intentionally not surfaced here: this exec
	// captures stdout/stderr into a buffer (one-shot), so passing them
	// through to kubectl would break the run ('-t' errors with 'stdin
	// is not a tty'). They will land alongside the WebSocket-backed
	// interactive exec in the cloud backend.

	k8sPortForwardCmd.Flags().StringVar(&k8sPFAddresses, "address", "", "Addresses to listen on (default localhost)")
	k8sPortForwardCmd.Flags().StringVar(&k8sPFKind, "kind", "pod", "Target kind: pod, svc, or deploy (used only when the arg has no kind/ prefix)")
}

func buildK8sClusterOpsClient() *k8s.Client {
	debug := k8sCOpsDebug || viper.GetBool("debug")
	kubeconfig := k8sCOpsKubeconfig
	if kubeconfig == "" {
		kubeconfig = getKubeconfigPath()
	}
	client := k8s.NewClient(kubeconfig, k8sCOpsContext, debug)
	if k8sCOpsNamespace != "" {
		client.SetNamespace(k8sCOpsNamespace)
	}
	return client
}

// resolveApplyManifest returns the manifest text from whichever source the
// user picked (-f, --manifest, --stdin). Exactly one source must be set.
func resolveApplyManifest() (string, error) {
	picks := 0
	if k8sApplyFile != "" {
		picks++
	}
	if k8sApplyManifest != "" {
		picks++
	}
	if k8sApplyStdin {
		picks++
	}
	if picks == 0 {
		return "", fmt.Errorf("provide one of --file/-f, --manifest, or --stdin")
	}
	if picks > 1 {
		return "", fmt.Errorf("use only one of --file/-f, --manifest, or --stdin")
	}

	switch {
	case k8sApplyFile != "":
		data, err := os.ReadFile(k8sApplyFile)
		if err != nil {
			return "", fmt.Errorf("read manifest file %q: %w", k8sApplyFile, err)
		}
		return string(data), nil
	case k8sApplyManifest != "":
		return k8sApplyManifest, nil
	default:
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read manifest from stdin: %w", err)
		}
		return string(data), nil
	}
}

func runK8sApply(cmd *cobra.Command, args []string) error {
	manifest, err := resolveApplyManifest()
	if err != nil {
		return err
	}

	ctx := context.Background()
	client := buildK8sClusterOpsClient()

	var output string
	if k8sApplyServerDry {
		output, err = client.ApplyDryRunServer(ctx, manifest, k8sCOpsNamespace)
	} else {
		output, err = client.Apply(ctx, manifest, k8sCOpsNamespace)
	}
	if err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

func runK8sExec(cmd *cobra.Command, args []string) error {
	pod := args[0]
	command := args[1:]
	// Cobra strips a literal "--" between flags and positional args, but
	// some shells / older invocations still pass it through; drop it if
	// present so `clanker k8s exec my-pod -- env` and `clanker k8s exec
	// my-pod env` behave identically.
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		return fmt.Errorf("a command to exec is required after the pod name")
	}

	ctx := context.Background()
	client := buildK8sClusterOpsClient()

	kubectlArgs := []string{"exec", pod}
	if k8sExecContainer != "" {
		kubectlArgs = append(kubectlArgs, "-c", k8sExecContainer)
	}
	kubectlArgs = append(kubectlArgs, "--")
	kubectlArgs = append(kubectlArgs, command...)

	output, err := client.RunWithNamespace(ctx, k8sCOpsNamespace, kubectlArgs...)
	if err != nil {
		return fmt.Errorf("exec pod/%s failed: %w", pod, err)
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	return nil
}

// parsePortPair validates a "<local>:<remote>" port spec; returns it
// unchanged if valid, error otherwise. Empty local is allowed ("kubectl
// pick" semantic; e.g., ":80" or "0:80").
func parsePortPair(s string) (string, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("port spec %q must be <local>:<remote>", s)
	}
	if parts[1] == "" {
		return "", fmt.Errorf("port spec %q is missing the remote port", s)
	}
	return s, nil
}

// normalizePFTarget prepends the chosen kind (pod/svc/deploy) to bare names.
// If the user already passed "svc/foo" we pass it through unchanged.
func normalizePFTarget(raw, kind string) string {
	if strings.Contains(raw, "/") {
		return raw
	}
	switch strings.ToLower(kind) {
	case "svc", "service":
		return "svc/" + raw
	case "deploy", "deployment":
		return "deployment/" + raw
	default:
		return "pod/" + raw
	}
}

func runK8sPortForward(cmd *cobra.Command, args []string) error {
	target := normalizePFTarget(args[0], k8sPFKind)
	portSpec, err := parsePortPair(args[1])
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := buildK8sClusterOpsClient()

	pf, err := client.PortForwardStream(ctx, k8sCOpsNamespace, k8s.PortForwardStreamOptions{
		Target:    target,
		PortSpec:  portSpec,
		Addresses: k8sPFAddresses,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
	if err != nil {
		return fmt.Errorf("start port-forward %s %s: %w", target, portSpec, err)
	}

	// On Ctrl-C, give kubectl SIGINT so it can release the local socket
	// gracefully; only fall back to context-cancel (SIGKILL) if it
	// refuses to die within a short window. Without this kubectl gets
	// killed hard and may leave the port half-bound on rapid reconnects.
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			if pf.Process != nil {
				_ = pf.Process.Signal(syscall.SIGINT)
			}
			// Hard cancel after a grace window if kubectl is still up.
			select {
			case <-time.After(2 * time.Second):
				cancel()
			case <-done:
			}
		case <-done:
		}
	}()

	err = pf.Wait()
	close(done)
	if err != nil {
		// kubectl exits non-zero on SIGINT/SIGTERM; treat that as clean
		// shutdown so we don't paper the user with a useless error.
		if ctx.Err() != nil || isInterruptedExit(err) {
			return nil
		}
		return fmt.Errorf("port-forward exited: %w", err)
	}
	return nil
}

// isInterruptedExit returns true when the underlying *exec.ExitError was
// produced by a SIGINT/SIGTERM/SIGKILL — i.e., the user (or our cancel
// path) tore down kubectl on purpose.
func isInterruptedExit(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	if !ws.Signaled() {
		return false
	}
	switch ws.Signal() {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL:
		return true
	}
	return false
}
