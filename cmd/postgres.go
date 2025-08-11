package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var postgresCmd = &cobra.Command{
	Use:   "postgres",
	Short: "PostgreSQL database operations",
	Long:  `Perform operations on PostgreSQL databases configured in your clanker configuration.`,
}

var postgresListCmd = &cobra.Command{
	Use:   "list",
	Short: "List PostgreSQL connections",
	Long:  `List all PostgreSQL connections configured in the clanker configuration file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get default connection
		defaultConnection := viper.GetString("postgres.default_connection")
		if defaultConnection == "" {
			defaultConnection = "dev"
		}

		// Get all configured connections
		connections := viper.GetStringMap("postgres.connections")

		if len(connections) == 0 {
			fmt.Println("No PostgreSQL connections configured.")
			return nil
		}

		fmt.Printf("Available PostgreSQL Connections (default: %s):\n\n", defaultConnection)

		for connName, connData := range connections {
			config := connData.(map[string]interface{})
			host := "unknown"
			database := "unknown"
			description := ""

			if h, ok := config["host"].(string); ok {
				host = h
			}
			if d, ok := config["database"].(string); ok {
				database = d
			}
			if desc, ok := config["description"].(string); ok {
				description = desc
			}

			marker := ""
			if connName == defaultConnection {
				marker = " (default)"
			}

			fmt.Printf("  %s%s\n", connName, marker)
			fmt.Printf("    Host: %s\n", host)
			fmt.Printf("    Database: %s\n", database)
			if description != "" {
				fmt.Printf("    Description: %s\n", description)
			}
			fmt.Println()
		}

		fmt.Println("Usage: clanker ask --postgres <connection-name> \"your database question\"")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(postgresCmd)
	postgresCmd.AddCommand(postgresListCmd)
}
