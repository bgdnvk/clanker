package cmd

import (
	"context"
	"fmt"

	ghclient "github.com/bgdnvk/clanker/internal/github"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func listGitHubRepos() error {
	// Get default repo
	defaultRepo := viper.GetString("github.default_repo")
	if defaultRepo == "" {
		defaultRepo = "infrastructure"
	}

	// Get all configured repos
	repos := viper.Get("github.repos")
	reposList, ok := repos.([]interface{})
	if !ok || len(reposList) == 0 {
		fmt.Println("No GitHub repositories configured.")
		return nil
	}

	fmt.Printf("Available GitHub Repositories (default: %s):\n\n", defaultRepo)

	for _, r := range reposList {
		if repoMap, ok := r.(map[string]interface{}); ok {
			owner := repoMap["owner"].(string)
			repo := repoMap["repo"].(string)
			description := ""

			if d, ok := repoMap["description"].(string); ok {
				description = d
			}

			marker := ""
			if repo == defaultRepo {
				marker = " (default)"
			}

			fmt.Printf("  %s/%s%s\n", owner, repo, marker)
			if description != "" {
				fmt.Printf("    Description: %s\n", description)
			}
			fmt.Println()
		}
	}

	fmt.Println("Usage: clanker github list workflows --repo <repo-name>")

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
  repos        - Configured GitHub repositories
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

		// Get GitHub configuration
		token := viper.GetString("github.token")
		defaultRepo := viper.GetString("github.default_repo")

		// Get repo flag or use default
		repoFlag, _ := cmd.Flags().GetString("repo")
		var owner, repo string

		if repoFlag != "" {
			// Use specified repo
			repos := viper.Get("github.repos")
			if reposList, ok := repos.([]interface{}); ok {
				for _, r := range reposList {
					if repoMap, ok := r.(map[string]interface{}); ok {
						if repoName, ok := repoMap["repo"].(string); ok && repoName == repoFlag {
							owner = repoMap["owner"].(string)
							repo = repoName
							break
						}
					}
				}
			}
		} else {
			// Use default repo
			repos := viper.Get("github.repos")
			if reposList, ok := repos.([]interface{}); ok {
				for _, r := range reposList {
					if repoMap, ok := r.(map[string]interface{}); ok {
						if repoName, ok := repoMap["repo"].(string); ok && repoName == defaultRepo {
							owner = repoMap["owner"].(string)
							repo = repoName
							break
						}
					}
				}
			}
		}

		if owner == "" || repo == "" {
			return fmt.Errorf("github repository configuration not found. Use --repo flag or configure github.default_repo")
		}

		client := ghclient.NewClient(token, owner, repo)

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
		default:
			return fmt.Errorf("unsupported resource type: %s", resourceType)
		}

		return nil
	},
}

var githubStatusCmd = &cobra.Command{
	Use:   "status [workflow-name]",
	Short: "Get status of a specific workflow",
	Long:  `Get the status of a specific GitHub Actions workflow by name.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workflowName := args[0]

		ctx := context.Background()

		// Get GitHub configuration
		token := viper.GetString("github.token")
		owner := viper.GetString("github.owner")
		repo := viper.GetString("github.repo")

		if owner == "" || repo == "" {
			return fmt.Errorf("github.owner and github.repo must be configured")
		}

		client := ghclient.NewClient(token, owner, repo)

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
}
