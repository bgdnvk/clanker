package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/cost"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	costProvider  string
	costStartDate string
	costEndDate   string
	costFormat    string
	costOutput    string
	costTop       int
	costTagKey    string
	costGroupBy   string
)

func init() {
	rootCmd.AddCommand(costCmd)

	// Subcommands
	costCmd.AddCommand(costSummaryCmd)
	costCmd.AddCommand(costDetailCmd)
	costCmd.AddCommand(costTrendCmd)
	costCmd.AddCommand(costForecastCmd)
	costCmd.AddCommand(costAnomaliesCmd)
	costCmd.AddCommand(costTagsCmd)
	costCmd.AddCommand(costExportCmd)
	costCmd.AddCommand(costLLMCmd)

	// Persistent flags for all cost commands
	costCmd.PersistentFlags().StringVar(&costProvider, "provider", "all", "Filter by provider (aws, gcp, azure, cloudflare, llm, all)")
	costCmd.PersistentFlags().StringVar(&costStartDate, "start", "", "Start date YYYY-MM-DD (default: first of month)")
	costCmd.PersistentFlags().StringVar(&costEndDate, "end", "", "End date YYYY-MM-DD (default: today)")
	costCmd.PersistentFlags().StringVar(&costFormat, "format", "table", "Output format: table, json")
	costCmd.PersistentFlags().IntVar(&costTop, "top", 10, "Limit results (default: 10)")

	// Export specific flags
	costExportCmd.Flags().StringVar(&costOutput, "output", "", "Output file path (required)")
	costExportCmd.Flags().StringVar(&costGroupBy, "group-by", "provider", "Group by: provider, service, tag")
	costExportCmd.MarkFlagRequired("output")

	// Tags specific flags
	costTagsCmd.Flags().StringVar(&costTagKey, "key", "", "Filter by tag key")
}

// costCmd represents the cost command
var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "View and analyze cloud infrastructure costs",
	Long: `View and analyze cost data across all configured cloud providers.

Supports AWS, GCP, Azure, Cloudflare, and LLM providers.

Examples:
  clanker cost                              # Show cost summary
  clanker cost summary --provider aws       # AWS costs only
  clanker cost detail --provider llm        # LLM cost details
  clanker cost trend --start 2024-01-01     # Cost trend from Jan 1
  clanker cost export --output costs.csv    # Export to CSV`,
	Run: func(cmd *cobra.Command, args []string) {
		// Default to summary when no subcommand is provided
		runCostSummary(cmd, args)
	},
}

// costSummaryCmd shows cost summary
var costSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show cost summary across providers",
	Long: `Display a summary of costs across all configured providers.

Shows total cost, per-provider breakdown, top services, and forecast.

Examples:
  clanker cost summary
  clanker cost summary --provider aws,gcp
  clanker cost summary --start 2024-01-01 --end 2024-01-31`,
	Run: runCostSummary,
}

func runCostSummary(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	client := getCostClient(debug)
	formatter := cost.NewFormatter(costFormat, true)

	startDate, endDate := resolveDateRange()

	summary, err := client.GetSummary(ctx, startDate, endDate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching cost summary: %v\n", err)
		os.Exit(1)
	}

	// Filter by provider if specified
	if costProvider != "" && costProvider != "all" {
		providers := strings.Split(costProvider, ",")
		var filtered []cost.ProviderCost
		for _, pc := range summary.ProviderCosts {
			for _, p := range providers {
				if strings.EqualFold(pc.Provider, strings.TrimSpace(p)) {
					filtered = append(filtered, pc)
					break
				}
			}
		}
		summary.ProviderCosts = filtered

		// Recalculate total
		var total float64
		for _, pc := range filtered {
			total += pc.TotalCost
		}
		summary.TotalCost = total
	}

	output, err := formatter.FormatSummary(summary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
		os.Exit(1)
	}

	formatter.Print(output)
}

// costDetailCmd shows detailed cost breakdown
var costDetailCmd = &cobra.Command{
	Use:   "detail",
	Short: "Show detailed cost breakdown by service",
	Long: `Display detailed cost breakdown by service for a specific provider.

Examples:
  clanker cost detail --provider aws
  clanker cost detail --provider llm --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		formatter := cost.NewFormatter(costFormat, true)

		startDate, endDate := resolveDateRange()

		if costProvider == "all" || costProvider == "" {
			// Get services for all providers
			services, err := client.GetServices(ctx, "", startDate, endDate)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching services: %v\n", err)
				os.Exit(1)
			}

			output, err := formatter.FormatServices(services, costTop)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
				os.Exit(1)
			}
			formatter.Print(output)
		} else {
			// Get provider-specific breakdown
			providerCost, err := client.GetByProvider(ctx, costProvider, startDate, endDate)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching provider costs: %v\n", err)
				os.Exit(1)
			}

			output, err := formatter.FormatProviderCost(providerCost)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
				os.Exit(1)
			}
			formatter.Print(output)
		}
	},
}

// costTrendCmd shows cost trend
var costTrendCmd = &cobra.Command{
	Use:   "trend",
	Short: "Show cost trend over time",
	Long: `Display daily cost trend over the specified period.

Examples:
  clanker cost trend
  clanker cost trend --start 2024-01-01 --end 2024-01-31
  clanker cost trend --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		formatter := cost.NewFormatter(costFormat, true)

		startDate, endDate := resolveDateRange()

		trend, err := client.GetTrend(ctx, startDate, endDate, "daily")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching trend data: %v\n", err)
			os.Exit(1)
		}

		output, err := formatter.FormatTrend(trend)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			os.Exit(1)
		}

		formatter.Print(output)
	},
}

// costForecastCmd shows cost forecast
var costForecastCmd = &cobra.Command{
	Use:   "forecast",
	Short: "Show cost forecast",
	Long: `Display cost forecast for end of month and next month.

Examples:
  clanker cost forecast
  clanker cost forecast --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		formatter := cost.NewFormatter(costFormat, true)

		forecast, err := client.GetForecast(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching forecast: %v\n", err)
			os.Exit(1)
		}

		output, err := formatter.FormatForecast(forecast)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			os.Exit(1)
		}

		formatter.Print(output)
	},
}

// costAnomaliesCmd shows cost anomalies
var costAnomaliesCmd = &cobra.Command{
	Use:   "anomalies",
	Short: "Show detected cost anomalies",
	Long: `Display detected cost anomalies and potential waste.

Examples:
  clanker cost anomalies
  clanker cost anomalies --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		formatter := cost.NewFormatter(costFormat, true)

		anomalies, err := client.GetAnomalies(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching anomalies: %v\n", err)
			os.Exit(1)
		}

		output, err := formatter.FormatAnomalies(anomalies)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			os.Exit(1)
		}

		formatter.Print(output)
	},
}

// costTagsCmd shows cost by tags
var costTagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "Show cost grouped by tags",
	Long: `Display costs grouped by user-defined tags.

Examples:
  clanker cost tags
  clanker cost tags --key Environment
  clanker cost tags --key Team --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		formatter := cost.NewFormatter(costFormat, true)

		startDate, endDate := resolveDateRange()

		tags, err := client.GetTags(ctx, costTagKey, startDate, endDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching tag data: %v\n", err)
			os.Exit(1)
		}

		output, err := formatter.FormatTags(tags)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			os.Exit(1)
		}

		formatter.Print(output)
	},
}

// costLLMCmd shows LLM usage and costs
var costLLMCmd = &cobra.Command{
	Use:   "llm",
	Short: "Show LLM usage and costs",
	Long: `Display LLM usage and cost data for all configured AI providers.

Supports OpenAI, Anthropic, Gemini, DeepSeek, MiniMax, and AWS Bedrock.

Examples:
  clanker cost llm
  clanker cost llm --start 2024-01-01
  clanker cost llm --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		formatter := cost.NewFormatter(costFormat, true)

		startDate, endDate := resolveDateRange()

		usage, err := client.GetLLMUsage(ctx, startDate, endDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching LLM usage: %v\n", err)
			os.Exit(1)
		}

		output, err := formatter.FormatLLMUsage(usage)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			os.Exit(1)
		}

		formatter.Print(output)
	},
}

// costExportCmd exports cost data
var costExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export cost data to file",
	Long: `Export cost data to a file in CSV or JSON format.

Examples:
  clanker cost export --output costs.csv
  clanker cost export --output costs.json --format json
  clanker cost export --output aws-costs.csv --provider aws`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		client := getCostClient(debug)
		exporter := cost.NewExporter()

		startDate, endDate := resolveDateRange()

		// Determine format from output filename if not specified
		format := costFormat
		if format == "table" {
			if strings.HasSuffix(costOutput, ".json") {
				format = "json"
			} else {
				format = "csv"
			}
		}

		// Fetch data based on provider
		var data interface{}
		var err error

		if costProvider == "llm" {
			data, err = client.GetLLMUsage(ctx, startDate, endDate)
		} else if costProvider != "" && costProvider != "all" {
			data, err = client.GetByProvider(ctx, costProvider, startDate, endDate)
		} else {
			data, err = client.GetSummary(ctx, startDate, endDate)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching cost data: %v\n", err)
			os.Exit(1)
		}

		if err := exporter.ExportToFile(data, format, costOutput); err != nil {
			fmt.Fprintf(os.Stderr, "Error exporting data: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Cost data exported to: %s\n", costOutput)
	},
}

// Helper functions

func getCostClient(debug bool) *cost.Client {
	// Try to get backend URL from config
	backendURL := viper.GetString("backend.url")
	if backendURL == "" {
		// Fall back to local development server
		backendURL = "http://localhost:8080"
	}

	return cost.NewClient(backendURL, debug)
}

func resolveDateRange() (string, string) {
	startDate := costStartDate
	endDate := costEndDate

	if startDate == "" {
		// Default to first of current month
		now := time.Now()
		firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		startDate = firstOfMonth.Format("2006-01-02")
	}

	if endDate == "" {
		// Default to today
		endDate = time.Now().Format("2006-01-02")
	}

	return startDate, endDate
}
