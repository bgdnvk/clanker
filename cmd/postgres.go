package cmd

import (
	"github.com/spf13/cobra"
)

var postgresCmd = &cobra.Command{
	Use:   "postgres",
	Short: "Legacy PostgreSQL alias",
	Long:  `Legacy alias for database inspection commands. Prefer 'clanker db ...'.`,
}

var postgresListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List PostgreSQL connections",
	Long:    `List configured PostgreSQL-style database connections. Prefer 'clanker db list'.`,
	RunE:    runDBList,
	Aliases: []string{"ls"},
	Hidden:  false,
}

var postgresInspectCmd = &cobra.Command{
	Use:   "inspect [connection]",
	Short: "Inspect a PostgreSQL connection",
	Long:  `Inspect a configured PostgreSQL, Supabase, or Neon connection. Prefer 'clanker db inspect'.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDBInspect,
}

func init() {
	rootCmd.AddCommand(postgresCmd)
	postgresCmd.AddCommand(postgresListCmd)
	postgresCmd.AddCommand(postgresInspectCmd)
}
