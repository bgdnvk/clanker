package cmd

import (
	"fmt"
	"os"

	"github.com/bgdnvk/clanker/internal/aws"
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/gcp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "clanker",
	Short: "AI-powered terminal for Cloud queries",
	Long: `Clanker is an AI-powered CLI tool that helps you query your cloud infrastructure
using natural language. Ask questions about your systems,
get insights, and perform operations through an intelligent interface.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.clanker.yaml)")
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug output (shows progress + internal diagnostics)")
	rootCmd.PersistentFlags().Bool("local-mode", true, "enable local mode with rate limiting to prevent system overload (default: true)")
	rootCmd.PersistentFlags().Int("local-delay", 100, "delay in milliseconds between calls in local mode (default 100ms)")

	// Backend integration flags
	rootCmd.PersistentFlags().String("api-key", "", "Backend API key (or set CLANKER_BACKEND_API_KEY)")
	rootCmd.PersistentFlags().String("backend-env", "", "Backend environment: testing, staging, production (or set CLANKER_BACKEND_ENV)")
	rootCmd.PersistentFlags().String("backend-url", "", "Backend API URL, overrides backend-env (or set CLANKER_BACKEND_URL)")

	// TODO: add error return here
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	viper.BindPFlag("local_mode", rootCmd.PersistentFlags().Lookup("local-mode"))
	viper.BindPFlag("local_delay_ms", rootCmd.PersistentFlags().Lookup("local-delay"))
	viper.BindPFlag("backend.api_key", rootCmd.PersistentFlags().Lookup("api-key"))
	viper.BindPFlag("backend.env", rootCmd.PersistentFlags().Lookup("backend-env"))
	viper.BindPFlag("backend.url", rootCmd.PersistentFlags().Lookup("backend-url"))

	// Set defaults for local mode
	viper.SetDefault("local_mode", true)
	viper.SetDefault("local_delay_ms", 100)

	// Register AWS static commands
	awsCmd := aws.CreateAWSCommands()
	rootCmd.AddCommand(awsCmd)

	// Register GCP static commands
	gcpCmd := gcp.CreateGCPCommands()
	rootCmd.AddCommand(gcpCmd)

	// Register Azure static commands
	azureCmd := azure.CreateAzureCommands()
	rootCmd.AddCommand(azureCmd)

	// Register Cloudflare static commands, ask command, and deploy commands
	cfCmd := cloudflare.CreateCloudflareCommands()
	AddCfAskCommand(cfCmd)
	AddCfDeployCommands(cfCmd)
	rootCmd.AddCommand(cfCmd)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding home directory: %v\n", err)
			os.Exit(1)
		}

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".clanker")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		if viper.GetBool("debug") {
			fmt.Println("Using config file:", viper.ConfigFileUsed())
		}
	}
}
