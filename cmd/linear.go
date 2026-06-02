package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/linear"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

var linearAskCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask natural-language questions about your Linear workspace",
	Long: `Ask natural-language questions about your Linear workspace using AI.

The assistant fetches relevant Linear data (issues, projects, cycles, teams)
based on the question and replies in markdown. Conversation history is
maintained per-workspace so you can ask follow-ups.

Examples:
  clanker linear ask "what's on my plate this cycle?"
  clanker linear ask "what projects are blocked?"
  clanker linear ask "are there unresolved bugs in the auth-rewrite project?"
  clanker linear ask "what's our current sprint look like for the ENG team?" --team ENG`,
	Args: cobra.ExactArgs(1),
	RunE: runLinearAsk,
}

var (
	linearAskAPIKey      string
	linearAskWorkspaceID string
	linearAskTeam        string
	linearAskAIProfile   string
	linearAskDebug       bool
)

func init() {
	linearAskCmd.Flags().StringVar(&linearAskAPIKey, "api-key", "", "Linear Personal API Key")
	linearAskCmd.Flags().StringVar(&linearAskWorkspaceID, "workspace", "", "Workspace ID")
	linearAskCmd.Flags().StringVar(&linearAskTeam, "team", "", "Default team key (e.g. ENG)")
	linearAskCmd.Flags().StringVar(&linearAskAIProfile, "ai-profile", "", "AI profile to use for LLM queries")
	linearAskCmd.Flags().BoolVar(&linearAskDebug, "debug", false, "Enable debug output")
}

// AddLinearAskCommand wires the ask subcommand onto the base linear command.
func AddLinearAskCommand(linearCmd *cobra.Command) {
	linearCmd.AddCommand(linearAskCmd)
}

func runLinearAsk(cmd *cobra.Command, args []string) error {
	question := strings.TrimSpace(args[0])
	if question == "" {
		return fmt.Errorf("question cannot be empty")
	}

	debug := linearAskDebug || viper.GetBool("debug")

	apiKey := linearAskAPIKey
	if apiKey == "" {
		apiKey = linear.ResolveAPIKey()
	}
	if apiKey == "" {
		return fmt.Errorf("linear api_key is required (set --api-key, LINEAR_API_KEY, or linear.api_key in config)")
	}

	workspaceID := linearAskWorkspaceID
	if workspaceID == "" {
		workspaceID = linear.ResolveWorkspaceID()
	}

	team := linearAskTeam
	if team == "" {
		team = linear.ResolveDefaultTeam()
	}

	client, err := linear.NewClient(apiKey, workspaceID, team, debug)
	if err != nil {
		return fmt.Errorf("create linear client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Use a stable history key — workspaceID if known, else the configured
	// team (e.g. "ENG") so consecutive asks in the same shell session share
	// history before we've fetched the workspace UUID.
	historyKey := workspaceID
	if historyKey == "" {
		historyKey = team
	}
	history := linear.NewConversationHistory(historyKey)
	if err := history.Load(); err != nil && debug {
		fmt.Printf("[debug] load history: %v\n", err)
	}

	if status, err := linear.GatherAccountStatus(ctx, client, workspaceID); err == nil && status != nil {
		if status.WorkspaceID == "" {
			status.WorkspaceID = historyKey
		}
		history.UpdateAccountStatus(status)
	} else if debug {
		fmt.Printf("[debug] gather status: %v\n", err)
	}

	dataContext, err := gatherLinearContext(ctx, client, question, team, debug)
	if err != nil && debug {
		fmt.Printf("[debug] gather context: %v\n", err)
	}

	prompt := buildLinearPrompt(question, dataContext, history.GetRecentContext(5), history.GetAccountStatusContext())

	aiProfile := linearAskAIProfile
	if aiProfile == "" {
		aiProfile = viper.GetString("ai.default_provider")
	}
	apiKey2 := resolveAIKeyForProfile(aiProfile)
	aiClient := ai.NewClient(aiProfile, apiKey2, debug)

	answer, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return fmt.Errorf("AI query failed: %w", err)
	}

	fmt.Println(answer)

	history.AddEntry(question, answer, historyKey)
	if err := history.Save(); err != nil && debug {
		fmt.Printf("[debug] save history: %v\n", err)
	}

	return nil
}

// gatherLinearContext fetches Linear data relevant to the question. Sections
// are picked by keyword routing; matched sections run concurrently because
// they hit independent GraphQL queries.
func gatherLinearContext(ctx context.Context, client *linear.Client, question, team string, debug bool) (string, error) {
	q := strings.ToLower(question)

	wantIssues := containsAny(q, []string{"issue", "bug", "task", "ticket", "blocker", "work", "plate", "mine", "my", "assigned"})
	wantProjects := containsAny(q, []string{"project", "initiative", "delivery", "milestone"})
	wantCycles := containsAny(q, []string{"cycle", "sprint", "iteration"})
	wantTeams := containsAny(q, []string{"team", "squad"})
	wantLabels := containsAny(q, []string{"label", "tag"})

	if !wantIssues && !wantProjects && !wantCycles && !wantTeams && !wantLabels {
		// Default: "what's on my plate" — open issues + active projects + current cycle.
		wantIssues, wantProjects, wantCycles = true, true, true
	}

	g, gctx := errgroup.WithContext(ctx)
	var issuesBlock, projectsBlock, cyclesBlock, teamsBlock, labelsBlock string

	if wantIssues {
		g.Go(func() error {
			filter := linear.IssueFilter{StateType: "started"}
			if team != "" {
				filter.TeamKey = team
			}
			issues, _, err := client.ListIssues(gctx, filter, 25, "")
			if err != nil {
				if debug {
					fmt.Printf("[debug] list issues: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("In-progress issues:\n")
			for _, i := range issues {
				state := ""
				if i.State != nil {
					state = i.State.Name
				}
				assignee := "(unassigned)"
				if i.Assignee != nil {
					assignee = i.Assignee.DisplayName
				}
				fmt.Fprintf(&b, "  - [%s] %s — %s (assignee=%s, priority=%d, updated=%s)\n",
					state, i.Identifier, i.Title, assignee, i.Priority, i.UpdatedAt.Format(time.RFC3339))
			}
			b.WriteString("\n")
			issuesBlock = b.String()
			return nil
		})
	}

	if wantProjects {
		g.Go(func() error {
			projects, _, err := client.ListProjects(gctx, linear.ProjectFilter{State: "started"}, 25, "")
			if err != nil {
				if debug {
					fmt.Printf("[debug] list projects: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Active projects:\n")
			for _, p := range projects {
				fmt.Fprintf(&b, "  - %s (%.0f%% progress) %s\n", p.Name, p.Progress*100, p.URL)
			}
			b.WriteString("\n")
			projectsBlock = b.String()
			return nil
		})
	}

	if wantCycles {
		g.Go(func() error {
			var teamID string
			if team != "" {
				if t, _, err := client.GetTeam(gctx, team); err == nil && t != nil {
					teamID = t.ID
				}
			}
			cycles, err := client.ListCycles(gctx, linear.CycleFilter{TeamID: teamID, IsActive: true})
			if err != nil {
				if debug {
					fmt.Printf("[debug] list cycles: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Current cycles:\n")
			for _, c := range cycles {
				fmt.Fprintf(&b, "  - Cycle %d (%s): %.0f%% complete\n", c.Number, c.Name, c.Progress*100)
			}
			b.WriteString("\n")
			cyclesBlock = b.String()
			return nil
		})
	}

	if wantTeams {
		g.Go(func() error {
			teams, err := client.ListTeams(gctx)
			if err != nil {
				if debug {
					fmt.Printf("[debug] list teams: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Teams:\n")
			for _, t := range teams {
				fmt.Fprintf(&b, "  - %s (%s)\n", t.Key, t.Name)
			}
			b.WriteString("\n")
			teamsBlock = b.String()
			return nil
		})
	}

	if wantLabels {
		g.Go(func() error {
			labels, err := client.ListLabels(gctx, "")
			if err != nil {
				if debug {
					fmt.Printf("[debug] list labels: %v\n", err)
				}
				return nil
			}
			var b strings.Builder
			b.WriteString("Labels:\n")
			for i, l := range labels {
				if i >= 30 {
					b.WriteString("  - (more labels omitted)\n")
					break
				}
				fmt.Fprintf(&b, "  - %s\n", l.Name)
			}
			b.WriteString("\n")
			labelsBlock = b.String()
			return nil
		})
	}

	_ = g.Wait()

	var sb strings.Builder
	sb.WriteString(issuesBlock)
	sb.WriteString(projectsBlock)
	sb.WriteString(cyclesBlock)
	sb.WriteString(teamsBlock)
	sb.WriteString(labelsBlock)

	if sb.Len() == 0 {
		return "No Linear data fetched (check API key permissions and workspace).", nil
	}
	return sb.String(), nil
}

func buildLinearPrompt(question, dataContext, historyContext, statusContext string) string {
	var sb strings.Builder

	sb.WriteString("You are a Linear project-management assistant. ")
	sb.WriteString("Help the user triage work, find blockers, and understand their workspace.\n\n")
	sb.WriteString("Vocabulary cheat-sheet: an *issue* is a work item (ticket/task); a *project* is a delivery effort grouping issues; a *cycle* is a time-boxed sprint; an *identifier* is the human-facing code like `ENG-42`. ")
	sb.WriteString("When citing an issue ALWAYS use its identifier (e.g. ENG-42), not its UUID. ")
	sb.WriteString("Priority values: 0=none, 1=urgent, 2=high, 3=medium, 4=low.\n\n")

	if statusContext != "" {
		sb.WriteString("Workspace status:\n")
		sb.WriteString(statusContext)
		sb.WriteString("\n\n")
	}

	if dataContext != "" {
		sb.WriteString("Linear data:\n")
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

	sb.WriteString("Respond in concise markdown. Reference issues by their identifier (ENG-42) so the user can jump to them.")
	return sb.String()
}
