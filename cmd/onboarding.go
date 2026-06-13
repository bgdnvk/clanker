package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/onboarding"
	"github.com/spf13/cobra"
)

var onboardingCmd = &cobra.Command{
	Use:   "onboarding",
	Short: "Scan and install provider CLIs for Clanker Cloud onboarding",
	Long: `Scan this machine for provider CLIs and credentials Clanker Cloud can use.

The JSON output is designed for Clanker Cloud and MCP agents so first-run setup
can detect AWS, GCP, Azure, Cloudflare, DigitalOcean, Hetzner, Kubernetes,
GitHub, and Terraform tooling before asking the user to install anything.`,
}

var onboardingScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan local provider CLIs and detected provider context",
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		providers, _ := cmd.Flags().GetStringSlice("provider")
		result := onboarding.Scan(cmd.Context(), onboarding.ScanOptions{WantedProviders: providers})
		if strings.EqualFold(format, "json") {
			return writeOnboardingJSON(result)
		}
		printOnboardingScan(result)
		return nil
	},
}

var onboardingInstallCmd = &cobra.Command{
	Use:   "install [tool...]",
	Short: "Install missing provider CLIs from the onboarding scan",
	Long: `Install provider CLIs using allowlisted platform commands.

Pass explicit tools such as aws, gcloud, az, wrangler, doctl, hcloud, kubectl,
gh, terraform, opentofu, or docker. If no tools are passed, Clanker installs
the missing tools for detected providers or providers named with --provider.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		providers, _ := cmd.Flags().GetStringSlice("provider")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		yes, _ := cmd.Flags().GetBool("yes")
		timeoutText, _ := cmd.Flags().GetString("timeout")
		timeout := 20 * time.Minute
		if strings.TrimSpace(timeoutText) != "" {
			parsed, err := time.ParseDuration(timeoutText)
			if err != nil {
				return fmt.Errorf("invalid --timeout: %w", err)
			}
			timeout = parsed
		}
		result := onboarding.Install(cmd.Context(), onboarding.InstallOptions{
			Tools:           args,
			WantedProviders: providers,
			DryRun:          dryRun,
			AssumeYes:       yes,
			Timeout:         timeout,
		})
		if strings.EqualFold(format, "json") {
			return writeOnboardingJSON(result)
		}
		printOnboardingInstall(result)
		if !result.OK {
			return fmt.Errorf("one or more onboarding installs failed")
		}
		return nil
	},
}

var onboardingAgentPromptCmd = &cobra.Command{
	Use:   "agent-prompt",
	Short: "Print a setup prompt for local agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		providers, _ := cmd.Flags().GetStringSlice("provider")
		result := onboarding.Scan(cmd.Context(), onboarding.ScanOptions{WantedProviders: providers})
		fmt.Print(result.AgentInstructions)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(onboardingCmd)
	onboardingCmd.AddCommand(onboardingScanCmd, onboardingInstallCmd, onboardingAgentPromptCmd)

	onboardingScanCmd.Flags().String("format", "text", "Output format: text or json")
	onboardingScanCmd.Flags().StringSlice("provider", nil, "Provider(s) the user wants to use, comma-separated or repeated")

	onboardingInstallCmd.Flags().String("format", "text", "Output format: text or json")
	onboardingInstallCmd.Flags().StringSlice("provider", nil, "Provider(s) the user wants to use, comma-separated or repeated")
	onboardingInstallCmd.Flags().Bool("dry-run", false, "Print the selected install commands without running them")
	onboardingInstallCmd.Flags().Bool("yes", false, "Run install commands without an interactive confirmation")
	onboardingInstallCmd.Flags().String("timeout", "20m", "Timeout per install command")

	onboardingAgentPromptCmd.Flags().StringSlice("provider", nil, "Provider(s) the user wants to use, comma-separated or repeated")
}

func writeOnboardingJSON(value any) error {
	payload, err := onboarding.MarshalPretty(value)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(payload)
	return err
}

func printOnboardingScan(result onboarding.ScanResult) {
	fmt.Printf("Clanker onboarding scan (%s/%s)\n", result.OS, result.Arch)
	fmt.Println("\nProviders:")
	for _, provider := range result.Providers {
		state := "not detected"
		if provider.Ready {
			state = "ready"
		} else if provider.Detected || provider.Configured || provider.Wanted {
			state = "needs tools"
		}
		line := fmt.Sprintf("- %s: %s", provider.Name, state)
		if len(provider.MissingTools) > 0 {
			line += " (missing: " + strings.Join(provider.MissingTools, ", ") + ")"
		}
		fmt.Println(line)
	}
	fmt.Println("\nRecommended provider CLIs:")
	if len(result.RecommendedTools) == 0 {
		fmt.Println("- none yet; choose providers with --provider aws,gcp,azure,...")
	} else {
		for _, id := range result.RecommendedTools {
			tool := result.Tools[id]
			state := "missing"
			if tool.Installed {
				state = "installed"
			}
			fmt.Printf("- %s: %s\n", tool.Tool, state)
		}
	}
	if len(result.MissingTools) > 0 {
		fmt.Println("\nInstall missing tools:")
		ids := make([]string, 0, len(result.MissingTools))
		for _, tool := range result.MissingTools {
			ids = append(ids, tool.ID)
		}
		fmt.Printf("  clanker onboarding install --yes %s\n", strings.Join(ids, " "))
	}
}

func printOnboardingInstall(result onboarding.InstallResult) {
	if result.Message != "" {
		fmt.Println(result.Message)
	}
	for _, tool := range result.Results {
		if tool.Error != "" {
			fmt.Printf("%s: failed: %s\n", tool.Tool, tool.Error)
			continue
		}
		if tool.DryRun {
			fmt.Printf("%s: dry run\n", tool.Tool)
			for _, command := range tool.Commands {
				fmt.Printf("  %s\n", command)
			}
			continue
		}
		if tool.Installed {
			fmt.Printf("%s: installed\n", tool.Tool)
		} else {
			fmt.Printf("%s: commands finished, but %s was not found in PATH yet\n", tool.Tool, tool.Binary)
		}
	}
}

func withTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
