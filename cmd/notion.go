package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/notion"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

var notionAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural-language questions about your Notion workspace",
	Long: `Ask natural-language questions about your Notion workspace using AI.

The assistant fetches accessible pages and databases based on the question and
replies in markdown. Conversation history is maintained per-workspace so
follow-up questions land in context.

Note: Notion integrations only see content explicitly shared with them.
If results come back empty, share the relevant pages or databases with the
integration via "..." → "Connections" in the Notion UI.

Examples:
  clanker notion ask "where is the prod-RDS-snapshot policy?"
  clanker notion ask "what databases do we track incidents in?"
  clanker notion ask "summarise our last 5 runbooks"`,
	Args: cobra.ExactArgs(1),
	RunE: runNotionAsk,
}

var (
	notionAskToken     string
	notionAskDatabase  string
	notionAskAIProfile string
	notionAskDebug     bool
)

func init() {
	notionAskCmd.Flags().StringVar(&notionAskToken, "token", "", "Notion integration token")
	notionAskCmd.Flags().StringVar(&notionAskDatabase, "database", "", "Default database id")
	notionAskCmd.Flags().StringVar(&notionAskAIProfile, "ai-profile", "", "AI profile to use for LLM queries")
	notionAskCmd.Flags().BoolVar(&notionAskDebug, "debug", false, "Enable debug output")
}

// AddNotionAskCommand wires the ask subcommand onto the base notion command.
func AddNotionAskCommand(notionCmd *cobra.Command) {
	notionCmd.AddCommand(notionAskCmd)
}

func runNotionAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}

	debug := notionAskDebug || viper.GetBool("debug")

	token := notionAskToken
	if token == "" {
		token = notion.ResolveToken()
	}
	if token == "" {
		return fmt.Errorf("notion integration token is required (set --token, NOTION_API_KEY, or notion.integration_token in config)")
	}

	database := notionAskDatabase
	if database == "" {
		database = notion.ResolveDefaultDatabaseID()
	}

	client, err := notion.NewClient(token, database, debug)
	if err != nil {
		return fmt.Errorf("create notion client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	status, _ := notion.GatherAccountStatus(ctx, client)
	workspaceName := ""
	if status != nil {
		workspaceName = status.WorkspaceName
	}
	if workspaceName == "" {
		workspaceName = "default"
	}

	history := notion.NewConversationHistory(workspaceName)
	if err := history.Load(); err != nil && debug {
		fmt.Printf("[debug] load history: %v\n", err)
	}
	if status != nil {
		history.UpdateAccountStatus(status)
	}

	dataContext, err := gatherNotionContext(ctx, client, question, debug)
	if err != nil && debug {
		fmt.Printf("[debug] gather context: %v\n", err)
	}

	prompt := buildNotionPrompt(question, dataContext, history.GetRecentContext(5), history.GetAccountStatusContext())

	aiProfile := notionAskAIProfile
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

	history.AddEntry(question, answer, workspaceName)
	if err := history.Save(); err != nil && debug {
		fmt.Printf("[debug] save history: %v\n", err)
	}

	return nil
}

// gatherNotionContext fetches Notion data relevant to the question. The
// matched sections run concurrently — empty results often mean the
// integration hasn't been shared yet, so the prompt below tells the LLM
// to suggest sharing as the next step.
func gatherNotionContext(ctx context.Context, client *notion.Client, question string, debug bool) (string, error) {
	q := strings.ToLower(question)

	wantPages := containsAny(q, []string{"page", "doc", "document", "runbook", "spec", "design", "rfc", "wiki"})
	wantDatabases := containsAny(q, []string{"database", "db", "table", "row", "tracker", "list"})
	wantUsers := containsAny(q, []string{"user", "who", "person", "owner", "assignee"})

	// Default behaviour: pull a small page sample so the LLM has something
	// to ground its answer in even when the question is vague.
	if !wantPages && !wantDatabases && !wantUsers {
		wantPages = true
		wantDatabases = true
	}

	g, gctx := errgroup.WithContext(ctx)
	var pagesBlock, dbsBlock, usersBlock string

	if wantPages {
		g.Go(func() error {
			pages, err := client.SearchPages(gctx, question, 25)
			if err != nil {
				if debug {
					fmt.Printf("[debug] search pages: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Accessible pages matching the question:\n")
			if len(pages) == 0 {
				b.WriteString("  (none — has the integration been shared with the relevant pages?)\n")
			}
			for _, p := range pages {
				fmt.Fprintf(&b, "  - %s (id=%s, url=%s, edited=%s)\n",
					strings.TrimSpace(notion.TitleOfPage(&p)),
					p.ID,
					p.URL,
					p.LastEditedTime.Format(time.RFC3339),
				)
			}
			b.WriteString("\n")
			pagesBlock = b.String()
			return nil
		})
	}

	if wantDatabases {
		g.Go(func() error {
			dbs, err := client.ListDatabases(gctx, "", 25)
			if err != nil {
				if debug {
					fmt.Printf("[debug] list databases: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Accessible databases:\n")
			if len(dbs) == 0 {
				b.WriteString("  (none — share at least one database with the integration to query rows)\n")
			}
			for _, db := range dbs {
				fmt.Fprintf(&b, "  - %s (id=%s, url=%s)\n", notion.TitleOfDatabase(&db), db.ID, db.URL)
			}
			b.WriteString("\n")
			dbsBlock = b.String()
			return nil
		})
	}

	if wantUsers {
		g.Go(func() error {
			users, err := client.ListUsers(gctx, 25)
			if err != nil {
				if debug {
					fmt.Printf("[debug] list users: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Workspace users:\n")
			for _, u := range users {
				email := ""
				if u.Person != nil {
					email = u.Person.Email
				}
				fmt.Fprintf(&b, "  - %s (%s, %s)\n", u.Name, u.Type, email)
			}
			b.WriteString("\n")
			usersBlock = b.String()
			return nil
		})
	}

	_ = g.Wait()

	var sb strings.Builder
	sb.WriteString(pagesBlock)
	sb.WriteString(dbsBlock)
	sb.WriteString(usersBlock)

	if sb.Len() == 0 {
		return "No Notion data fetched (check the integration token and that pages have been shared with the integration).", nil
	}
	return sb.String(), nil
}

func buildNotionPrompt(question, dataContext, historyContext, statusContext string) string {
	var sb strings.Builder

	sb.WriteString("You are a Notion knowledge-base assistant. ")
	sb.WriteString("Help the user navigate their Notion workspace and answer questions about pages, databases, and users.\n\n")
	sb.WriteString("Vocabulary cheat-sheet: a *page* is a document (long-form prose). ")
	sb.WriteString("A *database* is a typed table whose rows are themselves pages. ")
	sb.WriteString("A *block* is a content atom (paragraph, heading, list item).\n\n")
	sb.WriteString("Important — Notion integrations only see content explicitly shared with them. ")
	sb.WriteString("If the data section is empty or the user's target isn't there, suggest they share the relevant page or database with the integration via \"...\" → \"Connections\" in Notion.\n\n")

	if statusContext != "" {
		sb.WriteString("Workspace status:\n")
		sb.WriteString(statusContext)
		sb.WriteString("\n\n")
	}

	if dataContext != "" {
		sb.WriteString("Notion data:\n")
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

	sb.WriteString("Respond in concise markdown. Cite page titles plus URLs when referencing specific pages.")
	return sb.String()
}
