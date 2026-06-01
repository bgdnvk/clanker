package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/sentry"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/viper"
)

// MCP tool argument types — exported via JSON Schema by mark3labs/mcp-go.

type sentryAskMCPArgs struct {
	Question    string `json:"question" jsonschema:"description=Natural language question about Sentry issues/events/releases,required"`
	OrgSlug     string `json:"orgSlug,omitempty" jsonschema:"description=Sentry org slug (falls back to config/env)"`
	Project     string `json:"project,omitempty" jsonschema:"description=Optional default project slug for release/alert/event context"`
	Environment string `json:"environment,omitempty" jsonschema:"description=Filter to a specific environment (e.g. prod)"`
	Token       string `json:"token,omitempty" jsonschema:"description=Sentry User Auth Token (falls back to config/env)"`
	Host        string `json:"host,omitempty" jsonschema:"description=Sentry host; defaults to sentry.io"`
	Debug       bool   `json:"debug,omitempty" jsonschema:"description=Enable debug output"`
}

type sentryListIssuesMCPArgs struct {
	OrgSlug     string `json:"orgSlug,omitempty"`
	Query       string `json:"query,omitempty" jsonschema:"description=Sentry search query (passed through verbatim)"`
	Environment string `json:"environment,omitempty"`
	StatsPeriod string `json:"statsPeriod,omitempty" jsonschema:"description=e.g. 24h, 7d, 14d"`
	Limit       int    `json:"limit,omitempty"`
	Token       string `json:"token,omitempty"`
	Host        string `json:"host,omitempty"`
}

type sentryGetIssueMCPArgs struct {
	IssueID string `json:"issueId" jsonschema:"description=Issue ID,required"`
	Token   string `json:"token,omitempty"`
	Host    string `json:"host,omitempty"`
}

type sentryResolveIssuesMCPArgs struct {
	OrgSlug  string   `json:"orgSlug,omitempty"`
	IssueIDs []string `json:"issueIds" jsonschema:"description=Issue IDs to resolve,required"`
	Token    string   `json:"token,omitempty"`
	Host     string   `json:"host,omitempty"`
}

type sentryListReleasesMCPArgs struct {
	OrgSlug string `json:"orgSlug,omitempty"`
	Project string `json:"project" jsonschema:"description=Sentry project slug,required"`
	Token   string `json:"token,omitempty"`
	Host    string `json:"host,omitempty"`
}

func registerSentryMCPTools(server *mcptransport.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"clanker_sentry_ask",
			mcp.WithDescription("Ask a natural-language question about Sentry. Fetches relevant issues, releases, and monitors and answers via the configured AI provider."),
			mcp.WithInputSchema[sentryAskMCPArgs](),
			// The ask tool only reads from Sentry (ListIssues, ListReleases,
			// ListMonitors, ListIssueAlertRules) then routes the result
			// through the LLM. Marking it read-only lets cautious MCP
			// clients (e.g. Claude Desktop's safe-tool list) invoke it
			// without user confirmation.
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args sentryAskMCPArgs) (*mcp.CallToolResult, error) {
			return handleMCPSentryAsk(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_sentry_list_issues",
			mcp.WithDescription("List Sentry issues. Query passes through Sentry's search syntax (e.g. 'is:unresolved level:error environment:prod')."),
			mcp.WithInputSchema[sentryListIssuesMCPArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args sentryListIssuesMCPArgs) (*mcp.CallToolResult, error) {
			return handleMCPSentryListIssues(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_sentry_get_issue",
			mcp.WithDescription("Fetch a single Sentry issue by ID, including recent events."),
			mcp.WithInputSchema[sentryGetIssueMCPArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args sentryGetIssueMCPArgs) (*mcp.CallToolResult, error) {
			return handleMCPSentryGetIssue(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_sentry_resolve_issues",
			mcp.WithDescription("Mark one or more Sentry issues as resolved. Mutates upstream — confirm with the user before calling."),
			mcp.WithInputSchema[sentryResolveIssuesMCPArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args sentryResolveIssuesMCPArgs) (*mcp.CallToolResult, error) {
			return handleMCPSentryResolveIssues(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_sentry_list_releases",
			mcp.WithDescription("List Sentry releases for a project."),
			mcp.WithInputSchema[sentryListReleasesMCPArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args sentryListReleasesMCPArgs) (*mcp.CallToolResult, error) {
			return handleMCPSentryListReleases(ctx, args)
		}),
	)
}

func mcpSentryClient(token, orgSlug, host string, debug bool) (*sentry.Client, string, error) {
	if token == "" {
		token = sentry.ResolveAuthToken()
	}
	if token == "" {
		return nil, "", fmt.Errorf("sentry auth token not configured (set sentry.auth_token in ~/.clanker.yaml or SENTRY_AUTH_TOKEN)")
	}
	if orgSlug == "" {
		orgSlug = sentry.ResolveOrgSlug()
	}
	if host == "" {
		host = sentry.ResolveHost()
	}
	client, err := sentry.NewClient(token, orgSlug, host, debug)
	if err != nil {
		return nil, "", err
	}
	return client, orgSlug, nil
}

func handleMCPSentryAsk(ctx context.Context, args sentryAskMCPArgs) (*mcp.CallToolResult, error) {
	client, org, err := mcpSentryClient(args.Token, args.OrgSlug, args.Host, args.Debug)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if org == "" {
		return mcp.NewToolResultError("sentry org slug is required (set sentry.org_slug or pass orgSlug)"), nil
	}

	contextStr, err := gatherSentryContext(ctx, client, args.Question, args.Project, args.Environment, args.Debug)
	if err != nil && args.Debug {
		return mcp.NewToolResultError(fmt.Sprintf("gather context: %v", err)), nil
	}

	status, _ := sentry.GatherAccountStatus(ctx, client, org)
	statusStr := ""
	if status != nil {
		statusStr = fmt.Sprintf("Org: %s — Projects: %d — Unresolved: %d", org, status.ProjectCount, status.UnresolvedCount)
	}

	prompt := buildSentryPrompt(args.Question, contextStr, "", statusStr)

	aiProfile := viper.GetString("ai.default_provider")
	apiKey := resolveAIKeyForProfile(aiProfile)
	aiClient := ai.NewClient(aiProfile, apiKey, args.Debug)
	answer, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}
	return mcp.NewToolResultText(answer), nil
}

func handleMCPSentryListIssues(ctx context.Context, args sentryListIssuesMCPArgs) (*mcp.CallToolResult, error) {
	client, org, err := mcpSentryClient(args.Token, args.OrgSlug, args.Host, false)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if org == "" {
		return mcp.NewToolResultError("sentry org slug is required"), nil
	}
	period := args.StatsPeriod
	if period == "" {
		period = "14d"
	}
	limit := args.Limit
	if limit == 0 {
		limit = 50
	}
	issues, nextCursor, err := client.ListIssues(ctx, org, sentry.IssueListOptions{
		Query:       args.Query,
		Environment: args.Environment,
		StatsPeriod: period,
		Limit:       limit,
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(map[string]any{
		"issues":     issues,
		"nextCursor": nextCursor,
	})
}

func handleMCPSentryGetIssue(ctx context.Context, args sentryGetIssueMCPArgs) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(args.IssueID) == "" {
		return mcp.NewToolResultError("issueId is required"), nil
	}
	client, _, err := mcpSentryClient(args.Token, "", args.Host, false)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	issue, err := client.GetIssue(ctx, args.IssueID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	events, err := client.GetIssueEvents(ctx, args.IssueID, 5)
	if err != nil {
		return mcp.NewToolResultJSON(map[string]any{"issue": issue, "eventsError": err.Error()})
	}
	return mcp.NewToolResultJSON(map[string]any{"issue": issue, "recentEvents": events})
}

func handleMCPSentryResolveIssues(ctx context.Context, args sentryResolveIssuesMCPArgs) (*mcp.CallToolResult, error) {
	if len(args.IssueIDs) == 0 {
		return mcp.NewToolResultError("issueIds is required"), nil
	}
	client, org, err := mcpSentryClient(args.Token, args.OrgSlug, args.Host, false)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if org == "" {
		return mcp.NewToolResultError("sentry org slug is required"), nil
	}
	if err := client.ResolveIssues(ctx, org, args.IssueIDs); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(map[string]any{"resolved": args.IssueIDs})
}

func handleMCPSentryListReleases(ctx context.Context, args sentryListReleasesMCPArgs) (*mcp.CallToolResult, error) {
	client, org, err := mcpSentryClient(args.Token, args.OrgSlug, args.Host, false)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if org == "" {
		return mcp.NewToolResultError("sentry org slug is required"), nil
	}
	releases, err := client.ListReleases(ctx, org, args.Project)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(releases)
}
