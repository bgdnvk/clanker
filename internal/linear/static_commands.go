package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateLinearCommands builds the `clanker linear` command tree. The ask
// subcommand is added separately by cmd/linear.go so internal/linear doesn't
// import internal/ai.
func CreateLinearCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "linear",
		Short:   "Query Linear issues, projects, cycles, teams, and docs",
		Long:    "Query and manage Linear directly. Useful for scripting and CI hooks.",
		Aliases: []string{"lin"},
	}

	cmd.PersistentFlags().String("api-key", "", "Linear Personal API Key")
	cmd.PersistentFlags().String("workspace", "", "Workspace ID (overrides config)")
	cmd.PersistentFlags().String("team", "", "Default team key (e.g. ENG) — overrides config")
	cmd.PersistentFlags().String("format", "table", "Output format: table | json")

	cmd.AddCommand(buildListCommand())
	cmd.AddCommand(buildGetCommand())
	cmd.AddCommand(buildResolveCommand())
	cmd.AddCommand(buildAssignCommand())
	cmd.AddCommand(buildCommentCommand())
	cmd.AddCommand(buildCreateCommand())
	cmd.AddCommand(buildUpdateCommand())
	cmd.AddCommand(buildLabelCommand())

	return cmd
}

// linearFlag reads a persistent flag from any depth in the linear command
// tree. cmd.Flags() merges inherited persistent flags from every ancestor,
// so this resolves flags registered on the `linear` parent even when called
// from 3-level-deep leaves.
func linearFlag(cmd *cobra.Command, name string) string {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// buildClient resolves credentials and flags into a ready *Client plus the
// effective workspace ID (which callers often need for history scoping).
func buildClient(cmd *cobra.Command) (*Client, string, error) {
	apiKey := linearFlag(cmd, "api-key")
	if apiKey == "" {
		apiKey = ResolveAPIKey()
	}
	if apiKey == "" {
		return nil, "", fmt.Errorf("linear api_key is required (set linear.api_key, LINEAR_API_KEY, or --api-key)")
	}
	workspaceID := linearFlag(cmd, "workspace")
	if workspaceID == "" {
		workspaceID = ResolveWorkspaceID()
	}
	team := linearFlag(cmd, "team")
	if team == "" {
		team = ResolveDefaultTeam()
	}
	debug := viper.GetBool("debug")
	client, err := NewClient(apiKey, workspaceID, team, debug)
	if err != nil {
		return nil, "", err
	}
	return client, workspaceID, nil
}

func buildListCommand() *cobra.Command {
	listCmd := &cobra.Command{
		Use:   "list <resource>",
		Short: "List Linear resources",
		Long: `List Linear resources of a specific type.

Supported resources:
  issues      - Issues (filter by --state / --team / --project / --label / --assignee)
  projects    - Projects (filter by --state)
  teams       - Teams
  cycles      - Cycles (filter by --team / --active / --future)
  labels      - Issue labels (filter by --team)
  users       - Workspace users
  docs        - Project documents`,
		Args: cobra.ExactArgs(1),
		RunE: runList,
	}
	listCmd.Flags().String("state", "", "State filter (issues: started/completed/cancelled/backlog/triage; projects: backlog/planned/started/paused/completed/canceled)")
	listCmd.Flags().String("project", "", "Project UUID filter")
	listCmd.Flags().String("cycle", "", "Cycle UUID filter")
	listCmd.Flags().String("label", "", "Filter by exact label name")
	listCmd.Flags().String("assignee", "", "Filter by assignee user ID")
	listCmd.Flags().Bool("active", false, "Cycles: only currently-active cycles")
	listCmd.Flags().Bool("future", false, "Cycles: only future cycles")
	listCmd.Flags().Int("limit", 0, "Max rows to return")
	return listCmd
}

func buildGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <resource> <id-or-identifier>",
		Short: "Get a single Linear resource",
		Long: `Get a single Linear resource by ID (UUID) or human identifier.

Examples:
  clanker linear get issue 2b0e3c00-9c4f-4b6a-9b4f-...
  clanker linear get issue ENG-123        # by identifier
  clanker linear get project <uuid>
  clanker linear get cycle <uuid>`,
		Args: cobra.ExactArgs(2),
		RunE: runGet,
	}
}

func buildResolveCommand() *cobra.Command {
	resolve := &cobra.Command{
		Use:   "resolve <issue-id> [issue-id...]",
		Short: "Mark issues as done (moves to first 'completed'-type state on the issue's team)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateIssuesToStateType(cmd, args, "completed")
		},
	}
	return resolve
}

func buildAssignCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <issue-id> <username-or-email>",
		Short: "Assign an issue to a user",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			user, err := client.FindUserByDisplayName(ctx, args[1])
			if err != nil {
				return fmt.Errorf("find user %q: %w", args[1], err)
			}
			if user == nil {
				return fmt.Errorf("no user matched %q", args[1])
			}
			issue, err := client.UpdateIssue(ctx, args[0], UpdateIssueInput{AssigneeID: &user.ID})
			if err != nil {
				return err
			}
			fmt.Printf("Assigned %s to %s\n", issue.Identifier, user.DisplayName)
			return nil
		},
	}
}

func buildCommentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "comment <issue-id> <body>",
		Short: "Post a comment on an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := client.AddComment(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("Posted comment %s\n", c.ID)
			return nil
		},
	}
}

func buildCreateCommand() *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create Linear resources (issue, project, cycle)",
	}
	createIssue := &cobra.Command{
		Use:   "issue",
		Short: "Create an issue",
		RunE: func(cmd *cobra.Command, args []string) error {
			title, _ := cmd.Flags().GetString("title")
			body, _ := cmd.Flags().GetString("body")
			teamID, _ := cmd.Flags().GetString("team-id")
			projectID, _ := cmd.Flags().GetString("project-id")
			assigneeID, _ := cmd.Flags().GetString("assignee-id")
			priority, _ := cmd.Flags().GetInt("priority")
			labels, _ := cmd.Flags().GetStringSlice("label-id")
			if title == "" || teamID == "" {
				return fmt.Errorf("--title and --team-id are required")
			}
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			issue, err := client.CreateIssue(ctx, CreateIssueInput{
				Title:       title,
				Description: body,
				TeamID:      teamID,
				ProjectID:   projectID,
				AssigneeID:  assigneeID,
				Priority:    priority,
				LabelIDs:    labels,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Created %s: %s\n", issue.Identifier, issue.URL)
			return nil
		},
	}
	createIssue.Flags().String("title", "", "Issue title (required)")
	createIssue.Flags().String("body", "", "Issue description (markdown)")
	createIssue.Flags().String("team-id", "", "Team UUID (required)")
	createIssue.Flags().String("project-id", "", "Project UUID")
	createIssue.Flags().String("assignee-id", "", "Assignee user UUID")
	createIssue.Flags().Int("priority", 0, "Priority: 1 (urgent) | 2 (high) | 3 (medium) | 4 (low)")
	createIssue.Flags().StringSlice("label-id", nil, "Label UUIDs (repeatable)")
	create.AddCommand(createIssue)

	createProject := &cobra.Command{
		Use:   "project",
		Short: "Create a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			desc, _ := cmd.Flags().GetString("description")
			teams, _ := cmd.Flags().GetStringSlice("team-id")
			lead, _ := cmd.Flags().GetString("lead-id")
			if name == "" || len(teams) == 0 {
				return fmt.Errorf("--name and at least one --team-id required")
			}
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			p, err := client.CreateProject(ctx, CreateProjectInput{
				Name:        name,
				Description: desc,
				TeamIDs:     teams,
				LeadID:      lead,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Created project %s: %s\n", p.Name, p.URL)
			return nil
		},
	}
	createProject.Flags().String("name", "", "Project name (required)")
	createProject.Flags().String("description", "", "Description (markdown)")
	createProject.Flags().StringSlice("team-id", nil, "Team UUIDs (repeatable, at least one)")
	createProject.Flags().String("lead-id", "", "Lead user UUID")
	create.AddCommand(createProject)

	createCycle := &cobra.Command{
		Use:   "cycle",
		Short: "Create a cycle",
		RunE: func(cmd *cobra.Command, args []string) error {
			teamID, _ := cmd.Flags().GetString("team-id")
			name, _ := cmd.Flags().GetString("name")
			startsAt, _ := cmd.Flags().GetString("starts-at")
			endsAt, _ := cmd.Flags().GetString("ends-at")
			if teamID == "" || startsAt == "" || endsAt == "" {
				return fmt.Errorf("--team-id, --starts-at, --ends-at required")
			}
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cyc, err := client.CreateCycle(ctx, CreateCycleInput{TeamID: teamID, Name: name, StartsAt: startsAt, EndsAt: endsAt})
			if err != nil {
				return err
			}
			fmt.Printf("Created cycle %s (number %d)\n", cyc.Name, cyc.Number)
			return nil
		},
	}
	createCycle.Flags().String("team-id", "", "Team UUID (required)")
	createCycle.Flags().String("name", "", "Cycle name")
	createCycle.Flags().String("starts-at", "", "ISO-8601 start (required)")
	createCycle.Flags().String("ends-at", "", "ISO-8601 end (required)")
	create.AddCommand(createCycle)

	return create
}

func buildUpdateCommand() *cobra.Command {
	update := &cobra.Command{
		Use:   "update",
		Short: "Update Linear resources (issue, project)",
	}
	updateIssue := &cobra.Command{
		Use:   "issue <id>",
		Short: "Update an issue's state, assignee, priority, etc.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := UpdateIssueInput{}
			if v, _ := cmd.Flags().GetString("title"); v != "" {
				input.Title = &v
			}
			if v, _ := cmd.Flags().GetString("description"); v != "" {
				input.Description = &v
			}
			if v, _ := cmd.Flags().GetString("state-id"); v != "" {
				input.StateID = &v
			}
			if v, _ := cmd.Flags().GetString("assignee-id"); v != "" {
				input.AssigneeID = &v
			}
			if v, _ := cmd.Flags().GetString("project-id"); v != "" {
				input.ProjectID = &v
			}
			if v, _ := cmd.Flags().GetString("cycle-id"); v != "" {
				input.CycleID = &v
			}
			if v, _ := cmd.Flags().GetInt("priority"); v > 0 {
				input.Priority = &v
			}
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			issue, err := client.UpdateIssue(ctx, args[0], input)
			if err != nil {
				return err
			}
			fmt.Printf("Updated %s\n", issue.Identifier)
			return nil
		},
	}
	updateIssue.Flags().String("title", "", "New title")
	updateIssue.Flags().String("description", "", "New description (markdown)")
	updateIssue.Flags().String("state-id", "", "Move to this workflow state (UUID)")
	updateIssue.Flags().String("assignee-id", "", "Reassign to this user (UUID)")
	updateIssue.Flags().String("project-id", "", "Move to this project (UUID)")
	updateIssue.Flags().String("cycle-id", "", "Move to this cycle (UUID)")
	updateIssue.Flags().Int("priority", 0, "Priority: 1 | 2 | 3 | 4")
	update.AddCommand(updateIssue)

	return update
}

func buildLabelCommand() *cobra.Command {
	lc := &cobra.Command{
		Use:   "label",
		Short: "Manage issue labels",
	}
	lc.AddCommand(&cobra.Command{
		Use:   "create <name> <team-id>",
		Short: "Create a label on a team. Useful for the infra:<type>:<id> annotation convention.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			color, _ := cmd.Flags().GetString("color")
			client, _, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			lbl, err := client.CreateLabel(ctx, args[1], args[0], color)
			if err != nil {
				return err
			}
			fmt.Printf("Created label %s (%s)\n", lbl.Name, lbl.ID)
			return nil
		},
	})
	if create := lc.Commands()[0]; create != nil {
		create.Flags().String("color", "", "Hex color e.g. #5e6ad2 (optional)")
	}
	return lc
}

func updateIssuesToStateType(cmd *cobra.Command, ids []string, targetType string) error {
	client, _, err := buildClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Linear doesn't have a built-in "set state to first 'completed' state
	// on this issue's team" — we have to look it up. To keep it simple we
	// fetch each issue, ask its team for states, pick the matching one.
	for _, id := range ids {
		issue, err := client.GetIssue(ctx, id)
		if err != nil {
			return fmt.Errorf("get %s: %w", id, err)
		}
		if issue.Team == nil {
			return fmt.Errorf("%s has no team — cannot pick target state", issue.Identifier)
		}
		_, states, err := client.GetTeam(ctx, issue.Team.ID)
		if err != nil {
			return fmt.Errorf("get team for %s: %w", issue.Identifier, err)
		}
		var stateID string
		for _, s := range states {
			if s.Type == targetType {
				stateID = s.ID
				break
			}
		}
		if stateID == "" {
			return fmt.Errorf("no %q-type state found on team %s", targetType, issue.Team.Key)
		}
		if _, err := client.UpdateIssue(ctx, id, UpdateIssueInput{StateID: &stateID}); err != nil {
			return fmt.Errorf("update %s: %w", issue.Identifier, err)
		}
		fmt.Printf("Moved %s → %s\n", issue.Identifier, targetType)
	}
	return nil
}

// runList ----------------------------------------------------------------

func runList(cmd *cobra.Command, args []string) error {
	resource := strings.ToLower(args[0])
	client, _, err := buildClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	format := linearFlag(cmd, "format")
	team := linearFlag(cmd, "team")
	limit, _ := cmd.Flags().GetInt("limit")

	switch resource {
	case "issues":
		state, _ := cmd.Flags().GetString("state")
		project, _ := cmd.Flags().GetString("project")
		cycle, _ := cmd.Flags().GetString("cycle")
		label, _ := cmd.Flags().GetString("label")
		assignee, _ := cmd.Flags().GetString("assignee")
		filter := IssueFilter{
			StateType:  state,
			TeamKey:    team,
			ProjectID:  project,
			CycleID:    cycle,
			LabelName:  label,
			AssigneeID: assignee,
		}
		issues, _, err := client.ListIssues(ctx, filter, limit, "")
		if err != nil {
			return err
		}
		return renderIssues(issues, format)

	case "projects":
		state, _ := cmd.Flags().GetString("state")
		projects, _, err := client.ListProjects(ctx, ProjectFilter{State: state}, limit, "")
		if err != nil {
			return err
		}
		return renderProjects(projects, format)

	case "teams":
		teams, err := client.ListTeams(ctx)
		if err != nil {
			return err
		}
		return renderTeams(teams, format)

	case "cycles":
		active, _ := cmd.Flags().GetBool("active")
		future, _ := cmd.Flags().GetBool("future")
		var teamID string
		if team != "" {
			t, _, err := client.GetTeam(ctx, team)
			if err == nil && t != nil {
				teamID = t.ID
			}
		}
		cycles, err := client.ListCycles(ctx, CycleFilter{TeamID: teamID, IsActive: active, IsFuture: future})
		if err != nil {
			return err
		}
		return renderCycles(cycles, format)

	case "labels":
		var teamID string
		if team != "" {
			t, _, err := client.GetTeam(ctx, team)
			if err == nil && t != nil {
				teamID = t.ID
			}
		}
		labels, err := client.ListLabels(ctx, teamID)
		if err != nil {
			return err
		}
		return renderLabels(labels, format)

	case "users":
		users, err := client.ListUsers(ctx)
		if err != nil {
			return err
		}
		return renderUsers(users, format)

	case "docs", "documents":
		docs, err := client.ListDocuments(ctx)
		if err != nil {
			return err
		}
		return renderDocs(docs, format)

	default:
		return fmt.Errorf("unknown resource: %s (try issues|projects|teams|cycles|labels|users|docs)", resource)
	}
}

func runGet(cmd *cobra.Command, args []string) error {
	resource := strings.ToLower(args[0])
	id := args[1]
	client, _, err := buildClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch resource {
	case "issue":
		issue, err := client.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(issue)
	case "project":
		p, err := client.GetProject(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(p)
	case "cycle":
		cy, err := client.GetCycle(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(cy)
	case "doc", "document":
		d, err := client.GetDocument(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(d)
	case "team":
		t, states, err := client.GetTeam(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(struct {
			Team   *Team           `json:"team"`
			States []WorkflowState `json:"states"`
		}{t, states})
	default:
		return fmt.Errorf("unknown resource: %s (try issue|project|cycle|doc|team)", resource)
	}
}

// Renderers --------------------------------------------------------------

func renderJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func newTabwriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func renderIssues(issues []Issue, format string) error {
	if format == "json" {
		return renderJSON(issues)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "IDENTIFIER\tSTATE\tPRIORITY\tTITLE\tASSIGNEE\tUPDATED")
	for _, i := range issues {
		state := ""
		if i.State != nil {
			state = i.State.Name
		}
		assignee := ""
		if i.Assignee != nil {
			assignee = i.Assignee.DisplayName
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
			i.Identifier, state, i.Priority, truncate(i.Title, 60), assignee, i.UpdatedAt.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func renderProjects(projects []Project, format string) error {
	if format == "json" {
		return renderJSON(projects)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "NAME\tSTATE\tPROGRESS\tURL")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%.0f%%\t%s\n", p.Name, p.State, p.Progress*100, p.URL)
	}
	return w.Flush()
}

func renderTeams(teams []Team, format string) error {
	if format == "json" {
		return renderJSON(teams)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "KEY\tNAME\tDESCRIPTION")
	for _, t := range teams {
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.Key, t.Name, truncate(t.Description, 60))
	}
	return w.Flush()
}

func renderCycles(cycles []Cycle, format string) error {
	if format == "json" {
		return renderJSON(cycles)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "NUMBER\tNAME\tPROGRESS")
	for _, c := range cycles {
		fmt.Fprintf(w, "%d\t%s\t%.0f%%\n", c.Number, c.Name, c.Progress*100)
	}
	return w.Flush()
}

func renderLabels(labels []Label, format string) error {
	if format == "json" {
		return renderJSON(labels)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "NAME\tCOLOR\tTEAM-ID")
	for _, l := range labels {
		fmt.Fprintf(w, "%s\t%s\t%s\n", l.Name, l.Color, l.TeamID)
	}
	return w.Flush()
}

func renderUsers(users []User, format string) error {
	if format == "json" {
		return renderJSON(users)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "DISPLAY-NAME\tEMAIL\tACTIVE")
	for _, u := range users {
		fmt.Fprintf(w, "%s\t%s\t%v\n", u.DisplayName, u.Email, u.Active)
	}
	return w.Flush()
}

func renderDocs(docs []Document, format string) error {
	if format == "json" {
		return renderJSON(docs)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "TITLE\tURL")
	for _, d := range docs {
		fmt.Fprintf(w, "%s\t%s\n", truncate(d.Title, 60), d.URL)
	}
	return w.Flush()
}
