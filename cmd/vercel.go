package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Phase-1 stub. The full conversational Vercel ask-mode (with history and
// tool-use) lands in phase 4; for now one-shot queries are served by the
// main `clanker ask --vercel "..."` flow. Keeping the subcommand registered
// from day one means existing `clanker cf ask` muscle memory carries over
// and docs can reference it without a version gate.
var vercelAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural language questions about your Vercel account (phase 4+)",
	Long: `Ask natural language questions about your Vercel account using AI.

NOTE: conversational history and per-project tool-use arrive in phase 4.
For one-shot queries today, use ` + "`clanker ask --vercel \"...\"`" + ` — it
resolves your Vercel token/team, gathers context, and drives the configured
AI provider. You can also run ` + "`clanker vercel list projects`" + ` for raw
data.`,
	Args: cobra.ExactArgs(1),
	RunE: runVercelAsk,
}

// AddVercelAskCommand attaches the ask subcommand to the `vercel` tree.
// Called from root.go after `vercel.CreateVercelCommands()` returns.
func AddVercelAskCommand(vercelCmd *cobra.Command) {
	vercelCmd.AddCommand(vercelAskCmd)
}

func runVercelAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}
	return fmt.Errorf("vercel ask subcommand not yet implemented — use 'clanker ask --vercel %s' instead", question)
}
