package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/sre"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Shared flags for the fix subcommand tree.
var (
	k8sFixNamespace string
	k8sFixName      string
	k8sFixContext   string
	k8sFixCluster   string
	k8sFixApprove   string // "step" (default) | "auto" | "plan-only"
	k8sFixJSON      bool
	k8sFixDebug     bool
)

var k8sFixCmd = &cobra.Command{
	Use:   "fix [playbook-id]",
	Short: "Run a remediation playbook against the active cluster",
	Long: `Run a curated remediation playbook. Each playbook emits an ordered
set of diagnostic + mutation steps with per-step approval gates.

Use 'clanker k8s fix' with no arguments to list the available
playbooks. Use '--approve plan-only' to inspect the plan without
running any step. Use '--approve auto' to skip per-step prompts
EXCEPT for steps the playbook marks as requiring explicit approval.

Examples:
  clanker k8s fix
  clanker k8s fix crashloop-recovery --name my-pod -n prod
  clanker k8s fix crashloop-recovery --name my-pod -n prod --approve plan-only
  clanker k8s fix crashloop-recovery --name my-pod --approve auto`,
	RunE: runK8sFix,
}

func init() {
	k8sCmd.AddCommand(k8sFixCmd)

	k8sFixCmd.Flags().StringVarP(&k8sFixNamespace, "namespace", "n", "default", "Kubernetes namespace")
	k8sFixCmd.Flags().StringVar(&k8sFixName, "name", "", "Target resource name (required by most playbooks)")
	k8sFixCmd.Flags().StringVar(&k8sFixContext, "context", "", "kubectl context (defaults to active context)")
	k8sFixCmd.Flags().StringVar(&k8sFixCluster, "cluster", "", "Cluster name (informational; used in audit log)")
	k8sFixCmd.Flags().StringVar(&k8sFixApprove, "approve", "step",
		"Approval mode: step (prompt per step) | auto (skip prompts except for forced-approval steps) | plan-only (print plan, don't run)")
	k8sFixCmd.Flags().BoolVar(&k8sFixJSON, "json", false, "Emit the playbook plan as JSON (forces --approve plan-only)")
	k8sFixCmd.Flags().BoolVar(&k8sFixDebug, "debug", false, "Enable debug output")
}

func runK8sFix(cmd *cobra.Command, args []string) error {
	registry := sre.NewPlaybookRegistry()

	if len(args) == 0 {
		return listPlaybooks(os.Stdout, registry)
	}
	if len(args) > 1 {
		return fmt.Errorf("expected 0 or 1 playbook ID, got %d", len(args))
	}

	id := strings.TrimSpace(args[0])
	playbook, err := registry.Get(id)
	if err != nil {
		return err
	}

	debug := k8sFixDebug || viper.GetBool("debug")
	kubeconfig := getKubeconfigPath()
	client := newSREClient(kubeconfig, k8sFixContext, debug)

	plan, err := playbook.Plan(context.Background(), client, sre.PlaybookInput{
		Namespace: k8sFixNamespace,
		Name:      k8sFixName,
		Context:   k8sFixContext,
		Cluster:   k8sFixCluster,
	})
	if err != nil {
		return fmt.Errorf("plan failed: %w", err)
	}

	if k8sFixJSON {
		out, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	approve := strings.ToLower(strings.TrimSpace(k8sFixApprove))
	switch approve {
	case "step", "auto", "plan-only":
	default:
		return fmt.Errorf("--approve must be one of: step, auto, plan-only (got %q)", k8sFixApprove)
	}

	printPlaybookPlan(os.Stdout, plan)

	if approve == "plan-only" {
		return nil
	}
	if len(plan.Steps) == 0 {
		fmt.Println("Nothing to do.")
		return nil
	}

	return executePlaybookPlan(context.Background(), plan, approve)
}

// listPlaybooks prints a table of registered playbooks for the user
// who ran 'clanker k8s fix' with no arguments.
func listPlaybooks(out *os.File, registry *sre.PlaybookRegistry) error {
	playbooks := registry.All()
	if len(playbooks) == 0 {
		fmt.Fprintln(out, "No playbooks registered.")
		return nil
	}
	fmt.Fprintln(out, "Available playbooks:")
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tDESCRIPTION")
	fmt.Fprintln(w, "──\t─────\t───────────")
	for _, p := range playbooks {
		fmt.Fprintf(w, "%s\t%s\t%s\n", p.ID(), p.Title(), p.Description())
	}
	w.Flush()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run 'clanker k8s fix <id>' to start a playbook.")
	return nil
}

// printPlaybookPlan renders the plan as a human-readable summary so
// the user can review before approving. Mirrors the layout the
// frontend's K8sPlaybookRunner will use (Phase 4e).
func printPlaybookPlan(out *os.File, plan *sre.PlaybookPlan) {
	fmt.Fprintf(out, "Playbook: %s\n", plan.Title)
	if plan.Target != "" {
		fmt.Fprintf(out, "Target:   %s\n", plan.Target)
	}
	fmt.Fprintf(out, "Summary:  %s\n", plan.Summary)
	if len(plan.Notes) > 0 {
		fmt.Fprintln(out)
		for _, n := range plan.Notes {
			fmt.Fprintf(out, "Note: %s\n", n)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Steps (%d):\n", len(plan.Steps))
	for i, s := range plan.Steps {
		marker := "•"
		if s.Mutating {
			marker = "✎"
		}
		fmt.Fprintf(out, "  %d. %s  %s\n", i+1, marker, s.Description)
		if s.Reason != "" {
			fmt.Fprintf(out, "       reason: %s\n", s.Reason)
		}
		fmt.Fprintf(out, "       run:    %s %s\n", s.Command, strings.Join(s.Args, " "))
		if s.Risk != "" {
			fmt.Fprintf(out, "       risk:   %s\n", s.Risk)
		}
		if s.RequiresApproval {
			fmt.Fprintln(out, "       (requires explicit approval even in auto mode)")
		}
	}
	fmt.Fprintln(out)
}

// executePlaybookPlan runs the steps in order, honouring the approval
// mode. Returns on the first step error so the user can investigate
// without the rest of the plan running on a broken state.
func executePlaybookPlan(ctx context.Context, plan *sre.PlaybookPlan, approve string) error {
	reader := bufio.NewReader(os.Stdin)
	for i, step := range plan.Steps {
		needApproval := approve == "step" || step.RequiresApproval
		if needApproval {
			fmt.Printf("Step %d/%d: %s\n", i+1, len(plan.Steps), step.Description)
			fmt.Printf("  $ %s %s\n", step.Command, strings.Join(step.Args, " "))
			fmt.Print("Approve? [y/N/q]: ")
			line, _ := reader.ReadString('\n')
			ans := strings.ToLower(strings.TrimSpace(line))
			switch ans {
			case "y", "yes":
				// continue
			case "q", "quit":
				return fmt.Errorf("playbook aborted by user at step %d", i+1)
			default:
				fmt.Println("  skipped.")
				continue
			}
		} else {
			fmt.Printf("Step %d/%d: %s\n", i+1, len(plan.Steps), step.Description)
		}

		output, err := runPlaybookStep(ctx, step)
		fmt.Print(output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Println()
		}
		if err != nil {
			return fmt.Errorf("step %d (%s) failed: %w", i+1, step.ID, err)
		}
	}
	return nil
}

// runPlaybookStep invokes the step's command + args via os/exec. We
// shell out directly (not through clanker's k8s.Client) because each
// playbook step already carries the fully-rendered argv — including
// the --namespace / --context flags — so we don't need to re-thread
// kubeconfig.
func runPlaybookStep(ctx context.Context, step sre.PlaybookStep) (string, error) {
	cmd := exec.CommandContext(ctx, step.Command, step.Args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// newSREClient builds an sre.K8sClient backed by the existing kubectl
// shellout, via the same NewSREAdapter the other SRE commands use
// (autoscaler, health, hpa-validate, karpenter, workloads).
func newSREClient(kubeconfig, kubectlContext string, debug bool) sre.K8sClient {
	return k8s.NewSREAdapter(k8s.NewClient(kubeconfig, kubectlContext, debug))
}
