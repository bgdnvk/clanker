package cmd

import (
	"context"
	"fmt"
	"strings"

	ghclient "github.com/bgdnvk/clanker/internal/github"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func listGitHubRepos() error {
	client := ghclient.NewClient(viper.GetString("github.token"), "", "")
	repos, err := client.ListRepositories(context.Background(), 100)
	if err != nil {
		return err
	}
	fmt.Print(ghclient.FormatRepositories(repos))
	return nil
}

// githubCmd represents the github command
var githubCmd = &cobra.Command{
	Use:   "github",
	Short: "Query GitHub repository information directly",
	Long:  `Query your GitHub repository information without AI interpretation. Useful for getting raw GitHub data.`,
}

var githubListCmd = &cobra.Command{
	Use:   "list [resource]",
	Short: "List GitHub resources",
	Long: `List GitHub resources of a specific type.
	
Supported resources:
	repos        - Accessible GitHub repositories
	runners      - Repository self-hosted runners
  workflows    - GitHub Actions workflows
  runs         - Recent workflow runs
  prs          - Recent pull requests`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resourceType := args[0]

		ctx := context.Background()

		// Handle repos list specially
		if resourceType == "repos" || resourceType == "repositories" {
			return listGitHubRepos()
		}

		token := viper.GetString("github.token")
		repoFlag, _ := cmd.Flags().GetString("repo")
		var owner, repo string
		if trimmed := strings.TrimSpace(repoFlag); trimmed != "" {
			parts := strings.SplitN(trimmed, "/", 2)
			if len(parts) == 2 {
				owner = strings.TrimSpace(parts[0])
				repo = strings.TrimSpace(parts[1])
			} else {
				repo = trimmed
			}
		}

		client := ghclient.NewClient(token, owner, repo)
		if owner == "" || repo == "" {
			resolvedOwner, resolvedRepo, err := client.ResolveRepository(ctx)
			if err == nil {
				owner = resolvedOwner
				repo = resolvedRepo
			}
		}

		switch resourceType {
		case "workflows", "workflow":
			info, err := client.GetRelevantContext(ctx, "workflows")
			if err != nil {
				return err
			}
			fmt.Print(info)
		case "runs", "run":
			info, err := client.GetRelevantContext(ctx, "runs")
			if err != nil {
				return err
			}
			fmt.Print(info)
		case "prs", "pr", "pullrequests", "pull-requests":
			info, err := client.GetRelevantContext(ctx, "pull requests")
			if err != nil {
				return err
			}
			fmt.Print(info)
		case "runners", "runner":
			runners, err := client.ListRunners(ctx)
			if err != nil {
				return err
			}
			fmt.Print(ghclient.FormatRunners(runners))
		default:
			return fmt.Errorf("unsupported resource type: %s", resourceType)
		}

		return nil
	},
}

var githubStatusCmd = &cobra.Command{
	Use:   "status [workflow-name]",
	Short: "Show GitHub auth or workflow status",
	Long:  `Show general GitHub CLI/auth status, or the status of a specific GitHub Actions workflow by name.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		repoFlag, _ := cmd.Flags().GetString("repo")
		var owner, repo string
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
		if len(args) == 0 {
			status, err := client.FormatStatus(ctx)
			if err != nil {
				return err
			}
			fmt.Print(status)
			return nil
		}

		workflowName := args[0]

		status, err := client.GetWorkflowStatus(ctx, workflowName)
		if err != nil {
			return err
		}

		fmt.Println(status)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(githubCmd)
	githubCmd.AddCommand(githubListCmd)
	githubCmd.AddCommand(githubStatusCmd)

	// Add repo flag to list command
	githubListCmd.Flags().StringP("repo", "r", "", "Repository name to use (overrides default)")
	githubStatusCmd.Flags().StringP("repo", "r", "", "Repository name to use (overrides default)")
}
