package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bgdnvk/clanker/internal/sre"
	"github.com/spf13/cobra"
)

var sreCmd = &cobra.Command{
	Use:   "sre",
	Short: "Run the Clanker SRE bot",
	Long: `Run the Clanker SRE bot for adaptive, read-only infrastructure visibility.

Docker is the default runtime. Use --target local, launchd, systemd, k8s, or cloud-vm when you want another install path.`,
}

var sreDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Discover local, cloud, and observability signals for SRE install planning",
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		discovery := sre.Discover(cmd.Context())
		sre.SortDiscovery(&discovery)
		if strings.EqualFold(format, "json") {
			return json.NewEncoder(os.Stdout).Encode(discovery)
		}
		fmt.Print(formatSREDiscovery(discovery))
		return nil
	},
}

var srePlanCmd = &cobra.Command{
	Use:   "plan",
	Short: "Plan an adaptive SRE installation",
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		plan, err := buildSREPlan(cmd)
		if err != nil {
			return err
		}
		if strings.EqualFold(format, "json") {
			return json.NewEncoder(os.Stdout).Encode(plan)
		}
		fmt.Print(sre.FormatPlanText(plan))
		return nil
	},
}

var sreInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Generate SRE install assets for Docker, local, systemd, launchd, k8s, or cloud-vm",
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		apply, _ := cmd.Flags().GetBool("apply")
		outputDir, _ := cmd.Flags().GetString("output-dir")
		plan, err := buildSREPlan(cmd)
		if err != nil {
			return err
		}
		if apply {
			if err := sre.ApplyPlan(plan, outputDir); err != nil {
				return err
			}
			if strings.TrimSpace(outputDir) == "" {
				stateDir, stateErr := sre.DefaultStateDir()
				if stateErr == nil {
					outputDir = stateDir + string(os.PathSeparator) + "install"
				}
			}
			fmt.Fprintf(os.Stderr, "[sre] wrote install assets to %s\n", outputDir)
		}
		if strings.EqualFold(format, "json") {
			return json.NewEncoder(os.Stdout).Encode(plan)
		}
		fmt.Print(sre.FormatPlanText(plan))
		return nil
	},
}

var sreRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the SRE bot in the foreground",
	RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("target")
		agentID, _ := cmd.Flags().GetString("agent-id")
		name, _ := cmd.Flags().GetString("name")
		cerebroURL, _ := cmd.Flags().GetString("cerebro-url")
		ingestToken, _ := cmd.Flags().GetString("ingest-token")
		provider, _ := cmd.Flags().GetString("provider")
		deployID, _ := cmd.Flags().GetString("deploy-id")
		interval, err := durationFlag(cmd, "interval", sre.DefaultInterval)
		if err != nil {
			return err
		}
		once, _ := cmd.Flags().GetBool("once")

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = sre.Run(ctx, sre.RunOptions{
			Target:      target,
			AgentID:     agentID,
			AgentName:   name,
			BackendURL:  cerebroURL,
			IngestToken: ingestToken,
			Interval:    interval,
			Once:        once,
			Writer:      os.Stdout,
			Provider:    provider,
			DeployID:    deployID,
		})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	},
}

var sreStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local SRE install status and adaptive target recommendation",
	RunE: func(cmd *cobra.Command, args []string) error {
		discovery := sre.Discover(cmd.Context())
		stateDir, err := sre.DefaultStateDir()
		if err != nil {
			return err
		}
		fmt.Printf("Clanker SRE\n")
		fmt.Printf("Recommended target: %s\n", discovery.RecommendedTarget)
		fmt.Printf("Docker: %v (%s)\n", discovery.Docker.Available, discovery.Docker.Detail)
		fmt.Printf("Kubernetes: %v (%s)\n", discovery.Kubernetes.Available, discovery.Kubernetes.Detail)
		fmt.Printf("OTel: %v (%s)\n", discovery.OTel.Available, discovery.OTel.Detail)
		fmt.Printf("State dir: %s\n", stateDir)
		return nil
	},
}

var sreDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Validate SRE prerequisites for the selected install target",
	RunE: func(cmd *cobra.Command, args []string) error {
		plan, err := buildSREPlan(cmd)
		if err != nil {
			return err
		}
		if plan.Available && len(plan.Warnings) == 0 {
			fmt.Printf("SRE target %s looks ready\n", plan.Target)
			return nil
		}
		fmt.Printf("SRE target %s needs attention\n", plan.Target)
		for _, warning := range plan.Warnings {
			fmt.Printf("- %s\n", warning)
		}
		if !plan.Available {
			return fmt.Errorf("target %s is not available on this host", plan.Target)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(sreCmd)
	sreCmd.PersistentFlags().Bool("sre", false, "mark this command as running through the Clanker SRE path")
	sreCmd.AddCommand(sreDiscoverCmd, srePlanCmd, sreInstallCmd, sreRunCmd, sreStatusCmd, sreDoctorCmd)

	sreDiscoverCmd.Flags().String("format", "text", "Output format: text or json")
	addSREPlanFlags(srePlanCmd)
	addSREPlanFlags(sreInstallCmd)
	sreInstallCmd.Flags().Bool("apply", false, "Write generated install assets to disk")
	sreInstallCmd.Flags().String("output-dir", "", "Directory for generated install assets (default ~/.clanker/sre/install)")
	addSRERunFlags(sreRunCmd)
	addSREPlanFlags(sreDoctorCmd)
}

func addSREPlanFlags(cmd *cobra.Command) {
	cmd.Flags().String("target", "docker", "Install target: docker, auto, local, launchd, systemd, k8s, or cloud-vm")
	cmd.Flags().String("image", sre.DefaultImage, "Container image for docker, k8s, and cloud-vm targets")
	cmd.Flags().String("name", sre.DefaultAgentName, "SRE bot name/container/service name")
	cmd.Flags().String("cerebro-url", "", "Cerebro API base URL, e.g. http://127.0.0.1:8080/api")
	cmd.Flags().String("ingest-token-env", sre.DefaultIngestTokenEnv, "Environment variable name that holds the Cerebro ingest token")
	cmd.Flags().String("provider", "", "Cloud provider name for heartbeat identification (aws, gcp, azure, etc.)")
	cmd.Flags().String("deploy-id", "", "Stable SRE deployment ID for heartbeat verification")
	cmd.Flags().String("interval", sre.DefaultInterval.String(), "Heartbeat/discovery interval")
	cmd.Flags().String("format", "text", "Output format: text or json")
}

func addSRERunFlags(cmd *cobra.Command) {
	cmd.Flags().String("target", "docker", "Runtime target label: docker, local, launchd, systemd, k8s, or cloud-vm")
	cmd.Flags().String("agent-id", "", "Stable SRE agent ID (default derived from hostname)")
	cmd.Flags().String("name", sre.DefaultAgentName, "SRE bot name")
	cmd.Flags().String("cerebro-url", "", "Cerebro API base URL, e.g. http://127.0.0.1:8080/api")
	cmd.Flags().String("ingest-token", "", "Cerebro ingest token (or set CLANKER_CEREBRO_INGEST_TOKEN)")
	cmd.Flags().String("provider", "", "Cloud provider name for heartbeat identification (aws, gcp, azure, etc.)")
	cmd.Flags().String("deploy-id", "", "Stable SRE deployment ID for heartbeat verification")
	cmd.Flags().String("interval", sre.DefaultInterval.String(), "Heartbeat/discovery interval")
	cmd.Flags().Bool("once", false, "Send one heartbeat and exit")
}

func buildSREPlan(cmd *cobra.Command) (sre.InstallPlan, error) {
	interval, err := durationFlag(cmd, "interval", sre.DefaultInterval)
	if err != nil {
		return sre.InstallPlan{}, err
	}
	target, _ := cmd.Flags().GetString("target")
	image, _ := cmd.Flags().GetString("image")
	name, _ := cmd.Flags().GetString("name")
	cerebroURL, _ := cmd.Flags().GetString("cerebro-url")
	ingestTokenEnv, _ := cmd.Flags().GetString("ingest-token-env")
	provider, _ := cmd.Flags().GetString("provider")
	deployID, _ := cmd.Flags().GetString("deploy-id")
	discovery := sre.Discover(cmd.Context())
	return sre.BuildPlan(discovery, sre.PlanOptions{Target: target, Image: image, Name: name, BackendURL: cerebroURL, IngestTokenEnv: ingestTokenEnv, Provider: provider, DeployID: deployID, Interval: interval}), nil
}

func durationFlag(cmd *cobra.Command, name string, fallback time.Duration) (time.Duration, error) {
	value, _ := cmd.Flags().GetString(name)
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s duration: %w", name, err)
	}
	return duration, nil
}

func formatSREDiscovery(discovery sre.Discovery) string {
	var out strings.Builder
	out.WriteString("Clanker SRE discovery\n")
	out.WriteString("Host: " + discovery.Hostname + " (" + discovery.OS + "/" + discovery.Arch + ")\n")
	out.WriteString("Recommended target: " + discovery.RecommendedTarget + "\n")
	out.WriteString("\nCapabilities:\n")
	out.WriteString(fmt.Sprintf("- local: %v (%s)\n", discovery.Local.Available, discovery.Local.Detail))
	out.WriteString(fmt.Sprintf("- docker: %v (%s)\n", discovery.Docker.Available, discovery.Docker.Detail))
	out.WriteString(fmt.Sprintf("- kubernetes: %v (%s)\n", discovery.Kubernetes.Available, discovery.Kubernetes.Detail))
	out.WriteString(fmt.Sprintf("- otel: %v (%s)\n", discovery.OTel.Available, discovery.OTel.Detail))
	out.WriteString(fmt.Sprintf("- databases: %v (%s)\n", discovery.Databases.Available, discovery.Databases.Detail))
	out.WriteString(fmt.Sprintf("- cicd: %v (%s)\n", discovery.CICD.Available, discovery.CICD.Detail))
	out.WriteString(fmt.Sprintf("- terraform: %v (%s)\n", discovery.Terraform.Available, discovery.Terraform.Detail))
	out.WriteString("\nProviders:\n")
	for _, provider := range discovery.Providers {
		out.WriteString(fmt.Sprintf("- %s: %v (%s)\n", provider.Name, provider.Available, provider.Detail))
	}
	if len(discovery.Notes) > 0 {
		out.WriteString("\nNotes:\n")
		for _, note := range discovery.Notes {
			out.WriteString("- " + note + "\n")
		}
	}
	return out.String()
}
