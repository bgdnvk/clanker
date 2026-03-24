package hermes

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

//go:embed bridge.py
var embeddedBridgeScript []byte

// Runner manages the lifecycle of a Hermes bridge subprocess and provides
// methods to send prompts and receive streaming events.
type Runner struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	scanner    *bufio.Scanner
	debug      bool
	hermesPath string
	env        []string
	sessionID  string
	nextID     int
	mu         sync.Mutex
	running    bool
	bridgeFile string // temp file path if we wrote the embedded bridge
}

// FindHermesPath locates the hermes-agent vendor directory.
// It checks (in order): config value, ./vendor/hermes-agent relative to cwd,
// then relative to the executable.
func FindHermesPath() (string, error) {
	// 1. Explicit config
	if p := viper.GetString("hermes.path"); p != "" {
		abs, err := filepath.Abs(p)
		if err == nil {
			if isHermesDir(abs) {
				return abs, nil
			}
		}
		// Try as-is
		if isHermesDir(p) {
			return p, nil
		}
	}

	// 2. Relative to cwd
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "vendor", "hermes-agent")
		if isHermesDir(candidate) {
			return candidate, nil
		}
	}

	// 3. Relative to executable
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "vendor", "hermes-agent")
		if isHermesDir(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("hermes-agent not found; run 'make setup-hermes' to install")
}

func isHermesDir(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".venv", "bin", "python"))
	return err == nil && !info.IsDir()
}

// NewRunner creates a Runner but does not start the bridge process.
func NewRunner(hermesPath string, debug bool) *Runner {
	return &Runner{
		hermesPath: hermesPath,
		debug:      debug,
		sessionID:  fmt.Sprintf("clanker-%d", time.Now().UnixNano()),
	}
}

// SetEnv sets additional environment variables forwarded to the bridge process.
// Each entry must be in KEY=VALUE format.
func (r *Runner) SetEnv(env []string) {
	r.env = env
}

// Start spawns the bridge Python process and sends the initialize handshake.
func (r *Runner) Start(ctx context.Context) error {
	pythonBin := filepath.Join(r.hermesPath, ".venv", "bin", "python")
	if _, err := os.Stat(pythonBin); err != nil {
		return fmt.Errorf("python venv not found at %s: %w", pythonBin, err)
	}

	// Determine bridge script path. Prefer one in the vendor dir; fall back
	// to writing the embedded copy to a temp file.
	bridgePath := filepath.Join(r.hermesPath, "bridge.py")
	if _, err := os.Stat(bridgePath); err != nil {
		tmpFile, err := os.CreateTemp("", "hermes-bridge-*.py")
		if err != nil {
			return fmt.Errorf("failed to create temp bridge script: %w", err)
		}
		if _, err := tmpFile.Write(embeddedBridgeScript); err != nil {
			tmpFile.Close()
			return fmt.Errorf("failed to write bridge script: %w", err)
		}
		tmpFile.Close()
		bridgePath = tmpFile.Name()
		r.bridgeFile = bridgePath
	}

	r.cmd = exec.CommandContext(ctx, pythonBin, bridgePath)

	// Build environment: inherit current env, add hermes-specific vars, then
	// user-supplied vars (which may override).
	env := os.Environ()
	env = append(env, "PYTHONUNBUFFERED=1")
	if r.debug {
		env = append(env, "HERMES_BRIDGE_DEBUG=1")
	}
	env = append(env, r.env...)
	r.cmd.Env = env

	// The bridge reads from the hermes-agent root so that run_agent imports work.
	r.cmd.Dir = r.hermesPath

	var err error
	r.stdin, err = r.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// Send stderr to our stderr so the user sees bridge diagnostics.
	r.cmd.Stderr = os.Stderr

	// Use a large scanner buffer for big JSON responses.
	r.scanner = bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	r.scanner.Buffer(buf, 512*1024)

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start bridge process: %w", err)
	}
	r.running = true

	if r.debug {
		fmt.Fprintf(os.Stderr, "[hermes] bridge started (pid %d)\n", r.cmd.Process.Pid)
	}

	// Send initialize handshake.
	r.nextID = 1
	resp, err := r.call("initialize", nil)
	if err != nil {
		r.Stop()
		return fmt.Errorf("initialize handshake failed: %w", err)
	}

	var initResult InitResult
	if err := json.Unmarshal(resp.Result, &initResult); err != nil {
		r.Stop()
		return fmt.Errorf("invalid initialize response: %w", err)
	}

	if r.debug {
		fmt.Fprintf(os.Stderr, "[hermes] bridge version: %s\n", initResult.Version)
	}

	return nil
}

// call sends a request and reads a single response (no streaming).
func (r *Runner) call(method string, params interface{}) (*Response, error) {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.mu.Unlock()

	req := NewRequest(id, method, params)
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	r.mu.Lock()
	_, err = r.stdin.Write(append(data, '\n'))
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write to bridge stdin: %w", err)
	}

	// Read lines until we get a response with our ID.
	for {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return nil, fmt.Errorf("bridge stdout read error: %w", err)
			}
			return nil, fmt.Errorf("bridge process exited unexpectedly")
		}

		var resp Response
		if err := json.Unmarshal(r.scanner.Bytes(), &resp); err != nil {
			if r.debug {
				fmt.Fprintf(os.Stderr, "[hermes] skipping unparseable line: %s\n", r.scanner.Text())
			}
			continue
		}

		if resp.ID == id {
			if resp.Error != nil {
				return nil, resp.Error
			}
			return &resp, nil
		}
		// Skip notifications during the init handshake.
	}
}

// Prompt sends a user prompt and returns a channel of streaming Events.
// The channel is closed after the final result event or an error.
func (r *Runner) Prompt(ctx context.Context, text string) (<-chan Event, error) {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.mu.Unlock()

	req := NewRequest(id, "prompt", &PromptParams{
		Text:      text,
		SessionID: r.sessionID,
	})
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal prompt request: %w", err)
	}

	r.mu.Lock()
	_, err = r.stdin.Write(append(data, '\n'))
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write prompt to bridge: %w", err)
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
					ch <- Event{Error: fmt.Errorf("bridge read error: %w", err)}
				} else {
					ch <- Event{Error: fmt.Errorf("bridge process exited")}
				}
				return
			}

			var resp Response
			if err := json.Unmarshal(r.scanner.Bytes(), &resp); err != nil {
				if r.debug {
					fmt.Fprintf(os.Stderr, "[hermes] skipping bad line: %s\n", r.scanner.Text())
				}
				continue
			}

			// Response with matching ID means the prompt is complete.
			if resp.ID == id {
				if resp.Error != nil {
					ch <- Event{Error: resp.Error}
					return
				}
				var result PromptResult
				if err := json.Unmarshal(resp.Result, &result); err != nil {
					ch <- Event{Error: fmt.Errorf("parse prompt result: %w", err)}
					return
				}
				ch <- Event{Type: "final", Final: &result}
				return
			}

			// Notification (no ID, has Method).
			if resp.Method != "" {
				switch resp.Method {
				case MethodMessageDelta:
					var p MessageDeltaParams
					if err := json.Unmarshal(resp.Params, &p); err == nil {
						ch <- Event{Type: MethodMessageDelta, MessageDelta: &p}
					}
				case MethodToolCall:
					var p ToolCallParams
					if err := json.Unmarshal(resp.Params, &p); err == nil {
						ch <- Event{Type: MethodToolCall, ToolCall: &p}
					}
				case MethodThought:
					var p ThoughtParams
					if err := json.Unmarshal(resp.Params, &p); err == nil {
						ch <- Event{Type: MethodThought, Thought: &p}
					}
				default:
					if r.debug {
						fmt.Fprintf(os.Stderr, "[hermes] unknown notification: %s\n", resp.Method)
					}
				}
			}
		}
	}()

	return ch, nil
}

// PromptSync sends a prompt and blocks until the full response is received.
func (r *Runner) PromptSync(ctx context.Context, text string) (string, error) {
	ch, err := r.Prompt(ctx, text)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for event := range ch {
		if event.Error != nil {
			return "", event.Error
		}
		if event.MessageDelta != nil {
			sb.WriteString(event.MessageDelta.Text)
		}
		if event.Final != nil {
			// If no deltas were received, use the final text.
			if sb.Len() == 0 && event.Final.Text != "" {
				return event.Final.Text, nil
			}
			break
		}
	}

	return sb.String(), nil
}

// Stop gracefully shuts down the bridge process.
func (r *Runner) Stop() error {
	if !r.running {
		return nil
	}
	r.running = false

	// Close stdin to signal the bridge to exit.
	if r.stdin != nil {
		r.stdin.Close()
	}

	// Clean up temp bridge file.
	if r.bridgeFile != "" {
		os.Remove(r.bridgeFile)
	}

	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}

	// Wait for the process with a timeout.
	done := make(chan error, 1)
	go func() {
		done <- r.cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		if r.debug {
			fmt.Fprintf(os.Stderr, "[hermes] bridge did not exit in 5s, sending interrupt\n")
		}
		r.cmd.Process.Signal(os.Interrupt)
	}

	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		if r.debug {
			fmt.Fprintf(os.Stderr, "[hermes] bridge still running, killing\n")
		}
		r.cmd.Process.Kill()
		<-done
		return nil
	}
}

// IsRunning returns whether the bridge process is alive.
func (r *Runner) IsRunning() bool {
	return r.running
}
