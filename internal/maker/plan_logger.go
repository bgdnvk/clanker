package maker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PlanLogWriter writes plan execution output to ~/.clanker/logs/plan/<runID>/
type PlanLogWriter struct {
	runID      string
	logDir     string
	outputFile *os.File
	eventsFile *os.File
	fixesFile  *os.File
	startTime  time.Time
	mu         sync.Mutex

	// Tracking for summary
	commandsRun       int
	commandsSucceeded int
	commandsFailed    int
	commandsSkipped   int
	fixAttempts       int
	fixSuccesses      int
	bindings          map[string]string
}

// PlanLogEvent represents a structured event in events.log
type PlanLogEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

// PlanLogSummary represents the final summary.json
type PlanLogSummary struct {
	RunID     string    `json:"runID"`
	StartTime time.Time `json:"startTime"`
	EndTime   time.Time `json:"endTime"`
	Duration  string    `json:"duration"`
	Status    string    `json:"status"`
	Plan      struct {
		Question     string `json:"question"`
		CommandCount int    `json:"commandCount"`
	} `json:"plan"`
	Execution struct {
		CommandsRun       int `json:"commandsRun"`
		CommandsSucceeded int `json:"commandsSucceeded"`
		CommandsFailed    int `json:"commandsFailed"`
		CommandsSkipped   int `json:"commandsSkipped"`
	} `json:"execution"`
	Fixes struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
	} `json:"fixes"`
	Bindings map[string]string `json:"bindings,omitempty"`
}

// NewPlanLogWriter creates a new plan log writer with a unique run ID
func NewPlanLogWriter(runID string) (*PlanLogWriter, error) {
	if runID == "" {
		runID = fmt.Sprintf("run-%d-%s", time.Now().Unix(), generateSessionID())
	}

	// Get home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = os.TempDir()
	}

	logDir := filepath.Join(homeDir, ".clanker", "logs", "plan", runID)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open output.log
	outputFile, err := os.Create(filepath.Join(logDir, "output.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to create output.log: %w", err)
	}

	// Open events.log
	eventsFile, err := os.Create(filepath.Join(logDir, "events.log"))
	if err != nil {
		outputFile.Close()
		return nil, fmt.Errorf("failed to create events.log: %w", err)
	}

	// Open fixes.log
	fixesFile, err := os.Create(filepath.Join(logDir, "fixes.log"))
	if err != nil {
		outputFile.Close()
		eventsFile.Close()
		return nil, fmt.Errorf("failed to create fixes.log: %w", err)
	}

	w := &PlanLogWriter{
		runID:      runID,
		logDir:     logDir,
		outputFile: outputFile,
		eventsFile: eventsFile,
		fixesFile:  fixesFile,
		startTime:  time.Now(),
		bindings:   make(map[string]string),
	}

	return w, nil
}

// Write implements io.Writer interface for capturing all output
func (w *PlanLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.outputFile == nil {
		return len(p), nil
	}

	// Add timestamp prefix to each line
	timestamp := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("[%s] %s", timestamp, string(p))
	_, err = w.outputFile.WriteString(line)
	if err != nil {
		return 0, err
	}
	// Return len(p) to satisfy io.Writer contract - caller expects
	// the number of bytes from p that were processed, not the length
	// of our timestamped output
	return len(p), nil
}

// WriteEvent writes a structured event to events.log
func (w *PlanLogWriter) WriteEvent(eventType, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.eventsFile == nil {
		return
	}

	event := PlanLogEvent{
		Timestamp: time.Now(),
		Type:      eventType,
		Message:   message,
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	w.eventsFile.WriteString(string(data) + "\n")
}

// WriteFix writes a fix attempt to fixes.log
func (w *PlanLogWriter) WriteFix(fixType, command, result string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.fixAttempts++

	if w.fixesFile == nil {
		return
	}

	timestamp := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("[%s] TYPE=%s CMD=%s RESULT=%s\n", timestamp, fixType, command, result)
	w.fixesFile.WriteString(line)
}

// WriteFixSuccess records a successful fix
func (w *PlanLogWriter) WriteFixSuccess(fixType, command, result string) {
	w.mu.Lock()
	w.fixSuccesses++
	w.mu.Unlock()

	w.WriteFix(fixType, command, "SUCCESS: "+result)
}

// WritePlan saves the original plan to plan.json
func (w *PlanLogWriter) WritePlan(plan *Plan) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	planPath := filepath.Join(w.logDir, "plan.json")
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal plan: %w", err)
	}

	return os.WriteFile(planPath, data, 0644)
}

// RecordCommandStart records the start of a command execution
func (w *PlanLogWriter) RecordCommandStart(idx int, service, operation string) {
	w.WriteEvent("command_start", fmt.Sprintf("cmd %d: %s %s", idx+1, service, operation))
}

// RecordCommandSuccess records a successful command execution
func (w *PlanLogWriter) RecordCommandSuccess(idx int, service, operation, output string) {
	w.mu.Lock()
	w.commandsRun++
	w.commandsSucceeded++
	w.mu.Unlock()

	truncatedOutput := output
	if len(truncatedOutput) > 100 {
		truncatedOutput = truncatedOutput[:100] + "..."
	}
	w.WriteEvent("command_complete", fmt.Sprintf("cmd %d: %s %s - success", idx+1, service, operation))
}

// RecordCommandFailure records a failed command execution
func (w *PlanLogWriter) RecordCommandFailure(idx int, service, operation, errMsg string) {
	w.mu.Lock()
	w.commandsRun++
	w.commandsFailed++
	w.mu.Unlock()

	w.WriteEvent("command_failed", fmt.Sprintf("cmd %d: %s %s - %s", idx+1, service, operation, errMsg))
}

// RecordCommandSkipped records a skipped command
func (w *PlanLogWriter) RecordCommandSkipped(idx int, service, operation, reason string) {
	w.mu.Lock()
	w.commandsSkipped++
	w.mu.Unlock()

	w.WriteEvent("command_skipped", fmt.Sprintf("cmd %d: %s %s - %s", idx+1, service, operation, reason))
}

// UpdateBindings updates the tracked bindings
func (w *PlanLogWriter) UpdateBindings(bindings map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for k, v := range bindings {
		w.bindings[k] = v
	}
}

// WriteSummary writes the final summary.json
func (w *PlanLogWriter) WriteSummary(status string, plan *Plan) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	endTime := time.Now()
	summary := PlanLogSummary{
		RunID:     w.runID,
		StartTime: w.startTime,
		EndTime:   endTime,
		Duration:  endTime.Sub(w.startTime).Round(time.Second).String(),
		Status:    status,
		Bindings:  w.bindings,
	}

	if plan != nil {
		summary.Plan.Question = plan.Question
		summary.Plan.CommandCount = len(plan.Commands)
	}

	summary.Execution.CommandsRun = w.commandsRun
	summary.Execution.CommandsSucceeded = w.commandsSucceeded
	summary.Execution.CommandsFailed = w.commandsFailed
	summary.Execution.CommandsSkipped = w.commandsSkipped

	summary.Fixes.Total = w.fixAttempts
	summary.Fixes.Successful = w.fixSuccesses

	summaryPath := filepath.Join(w.logDir, "summary.json")
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal summary: %w", err)
	}

	return os.WriteFile(summaryPath, data, 0644)
}

// GetLogDir returns the log directory path
func (w *PlanLogWriter) GetLogDir() string {
	return w.logDir
}

// GetRunID returns the run ID
func (w *PlanLogWriter) GetRunID() string {
	return w.runID
}

// Close closes all log files and writes the summary
func (w *PlanLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var errs []error

	if w.outputFile != nil {
		if err := w.outputFile.Close(); err != nil {
			errs = append(errs, err)
		}
		w.outputFile = nil
	}

	if w.eventsFile != nil {
		if err := w.eventsFile.Close(); err != nil {
			errs = append(errs, err)
		}
		w.eventsFile = nil
	}

	if w.fixesFile != nil {
		if err := w.fixesFile.Close(); err != nil {
			errs = append(errs, err)
		}
		w.fixesFile = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing log files: %v", errs)
	}

	return nil
}
