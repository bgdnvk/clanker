package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/cloudflare/dns"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural language questions about your Cloudflare account",
	Long: `Ask natural language questions about your Cloudflare account using AI.

The AI will analyze your question, determine what Cloudflare operations are needed,
execute them, and provide a comprehensive markdown-formatted response.

Conversation history is maintained per account for follow-up questions.

Examples:
  clanker cf ask "list all my zones"
  clanker cf ask "show dns records for example.com"
  clanker cf ask "what workers do I have deployed"
  clanker cf ask "list my tunnels"
  clanker cf ask "show firewall rules for example.com"`,
	Args: cobra.ExactArgs(1),
	RunE: runCfAsk,
}

// Flags
var (
	cfAskAccountID string
	cfAskAPIToken  string
	cfAskZone      string
	cfAskAIProfile string
	cfAskDebug     bool
)

func init() {
	// Add ask command to the cloudflare command (registered in root.go)
	// The static commands are added via CreateCloudflareCommands()

	// Ask command flags
	cfAskCmd.Flags().StringVar(&cfAskAccountID, "account-id", "", "Cloudflare account ID")
	cfAskCmd.Flags().StringVar(&cfAskAPIToken, "api-token", "", "Cloudflare API token")
	cfAskCmd.Flags().StringVar(&cfAskZone, "zone", "", "Default zone name for zone-specific queries")
	cfAskCmd.Flags().StringVar(&cfAskAIProfile, "ai-profile", "", "AI profile to use for LLM queries")
	cfAskCmd.Flags().BoolVar(&cfAskDebug, "debug", false, "Enable debug output")
}

// AddCfAskCommand adds the ask subcommand to the cf command
func AddCfAskCommand(cfCmd *cobra.Command) {
	cfCmd.AddCommand(cfAskCmd)
}

func runCfAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}

	debug := cfAskDebug || viper.GetBool("debug")

	// Resolve account ID
	accountID := cfAskAccountID
	if accountID == "" {
		accountID = cloudflare.ResolveAccountID()
	}

	// Resolve API token
	apiToken := cfAskAPIToken
	if apiToken == "" {
		apiToken = cloudflare.ResolveAPIToken()
	}

	if apiToken == "" {
		return fmt.Errorf("cloudflare API token is required (set via --api-token, CLOUDFLARE_API_TOKEN, or cloudflare.api_token in config)")
	}

	// Create Cloudflare client
	client, err := cloudflare.NewClient(accountID, apiToken, debug)
	if err != nil {
		return fmt.Errorf("failed to create Cloudflare client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Load conversation history
	conversationID := accountID
	if conversationID == "" {
		conversationID = "default"
	}
	history := cloudflare.NewConversationHistory(conversationID)
	if err := history.Load(); err != nil && debug {
		fmt.Printf("[debug] failed to load conversation history: %v\n", err)
	}

	// Gather account status for context
	status, err := cloudflare.GatherAccountStatus(ctx, client)
	if err != nil && debug {
		fmt.Printf("[debug] failed to gather account status: %v\n", err)
	}
	if status != nil {
		history.UpdateAccountStatus(status)
	}

	// Determine query type and route to appropriate handler
	answer, err := handleCfQuery(ctx, client, question, history, debug)
	if err != nil {
		return err
	}

	// Print the answer
	fmt.Println(answer)

	// Save conversation history
	history.AddEntry(question, answer, conversationID)
	if err := history.Save(); err != nil && debug {
		fmt.Printf("[debug] failed to save conversation history: %v\n", err)
	}

	return nil
}

func handleCfQuery(ctx context.Context, client *cloudflare.Client, question string, history *cloudflare.ConversationHistory, debug bool) (string, error) {
	questionLower := strings.ToLower(question)

	// Route to appropriate subagent based on query content
	if isDNSQuery(questionLower) {
		return handleDNSQuery(ctx, client, question, debug)
	}

	// For general queries, get context and use AI
	return handleGeneralCfQuery(ctx, client, question, history, debug)
}

func isDNSQuery(query string) bool {
	dnsKeywords := []string{
		"dns", "zone", "record", "a record", "aaaa", "cname", "mx", "txt",
		"nameserver", "ns record", "srv", "caa", "domain",
	}

	for _, keyword := range dnsKeywords {
		if strings.Contains(query, keyword) {
			return true
		}
	}
	return false
}

func handleDNSQuery(ctx context.Context, client *cloudflare.Client, question string, debug bool) (string, error) {
	// Create DNS subagent
	dnsAgent := dns.NewSubAgent(client, debug)

	opts := dns.QueryOptions{}
	if cfAskZone != "" {
		opts.ZoneName = cfAskZone
	}

	response, err := dnsAgent.HandleQuery(ctx, question, opts)
	if err != nil {
		return "", fmt.Errorf("DNS query failed: %w", err)
	}

	switch response.Type {
	case dns.ResponseTypeResult:
		return response.Result, nil
	case dns.ResponseTypePlan:
		// For modifications, show the plan
		return formatDNSPlan(response.Plan), nil
	case dns.ResponseTypeError:
		return "", response.Error
	default:
		return response.Message, nil
	}
}

func formatDNSPlan(plan *dns.Plan) string {
	if plan == nil {
		return "No plan generated."
	}

	var sb strings.Builder
	sb.WriteString("DNS Modification Plan:\n")
	sb.WriteString(fmt.Sprintf("\nSummary: %s\n\n", plan.Summary))
	sb.WriteString("Commands to execute:\n")

	for i, cmd := range plan.Commands {
		sb.WriteString(fmt.Sprintf("\n%d. %s %s\n", i+1, cmd.Method, cmd.Endpoint))
		sb.WriteString(fmt.Sprintf("   Reason: %s\n", cmd.Reason))
		if cmd.Body != "" {
			sb.WriteString(fmt.Sprintf("   Body: %s\n", cmd.Body))
		}
	}

	sb.WriteString("\nTo apply this plan, use: clanker cf apply <plan-file>\n")
	return sb.String()
}

func handleGeneralCfQuery(ctx context.Context, client *cloudflare.Client, question string, history *cloudflare.ConversationHistory, debug bool) (string, error) {
	// Get relevant context from Cloudflare
	cfContext, err := client.GetRelevantContext(ctx, question)
	if err != nil && debug {
		fmt.Printf("[debug] failed to get Cloudflare context: %v\n", err)
	}

	// Get conversation history context
	historyContext := history.GetRecentContext(5)
	statusContext := history.GetAccountStatusContext()

	// Build prompt for AI
	prompt := buildCfPrompt(question, cfContext, historyContext, statusContext)

	// Get AI profile
	aiProfile := cfAskAIProfile
	if aiProfile == "" {
		aiProfile = viper.GetString("ai.default_provider")
	}

	// Create AI client
	apiKey := viper.GetString(fmt.Sprintf("ai.providers.%s.api_key", aiProfile))
	aiClient := ai.NewClient(aiProfile, apiKey, debug)

	// Get AI response
	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("AI query failed: %w", err)
	}

	return response, nil
}

func buildCfPrompt(question, cfContext, historyContext, statusContext string) string {
	var sb strings.Builder

	sb.WriteString("You are a Cloudflare infrastructure assistant. ")
	sb.WriteString("Answer questions about the user's Cloudflare account based on the provided context.\n\n")

	if statusContext != "" {
		sb.WriteString("Account Status:\n")
		sb.WriteString(statusContext)
		sb.WriteString("\n\n")
	}

	if cfContext != "" {
		sb.WriteString("Cloudflare Context:\n")
		sb.WriteString(cfContext)
		sb.WriteString("\n\n")
	}

	if historyContext != "" {
		sb.WriteString("Recent Conversation:\n")
		sb.WriteString(historyContext)
		sb.WriteString("\n\n")
	}

	sb.WriteString("User Question: ")
	sb.WriteString(question)
	sb.WriteString("\n\n")

	sb.WriteString("Provide a helpful, concise response in markdown format.")

	return sb.String()
}
