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

// CodexRequest is the Responses API request format.
type CodexRequest struct {
	Model        string    `json:"model"`
	Instructions string    `json:"instructions,omitempty"`
	Input        []Message `json:"input"`
	Stream       bool      `json:"stream"`
}

// AskCodex sends a non-streaming request to the Codex Responses API
// and returns the full text response. This is used when the OpenAI auth
// method is OAuth rather than API key.
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

	return askCodexWithToken(ctx, oauthToken, model, prompt)
}

func askCodexWithToken(ctx context.Context, oauthToken, model, prompt string) (string, error) {
	codexReq := CodexRequest{
		Model:  model,
		Input:  []Message{{Role: "user", Content: sanitizeASCII(prompt)}},
		Stream: false,
	}

	body, err := json.Marshal(codexReq)
	if err != nil {
		return "", fmt.Errorf("marshal codex request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, codexResponsesURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create codex request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+oauthToken)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("codex request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read codex response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("codex returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
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
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode codex response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("codex api error (%s): %s", result.Error.Code, result.Error.Message)
	}

	var sb strings.Builder
	for _, item := range result.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" || part.Type == "text" {
				sb.WriteString(part.Text)
			}
		}
	}

	return sb.String(), nil
}

// AskCodexStream sends a streaming request to the Codex Responses API
// and returns text deltas on a channel.
func AskCodexStream(ctx context.Context, oauthToken, model, prompt string) (<-chan string, <-chan error) {
	textCh := make(chan string, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(textCh)
		defer close(errCh)

		codexReq := CodexRequest{
			Model:  model,
			Input:  []Message{{Role: "user", Content: prompt}},
			Stream: true,
		}

		body, err := json.Marshal(codexReq)
		if err != nil {
			errCh <- fmt.Errorf("marshal codex request: %w", err)
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, codexResponsesURL, bytes.NewReader(body))
		if err != nil {
			errCh <- fmt.Errorf("create codex request: %w", err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+oauthToken)
		httpReq.Header.Set("Accept", "text/event-stream")

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			errCh <- fmt.Errorf("codex request failed: %w", err)
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

			var event struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			}
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

// askCodexWithHistory sends a multi-turn conversation to the Codex Responses API.
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

	// Build messages: system prompt as instructions is handled by askCodexWithToken
	// but we merge them into a single prompt for simplicity.
	var sb strings.Builder
	if conv.SystemPrompt != "" {
		sb.WriteString(conv.SystemPrompt)
		sb.WriteString("\n\n")
	}
	for _, m := range conv.Messages {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}

	return askCodexWithToken(ctx, oauthToken, model, sb.String())
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
