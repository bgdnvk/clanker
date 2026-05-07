package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/updater"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update clanker",
	Long: `Update the clanker binary from GitHub.

By default, clanker updates from the latest GitHub release. Set update.channel
to "main" in ~/.clanker.yaml, or pass --channel main, to build and install the
latest commit from the main branch instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		channel, _ := cmd.Flags().GetString("channel")
		if strings.TrimSpace(channel) == "" {
			channel = viper.GetString("update.channel")
		}

		repo, _ := cmd.Flags().GetString("repo")
		installPath, _ := cmd.Flags().GetString("install-path")
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		token := strings.TrimSpace(viper.GetString("github.token"))
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}

		result, err := updater.Update(cmd.Context(), updater.Options{
			Channel:        channel,
			Repo:           repo,
			InstallPath:    installPath,
			CurrentVersion: Version,
			Token:          token,
			Force:          force,
			DryRun:         dryRun,
			Stdout:         cmd.OutOrStdout(),
			Stderr:         cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}

		if dryRun {
			if result.Channel == updater.ChannelMain {
				fmt.Fprintf(cmd.OutOrStdout(), "Would update clanker from %s commit %s into %s\n", result.SourceURL, result.TargetSHA, result.InstallPath)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Would update clanker to %s from %s into %s\n", result.TargetVersion, result.SourceURL, result.InstallPath)
			return nil
		}

		if !result.Updated {
			fmt.Fprintf(cmd.OutOrStdout(), "clanker is already up to date (%s)\n", result.TargetVersion)
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Updated clanker to %s via %s at %s\n", result.TargetVersion, result.Channel, result.InstallPath)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)

	updateCmd.Flags().String("channel", "", "update channel: release or main (default: update.channel config or release)")
	updateCmd.Flags().String("repo", updater.DefaultRepo, "GitHub repository to update from, in owner/repo form")
	updateCmd.Flags().String("install-path", "", "path to replace (default: current clanker executable)")
	updateCmd.Flags().Bool("force", false, "update even if the current version appears current")
	updateCmd.Flags().Bool("dry-run", false, "show what would be updated without changing files")
}
