package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/codeview"
	"github.com/spf13/cobra"
)

var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Analyze source repositories",
}

var codeAnalyzeCmd = &cobra.Command{
	Use:   "analyze [repo-url-or-path]",
	Short: "Map codebase languages, patterns, and file connections",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repo := strings.TrimSpace(args[0])
		outputJSON, _ := cmd.Flags().GetBool("json")
		keepClone, _ := cmd.Flags().GetBool("keep-clone")

		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()

		analysis, cleanup, err := codeview.CloneAndAnalyze(ctx, repo, codeview.AnalyzeOptions{KeepClone: keepClone})
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}

		if outputJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(analysis)
		}

		fmt.Fprintf(os.Stdout, "%s\n", analysis.RepoURL)
		fmt.Fprintf(os.Stdout, "Primary language: %s\n", analysis.Summary.PrimaryLanguage)
		fmt.Fprintf(os.Stdout, "Files: %d source / %d total\n", analysis.Summary.SourceFiles, analysis.Summary.TotalFiles)
		fmt.Fprintf(os.Stdout, "Patterns: %d\n", analysis.Summary.PatternCount)
		for _, pattern := range analysis.Patterns {
			fmt.Fprintf(os.Stdout, "- %s: %d files\n", pattern.Label, len(pattern.Files))
		}
		return nil
	},
}

func init() {
	codeAnalyzeCmd.Flags().Bool("json", false, "Print the full code view graph as JSON")
	codeAnalyzeCmd.Flags().Bool("keep-clone", false, "Keep the temporary clone and include clonePath in JSON")
	codeCmd.AddCommand(codeAnalyzeCmd)
	rootCmd.AddCommand(codeCmd)
}
