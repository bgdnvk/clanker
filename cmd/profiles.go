package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var profilesCmd = &cobra.Command{
	Use:   "profiles",
	Short: "List available AWS profiles",
	Long:  `List all AWS profiles configured in the clanker configuration file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get default profile
		defaultProfile := viper.GetString("aws.default_profile")
		if defaultProfile == "" {
			defaultProfile = "default"
		}

		// Get all configured profiles
		profiles := viper.GetStringMap("aws.profiles")

		if len(profiles) == 0 {
			fmt.Println("No AWS profiles configured.")
			return nil
		}

		fmt.Printf("Available AWS Profiles (default: %s):\n\n", defaultProfile)

		for profileName, profileData := range profiles {
			config := profileData.(map[string]interface{})
			region := "unknown"
			description := ""

			if r, ok := config["region"].(string); ok {
				region = r
			}
			if d, ok := config["description"].(string); ok {
				description = d
			}

			marker := ""
			if profileName == defaultProfile {
				marker = " (default)"
			}

			fmt.Printf("  %s%s\n", profileName, marker)
			fmt.Printf("    Region: %s\n", region)
			if description != "" {
				fmt.Printf("    Description: %s\n", description)
			}
			fmt.Println()
		}

		fmt.Println("Usage: clanker ask --profile <profile-name> \"your question\"")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(profilesCmd)
}
