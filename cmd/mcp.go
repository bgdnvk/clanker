package cmd

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/bgdnvk/clanker/internal/vercel"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type routeArgs struct {
	Question string `json:"question"`
}

type versionArgs struct{}

type commandArgs struct {
	Args       []string `json:"args"`
	Profile    string   `json:"profile,omitempty"`
	BackendURL string   `json:"backendUrl,omitempty"`
	BackendEnv string   `json:"backendEnv,omitempty"`
	Debug      *bool    `json:"debug,omitempty"`
}

type vercelAskArgs struct {
	Question string `json:"question" jsonschema:"description=Natural language question about Vercel infrastructure,required"`
	Token    string `json:"token,omitempty" jsonschema:"description=Vercel API token (falls back to config/env)"`
	TeamID   string `json:"teamId,omitempty" jsonschema:"description=Vercel team ID"`
	Debug    bool   `json:"debug,omitempty" jsonschema:"description=Enable debug output"`
}

type vercelListArgs struct {
	Resource string `json:"resource" jsonschema:"description=Resource type: projects|deployments|domains|env|teams|aliases|kv|blob|postgres|edge-configs,required"`
	Token    string `json:"token,omitempty" jsonschema:"description=Vercel API token (falls back to config/env)"`
	TeamID   string `json:"teamId,omitempty" jsonschema:"description=Vercel team ID"`
	Project  string `json:"project,omitempty" jsonschema:"description=Project ID for scoped resources (deployments and env)"`
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Expose Clanker over MCP",
	RunE: func(cmd *cobra.Command, args []string) error {
		transport, _ := cmd.Flags().GetString("transport")
		listenAddr, _ := cmd.Flags().GetString("listen")

		server := newClankerMCPServer()

		switch strings.ToLower(strings.TrimSpace(transport)) {
		case "stdio":
			stdioServer := mcptransport.NewStdioServer(server)
			stdioServer.SetErrorLogger(nil)
			return stdioServer.Listen(cmd.Context(), os.Stdin, os.Stdout)
		case "http", "streamable-http":
			httpServer := mcptransport.NewStreamableHTTPServer(server, mcptransport.WithEndpointPath("/mcp"), mcptransport.WithStateLess(true))
			log.Printf("[mcp] clanker MCP listening on http://%s/mcp", listenAddr)
			return httpServer.Start(listenAddr)
		default:
			return fmt.Errorf("unsupported transport %q", transport)
		}
	},
}

func newClankerMCPServer() *mcptransport.MCPServer {
	server := mcptransport.NewMCPServer("clanker", Version)

	server.AddTool(
		mcp.NewTool(
			"clanker_version",
			mcp.WithDescription("Return the current Clanker CLI version."),
			mcp.WithInputSchema[versionArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ versionArgs) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultJSON(map[string]any{"version": Version})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_route_question",
			mcp.WithDescription("Return which internal Clanker route should handle a question, including Clanker Cloud app requests."),
			mcp.WithInputSchema[routeArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args routeArgs) (*mcp.CallToolResult, error) {
			agent, reason := determineRoutingDecision(args.Question)
			return mcp.NewToolResultJSON(map[string]any{
				"agent":  agent,
				"reason": reason,
			})
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_run_command",
			mcp.WithDescription("Run the local Clanker CLI with the given arguments. Use this for ask, openclaw, codex-related helper flows, and other CLI features."),
			mcp.WithInputSchema[commandArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args commandArgs) (*mcp.CallToolResult, error) {
			result, err := runClankerCommand(ctx, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(result)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_vercel_ask",
			mcp.WithDescription("Ask a natural language question about your Vercel infrastructure. Gathers Vercel context (projects, deployments, domains) and uses the configured AI provider to answer."),
			mcp.WithInputSchema[vercelAskArgs](),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args vercelAskArgs) (*mcp.CallToolResult, error) {
			return handleMCPVercelAsk(ctx, args)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_vercel_list",
			mcp.WithDescription("List Vercel resources (projects, deployments, domains, env vars, teams, aliases, kv, blob, postgres, edge-configs). Returns raw JSON from the Vercel API."),
			mcp.WithInputSchema[vercelListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args vercelListArgs) (*mcp.CallToolResult, error) {
			return handleMCPVercelList(ctx, args)
		}),
	)

	return server
}

// handleMCPVercelAsk resolves Vercel credentials, gathers context, and asks
// the configured AI provider about the user's Vercel infrastructure.
func handleMCPVercelAsk(ctx context.Context, args vercelAskArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = vercel.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Vercel token not configured. Set vercel.api_token in ~/.clanker.yaml or export VERCEL_TOKEN"), nil
	}

	teamID := args.TeamID
	if teamID == "" {
		teamID = vercel.ResolveTeamID()
	}

	client, err := vercel.NewClient(token, teamID, args.Debug)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Vercel client: %v", err)), nil
	}

	vercelContext, _ := client.GetRelevantContext(ctx, args.Question)

	prompt := buildVercelPrompt(args.Question, vercelContext, "")

	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}
	apiKey := mcpResolveProviderKey(provider)

	aiClient := ai.NewClient(provider, apiKey, args.Debug, provider)

	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}

	return mcp.NewToolResultText(response), nil
}

// handleMCPVercelList resolves Vercel credentials and lists the requested
// resource type, returning raw JSON from the Vercel API.
func handleMCPVercelList(ctx context.Context, args vercelListArgs) (*mcp.CallToolResult, error) {
	token := args.Token
	if token == "" {
		token = vercel.ResolveAPIToken()
	}
	if token == "" {
		return mcp.NewToolResultError("Vercel token not configured. Set vercel.api_token in ~/.clanker.yaml or export VERCEL_TOKEN"), nil
	}

	teamID := args.TeamID
	if teamID == "" {
		teamID = vercel.ResolveTeamID()
	}

	client, err := vercel.NewClient(token, teamID, false)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create Vercel client: %v", err)), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resource := strings.ToLower(strings.TrimSpace(args.Resource))
	endpoint := ""

	switch resource {
	case "projects", "project":
		endpoint = "/v9/projects?limit=100"
	case "deployments", "deployment":
		endpoint = "/v6/deployments?limit=20"
		if args.Project != "" {
			endpoint += "&projectId=" + url.QueryEscape(args.Project)
		}
	case "domains", "domain":
		endpoint = "/v5/domains?limit=100"
	case "env", "envs", "env-vars", "environment":
		if args.Project == "" {
			return mcp.NewToolResultError("project is required to list env vars"), nil
		}
		endpoint = fmt.Sprintf("/v10/projects/%s/env", url.PathEscape(args.Project))
	case "teams", "team":
		endpoint = "/v2/teams?limit=50"
	case "aliases", "alias":
		endpoint = "/v4/aliases?limit=50"
		if args.Project != "" {
			endpoint += "&projectId=" + url.QueryEscape(args.Project)
		}
	case "kv":
		endpoint = "/v1/storage/stores?storeType=kv"
	case "blob":
		endpoint = "/v1/storage/stores?storeType=blob"
	case "postgres", "pg":
		endpoint = "/v1/storage/stores?storeType=postgres"
	case "edge-configs", "edge-config", "edge":
		endpoint = "/v1/edge-config"
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown resource type: %s (expected: projects, deployments, domains, env, teams, aliases, kv, blob, postgres, edge-configs)", resource)), nil
	}

	result, err := client.RunAPIWithContext(ctx, "GET", endpoint, "")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Vercel API error: %v", err)), nil
	}

	return mcp.NewToolResultText(result), nil
}

// mcpResolveProviderKey resolves the API key for the given AI provider using
// config and environment variables. Mirrors the resolution logic in ask.go.
func mcpResolveProviderKey(provider string) string {
	switch provider {
	case "bedrock", "claude", "gemini", "gemini-api":
		return ""
	case "openai":
		return resolveOpenAIKey("")
	case "anthropic":
		return resolveAnthropicKey("")
	case "cohere":
		return resolveCohereKey("")
	case "deepseek":
		return resolveDeepSeekKey("")
	case "minimax":
		return resolveMiniMaxKey("")
	default:
		return viper.GetString(fmt.Sprintf("ai.providers.%s.api_key", provider))
	}
}

func runClankerCommand(ctx context.Context, args commandArgs) (map[string]any, error) {
	if len(args.Args) == 0 {
		return nil, fmt.Errorf("args are required")
	}

	cleanArgs := make([]string, 0, len(args.Args)+6)
	for _, arg := range args.Args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		cleanArgs = append(cleanArgs, trimmed)
	}
	if len(cleanArgs) == 0 {
		return nil, fmt.Errorf("args are required")
	}
	if cleanArgs[0] == "mcp" {
		return nil, fmt.Errorf("refusing to recursively invoke clanker mcp")
	}

	if profile := strings.TrimSpace(args.Profile); profile != "" {
		cleanArgs = append(cleanArgs, "--profile", profile)
	}
	if backendURL := strings.TrimSpace(args.BackendURL); backendURL != "" {
		cleanArgs = append(cleanArgs, "--backend-url", backendURL)
	}
	if backendEnv := strings.TrimSpace(args.BackendEnv); backendEnv != "" {
		cleanArgs = append(cleanArgs, "--backend-env", backendEnv)
	}
	if args.Debug != nil && *args.Debug {
		cleanArgs = append(cleanArgs, "--debug")
		viper.Set("debug", true)
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve clanker executable: %w", err)
	}

	cmd := exec.CommandContext(ctx, exe, cleanArgs...)
	output, err := cmd.CombinedOutput()
	result := map[string]any{
		"command":   append([]string{exe}, cleanArgs...),
		"output":    string(output),
		"exitError": err != nil,
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result["exitCode"] = exitErr.ExitCode()
		} else {
			result["exitCode"] = -1
		}
		return result, fmt.Errorf("clanker command failed: %w", err)
	}
	result["exitCode"] = 0
	return result, nil
}

func init() {
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.Flags().String("transport", "http", "MCP transport to use: http or stdio")
	mcpCmd.Flags().String("listen", "127.0.0.1:39393", "Listen address for HTTP MCP transport")
}
