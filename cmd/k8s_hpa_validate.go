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

var hpaValidateSeverity string

var k8sAutoscalerValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Lint HorizontalPodAutoscalers + KEDA ScaledObjects for config smells",
	Long: `Inspect every HPA and KEDA ScaledObject in the cluster and surface
configuration issues that would otherwise show up only at scale-out time:

  • minReplicas absent / minReplicas > maxReplicas / minReplicas == maxReplicas
  • metrics block missing or malformed
  • scaleTargetRef.name empty
  • KEDA: idleReplicaCount >= minReplicaCount, missing trigger types,
    aggressively-low polling intervals

Read-only — no kubectl mutations.

Examples:
  clanker k8s autoscaler validate
  clanker k8s autoscaler validate --severity warning
  clanker k8s autoscaler validate -o json`,
	RunE: runHPAValidate,
}

func init() {
	k8sAutoscalerCmd.AddCommand(k8sAutoscalerValidateCmd)
	// --output / --kubeconfig / --context come from k8sAutoscalerCmd's
	// PersistentFlags. Only --severity is local to validate.
	k8sAutoscalerValidateCmd.Flags().StringVar(&hpaValidateSeverity, "severity", "", "Minimum severity to surface (info, warning, critical) — default shows all")
}

func runHPAValidate(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := k8s.NewClient(autoscalerKubeconfig, autoscalerContext, debug)
	validator := sre.NewHPAValidator(k8s.NewSREAdapter(client), debug)

	report, err := validator.Validate(ctx)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	report.Findings = filterHPAFindingsBySeverity(report.Findings, hpaValidateSeverity)

	switch strings.ToLower(autoscalerOutput) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	default:
		printHPAValidationReport(os.Stdout, report)
		return nil
	}
}

func filterHPAFindingsBySeverity(findings []sre.HPAFinding, floor string) []sre.HPAFinding {
	floor = strings.ToLower(strings.TrimSpace(floor))
	if floor == "" {
		return findings
	}
	rank := map[string]int{"info": 0, "warning": 1, "critical": 2}
	cut, ok := rank[floor]
	if !ok {
		return findings
	}
	out := make([]sre.HPAFinding, 0, len(findings))
	for _, f := range findings {
		if rank[strings.ToLower(string(f.Severity))] >= cut {
			out = append(out, f)
		}
	}
	return out
}

func printHPAValidationReport(out io.Writer, report *sre.HPAValidationReport) {
	if report == nil {
		fmt.Fprintln(out, "No validation report.")
		return
	}

	keda := "not detected"
	if report.KEDAInstalled {
		keda = "detected"
	}
	fmt.Fprintf(out, "Scanned %d HPA(s) + %d KEDA ScaledObject(s)   KEDA: %s\n\n",
		report.HPAsScanned, report.ScaledObjectsScanned, keda)

	if len(report.Findings) == 0 {
		fmt.Fprintln(out, "No configuration issues detected. ✓")
		return
	}

	// Sort critical → warning → info, then by resource for stable output.
	sort.SliceStable(report.Findings, func(i, j int) bool {
		ri := severityRank(report.Findings[i].Severity)
		rj := severityRank(report.Findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if report.Findings[i].Namespace != report.Findings[j].Namespace {
			return report.Findings[i].Namespace < report.Findings[j].Namespace
		}
		return report.Findings[i].Name < report.Findings[j].Name
	})

	fmt.Fprintf(out, "%d issue(s):\n", len(report.Findings))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEVERITY\tKIND\tNAMESPACE/NAME\tISSUE")
	fmt.Fprintln(w, "--------\t----\t--------------\t-----")
	for _, f := range report.Findings {
		ns := f.Namespace
		if ns == "" {
			ns = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\n",
			strings.ToUpper(string(f.Severity)),
			f.Resource,
			ns, f.Name,
			truncate(f.Issue, 70),
		)
	}
	w.Flush()
}
