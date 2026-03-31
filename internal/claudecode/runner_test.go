package claudecode

import (
	"encoding/json"
	"testing"
)

func TestFindClaudePath(t *testing.T) {
	path, err := FindClaudePath()
	if err != nil {
		t.Skipf("claude CLI not installed, skipping: %v", err)
	}
	if path == "" {
		t.Fatal("FindClaudePath returned empty path with no error")
	}
}

func TestCheckAvailable(t *testing.T) {
	version, err := CheckAvailable()
	if err != nil {
		t.Skipf("claude CLI not available, skipping: %v", err)
	}
	if version == "" {
		t.Fatal("CheckAvailable returned empty version")
	}
	t.Logf("claude CLI version: %s", version)
}

func TestParseStreamEvent_System(t *testing.T) {
	r := &Runner{debug: false}
	raw := &StreamEvent{
		Type:    "system",
		Subtype: "init",
	}
	events := r.parseStreamEvent(raw)
	if len(events) != 0 {
		t.Fatalf("expected 0 events for system init, got %d", len(events))
	}
}

func TestParseStreamEvent_AssistantText(t *testing.T) {
	r := &Runner{debug: false}
	raw := &StreamEvent{
		Type: "assistant",
		Message: &AssistantMessage{
			Content: []ContentBlock{
				{Type: "text", Text: "Hello world"},
			},
		},
	}
	events := r.parseStreamEvent(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "message_delta" {
		t.Errorf("expected type message_delta, got %s", events[0].Type)
	}
	if events[0].Text != "Hello world" {
		t.Errorf("expected text 'Hello world', got %q", events[0].Text)
	}
}

func TestParseStreamEvent_AssistantToolUse(t *testing.T) {
	r := &Runner{debug: false}
	raw := &StreamEvent{
		Type: "assistant",
		Message: &AssistantMessage{
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Bash", Input: map[string]any{"command": "ls"}},
			},
		},
	}
	events := r.parseStreamEvent(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "tool_call" {
		t.Errorf("expected type tool_call, got %s", events[0].Type)
	}
	if events[0].ToolCall == nil {
		t.Fatal("expected ToolCall to be set")
	}
	if events[0].ToolCall.Name != "Bash" {
		t.Errorf("expected tool name Bash, got %s", events[0].ToolCall.Name)
	}
}

func TestParseStreamEvent_Result(t *testing.T) {
	r := &Runner{debug: false}
	raw := &StreamEvent{
		Type:       "result",
		Result:     "final answer",
		SessionID:  "session-123",
		DurationMS: 1500,
		TotalCost:  0.05,
	}
	events := r.parseStreamEvent(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "final" {
		t.Errorf("expected type final, got %s", events[0].Type)
	}
	if events[0].Final.Text != "final answer" {
		t.Errorf("expected final text 'final answer', got %q", events[0].Final.Text)
	}
	if events[0].Final.CostUSD != 0.05 {
		t.Errorf("expected cost 0.05, got %f", events[0].Final.CostUSD)
	}
}

func TestParseStreamEvent_RateLimit(t *testing.T) {
	r := &Runner{debug: false}
	raw := &StreamEvent{Type: "rate_limit_event"}
	events := r.parseStreamEvent(raw)
	if len(events) != 0 {
		t.Fatalf("expected 0 events for rate_limit_event, got %d", len(events))
	}
}

func TestParseStreamEvent_MultipleBlocks(t *testing.T) {
	r := &Runner{debug: false}
	raw := &StreamEvent{
		Type: "assistant",
		Message: &AssistantMessage{
			Content: []ContentBlock{
				{Type: "thinking", Text: "Let me think..."},
				{Type: "text", Text: "Here is my answer"},
				{Type: "tool_use", Name: "Read", Input: map[string]any{"file": "foo.go"}},
			},
		},
	}
	events := r.parseStreamEvent(raw)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Type != "thought" {
		t.Errorf("expected thought, got %s", events[0].Type)
	}
	if events[1].Type != "message_delta" {
		t.Errorf("expected message_delta, got %s", events[1].Type)
	}
	if events[2].Type != "tool_call" {
		t.Errorf("expected tool_call, got %s", events[2].Type)
	}
}

func TestStreamEventJSON(t *testing.T) {
	// Test parsing of a real stream-json line from claude CLI.
	line := `{"type":"result","subtype":"success","is_error":false,"duration_ms":2393,"num_turns":1,"result":"hello","stop_reason":"end_turn","session_id":"abc-123","total_cost_usd":0.113}`
	var raw StreamEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("failed to parse result event: %v", err)
	}
	if raw.Type != "result" {
		t.Errorf("expected type result, got %s", raw.Type)
	}
	if raw.Result != "hello" {
		t.Errorf("expected result 'hello', got %q", raw.Result)
	}
	if raw.SessionID != "abc-123" {
		t.Errorf("expected session_id 'abc-123', got %q", raw.SessionID)
	}
}

func TestResultEventJSON(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"result":"test output","duration_ms":500,"total_cost_usd":0.01,"session_id":"s1"}`
	var re ResultEvent
	if err := json.Unmarshal([]byte(line), &re); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if re.Result != "test output" {
		t.Errorf("expected 'test output', got %q", re.Result)
	}
}
