package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

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

	return server
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
