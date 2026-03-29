package clankercloud

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

var candidatePorts = []int{8080, 8081, 8082, 8083, 8084}

type Client struct {
	httpClient *http.Client
	baseURL    string
}

type AskResult struct {
	BaseURL      string            `json:"baseUrl"`
	Status       int               `json:"status"`
	ContentType  string            `json:"contentType,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	FinalMessage string            `json:"finalMessage,omitempty"`
	RawBody      string            `json:"rawBody,omitempty"`
	Events       []SSEEvent        `json:"events,omitempty"`
}

type SSEEvent struct {
	Message string `json:"message,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Raw     string `json:"raw,omitempty"`
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) ResolveBaseURL(ctx context.Context) (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("CLANKER_CLOUD_API_BASE_URL")); explicit != "" {
		return strings.TrimRight(explicit, "/"), nil
	}
	if trimmed := strings.TrimSpace(c.baseURL); trimmed != "" {
		if err := c.checkHealth(ctx, trimmed); err == nil {
			return trimmed, nil
		}
	}
	for _, port := range candidatePorts {
		candidate := fmt.Sprintf("http://127.0.0.1:%d/api", port)
		if err := c.checkHealth(ctx, candidate); err == nil {
			c.baseURL = candidate
			return candidate, nil
		}
	}
	return "", fmt.Errorf("clanker-cloud app backend is not running")
}

func (c *Client) AskAgent(ctx context.Context, question string, profile string) (*AskResult, error) {
	baseURL, err := c.ResolveBaseURL(ctx)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"question":     strings.TrimSpace(question),
		"userQuestion": strings.TrimSpace(question),
		"profile":      strings.TrimSpace(profile),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode ask payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/agent/ask", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create ask request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if trimmed := strings.TrimSpace(profile); trimmed != "" {
		req.Header.Set("X-AWS-Profile", trimmed)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform ask request: %w", err)
	}
	defer resp.Body.Close()

	result := &AskResult{
		BaseURL:     baseURL,
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     map[string]string{},
	}
	for key, values := range resp.Header {
		if len(values) > 0 {
			result.Headers[key] = strings.Join(values, ", ")
		}
	}

	if strings.Contains(strings.ToLower(result.ContentType), "text/event-stream") {
		events, finalMessage, err := parseSSE(resp.Body)
		if err != nil {
			return nil, err
		}
		result.Events = events
		result.FinalMessage = finalMessage
		return result, nil
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read ask response: %w", err)
	}
	result.RawBody = string(bytes.TrimSpace(rawBody))
	result.FinalMessage = result.RawBody
	return result, nil
}

func (c *Client) checkHealth(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}

func parseSSE(r io.Reader) ([]SSEEvent, string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var dataLines []string
	finalMessage := ""
	events := make([]SSEEvent, 0)

	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = nil
		event := SSEEvent{Raw: payload}
		var parsed struct {
			Message string `json:"message"`
			Done    bool   `json:"done"`
		}
		if err := json.Unmarshal([]byte(payload), &parsed); err == nil {
			event.Message = parsed.Message
			event.Done = parsed.Done
			if strings.TrimSpace(parsed.Message) != "" {
				finalMessage = parsed.Message
			}
		} else if strings.TrimSpace(payload) != "" {
			finalMessage = payload
		}
		events = append(events, event)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()

	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("parse sse: %w", err)
	}

	return events, finalMessage, nil
}
