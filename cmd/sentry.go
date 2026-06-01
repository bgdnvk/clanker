package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/sentry"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var sentryAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural-language questions about your Sentry organisation",
	Long: `Ask natural-language questions about your Sentry organisation using AI.

The assistant fetches relevant Sentry data (issues, events, releases, alerts,
monitors) based on the question and replies in markdown. Conversation history
is maintained per-org so you can ask follow-up questions.

Examples:
  clanker sentry ask "what's the worst error today?"
  clanker sentry ask "any new errors since the last release?"
  clanker sentry ask "are any monitors failing?"
  clanker sentry ask "show me unresolved issues in prod" --environment prod`,
	Args: cobra.ExactArgs(1),
	RunE: runSentryAsk,
}

var (
	sentryAskAuthToken   string
	sentryAskOrgSlug     string
	sentryAskProject     string
	sentryAskHost        string
	sentryAskEnvironment string
	sentryAskAIProfile   string
	sentryAskDebug       bool
)

func init() {
	sentryAskCmd.Flags().StringVar(&sentryAskAuthToken, "auth-token", "", "Sentry User Auth Token")
	sentryAskCmd.Flags().StringVar(&sentryAskOrgSlug, "org", "", "Sentry org slug")
	sentryAskCmd.Flags().StringVar(&sentryAskProject, "project", "", "Default Sentry project slug")
	sentryAskCmd.Flags().StringVar(&sentryAskHost, "host", "", "Sentry host (default sentry.io)")
	sentryAskCmd.Flags().StringVar(&sentryAskEnvironment, "environment", "", "Filter to a specific environment")
	sentryAskCmd.Flags().StringVar(&sentryAskAIProfile, "ai-profile", "", "AI profile to use for LLM queries")
	sentryAskCmd.Flags().BoolVar(&sentryAskDebug, "debug", false, "Enable debug output")
}

// AddSentryAskCommand wires the ask subcommand onto the base sentry command.
func AddSentryAskCommand(sentryCmd *cobra.Command) {
	sentryCmd.AddCommand(sentryAskCmd)
}

func runSentryAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}

	debug := sentryAskDebug || viper.GetBool("debug")

	authToken := sentryAskAuthToken
	if authToken == "" {
		authToken = sentry.ResolveAuthToken()
	}
	if authToken == "" {
		return fmt.Errorf("sentry auth token is required (set --auth-token, SENTRY_AUTH_TOKEN, or sentry.auth_token in config)")
	}

	org := sentryAskOrgSlug
	if org == "" {
		org = sentry.ResolveOrgSlug()
	}
	if org == "" {
		return fmt.Errorf("sentry org slug is required (set --org, SENTRY_ORG, or sentry.org_slug in config)")
	}

	host := sentryAskHost
	if host == "" {
		host = sentry.ResolveHost()
	}

	project := sentryAskProject
	if project == "" {
		project = sentry.ResolveDefaultProject()
	}

	client, err := sentry.NewClient(authToken, org, host, debug)
	if err != nil {
		return fmt.Errorf("create sentry client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	history := sentry.NewConversationHistory(org)
	if err := history.Load(); err != nil && debug {
		fmt.Printf("[debug] load history: %v\n", err)
	}

	if status, err := sentry.GatherAccountStatus(ctx, client, org); err == nil && status != nil {
		history.UpdateAccountStatus(status)
	} else if debug {
		fmt.Printf("[debug] gather status: %v\n", err)
	}

	// Renamed from `context` because that local was shadowing the imported
	// context package — a foot-gun for any future edit that needs to call
	// context.WithTimeout / context.Background below this line.
	dataContext, err := gatherSentryContext(ctx, client, question, project, sentryAskEnvironment, debug)
	if err != nil && debug {
		fmt.Printf("[debug] gather context: %v\n", err)
	}

	prompt := buildSentryPrompt(question, dataContext, history.GetRecentContext(5), history.GetAccountStatusContext())

	aiProfile := sentryAskAIProfile
	if aiProfile == "" {
		aiProfile = viper.GetString("ai.default_provider")
	}
	apiKey := resolveAIKeyForProfile(aiProfile)
	aiClient := ai.NewClient(aiProfile, apiKey, debug)

	answer, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return fmt.Errorf("AI query failed: %w", err)
	}

	fmt.Println(answer)

	history.AddEntry(question, answer, org)
	if err := history.Save(); err != nil && debug {
		fmt.Printf("[debug] save history: %v\n", err)
	}

	return nil
}

// gatherSentryContext fetches Sentry data relevant to the question. Keyword
// routing is deliberately lightweight — Sentry's search syntax is rich enough
// that we mostly want to forward `is:unresolved`-style filters through and let
// the LLM correlate from a focused page of issues + recent releases.
func gatherSentryContext(ctx context.Context, client *sentry.Client, question, project, environment string, debug bool) (string, error) {
	questionLower := strings.ToLower(question)

	var sb strings.Builder

	wantIssues := containsAny(questionLower, []string{"issue", "error", "crash", "exception", "problem", "fail", "bug", "what's", "whats", "blowing", "broken"})
	wantReleases := containsAny(questionLower, []string{"release", "deploy", "version", "rollout", "regress"})
	wantMonitors := containsAny(questionLower, []string{"monitor", "cron", "schedule"})
	wantAlerts := containsAny(questionLower, []string{"alert", "rule", "notify", "page"})

	// Default to issues if the question doesn't pattern-match — most "what's
	// going on" questions want issues first.
	if !wantIssues && !wantReleases && !wantMonitors && !wantAlerts {
		wantIssues = true
	}

	if wantIssues {
		query := "is:unresolved"
		if strings.Contains(questionLower, "error") || strings.Contains(questionLower, "crash") {
			query = "is:unresolved level:error"
		}
		issues, _, err := client.ListIssues(ctx, client.OrgSlug(), sentry.IssueListOptions{
			Query:       query,
			Environment: environment,
			StatsPeriod: "24h",
			Limit:       25,
		})
		if err != nil {
			if debug {
				fmt.Printf("[debug] list issues: %v\n", err)
			}
		} else {
			sb.WriteString("Recent unresolved issues (last 24h):\n")
			for _, i := range issues {
				sb.WriteString(fmt.Sprintf("  - [%s] %s — %s (count=%s, users=%d, lastSeen=%s)\n",
					i.Level, i.ShortID, i.Title, i.Count, i.UserCount, i.LastSeen.Format(time.RFC3339)))
			}
			sb.WriteString("\n")
		}
	}

	if wantReleases && project != "" {
		releases, err := client.ListReleases(ctx, client.OrgSlug(), project)
		if err != nil {
			if debug {
				fmt.Printf("[debug] list releases: %v\n", err)
			}
		} else {
			sb.WriteString("Recent releases:\n")
			for i, r := range releases {
				if i >= 10 {
					break
				}
				released := "(unreleased)"
				if r.DateReleased != nil && !r.DateReleased.IsZero() {
					released = r.DateReleased.Format(time.RFC3339)
				}
				sb.WriteString(fmt.Sprintf("  - %s (newGroups=%d, released=%s)\n", r.ShortVersion, r.NewGroups, released))
			}
			sb.WriteString("\n")
		}
	}

	if wantMonitors {
		monitors, err := client.ListMonitors(ctx, client.OrgSlug())
		if err != nil {
			if debug {
				fmt.Printf("[debug] list monitors: %v\n", err)
			}
		} else {
			sb.WriteString("Sentry Crons monitors:\n")
			for _, m := range monitors {
				sb.WriteString(fmt.Sprintf("  - %s (%s) status=%s muted=%v\n", m.Slug, m.Name, m.Status, m.IsMuted))
			}
			sb.WriteString("\n")
		}
	}

	if wantAlerts && project != "" {
		rules, err := client.ListIssueAlertRules(ctx, client.OrgSlug(), project)
		if err != nil {
			if debug {
				fmt.Printf("[debug] list alert rules: %v\n", err)
			}
		} else {
			sb.WriteString("Alert rules:\n")
			for _, r := range rules {
				sb.WriteString(fmt.Sprintf("  - %s (env=%s, frequency=%dmin)\n", r.Name, r.Environment, r.Frequency))
			}
			sb.WriteString("\n")
		}
	}

	if sb.Len() == 0 {
		return "No Sentry data fetched (check token permissions and org slug).", nil
	}
	return sb.String(), nil
}

func buildSentryPrompt(question, dataContext, historyContext, statusContext string) string {
	var sb strings.Builder

	sb.WriteString("You are a Sentry observability assistant. ")
	sb.WriteString("Help the user triage and understand error patterns in their Sentry organisation.\n\n")
	sb.WriteString("Vocabulary cheat-sheet: an *issue* is a deduplicated group of errors with the same fingerprint; ")
	sb.WriteString("an *event* is a single occurrence. ")
	sb.WriteString("`count` is total occurrences; `userCount` is unique users affected. ")
	sb.WriteString("Recommend the next investigative step where useful.\n\n")

	if statusContext != "" {
		sb.WriteString("Org status:\n")
		sb.WriteString(statusContext)
		sb.WriteString("\n\n")
	}

	if dataContext != "" {
		sb.WriteString("Sentry data:\n")
		sb.WriteString(dataContext)
		sb.WriteString("\n")
	}

	if historyContext != "" {
		sb.WriteString("Recent conversation:\n")
		sb.WriteString(historyContext)
		sb.WriteString("\n")
	}

	sb.WriteString("User question: ")
	sb.WriteString(question)
	sb.WriteString("\n\n")

	sb.WriteString("Respond in concise markdown. When you reference an issue, include its short ID (e.g. BACKEND-42) so the user can jump to it.")
	return sb.String()
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// resolveAIKeyForProfile mirrors the dispatch in cmd/cf.go so the ask command
// finds the right API key for the configured AI provider.
func resolveAIKeyForProfile(profile string) string {
	switch profile {
	case "bedrock", "claude", "gemini":
		return ""
	case "gemini-api":
		return resolveGeminiAPIKey("")
	case "openai":
		return resolveOpenAIKey("")
	case "anthropic":
		return resolveAnthropicKey("")
	case "deepseek":
		return resolveDeepSeekKey("")
	case "cohere":
		return resolveCohereKey("")
	case "minimax":
		return resolveMiniMaxKey("")
	default:
		return viper.GetString(fmt.Sprintf("ai.providers.%s.api_key", profile))
	}
}
