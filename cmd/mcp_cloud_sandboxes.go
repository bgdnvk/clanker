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
	Name     string         `json:"name,omitempty" jsonschema:"description=Sandbox name; defaults to agent-sandbox."`
	Agent    string         `json:"agent,omitempty" jsonschema:"description=Agent image: clanker-cli, codex, claude-code, openclaw, hermes, empty, or clanker-vision."`
	Region   string         `json:"region,omitempty" jsonschema:"description=Sandbox region; defaults to earth."`
	Metadata map[string]any `json:"metadata,omitempty" jsonschema:"description=Optional non-secret orchestration metadata."`
}

type cloudSandboxListArgs struct{}

type cloudSandboxIDArgs struct {
	SandboxID string `json:"sandboxId" jsonschema:"description=Sandbox id,required"`
}

type cloudSandboxCommandArgs struct {
	SandboxID      string            `json:"sandboxId" jsonschema:"description=Sandbox id,required"`
	Command        string            `json:"command" jsonschema:"description=Shell command to run in the sandbox,required"`
	WorkingDir     string            `json:"workingDir,omitempty" jsonschema:"description=Optional sandbox working directory such as /workspace or /data."`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty" jsonschema:"description=Optional command timeout in seconds."`
	Env            map[string]string `json:"env,omitempty" jsonschema:"description=Optional non-secret environment variables for the command."`
}

type cloudSandboxMessageArgs struct {
	SandboxID string         `json:"sandboxId" jsonschema:"description=Sandbox id,required"`
	Role      string         `json:"role,omitempty" jsonschema:"description=Message role; defaults to user."`
	Content   string         `json:"content" jsonschema:"description=Message content to send to the sandbox agent,required"`
	Metadata  map[string]any `json:"metadata,omitempty" jsonschema:"description=Optional non-secret orchestration metadata."`
}

type cloudSandboxTaskArgs struct {
	Task     string         `json:"task" jsonschema:"description=Task to send to the new sandbox agent,required"`
	Name     string         `json:"name,omitempty" jsonschema:"description=Sandbox name; defaults to agent-runner."`
	Agent    string         `json:"agent,omitempty" jsonschema:"description=Agent image; defaults to clanker-cli."`
	Region   string         `json:"region,omitempty" jsonschema:"description=Sandbox region; defaults to earth."`
	Metadata map[string]any `json:"metadata,omitempty" jsonschema:"description=Optional non-secret orchestration metadata."`
}

func registerCloudSandboxMCPTools(server *mcptransport.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_create_sandbox",
			mcp.WithDescription("Create an account-owned hosted Clanker Cloud sandbox using trusted server configuration. The sandbox remains allocated until it is explicitly deleted."),
			mcp.WithInputSchema[cloudSandboxCreateArgs](),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxCreateArgs) (*mcp.CallToolResult, error) {
			client := newMCPSandboxClient()
			if client.AccountKey() == "" {
				return mcp.NewToolResultError("CLANKER_CLOUD_API_KEY is required so the sandbox remains account-owned and cleanup can always be retried"), nil
			}
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
			result, err := newMCPSandboxClient().List(ctx)
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
			result, err := newMCPSandboxClient().Inspect(ctx, args.SandboxID)
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_delete_sandbox",
			mcp.WithDescription("Delete one hosted Clanker Cloud sandbox by id. Use only after user approval if the sandbox still contains needed work."),
			mcp.WithInputSchema[cloudSandboxIDArgs](),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxIDArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			result, err := newMCPSandboxClient().Delete(ctx, args.SandboxID)
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_run_sandbox_command",
			mcp.WithDescription("Run a shell command in a hosted Clanker Cloud sandbox. Shell commands may change or delete data and require appropriate user authorization."),
			mcp.WithInputSchema[cloudSandboxCommandArgs](),
			mcp.WithDestructiveHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxCommandArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			if strings.TrimSpace(args.Command) == "" {
				return mcp.NewToolResultError("command is required"), nil
			}
			result, err := newMCPSandboxClient().Command(ctx, args.SandboxID, clankercloud.SandboxCommandRequest{
				Command:        args.Command,
				WorkingDir:     args.WorkingDir,
				TimeoutSeconds: args.TimeoutSeconds,
				Env:            args.Env,
			})
			return mcpSandboxResult(result, err)
		}),
	)

	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_send_sandbox_message",
			mcp.WithDescription("Send a message to a hosted Clanker Cloud sandbox agent. Agent messages may cause side effects in the sandbox."),
			mcp.WithInputSchema[cloudSandboxMessageArgs](),
			mcp.WithDestructiveHintAnnotation(true),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxMessageArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.SandboxID) == "" {
				return mcp.NewToolResultError("sandboxId is required"), nil
			}
			if strings.TrimSpace(args.Content) == "" {
				return mcp.NewToolResultError("content is required"), nil
			}
			result, err := newMCPSandboxClient().Message(ctx, args.SandboxID, clankercloud.SandboxMessageRequest{
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
			mcp.WithDescription("Create an account-owned temporary Clanker Cloud sandbox, run one task, and dispose the sandbox before returning. Account ownership preserves a manual cleanup path if automatic disposal fails."),
			mcp.WithInputSchema[cloudSandboxTaskArgs](),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(false),
		),
		mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args cloudSandboxTaskArgs) (*mcp.CallToolResult, error) {
			if strings.TrimSpace(args.Task) == "" {
				return mcp.NewToolResultError("task is required"), nil
			}
			client := newMCPSandboxClient()
			if client.AccountKey() == "" {
				return mcp.NewToolResultError("CLANKER_CLOUD_API_KEY is required so the temporary sandbox remains manageable if automatic cleanup fails"), nil
			}
			metadata := trustedMCPSandboxTaskMetadata(args.Metadata)
			result, err := executeSandboxRun(ctx, client, clankercloud.SandboxCreateRequest{
				Name:     firstNonEmpty(args.Name, "agent-runner"),
				Agent:    args.Agent,
				Region:   args.Region,
				Metadata: metadata,
			}, clankercloud.SandboxMessageRequest{
				Role:     "user",
				Content:  args.Task,
				Metadata: metadata,
			}, false)
			if err != nil {
				result["ok"] = false
				result["error"] = err.Error()
			}
			return mcp.NewToolResultJSON(result)
		}),
	)
}

func newMCPSandboxClient() *clankercloud.SandboxClient {
	return clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{})
}

func trustedMCPSandboxTaskMetadata(input map[string]any) map[string]any {
	metadata := make(map[string]any, len(input)+2)
	for key, value := range input {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "source", "mode":
			continue
		}
		metadata[key] = value
	}
	metadata["source"] = "clanker-mcp"
	metadata["mode"] = "sandbox-task"
	return metadata
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
