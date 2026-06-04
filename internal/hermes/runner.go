package hermes

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
)

//go:embed bridge.py
var embeddedBridgeScript []byte

// ErrBridgeExited is returned by Prompt / PromptSync when the bridge
// subprocess died mid-call. cmd/talk uses this to drive its automatic
// restart loop — see clanker-cli #21.
var ErrBridgeExited = errors.New("hermes bridge process exited")

// Runner manages the lifecycle of a Hermes bridge subprocess and provides
// methods to send prompts and receive streaming events.
//
// Concurrency model (clanker-cli #20):
//   - A single dispatcher goroutine owns r.scanner — no other goroutine
//     touches it. Responses are routed by ID via the `inbox` map and
//     notifications go to the currently-active prompt's `notifSink`.
//   - Prompts are serialised by `promptMu` because the bridge protocol
//     does NOT tag notifications with a request ID; the dispatcher
//     can't demux them across overlapping prompts. Today's callers
//     (cmd/talk REPL, cmd/ask PromptSync) are already serial. The
//     mutex pins that invariant explicitly so a future caller that
//     fires concurrent prompts blocks safely instead of corrupting
//     state.
//   - `running` is atomic.Bool — read by IsRunning, written by Start
//     and Stop. No data race even though Stop runs on a different
//     goroutine than the dispatcher's exit path.
type Runner struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	scanner    *bufio.Scanner
	debug      bool
	hermesPath string
	env        []string
	sessionID  string
	bridgeFile string // temp file path if we wrote the embedded bridge

	mu     sync.Mutex // guards: nextID, inbox, notifSink, dispatchErr
	nextID int

	// inbox routes ID-matched responses (call() + Prompt completion)
	// from the dispatcher to whichever goroutine is awaiting that ID.
	// Each Prompt/call allocates an ID, registers a 1-slot channel, and
	// deregisters on completion.
	inbox map[int]chan *Response

	// notifSink receives notifications (responses with Method != ""
	// and no ID). Set under r.mu by a Prompt call before sending the
	// request and cleared on completion. Serialised by promptMu so at
	// most one stream-consumer exists at a time.
	notifSink chan<- *Response

	// promptMu serialises Prompt calls. The bridge protocol does not
	// tag notifications with a request ID — overlapping prompts
	// cannot be demuxed. Hold this for the entire lifetime of a
	// Prompt call (request → final response).
	promptMu sync.Mutex

	// dispatchErr is set by the dispatcher when it exits (scanner
	// EOF, read error, or panic). Subsequent callers see it via
	// shutdownReason() so they can surface a useful message.
	dispatchErr  error
	dispatchDone chan struct{}

	running atomic.Bool

	// stderrTail captures the last few KB of bridge stderr so the
	// "Clanker talk restart" path (#21) can include the actual error
	// (e.g. "ModuleNotFoundError: pydantic_ai") in its message instead
	// of just saying "bridge exited."
	stderrTail *ringBuffer
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
		inbox:      make(map[int]chan *Response),
		nextID:     1,
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

	// Tee stderr to a small ring buffer for the restart-error message
	// path (clanker-cli #21) while still forwarding to the user's
	// terminal for real-time diagnostics.
	r.stderrTail = newRingBuffer(4 * 1024)
	stderrPipe, err := r.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start bridge process: %w", err)
	}

	// Start stderr drainer. Tees bridge stderr to os.Stderr (live diag)
	// AND a small ring buffer (#21 restart messages).
	go func() {
		_, _ = io.Copy(io.MultiWriter(os.Stderr, r.stderrTail), stderrPipe)
	}()

	// Wire scanner + dispatcher BEFORE the handshake. The dispatcher
	// has been the sole reader of r.scanner since clanker-cli #20.
	if err := r.startWithStreams(stdout); err != nil {
		r.Stop()
		return err
	}

	r.running.Store(true)

	if r.debug {
		fmt.Fprintf(os.Stderr, "[hermes] bridge started (pid %d)\n", r.cmd.Process.Pid)
	}

	// Send initialize handshake — uses call() which now goes through
	// the dispatcher's inbox routing.
	resp, err := r.call(ctx, "initialize", nil)
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

// startWithStreams wires the scanner + dispatcher around a caller-supplied
// stdout stream. Split out from Start() so tests can drive a mock bridge
// over an io.Pipe without spawning a Python process.
func (r *Runner) startWithStreams(stdout io.Reader) error {
	r.scanner = bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	r.scanner.Buffer(buf, 512*1024)
	r.dispatchDone = make(chan struct{})
	r.dispatchErr = nil
	go r.dispatch()
	return nil
}

// dispatch is the single goroutine that reads r.scanner. It owns the
// scanner exclusively — no other goroutine touches it. Responses are
// routed to the registered inbox channel by ID; notifications go to
// the currently-active prompt's notifSink. On EOF / read error / panic
// it closes every pending inbox channel with ErrBridgeExited so callers
// don't block forever.
func (r *Runner) dispatch() {
	defer func() {
		if p := recover(); p != nil {
			r.mu.Lock()
			if r.dispatchErr == nil {
				r.dispatchErr = fmt.Errorf("hermes dispatcher panic: %v", p)
			}
			r.mu.Unlock()
			if r.debug {
				fmt.Fprintf(os.Stderr, "[hermes] dispatcher panic: %v\n%s\n", p, debug.Stack())
			}
		}
		r.shutdownInbox()
		close(r.dispatchDone)
	}()

	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			if r.debug {
				fmt.Fprintf(os.Stderr, "[hermes] skipping unparseable line: %s\n", r.scanner.Text())
			}
			continue
		}

		// ID > 0 → response to a request. Route to the registered channel.
		if resp.ID > 0 {
			r.mu.Lock()
			ch, ok := r.inbox[resp.ID]
			if ok {
				delete(r.inbox, resp.ID)
			}
			r.mu.Unlock()
			if ok {
				// Non-blocking send: inbox channels are 1-buffered and
				// only the awaiting goroutine reads from them, so this
				// never blocks. Use select-default as defence in depth
				// in case a caller abandoned (ctx-cancelled).
				select {
				case ch <- &resp:
				default:
				}
			}
			continue
		}

		// No ID → notification. Route to the currently-active prompt's
		// sink, if any. Drop silently if no prompt is in flight (or if
		// the sink buffer is full; better to drop a delta than block
		// the dispatcher).
		if resp.Method != "" {
			r.mu.Lock()
			sink := r.notifSink
			r.mu.Unlock()
			if sink != nil {
				select {
				case sink <- &resp:
				default:
					if r.debug {
						fmt.Fprintf(os.Stderr, "[hermes] dropped notification (sink full): %s\n", resp.Method)
					}
				}
			}
		}
	}

	// Scanner exited. Record the reason and fan it out to every
	// pending caller via shutdownInbox in the deferred path above.
	r.mu.Lock()
	if r.dispatchErr == nil {
		if err := r.scanner.Err(); err != nil {
			r.dispatchErr = fmt.Errorf("%w: %v", ErrBridgeExited, err)
		} else {
			r.dispatchErr = ErrBridgeExited
		}
	}
	r.mu.Unlock()
}

// shutdownInbox closes every registered inbox channel with the recorded
// dispatchErr so callers blocked in call() / Prompt unblock instead of
// hanging forever. notifSink is also closed.
func (r *Runner) shutdownInbox() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Closing the inbox channel signals the awaiter that the bridge died.
	// Receivers detect this via the second return value of `<-ch`.
	for id, ch := range r.inbox {
		close(ch)
		delete(r.inbox, id)
	}
	if r.notifSink != nil {
		// Don't close — receiver does that. Just drop the reference so
		// new sends from any latecomer hit the nil-check above.
		r.notifSink = nil
	}
}

// shutdownReason returns the dispatcher's recorded exit error, or a
// generic "bridge process exited" if none was set. Used by Prompt /
// call when their inbox channel closes before yielding a response.
func (r *Runner) shutdownReason() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatchErr != nil {
		// Augment with the stderr tail so the talk restart path (#21)
		// gets useful "ModuleNotFoundError: pydantic_ai" diagnostics
		// instead of just "bridge process exited".
		if r.stderrTail != nil {
			tail := strings.TrimSpace(r.stderrTail.String())
			if tail != "" {
				return fmt.Errorf("%w; stderr tail: %s", r.dispatchErr, lastLines(tail, 3))
			}
		}
		return r.dispatchErr
	}
	return ErrBridgeExited
}

// registerInbox allocates the next request ID and registers a 1-slot
// channel for the dispatcher to deliver the matching response to. The
// returned channel is closed by the dispatcher on bridge death so the
// caller's `<-ch` unblocks. Caller MUST defer-delete by ID to avoid
// leaking entries when ctx cancels mid-call.
func (r *Runner) registerInbox() (int, chan *Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	ch := make(chan *Response, 1)
	r.inbox[id] = ch
	return id, ch
}

func (r *Runner) clearInbox(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inbox, id)
}

// writeRequest serialises a JSON-RPC request to the bridge stdin under
// the mu lock so concurrent writers can't interleave bytes.
func (r *Runner) writeRequest(req *Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stdin == nil {
		return ErrBridgeExited
	}
	if _, err := r.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write to bridge stdin: %w", err)
	}
	return nil
}

// call sends a request and waits for the response with the matching ID.
func (r *Runner) call(ctx context.Context, method string, params any) (*Response, error) {
	id, ch := r.registerInbox()
	defer r.clearInbox(id)

	req := NewRequest(id, method, params)
	if err := r.writeRequest(req); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, r.shutdownReason()
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	}
}

// Prompt sends a user prompt and returns a channel of streaming Events.
// Serialised by promptMu — only one Prompt may be in flight per Runner
// at a time because the bridge protocol doesn't tag notifications with
// a request ID. Today's callers are all sequential; this lock pins
// that invariant so a future concurrent caller blocks instead of
// corrupting state.
func (r *Runner) Prompt(ctx context.Context, text string) (<-chan Event, error) {
	// Hold the prompt lock for the lifetime of the call. The goroutine
	// below releases it when it exits (final response or error).
	r.promptMu.Lock()

	id, inboxCh := r.registerInbox()
	notif := make(chan *Response, 64)

	// Install ourselves as the current notification sink. The dispatcher
	// fans notifications here until we clear it on exit.
	r.mu.Lock()
	r.notifSink = notif
	r.mu.Unlock()

	req := NewRequest(id, "prompt", &PromptParams{
		Text:      text,
		SessionID: r.sessionID,
	})
	if err := r.writeRequest(req); err != nil {
		r.clearInbox(id)
		r.mu.Lock()
		r.notifSink = nil
		r.mu.Unlock()
		r.promptMu.Unlock()
		return nil, fmt.Errorf("write prompt to bridge: %w", err)
	}

	out := make(chan Event, 64)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				if r.debug {
					fmt.Fprintf(os.Stderr, "[hermes] prompt goroutine panic: %v\n%s\n", p, debug.Stack())
				}
				select {
				case out <- Event{Error: fmt.Errorf("hermes prompt panic: %v", p)}:
				default:
				}
			}
			r.clearInbox(id)
			r.mu.Lock()
			r.notifSink = nil
			r.mu.Unlock()
			close(out)
			r.promptMu.Unlock()
		}()

		for {
			select {
			case <-ctx.Done():
				out <- Event{Error: ctx.Err()}
				return
			case n, ok := <-notif:
				if !ok {
					out <- Event{Error: r.shutdownReason()}
					return
				}
				ev := translateNotification(n, r.debug)
				if ev != nil {
					out <- *ev
				}
			case resp, ok := <-inboxCh:
				if !ok {
					out <- Event{Error: r.shutdownReason()}
					return
				}
				if resp.Error != nil {
					out <- Event{Error: resp.Error}
					return
				}
				// Drain queued notifications first. The dispatcher
				// emits deltas before the final response on the wire,
				// but select picks between buffered channels at
				// random — without this drain a fast bridge can hand
				// us a "final" before we've flushed earlier deltas
				// already sitting in `notif`.
			drain:
				for {
					select {
					case n, ok := <-notif:
						if !ok {
							break drain
						}
						if ev := translateNotification(n, r.debug); ev != nil {
							out <- *ev
						}
					default:
						break drain
					}
				}
				var result PromptResult
				if err := json.Unmarshal(resp.Result, &result); err != nil {
					out <- Event{Error: fmt.Errorf("parse prompt result: %w", err)}
					return
				}
				out <- Event{Type: "final", Final: &result}
				return
			}
		}
	}()

	return out, nil
}

// translateNotification turns a JSON-RPC notification into a typed Event.
// Returns nil for unknown methods (logged in debug mode).
func translateNotification(n *Response, debugMode bool) *Event {
	switch n.Method {
	case MethodMessageDelta:
		var p MessageDeltaParams
		if err := json.Unmarshal(n.Params, &p); err == nil {
			return &Event{Type: MethodMessageDelta, MessageDelta: &p}
		}
	case MethodToolCall:
		var p ToolCallParams
		if err := json.Unmarshal(n.Params, &p); err == nil {
			return &Event{Type: MethodToolCall, ToolCall: &p}
		}
	case MethodThought:
		var p ThoughtParams
		if err := json.Unmarshal(n.Params, &p); err == nil {
			return &Event{Type: MethodThought, Thought: &p}
		}
	default:
		if debugMode {
			fmt.Fprintf(os.Stderr, "[hermes] unknown notification: %s\n", n.Method)
		}
	}
	return nil
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

// Stop gracefully shuts down the bridge process. Safe to call from any
// goroutine; idempotent.
func (r *Runner) Stop() error {
	if !r.running.CompareAndSwap(true, false) {
		return nil
	}

	// Close stdin to signal the bridge to exit.
	if r.stdin != nil {
		r.stdin.Close()
	}

	// Wait for the dispatcher to drain (scanner.Scan returns false when
	// stdout closes). If it doesn't, the process kill below will force it.
	if r.dispatchDone != nil {
		select {
		case <-r.dispatchDone:
		case <-time.After(2 * time.Second):
		}
	}

	// Clean up temp bridge file even if the process never started fully —
	// previously the early-error path leaked the temp file.
	if r.bridgeFile != "" {
		os.Remove(r.bridgeFile)
		r.bridgeFile = ""
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

// IsRunning returns whether the bridge process is alive. Cheap atomic
// read; safe to call from any goroutine.
func (r *Runner) IsRunning() bool {
	return r.running.Load()
}

// IsBridgeExitError reports whether err originated from the dispatcher
// noticing the bridge died (EOF, read error, or scanner failure). Used
// by cmd/talk to decide whether to restart the bridge or surface the
// error to the user. See clanker-cli #21.
func IsBridgeExitError(err error) bool {
	return errors.Is(err, ErrBridgeExited)
}

// lastLines returns up to n trailing lines of s, separated by `;` so
// the result fits cleanly on one error-line.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
