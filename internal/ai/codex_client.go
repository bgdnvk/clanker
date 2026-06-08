package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

const defaultCodexInstructions = "You are Clanker, an infrastructure and software engineering assistant. Follow the user's prompt exactly. When the prompt requests JSON, return only valid JSON with no markdown."

// CodexRequest is the Responses API request format.
type CodexRequest struct {
	Model        string    `json:"model"`
	Instructions string    `json:"instructions,omitempty"`
	Input        []Message `json:"input"`
	Store        bool      `json:"store"`
	Stream       bool      `json:"stream"`
}

func codexInstructions(systemPrompt string) string {
	if trimmed := strings.TrimSpace(systemPrompt); trimmed != "" {
		return sanitizeASCII(trimmed)
	}
	return defaultCodexInstructions
}

// AskCodex sends a streaming request to the Codex Responses API and aggregates
// the text response. The ChatGPT Codex endpoint requires stream=true even for
// callers that want a synchronous string result.
func (c *Client) AskCodex(ctx context.Context, prompt string) (string, error) {
	oauthToken, err := GetValidOAuthToken()
	if err != nil {
		return "", fmt.Errorf("failed to get oauth token: %w", err)
	}

	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile: %w", err)
	}
	model := profileLLMCall.Model
	if model == "" {
		model = "gpt-5.4"
	}
	emitProgressTrace("provider", fmt.Sprintf("Calling Codex Responses API with model %s.", model))

	return askCodexWithToken(ctx, oauthToken, model, prompt)
}

func askCodexWithToken(ctx context.Context, oauthToken, model, prompt string) (string, error) {
	return askCodexStreaming(ctx, oauthToken, model, codexInstructions(""), []Message{{Role: "user", Content: sanitizeASCII(prompt)}})
}

// AskCodexStream sends a streaming request to the Codex Responses API
// and returns text deltas on a channel.
func AskCodexStream(ctx context.Context, oauthToken, model, prompt string) (<-chan string, <-chan error) {
	textCh := make(chan string, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(textCh)
		defer close(errCh)

		resp, err := doCodexStreamingRequest(ctx, oauthToken, CodexRequest{
			Model:        model,
			Instructions: codexInstructions(""),
			Input:        []Message{{Role: "user", Content: sanitizeASCII(prompt)}},
		})
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			errCh <- fmt.Errorf("codex returned status %d: %s", resp.StatusCode, string(respBody))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}

			var event codexStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "response.output_text.delta":
				if event.Delta != "" {
					select {
					case textCh <- event.Delta:
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}
				}
			case "response.completed", "response.done":
				return
			}
		}

		if err := scanner.Err(); err != nil {
			errCh <- fmt.Errorf("reading codex stream: %w", err)
		}
	}()

	return textCh, errCh
}

type codexStreamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Response *struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
	} `json:"response,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func doCodexStreamingRequest(ctx context.Context, oauthToken string, codexReq CodexRequest) (*http.Response, error) {
	codexReq.Store = false
	codexReq.Stream = true
	if codexReq.Input == nil {
		codexReq.Input = []Message{}
	}

	body, err := json.Marshal(codexReq)
	if err != nil {
		return nil, fmt.Errorf("marshal codex request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, codexResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create codex request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+oauthToken)
	httpReq.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("codex request failed: %w", err)
	}
	return resp, nil
}

func askCodexStreaming(ctx context.Context, oauthToken, model, instructions string, input []Message) (string, error) {
	resp, err := doCodexStreamingRequest(ctx, oauthToken, CodexRequest{
		Model:        model,
		Instructions: instructions,
		Input:        input,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("codex returned status %d: %s", resp.StatusCode, string(respBody))
	}

	text, err := readCodexStreamText(resp.Body)
	if err != nil {
		return "", err
	}
	return text, nil
}

func readCodexStreamText(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var sb strings.Builder
	var completedText string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event codexStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Error != nil {
			return "", fmt.Errorf("codex api error (%s): %s", event.Error.Code, event.Error.Message)
		}

		switch event.Type {
		case "response.output_text.delta":
			sb.WriteString(event.Delta)
		case "response.completed", "response.done":
			if event.Response != nil {
				if event.Response.Error != nil {
					return "", fmt.Errorf("codex api error (%s): %s", event.Response.Error.Code, event.Response.Error.Message)
				}
				completedText = codexResponseText(event.Response.Output)
			}
		case "error":
			if event.Error != nil {
				return "", fmt.Errorf("codex api error (%s): %s", event.Error.Code, event.Error.Message)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading codex stream: %w", err)
	}
	if sb.Len() > 0 {
		return sb.String(), nil
	}
	return completedText, nil
}

func codexResponseText(output []struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}) string {
	var sb strings.Builder
	for _, item := range output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" || part.Type == "text" {
				sb.WriteString(part.Text)
			}
		}
	}
	return sb.String()
}

// askCodexWithHistory sends a multi-turn conversation to the Codex Responses API
// using structured message roles (system prompt becomes instructions, messages
// become the input array).
func (c *Client) askCodexWithHistory(ctx context.Context, conv *ConversationContext) (string, error) {
	oauthToken, err := GetValidOAuthToken()
	if err != nil {
		return "", fmt.Errorf("failed to get oauth token: %w", err)
	}

	profileLLMCall, err := c.getAIProfile(c.aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile: %w", err)
	}
	model := profileLLMCall.Model
	if model == "" {
		model = "gpt-5.4"
	}
	emitProgressTrace("provider", fmt.Sprintf("Calling Codex Responses API with model %s.", model))

	input := make([]Message, len(conv.Messages))
	for i, m := range conv.Messages {
		input[i] = Message{Role: m.Role, Content: sanitizeASCII(m.Content)}
	}
	return askCodexStreaming(ctx, oauthToken, model, codexInstructions(conv.SystemPrompt), input)
}

// IsOpenAIOAuthActive returns true if the user has a saved OAuth token
// or an OPENAI_OAUTH_TOKEN environment variable.
func IsOpenAIOAuthActive() bool {
	if envToken := strings.TrimSpace(os.Getenv("OPENAI_OAUTH_TOKEN")); envToken != "" {
		return true
	}
	tokens, err := LoadOAuthTokens()
	if err != nil {
		return false
	}
	return tokens.AccessToken != ""
}
