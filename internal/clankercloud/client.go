package clankercloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var candidatePorts = []int{8080, 8081, 8082, 8083, 8084}

const defaultAppBundleID = "com.clanker.cloud"
const defaultAppName = "Clanker Cloud"
const defaultHTTPTimeout = 12 * time.Minute

type Client struct {
	httpClient *http.Client
	baseURL    string
}

type AppPortStatus struct {
	Port        int    `json:"port,omitempty"`
	BaseURL     string `json:"baseUrl"`
	MCPEndpoint string `json:"mcpEndpoint"`
	Healthy     bool   `json:"healthy"`
	Error       string `json:"error,omitempty"`
}

type AppStatus struct {
	AppName            string          `json:"appName"`
	BundleID           string          `json:"bundleId"`
	Running            bool            `json:"running"`
	Port               int             `json:"port,omitempty"`
	APIBaseURL         string          `json:"apiBaseUrl,omitempty"`
	MCPEndpoint        string          `json:"mcpEndpoint,omitempty"`
	Transport          string          `json:"transport"`
	PortRange          []int           `json:"portRange"`
	Ports              []AppPortStatus `json:"ports"`
	ExplicitAPIBaseURL string          `json:"explicitApiBaseUrl,omitempty"`
	CheckedAt          time.Time       `json:"checkedAt"`
}

type LaunchOptions struct {
	AppPath        string
	BundleID       string
	Wait           bool
	TimeoutSeconds int
}

type APICallResult struct {
	BaseURL      string            `json:"baseUrl"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Status       int               `json:"status"`
	ContentType  string            `json:"contentType,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         any               `json:"body,omitempty"`
	Events       []SSEEvent        `json:"events,omitempty"`
	FinalMessage string            `json:"finalMessage,omitempty"`
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
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
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

func MCPEndpointForBase(base string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	trimmed = strings.TrimSuffix(trimmed, "/api")
	return trimmed + "/mcp"
}

func (c *Client) Status(ctx context.Context) AppStatus {
	status := AppStatus{
		AppName:   defaultAppName,
		BundleID:  defaultAppBundleID,
		Transport: "streamable-http",
		PortRange: append([]int(nil), candidatePorts...),
		Ports:     []AppPortStatus{},
		CheckedAt: time.Now().UTC(),
	}

	if explicit := strings.TrimSpace(os.Getenv("CLANKER_CLOUD_API_BASE_URL")); explicit != "" {
		base := strings.TrimRight(explicit, "/")
		portStatus := AppPortStatus{BaseURL: base, MCPEndpoint: MCPEndpointForBase(base)}
		if err := c.checkHealth(ctx, base); err == nil {
			portStatus.Healthy = true
			status.Running = true
			status.APIBaseURL = base
			status.MCPEndpoint = MCPEndpointForBase(base)
			c.baseURL = base
		} else {
			portStatus.Error = err.Error()
		}
		status.ExplicitAPIBaseURL = base
		status.Ports = append(status.Ports, portStatus)
		return status
	}

	for _, port := range candidatePorts {
		base := fmt.Sprintf("http://127.0.0.1:%d/api", port)
		portStatus := AppPortStatus{
			Port:        port,
			BaseURL:     base,
			MCPEndpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		}
		if err := c.checkHealth(ctx, base); err == nil {
			portStatus.Healthy = true
			if !status.Running {
				status.Running = true
				status.Port = port
				status.APIBaseURL = base
				status.MCPEndpoint = portStatus.MCPEndpoint
				c.baseURL = base
			}
		} else {
			portStatus.Error = err.Error()
		}
		status.Ports = append(status.Ports, portStatus)
	}

	return status
}

func (c *Client) WaitForBackend(ctx context.Context, timeout time.Duration) (AppStatus, error) {
	deadline := time.Now().Add(timeout)
	for {
		status := c.Status(ctx)
		if status.Running {
			return status, nil
		}
		if time.Now().After(deadline) {
			return status, fmt.Errorf("timed out waiting for Clanker Cloud app backend after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

type launchCandidate struct {
	Label   string
	Command string
	Args    []string
}

func appLaunchCandidates(opts LaunchOptions) []launchCandidate {
	appPath := strings.TrimSpace(opts.AppPath)
	bundleID := firstNonEmpty(opts.BundleID, defaultAppBundleID)
	candidates := make([]launchCandidate, 0, 4)

	switch runtime.GOOS {
	case "darwin":
		if appPath != "" {
			candidates = append(candidates, launchCandidate{Label: "macos-app-path", Command: "open", Args: []string{appPath}})
		}
		if bundleID != "" {
			candidates = append(candidates, launchCandidate{Label: "macos-bundle-id", Command: "open", Args: []string{"-b", bundleID}})
		}
		candidates = append(candidates, launchCandidate{Label: "macos-app-name", Command: "open", Args: []string{"-a", defaultAppName}})
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, launchCandidate{Label: "macos-user-applications", Command: "open", Args: []string{filepath.Join(home, "Applications", defaultAppName+".app")}})
		}
		candidates = append(candidates, launchCandidate{Label: "macos-applications", Command: "open", Args: []string{filepath.Join("/Applications", defaultAppName+".app")}})
	case "windows":
		if appPath != "" {
			candidates = append(candidates, launchCandidate{Label: "windows-app-path", Command: "cmd", Args: []string{"/C", "start", "", appPath}})
		}
		candidates = append(candidates, launchCandidate{Label: "windows-app-name", Command: "cmd", Args: []string{"/C", "start", "", defaultAppName}})
	default:
		if appPath != "" {
			candidates = append(candidates, launchCandidate{Label: "linux-app-path", Command: appPath})
		}
		if _, err := exec.LookPath("gtk-launch"); err == nil {
			candidates = append(candidates, launchCandidate{Label: "linux-desktop-entry", Command: "gtk-launch", Args: []string{defaultAppBundleID}})
		}
		if _, err := exec.LookPath("clanker-cloud"); err == nil {
			candidates = append(candidates, launchCandidate{Label: "linux-path-binary", Command: "clanker-cloud"})
		}
	}

	return candidates
}

func (c *Client) LaunchApp(ctx context.Context, opts LaunchOptions) map[string]any {
	timeoutSeconds := opts.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	if timeoutSeconds > 900 {
		timeoutSeconds = 900
	}

	before := c.Status(ctx)
	result := map[string]any{
		"appName":        defaultAppName,
		"bundleId":       firstNonEmpty(opts.BundleID, defaultAppBundleID),
		"alreadyRunning": before.Running,
		"wait":           opts.Wait,
		"timeoutSeconds": timeoutSeconds,
		"before":         before,
	}
	if before.Running {
		result["launched"] = false
		result["status"] = before
		return result
	}

	candidates := appLaunchCandidates(opts)
	attempts := make([]map[string]any, 0, len(candidates))
	launched := false
	for _, candidate := range candidates {
		attempt := map[string]any{
			"label":   candidate.Label,
			"command": append([]string{candidate.Command}, candidate.Args...),
		}
		runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		output, err := exec.CommandContext(runCtx, candidate.Command, candidate.Args...).CombinedOutput()
		cancel()
		if trimmedOutput := strings.TrimSpace(string(output)); trimmedOutput != "" {
			attempt["output"] = trimmedOutput
		}
		if err != nil {
			attempt["ok"] = false
			attempt["error"] = err.Error()
			attempts = append(attempts, attempt)
			continue
		}
		attempt["ok"] = true
		attempts = append(attempts, attempt)
		launched = true
		break
	}

	result["attempts"] = attempts
	result["launched"] = launched
	if !launched {
		result["status"] = c.Status(ctx)
		if len(candidates) == 0 {
			result["error"] = "no launch command candidates available; pass appPath"
		} else {
			result["error"] = "failed to launch Clanker Cloud app with available launch commands"
		}
		return result
	}

	if !opts.Wait {
		result["status"] = c.Status(ctx)
		return result
	}

	status, err := c.WaitForBackend(ctx, time.Duration(timeoutSeconds)*time.Second)
	result["status"] = status
	if err != nil {
		result["error"] = err.Error()
	}
	return result
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

func (c *Client) CallAPI(ctx context.Context, method string, path string, query map[string]string, body []byte, profile string) (*APICallResult, error) {
	baseURL, err := c.ResolveBaseURL(ctx)
	if err != nil {
		return nil, err
	}

	trimmedMethod := strings.ToUpper(strings.TrimSpace(method))
	if trimmedMethod == "" {
		trimmedMethod = http.MethodGet
	}

	trimmedPath := strings.TrimSpace(path)
	if !strings.HasPrefix(trimmedPath, "/api/") {
		return nil, fmt.Errorf("path must start with /api/")
	}

	fullURL := strings.TrimRight(baseURL, "/") + strings.TrimPrefix(trimmedPath, "/api")
	parsedURL, err := url.Parse(fullURL)
	if err != nil {
		return nil, fmt.Errorf("parse request url: %w", err)
	}
	params := parsedURL.Query()
	for key, value := range query {
		params.Set(key, value)
	}
	parsedURL.RawQuery = params.Encode()

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, trimmedMethod, parsedURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if trimmedProfile := strings.TrimSpace(profile); trimmedProfile != "" {
		req.Header.Set("X-AWS-Profile", trimmedProfile)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	result := &APICallResult{
		BaseURL:     baseURL,
		Method:      trimmedMethod,
		Path:        trimmedPath,
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
		result.Body = map[string]any{"events": events, "final": finalMessage}
		return result, nil
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	result.Body = decodeBody(rawBody)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func decodeBody(raw []byte) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var decoded any
		if err := json.Unmarshal(trimmed, &decoded); err == nil {
			return decoded
		}
	}
	return string(trimmed)
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
