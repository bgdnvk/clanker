package maker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// agenticSystemPrompt returns the base system prompt for the agentic remediation loop
func agenticSystemPrompt(destroyer bool) string {
	return fmt.Sprintf(`You are an expert AWS CLI remediation agent operating in a ReAct-style loop.
Your goal is to diagnose, fix, and verify AWS CLI command failures.

You operate in three phases:
1. DIAGNOSE: Run read-only commands to understand the current AWS state
2. REMEDIATE: Execute commands to fix the issue
3. VERIFY: Confirm the original command now succeeds

STRICT RULES:
- Output ONLY valid JSON matching the requested schema
- NEVER propose delete/terminate/destroy/remove/purge commands
- Diagnostic commands must be read-only (describe-, list-, get-)
- Remediation commands must be from: create-, put-, update-, add-, attach-, associate-, register-, enable-, tag-, set-, modify-, authorize-
- Commands are AWS CLI subcommands only (NO "aws" prefix)
- Do NOT include --profile/--region/--no-cli-pager flags
- Destroyer mode (can replace existing resources): %t

Be concise and focused. Prefer minimal changes that fix the issue.`, destroyer)
}

// buildDiagnosePrompt creates the prompt for the diagnostic phase
func buildDiagnosePrompt(state *AgenticRemediationState) string {
	var previousDiagnostics string
	if len(state.DiagnosticOutput) > 0 {
		parts := make([]string, 0)
		for cmd, output := range state.DiagnosticOutput {
			truncated := output
			if len(truncated) > 500 {
				truncated = truncated[:500] + "..."
			}
			parts = append(parts, fmt.Sprintf("Command: %s\nOutput: %s", cmd, truncated))
		}
		previousDiagnostics = strings.Join(parts, "\n---\n")
	}

	var previousActions string
	if len(state.RemediationActions) > 0 {
		parts := make([]string, 0)
		for _, a := range state.RemediationActions {
			parts = append(parts, fmt.Sprintf("- %v: %s (success=%v)", a.Command, a.Reason, a.Success))
		}
		previousActions = strings.Join(parts, "\n")
	}

	return fmt.Sprintf(`PHASE: DIAGNOSE (iteration %d)

FAILED COMMAND:
%v

ERROR OUTPUT:
%s

ERROR CATEGORY: %s

CURRENT BINDINGS:
%s

PREVIOUS DIAGNOSTIC OUTPUT (if any):
%s

PREVIOUS REMEDIATION ATTEMPTS (if any):
%s

Output JSON with this schema:
{
  "analysis": "Brief analysis of the error",
  "hypothesis": "What you think is wrong",
  "commands": [
    {"args": ["service", "describe-...", ...], "purpose": "What this will tell us", "bind_result": "OPTIONAL_BINDING_KEY"}
  ],
  "notes": ["Optional notes"]
}

Focus on READ-ONLY commands: describe-, list-, get-.
Maximum 5 diagnostic commands.
`, state.Iteration, state.FailedCommand, truncateString(state.FailedOutput, 1000),
		state.ErrorCategory, formatBindingsForPrompt(state.LearnedBindings),
		previousDiagnostics, previousActions)
}

// buildRemediatePrompt creates the prompt for the remediation phase
func buildRemediatePrompt(state *AgenticRemediationState) string {
	var diagnosticSummary string
	if len(state.DiagnosticOutput) > 0 {
		parts := make([]string, 0)
		for cmd, output := range state.DiagnosticOutput {
			truncated := output
			if len(truncated) > 300 {
				truncated = truncated[:300] + "..."
			}
			parts = append(parts, fmt.Sprintf("=== %s ===\n%s", cmd, truncated))
		}
		diagnosticSummary = strings.Join(parts, "\n\n")
	}

	return fmt.Sprintf(`PHASE: REMEDIATE

Based on diagnostics, fix the failing command.

FAILED COMMAND:
%v

ERROR OUTPUT:
%s

DIAGNOSTIC RESULTS:
%s

CURRENT BINDINGS:
%s

Output JSON with this schema:
{
  "root_cause": "The confirmed root cause",
  "fix": "Human-readable description of the fix",
  "commands": [
    {"args": ["service", "create-...", ...], "reason": "Why this helps"}
  ],
  "skip": false,
  "bindings": {"KEY": "value"},
  "notes": ["Optional"]
}

Rules:
- If the issue is already resolved, set "skip": true
- Use bindings to pass values between commands
- Prefer create/put/update commands
- Maximum 8 commands
`, state.FailedCommand, truncateString(state.FailedOutput, 500),
		diagnosticSummary, formatBindingsForPrompt(state.LearnedBindings))
}

// buildVerifyAssessmentPrompt creates the prompt for verification assessment
func buildVerifyAssessmentPrompt(state *AgenticRemediationState, retryOutput string, retryErr error) string {
	errStr := ""
	if retryErr != nil {
		errStr = retryErr.Error()
	}

	return fmt.Sprintf(`PHASE: VERIFY

The original command was retried after remediation.

ORIGINAL COMMAND:
%v

RETRY OUTPUT:
%s

RETRY ERROR (if any):
%s

REMEDIATION ACTIONS TAKEN:
%s

Assess whether the fix worked. Output JSON:
{
  "success": true/false,
  "confidence": 0.0-1.0,
  "explanation": "Why you believe the fix worked or didn't",
  "next_action": "complete" | "retry_remediation" | "fail"
}

Set "success": true if the command appears to have succeeded or the error is benign (already exists, etc.).
Set "next_action": "retry_remediation" if we should try a different approach.
Set "next_action": "fail" if the situation is unrecoverable.
`, state.FailedCommand, truncateString(retryOutput, 800), errStr,
		formatRemediationActions(state.RemediationActions))
}

// truncateString truncates a string to maxLen with ellipsis
func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// formatBindingsForPrompt formats bindings map for inclusion in prompts
func formatBindingsForPrompt(bindings map[string]string) string {
	if len(bindings) == 0 {
		return "(none)"
	}

	// Filter to relevant bindings, exclude very long values
	safe := make(map[string]string)
	for k, v := range bindings {
		// Skip empty or secret-looking values
		if v == "" {
			continue
		}
		lower := strings.ToLower(k)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "token") {
			continue
		}
		// Truncate long values
		if len(v) > 200 {
			v = v[:200] + "..."
		}
		safe[k] = v
	}

	if len(safe) == 0 {
		return "(none)"
	}

	data, _ := json.MarshalIndent(safe, "", "  ")
	return string(data)
}

// formatRemediationActions formats actions for prompt
func formatRemediationActions(actions []RemediationAction) string {
	if len(actions) == 0 {
		return "(none)"
	}

	var parts []string
	for _, a := range actions {
		status := "success"
		if !a.Success {
			status = "failed"
		}
		cmdStr := ""
		if len(a.Command) >= 2 {
			cmdStr = fmt.Sprintf("%s %s", a.Command[0], a.Command[1])
		} else if len(a.Command) == 1 {
			cmdStr = a.Command[0]
		}
		parts = append(parts, fmt.Sprintf("- %s (%s): %s", cmdStr, status, a.Reason))
	}
	return strings.Join(parts, "\n")
}

// agenticDockerSystemPrompt returns the system prompt for docker remediation
func agenticDockerSystemPrompt() string {
	return `You are an expert Docker troubleshooting agent operating in a ReAct-style loop.
Your goal is to diagnose, fix, and verify Docker command failures.

You operate in three phases:
1. DIAGNOSE: Run read-only docker commands to understand the current state
2. REMEDIATE: Execute commands to fix the issue
3. VERIFY: Confirm the original command now succeeds

STRICT RULES:
- Output ONLY valid JSON matching the requested schema
- Commands are shell commands as arrays of args (e.g. ["docker", "buildx", "ls"])
- NEVER propose destructive commands (rm -rf /, sudo rm, etc.)
- Diagnostic commands: docker info, docker buildx inspect, docker buildx ls, docker version
- Remediation commands: docker buildx create, docker buildx rm, docker buildx use, docker login

Common docker buildx fixes:
- "unknown flag: --name" -> use positional: ["docker", "buildx", "create", "<name>", "--driver", "docker-container"]
- Builder not found -> docker buildx rm -f <name> && docker buildx create <name> --driver docker-container
- Driver issue -> recreate builder with correct driver
- Daemon not running -> start Docker Desktop or docker service

Be concise and focused. Prefer minimal changes that fix the issue.`
}

// buildDockerDiagnosePrompt creates diagnose prompt for docker issues
func buildDockerDiagnosePrompt(state *AgenticRemediationState) string {
	return fmt.Sprintf(`PHASE: DIAGNOSE (iteration %d)

FAILED COMMAND:
%s

ERROR OUTPUT:
%s

PREVIOUS DIAGNOSTIC OUTPUT:
%s

Output JSON:
{
  "analysis": "Brief analysis of the error",
  "hypothesis": "What you think is wrong",
  "commands": [
    {"args": ["docker", "buildx", "ls"], "purpose": "List current builders"}
  ],
  "notes": []
}

Focus on: docker info, docker buildx ls, docker buildx inspect, docker version
Maximum 3 diagnostic commands.
`, state.Iteration, strings.Join(state.FailedCommand, " "),
		truncateString(state.FailedOutput, 1000),
		formatDockerDiagnostics(state.DiagnosticOutput))
}

// buildDockerRemediatePrompt creates remediate prompt for docker issues
func buildDockerRemediatePrompt(state *AgenticRemediationState) string {
	return fmt.Sprintf(`PHASE: REMEDIATE

Based on diagnostics, fix the docker issue.

FAILED COMMAND:
%s

ERROR OUTPUT:
%s

DIAGNOSTIC RESULTS:
%s

Output JSON:
{
  "root_cause": "The confirmed root cause",
  "fix": "Human-readable description of the fix",
  "commands": [
    {"args": ["docker", "buildx", "rm", "-f", "clanker-builder"], "reason": "Remove broken builder"},
    {"args": ["docker", "buildx", "create", "clanker-builder", "--driver", "docker-container"], "reason": "Create new builder"}
  ],
  "skip": false,
  "notes": []
}

Rules:
- Commands are full shell commands as array of args
- Maximum 4 commands
- If already fixed, set "skip": true
`, strings.Join(state.FailedCommand, " "),
		truncateString(state.FailedOutput, 500),
		formatDockerDiagnostics(state.DiagnosticOutput))
}

// formatDockerDiagnostics formats docker diagnostic output for prompts
func formatDockerDiagnostics(output map[string]string) string {
	if len(output) == 0 {
		return "(none)"
	}
	var parts []string
	for cmd, out := range output {
		truncated := out
		if len(truncated) > 300 {
			truncated = truncated[:300] + "..."
		}
		parts = append(parts, fmt.Sprintf("=== %s ===\n%s", cmd, truncated))
	}
	return strings.Join(parts, "\n\n")
}
