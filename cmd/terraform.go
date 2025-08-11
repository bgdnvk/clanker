package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var terraformCmd = &cobra.Command{
	Use:   "terraform",
	Short: "Terraform workspace operations",
	Long:  `Perform operations on Terraform workspaces configured in your clanker configuration.`,
}

var terraformListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Terraform workspaces",
	Long:  `List all Terraform workspaces configured in the clanker configuration file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get default workspace
		defaultWorkspace := viper.GetString("terraform.default_workspace")
		if defaultWorkspace == "" {
			defaultWorkspace = "dev"
		}

		// Get all configured workspaces
		workspaces := viper.GetStringMap("terraform.workspaces")

		if len(workspaces) == 0 {
			fmt.Println("No Terraform workspaces configured.")
			return nil
		}

		fmt.Printf("Available Terraform Workspaces (default: %s):\n\n", defaultWorkspace)

		for workspaceName, workspaceData := range workspaces {
			config := workspaceData.(map[string]interface{})
			path := "unknown"
			description := ""

			if p, ok := config["path"].(string); ok {
				path = p
			}
			if d, ok := config["description"].(string); ok {
				description = d
			}

			marker := ""
			if workspaceName == defaultWorkspace {
				marker = " (default)"
			}

			fmt.Printf("  %s%s\n", workspaceName, marker)
			fmt.Printf("    Path: %s\n", path)
			if description != "" {
				fmt.Printf("    Description: %s\n", description)
			}
			fmt.Println()
		}

		fmt.Println("Usage: clanker ask --terraform <workspace-name> \"your infrastructure question\"")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(terraformCmd)
	terraformCmd.AddCommand(terraformListCmd)
}
