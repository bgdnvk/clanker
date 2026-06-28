package clankerbox

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/claudecode"
	"github.com/bgdnvk/clanker/internal/hermes"
	"github.com/gorilla/websocket"
)

type RuntimeConfig struct {
	Name           string `json:"name"`
	Agent          string `json:"agent"`
	Region         string `json:"region"`
	RequireAuth    bool   `json:"requireAuth"`
	EnableTerminal bool   `json:"enableTerminal"`
	APIToken       string `json:"-"`
	Version        string `json:"version,omitempty"`
}

type MessageRequest struct {
	SessionID string         `json:"sessionId,omitempty"`
	Message   string         `json:"message"`
	Context   map[string]any `json:"context,omitempty"`
}

type MessageResponse struct {
	OK        bool              `json:"ok"`
	SessionID string            `json:"sessionId,omitempty"`
	Agent     string            `json:"agent"`
	Message   string            `json:"message,omitempty"`
	Error     string            `json:"error,omitempty"`
	Terminal  *TerminalResponse `json:"terminal,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

type TerminalRequest struct {
	SessionID      string `json:"sessionId,omitempty"`
	Command        string `json:"command"`
	WorkingDir     string `json:"workingDir,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type TerminalResponse struct {
	OK        bool      `json:"ok"`
	SessionID string    `json:"sessionId,omitempty"`
	Command   string    `json:"command,omitempty"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	ExitCode  int       `json:"exitCode"`
	CreatedAt time.Time `json:"createdAt"`
}

type AgentRunner interface {
	RunAgentMessage(ctx context.Context, cfg RuntimeConfig, req MessageRequest) (string, error)
}

type Server struct {
	cfg      RuntimeConfig
	runner   AgentRunner
	vision   *VisionService
	upgrader websocket.Upgrader
}

func RuntimeConfigFromEnv(version string) RuntimeConfig {
	return RuntimeConfig{
		Name:           envOr("CLANKER_BOX_NAME", "clanker-box"),
		Agent:          envOr("CLANKER_BOX_AGENT", "clanker-cli"),
		Region:         envOr("CLANKER_BOX_REGION", "us-central1"),
		RequireAuth:    parseBoolEnv("CLANKER_BOX_REQUIRE_AUTH", true),
		EnableTerminal: parseBoolEnv("CLANKER_BOX_ENABLE_TERMINAL", true),
		APIToken:       strings.TrimSpace(os.Getenv("CLANKER_BOX_API_TOKEN")),
		Version:        version,
	}
}

func NewServer(cfg RuntimeConfig, runner AgentRunner) *Server {
	if runner == nil {
		runner = DefaultRunner{}
	}
	return &Server{
		cfg:    cfg,
		runner: runner,
		vision: NewVisionService(),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/box/info", s.withAuth(s.handleInfo))
	mux.HandleFunc("/v1/box/messages", s.withAuth(s.handleMessage))
	mux.HandleFunc("/v1/box/ws", s.withAuth(s.handleWebSocket))
	mux.HandleFunc("/v1/box/terminal", s.withAuth(s.handleTerminal))
	mux.HandleFunc("/v1/box/vision", s.withAuth(s.handleVision))
	return mux
}

func (s *Server) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	agent, _ := AgentByID(s.cfg.Agent)
	region, _ := RegionByID(s.cfg.Region)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"name":        s.cfg.Name,
		"agent":       agent,
		"region":      region,
		"requireAuth": s.cfg.RequireAuth,
		"terminal":    s.cfg.EnableTerminal,
		"version":     s.cfg.Version,
		"endpoints": []EndpointSpec{
			{Kind: "health", Path: "/healthz", Description: "Liveness check."},
			{Kind: "info", Path: "/v1/box/info", Description: "Runtime metadata."},
			{Kind: "message", Path: "/v1/box/messages", Description: "JSON message endpoint."},
			{Kind: "websocket", Path: "/v1/box/ws", Description: "Bidirectional message endpoint."},
			{Kind: "terminal", Path: "/v1/box/terminal", Description: "Authenticated WebSocket shell endpoint for box access."},
			{Kind: "vision", Path: "/v1/box/vision", Description: "Authenticated browser and office-control endpoint with visible action observations and stop control."},
		},
	})
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req MessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, MessageResponse{OK: false, Agent: s.cfg.Agent, Error: "invalid json", CreatedAt: time.Now().UTC()})
		return
	}
	reply, terminal, err := s.run(r.Context(), req)
	resp := MessageResponse{OK: err == nil, SessionID: req.SessionID, Agent: s.cfg.Agent, Message: reply, Terminal: terminal, CreatedAt: time.Now().UTC()}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		var req MessageRequest
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		reply, terminal, runErr := s.run(ctx, req)
		cancel()
		resp := MessageResponse{OK: runErr == nil, SessionID: req.SessionID, Agent: s.cfg.Agent, Message: reply, Terminal: terminal, CreatedAt: time.Now().UTC()}
		if runErr != nil {
			resp.Error = runErr.Error()
		}
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.EnableTerminal {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "terminal access is disabled"})
		return
	}
	if r.Method == http.MethodPost {
		var req TerminalRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, TerminalResponse{OK: false, Error: "invalid json", ExitCode: 1, CreatedAt: time.Now().UTC()})
			return
		}
		resp := s.runTerminal(r.Context(), req)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		var req TerminalRequest
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		resp := s.runTerminal(r.Context(), req)
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}
}

func (s *Server) handleVision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req VisionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, VisionResponse{OK: false, Status: "failed", Error: "invalid json", Policy: DefaultVisionPolicy()})
		return
	}
	if s.vision == nil {
		s.vision = NewVisionService()
	}
	resp := s.vision.Handle(r.Context(), req)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) runTerminal(parent context.Context, req TerminalRequest) TerminalResponse {
	req.Command = strings.TrimSpace(req.Command)
	resp := TerminalResponse{
		OK:        false,
		SessionID: req.SessionID,
		Command:   req.Command,
		ExitCode:  1,
		CreatedAt: time.Now().UTC(),
	}
	if req.Command == "" {
		resp.Error = "command is required"
		return resp
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 || timeout > 120 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Command)
	cmd.Dir = terminalWorkingDir(req.WorkingDir)
	cmd.Env = append(os.Environ(),
		"CLANKER_BOX_NAME="+s.cfg.Name,
		"CLANKER_BOX_AGENT="+s.cfg.Agent,
		"CLANKER_BOX_REGION="+s.cfg.Region,
	)
	output, err := cmd.CombinedOutput()
	resp.Output = trimTerminalOutput(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		resp.Error = "command timed out"
		return resp
	}
	if err != nil {
		resp.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		}
		return resp
	}
	resp.OK = true
	resp.ExitCode = 0
	return resp
}

func (s *Server) run(ctx context.Context, req MessageRequest) (string, *TerminalResponse, error) {
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		return "", nil, fmt.Errorf("message is required")
	}
	if _, ok := AgentByID(s.cfg.Agent); !ok {
		return "", nil, fmt.Errorf("unsupported agent %q", s.cfg.Agent)
	}
	if terminalReq, ok := s.terminalRequestFromMessage(req); ok {
		resp := s.runTerminal(ctx, terminalReq)
		message := formatTerminalMessage(resp)
		if resp.OK {
			return message, &resp, nil
		}
		errMessage := resp.Error
		if errMessage == "" {
			errMessage = "terminal command failed"
		}
		return message, &resp, fmt.Errorf("%s", errMessage)
	}
	reply, err := s.runner.RunAgentMessage(ctx, s.cfg, req)
	return reply, nil, err
}

func (s *Server) terminalRequestFromMessage(req MessageRequest) (TerminalRequest, bool) {
	if !s.cfg.EnableTerminal {
		return TerminalRequest{}, false
	}
	command, ok := terminalCommandFromMessage(req.Message, s.cfg.Agent)
	if !ok {
		return TerminalRequest{}, false
	}
	return TerminalRequest{
		SessionID:      req.SessionID,
		Command:        command,
		TimeoutSeconds: 120,
	}, true
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.RequireAuth {
			next(w, r)
			return
		}
		expected := strings.TrimSpace(s.cfg.APIToken)
		if expected == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "box auth token is not configured"})
			return
		}
		got := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if got == "" {
			got = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		if got == "" {
			got = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

type DefaultRunner struct{}

func (DefaultRunner) RunAgentMessage(ctx context.Context, cfg RuntimeConfig, req MessageRequest) (string, error) {
	if err := ensureAgentInstalled(ctx, cfg.Agent); err != nil {
		return "", err
	}
	switch normalizeID(cfg.Agent) {
	case "empty":
		return "Empty sandbox is running. Use the terminal endpoint to run shell commands.", nil
	case "hermes":
		return runHermes(ctx, req.Message)
	case "claude-code":
		return runClaudeCode(ctx, req.Message)
	case "clanker-cli":
		return runCommand(ctx, "CLANKER_BOX_CLANKER_COMMAND", []string{executable(), "ask", req.Message}, req.Message)
	case "clanker-vision":
		return RunClankerVisionMessage(ctx, req)
	case "codex":
		return runCommand(ctx, "CLANKER_BOX_CODEX_COMMAND", []string{"codex", "exec", "--skip-git-repo-check", req.Message}, req.Message)
	case "openclaw":
		if url := strings.TrimSpace(os.Getenv("CLANKER_BOX_OPENCLAW_URL")); url != "" {
			return "OpenClaw gateway is attached at " + url, nil
		}
		return runCommand(ctx, "CLANKER_BOX_OPENCLAW_COMMAND", []string{"openclaw", "agent", req.Message}, req.Message)
	default:
		return "", fmt.Errorf("unsupported agent %q", cfg.Agent)
	}
}

func terminalCommandFromMessage(message string, agent string) (string, bool) {
	text := strings.TrimSpace(message)
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(text, "$ "):
		return strings.TrimSpace(strings.TrimPrefix(text, "$ ")), true
	case strings.HasPrefix(lower, "run "):
		return strings.TrimSpace(text[4:]), true
	case strings.HasPrefix(lower, "exec "):
		return strings.TrimSpace(text[5:]), true
	case strings.HasPrefix(lower, "execute "):
		return strings.TrimSpace(text[8:]), true
	case strings.HasPrefix(lower, "shell "):
		return strings.TrimSpace(text[6:]), true
	case strings.HasPrefix(lower, "terminal "):
		return strings.TrimSpace(text[9:]), true
	case lower == "doctor" || strings.Contains(lower, "cloud doctor"):
		return "clanker cloud doctor", true
	case strings.HasPrefix(lower, "install "):
		target := strings.TrimSpace(text[len("install "):])
		return installCommandFromChat(target, agent), true
	default:
		return "", false
	}
}

func installCommandFromChat(target string, agent string) string {
	normalized := normalizeID(target)
	normalized = strings.TrimPrefix(normalized, "the-")
	normalized = strings.TrimSuffix(normalized, "-agent")
	switch normalized {
	case "codex", "openai-codex":
		return "clanker box install codex"
	case "claude", "claude-code", "anthropic-claude-code":
		return "clanker box install claude-code"
	case "hermes", "nous-hermes":
		return "clanker box install hermes"
	case "openclaw", "open-claw":
		return "clanker box install openclaw"
	case "clanker", "clanker-cli", "cli":
		return "clanker box install clanker-cli && clanker --version"
	case "clanker-vision", "vision", "computer-use", "office-agent", "browser-agent":
		return "clanker box install clanker-vision"
	case "docker", "docker-cli":
		return dockerCLIInstallCommand()
	case "":
		if normalizeID(agent) == "empty" {
			return "pwd && ls -la"
		}
		return "clanker box install " + normalizeID(agent)
	default:
		return fmt.Sprintf("if command -v %s >/dev/null 2>&1; then %s --version || true; else printf 'No built-in installer for %s. Use the terminal to run the official installer.\\n'; exit 1; fi", shellQuoteWord(normalized), shellQuoteWord(normalized), shellQuoteWord(target))
	}
}

func dockerCLIInstallCommand() string {
	return `set -e; export PATH="$HOME/.local/bin:$PATH"; arch="$(uname -m)"; case "$arch" in x86_64|amd64) docker_arch=x86_64 ;; aarch64|arm64) docker_arch=aarch64 ;; *) echo "unsupported docker static binary arch: $arch"; exit 1 ;; esac; mkdir -p "$HOME/.local/bin" "$HOME/.cache/clanker-box/docker"; cd "$HOME/.cache/clanker-box/docker"; curl -fsSL "https://download.docker.com/linux/static/stable/${docker_arch}/docker-27.5.1.tgz" -o docker.tgz; tar -xzf docker.tgz; cp docker/docker "$HOME/.local/bin/docker"; chmod +x "$HOME/.local/bin/docker"; "$HOME/.local/bin/docker" --version; printf '\nDocker CLI installed in $HOME/.local/bin. Cloud Run does not provide a Docker daemon, so docker build/run needs a remote daemon or Cloud Build.\n'`
}

func formatTerminalMessage(resp TerminalResponse) string {
	var b strings.Builder
	if resp.Command != "" {
		b.WriteString("Executed in Clanker Box terminal:\n$ ")
		b.WriteString(resp.Command)
		b.WriteString("\n")
	}
	if strings.TrimSpace(resp.Output) != "" {
		b.WriteString(resp.Output)
		b.WriteString("\n")
	}
	if resp.Error != "" {
		b.WriteString("exit ")
		b.WriteString(fmt.Sprint(resp.ExitCode))
		b.WriteString(": ")
		b.WriteString(resp.Error)
	}
	return strings.TrimSpace(b.String())
}

func shellQuoteWord(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	if regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`).MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func runHermes(ctx context.Context, message string) (string, error) {
	path, err := hermes.FindHermesPath()
	if err != nil {
		return "", err
	}
	runner := hermes.NewRunner(path, parseBoolEnv("CLANKER_BOX_DEBUG", false))
	if err := runner.Start(ctx); err != nil {
		return "", err
	}
	defer runner.Stop()
	return runner.PromptSync(ctx, message)
}

func runClaudeCode(ctx context.Context, message string) (string, error) {
	runner := claudecode.NewRunner(parseBoolEnv("CLANKER_BOX_DEBUG", false))
	return runner.AskSync(ctx, message)
}

func runCommand(ctx context.Context, envKey string, fallback []string, prompt string) (string, error) {
	if custom := strings.TrimSpace(os.Getenv(envKey)); custom != "" {
		cmd := exec.CommandContext(ctx, "sh", "-lc", custom)
		cmd.Env = append(os.Environ(), "CLANKER_BOX_PROMPT="+prompt)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%s failed: %w: %s", envKey, err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if len(fallback) == 0 {
		return "", fmt.Errorf("no command configured")
	}
	cmd := exec.CommandContext(ctx, fallback[0], fallback[1:]...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", fallback[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func executable() string {
	if path, err := os.Executable(); err == nil && strings.TrimSpace(path) != "" {
		return path
	}
	return "clanker"
}

func terminalWorkingDir(requested string) string {
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested
	}
	if workdir := strings.TrimSpace(os.Getenv("CLANKER_BOX_WORKDIR")); workdir != "" {
		return workdir
	}
	if workdir, err := os.Getwd(); err == nil && strings.TrimSpace(workdir) != "" {
		return workdir
	}
	return "/workspace"
}

func trimTerminalOutput(output string) string {
	const maxTerminalOutputBytes = 64 * 1024
	if len(output) <= maxTerminalOutputBytes {
		return output
	}
	return "[output truncated]\n" + output[len(output)-maxTerminalOutputBytes:]
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func envOr(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func parseBoolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
