package cmd

import (
	"fmt"

	"github.com/bgdnvk/clanker/internal/codebase"
	"github.com/spf13/cobra"
)

// codeCmd represents the code command
var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Analyze codebase directly",
	Long:  `Analyze your codebase without AI interpretation. Useful for getting raw code information.`,
}

var codeScanCmd = &cobra.Command{
	Use:   "scan [path]",
	Short: "Scan codebase for files and structure",
	Long:  `Scan the specified path (or current directory) for source code files and provide structure information.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		analyzer := codebase.NewAnalyzer(path)
		context, err := analyzer.GetRelevantContext("overview")
		if err != nil {
			return fmt.Errorf("failed to analyze codebase: %w", err)
		}

		fmt.Print(context)
		return nil
	},
}

var codeSearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for code patterns",
	Long:  `Search the codebase for specific patterns, functions, or keywords.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		path, _ := cmd.Flags().GetString("path")
		if path == "" {
			path = "."
		}

		analyzer := codebase.NewAnalyzer(path)
		context, err := analyzer.GetRelevantContext(query)
		if err != nil {
			return fmt.Errorf("failed to search codebase: %w", err)
		}

		fmt.Print(context)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(codeCmd)
	codeCmd.AddCommand(codeScanCmd)
	codeCmd.AddCommand(codeSearchCmd)

	codeSearchCmd.Flags().String("path", "", "Path to search (default: current directory)")
}
