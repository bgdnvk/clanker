package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	tfclient "github.com/bgdnvk/clanker/internal/terraform"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var terraformCmd = &cobra.Command{
	Use:   "terraform",
	Short: "Terraform workspace operations",
	Long:  `Perform operations on Terraform workspaces configured in your clanker configuration.`,
}

var terraformListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Terraform workspaces",
	Long:  `List all Terraform workspaces configured in the clanker configuration file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get default workspace
		defaultWorkspace := viper.GetString("terraform.default_workspace")
		if defaultWorkspace == "" {
			defaultWorkspace = "dev"
		}

		// Get all configured workspaces
		workspaces := viper.GetStringMap("terraform.workspaces")

		if len(workspaces) == 0 {
			fmt.Println("No Terraform workspaces configured.")
			return nil
		}

		fmt.Printf("Available Terraform Workspaces (default: %s):\n\n", defaultWorkspace)

		for workspaceName, workspaceData := range workspaces {
			config := workspaceData.(map[string]interface{})
			path := "unknown"
			description := ""

			if p, ok := config["path"].(string); ok {
				path = p
			}
			if d, ok := config["description"].(string); ok {
				description = d
			}

			marker := ""
			if workspaceName == defaultWorkspace {
				marker = " (default)"
			}

			fmt.Printf("  %s%s\n", workspaceName, marker)
			fmt.Printf("    Path: %s\n", path)
			if description != "" {
				fmt.Printf("    Description: %s\n", description)
			}
			fmt.Println()
		}

		fmt.Println("Usage: clanker ask --terraform <workspace-name> \"your infrastructure question\"")

		return nil
	},
}

var terraformAnalyzeCmd = &cobra.Command{
	Use:   "analyze [workspace-or-path]",
	Short: "Analyze Terraform/OpenTofu state, drift, and IaC alternatives",
	Long: `Analyze a Terraform or OpenTofu workspace.

By default this scans local configuration/state metadata only. Add --drift to run
a refresh-only drift check, or --plan to run a normal detailed-exitcode plan.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace, _ := cmd.Flags().GetString("workspace")
		if len(args) > 0 {
			workspace = args[0]
		}
		tool, _ := cmd.Flags().GetString("tool")
		format, _ := cmd.Flags().GetString("format")
		checkDrift, _ := cmd.Flags().GetBool("drift")
		includePlan, _ := cmd.Flags().GetBool("plan")
		maxLines, _ := cmd.Flags().GetInt("max-lines")

		client, err := tfclient.NewClientWithTool(workspace, tool)
		if err != nil {
			return err
		}
		report, err := client.Analyze(cmd.Context(), tfclient.AnalysisOptions{
			Tool:           tool,
			CheckDrift:     checkDrift,
			IncludePlan:    includePlan,
			MaxOutputLines: maxLines,
		})
		if err != nil {
			return err
		}

		if strings.EqualFold(format, "json") {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		}
		fmt.Print(formatTerraformAnalysis(report))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(terraformCmd)
	terraformCmd.AddCommand(terraformListCmd, terraformAnalyzeCmd)
	terraformAnalyzeCmd.Flags().String("workspace", "", "Configured workspace name or local path")
	terraformAnalyzeCmd.Flags().String("tool", "", "IaC binary to use: terraform or tofu (default auto-detect)")
	terraformAnalyzeCmd.Flags().Bool("drift", false, "Run refresh-only drift detection with detailed exit codes")
	terraformAnalyzeCmd.Flags().Bool("plan", false, "Run a normal speculative plan with detailed exit codes")
	terraformAnalyzeCmd.Flags().Int("max-lines", 80, "Maximum command output lines to include")
	terraformAnalyzeCmd.Flags().String("format", "text", "Output format: text or json")
}

func formatTerraformAnalysis(report tfclient.AnalysisReport) string {
	var out strings.Builder
	out.WriteString("Terraform analysis\n")
	out.WriteString(fmt.Sprintf("Workspace: %s\n", fallbackText(report.Workspace, "local")))
	out.WriteString(fmt.Sprintf("Path: %s\n", report.Path))
	out.WriteString(fmt.Sprintf("Tool: %s", report.Tool))
	if report.ToolPath != "" {
		out.WriteString(fmt.Sprintf(" (%s)", report.ToolPath))
	}
	out.WriteString("\n")
	out.WriteString(fmt.Sprintf("Mode: %s\n", fallbackText(report.Mode, "unknown")))
	out.WriteString(fmt.Sprintf("Files: %d\n", len(report.Files)))
	if len(report.Backends) > 0 {
		out.WriteString(fmt.Sprintf("Backends: %s\n", strings.Join(report.Backends, ", ")))
	}
	if len(report.ProviderSources) > 0 {
		out.WriteString(fmt.Sprintf("Providers: %s\n", strings.Join(report.ProviderSources, ", ")))
	}
	if len(report.Modules) > 0 {
		out.WriteString(fmt.Sprintf("Modules: %s\n", strings.Join(report.Modules, ", ")))
	}
	if report.State != nil {
		out.WriteString(fmt.Sprintf("State resources: %d\n", report.State.ResourceCount))
		if len(report.State.ResourceTypes) > 0 {
			out.WriteString("Resource types:\n")
			for _, line := range sortedCountLines(report.State.ResourceTypes) {
				out.WriteString("  " + line + "\n")
			}
		}
	}
	if len(report.StaleArtifacts) > 0 {
		out.WriteString("\nStale artifacts:\n")
		for _, artifact := range report.StaleArtifacts {
			out.WriteString(fmt.Sprintf("- %s (%s, age %s): %s\n", artifact.Path, artifact.Kind, artifact.Age, artifact.Recommendation))
		}
	}
	if report.Drift != nil {
		out.WriteString("\nDrift/plan:\n")
		out.WriteString(fmt.Sprintf("Command: %s\n", report.Drift.Command))
		out.WriteString(fmt.Sprintf("Exit code: %d\n", report.Drift.ExitCode))
		out.WriteString(fmt.Sprintf("Changes present: %v\n", report.Drift.HasChanges))
		for _, line := range report.Drift.Summary {
			out.WriteString("  " + line + "\n")
		}
		if report.Drift.Error != "" {
			out.WriteString("Error: " + report.Drift.Error + "\n")
		}
	} else {
		out.WriteString("\nDrift/plan: not checked (use --drift or --plan)\n")
	}
	if len(report.Alternatives) > 0 {
		out.WriteString("\nIaC alternatives:\n")
		for _, alt := range report.Alternatives {
			status := "not detected"
			if alt.Detected {
				status = "detected"
			}
			out.WriteString(fmt.Sprintf("- %s: %s; %s; drift/diff: %s (%s)\n", alt.Name, status, alt.Category, fallbackText(alt.DriftCommand, "n/a"), alt.DocsURL))
		}
	}
	if len(report.Warnings) > 0 {
		out.WriteString("\nWarnings:\n")
		for _, warning := range report.Warnings {
			out.WriteString("- " + warning + "\n")
		}
	}
	if len(report.Recommendations) > 0 {
		out.WriteString("\nRecommendations:\n")
		for _, recommendation := range report.Recommendations {
			out.WriteString("- " + recommendation + "\n")
		}
	}
	return out.String()
}

func sortedCountLines(values map[string]int) []string {
	lines := make([]string, 0, len(values))
	for key, count := range values {
		lines = append(lines, fmt.Sprintf("%s: %d", key, count))
	}
	sort.Strings(lines)
	return lines
}

func fallbackText(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
