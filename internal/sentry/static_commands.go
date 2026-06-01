package sentry

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

// CreateSentryCommands builds the `clanker sentry` command tree. The ask
// subcommand is added separately by cmd/sentry.go (so cmd/ keeps its
// dependency on internal/ai out of this package).
func CreateSentryCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sentry",
		Short:   "Query Sentry issues, events, releases, alerts, and monitors",
		Long:    "Query Sentry directly without LLM interpretation. Useful for scripting and CI hooks.",
		Aliases: []string{"sn"},
	}

	cmd.PersistentFlags().String("org", "", "Sentry org slug (overrides config)")
	cmd.PersistentFlags().String("project", "", "Sentry project slug (overrides config)")
	cmd.PersistentFlags().String("host", "", "Sentry host (default sentry.io)")
	cmd.PersistentFlags().String("auth-token", "", "Sentry User Auth Token")
	cmd.PersistentFlags().String("format", "table", "Output format: table | json")

	cmd.AddCommand(buildListCommand())
	cmd.AddCommand(buildGetCommand())
	cmd.AddCommand(buildResolveCommand())
	cmd.AddCommand(buildIgnoreCommand())
	cmd.AddCommand(buildAssignCommand())
	cmd.AddCommand(buildMonitorCommand())
	cmd.AddCommand(buildAlertCommand())

	return cmd
}

func buildListCommand() *cobra.Command {
	listCmd := &cobra.Command{
		Use:   "list <resource>",
		Short: "List Sentry resources",
		Long: `List Sentry resources of a specific type.

Supported resources:
  orgs        - Organizations the auth token can access
  projects    - Projects within an org (needs --org)
  issues      - Issues, with optional --query / --environment / --period
  events      - Recent events for a project (needs --project)
  releases    - Releases for a project (needs --project)
  alerts      - Issue alert rules for a project (needs --project)
  monitors    - Sentry Crons monitors in an org
  teams       - Teams in an org
  members     - Members in an org`,
		Args: cobra.ExactArgs(1),
		RunE: runList,
	}
	listCmd.Flags().String("query", "", "Sentry search query (passed through verbatim: e.g. 'is:unresolved level:error')")
	listCmd.Flags().String("environment", "", "Filter by environment")
	listCmd.Flags().String("period", "14d", "Stats period (24h, 7d, 14d, 30d, 90d)")
	listCmd.Flags().Int("limit", 0, "Maximum rows to return")
	listCmd.Flags().Bool("unresolved", false, "Shortcut for --query='is:unresolved'")
	return listCmd
}

func buildGetCommand() *cobra.Command {
	getCmd := &cobra.Command{
		Use:   "get <resource> <id>",
		Short: "Get a single Sentry resource",
		Long: `Get a single Sentry resource by ID.

Examples:
  clanker sentry get issue ABC-123
  clanker sentry get event <eventID> --project backend
  clanker sentry get release v1.2.3 --project backend`,
		Args: cobra.ExactArgs(2),
		RunE: runGet,
	}
	return getCmd
}

func buildResolveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <issue-id> [issue-id...]",
		Short: "Mark one or more issues as resolved",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.ResolveIssues(ctx, org, args); err != nil {
				return err
			}
			fmt.Printf("Resolved %d issue(s)\n", len(args))
			return nil
		},
	}
}

func buildIgnoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ignore <issue-id> [issue-id...]",
		Short: "Mark one or more issues as ignored",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.IgnoreIssues(ctx, org, args); err != nil {
				return err
			}
			fmt.Printf("Ignored %d issue(s)\n", len(args))
			return nil
		},
	}
}

func buildAssignCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <issue-id> <username>",
		Short: "Assign an issue to a user",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.AssignIssue(ctx, org, args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("Assigned issue %s to %s\n", args[0], args[1])
			return nil
		},
	}
}

func buildMonitorCommand() *cobra.Command {
	monCmd := &cobra.Command{
		Use:   "monitor",
		Short: "Manage Sentry Crons monitors",
	}
	monCmd.AddCommand(&cobra.Command{
		Use:   "mute <monitor-slug>",
		Short: "Mute alerts for a monitor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.MuteMonitor(ctx, org, args[0]); err != nil {
				return err
			}
			fmt.Printf("Muted monitor %s\n", args[0])
			return nil
		},
	})
	monCmd.AddCommand(&cobra.Command{
		Use:   "unmute <monitor-slug>",
		Short: "Unmute a previously-muted monitor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.UnmuteMonitor(ctx, org, args[0]); err != nil {
				return err
			}
			fmt.Printf("Unmuted monitor %s\n", args[0])
			return nil
		},
	})
	monCmd.AddCommand(&cobra.Command{
		Use:   "checkins <monitor-slug>",
		Short: "Show recent check-ins for a monitor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			checkins, err := client.GetMonitorCheckins(ctx, org, args[0], 20)
			if err != nil {
				return err
			}
			return renderCheckins(checkins, sentryFlag(cmd, "format"))
		},
	})
	return monCmd
}

func buildAlertCommand() *cobra.Command {
	alertCmd := &cobra.Command{
		Use:   "alert",
		Short: "Manage Sentry alert rules",
	}
	alertCmd.AddCommand(&cobra.Command{
		Use:   "delete <rule-id>",
		Short: "Delete an issue alert rule (needs --project)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, org, err := buildClient(cmd)
			if err != nil {
				return err
			}
			project := sentryFlag(cmd, "project")
			if project == "" {
				project = ResolveDefaultProject()
			}
			if project == "" {
				return fmt.Errorf("--project is required to delete alert rules")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.DeleteIssueAlertRule(ctx, org, project, args[0]); err != nil {
				return err
			}
			fmt.Printf("Deleted alert rule %s\n", args[0])
			return nil
		},
	})
	return alertCmd
}

// sentryFlag reads a persistent flag from any depth in the sentry command
// tree. cmd.Flags() merges inherited persistent flags from every ancestor,
// so this resolves flags registered on the `sentry` parent even when called
// from 3-level-deep leaves like `clanker sentry monitor checkins X`.
func sentryFlag(cmd *cobra.Command, name string) string {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// buildClient resolves credentials + flags into a ready Client, returning the
// effective org slug separately so callers don't have to re-read flags.
func buildClient(cmd *cobra.Command) (*Client, string, error) {
	authToken := sentryFlag(cmd, "auth-token")
	if authToken == "" {
		authToken = ResolveAuthToken()
	}
	if authToken == "" {
		return nil, "", fmt.Errorf("sentry auth_token is required (set sentry.auth_token, SENTRY_AUTH_TOKEN, or --auth-token)")
	}

	org := sentryFlag(cmd, "org")
	if org == "" {
		org = ResolveOrgSlug()
	}

	host := sentryFlag(cmd, "host")
	if host == "" {
		host = ResolveHost()
	}

	debug := viper.GetBool("debug")
	client, err := NewClient(authToken, org, host, debug)
	if err != nil {
		return nil, "", err
	}
	return client, org, nil
}

func runList(cmd *cobra.Command, args []string) error {
	resource := strings.ToLower(args[0])
	client, org, err := buildClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	format := sentryFlag(cmd, "format")
	project := sentryFlag(cmd, "project")
	if project == "" {
		project = ResolveDefaultProject()
	}

	switch resource {
	case "orgs", "organizations":
		items, err := client.ListOrganizations(ctx)
		if err != nil {
			return err
		}
		return renderOrgs(items, format)

	case "projects":
		if org == "" {
			return fmt.Errorf("--org or sentry.org_slug is required")
		}
		items, err := client.ListProjects(ctx, org)
		if err != nil {
			return err
		}
		return renderProjects(items, format)

	case "issues":
		query, _ := cmd.Flags().GetString("query")
		env, _ := cmd.Flags().GetString("environment")
		period, _ := cmd.Flags().GetString("period")
		limit, _ := cmd.Flags().GetInt("limit")
		unresolved, _ := cmd.Flags().GetBool("unresolved")
		if unresolved && query == "" {
			query = "is:unresolved"
		}
		items, _, err := client.ListIssues(ctx, org, IssueListOptions{
			Query:       query,
			Environment: env,
			StatsPeriod: period,
			Limit:       limit,
		})
		if err != nil {
			return err
		}
		return renderIssues(items, format)

	case "events":
		if project == "" {
			return fmt.Errorf("--project is required to list events")
		}
		limit, _ := cmd.Flags().GetInt("limit")
		items, err := client.ListProjectEvents(ctx, org, project, limit)
		if err != nil {
			return err
		}
		return renderEvents(items, format)

	case "releases":
		if project == "" {
			return fmt.Errorf("--project is required to list releases")
		}
		items, err := client.ListReleases(ctx, org, project)
		if err != nil {
			return err
		}
		return renderReleases(items, format)

	case "alerts", "alert-rules":
		if project == "" {
			return fmt.Errorf("--project is required to list issue alert rules")
		}
		issueRules, err := client.ListIssueAlertRules(ctx, org, project)
		if err != nil {
			return err
		}
		return renderIssueAlertRules(issueRules, format)

	case "metric-alerts":
		rules, err := client.ListMetricAlertRules(ctx, org)
		if err != nil {
			return err
		}
		return renderMetricAlertRules(rules, format)

	case "monitors":
		items, err := client.ListMonitors(ctx, org)
		if err != nil {
			return err
		}
		return renderMonitors(items, format)

	case "teams":
		items, err := client.ListTeams(ctx, org)
		if err != nil {
			return err
		}
		return renderTeams(items, format)

	case "members":
		items, err := client.ListMembers(ctx, org)
		if err != nil {
			return err
		}
		return renderMembers(items, format)

	default:
		return fmt.Errorf("unknown resource: %s (try orgs|projects|issues|events|releases|alerts|monitors|teams|members)", resource)
	}
}

func runGet(cmd *cobra.Command, args []string) error {
	resource := strings.ToLower(args[0])
	id := args[1]
	client, org, err := buildClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	project := sentryFlag(cmd, "project")
	if project == "" {
		project = ResolveDefaultProject()
	}

	switch resource {
	case "issue":
		issue, err := client.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(issue)
	case "event":
		if project == "" {
			return fmt.Errorf("--project is required to fetch an event")
		}
		ev, err := client.GetEvent(ctx, org, project, id)
		if err != nil {
			return err
		}
		return renderJSON(ev)
	case "release":
		if project == "" {
			return fmt.Errorf("--project is required to fetch a release")
		}
		rel, err := client.GetRelease(ctx, org, project, id)
		if err != nil {
			return err
		}
		return renderJSON(rel)
	case "monitor":
		m, err := client.GetMonitor(ctx, org, id)
		if err != nil {
			return err
		}
		return renderJSON(m)
	case "org", "organization":
		o, err := client.GetOrganization(ctx, id)
		if err != nil {
			return err
		}
		return renderJSON(o)
	default:
		return fmt.Errorf("unknown resource: %s (try issue|event|release|monitor|org)", resource)
	}
}

// renderers -----------------------------------------------------------------

// renderJSON dumps v as indented JSON. Used by `get` subcommands where the
// returned object's shape varies per-resource and JSON is the most useful
// machine-readable form regardless of --format.
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

func renderOrgs(orgs []Organization, format string) error {
	if format == "json" {
		return renderJSON(orgs)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "SLUG\tNAME\tCREATED")
	for _, o := range orgs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", o.Slug, o.Name, o.DateCreated.Format("2006-01-02"))
	}
	return w.Flush()
}

func renderProjects(projects []Project, format string) error {
	if format == "json" {
		return renderJSON(projects)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "SLUG\tNAME\tPLATFORM")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\n", p.Slug, p.Name, p.Platform)
	}
	return w.Flush()
}

func renderIssues(issues []Issue, format string) error {
	if format == "json" {
		return renderJSON(issues)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "SHORT-ID\tLEVEL\tSTATUS\tCOUNT\tUSERS\tTITLE\tLAST-SEEN")
	for _, i := range issues {
		title := i.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			i.ShortID, i.Level, i.Status, i.Count, i.UserCount, title, i.LastSeen.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func renderEvents(events []Event, format string) error {
	if format == "json" {
		return renderJSON(events)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "EVENT-ID\tTITLE\tCREATED")
	for _, e := range events {
		title := e.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.EventID, title, e.DateCreated.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func renderReleases(releases []Release, format string) error {
	if format == "json" {
		return renderJSON(releases)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "VERSION\tNEW-GROUPS\tCREATED\tRELEASED")
	for _, r := range releases {
		released := "—"
		if r.DateReleased != nil && !r.DateReleased.IsZero() {
			released = r.DateReleased.Format("2006-01-02")
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", r.ShortVersion, r.NewGroups, r.DateCreated.Format("2006-01-02"), released)
	}
	return w.Flush()
}

func renderIssueAlertRules(rules []IssueAlertRule, format string) error {
	if format == "json" {
		return renderJSON(rules)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "ID\tNAME\tENV\tFREQUENCY\tCREATED")
	for _, r := range rules {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.ID, r.Name, r.Environment, r.Frequency, r.DateCreated.Format("2006-01-02"))
	}
	return w.Flush()
}

func renderMetricAlertRules(rules []MetricAlertRule, format string) error {
	if format == "json" {
		return renderJSON(rules)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "ID\tNAME\tQUERY\tAGGREGATE\tTHRESHOLD")
	for _, r := range rules {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.2f\n", r.ID, r.Name, r.Query, r.Aggregate, r.Threshold)
	}
	return w.Flush()
}

func renderMonitors(monitors []Monitor, format string) error {
	if format == "json" {
		return renderJSON(monitors)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "SLUG\tNAME\tSTATUS\tMUTED\tTYPE")
	for _, m := range monitors {
		fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\n", m.Slug, m.Name, m.Status, m.IsMuted, m.Type)
	}
	return w.Flush()
}

func renderCheckins(checkins []MonitorCheckin, format string) error {
	if format == "json" {
		return renderJSON(checkins)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "ID\tSTATUS\tDURATION-MS\tCREATED")
	for _, c := range checkins {
		dur := "—"
		if c.Duration != nil {
			dur = fmt.Sprintf("%.0f", *c.Duration)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.ID, c.Status, dur, c.DateCreated.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func renderTeams(teams []Team, format string) error {
	if format == "json" {
		return renderJSON(teams)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "SLUG\tNAME\tMEMBERS\tCREATED")
	for _, t := range teams {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", t.Slug, t.Name, t.MemberCount, t.DateCreated.Format("2006-01-02"))
	}
	return w.Flush()
}

func renderMembers(members []Member, format string) error {
	if format == "json" {
		return renderJSON(members)
	}
	w := newTabwriter()
	fmt.Fprintln(w, "EMAIL\tNAME\tROLE")
	for _, m := range members {
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.Email, m.Name, m.Role)
	}
	return w.Flush()
}
