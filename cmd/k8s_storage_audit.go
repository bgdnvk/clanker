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
	"github.com/bgdnvk/clanker/internal/k8s/storage"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	storageAuditOutput     string
	storageAuditKubeconfig string
	storageAuditContext    string
)

var k8sStorageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Inspect Kubernetes storage posture",
	Long:  `Inspect or audit PersistentVolume / PersistentVolumeClaim health and waste.`,
}

var k8sStorageAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Detect orphaned, Released, and stuck PVCs/PVs",
	Long: `Surface storage waste and configuration issues:

  • PVCs stuck Pending (no PV bound — provisioner missing or wrong class)
  • PVCs in Lost state (underlying PV deleted, data gone)
  • PVCs Bound but not referenced by any pod (orphaned spend)
  • PVs in Released state with Retain reclaim policy (manual cleanup needed)
  • PVs in Available state never bound (verify intent)
  • PVs in Failed state (provisioner unhealthy)

Read-only — only kubectl get is invoked.

Examples:
  clanker k8s storage audit
  clanker k8s storage audit -o json
  clanker k8s storage audit --kubeconfig ~/.kube/prod`,
	RunE: runStorageAudit,
}

func init() {
	k8sCmd.AddCommand(k8sStorageCmd)
	k8sStorageCmd.AddCommand(k8sStorageAuditCmd)

	k8sStorageCmd.PersistentFlags().StringVarP(&storageAuditOutput, "output", "o", "table", "Output format (table, json)")
	k8sStorageCmd.PersistentFlags().StringVar(&storageAuditKubeconfig, "kubeconfig", "", "Path to kubeconfig (default: ~/.kube/config)")
	k8sStorageCmd.PersistentFlags().StringVar(&storageAuditContext, "context", "", "kubectl context to use")
}

func runStorageAudit(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(storageAuditKubeconfig, storageAuditContext, debug)
	auditor := storage.NewAuditor(k8s.NewStorageAdapter(client), debug)

	report, err := auditor.Audit(ctx)
	if err != nil {
		return fmt.Errorf("storage audit failed: %w", err)
	}

	switch strings.ToLower(storageAuditOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printStorageAuditReport(os.Stdout, report)
		return nil
	}
}

func printStorageAuditReport(out io.Writer, report *storage.AuditReport) {
	if report == nil {
		fmt.Fprintln(out, "No audit report.")
		return
	}

	fmt.Fprintf(out, "Scanned %d PV(s), %d PVC(s), %d pod(s)\n", report.PVsScanned, report.PVCsScanned, report.PodsScanned)
	fmt.Fprintf(out, "Waste signals: %d orphaned PVC(s), %d Pending PVC(s), %d unbound PV(s)\n\n",
		report.OrphanedPVCs, report.PendingPVCs, report.OrphanedPVs)

	if len(report.Findings) == 0 {
		fmt.Fprintln(out, "No storage issues detected. ✓")
		return
	}

	fmt.Fprintf(out, "%d finding(s):\n", len(report.Findings))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tNAMESPACE/NAME\tCAPACITY\tISSUE")
	fmt.Fprintln(w, "----\t--------------\t--------\t-----")
	for _, f := range report.Findings {
		ns := f.Namespace
		if ns == "" {
			ns = "-"
		}
		cap := f.Capacity
		if cap == "" {
			cap = "-"
		}
		fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\n",
			strings.ToUpper(f.Kind), ns, f.Name, cap, truncate(f.Issue, 60),
		)
	}
	w.Flush()
}
