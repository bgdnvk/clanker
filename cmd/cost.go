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
	costProfile   string
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
	costCmd.PersistentFlags().StringVar(&costProvider, "provider", "all", "Filter by provider (aws, gcp, azure, cloudflare, all)")
	costCmd.PersistentFlags().StringVar(&costStartDate, "start", "", "Start date YYYY-MM-DD (default: first of month)")
	costCmd.PersistentFlags().StringVar(&costEndDate, "end", "", "End date YYYY-MM-DD (default: today)")
	costCmd.PersistentFlags().StringVar(&costFormat, "format", "table", "Output format: table, json")
	costCmd.PersistentFlags().IntVar(&costTop, "top", 10, "Limit results (default: 10)")
	costCmd.PersistentFlags().StringVar(&costProfile, "profile", "", "AWS profile to use (default: from AWS_PROFILE env)")

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
	Long: `View and analyze cost data directly from cloud provider APIs.

Fetches real cost data using your configured cloud credentials:
- AWS: Uses Cost Explorer API with your AWS profile/credentials
- GCP: Uses Cloud Billing API (coming soon)
- Azure: Uses Cost Management API (coming soon)
- Cloudflare: Uses Analytics API (coming soon)

Examples:
  clanker cost                              # Show cost summary
  clanker cost summary --provider aws       # AWS costs only
  clanker cost detail --provider aws        # AWS service breakdown
  clanker cost trend --start 2024-01-01     # Cost trend from Jan 1
  clanker cost export --output costs.csv    # Export to CSV
  clanker cost --profile myaws              # Use specific AWS profile`,
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
  clanker cost summary --provider aws
  clanker cost summary --start 2024-01-01 --end 2024-01-31`,
	Run: runCostSummary,
}

func runCostSummary(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	debug := viper.GetBool("debug")

	aggregator := getCostAggregator(ctx, debug)
	formatter := cost.NewFormatter(costFormat, true)

	start, end := resolveDateRangeAsTime()

	summary, err := aggregator.GetSummary(ctx, start, end)
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
  clanker cost detail --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		debug := viper.GetBool("debug")

		aggregator := getCostAggregator(ctx, debug)
		formatter := cost.NewFormatter(costFormat, true)

		start, end := resolveDateRangeAsTime()

		if costProvider == "all" || costProvider == "" {
			// Get summary for all providers
			summary, err := aggregator.GetSummary(ctx, start, end)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching costs: %v\n", err)
				os.Exit(1)
			}

			output, err := formatter.FormatServices(summary.TopServices, costTop)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
				os.Exit(1)
			}
			formatter.Print(output)
		} else {
			// Get provider-specific breakdown
			providerCost, err := aggregator.GetByProvider(ctx, costProvider, start, end)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching provider costs: %v\n", err)
				os.Exit(1)
			}

			if providerCost == nil {
				fmt.Fprintf(os.Stderr, "Provider '%s' not configured or no data available\n", costProvider)
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

		aggregator := getCostAggregator(ctx, debug)
		formatter := cost.NewFormatter(costFormat, true)

		start, end := resolveDateRangeAsTime()

		trend, err := aggregator.GetTrend(ctx, start, end)
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

		aggregator := getCostAggregator(ctx, debug)
		formatter := cost.NewFormatter(costFormat, true)

		forecast, err := aggregator.GetForecast(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching forecast: %v\n", err)
			os.Exit(1)
		}

		if forecast == nil {
			fmt.Println("No forecast data available. AWS Cost Explorer forecast requires sufficient historical data.")
			os.Exit(0)
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

		aggregator := getCostAggregator(ctx, debug)
		formatter := cost.NewFormatter(costFormat, true)

		anomalies, err := aggregator.GetAnomalies(ctx)
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

		aggregator := getCostAggregator(ctx, debug)
		formatter := cost.NewFormatter(costFormat, true)

		start, end := resolveDateRangeAsTime()

		tagKey := costTagKey
		if tagKey == "" {
			tagKey = "Environment" // default tag key
		}

		tags, err := aggregator.GetTags(ctx, tagKey, start, end)
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
	Long: `Display LLM usage and cost data.

Note: LLM cost tracking requires the clanker-cloud backend to record usage.
For direct AWS Bedrock costs, use: clanker cost detail --provider aws

Examples:
  clanker cost llm
  clanker cost llm --format json`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("LLM cost tracking is available through the clanker-cloud backend.")
		fmt.Println("For AWS Bedrock costs, use: clanker cost detail --provider aws")
		fmt.Println("\nBedrock costs are included in your AWS bill under service names like:")
		fmt.Println("  - Claude Opus 4.5 (Amazon Bedrock Edition)")
		fmt.Println("  - Claude Haiku 4.5 (Amazon Bedrock Edition)")
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

		aggregator := getCostAggregator(ctx, debug)
		exporter := cost.NewExporter()

		start, end := resolveDateRangeAsTime()

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

		if costProvider != "" && costProvider != "all" {
			data, err = aggregator.GetByProvider(ctx, costProvider, start, end)
		} else {
			data, err = aggregator.GetSummary(ctx, start, end)
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

func getCostAggregator(ctx context.Context, debug bool) *cost.Aggregator {
	aggregator := cost.NewAggregator(debug)

	// Get AWS profile from flag or environment
	awsProfile := costProfile
	if awsProfile == "" {
		awsProfile = os.Getenv("AWS_PROFILE")
	}

	// Initialize AWS provider
	awsProvider, err := cost.NewAWSProvider(ctx, awsProfile, debug)
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[cost] AWS provider not available: %v\n", err)
		}
	} else {
		aggregator.RegisterProvider(awsProvider)
	}

	// TODO: Add GCP provider when implemented
	// gpcProvider, err := cost.NewGCPProvider(ctx, debug)
	// if err == nil {
	//     aggregator.RegisterProvider(gcpProvider)
	// }

	// TODO: Add Azure provider when implemented
	// azureProvider, err := cost.NewAzureProvider(ctx, debug)
	// if err == nil {
	//     aggregator.RegisterProvider(azureProvider)
	// }

	// TODO: Add Cloudflare provider when implemented
	// cfProvider, err := cost.NewCloudflareProvider(ctx, debug)
	// if err == nil {
	//     aggregator.RegisterProvider(cfProvider)
	// }

	return aggregator
}

func resolveDateRangeAsTime() (time.Time, time.Time) {
	var start, end time.Time

	if costStartDate == "" {
		// Default to first of current month
		now := time.Now()
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	} else {
		parsed, err := time.Parse("2006-01-02", costStartDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid start date format: %v\n", err)
			os.Exit(1)
		}
		start = parsed
	}

	if costEndDate == "" {
		// Default to today
		end = time.Now().UTC().Truncate(24 * time.Hour)
	} else {
		parsed, err := time.Parse("2006-01-02", costEndDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid end date format: %v\n", err)
			os.Exit(1)
		}
		end = parsed
	}

	return start, end
}
