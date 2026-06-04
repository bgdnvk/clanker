package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestRunner creates a Runner wired against caller-supplied pipes
// instead of an exec.Cmd. The bridge.py subprocess is replaced by a
// goroutine that reads request JSON from `in` and writes response JSON
// to `out`. Tests use this to exercise the dispatcher under -race
// without spawning Python.
func newTestRunner(t *testing.T, stdinWriter io.WriteCloser, stdoutReader io.Reader) *Runner {
	t.Helper()
	r := &Runner{
		debug:     false,
		stdin:     stdinWriter,
		sessionID: "test",
		inbox:     make(map[int]chan *Response),
		nextID:    1,
	}
	if err := r.startWithStreams(stdoutReader); err != nil {
		t.Fatalf("startWithStreams: %v", err)
	}
	r.running.Store(true)
	return r
}

// teeReader records every line read so the fake bridge in the test can
// know which IDs to respond to. Each test below uses a goroutine to
// scan stdin and synthesise responses + notifications.
type pipePair struct {
	stdinR  io.ReadCloser
	stdinW  io.WriteCloser
	stdoutR io.Reader
	stdoutW io.WriteCloser
}

func newPipePair() pipePair {
	sr, sw := io.Pipe()
	or, ow := io.Pipe()
	return pipePair{stdinR: sr, stdinW: sw, stdoutR: or, stdoutW: ow}
}

// fakeBridge reads JSON-RPC requests off stdin and dispatches them via
// the supplied responder. Returns when stdin closes.
func fakeBridge(t *testing.T, p pipePair, responder func(req Request) []Response) {
	t.Helper()
	decoder := json.NewDecoder(p.stdinR)
	for {
		var req Request
		if err := decoder.Decode(&req); err != nil {
			return
		}
		for _, resp := range responder(req) {
			data, err := json.Marshal(resp)
			if err != nil {
				t.Errorf("fakeBridge marshal: %v", err)
				return
			}
			if _, err := p.stdoutW.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}
}

// TestDispatcher_ResponseRoutedByID — the load-bearing assertion from
// #20. Two requests in flight (call + prompt would race the scanner
// pre-fix). Here we exercise the routing layer by issuing back-to-back
// calls and verifying each gets its own response.
func TestDispatcher_ResponseRoutedByID(t *testing.T) {
	p := newPipePair()
	go fakeBridge(t, p, func(req Request) []Response {
		// Echo a Result envelope with the request method tag.
		return []Response{{
			ID:     req.ID,
			Result: jsonRaw(t, map[string]string{"echo": req.Method}),
		}}
	})

	r := newTestRunner(t, p.stdinW, p.stdoutR)
	defer func() { _ = p.stdinW.Close(); _ = p.stdoutW.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp1, err := r.call(ctx, "first", nil)
	if err != nil {
		t.Fatalf("call(first): %v", err)
	}
	resp2, err := r.call(ctx, "second", nil)
	if err != nil {
		t.Fatalf("call(second): %v", err)
	}
	if string(resp1.Result) == string(resp2.Result) {
		t.Errorf("expected distinct responses, got identical: %s", resp1.Result)
	}
	if !strings.Contains(string(resp1.Result), `"first"`) {
		t.Errorf("first response should contain method tag 'first', got: %s", resp1.Result)
	}
}

// TestDispatcher_PromptNotificationsThenFinal exercises the Prompt path:
// the bridge sends two MessageDelta notifications then a final response.
// The Event channel should yield deltas in order then a Final.
func TestDispatcher_PromptNotificationsThenFinal(t *testing.T) {
	p := newPipePair()
	go fakeBridge(t, p, func(req Request) []Response {
		if req.Method != "prompt" {
			return []Response{{ID: req.ID, Result: jsonRaw(t, map[string]any{})}}
		}
		return []Response{
			{Method: MethodMessageDelta, Params: jsonRaw(t, MessageDeltaParams{Text: "hello "})},
			{Method: MethodMessageDelta, Params: jsonRaw(t, MessageDeltaParams{Text: "world"})},
			{ID: req.ID, Result: jsonRaw(t, PromptResult{Text: "hello world"})},
		}
	})

	r := newTestRunner(t, p.stdinW, p.stdoutR)
	defer func() { _ = p.stdinW.Close(); _ = p.stdoutW.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := r.Prompt(ctx, "anything")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var deltas []string
	var final *PromptResult
	for ev := range ch {
		if ev.Error != nil {
			t.Fatalf("event error: %v", ev.Error)
		}
		if ev.MessageDelta != nil {
			deltas = append(deltas, ev.MessageDelta.Text)
		}
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if got, want := strings.Join(deltas, ""), "hello world"; got != want {
		t.Errorf("deltas joined = %q, want %q", got, want)
	}
	if final == nil {
		t.Fatal("expected a Final event, got none")
	}
	if final.Text != "hello world" {
		t.Errorf("final text = %q, want %q", final.Text, "hello world")
	}
}

// TestDispatcher_SerializesConcurrentPrompts proves the promptMu lock
// works. Two goroutines issue Prompt at once; the lock should make them
// run end-to-end one after the other rather than interleaving. Each
// prompt's deltas should arrive on its own channel with no cross-talk.
func TestDispatcher_SerializesConcurrentPrompts(t *testing.T) {
	p := newPipePair()
	go fakeBridge(t, p, func(req Request) []Response {
		if req.Method != "prompt" {
			return []Response{{ID: req.ID, Result: jsonRaw(t, map[string]any{})}}
		}
		paramsJSON, _ := json.Marshal(req.Params)
		var pp PromptParams
		_ = json.Unmarshal(paramsJSON, &pp)
		return []Response{
			{Method: MethodMessageDelta, Params: jsonRaw(t, MessageDeltaParams{Text: "[" + pp.Text + "]"})},
			{ID: req.ID, Result: jsonRaw(t, PromptResult{Text: pp.Text})},
		}
	})

	r := newTestRunner(t, p.stdinW, p.stdoutR)
	defer func() { _ = p.stdinW.Close(); _ = p.stdoutW.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	results := make(chan string, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			out, err := r.PromptSync(ctx, text)
			if err != nil {
				t.Errorf("PromptSync(%s): %v", text, err)
				return
			}
			results <- out + "|" + text
		}(text)
	}
	wg.Wait()
	close(results)

	seen := map[string]bool{}
	for r := range results {
		seen[r] = true
	}
	// Each goroutine should receive ITS OWN response — the dispatcher
	// must not have crossed the streams.
	if !seen["[alpha]|alpha"] {
		t.Errorf("alpha goroutine did not receive its own delta+final (results: %v)", seen)
	}
	if !seen["[beta]|beta"] {
		t.Errorf("beta goroutine did not receive its own delta+final (results: %v)", seen)
	}
}

// TestDispatcher_BridgeDeathUnblocksCallers — when the bridge stdout
// closes mid-call, every pending call() / Prompt should unblock with
// ErrBridgeExited. Pre-fix the awaiter looped forever on scanner.Scan
// returning false but never reaching the inbox channel.
func TestDispatcher_BridgeDeathUnblocksCallers(t *testing.T) {
	p := newPipePair()
	// Drain stdin (acting as a no-op bridge) so the request write
	// doesn't block on an io.Pipe with no reader.
	go func() { _, _ = io.Copy(io.Discard, p.stdinR) }()
	r := newTestRunner(t, p.stdinW, p.stdoutR)
	defer func() { _ = p.stdinW.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := r.call(ctx, "ping", nil)
		errCh <- err
	}()

	// Give the call goroutine a moment to write its request.
	time.Sleep(50 * time.Millisecond)

	// Simulate bridge dying: close stdout from "bridge" side.
	_ = p.stdoutW.Close()

	select {
	case err := <-errCh:
		if !IsBridgeExitError(err) {
			t.Errorf("expected ErrBridgeExited, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("call did not unblock after bridge death within 1s — regression of #20")
	}
}

// TestDispatcher_DroppedNotificationDoesNotBlock — if the notif sink
// buffer fills (rare in practice but possible if the consumer is slow),
// the dispatcher must drop rather than block. Otherwise one stuck
// consumer wedges the whole runner including the final-response path.
func TestDispatcher_DroppedNotificationDoesNotBlock(t *testing.T) {
	p := newPipePair()
	go fakeBridge(t, p, func(req Request) []Response {
		if req.Method != "prompt" {
			return []Response{{ID: req.ID, Result: jsonRaw(t, map[string]any{})}}
		}
		// Send 200 deltas (more than the 64-buffer notif sink) then final.
		resps := make([]Response, 0, 201)
		for range 200 {
			resps = append(resps, Response{Method: MethodMessageDelta, Params: jsonRaw(t, MessageDeltaParams{Text: "x"})})
		}
		resps = append(resps, Response{ID: req.ID, Result: jsonRaw(t, PromptResult{Text: "done"})})
		return resps
	})

	r := newTestRunner(t, p.stdinW, p.stdoutR)
	defer func() { _ = p.stdinW.Close(); _ = p.stdoutW.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	// The consumer (PromptSync) drains the channel so deltas flow. The
	// concern here is that even under flood the final response still
	// arrives — verifies the dispatcher's select-default drop semantics.
	final, err := r.PromptSync(ctx, "flood")
	if err != nil {
		t.Fatalf("PromptSync under flood: %v", err)
	}
	if final == "" {
		t.Error("expected text output from 200-delta flood")
	}
}

func TestIsBridgeExitError(t *testing.T) {
	if !IsBridgeExitError(ErrBridgeExited) {
		t.Error("ErrBridgeExited should be recognised by IsBridgeExitError")
	}
	wrapped := errors.New("oh no")
	if IsBridgeExitError(wrapped) {
		t.Error("plain error should not be recognised as bridge-exit")
	}
	if !IsBridgeExitError(errors.Join(ErrBridgeExited, errors.New("io error"))) {
		t.Error("errors.Join with ErrBridgeExited should be recognised")
	}
}

func jsonRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
