package clankerbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	VisionAgentID   = "clanker-vision"
	visionUserAgent = "ClankerVision/0.1"
)

type VisionRequest struct {
	SessionID   string     `json:"sessionId,omitempty"`
	Action      string     `json:"action,omitempty"`
	Instruction string     `json:"instruction,omitempty"`
	URL         string     `json:"url,omitempty"`
	Selector    string     `json:"selector,omitempty"`
	X           int        `json:"x,omitempty"`
	Y           int        `json:"y,omitempty"`
	Text        string     `json:"text,omitempty"`
	Content     string     `json:"content,omitempty"`
	FileName    string     `json:"fileName,omitempty"`
	Rows        [][]string `json:"rows,omitempty"`
	To          string     `json:"to,omitempty"`
	Subject     string     `json:"subject,omitempty"`
	Command     string     `json:"command,omitempty"`
	Confirm     bool       `json:"confirm,omitempty"`
}

type VisionResponse struct {
	OK                   bool               `json:"ok"`
	SessionID            string             `json:"sessionId"`
	Status               string             `json:"status"`
	Summary              string             `json:"summary,omitempty"`
	Action               string             `json:"action,omitempty"`
	Events               []VisionEvent      `json:"events,omitempty"`
	Observation          *VisionObservation `json:"observation,omitempty"`
	Artifacts            []VisionArtifact   `json:"artifacts,omitempty"`
	RequiresConfirmation bool               `json:"requiresConfirmation,omitempty"`
	ConfirmationReason   string             `json:"confirmationReason,omitempty"`
	Policy               VisionPolicy       `json:"policy"`
	Error                string             `json:"error,omitempty"`
}

type VisionEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Action    string `json:"action,omitempty"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	X         int    `json:"x,omitempty"`
	Y         int    `json:"y,omitempty"`
	FilePath  string `json:"filePath,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type VisionObservation struct {
	URL                 string         `json:"url,omitempty"`
	Title               string         `json:"title,omitempty"`
	Width               int            `json:"width,omitempty"`
	Height              int            `json:"height,omitempty"`
	ScreenshotMediaType string         `json:"screenshotMediaType,omitempty"`
	ScreenshotBase64    string         `json:"screenshotBase64,omitempty"`
	Pointer             *VisionPointer `json:"pointer,omitempty"`
	ScreenshotPath      string         `json:"screenshotPath,omitempty"`
	ScreenshotRedacted  bool           `json:"screenshotRedacted,omitempty"`
}

type VisionPointer struct {
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Label string `json:"label,omitempty"`
}

type VisionArtifact struct {
	Kind     string `json:"kind"`
	FilePath string `json:"filePath"`
	Media    string `json:"media,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

type VisionPolicy struct {
	DataRetention        string   `json:"dataRetention"`
	DataScope            string   `json:"dataScope"`
	AllowedDomains       []string `json:"allowedDomains"`
	AllowedCapabilities  []string `json:"allowedCapabilities"`
	BlockedCapabilities  []string `json:"blockedCapabilities"`
	ConfirmationRequired []string `json:"confirmationRequired"`
	KillSwitch           string   `json:"killSwitch"`
}

type VisionService struct {
	mu       sync.Mutex
	sessions map[string]*visionSession
}

type visionSession struct {
	ID        string
	Status    string
	Stopped   bool
	Events    []VisionEvent
	CreatedAt time.Time
	UpdatedAt time.Time
}

type browserBridgeResult struct {
	URL                 string         `json:"url"`
	Title               string         `json:"title"`
	Width               int            `json:"width"`
	Height              int            `json:"height"`
	ScreenshotMediaType string         `json:"screenshotMediaType"`
	ScreenshotBase64    string         `json:"screenshotBase64"`
	ScreenshotPath      string         `json:"screenshotPath"`
	Pointer             *VisionPointer `json:"pointer,omitempty"`
	Error               string         `json:"error,omitempty"`
}

var (
	defaultVisionService = NewVisionService()
	dangerousVisionText  = regexp.MustCompile(`(?i)\b(password|passcode|2fa|mfa|otp|recovery code|secret key|private key|api key|credit card|wire transfer|buy now|checkout|delete all|format disk|rm\s+-rf|sudo\s+rm|shutdown|reboot)\b`)
	sendLikeVisionText   = regexp.MustCompile(`(?i)\b(send|submit|publish|post|pay|purchase|checkout|transfer|invite|delete|remove|unsubscribe)\b`)
)

func NewVisionService() *VisionService {
	return &VisionService{sessions: map[string]*visionSession{}}
}

func (s *VisionService) Handle(ctx context.Context, req VisionRequest) VisionResponse {
	session := s.session(req.SessionID)
	action := normalizeVisionAction(req.Action)
	if action == "" {
		action = inferVisionAction(req)
	}
	resp := VisionResponse{
		OK:        true,
		SessionID: session.ID,
		Status:    session.Status,
		Action:    action,
		Policy:    DefaultVisionPolicy(),
	}

	if action == "stop" {
		s.markStopped(session.ID)
		event := s.appendEvent(session.ID, VisionEvent{Type: "control", Action: action, Status: "stopped", Summary: "Vision agent stopped by user."})
		resp.Status = "stopped"
		resp.Summary = "Stopped. New actions require a fresh session or explicit status check."
		resp.Events = []VisionEvent{event}
		return resp
	}

	if action == "status" {
		resp.Summary = "Clanker Vision is ready for office and browser work inside this account-scoped box."
		resp.Events = s.events(session.ID)
		return resp
	}

	if session.Stopped {
		resp.OK = false
		resp.Status = "stopped"
		resp.Error = "vision session is stopped"
		resp.Summary = "Use a new session to resume automation."
		return resp
	}

	if blockReason := visionPolicyBlockReason(req, action); blockReason != "" {
		resp.OK = false
		resp.Status = "blocked"
		resp.Error = blockReason
		resp.Summary = blockReason
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "policy", Action: action, Status: "blocked", Summary: blockReason})}
		return resp
	}

	if reason := visionConfirmationReason(req, action); reason != "" && !req.Confirm {
		resp.RequiresConfirmation = true
		resp.ConfirmationReason = reason
		resp.Summary = reason
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "policy", Action: action, Status: "needs_confirmation", Summary: reason})}
		return resp
	}

	s.setStatus(session.ID, "running")
	defer s.setStatus(session.ID, "idle")

	switch action {
	case "browser.open", "browser.click", "browser.type", "browser.press", "browser.screenshot":
		observation, err := runVisionBrowserAction(ctx, session.ID, action, req)
		if err != nil {
			resp.OK = false
			resp.Status = "failed"
			resp.Error = err.Error()
			resp.Summary = err.Error()
			resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "browser", Action: action, Status: "failed", Summary: err.Error(), X: req.X, Y: req.Y})}
			return resp
		}
		resp.Observation = observation
		resp.Summary = browserVisionSummary(action, observation)
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "browser", Action: action, Status: "completed", Summary: resp.Summary, X: req.X, Y: req.Y, FilePath: observation.ScreenshotPath})}
	case "office.write_doc":
		artifact, err := writeVisionDocument(ctx, session.ID, req)
		if err != nil {
			resp.OK = false
			resp.Status = "failed"
			resp.Error = err.Error()
			resp.Summary = err.Error()
			resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "office", Action: action, Status: "failed", Summary: err.Error()})}
			return resp
		}
		resp.Artifacts = []VisionArtifact{artifact}
		resp.Summary = artifact.Summary
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "office", Action: action, Status: "completed", Summary: artifact.Summary, FilePath: artifact.FilePath})}
	case "office.write_sheet":
		artifact, err := writeVisionSheet(ctx, session.ID, req)
		if err != nil {
			resp.OK = false
			resp.Status = "failed"
			resp.Error = err.Error()
			resp.Summary = err.Error()
			resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "office", Action: action, Status: "failed", Summary: err.Error()})}
			return resp
		}
		resp.Artifacts = []VisionArtifact{artifact}
		resp.Summary = artifact.Summary
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "office", Action: action, Status: "completed", Summary: artifact.Summary, FilePath: artifact.FilePath})}
	case "email.draft":
		artifact, err := writeVisionEmailDraft(session.ID, req)
		if err != nil {
			resp.OK = false
			resp.Status = "failed"
			resp.Error = err.Error()
			resp.Summary = err.Error()
			resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "email", Action: action, Status: "failed", Summary: err.Error()})}
			return resp
		}
		resp.Artifacts = []VisionArtifact{artifact}
		resp.Summary = artifact.Summary
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "email", Action: action, Status: "drafted", Summary: artifact.Summary, FilePath: artifact.FilePath})}
	case "system.install":
		summary := "Install requests are staged for the user-controlled terminal. Run the shown command manually if you trust the package source."
		resp.Summary = summary
		resp.Events = []VisionEvent{s.appendEvent(session.ID, VisionEvent{Type: "system", Action: action, Status: "needs_terminal", Summary: summary})}
	default:
		resp.OK = false
		resp.Status = "unsupported"
		resp.Error = "unsupported vision action"
		resp.Summary = "Supported actions: browser.open, browser.click, browser.type, browser.press, browser.screenshot, office.write_doc, office.write_sheet, email.draft, system.install, status, stop."
	}
	return resp
}

func DefaultVisionPolicy() VisionPolicy {
	return VisionPolicy{
		DataRetention: "ephemeral-by-default; artifacts stay inside CLANKER_BOX_WORKDIR and optional per-box state prefix only",
		DataScope:     "current account-scoped Clanker Box runtime",
		AllowedDomains: []string{
			"browser navigation requested by the user",
			"office document and spreadsheet artifacts inside the box",
			"email drafts saved as files; no automatic sending",
		},
		AllowedCapabilities: []string{
			"browser.open",
			"browser.click",
			"browser.type",
			"browser.screenshot",
			"office.write_doc",
			"office.write_sheet",
			"email.draft",
			"user-confirmed install request staging",
		},
		BlockedCapabilities: []string{
			"payments and purchases",
			"credential or secret collection",
			"destructive filesystem or account changes without explicit user terminal action",
			"background persistence outside the running box",
			"email sending without a human approval flow",
		},
		ConfirmationRequired: []string{
			"send/submit/publish/pay/purchase/delete-like actions",
			"package installation commands",
			"actions involving authenticated accounts",
		},
		KillSwitch: "POST /v1/box/vision with action=stop",
	}
}

func RunClankerVisionMessage(ctx context.Context, req MessageRequest) (string, error) {
	visionReq := VisionRequest{
		SessionID:   req.SessionID,
		Instruction: req.Message,
	}
	if req.Context != nil {
		if raw, ok := req.Context["vision"]; ok {
			data, _ := json.Marshal(raw)
			_ = json.Unmarshal(data, &visionReq)
			if visionReq.Instruction == "" {
				visionReq.Instruction = req.Message
			}
			if visionReq.SessionID == "" {
				visionReq.SessionID = req.SessionID
			}
		}
	}
	resp := defaultVisionService.Handle(ctx, visionReq)
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return string(data), fmt.Errorf("%s", resp.Error)
	}
	return string(data), nil
}

func (s *VisionService) session(rawID string) *visionSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := sanitizeVisionID(rawID)
	if id == "" {
		id = "vision-" + randomHex(8)
	}
	if existing := s.sessions[id]; existing != nil {
		existing.UpdatedAt = time.Now().UTC()
		return existing
	}
	session := &visionSession{ID: id, Status: "idle", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	s.sessions[id] = session
	return session
}

func (s *VisionService) setStatus(sessionID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[sessionID]; session != nil {
		session.Status = status
		session.UpdatedAt = time.Now().UTC()
	}
}

func (s *VisionService) markStopped(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[sessionID]; session != nil {
		session.Stopped = true
		session.Status = "stopped"
		session.UpdatedAt = time.Now().UTC()
	}
}

func (s *VisionService) appendEvent(sessionID string, event VisionEvent) VisionEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ID == "" {
		event.ID = "evt-" + randomHex(6)
	}
	if event.CreatedAt == "" {
		event.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	session := s.sessions[sessionID]
	if session == nil {
		session = &visionSession{ID: sessionID, Status: "idle", CreatedAt: time.Now().UTC()}
		s.sessions[sessionID] = session
	}
	session.Events = append(session.Events, event)
	if len(session.Events) > 80 {
		session.Events = session.Events[len(session.Events)-80:]
	}
	session.UpdatedAt = time.Now().UTC()
	return event
}

func (s *VisionService) events(sessionID string) []VisionEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[sessionID]
	if session == nil {
		return nil
	}
	events := make([]VisionEvent, len(session.Events))
	copy(events, session.Events)
	return events
}

func normalizeVisionAction(raw string) string {
	action := strings.ToLower(strings.TrimSpace(raw))
	action = strings.ReplaceAll(action, "_", ".")
	switch action {
	case "", "auto":
		return ""
	case "open", "browser", "browser.open_url", "browser.navigate":
		return "browser.open"
	case "click", "browser.left_click":
		return "browser.click"
	case "type", "browser.input":
		return "browser.type"
	case "press", "key", "browser.key":
		return "browser.press"
	case "screenshot", "observe", "browser.observe":
		return "browser.screenshot"
	case "doc", "document", "write.doc", "office.document":
		return "office.write_doc"
	case "sheet", "spreadsheet", "excel", "write.sheet", "office.spreadsheet":
		return "office.write_sheet"
	case "email", "draft", "email.write":
		return "email.draft"
	case "install", "package.install", "system.package":
		return "system.install"
	default:
		return action
	}
}

func inferVisionAction(req VisionRequest) string {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{req.Instruction, req.URL, req.FileName, req.Subject}, " ")))
	switch {
	case req.URL != "":
		return "browser.open"
	case strings.Contains(text, "spreadsheet") || strings.Contains(text, "excel") || strings.Contains(text, "sheet") || strings.Contains(text, ".csv") || strings.Contains(text, ".xlsx"):
		return "office.write_sheet"
	case strings.Contains(text, "email") || strings.Contains(text, "gmail"):
		return "email.draft"
	case strings.Contains(text, "doc") || strings.Contains(text, "memo") || strings.Contains(text, "word"):
		return "office.write_doc"
	case strings.Contains(text, "install"):
		return "system.install"
	case strings.Contains(text, "click"):
		return "browser.click"
	case strings.Contains(text, "type"):
		return "browser.type"
	default:
		return "status"
	}
}

func visionPolicyBlockReason(req VisionRequest, action string) string {
	text := strings.Join([]string{req.Instruction, req.Text, req.Content, req.Command, req.URL, req.Subject}, " ")
	if dangerousVisionText.MatchString(text) {
		return "Blocked by Clanker Vision safety policy: credentials, payments, destructive system actions, and secret collection are outside the office-work prototype."
	}
	if action == "email.send" {
		return "Clanker Vision can draft email, but this prototype will not send email automatically."
	}
	return ""
}

func visionConfirmationReason(req VisionRequest, action string) string {
	if action == "system.install" {
		return "Package installation changes the box runtime and requires explicit user confirmation."
	}
	text := strings.Join([]string{req.Instruction, req.Text, req.Content, req.URL, req.Subject}, " ")
	if sendLikeVisionText.MatchString(text) {
		return "This looks like a send/submit/publish/delete-style action. Confirm before Clanker Vision continues."
	}
	return ""
}

func runVisionBrowserAction(ctx context.Context, sessionID, action string, req VisionRequest) (*VisionObservation, error) {
	if action == "browser.open" {
		if _, err := url.ParseRequestURI(strings.TrimSpace(req.URL)); err != nil {
			return nil, fmt.Errorf("valid url is required for browser.open")
		}
	}
	dir, err := visionSessionDir(sessionID)
	if err != nil {
		return nil, err
	}
	input := map[string]any{
		"action":      action,
		"url":         strings.TrimSpace(req.URL),
		"selector":    strings.TrimSpace(req.Selector),
		"x":           req.X,
		"y":           req.Y,
		"text":        req.Text,
		"instruction": req.Instruction,
		"profileDir":  filepath.Join(dir, "browser-profile"),
		"statePath":   filepath.Join(dir, "browser-state.json"),
		"shotDir":     filepath.Join(dir, "screenshots"),
	}
	if err := os.MkdirAll(filepath.Join(dir, "screenshots"), 0o700); err != nil {
		return nil, err
	}
	inputPath := filepath.Join(dir, "browser-input.json")
	outputPath := filepath.Join(dir, "browser-output.json")
	inputData, _ := json.Marshal(input)
	if err := os.WriteFile(inputPath, inputData, 0o600); err != nil {
		return nil, err
	}
	scriptPath := filepath.Join(dir, "browser-bridge.py")
	if err := os.WriteFile(scriptPath, []byte(visionBrowserBridgePython), 0o700); err != nil {
		return nil, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	python := strings.TrimSpace(os.Getenv("CLANKER_BOX_VISION_PYTHON"))
	if python == "" {
		python = "python3"
	}
	cmd := exec.CommandContext(timeoutCtx, python, scriptPath, inputPath, outputPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CLANKER_BOX_VISION_USER_AGENT="+visionUserAgent)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("browser action timed out")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("browser action failed: %w: %s", err, trimTerminalOutput(detail))
		}
		return nil, fmt.Errorf("browser action failed: %w", err)
	}
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	var bridge browserBridgeResult
	if err := json.Unmarshal(outputData, &bridge); err != nil {
		return nil, err
	}
	if bridge.Error != "" {
		return nil, fmt.Errorf("%s", bridge.Error)
	}
	return &VisionObservation{
		URL:                 bridge.URL,
		Title:               bridge.Title,
		Width:               bridge.Width,
		Height:              bridge.Height,
		ScreenshotMediaType: bridge.ScreenshotMediaType,
		ScreenshotBase64:    bridge.ScreenshotBase64,
		ScreenshotPath:      bridge.ScreenshotPath,
		Pointer:             bridge.Pointer,
	}, nil
}

func browserVisionSummary(action string, observation *VisionObservation) string {
	title := strings.TrimSpace(observation.Title)
	if title == "" {
		title = strings.TrimSpace(observation.URL)
	}
	switch action {
	case "browser.click":
		return "Clicked in browser and captured the updated screen: " + title
	case "browser.type":
		return "Typed in browser and captured the updated screen: " + title
	case "browser.press":
		return "Pressed key in browser and captured the updated screen: " + title
	default:
		return "Captured browser screen: " + title
	}
}

func writeVisionDocument(ctx context.Context, sessionID string, req VisionRequest) (VisionArtifact, error) {
	dir, err := visionSessionDir(sessionID)
	if err != nil {
		return VisionArtifact{}, err
	}
	docDir := filepath.Join(dir, "documents")
	if err := os.MkdirAll(docDir, 0o700); err != nil {
		return VisionArtifact{}, err
	}
	name := safeVisionFileName(firstNonEmptyVision(req.FileName, req.Subject, "clanker-vision-document"), ".html")
	content := strings.TrimSpace(firstNonEmptyVision(req.Content, req.Text, req.Instruction))
	if content == "" {
		return VisionArtifact{}, fmt.Errorf("document content is required")
	}
	title := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	htmlPath := filepath.Join(docDir, name)
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) + "</title></head><body><h1>" + html.EscapeString(title) + "</h1><pre style=\"white-space:pre-wrap;font-family:Arial,sans-serif\">" + html.EscapeString(content) + "</pre></body></html>"
	if err := os.WriteFile(htmlPath, []byte(body), 0o600); err != nil {
		return VisionArtifact{}, err
	}
	if converted, ok := libreOfficeConvert(ctx, htmlPath, docDir, "docx"); ok {
		return VisionArtifact{Kind: "document", FilePath: converted, Media: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Summary: "Wrote Word-compatible document: " + converted}, nil
	}
	return VisionArtifact{Kind: "document", FilePath: htmlPath, Media: "text/html", Summary: "Wrote HTML document draft: " + htmlPath}, nil
}

func writeVisionSheet(ctx context.Context, sessionID string, req VisionRequest) (VisionArtifact, error) {
	dir, err := visionSessionDir(sessionID)
	if err != nil {
		return VisionArtifact{}, err
	}
	sheetDir := filepath.Join(dir, "spreadsheets")
	if err := os.MkdirAll(sheetDir, 0o700); err != nil {
		return VisionArtifact{}, err
	}
	name := safeVisionFileName(firstNonEmptyVision(req.FileName, "clanker-vision-sheet"), ".csv")
	csvPath := filepath.Join(sheetDir, name)
	file, err := os.OpenFile(csvPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return VisionArtifact{}, err
	}
	writer := csv.NewWriter(file)
	rows := req.Rows
	if len(rows) == 0 {
		rows = rowsFromVisionCSV(req.Content)
	}
	if len(rows) == 0 {
		rows = [][]string{{"Task", "Status"}, {strings.TrimSpace(firstNonEmptyVision(req.Instruction, "Clanker Vision sheet")), "draft"}}
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			file.Close()
			return VisionArtifact{}, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		file.Close()
		return VisionArtifact{}, err
	}
	if err := file.Close(); err != nil {
		return VisionArtifact{}, err
	}
	if converted, ok := libreOfficeConvert(ctx, csvPath, sheetDir, "xlsx"); ok {
		return VisionArtifact{Kind: "spreadsheet", FilePath: converted, Media: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Summary: "Wrote Excel-compatible spreadsheet: " + converted}, nil
	}
	return VisionArtifact{Kind: "spreadsheet", FilePath: csvPath, Media: "text/csv", Summary: "Wrote CSV spreadsheet draft: " + csvPath}, nil
}

func writeVisionEmailDraft(sessionID string, req VisionRequest) (VisionArtifact, error) {
	dir, err := visionSessionDir(sessionID)
	if err != nil {
		return VisionArtifact{}, err
	}
	draftDir := filepath.Join(dir, "email-drafts")
	if err := os.MkdirAll(draftDir, 0o700); err != nil {
		return VisionArtifact{}, err
	}
	subject := strings.TrimSpace(firstNonEmptyVision(req.Subject, "Clanker Vision draft"))
	body := strings.TrimSpace(firstNonEmptyVision(req.Content, req.Text, req.Instruction))
	if body == "" {
		return VisionArtifact{}, fmt.Errorf("email draft content is required")
	}
	name := safeVisionFileName(subject, ".eml")
	path := filepath.Join(draftDir, name)
	to := strings.TrimSpace(req.To)
	if to == "" {
		to = "unspecified@example.invalid"
	}
	content := strings.Join([]string{
		"To: " + to,
		"Subject: " + subject,
		"X-Clanker-Vision-Draft: true",
		"",
		body,
		"",
	}, "\r\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return VisionArtifact{}, err
	}
	return VisionArtifact{Kind: "email-draft", FilePath: path, Media: "message/rfc822", Summary: "Saved email draft without sending: " + path}, nil
}

func libreOfficeConvert(ctx context.Context, inputPath, outDir, format string) (string, bool) {
	lo, err := exec.LookPath("libreoffice")
	if err != nil {
		lo, err = exec.LookPath("soffice")
	}
	if err != nil {
		return "", false
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, lo, "--headless", "--convert-to", format, "--outdir", outDir, inputPath)
	if err := cmd.Run(); err != nil {
		return "", false
	}
	base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath)) + "." + format
	converted := filepath.Join(outDir, base)
	if _, err := os.Stat(converted); err == nil {
		return converted, true
	}
	return "", false
}

func visionSessionDir(sessionID string) (string, error) {
	base := filepath.Join(terminalWorkingDir(""), ".clanker-vision", "sessions", sanitizeVisionID(sessionID))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return base, nil
}

func sanitizeVisionID(raw string) string {
	id := strings.ToLower(strings.TrimSpace(raw))
	id = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(id, "-")
	id = strings.Trim(id, ".-_")
	if len(id) > 96 {
		id = id[:96]
	}
	return id
}

func safeVisionFileName(raw string, ext string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-_")
	if name == "" {
		name = "clanker-vision"
	}
	if len(name) > 80 {
		name = strings.Trim(name[:80], ".-_")
	}
	if filepath.Ext(name) == "" {
		name += ext
	}
	return name
}

func rowsFromVisionCSV(raw string) [][]string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return [][]string{{"Content"}, {text}}
	}
	return rows
}

func firstNonEmptyVision(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

const visionBrowserBridgePython = `
import base64
import json
import os
import shutil
import sys
import time

def write_result(path, payload):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(payload, f)

def main():
    input_path = sys.argv[1]
    output_path = sys.argv[2]
    try:
        from playwright.sync_api import sync_playwright
    except Exception as exc:
        write_result(output_path, {"error": "Playwright is not installed in this Clanker Box image: %s" % exc})
        return

    with open(input_path, "r", encoding="utf-8") as f:
        req = json.load(f)

    action = req.get("action") or "browser.screenshot"
    profile_dir = req.get("profileDir")
    shot_dir = req.get("shotDir")
    state_path = req.get("statePath")
    os.makedirs(profile_dir, exist_ok=True)
    os.makedirs(shot_dir, exist_ok=True)

    state = {}
    if state_path and os.path.exists(state_path):
        try:
            with open(state_path, "r", encoding="utf-8") as f:
                state = json.load(f)
        except Exception:
            state = {}

    url = (req.get("url") or state.get("url") or "about:blank").strip()
    chromium = os.environ.get("CLANKER_BOX_CHROMIUM_PATH") or shutil.which("chromium") or shutil.which("chromium-browser") or shutil.which("google-chrome")
    launch_args = ["--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking"]

    with sync_playwright() as p:
        context = p.chromium.launch_persistent_context(
            profile_dir,
            headless=os.environ.get("CLANKER_BOX_VISION_HEADLESS", "true").lower() != "false",
            executable_path=chromium,
            viewport={"width": 1280, "height": 800},
            user_agent=os.environ.get("CLANKER_BOX_VISION_USER_AGENT", "ClankerVision/0.1"),
            args=launch_args,
        )
        page = context.pages[0] if context.pages else context.new_page()
        page.set_default_timeout(15000)
        if action == "browser.open" or page.url == "about:blank":
            page.goto(url, wait_until="domcontentloaded", timeout=30000)
        elif url and url != "about:blank":
            page.goto(url, wait_until="domcontentloaded", timeout=30000)

        pointer = None
        if action == "browser.click":
            x = int(req.get("x") or 0)
            y = int(req.get("y") or 0)
            if x <= 0 or y <= 0:
                selector = (req.get("selector") or "").strip()
                if not selector:
                    raise RuntimeError("browser.click needs x/y coordinates or a selector")
                box = page.locator(selector).first.bounding_box()
                if not box:
                    raise RuntimeError("selector did not resolve to a visible element")
                x = int(box["x"] + box["width"] / 2)
                y = int(box["y"] + box["height"] / 2)
            page.mouse.click(x, y)
            pointer = {"x": x, "y": y, "label": "click"}
        elif action == "browser.type":
            x = int(req.get("x") or 0)
            y = int(req.get("y") or 0)
            selector = (req.get("selector") or "").strip()
            text = req.get("text") or ""
            if selector:
                page.locator(selector).first.fill(text)
                box = page.locator(selector).first.bounding_box()
                if box:
                    pointer = {"x": int(box["x"] + box["width"] / 2), "y": int(box["y"] + box["height"] / 2), "label": "type"}
            else:
                if x > 0 and y > 0:
                    page.mouse.click(x, y)
                    pointer = {"x": x, "y": y, "label": "type"}
                page.keyboard.type(text)
        elif action == "browser.press":
            key = req.get("text") or "Enter"
            page.keyboard.press(key)

        try:
            page.wait_for_load_state("networkidle", timeout=5000)
        except Exception:
            pass
        time.sleep(0.2)

        title = page.title()
        final_url = page.url
        shot_path = os.path.join(shot_dir, "screen-%d.jpg" % int(time.time() * 1000))
        shot = page.screenshot(path=shot_path, type="jpeg", quality=70, full_page=False)
        if not shot:
            with open(shot_path, "rb") as f:
                shot = f.read()
        if state_path:
            with open(state_path, "w", encoding="utf-8") as f:
                json.dump({"url": final_url, "title": title}, f)
        context.close()

    write_result(output_path, {
        "url": final_url,
        "title": title,
        "width": 1280,
        "height": 800,
        "screenshotMediaType": "image/jpeg",
        "screenshotBase64": base64.b64encode(shot).decode("ascii"),
        "screenshotPath": shot_path,
        "pointer": pointer,
    })

if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        write_result(sys.argv[2], {"error": str(exc)})
`
