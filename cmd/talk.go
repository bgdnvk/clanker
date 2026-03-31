package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bgdnvk/clanker/internal/clankercloud"
	"github.com/bgdnvk/clanker/internal/claudecode"
	"github.com/bgdnvk/clanker/internal/hermes"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var talkCmd = &cobra.Command{
	Use:   "talk",
	Short: "Interactive conversation with an AI agent",
	Long: `Start an interactive multi-turn conversation with an AI agent.

The session maintains context across messages so follow-up questions work
naturally. Type 'exit' or 'quit' to end the session, or press Ctrl+D.

Examples:
  clanker talk
  clanker talk --agent hermes
  clanker talk --agent claude-code`,
	RunE: func(cmd *cobra.Command, args []string) error {
		agentName, _ := cmd.Flags().GetString("agent")
		debug := viper.GetBool("debug")

		if agentName == "" {
			agentName = "hermes"
		}

		switch agentName {
		case "hermes":
			return runHermesTalk(cmd.Context(), debug)
		case "claude-code":
			return runClaudeCodeTalk(cmd.Context(), debug)
		default:
			return fmt.Errorf("unknown agent: %s (available: hermes, claude-code)", agentName)
		}
	},
}

func runHermesTalk(parentCtx context.Context, debug bool) error {
	hermesPath, err := hermes.FindHermesPath()
	if err != nil {
		return fmt.Errorf("hermes agent not found: %w\nRun 'make setup-hermes' to install", err)
	}

	runner := hermes.NewRunner(hermesPath, debug)
	runner.SetEnv(buildHermesEnv())

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	if err := runner.Start(ctx); err != nil {
		return fmt.Errorf("failed to start hermes agent: %w", err)
	}
	defer runner.Stop()

	// Handle signals: Ctrl+C interrupts the current response but does not
	// kill the session. A second Ctrl+C exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for range sigCh {
			fmt.Fprintln(os.Stderr, "\nInterrupted. Type 'exit' to quit.")
		}
	}()
	defer signal.Stop(sigCh)

	fmt.Println("Hermes Agent (interactive mode)")
	fmt.Println("Type 'exit' or 'quit' to end the session.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			break // EOF (Ctrl+D)
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		lower := strings.ToLower(input)
		if lower == "exit" || lower == "quit" || lower == "/quit" || lower == "/exit" {
			fmt.Println("Goodbye.")
			break
		}

		routedAgent, _ := determineRoutingDecision(input)
		if routedAgent == "clanker-cloud" {
			if handled, err := handleClankerCloudTalk(ctx, input, debug); handled {
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				fmt.Println()
				continue
			}
		}

		events, err := runner.Prompt(ctx, input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}

		fmt.Print("hermes> ")
		hadDelta := false
		for event := range events {
			switch {
			case event.Error != nil:
				fmt.Fprintf(os.Stderr, "\nError: %v\n", event.Error)
			case event.MessageDelta != nil:
				fmt.Print(event.MessageDelta.Text)
				hadDelta = true
			case event.ToolCall != nil:
				if debug {
					fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", event.ToolCall.Name)
				}
			case event.Thought != nil:
				if debug {
					fmt.Fprintf(os.Stderr, "\n[thinking: %s]\n", event.Thought.Text)
				}
			case event.Final != nil:
				if !hadDelta && event.Final.Text != "" {
					fmt.Print(event.Final.Text)
				}
			}
		}
		fmt.Println()
		fmt.Println()
	}

	return nil
}

func handleClankerCloudTalk(ctx context.Context, question string, debug bool) (bool, error) {
	client := clankercloud.NewClient()
	result, err := client.AskAgent(ctx, question, "")
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "[clanker-cloud] route selected but app backend unavailable: %v\n", err)
		}
		return false, nil
	}

	if result.Status < 200 || result.Status >= 300 {
		message := strings.TrimSpace(result.FinalMessage)
		if message == "" {
			message = fmt.Sprintf("backend status %d", result.Status)
		}
		return true, fmt.Errorf("clanker-cloud request failed: %s", message)
	}

	fmt.Print("clanker-cloud> ")
	if strings.TrimSpace(result.FinalMessage) != "" {
		fmt.Println(result.FinalMessage)
	} else {
		fmt.Println("No response from Clanker Cloud.")
	}
	return true, nil
}

func runClaudeCodeTalk(parentCtx context.Context, debug bool) error {
	version, err := claudecode.CheckAvailable()
	if err != nil {
		return err
	}

	if debug {
		fmt.Fprintf(os.Stderr, "[claude-code] version: %s\n", version)
	}

	runner := claudecode.NewRunner(debug)

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	if err := runner.StartTalk(ctx); err != nil {
		return fmt.Errorf("failed to start claude-code agent: %w", err)
	}
	defer runner.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for range sigCh {
			fmt.Fprintln(os.Stderr, "\nInterrupted. Type 'exit' to quit.")
		}
	}()
	defer signal.Stop(sigCh)

	fmt.Println("Claude Code Agent (interactive mode)")
	fmt.Println("Type 'exit' or 'quit' to end the session.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		lower := strings.ToLower(input)
		if lower == "exit" || lower == "quit" || lower == "/quit" || lower == "/exit" {
			fmt.Println("Goodbye.")
			break
		}

		routedAgent, _ := determineRoutingDecision(input)
		if routedAgent == "clanker-cloud" {
			if handled, err := handleClankerCloudTalk(ctx, input, debug); handled {
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				fmt.Println()
				continue
			}
		}

		events, err := runner.Prompt(ctx, input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}

		fmt.Print("claude-code> ")
		hadDelta := false
		for event := range events {
			switch {
			case event.Error != nil:
				fmt.Fprintf(os.Stderr, "\nError: %v\n", event.Error)
			case event.Text != "":
				fmt.Print(event.Text)
				hadDelta = true
			case event.ToolCall != nil:
				if debug {
					fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", event.ToolCall.Name)
				}
			case event.Thought != "":
				if debug {
					fmt.Fprintf(os.Stderr, "\n[thinking: %s]\n", event.Thought)
				}
			case event.Final != nil:
				if !hadDelta && event.Final.Text != "" {
					fmt.Print(event.Final.Text)
				}
				if debug {
					fmt.Fprintf(os.Stderr, "\n[duration: %dms, cost: $%.4f]\n", event.Final.DurationMS, event.Final.CostUSD)
				}
			}
		}
		fmt.Println()
		fmt.Println()
	}

	return nil
}

func init() {
	rootCmd.AddCommand(talkCmd)
	talkCmd.Flags().String("agent", "", "Agent to use for conversation (default: hermes, options: hermes, claude-code)")
}
