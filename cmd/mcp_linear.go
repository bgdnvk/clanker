package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/linear"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/viper"
)

// Linear MCP tools — explicit mcp.WithString/Number/Bool/Object schema
// declarations (not WithInputSchema[T]() — struct-tag reflection is broken
// in this version of mark3labs/mcp-go per the Tencent PR's footnote).

func registerLinearMCPTools(server *mcptransport.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"clanker_linear_ask",
			mcp.WithDescription("Ask a natural-language question about Linear. Fetches relevant issues, projects, cycles, and teams then answers via the configured AI provider."),
			mcp.WithString("question", mcp.Required(), mcp.Description("The natural-language question")),
			mcp.WithString("apiKey", mcp.Description("Linear Personal API Key (falls back to config/env)")),
			mcp.WithString("workspaceId", mcp.Description("Workspace ID")),
			mcp.WithString("team", mcp.Description("Default team key (e.g. ENG)")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearAsk(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_list_issues",
			mcp.WithDescription("List Linear issues with optional filters. Returns the raw issue list — use clanker_linear_get_issue for full detail + comments."),
			mcp.WithString("apiKey", mcp.Description("Linear API key (falls back to config/env)")),
			mcp.WithString("workspaceId", mcp.Description("Workspace ID")),
			mcp.WithString("state", mcp.Description("State type filter: triage|backlog|unstarted|started|completed|cancelled")),
			mcp.WithString("team", mcp.Description("Team key (e.g. ENG) — narrows to one team")),
			mcp.WithString("teamId", mcp.Description("Team UUID — alternative to team key")),
			mcp.WithString("projectId", mcp.Description("Project UUID")),
			mcp.WithString("cycleId", mcp.Description("Cycle UUID")),
			mcp.WithString("label", mcp.Description("Exact label name to match")),
			mcp.WithString("assigneeId", mcp.Description("Filter to issues assigned to this user UUID")),
			mcp.WithNumber("limit", mcp.DefaultNumber(25), mcp.Description("Max issues returned (default 25)")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearListIssues(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_get_issue",
			mcp.WithDescription("Fetch a single Linear issue by ID (UUID) with its recent comments."),
			mcp.WithString("issueId", mcp.Required(), mcp.Description("Issue UUID")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearGetIssue(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_list_projects",
			mcp.WithDescription("List Linear projects, optionally filtered by state."),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
			mcp.WithString("state", mcp.Description("backlog|planned|started|paused|completed|canceled")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearListProjects(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_list_cycles",
			mcp.WithDescription("List Linear cycles (sprints), optionally filtered to active or future."),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
			mcp.WithString("teamId", mcp.Description("Team UUID to scope to")),
			mcp.WithBoolean("active", mcp.Description("Only active cycles")),
			mcp.WithBoolean("future", mcp.Description("Only future cycles")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearListCycles(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_list_teams",
			mcp.WithDescription("List Linear teams in the workspace."),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearListTeams(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_search_by_label",
			mcp.WithDescription("Find Linear issues by exact label name. Powers the infra-resource annotation lookup (label format: infra:<type>:<id>)."),
			mcp.WithString("label", mcp.Required(), mcp.Description("Exact label name e.g. infra:lambda:arn:aws:lambda:us-east-1:123:foo")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
			mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("Max issues returned")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearSearchByLabel(ctx, req)
		},
	)

	// Mutations — no read-only hint. Each is destructive against the user's
	// Linear workspace; cautious MCP clients should prompt for confirmation.
	server.AddTool(
		mcp.NewTool(
			"clanker_linear_create_issue",
			mcp.WithDescription("Create a Linear issue. Returns the new identifier (e.g. ENG-456) and URL."),
			mcp.WithString("title", mcp.Required(), mcp.Description("Issue title")),
			mcp.WithString("teamId", mcp.Required(), mcp.Description("Team UUID the issue belongs to")),
			mcp.WithString("description", mcp.Description("Markdown body")),
			mcp.WithString("projectId", mcp.Description("Project UUID")),
			mcp.WithString("cycleId", mcp.Description("Cycle UUID")),
			mcp.WithString("assigneeId", mcp.Description("Assignee user UUID")),
			mcp.WithNumber("priority", mcp.Description("1 (urgent) | 2 (high) | 3 (medium) | 4 (low); 0 omitted")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearCreateIssue(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_update_issue",
			mcp.WithDescription("Update a Linear issue. Pass only the fields to change. Common ops: move state (stateId), reassign (assigneeId), reprioritize (priority)."),
			mcp.WithString("issueId", mcp.Required(), mcp.Description("Issue UUID")),
			mcp.WithString("title", mcp.Description("New title")),
			mcp.WithString("description", mcp.Description("New description (markdown)")),
			mcp.WithString("stateId", mcp.Description("Move to this workflow state (UUID)")),
			mcp.WithString("assigneeId", mcp.Description("Reassign (use empty string to unassign)")),
			mcp.WithString("projectId", mcp.Description("Move to this project (UUID)")),
			mcp.WithString("cycleId", mcp.Description("Move to this cycle (UUID)")),
			mcp.WithNumber("priority", mcp.Description("1|2|3|4")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearUpdateIssue(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_comment_issue",
			mcp.WithDescription("Post a top-level comment on a Linear issue."),
			mcp.WithString("issueId", mcp.Required(), mcp.Description("Issue UUID")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Markdown body")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearCommentIssue(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_create_project",
			mcp.WithDescription("Create a Linear project across one or more teams."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Project name")),
			mcp.WithString("description", mcp.Description("Description (markdown)")),
			mcp.WithString("teamIdsCSV", mcp.Required(), mcp.Description("Comma-separated team UUIDs (project must belong to at least one)")),
			mcp.WithString("leadId", mcp.Description("Lead user UUID")),
			mcp.WithString("state", mcp.Description("backlog|planned|started|paused|completed|canceled")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearCreateProject(ctx, req)
		},
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_linear_update_project",
			mcp.WithDescription("Update a Linear project (state, lead, dates, name)."),
			mcp.WithString("projectId", mcp.Required(), mcp.Description("Project UUID")),
			mcp.WithString("name", mcp.Description("New name")),
			mcp.WithString("description", mcp.Description("New description")),
			mcp.WithString("state", mcp.Description("New state")),
			mcp.WithString("leadId", mcp.Description("New lead user UUID")),
			mcp.WithString("startDate", mcp.Description("YYYY-MM-DD")),
			mcp.WithString("targetDate", mcp.Description("YYYY-MM-DD")),
			mcp.WithString("apiKey", mcp.Description("Linear API key")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPLinearUpdateProject(ctx, req)
		},
	)
}

// --- Helpers ----------------------------------------------------------------

func mcpLinearClient(req mcp.CallToolRequest) (*linear.Client, string, error) {
	apiKey := strParam(req, "apiKey")
	if apiKey == "" {
		apiKey = linear.ResolveAPIKey()
	}
	if apiKey == "" {
		return nil, "", fmt.Errorf("linear api key not configured (set linear.api_key in ~/.clanker.yaml or LINEAR_API_KEY)")
	}
	workspaceID := strParam(req, "workspaceId")
	if workspaceID == "" {
		workspaceID = linear.ResolveWorkspaceID()
	}
	team := strParam(req, "team")
	if team == "" {
		team = linear.ResolveDefaultTeam()
	}
	client, err := linear.NewClient(apiKey, workspaceID, team, false)
	if err != nil {
		return nil, "", err
	}
	return client, workspaceID, nil
}

// --- Handlers ----------------------------------------------------------------

func handleMCPLinearAsk(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	question := strParam(req, "question")
	if question == "" {
		return mcp.NewToolResultError("question is required"), nil
	}
	client, workspaceID, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	contextStr, _ := gatherLinearContext(ctx, client, question, client.DefaultTeam(), false)
	status, _ := linear.GatherAccountStatus(ctx, client, workspaceID)
	statusStr := ""
	if status != nil {
		statusStr = fmt.Sprintf("Workspace: %s — Teams: %d — In-progress: %d — Active projects: %d",
			status.WorkspaceName, status.TeamCount, status.StartedIssueCount, status.ActiveProjectCount)
	}
	prompt := buildLinearPrompt(question, contextStr, "", statusStr)

	aiProfile := viper.GetString("ai.default_provider")
	apiKey := resolveAIKeyForProfile(aiProfile)
	aiClient := ai.NewClient(aiProfile, apiKey, false)
	answer, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}
	return mcp.NewToolResultText(answer), nil
}

func handleMCPLinearListIssues(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := intParam(req, "limit", 25)
	filter := linear.IssueFilter{
		StateType:  strParam(req, "state"),
		TeamID:     strParam(req, "teamId"),
		TeamKey:    strParam(req, "team"),
		ProjectID:  strParam(req, "projectId"),
		CycleID:    strParam(req, "cycleId"),
		LabelName:  strParam(req, "label"),
		AssigneeID: strParam(req, "assigneeId"),
	}
	issues, page, err := client.ListIssues(ctx, filter, limit, "")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(map[string]any{"issues": issues, "pageInfo": page})
}

func handleMCPLinearGetIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "issueId")
	if id == "" {
		return mcp.NewToolResultError("issueId is required"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	issue, err := client.GetIssue(ctx, id)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	comments, err := client.GetIssueComments(ctx, id, 25)
	if err != nil {
		return mcp.NewToolResultJSON(map[string]any{"issue": issue, "commentsError": err.Error()})
	}
	return mcp.NewToolResultJSON(map[string]any{"issue": issue, "comments": comments})
}

func handleMCPLinearListProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	projects, _, err := client.ListProjects(ctx, linear.ProjectFilter{State: strParam(req, "state")}, 50, "")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(projects)
}

func handleMCPLinearListCycles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	cycles, err := client.ListCycles(ctx, linear.CycleFilter{
		TeamID:   strParam(req, "teamId"),
		IsActive: boolParam(req, "active", false),
		IsFuture: boolParam(req, "future", false),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(cycles)
}

func handleMCPLinearListTeams(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	teams, err := client.ListTeams(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(teams)
}

func handleMCPLinearSearchByLabel(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	label := strParam(req, "label")
	if label == "" {
		return mcp.NewToolResultError("label is required"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := intParam(req, "limit", 50)
	issues, _, err := client.ListIssues(ctx, linear.IssueFilter{LabelName: label}, limit, "")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(issues)
}

func handleMCPLinearCreateIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := strParam(req, "title")
	teamID := strParam(req, "teamId")
	if title == "" || teamID == "" {
		return mcp.NewToolResultError("title and teamId are required"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	issue, err := client.CreateIssue(ctx, linear.CreateIssueInput{
		Title:       title,
		TeamID:      teamID,
		Description: strParam(req, "description"),
		ProjectID:   strParam(req, "projectId"),
		CycleID:     strParam(req, "cycleId"),
		AssigneeID:  strParam(req, "assigneeId"),
		Priority:    intParam(req, "priority", 0),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(issue)
}

func handleMCPLinearUpdateIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "issueId")
	if id == "" {
		return mcp.NewToolResultError("issueId is required"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	input := linear.UpdateIssueInput{}
	if v := strParam(req, "title"); v != "" {
		input.Title = &v
	}
	if v := strParam(req, "description"); v != "" {
		input.Description = &v
	}
	if v := strParam(req, "stateId"); v != "" {
		input.StateID = &v
	}
	if v := strParam(req, "assigneeId"); v != "" {
		input.AssigneeID = &v
	}
	if v := strParam(req, "projectId"); v != "" {
		input.ProjectID = &v
	}
	if v := strParam(req, "cycleId"); v != "" {
		input.CycleID = &v
	}
	if p := intParam(req, "priority", 0); p > 0 {
		input.Priority = &p
	}
	issue, err := client.UpdateIssue(ctx, id, input)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(issue)
}

func handleMCPLinearCommentIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "issueId")
	body := strParam(req, "body")
	if id == "" || body == "" {
		return mcp.NewToolResultError("issueId and body are required"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	c, err := client.AddComment(ctx, id, body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(c)
}

func handleMCPLinearCreateProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := strParam(req, "name")
	csv := strParam(req, "teamIdsCSV")
	if name == "" || csv == "" {
		return mcp.NewToolResultError("name and teamIdsCSV are required"), nil
	}
	var teams []string
	for _, t := range strings.Split(csv, ",") {
		if v := strings.TrimSpace(t); v != "" {
			teams = append(teams, v)
		}
	}
	if len(teams) == 0 {
		return mcp.NewToolResultError("teamIdsCSV must contain at least one team UUID"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	p, err := client.CreateProject(ctx, linear.CreateProjectInput{
		Name:        name,
		Description: strParam(req, "description"),
		TeamIDs:     teams,
		LeadID:      strParam(req, "leadId"),
		State:       strParam(req, "state"),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(p)
}

func handleMCPLinearUpdateProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "projectId")
	if id == "" {
		return mcp.NewToolResultError("projectId is required"), nil
	}
	client, _, err := mcpLinearClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	input := linear.UpdateProjectInput{}
	if v := strParam(req, "name"); v != "" {
		input.Name = &v
	}
	if v := strParam(req, "description"); v != "" {
		input.Description = &v
	}
	if v := strParam(req, "state"); v != "" {
		input.State = &v
	}
	if v := strParam(req, "leadId"); v != "" {
		input.LeadID = &v
	}
	if v := strParam(req, "startDate"); v != "" {
		input.StartDate = &v
	}
	if v := strParam(req, "targetDate"); v != "" {
		input.TargetDate = &v
	}
	p, err := client.UpdateProject(ctx, id, input)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(p)
}
