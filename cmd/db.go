package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/dbcontext"
	"github.com/spf13/cobra"
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Inspect configured databases",
	Long:  `Inspect configured Postgres, Supabase, Neon, MySQL, and SQLite connections from your clanker configuration.`,
}

var dbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured database connections",
	RunE:  runDBList,
}

var dbInspectCmd = &cobra.Command{
	Use:   "inspect [connection]",
	Short: "Inspect a configured database connection",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDBInspect,
}

func runDBList(cmd *cobra.Command, args []string) error {
	connections, defaultName, err := dbcontext.ListConnections()
	if err != nil {
		return err
	}
	if len(connections) == 0 {
		fmt.Println("No database connections configured. Add entries under databases.connections or legacy postgres.connections.")
		return nil
	}

	fmt.Printf("Available Database Connections (default: %s):\n\n", defaultName)
	for _, connection := range connections {
		marker := ""
		if connection.Name == defaultName {
			marker = " (default)"
		}
		fmt.Printf("  %s%s\n", connection.Name, marker)
		fmt.Printf("    Type: %s\n", connection.Kind())
		fmt.Printf("    Target: %s\n", connection.Target())
		if connection.Description != "" {
			fmt.Printf("    Description: %s\n", connection.Description)
		}
		fmt.Println()
	}

	fmt.Println("Usage: clanker db inspect [connection]")
	return nil
}

func runDBInspect(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	inspection, err := dbcontext.Inspect(context.Background(), name)
	if err != nil {
		return err
	}

	fmt.Printf("Connection: %s\n", inspection.Connection.Name)
	fmt.Printf("Type: %s\n", inspection.Connection.Kind())
	fmt.Printf("Target: %s\n", inspection.Connection.Target())
	fmt.Printf("Ping: %d ms\n", inspection.PingMillis)
	if inspection.Version != "" {
		fmt.Printf("Version: %s\n", inspection.Version)
	}
	if inspection.CurrentDatabase != "" {
		fmt.Printf("Database: %s\n", inspection.CurrentDatabase)
	}
	if len(inspection.Objects) == 0 {
		fmt.Println("Objects: none discovered")
		return nil
	}

	fmt.Println("Objects:")
	for _, object := range inspection.Objects {
		qualifiedName := object.Name
		if object.Schema != "" {
			qualifiedName = object.Schema + "." + object.Name
		}
		fmt.Printf("  - %s [%s]\n", qualifiedName, object.Type)
		if len(object.Columns) == 0 {
			continue
		}
		columns := make([]string, 0, len(object.Columns))
		for _, column := range object.Columns {
			nullability := "nullable"
			if !column.Nullable {
				nullability = "not null"
			}
			typeName := strings.TrimSpace(column.Type)
			if typeName == "" {
				typeName = "unknown"
			}
			columns = append(columns, fmt.Sprintf("%s %s %s", column.Name, typeName, nullability))
		}
		fmt.Printf("    Columns: %s\n", strings.Join(columns, ", "))
	}

	return nil
}

func init() {
	rootCmd.AddCommand(dbCmd)
	dbCmd.AddCommand(dbListCmd)
	dbCmd.AddCommand(dbInspectCmd)
}
