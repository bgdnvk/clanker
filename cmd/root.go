package cmd

import (
	"fmt"
	"os"

	"github.com/bgdnvk/clanker/internal/aws"
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/digitalocean"
	"github.com/bgdnvk/clanker/internal/gcp"
	"github.com/bgdnvk/clanker/internal/hetzner"
	"github.com/bgdnvk/clanker/internal/vercel"
	"github.com/bgdnvk/clanker/internal/verda"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// Version is set at build time via ldflags
var Version = "dev"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "clanker",
	Short:   "AI-powered terminal for Cloud queries",
	Version: Version,
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

	// Add version command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("clanker version %s\n", Version)
		},
	})

	// Add --version / -v flags
	rootCmd.Flags().BoolP("version", "v", false, "Print version information")
	rootCmd.PreRun = func(cmd *cobra.Command, args []string) {
		if v, _ := cmd.Flags().GetBool("version"); v {
			fmt.Printf("clanker version %s\n", Version)
			os.Exit(0)
		}
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.clanker.yaml)")
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug output (shows progress + internal diagnostics)")
	rootCmd.PersistentFlags().Bool("local-mode", true, "enable local mode with rate limiting to prevent system overload (default: true)")
	rootCmd.PersistentFlags().Int("local-delay", 100, "delay in milliseconds between calls in local mode (default 100ms)")

	// Backend integration flags
	rootCmd.PersistentFlags().String("api-key", "", "Backend API key (or set CLANKER_BACKEND_API_KEY)")
	rootCmd.PersistentFlags().String("backend-env", "", "Backend environment: testing, staging, production (or set CLANKER_BACKEND_ENV)")
	rootCmd.PersistentFlags().String("backend-url", "", "Backend API URL, overrides backend-env (or set CLANKER_BACKEND_URL)")

	for _, binding := range []struct {
		key  string
		flag string
	}{
		{"debug", "debug"},
		{"local_mode", "local-mode"},
		{"local_delay_ms", "local-delay"},
		{"backend.api_key", "api-key"},
		{"backend.env", "backend-env"},
		{"backend.url", "backend-url"},
	} {
		if err := viper.BindPFlag(binding.key, rootCmd.PersistentFlags().Lookup(binding.flag)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to bind flag %s: %v\n", binding.flag, err)
		}
	}

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

	// Register Digital Ocean static commands
	doCmd := digitalocean.CreateDigitalOceanCommands()
	rootCmd.AddCommand(doCmd)

	// Register Hetzner static commands
	hetznerCmd := hetzner.CreateHetznerCommands()
	rootCmd.AddCommand(hetznerCmd)

	// Register Vercel static commands + ask stub (phase 1)
	vercelCmd := vercel.CreateVercelCommands()
	AddVercelAskCommand(vercelCmd)
	rootCmd.AddCommand(vercelCmd)

	// Register Verda Cloud static commands + ask subcommand
	verdaCmd := verda.CreateVerdaCommands()
	AddVerdaAskCommand(verdaCmd)
	rootCmd.AddCommand(verdaCmd)
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
