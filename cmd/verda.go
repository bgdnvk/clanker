package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// verdaAskCmd hangs off `clanker verda ask` and redirects to the main ask-mode
// flow. Keeping the subcommand registered from day one gives muscle memory
// parity with `clanker cf ask` / `clanker vercel ask` without a version gate.
var verdaAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural language questions about your Verda Cloud account",
	Long: `Ask natural language questions about your Verda Cloud (GPU/AI) account.

Use ` + "`clanker ask --verda \"...\"`" + ` — it resolves credentials, gathers
context (instances, clusters, volumes, balance, locations), and drives the
configured AI provider. Or run ` + "`clanker verda list instances`" + ` for raw data.`,
	Args: cobra.ExactArgs(1),
	RunE: runVerdaAsk,
}

// AddVerdaAskCommand attaches the ask subcommand to the `verda` tree.
// Called from root.go after `verda.CreateVerdaCommands()` returns.
func AddVerdaAskCommand(verdaCmd *cobra.Command) {
	verdaCmd.AddCommand(verdaAskCmd)
}

func runVerdaAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}
	debug, _ := cmd.Root().PersistentFlags().GetBool("debug")
	return handleVerdaQuery(cmd.Context(), question, debug)
}
