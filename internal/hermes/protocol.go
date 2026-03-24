package hermes

import "encoding/json"

// Request represents a JSON-RPC 2.0 request sent to the bridge process.
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      int         `json:"id"`
}

// Response represents a JSON-RPC 2.0 response or notification from the bridge.
// Responses have an ID matching the request. Notifications have Method set and ID==0.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e.Data != "" {
		return e.Message + ": " + e.Data
	}
	return e.Message
}

// PromptParams holds the parameters for the "prompt" method.
type PromptParams struct {
	Text      string `json:"text"`
	SessionID string `json:"session_id"`
}

// PromptResult holds the result of a completed prompt.
type PromptResult struct {
	Text      string `json:"text"`
	SessionID string `json:"session_id"`
}

// InitResult holds the result of the "initialize" method.
type InitResult struct {
	Version string `json:"version"`
}

// Event is a parsed bridge event delivered through the streaming channel.
type Event struct {
	Type         string
	MessageDelta *MessageDeltaParams
	ToolCall     *ToolCallParams
	Thought      *ThoughtParams
	Final        *PromptResult
	Error        error
}

// MessageDeltaParams holds a streaming text chunk from the agent.
type MessageDeltaParams struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// ToolCallParams holds tool invocation details.
type ToolCallParams struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

// ThoughtParams holds agent reasoning text.
type ThoughtParams struct {
	Text string `json:"text"`
}

// Notification method names used by the bridge.
const (
	MethodMessageDelta = "message_delta"
	MethodToolCall     = "tool_call"
	MethodThought      = "thought"
)

// NewRequest creates a JSON-RPC 2.0 request.
func NewRequest(id int, method string, params interface{}) *Request {
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}
}
