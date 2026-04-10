package cmd

import (
	"context"
	"fmt"
	"strings"

	ghclient "github.com/bgdnvk/clanker/internal/github"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cicdCmd = &cobra.Command{
	Use:   "cicd",
	Short: "Inspect CI/CD systems",
	Long:  `Inspect CI/CD state. The current MVP provider is GitHub Actions.`,
}

var cicdProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List supported CI/CD providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Supported CI/CD providers:")
		fmt.Println("- github-actions")
		return nil
	},
}

var cicdListCmd = &cobra.Command{
	Use:   "list [resource]",
	Short: "List CI/CD resources",
	Long: `List CI/CD resources for the selected provider.

Supported resources for github-actions:
  workflows
  runs
  runners`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := cicdProvider(cmd)
		if err != nil {
			return err
		}
		if provider != "github-actions" {
			return fmt.Errorf("unsupported CI/CD provider %q", provider)
		}

		client, err := cicdGitHubClient(cmd)
		if err != nil {
			return err
		}

		resource := strings.ToLower(strings.TrimSpace(args[0]))
		switch resource {
		case "workflows", "workflow":
			info, err := client.GetRelevantContext(context.Background(), "workflows")
			if err != nil {
				return err
			}
			fmt.Print(info)
		case "runs", "run", "pipelines", "pipeline":
			info, err := client.GetRelevantContext(context.Background(), "workflow runs")
			if err != nil {
				return err
			}
			fmt.Print(info)
		case "runners", "runner":
			runners, err := client.ListRunners(context.Background())
			if err != nil {
				return err
			}
			fmt.Print(ghclient.FormatRunners(runners))
		default:
			return fmt.Errorf("unsupported CI/CD resource %q", resource)
		}

		return nil
	},
}

var cicdStatusCmd = &cobra.Command{
	Use:   "status [workflow-name]",
	Short: "Show CI/CD status",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := cicdProvider(cmd)
		if err != nil {
			return err
		}
		if provider != "github-actions" {
			return fmt.Errorf("unsupported CI/CD provider %q", provider)
		}

		client, err := cicdGitHubClient(cmd)
		if err != nil {
			return err
		}

		if len(args) == 0 {
			info, err := client.GetRelevantContext(context.Background(), "workflows runs")
			if err != nil {
				return err
			}
			fmt.Print(info)
			return nil
		}

		status, err := client.GetWorkflowStatus(context.Background(), args[0])
		if err != nil {
			return err
		}
		fmt.Println(status)
		return nil
	},
}

func cicdProvider(cmd *cobra.Command) (string, error) {
	provider, _ := cmd.Flags().GetString("provider")
	trimmed := strings.ToLower(strings.TrimSpace(provider))
	if trimmed == "" {
		trimmed = "github-actions"
	}
	return trimmed, nil
}

func cicdGitHubClient(cmd *cobra.Command) (*ghclient.Client, error) {
	repoFlag, _ := cmd.Flags().GetString("repo")
	var owner string
	var repo string
	if trimmed := strings.TrimSpace(repoFlag); trimmed != "" {
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) == 2 {
			owner = strings.TrimSpace(parts[0])
			repo = strings.TrimSpace(parts[1])
		} else {
			repo = trimmed
		}
	}
	client := ghclient.NewClient(viper.GetString("github.token"), owner, repo)
	if owner == "" || repo == "" {
		if _, _, err := client.ResolveRepository(context.Background()); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func init() {
	rootCmd.AddCommand(cicdCmd)
	cicdCmd.AddCommand(cicdProvidersCmd)
	cicdCmd.AddCommand(cicdListCmd)
	cicdCmd.AddCommand(cicdStatusCmd)

	cicdListCmd.Flags().StringP("repo", "r", "", "Repository name to use (overrides default)")
	cicdStatusCmd.Flags().StringP("repo", "r", "", "Repository name to use (overrides default)")
	cicdListCmd.Flags().String("provider", "github-actions", "CI/CD provider to query")
	cicdStatusCmd.Flags().String("provider", "github-actions", "CI/CD provider to query")
}
