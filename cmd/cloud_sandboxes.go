package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/clankercloud"
	"github.com/spf13/cobra"
)

func newCloudSandboxesCmd() *cobra.Command {
	var apiBaseURL string
	var apiKey string
	var sandboxToken string

	client := func() *clankercloud.SandboxClient {
		return clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
			BaseURL:      apiBaseURL,
			AccountKey:   apiKey,
			SandboxToken: sandboxToken,
		})
	}

	cmd := &cobra.Command{
		Use:     "sandboxes",
		Aliases: []string{"sandbox"},
		Short:   "Create and operate hosted Clanker Cloud sandboxes",
		Long: strings.TrimSpace(`Create anonymous or account-owned Clanker Cloud sandboxes, then run commands or send task messages.

The public sandbox API defaults to https://clankercloud.ai/api. Set CLANKER_CLOUD_API_KEY for account-owned sandboxes and CLANKER_SANDBOX_TOKEN for per-sandbox commands.`),
	}
	cmd.PersistentFlags().StringVar(&apiBaseURL, "api-base-url", "", "Clanker Cloud sandbox API base URL (default: CLANKER_CLOUD_SANDBOX_API_BASE_URL or https://clankercloud.ai/api)")
	cmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "Clanker Cloud account API key (default: CLANKER_CLOUD_API_KEY)")
	cmd.PersistentFlags().StringVar(&sandboxToken, "sandbox-token", "", "sandbox token returned by create (default: CLANKER_SANDBOX_TOKEN)")

	cmd.AddCommand(newCloudSandboxesCreateCmd(client))
	cmd.AddCommand(newCloudSandboxesListCmd(client))
	cmd.AddCommand(newCloudSandboxesInspectCmd(client))
	cmd.AddCommand(newCloudSandboxesDeleteCmd(client))
	cmd.AddCommand(newCloudSandboxesCommandCmd(client))
	cmd.AddCommand(newCloudSandboxesMessageCmd(client))
	cmd.AddCommand(newCloudSandboxesRunCmd(&apiBaseURL, &apiKey, &sandboxToken))

	return cmd
}

func newCloudSandboxesCreateCmd(client func() *clankercloud.SandboxClient) *cobra.Command {
	var name string
	var agent string
	var region string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a hosted sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().Create(cmd.Context(), clankercloud.SandboxCreateRequest{Name: name, Agent: agent, Region: region})
			return printSandboxResult(result, err)
		},
	}
	cmd.Flags().StringVar(&name, "name", "agent-sandbox", "sandbox name")
	cmd.Flags().StringVar(&agent, "agent", "clanker-cli", "sandbox agent image: clanker-cli, codex, claude-code, openclaw, hermes, empty, or clanker-vision")
	cmd.Flags().StringVar(&region, "region", "earth", "sandbox region")
	return cmd
}

func newCloudSandboxesListCmd(client func() *clankercloud.SandboxClient) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List account-owned sandboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().List(cmd.Context())
			return printSandboxResult(result, err)
		},
	}
}

func newCloudSandboxesInspectCmd(client func() *clankercloud.SandboxClient) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect SANDBOX_ID",
		Short: "Inspect one sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().Inspect(cmd.Context(), args[0])
			return printSandboxResult(result, err)
		},
	}
}

func newCloudSandboxesDeleteCmd(client func() *clankercloud.SandboxClient) *cobra.Command {
	return &cobra.Command{
		Use:   "delete SANDBOX_ID",
		Short: "Delete one sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().Delete(cmd.Context(), args[0])
			return printSandboxResult(result, err)
		},
	}
}

func newCloudSandboxesCommandCmd(client func() *clankercloud.SandboxClient) *cobra.Command {
	var timeoutSeconds int
	cmd := &cobra.Command{
		Use:   "command SANDBOX_ID COMMAND...",
		Short: "Run a shell command in a sandbox",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := clankercloud.SandboxCommandRequest{
				Command:        strings.Join(args[1:], " "),
				TimeoutSeconds: timeoutSeconds,
			}
			result, err := client().Command(cmd.Context(), args[0], payload)
			return printSandboxResult(result, err)
		},
	}
	cmd.Flags().IntVar(&timeoutSeconds, "timeout-seconds", 0, "optional command timeout in seconds")
	return cmd
}

func newCloudSandboxesMessageCmd(client func() *clankercloud.SandboxClient) *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "message SANDBOX_ID MESSAGE...",
		Short: "Send a task message to a sandbox agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := clankercloud.SandboxMessageRequest{
				Role:    role,
				Content: strings.Join(args[1:], " "),
			}
			result, err := client().Message(cmd.Context(), args[0], payload)
			return printSandboxResult(result, err)
		},
	}
	cmd.Flags().StringVar(&role, "role", "user", "message role")
	return cmd
}

func newCloudSandboxesRunCmd(apiBaseURL *string, apiKey *string, sandboxToken *string) *cobra.Command {
	var name string
	var agentName string
	var region string
	cmd := &cobra.Command{
		Use:   "run TASK...",
		Short: "Create a sandbox and send it a task message",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.TrimSpace(strings.Join(args, " "))
			client := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
				BaseURL:      derefString(apiBaseURL),
				AccountKey:   derefString(apiKey),
				SandboxToken: derefString(sandboxToken),
			})
			created, err := client.Create(cmd.Context(), clankercloud.SandboxCreateRequest{Name: name, Agent: agentName, Region: region})
			if err != nil {
				return err
			}
			if !clankercloud.SandboxResultOK(created) {
				_ = printJSON(created)
				return clankercloud.SandboxResultStatusError(created)
			}

			sandboxID := clankercloud.ExtractSandboxID(created.Body)
			token := clankercloud.ExtractSandboxToken(created.Body)
			if sandboxID == "" {
				_ = printJSON(created)
				return fmt.Errorf("create response did not include a sandbox id")
			}
			if token == "" {
				token = client.SandboxToken()
			}
			if token == "" {
				token = client.AccountKey()
			}
			runClient := clankercloud.NewSandboxClient(clankercloud.SandboxClientOptions{
				BaseURL:      derefString(apiBaseURL),
				AccountKey:   derefString(apiKey),
				SandboxToken: token,
			})
			message, err := runClient.Message(cmd.Context(), sandboxID, clankercloud.SandboxMessageRequest{
				Role:    "user",
				Content: task,
				Metadata: map[string]any{
					"source": "clanker-cli",
					"mode":   "sandboxes.run",
				},
			})
			if err != nil {
				return err
			}
			out := map[string]any{
				"created": created,
				"message": message,
			}
			if !clankercloud.SandboxResultOK(message) {
				_ = printJSON(out)
				return clankercloud.SandboxResultStatusError(message)
			}
			return printJSON(out)
		},
	}
	cmd.Flags().StringVar(&name, "name", "agent-runner", "sandbox name")
	cmd.Flags().StringVar(&agentName, "agent", "clanker-cli", "sandbox agent image")
	cmd.Flags().StringVar(&region, "region", "earth", "sandbox region")
	return cmd
}

func printSandboxResult(result any, err error) error {
	if err != nil {
		return err
	}
	if err := printJSON(result); err != nil {
		return err
	}
	if apiResult, ok := result.(*clankercloud.SandboxAPIResult); ok {
		return clankercloud.SandboxResultStatusError(apiResult)
	}
	return nil
}

func printJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	fmt.Fprintln(os.Stdout, string(encoded))
	return nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func init() {
	cloudCmd.AddCommand(newCloudSandboxesCmd())
}
