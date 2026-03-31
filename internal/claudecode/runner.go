package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner manages invocations of the claude CLI binary.
// It supports both single-shot (Ask) and interactive (Talk) modes.
type Runner struct {
	claudePath string
	model      string
	debug      bool

	// Talk mode state
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	scanner   *bufio.Scanner
	sessionID string
	mu        sync.Mutex
	running   bool
}

// FindClaudePath locates the claude CLI binary. Returns the full path
// or an error with installation instructions.
func FindClaudePath() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf(
			"claude CLI not found in PATH.\n" +
				"Install it from: https://docs.anthropic.com/en/docs/claude-code\n" +
				"  npm install -g @anthropic-ai/claude-code\n" +
				"Then verify with: claude --version")
	}
	return path, nil
}

// CheckAvailable verifies the claude CLI is installed and returns its version.
func CheckAvailable() (string, error) {
	path, err := FindClaudePath()
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude CLI found at %s but failed to get version: %w", path, err)
	}

	version := strings.TrimSpace(string(out))
	return version, nil
}

// NewRunner creates a runner for the claude CLI. The binary path is resolved
// lazily on first use if not already set.
func NewRunner(debug bool) *Runner {
	return &Runner{
		debug: debug,
	}
}

// SetModel overrides the model for claude CLI invocations.
func (r *Runner) SetModel(model string) {
	r.model = model
}

// resolve ensures the claude binary path is known.
func (r *Runner) resolve() error {
	if r.claudePath != "" {
		return nil
	}
	path, err := FindClaudePath()
	if err != nil {
		return err
	}
	r.claudePath = path
	return nil
}

// Ask sends a single question and returns a channel of streaming events.
// The channel is closed when the response is complete or an error occurs.
func (r *Runner) Ask(ctx context.Context, prompt string) (<-chan Event, error) {
	if err := r.resolve(); err != nil {
		return nil, err
	}

	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	}

	if r.model != "" {
		args = append(args, "--model", r.model)
	}

	cmd := exec.CommandContext(ctx, r.claudePath, args...)
	cmd.Env = os.Environ()
	cmd.Stderr = nil // we parse stderr via combined with stdout below

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Send stderr to our stderr so the user sees diagnostics in debug mode.
	if r.debug {
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start claude CLI: %w", err)
	}

	if r.debug {
		fmt.Fprintf(os.Stderr, "[claude-code] process started (pid %d)\n", cmd.Process.Pid)
	}

	ch := make(chan Event, 64)

	go func() {
		defer close(ch)
		defer func() {
			_ = cmd.Wait()
		}()

		scanner := bufio.NewScanner(stdoutPipe)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var raw StreamEvent
			if err := json.Unmarshal(line, &raw); err != nil {
				if r.debug {
					fmt.Fprintf(os.Stderr, "[claude-code] skipping unparseable line: %s\n", string(line))
				}
				continue
			}

			events := r.parseStreamEvent(&raw)
			for _, ev := range events {
				select {
				case ch <- ev:
				case <-ctx.Done():
					ch <- Event{Error: ctx.Err()}
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- Event{Error: fmt.Errorf("claude CLI read error: %w", err)}
		}
	}()

	return ch, nil
}

// AskSync sends a prompt and blocks until the full response is available.
func (r *Runner) AskSync(ctx context.Context, prompt string) (string, error) {
	ch, err := r.Ask(ctx, prompt)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for event := range ch {
		if event.Error != nil {
			return "", event.Error
		}
		if event.Text != "" {
			sb.WriteString(event.Text)
		}
		if event.Final != nil && sb.Len() == 0 {
			return event.Final.Text, nil
		}
	}

	return sb.String(), nil
}

// StartTalk launches an interactive session using claude's stdin streaming.
// The process stays alive across multiple Prompt() calls.
func (r *Runner) StartTalk(ctx context.Context) error {
	if err := r.resolve(); err != nil {
		return err
	}

	args := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
	}

	if r.model != "" {
		args = append(args, "--model", r.model)
	}

	r.cmd = exec.CommandContext(ctx, r.claudePath, args...)
	r.cmd.Env = os.Environ()

	if r.debug {
		r.cmd.Stderr = os.Stderr
	}

	var err error
	r.stdin, err = r.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	r.stdout, err = r.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	r.scanner = bufio.NewScanner(r.stdout)
	buf := make([]byte, 0, 64*1024)
	r.scanner.Buffer(buf, 1024*1024)

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start claude CLI: %w", err)
	}
	r.running = true

	if r.debug {
		fmt.Fprintf(os.Stderr, "[claude-code] interactive session started (pid %d)\n", r.cmd.Process.Pid)
	}

	// Read the init event to get the session ID.
	if r.scanner.Scan() {
		var raw StreamEvent
		if err := json.Unmarshal(r.scanner.Bytes(), &raw); err == nil {
			if raw.Type == "system" && raw.Subtype == "init" {
				r.sessionID = raw.SessionID
				if r.debug {
					fmt.Fprintf(os.Stderr, "[claude-code] session: %s, model: %s\n", raw.SessionID, raw.Model)
				}
			}
		}
	}

	return nil
}

// Prompt sends a message in an interactive talk session and returns streaming events.
func (r *Runner) Prompt(ctx context.Context, text string) (<-chan Event, error) {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil, fmt.Errorf("claude-code session not running")
	}

	// Claude Code stream-json input format: one JSON object per line.
	msg := map[string]string{
		"type":       "user",
		"content":    text,
		"session_id": r.sessionID,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("marshal prompt: %w", err)
	}

	_, err = r.stdin.Write(append(data, '\n'))
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write to claude-code stdin: %w", err)
	}

	ch := make(chan Event, 64)

	go func() {
		defer close(ch)

		for {
			select {
			case <-ctx.Done():
				ch <- Event{Error: ctx.Err()}
				return
			default:
			}

			if !r.scanner.Scan() {
				if err := r.scanner.Err(); err != nil {
					ch <- Event{Error: fmt.Errorf("claude-code read error: %w", err)}
				} else {
					ch <- Event{Error: fmt.Errorf("claude-code process exited")}
				}
				r.running = false
				return
			}

			var raw StreamEvent
			if err := json.Unmarshal(r.scanner.Bytes(), &raw); err != nil {
				if r.debug {
					fmt.Fprintf(os.Stderr, "[claude-code] skipping bad line: %s\n", r.scanner.Text())
				}
				continue
			}

			events := r.parseStreamEvent(&raw)
			for _, ev := range events {
				ch <- ev
			}

			// A result event means the turn is complete.
			if raw.Type == "result" {
				return
			}
		}
	}()

	return ch, nil
}

// Stop gracefully shuts down the interactive session.
func (r *Runner) Stop() error {
	if !r.running {
		return nil
	}
	r.running = false

	if r.stdin != nil {
		r.stdin.Close()
	}

	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- r.cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		if r.debug {
			fmt.Fprintf(os.Stderr, "[claude-code] process did not exit in 5s, sending interrupt\n")
		}
		_ = r.cmd.Process.Signal(os.Interrupt)
	}

	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		if r.debug {
			fmt.Fprintf(os.Stderr, "[claude-code] process still running, killing\n")
		}
		_ = r.cmd.Process.Kill()
		<-done
		return nil
	}
}

// IsRunning reports whether the interactive session is alive.
func (r *Runner) IsRunning() bool {
	return r.running
}

// parseStreamEvent converts a raw claude CLI stream event into zero or more
// normalized Event values.
func (r *Runner) parseStreamEvent(raw *StreamEvent) []Event {
	switch raw.Type {
	case "system":
		// Init event, skip (already handled in StartTalk).
		return nil

	case "assistant":
		if raw.Message == nil || len(raw.Message.Content) == 0 {
			return nil
		}
		var events []Event
		for _, block := range raw.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					events = append(events, Event{
						Type: "message_delta",
						Text: block.Text,
					})
				}
			case "tool_use":
				inputStr := ""
				if block.Input != nil {
					if b, err := json.Marshal(block.Input); err == nil {
						inputStr = string(b)
					}
				}
				events = append(events, Event{
					Type: "tool_call",
					ToolCall: &ToolCallInfo{
						Name:  block.Name,
						Input: inputStr,
					},
				})
			case "tool_result":
				// Tool results are internal, skip for display purposes.
			case "thinking":
				if block.Text != "" {
					events = append(events, Event{
						Type:    "thought",
						Thought: block.Text,
					})
				}
			}
		}
		return events

	case "result":
		return []Event{{
			Type: "final",
			Final: &FinalResult{
				Text:       raw.Result,
				SessionID:  raw.SessionID,
				DurationMS: raw.DurationMS,
				CostUSD:    raw.TotalCost,
			},
		}}

	case "rate_limit_event":
		// Informational, skip.
		return nil

	default:
		if r.debug {
			fmt.Fprintf(os.Stderr, "[claude-code] unhandled event type: %s\n", raw.Type)
		}
		return nil
	}
}
