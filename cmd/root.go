package cmd

import (
	"fmt"
	"os"

	"github.com/bgdnvk/clanker/internal/aws"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "clanker",
	Short: "AI-powered terminal for AWS and codebase queries",
	Long: `Clanker is an AI-powered CLI tool that helps you query your AWS infrastructure
and analyze your codebase using natural language. Ask questions about your systems,
get insights, and perform operations through an intelligent interface.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.clanker.yaml)")
	rootCmd.PersistentFlags().Bool("verbose", false, "verbose output")
	rootCmd.PersistentFlags().Bool("local-mode", true, "enable local mode with rate limiting to prevent system overload (default: true)")
	rootCmd.PersistentFlags().Int("local-delay", 100, "delay in milliseconds between calls in local mode (default 100ms)")

	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
	viper.BindPFlag("local_mode", rootCmd.PersistentFlags().Lookup("local-mode"))
	viper.BindPFlag("local_delay_ms", rootCmd.PersistentFlags().Lookup("local-delay"))

	// Set defaults for local mode
	viper.SetDefault("local_mode", true)
	viper.SetDefault("local_delay_ms", 100)

	// Register AWS static commands
	awsCmd := aws.CreateAWSCommands()
	rootCmd.AddCommand(awsCmd)
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
		if viper.GetBool("verbose") {
			fmt.Println("Using config file:", viper.ConfigFileUsed())
		}
	}
}
