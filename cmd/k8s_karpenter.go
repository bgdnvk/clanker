package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/bgdnvk/clanker/internal/k8s/sre"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	karpenterOutput     string
	karpenterKubeconfig string
	karpenterContext    string
)

var k8sKarpenterCmd = &cobra.Command{
	Use:   "karpenter",
	Short: "Inspect Karpenter NodePools and NodeClaims",
	Long: `Detect whether Karpenter is installed and list the NodePools and
NodeClaims it manages. Useful for spotting pools with no provisioned nodes,
mixing weights, or NodeClaims still in Pending state.

All operations are read-only.`,
}

var k8sKarpenterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Karpenter NodePools and NodeClaims",
	Long: `Detect Karpenter and dump the NodePool / NodeClaim inventory.

Examples:
  clanker k8s karpenter list
  clanker k8s karpenter list -o json
  clanker k8s karpenter list --kubeconfig ~/.kube/prod`,
	RunE: runKarpenterList,
}

func init() {
	k8sCmd.AddCommand(k8sKarpenterCmd)
	k8sKarpenterCmd.AddCommand(k8sKarpenterListCmd)

	k8sKarpenterCmd.PersistentFlags().StringVarP(&karpenterOutput, "output", "o", "table", "Output format (table, json)")
	k8sKarpenterCmd.PersistentFlags().StringVar(&karpenterKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sKarpenterCmd.PersistentFlags().StringVar(&karpenterContext, "context", "", "kubectl context to use")
}

type karpenterReport struct {
	Presence   *sre.KarpenterPresence `json:"presence"`
	NodePools  []sre.NodePoolSummary  `json:"nodePools,omitempty"`
	NodeClaims []sre.NodeClaimSummary `json:"nodeClaims,omitempty"`
}

func runKarpenterList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(karpenterKubeconfig, karpenterContext, debug)
	detector := sre.NewKarpenterDetector(k8s.NewSREAdapter(client), debug)

	presence, err := detector.Detect(ctx)
	if err != nil {
		return fmt.Errorf("karpenter detection failed: %w", err)
	}

	report := karpenterReport{Presence: presence}

	if presence.Installed {
		if presence.NodePoolsAvailable {
			pools, err := detector.ListNodePools(ctx)
			if err != nil {
				return fmt.Errorf("list NodePools: %w", err)
			}
			report.NodePools = pools
		}
		if presence.NodeClaimsAvailable {
			claims, err := detector.ListNodeClaims(ctx)
			if err != nil {
				return fmt.Errorf("list NodeClaims: %w", err)
			}
			report.NodeClaims = claims
		}
	}

	switch strings.ToLower(karpenterOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printKarpenterReport(os.Stdout, report)
		return nil
	}
}

func printKarpenterReport(out io.Writer, report karpenterReport) {
	if report.Presence == nil || !report.Presence.Installed {
		fmt.Fprintln(out, "Karpenter is not installed in this cluster.")
		if report.Presence != nil && report.Presence.Notes != "" {
			fmt.Fprintf(out, "Note: %s\n", report.Presence.Notes)
		}
		return
	}

	fmt.Fprintf(out, "Karpenter detected (api group: %s)\n", report.Presence.APIGroup)
	fmt.Fprintf(out, "  NodePools CRD: %s   NodeClaims CRD: %s\n",
		yesNoAvailable(report.Presence.NodePoolsAvailable),
		yesNoAvailable(report.Presence.NodeClaimsAvailable),
	)

	// NodePools table
	if len(report.NodePools) == 0 {
		fmt.Fprintln(out, "\nNo NodePools defined.")
	} else {
		// Stable display order: name asc.
		sort.SliceStable(report.NodePools, func(i, j int) bool {
			return report.NodePools[i].Name < report.NodePools[j].Name
		})
		fmt.Fprintf(out, "\nNodePools (%d):\n", len(report.NodePools))
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNODECLASS\tWEIGHT\tDISRUPTION\tAGE")
		fmt.Fprintln(w, "----\t---------\t------\t----------\t---")
		for _, p := range report.NodePools {
			disruption := p.Disruption
			if disruption == "" {
				disruption = "-"
			}
			age := p.Age
			if age == "" {
				age = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
				p.Name,
				orDash(p.NodeClass),
				p.Weight,
				disruption,
				age,
			)
		}
		w.Flush()
	}

	// NodeClaims table
	if len(report.NodeClaims) == 0 {
		fmt.Fprintln(out, "\nNo NodeClaims provisioned.")
	} else {
		sort.SliceStable(report.NodeClaims, func(i, j int) bool {
			if report.NodeClaims[i].NodePool != report.NodeClaims[j].NodePool {
				return report.NodeClaims[i].NodePool < report.NodeClaims[j].NodePool
			}
			return report.NodeClaims[i].Name < report.NodeClaims[j].Name
		})
		var pending int
		for _, c := range report.NodeClaims {
			if c.Status != "Ready" {
				pending++
			}
		}
		fmt.Fprintf(out, "\nNodeClaims (%d, %d not Ready):\n", len(report.NodeClaims), pending)
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNODEPOOL\tNODE\tSTATUS\tINSTANCE")
		fmt.Fprintln(w, "----\t--------\t----\t------\t--------")
		for _, c := range report.NodeClaims {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				c.Name,
				orDash(c.NodePool),
				orDash(c.NodeName),
				c.Status,
				orDash(c.InstanceID),
			)
		}
		w.Flush()
	}
}

func yesNoAvailable(b bool) string {
	if b {
		return "yes"
	}
	return "missing"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
