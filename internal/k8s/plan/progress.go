package plan

import (
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ProgressWriter handles progress output during plan execution
type ProgressWriter struct {
	w           io.Writer
	currentStep int
	totalSteps  int
	startTime   time.Time
	debug       bool
}

// NewProgressWriter creates a new ProgressWriter
func NewProgressWriter(w io.Writer, totalSteps int, debug bool) *ProgressWriter {
	return &ProgressWriter{
		w:          w,
		totalSteps: totalSteps,
		startTime:  time.Now(),
		debug:      debug,
	}
}

// StartStep marks the beginning of a new step
func (p *ProgressWriter) StartStep(step Step) {
	p.currentStep++
	fmt.Fprintf(p.w, "[k8s] running step %d/%d: %s\n", p.currentStep, p.totalSteps, step.Description)
}

// LogCommand logs a command being executed
func (p *ProgressWriter) LogCommand(prefix, cmd string) {
	if prefix == "" {
		prefix = "cmd"
	}
	// Truncate very long commands
	if len(cmd) > 200 {
		cmd = cmd[:200] + "..."
	}
	fmt.Fprintf(p.w, "[%s] %s\n", prefix, cmd)
}

// LogCommandOutput logs output from a command
func (p *ProgressWriter) LogCommandOutput(prefix, output string) {
	if strings.TrimSpace(output) == "" {
		return
	}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			fmt.Fprintf(p.w, "[%s] %s\n", prefix, line)
		}
	}
}

// LogStatus logs a status message
func (p *ProgressWriter) LogStatus(status string) {
	fmt.Fprintf(p.w, "[k8s] status: %s\n", status)
}

// LogWait logs a waiting message
func (p *ProgressWriter) LogWait(msg string) {
	fmt.Fprintf(p.w, "[k8s] waiting: %s\n", msg)
}

// LogWaitProgress logs waiting progress with iteration count
func (p *ProgressWriter) LogWaitProgress(msg string, current, max int) {
	fmt.Fprintf(p.w, "[k8s] waiting: %s (%d/%d)\n", msg, current, max)
}

// LogNote logs an informational note
func (p *ProgressWriter) LogNote(msg string) {
	fmt.Fprintf(p.w, "[k8s] note: %s\n", msg)
}

// LogWarning logs a warning message
func (p *ProgressWriter) LogWarning(msg string) {
	fmt.Fprintf(p.w, "[k8s] warning: %s\n", msg)
}

// LogError logs an error message
func (p *ProgressWriter) LogError(msg string) {
	fmt.Fprintf(p.w, "[k8s] error: %s\n", msg)
}

// LogConfigChange logs a configuration file change
func (p *ProgressWriter) LogConfigChange(change ConfigChange) {
	fmt.Fprintf(p.w, "[k8s] config change: %s\n", change.File)
	if change.Description != "" {
		fmt.Fprintf(p.w, "[k8s]   %s\n", change.Description)
	}
	if change.Diff != "" && p.debug {
		lines := strings.Split(change.Diff, "\n")
		for _, line := range lines {
			fmt.Fprintf(p.w, "[k8s]   %s\n", line)
		}
	}
}

// LogSSH logs SSH connection info
func (p *ProgressWriter) LogSSH(host, user string) {
	fmt.Fprintf(p.w, "[ssh] connecting to %s@%s\n", user, host)
}

// LogSSHConnected logs successful SSH connection
func (p *ProgressWriter) LogSSHConnected(host string) {
	fmt.Fprintf(p.w, "[ssh] connected to %s\n", host)
}

// LogSSHCommand logs an SSH command being executed
func (p *ProgressWriter) LogSSHCommand(scriptName string) {
	if scriptName != "" {
		fmt.Fprintf(p.w, "[ssh] running: %s\n", scriptName)
	}
}

// LogBinding logs a binding being learned
func (p *ProgressWriter) LogBinding(key, value string) {
	if p.debug {
		fmt.Fprintf(p.w, "[k8s] binding: %s=%s\n", key, value)
	}
}

// LogDebug logs a debug message (only if debug is enabled)
func (p *ProgressWriter) LogDebug(msg string) {
	if p.debug {
		fmt.Fprintf(p.w, "[k8s] debug: %s\n", msg)
	}
}

// LogSuccess logs a success message for a step
func (p *ProgressWriter) LogSuccess(msg string) {
	fmt.Fprintf(p.w, "[k8s] success: %s\n", msg)
}

// LogDuration logs the elapsed time
func (p *ProgressWriter) LogDuration() {
	elapsed := time.Since(p.startTime)
	fmt.Fprintf(p.w, "[k8s] completed in %s\n", formatDuration(elapsed))
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", hours, mins)
}

// DisplayPlan prints the plan in a human-readable format
func DisplayPlan(w io.Writer, plan *K8sPlan, opts PlanDisplayOptions) {
	fmt.Fprintf(w, "=== K8s %s Plan ===\n\n", formatOperation(plan.Operation))

	fmt.Fprintf(w, "Operation:      %s\n", formatOperation(plan.Operation))
	if plan.ClusterType != "" {
		fmt.Fprintf(w, "Cluster Type:   %s\n", strings.ToUpper(plan.ClusterType))
	}
	fmt.Fprintf(w, "Cluster Name:   %s\n", plan.ClusterName)
	fmt.Fprintf(w, "Region:         %s\n", plan.Region)
	fmt.Fprintf(w, "AWS Profile:    %s\n", plan.Profile)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Steps:")
	for i, step := range plan.Steps {
		fmt.Fprintf(w, "  %d. %s\n", i+1, step.Description)
		if opts.ShowCommands && len(step.Args) > 0 {
			cmdStr := strings.Join(step.Args, " ")
			if len(cmdStr) > 80 {
				cmdStr = cmdStr[:80] + "..."
			}
			fmt.Fprintf(w, "     %s %s\n", step.Command, cmdStr)
		}
		if step.WaitFor != nil && step.WaitFor.Description != "" {
			fmt.Fprintf(w, "     (wait: %s)\n", step.WaitFor.Description)
		}
	}
	fmt.Fprintln(w)

	// Show config changes
	hasConfigChanges := false
	for _, step := range plan.Steps {
		if step.ConfigChange != nil {
			if !hasConfigChanges {
				fmt.Fprintln(w, "Config Changes:")
				hasConfigChanges = true
			}
			fmt.Fprintf(w, "  %s\n", step.ConfigChange.File)
			if step.ConfigChange.Description != "" {
				fmt.Fprintf(w, "    %s\n", step.ConfigChange.Description)
			}
		}
	}
	if hasConfigChanges {
		fmt.Fprintln(w)
	}

	// Show notes
	if len(plan.Notes) > 0 {
		fmt.Fprintln(w, "Notes:")
		for _, note := range plan.Notes {
			fmt.Fprintf(w, "  - %s\n", note)
		}
		fmt.Fprintln(w)
	}
}

// DisplayConnection prints connection information
func DisplayConnection(w io.Writer, conn *Connection) {
	if conn == nil {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "To connect to your cluster:")
	for _, cmd := range conn.Commands {
		fmt.Fprintf(w, "  %s\n", cmd)
	}
}

// DisplayResult prints the execution result
func DisplayResult(w io.Writer, plan *K8sPlan, result *ExecResult) {
	fmt.Fprintln(w)
	if result.Success {
		fmt.Fprintf(w, "=== %s Completed Successfully ===\n", formatOperation(plan.Operation))
	} else {
		fmt.Fprintf(w, "=== %s Failed ===\n", formatOperation(plan.Operation))
		if len(result.Errors) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Errors:")
			for _, err := range result.Errors {
				fmt.Fprintf(w, "  - %s\n", err)
			}
		}
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Name:       %s\n", plan.ClusterName)

	if result.Connection != nil {
		if result.Connection.Endpoint != "" {
			fmt.Fprintf(w, "Endpoint:   %s\n", result.Connection.Endpoint)
		}
		if result.Connection.Kubeconfig != "" {
			fmt.Fprintf(w, "Kubeconfig: %s\n", result.Connection.Kubeconfig)
		}
	}

	DisplayConnection(w, result.Connection)
}

func formatOperation(op string) string {
	switch op {
	case "create-cluster":
		return "Cluster Creation"
	case "delete-cluster":
		return "Cluster Deletion"
	case "deploy":
		return "Deployment"
	case "scale":
		return "Scale"
	default:
		return cases.Title(language.English).String(strings.ReplaceAll(op, "-", " "))
	}
}
