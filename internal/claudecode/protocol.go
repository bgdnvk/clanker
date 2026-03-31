package claudecode

import "encoding/json"

// StreamEvent represents a single line of output from claude --output-format stream-json.
type StreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// Present when Type == "system" && Subtype == "init"
	SessionID string   `json:"session_id,omitempty"`
	Model     string   `json:"model,omitempty"`
	Tools     []string `json:"tools,omitempty"`

	// Present when Type == "assistant"
	Message *AssistantMessage `json:"message,omitempty"`

	// Present when Type == "result"
	Result     string  `json:"result,omitempty"`
	IsError    bool    `json:"is_error,omitempty"`
	StopReason string  `json:"stop_reason,omitempty"`
	DurationMS int     `json:"duration_ms,omitempty"`
	TotalCost  float64 `json:"total_cost_usd,omitempty"`
}

// AssistantMessage is the message object inside an assistant event.
type AssistantMessage struct {
	ID      string           `json:"id,omitempty"`
	Role    string           `json:"role,omitempty"`
	Content []ContentBlock   `json:"content,omitempty"`
	Usage   *json.RawMessage `json:"usage,omitempty"`
}

// ContentBlock represents a single block of content in a message.
type ContentBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// ResultEvent is the final event from a claude --output-format json run.
type ResultEvent struct {
	Type       string  `json:"type"`
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	StopReason string  `json:"stop_reason,omitempty"`
	DurationMS int     `json:"duration_ms,omitempty"`
	TotalCost  float64 `json:"total_cost_usd,omitempty"`
	SessionID  string  `json:"session_id,omitempty"`
}

// Event is the normalized event type consumed by callers, matching the
// pattern established by the hermes package.
type Event struct {
	Type     string
	Text     string
	ToolCall *ToolCallInfo
	Thought  string
	Final    *FinalResult
	Error    error
}

// ToolCallInfo holds details about a tool invocation by the agent.
type ToolCallInfo struct {
	Name  string
	Input string
}

// FinalResult holds the completed response from the agent.
type FinalResult struct {
	Text       string
	SessionID  string
	DurationMS int
	CostUSD    float64
}
