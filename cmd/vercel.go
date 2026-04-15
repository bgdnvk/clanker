package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Phase-1 stub. The real LLM-powered ask-mode for Vercel lands in phase 4
// (cost + AI ask-mode). Keeping the subcommand registered from day one means
// existing `clanker cf ask` muscle memory carries over and docs can reference
// it without a version gate.
var vercelAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural language questions about your Vercel account (phase 4+)",
	Long: `Ask natural language questions about your Vercel account using AI.

NOTE: the dedicated Vercel ask pipeline arrives in phase 4. Until then, use
'clanker ask --vercel "..."' — the main ask command will include Vercel context
when phase 4 ships. You can also run ` + "`clanker vercel list projects`" + ` today for
raw data.`,
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
	fmt.Println("Vercel ask-mode is not yet wired (planned for phase 4).")
	fmt.Println("For now, try:")
	fmt.Println("  clanker vercel list projects")
	fmt.Println("  clanker vercel list deployments --project <id>")
	fmt.Println("  clanker vercel analytics --period 30d")
	return nil
}
