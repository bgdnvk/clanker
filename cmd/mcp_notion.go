package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/notion"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/viper"
)

// Notion MCP tools — explicit schema declarations (same pattern as
// Linear/Tencent — struct-tag reflection in WithInputSchema[T]() is
// broken in this version of mark3labs/mcp-go).

func registerNotionMCPTools(server *mcptransport.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"clanker_notion_ask",
			mcp.WithDescription("Ask a natural-language question about a Notion workspace. Fetches matching pages, databases, and users then answers via the configured AI provider."),
			mcp.WithString("question", mcp.Required(), mcp.Description("The natural-language question")),
			mcp.WithString("token", mcp.Description("Notion integration token (falls back to config/env)")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		handleMCPNotionAsk,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_search",
			mcp.WithDescription("Search Notion by title. Returns matching pages and databases. NOTE: Notion's search indexes titles only — block content is NOT searched."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search terms (matched against page/database titles)")),
			mcp.WithString("filter", mcp.Description("page | database (omit for both)")),
			mcp.WithNumber("limit", mcp.DefaultNumber(25), mcp.Description("Max results (1..100)")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		handleMCPNotionSearch,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_get_page",
			mcp.WithDescription("Fetch a single Notion page by ID (metadata + properties, NOT content)."),
			mcp.WithString("pageId", mcp.Required(), mcp.Description("Page UUID")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		handleMCPNotionGetPage,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_get_page_blocks",
			mcp.WithDescription("Fetch a page's block content tree (capped at depth 3 / 200 blocks). Returns markdown rendering plus the raw tree."),
			mcp.WithString("pageId", mcp.Required(), mcp.Description("Page UUID")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		handleMCPNotionGetPageBlocks,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_query_database",
			mcp.WithDescription("Query rows in a Notion database. Filter and sorts use Notion's typed filter language — pass as JSON strings."),
			mcp.WithString("databaseId", mcp.Required(), mcp.Description("Database UUID")),
			mcp.WithString("filterJSON", mcp.Description("Filter object as a JSON string (Notion filter spec)")),
			mcp.WithString("sortsJSON", mcp.Description("Sorts array as a JSON string")),
			mcp.WithNumber("limit", mcp.DefaultNumber(25), mcp.Description("Max rows per page (1..100)")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		handleMCPNotionQueryDatabase,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_list_databases",
			mcp.WithDescription("List databases the integration has access to."),
			mcp.WithString("query", mcp.Description("Optional title filter")),
			mcp.WithNumber("limit", mcp.DefaultNumber(25), mcp.Description("Max results (1..100)")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		handleMCPNotionListDatabases,
	)

	// Mutations — no read-only hint; cautious MCP clients should prompt.
	server.AddTool(
		mcp.NewTool(
			"clanker_notion_create_page",
			mcp.WithDescription("Create a new Notion page under a parent (page or database). The body is markdown converted into Notion blocks."),
			mcp.WithString("parentId", mcp.Required(), mcp.Description("Parent page or database UUID")),
			mcp.WithString("parentType", mcp.DefaultString("page_id"), mcp.Description("page_id | database_id")),
			mcp.WithString("title", mcp.Required(), mcp.Description("Page title")),
			mcp.WithString("markdown", mcp.Description("Markdown body (converted to Notion blocks)")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithDestructiveHintAnnotation(true),
		),
		handleMCPNotionCreatePage,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_append_blocks",
			mcp.WithDescription("Append markdown content to an existing Notion page (converted to blocks). Useful for runbook updates, post-incident notes."),
			mcp.WithString("pageId", mcp.Required(), mcp.Description("Target page UUID")),
			mcp.WithString("markdown", mcp.Required(), mcp.Description("Markdown content to append")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithDestructiveHintAnnotation(true),
		),
		handleMCPNotionAppendBlocks,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_update_page_properties",
			mcp.WithDescription("Patch a page's typed property values (DB rows). Properties payload is the Notion typed property shape — pass as JSON string."),
			mcp.WithString("pageId", mcp.Required(), mcp.Description("Page UUID (DB row)")),
			mcp.WithString("propertiesJSON", mcp.Required(), mcp.Description("Properties patch as JSON string")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithDestructiveHintAnnotation(true),
		),
		handleMCPNotionUpdatePageProperties,
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_notion_create_database_row",
			mcp.WithDescription("Create a row in a Notion database. Properties must satisfy the database schema; pass as JSON string."),
			mcp.WithString("databaseId", mcp.Required(), mcp.Description("Database UUID")),
			mcp.WithString("propertiesJSON", mcp.Required(), mcp.Description("Properties payload as JSON string (Notion typed property shape)")),
			mcp.WithString("token", mcp.Description("Notion integration token")),
			mcp.WithDestructiveHintAnnotation(true),
		),
		handleMCPNotionCreateDatabaseRow,
	)
}

// --- Helpers ----------------------------------------------------------------

func mcpNotionClient(req mcp.CallToolRequest) (*notion.Client, error) {
	token := strParam(req, "token")
	if token == "" {
		token = notion.ResolveToken()
	}
	if token == "" {
		return nil, fmt.Errorf("notion integration token not configured (set notion.integration_token in ~/.clanker.yaml or NOTION_API_KEY)")
	}
	return notion.NewClient(token, notion.ResolveDefaultDatabaseID(), false)
}

// --- Handlers ----------------------------------------------------------------

func handleMCPNotionAsk(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	question := strParam(req, "question")
	if question == "" {
		return mcp.NewToolResultError("question is required"), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	contextStr, _ := gatherNotionContext(ctx, client, question, false)
	status, _ := notion.GatherAccountStatus(ctx, client)
	statusStr := ""
	if status != nil {
		statusStr = fmt.Sprintf("Workspace: %s — Accessible pages: %d — Databases: %d",
			status.WorkspaceName, status.AccessiblePages, status.DatabaseCount)
	}
	prompt := buildNotionPrompt(question, contextStr, "", statusStr)

	aiProfile := viper.GetString("ai.default_provider")
	apiKey := resolveAIKeyForProfile(aiProfile)
	aiClient := ai.NewClient(aiProfile, apiKey, false)
	answer, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}
	return mcp.NewToolResultText(answer), nil
}

func handleMCPNotionSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	results, _, _, err := client.Search(ctx, notion.SearchOptions{
		Query:        strParam(req, "query"),
		FilterObject: strParam(req, "filter"),
		PageSize:     intParam(req, "limit", 25),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(results)
}

func handleMCPNotionGetPage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "pageId")
	if id == "" {
		return mcp.NewToolResultError("pageId is required"), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	p, err := client.GetPage(ctx, id)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(p)
}

func handleMCPNotionGetPageBlocks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "pageId")
	if id == "" {
		return mcp.NewToolResultError("pageId is required"), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	tree, count, err := client.GetPageBlocks(ctx, id)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{
		"markdown":   notion.BlocksToMarkdown(tree),
		"blockCount": count,
		"tree":       tree,
	})
}

func handleMCPNotionQueryDatabase(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strParam(req, "databaseId")
	if id == "" {
		return mcp.NewToolResultError("databaseId is required"), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var filter map[string]any
	if raw := strParam(req, "filterJSON"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &filter); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("parse filterJSON: %v", err)), nil
		}
	}
	var sorts []map[string]any
	if raw := strParam(req, "sortsJSON"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sorts); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("parse sortsJSON: %v", err)), nil
		}
	}
	rows, next, more, err := client.QueryDatabase(ctx, id, notion.QueryDatabaseOptions{
		Filter:   filter,
		Sorts:    sorts,
		PageSize: intParam(req, "limit", 25),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{
		"rows":       rows,
		"nextCursor": next,
		"hasMore":    more,
	})
}

func handleMCPNotionListDatabases(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	dbs, err := client.ListDatabases(ctx, strParam(req, "query"), intParam(req, "limit", 25))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(dbs)
}

func handleMCPNotionCreatePage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	parentID := strParam(req, "parentId")
	if parentID == "" {
		return mcp.NewToolResultError("parentId is required"), nil
	}
	parentType := strParam(req, "parentType")
	if parentType == "" {
		parentType = notion.ParentTypePage
	}
	title := strParam(req, "title")
	if title == "" {
		return mcp.NewToolResultError("title is required"), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	props := notion.TitleProperty(title)
	var children []map[string]any
	if md := strParam(req, "markdown"); md != "" {
		children = notion.MarkdownToBlocks(md)
	}
	p, err := client.CreatePage(ctx, parentType, parentID, props, children)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(p)
}

func handleMCPNotionAppendBlocks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pageID := strParam(req, "pageId")
	if pageID == "" {
		return mcp.NewToolResultError("pageId is required"), nil
	}
	md := strParam(req, "markdown")
	if md == "" {
		return mcp.NewToolResultError("markdown is required"), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	blocks := notion.MarkdownToBlocks(md)
	if len(blocks) == 0 {
		return mcp.NewToolResultError("markdown produced zero blocks"), nil
	}
	appended, err := client.AppendBlockChildren(ctx, pageID, blocks)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(appended)
}

func handleMCPNotionUpdatePageProperties(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pageID := strParam(req, "pageId")
	if pageID == "" {
		return mcp.NewToolResultError("pageId is required"), nil
	}
	raw := strParam(req, "propertiesJSON")
	if raw == "" {
		return mcp.NewToolResultError("propertiesJSON is required"), nil
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("parse propertiesJSON: %v", err)), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	p, err := client.UpdatePageProperties(ctx, pageID, props)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(p)
}

func handleMCPNotionCreateDatabaseRow(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbID := strParam(req, "databaseId")
	if dbID == "" {
		return mcp.NewToolResultError("databaseId is required"), nil
	}
	raw := strParam(req, "propertiesJSON")
	if raw == "" {
		return mcp.NewToolResultError("propertiesJSON is required"), nil
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("parse propertiesJSON: %v", err)), nil
	}
	client, err := mcpNotionClient(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	p, err := client.CreateDatabaseRow(ctx, dbID, props)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(p)
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}
