package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/clankercloud"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
)

type cloudSandboxCreateArgs struct {
	Name       string         `json:"name,omitempty" jsonschema:"description=Sandbox name; defaults to agent-sandbox."`
	Agent      string         `json:"agent,omitempty" jsonschema:"description=Agent image: clanker-cli, codex, claude-code, openclaw, hermes, empty, or clanker-vision."`
	Region     string         `json:"region,omitempty" jsonschema:"description=Sandbox region; defaults to earth."`
	Metadata   map[string]any `json:"metadata,omitempty" jsonschema:"description=Optional non-secret orchestration metadata."`
	APIBaseURL string         `json:"apiBaseUrl,omitempty" jsonschema:"description=Sandbox API base URL; defaults to CLANKER_CLOUD_SANDBOX_API_BASE_URL or https://clankercloud.ai/api."`
	APIKey     string         `json:"apiKey,omitempty" jsonschema:"description=Clanker Cloud account API key; defaults to CLANKER_CLOUD_API_KEY."`
}

type cloudSandboxListArgs struct {
	APIBaseURL string `json:"apiBaseUrl,omitempty" jsonschema:"description=Sandbox API base URL."`
	APIKey     string `json:"apiKey,omitempty" jsonschema:"description=Clanker Cloud account API key; defaults to CLANKER_CLOUD_API_KEY."`
}

type cloudSandboxIDArgs struct {
	SandboxID    string `json:"sandboxId" jsonschema:"description=Sandbox id,required"`
	APIBaseURL   string `json:"apiBaseUrl,omitempty" jsonschema:"description=Sandbox API base URL."`
	APIKey       string `json:"apiKey,omitempty" jsonschema:"description=Clanker Cloud account API key; defaults to CLANKER_CLOUD_API_KEY."`
	SandboxToken string `json:"sandboxToken,omitempty" jsonschema:"description=Sandbox token returned by create; defaults to CLANKER_SANDBOX_TOKEN."`
}

type cloudSandboxCommandArgs struct {
	SandboxID      string            `json:"sandboxId" jsonschema:"description=Sandbox id,required"`
	Command        string            `json:"command" jsonschema:"description=Shell command to run in the sandbox,required"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty" jsonschema:"description=Optional command timeout in seconds."`
	Env            map[string]string `json:"env,omitempty" jsonschema:"description=Optional non-secret environment variables for the command."`
	APIBaseURL     string            `json:"apiBaseUrl,omitempty" jsonschema:"description=Sandbox API base URL."`
	APIKey         string            `json:"apiKey,omitempty" jsonschema:"description=Clanker Cloud account API key; defaults to CLANKER_CLOUD_API_KEY."`
	SandboxToken   string            `json:"sandboxToken,omitempty" jsonschema:"description=Sandbox token returned by create; defaults to CLANKER_SANDBOX_TOKEN."`
}

type cloudSandboxMessageArgs struct {
	SandboxID    string         `json:"sandboxId" jsonschema:"description=Sandbox id,required"`
	Role         string         `json:"role,omitempty" jsonschema:"description=Message role; defaults to user."`
	Content      string         `json:"content" jsonschema:"description=Message content to send to the sandbox agent,required"`
	Metadata     map[string]any `json:"metadata,omitempty" jsonschema:"description=Optional non-secret orchestration metadata."`
	APIBaseURL   string         `json:"apiBaseUrl,omitempty" jsonschema:"description=Sandbox API base URL."`
	APIKey       string         `json:"apiKey,omitempty" jsonschema:"description=Clanker Cloud account API key; defaults to CLANKER_CLOUD_API_KEY."`
	SandboxToken string         `json:"sandboxToken,omitempty" jsonschema:"description=Sandbox token returned by create; defaults to CLANKER_SANDBOX_TOKEN."`
}

type cloudSandboxTaskArgs struct {
	Task       string         `json:"task" jsonschema:"description=Task to send to the new sandbox agent,required"`
	Name       string         `json:"name,omitempty" jsonschema:"description=Sandbox name; defaults to agent-runner."`
	Agent      string         `json:"agent,omitempty" jsonschema:"description=Agent image; defaults to clanker-cli."`
	Region     string         `json:"region,omitempty" jsonschema:"description=Sandbox region; defaults to earth."`
	Metadata   map[string]any `json:"metadata,omitempty" jsonschema:"description=Optional non-secret orchestration metadata."`
	APIBaseURL string         `json:"apiBaseUrl,omitempty" jsonschema:"description=Sandbox API base URL."`
	APIKey     string         `json:"apiKey,omitempty" jsonschema:"description=Clanker Cloud account API key; defaults to CLANKER_CLOUD_API_KEY."`
}

func registerCloudSandboxMCPTools(server *mcptransport.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_create_sandbox",
			mcp.WithDescription("Create an anonymous or account-owned hosted Clanker Cloud sandbox. The response includes sandboxToken; keep it out of user-visible transcripts unless the user explicitly asks."),
			mcp.WithInputSchema[cloudSandboxCreateArgs](),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxCreateArgs) (*mcp.CallToolResult, error) {
			client := newMCPSandboxClient(args.APIBaseURL, args.APIKey, "")
			result, err := client.Create(ctx, clankercloud.SandboxCreateRequest{
				Name:     args.Name,
				Agent:    args.Agent,
				Region:   args.Region,
				Metadata: args.Metadata,
			})
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_list_sandboxes",
			mcp.WithDescription("List account-owned Clanker Cloud sandboxes using a Clanker Cloud account API key."),
			mcp.WithInputSchema[cloudSandboxListArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxListArgs) (*mcp.CallToolResult, error) {
			result, err := newMCPSandboxClient(args.APIBaseURL, args.APIKey, "").List(ctx)
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_inspect_sandbox",
			mcp.WithDescription("Inspect one hosted Clanker Cloud sandbox by id."),
			mcp.WithInputSchema[cloudSandboxIDArgs](),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxIDArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			result, err := newMCPSandboxClient(args.APIBaseURL, args.APIKey, args.SandboxToken).Inspect(ctx, args.SandboxID)
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_delete_sandbox",
			mcp.WithDescription("Delete one hosted Clanker Cloud sandbox by id. Use only after user approval if the sandbox still contains needed work."),
			mcp.WithInputSchema[cloudSandboxIDArgs](),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxIDArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			result, err := newMCPSandboxClient(args.APIBaseURL, args.APIKey, args.SandboxToken).Delete(ctx, args.SandboxID)
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_run_sandbox_command",
			mcp.WithDescription("Run a shell command in a hosted Clanker Cloud sandbox. Prefer read-only commands unless the user has approved side effects."),
			mcp.WithInputSchema[cloudSandboxCommandArgs](),
			mcp.WithDestructiveHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxCommandArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			if strings.TrimSpace(args.Command) == "" {
				return mcp.NewToolResultError("command is required"), nil
			}
			result, err := newMCPSandboxClient(args.APIBaseURL, args.APIKey, args.SandboxToken).Command(ctx, args.SandboxID, clankercloud.SandboxCommandRequest{
				Command:        args.Command,
				TimeoutSeconds: args.TimeoutSeconds,
				Env:            args.Env,
			})
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_send_sandbox_message",
			mcp.WithDescription("Send a message to a hosted Clanker Cloud sandbox agent."),
			mcp.WithInputSchema[cloudSandboxMessageArgs](),
			mcp.WithDestructiveHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxMessageArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			if strings.TrimSpace(args.Content) == "" {
				return mcp.NewToolResultError("content is required"), nil
			}
			result, err := newMCPSandboxClient(args.APIBaseURL, args.APIKey, args.SandboxToken).Message(ctx, args.SandboxID, clankercloud.SandboxMessageRequest{
				Role:     args.Role,
				Content:  args.Content,
				Metadata: args.Metadata,
			})
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_run_sandbox_task",
			mcp.WithDescription("Create a hosted Clanker Cloud sandbox and send the initial task message to its agent."),
			mcp.WithInputSchema[cloudSandboxTaskArgs](),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxTaskArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.Task) == "" {
				return mcp.NewToolResultError("task is required"), nil
			}
			client := newMCPSandboxClient(args.APIBaseURL, args.APIKey, "")
			metadata := map[string]any{
				"source": "clanker-mcp",
				"mode":   "sandbox-task",
			}
			for key, value := range args.Metadata {
				metadata[key] = value
			}
			created, err := client.Create(ctx, clankercloud.SandboxCreateRequest{
				Name:     firstNonEmpty(args.Name, "agent-runner"),
				Agent:    args.Agent,
				Region:   args.Region,
				Metadata: metadata,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if !clankercloud.SandboxResultOK(created) {
				return mcp.NewToolResultJSON(map[string]any{"created": created})
			}
			sandboxID := clankercloud.ExtractSandboxID(created.Body)
			if sandboxID == "" {
				return mcp.NewToolResultError("create response did not include a sandbox id"), nil
			}
			token := firstNonEmpty(clankercloud.ExtractSandboxToken(created.Body), client.SandboxToken(), client.AccountKey())
			message, err := newMCPSandboxClient(args.APIBaseURL, args.APIKey, token).Message(ctx, sandboxID, clankercloud.SandboxMessageRequest{
				Role:     "user",
				Content:  args.Task,
				Metadata: metadata,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{
				"created": created,
				"message": message,
			})
		}),
	)
}

func newMCPSandboxClient(apiBaseURL string, apiKey string, sandboxToken string) *clankercloud.SandboxClient {
	return clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
		BaseURL:      apiBaseURL,
		AccountKey:   apiKey,
		SandboxToken: sandboxToken,
	})
}

func mcpSandboxResult(result *clankercloud.SandboxAPIResult, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if result == nil {
		return mcp.NewToolResultError("sandbox request failed"), nil
	}
	if !clankercloud.SandboxResultOK(result) {
		return mcp.NewToolResultJSON(map[string]any{
			"ok":     false,
			"error":  fmt.Sprintf("sandbox request returned status %d", result.Status),
			"result": result,
		})
	}
	return mcp.NewToolResultJSON(result)
}
