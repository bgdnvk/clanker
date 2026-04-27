package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/networking"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	npAuditNamespaces string
	npAuditOutput     string
	npAuditKubeconfig string
	npAuditContext    string
)

var k8sNetworkPolicyCmd = &cobra.Command{
	Use:   "networkpolicy",
	Short: "Inspect Kubernetes NetworkPolicy posture",
	Long:  `Inspect or audit NetworkPolicy resources in the current cluster.`,
}

var k8sNetworkPolicyAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Audit default-deny NetworkPolicy coverage per namespace",
	Long: `Report whether each namespace has default-deny NetworkPolicies in place
for ingress and egress traffic.

A namespace is reported as default-deny in a direction when at least one
NetworkPolicy targets all pods (empty podSelector) with that direction in
its policyTypes and no rules in that direction.

Examples:
  clanker k8s networkpolicy audit
  clanker k8s networkpolicy audit --namespaces default,kube-system
  clanker k8s networkpolicy audit -o json`,
	RunE: runNetworkPolicyAudit,
}

func init() {
	k8sCmd.AddCommand(k8sNetworkPolicyCmd)
	k8sNetworkPolicyCmd.AddCommand(k8sNetworkPolicyAuditCmd)

	k8sNetworkPolicyAuditCmd.Flags().StringVar(&npAuditNamespaces, "namespaces", "", "Comma-separated namespaces to audit (default: all namespaces)")
	k8sNetworkPolicyAuditCmd.Flags().StringVarP(&npAuditOutput, "output", "o", "table", "Output format (table, json)")
	k8sNetworkPolicyAuditCmd.Flags().StringVar(&npAuditKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sNetworkPolicyAuditCmd.Flags().StringVar(&npAuditContext, "context", "", "kubectl context to use")
}

func runNetworkPolicyAudit(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(npAuditKubeconfig, npAuditContext, debug)
	manager := networking.NewNetworkPolicyManager(k8s.NewNetworkingAdapter(client), debug)

	var ns []string
	if strings.TrimSpace(npAuditNamespaces) != "" {
		for _, n := range strings.Split(npAuditNamespaces, ",") {
			if t := strings.TrimSpace(n); t != "" {
				ns = append(ns, t)
			}
		}
	}

	report, err := manager.AuditPolicies(ctx, ns)
	if err != nil {
		return fmt.Errorf("audit failed: %w", err)
	}

	switch strings.ToLower(npAuditOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printNetworkPolicyAuditTable(os.Stdout, report)
		return nil
	}
}

func printNetworkPolicyAuditTable(out io.Writer, report *networking.PolicyAuditReport) {
	if report == nil || len(report.Namespaces) == 0 {
		fmt.Fprintln(out, "No namespaces audited.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tPOLICIES\tDEFAULT-DENY INGRESS\tDEFAULT-DENY EGRESS")
	fmt.Fprintln(w, "---------\t--------\t--------------------\t-------------------")
	for _, ns := range report.Namespaces {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", ns.Namespace, ns.PolicyCount, yesNo(ns.DefaultDenyIn), yesNo(ns.DefaultDenyOut))
	}
	w.Flush()

	var uncovered []string
	for _, ns := range report.Namespaces {
		if !ns.DefaultDenyIn || !ns.DefaultDenyOut {
			uncovered = append(uncovered, ns.Namespace)
		}
	}
	if len(uncovered) > 0 {
		fmt.Fprintf(out, "\n⚠  %d namespace(s) lack default-deny in at least one direction: %s\n", len(uncovered), strings.Join(uncovered, ", "))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
