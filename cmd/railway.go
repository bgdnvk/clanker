package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Phase-1 stub. The full conversational Railway ask-mode (with history and
// tool-use) mirrors what Vercel ships today; for one-shot queries the main
// `clanker ask --railway "..."` flow handles credential resolution, context
// gathering and AI provider dispatch. Keeping the subcommand registered from
// day one means `clanker railway ask "..."` muscle memory carries over and
// docs can reference it without a version gate.
var railwayAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural language questions about your Railway account (phase 4+)",
	Long: `Ask natural language questions about your Railway account using AI.

NOTE: conversational history and per-service tool-use arrive in a later phase.
For one-shot queries today, use ` + "`clanker ask --railway \"...\"`" + ` — it
resolves your Railway token/workspace, gathers context, and drives the
configured AI provider. You can also run ` + "`clanker railway list projects`" + `
for raw data.`,
	Args: cobra.ExactArgs(1),
	RunE: runRailwayAsk,
}

// AddRailwayAskCommand attaches the ask subcommand to the `railway` tree.
// Called from root.go after `railway.CreateRailwayCommands()` returns.
func AddRailwayAskCommand(railwayCmd *cobra.Command) {
	railwayCmd.AddCommand(railwayAskCmd)
}

func runRailwayAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}
	return fmt.Errorf("railway ask subcommand not yet implemented — use 'clanker ask --railway %q' instead", question)
}
