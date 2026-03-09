package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
)

// AgenticRemediation runs the full ReAct-style remediation loop.
// It attempts to diagnose, fix, and verify AWS CLI command failures
// through multi-turn conversation with an LLM.
func AgenticRemediation(
	ctx context.Context,
	opts ExecOptions,
	failedArgs []string,
	awsArgs []string,
	stdinBytes []byte,
	failedOutput string,
	bindings map[string]string,
) (handled bool, err error) {
	// Check prerequisites
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return false, nil
	}

	// Initialize state
	state := &AgenticRemediationState{
		SessionID:          generateSessionID(),
		StartedAt:          time.Now(),
		FailedCommand:      stripAWSRuntimeFlags(failedArgs),
		FailedOutput:       failedOutput,
		History:            make([]ConversationTurn, 0),
		Phase:              PhaseDiagnose,
		Iteration:          0,
		DiagnosticOutput:   make(map[string]string),
		RemediationActions: make([]RemediationAction, 0),
		LearnedBindings:    make(map[string]string),
		Budget: &RemediationBudget{
			MaxIterations:       DefaultMaxIterations,
			MaxCommandsPerPhase: DefaultMaxCommandsPerPhase,
			MaxAPICallsTotal:    DefaultMaxAPICallsTotal,
			MaxDuration:         DefaultMaxDuration,
		},
	}

	// Copy existing bindings
	for k, v := range bindings {
		state.LearnedBindings[k] = v
	}

	// Classify the error
	failure := classifyAWSFailure(failedArgs, failedOutput)
	state.ErrorCategory = failure.Category

	_, _ = fmt.Fprintf(opts.Writer, "[agentic] starting remediation session %s for %s %s\n",
		state.SessionID, args0(failedArgs), args1(failedArgs))
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_loop_start", fmt.Sprintf("session=%s cmd=%s %s", state.SessionID, args0(failedArgs), args1(failedArgs)), "starting multi-turn remediation")
	}

	// Create AI client and conversation context
	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	conv := ai.NewConversationContext(agenticSystemPrompt(opts.Destroyer))

	// Main ReAct loop
	for state.Iteration < state.Budget.MaxIterations {
		state.Iteration++

		if time.Since(state.StartedAt) > state.Budget.MaxDuration {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic] budget exhausted: duration limit reached\n")
			break
		}

		_, _ = fmt.Fprintf(opts.Writer, "[agentic] iteration %d/%d, phase: %s\n",
			state.Iteration, state.Budget.MaxIterations, state.Phase)

		switch state.Phase {
		case PhaseDiagnose:
			if err := runDiagnosePhase(ctx, client, conv, opts, state); err != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[agentic] diagnose phase error: %v\n", err)
			}
			state.Phase = PhaseRemediate

		case PhaseRemediate:
			if err := runRemediatePhase(ctx, client, conv, opts, state); err != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[agentic] remediate phase error: %v\n", err)
				state.Phase = PhaseFailed
				continue
			}
			state.Phase = PhaseVerify

		case PhaseVerify:
			success, err := runVerifyPhase(ctx, client, conv, opts, state, stdinBytes)
			if err != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[agentic] verify phase error: %v\n", err)
			}
			if success {
				state.Phase = PhaseComplete
			} else {
				// Loop back to diagnose with new information
				state.Phase = PhaseDiagnose
			}

		case PhaseComplete:
			// Copy learned bindings back
			for k, v := range state.LearnedBindings {
				bindings[k] = v
			}
			_, _ = fmt.Fprintf(opts.Writer, "[agentic] remediation successful after %d iteration(s)\n",
				state.Iteration)
			if opts.PlanLogger != nil {
				opts.PlanLogger.WriteFixSuccess("agentic_loop_complete", fmt.Sprintf("session=%s iterations=%d", state.SessionID, state.Iteration), "multi-turn remediation succeeded")
			}
			return true, nil

		case PhaseFailed:
			_, _ = fmt.Fprintf(opts.Writer, "[agentic] remediation failed\n")
			if opts.PlanLogger != nil {
				opts.PlanLogger.WriteFix("agentic_loop_failed", fmt.Sprintf("session=%s iterations=%d", state.SessionID, state.Iteration), "multi-turn remediation failed")
			}
			return false, nil
		}
	}

	// Budget exhausted
	_, _ = fmt.Fprintf(opts.Writer, "[agentic] max iterations reached without resolution\n")
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_loop_exhausted", fmt.Sprintf("session=%s iterations=%d", state.SessionID, state.Iteration), "budget exhausted")
	}
	return false, fmt.Errorf("agentic remediation exhausted budget after %d iterations", state.Iteration)
}

// runDiagnosePhase executes the diagnostic phase
func runDiagnosePhase(
	ctx context.Context,
	client *ai.Client,
	conv *ai.ConversationContext,
	opts ExecOptions,
	state *AgenticRemediationState,
) error {
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] gathering diagnostic information...\n")

	// Build diagnostic prompt
	prompt := buildDiagnosePrompt(state)

	// Track the turn
	state.History = append(state.History, ConversationTurn{
		Role:      "user",
		Content:   prompt,
		Phase:     PhaseDiagnose,
		Timestamp: time.Now(),
	})

	// Ask LLM for diagnostic commands
	state.Budget.APICallsUsed++
	response, err := client.AskWithContext(ctx, conv, prompt)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	state.History = append(state.History, ConversationTurn{
		Role:      "assistant",
		Content:   response,
		Phase:     PhaseDiagnose,
		Timestamp: time.Now(),
	})

	// Parse diagnostic response
	var diag DiagnosticResponse
	cleaned := client.CleanJSONResponse(response)
	if err := json.Unmarshal([]byte(cleaned), &diag); err != nil {
		return fmt.Errorf("failed to parse diagnostic response: %w", err)
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] hypothesis: %s\n", diag.Hypothesis)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_diagnose", fmt.Sprintf("iteration=%d", state.Iteration), diag.Hypothesis)
	}

	// Execute diagnostic commands (read-only)
	for i, cmd := range diag.Commands {
		if i >= diagnosticPolicy.MaxCommands {
			break
		}

		// Normalize args
		cmd.Args = normalizeArgs(cmd.Args)

		if !isCommandAllowedForPhase(cmd.Args, diagnosticPolicy) {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] command blocked: %v\n", cmd.Args)
			continue
		}

		_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] running: %s %s (%s)\n",
			args0(cmd.Args), args1(cmd.Args), cmd.Purpose)

		cmdArgs := buildAWSExecArgs(cmd.Args, opts, opts.Writer)
		output, err := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer)

		state.Budget.CommandsExecuted++

		cmdKey := strings.Join(cmd.Args[:minInt(2, len(cmd.Args))], " ")
		state.DiagnosticOutput[cmdKey] = output

		if err != nil {
			state.DiagnosticOutput[cmdKey] = fmt.Sprintf("ERROR: %v\n%s", err, output)
		}

		// Learn bindings from output if specified
		if cmd.BindResult != "" && err == nil {
			state.LearnedBindings[cmd.BindResult] = strings.TrimSpace(output)
		}
	}

	return nil
}

// runRemediatePhase executes the remediation phase
func runRemediatePhase(
	ctx context.Context,
	client *ai.Client,
	conv *ai.ConversationContext,
	opts ExecOptions,
	state *AgenticRemediationState,
) error {
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] determining fix based on diagnostics...\n")

	// Build remediation prompt with diagnostic results
	prompt := buildRemediatePrompt(state)

	state.History = append(state.History, ConversationTurn{
		Role:      "user",
		Content:   prompt,
		Phase:     PhaseRemediate,
		Timestamp: time.Now(),
	})

	state.Budget.APICallsUsed++
	response, err := client.AskWithContext(ctx, conv, prompt)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	state.History = append(state.History, ConversationTurn{
		Role:      "assistant",
		Content:   response,
		Phase:     PhaseRemediate,
		Timestamp: time.Now(),
	})

	// Parse remediation response
	var remediation RemediationLLMResponse
	cleaned := client.CleanJSONResponse(response)
	if err := json.Unmarshal([]byte(cleaned), &remediation); err != nil {
		return fmt.Errorf("failed to parse remediation response: %w", err)
	}

	if remediation.Skip {
		_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] LLM says skip (already fixed)\n")
		return nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] root cause: %s\n", remediation.RootCause)
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] fix: %s\n", remediation.Fix)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_remediate", remediation.RootCause, remediation.Fix)
	}

	// Apply bindings from LLM
	for k, v := range remediation.Bindings {
		if strings.TrimSpace(v) != "" && bindingLooksCompatible(k, v) {
			state.LearnedBindings[k] = v
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] binding: %s = %s\n", k, v)
		}
	}

	// Execute remediation commands
	for i, cmd := range remediation.Commands {
		if i >= remediationPhasePolicy.MaxCommands {
			break
		}

		// Normalize args
		cmd.Args = normalizeArgs(cmd.Args)

		if !isCommandAllowedForPhase(cmd.Args, remediationPhasePolicy) {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] command blocked by phase policy: %v\n", cmd.Args)
			continue
		}

		// Validate with existing guardrails
		if err := validateRemediationCommand(cmd.Args); err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] command rejected: %v\n", err)
			continue
		}

		_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] step %d/%d: %s %s (%s)\n",
			i+1, len(remediation.Commands), args0(cmd.Args), args1(cmd.Args), cmd.Reason)

		// Apply bindings to command args
		cmdArgs := applyPlanBindings(cmd.Args, state.LearnedBindings)
		cmdArgs = buildAWSExecArgs(cmdArgs, opts, opts.Writer)

		output, err := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer)

		state.Budget.CommandsExecuted++

		action := RemediationAction{
			Phase:      PhaseRemediate,
			Command:    cmd.Args,
			Reason:     cmd.Reason,
			Output:     output,
			Success:    err == nil,
			ExecutedAt: time.Now(),
		}
		state.RemediationActions = append(state.RemediationActions, action)

		if err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] step %d warning: %v\n", i+1, err)
			// Continue - some steps may fail but others succeed
		} else {
			// Learn from successful command output
			learnPlanBindings(cmd.Args, output, state.LearnedBindings)
		}
	}

	return nil
}

// runVerifyPhase verifies the fix worked
func runVerifyPhase(
	ctx context.Context,
	client *ai.Client,
	conv *ai.ConversationContext,
	opts ExecOptions,
	state *AgenticRemediationState,
	stdinBytes []byte,
) (bool, error) {
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] retrying original command...\n")

	// Apply learned bindings to original args
	retryArgs := applyPlanBindings(state.FailedCommand, state.LearnedBindings)
	retryAWSArgs := buildAWSExecArgs(retryArgs, opts, opts.Writer)

	output, err := runAWSCommandStreaming(ctx, retryAWSArgs, stdinBytes, opts.Writer)
	state.Budget.CommandsExecuted++

	if err == nil {
		// Original command succeeded
		_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] original command succeeded\n")
		learnPlanBindings(retryArgs, output, state.LearnedBindings)
		if opts.PlanLogger != nil {
			opts.PlanLogger.WriteFixSuccess("agentic_verify", fmt.Sprintf("session=%s iteration=%d", state.SessionID, state.Iteration), "original command now succeeds")
		}
		return true, nil
	}

	// Original command still failed - ask LLM to assess
	prompt := buildVerifyAssessmentPrompt(state, output, err)

	state.Budget.APICallsUsed++
	response, errLLM := client.AskWithContext(ctx, conv, prompt)
	if errLLM != nil {
		return false, fmt.Errorf("LLM call failed: %w", errLLM)
	}

	var assessment VerificationAssessment
	cleaned := client.CleanJSONResponse(response)
	if errParse := json.Unmarshal([]byte(cleaned), &assessment); errParse != nil {
		// If we can't parse, assume failure
		_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] failed to parse LLM assessment: %v\n", errParse)
		return false, nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] assessment: success=%v confidence=%.2f\n",
		assessment.Success, assessment.Confidence)
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] explanation: %s\n", assessment.Explanation)

	state.VerificationResult = &VerificationResult{
		Command:    retryArgs,
		Output:     output,
		Success:    assessment.Success,
		Confidence: assessment.Confidence,
	}

	// Check if LLM says to give up
	if assessment.NextAction == "fail" {
		return false, fmt.Errorf("LLM assessment: unrecoverable failure")
	}

	return assessment.Success, nil
}

// isCommandAllowedForPhase validates a command against phase-specific policy
func isCommandAllowedForPhase(args []string, policy PhaseCommandPolicy) bool {
	if len(args) < 2 {
		return false
	}

	service := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))

	// Check service allowlist if specified
	if len(policy.AllowedServices) > 0 {
		found := false
		for _, s := range policy.AllowedServices {
			if s == service {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check blocklist first
	if policy.BlockedOps[op] {
		return false
	}

	// Check dangerous prefixes (always blocked)
	for _, prefix := range []string{"delete", "terminate", "destroy", "remove", "purge", "drop", "truncate"} {
		if strings.HasPrefix(op, prefix) {
			return false
		}
	}

	// Check allowlist
	for _, prefix := range policy.AllowedPrefixes {
		if strings.HasPrefix(op, prefix) {
			return true
		}
	}

	return false
}

// generateSessionID creates a short unique session identifier
func generateSessionID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()&0xFFFFFFFF)[:8]
}

// minInt returns the minimum of two ints
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// shouldUseAgenticRemediation determines if the full agentic loop is appropriate
func shouldUseAgenticRemediation(failure AWSFailure, output string, simpleFailed bool) bool {
	// Use agentic for complex failures that simple remediation couldn't handle
	if !simpleFailed {
		return false
	}

	// Good candidates for agentic remediation:
	// 1. NotFound errors on follow-on operations (missing prerequisite)
	// 2. Validation errors with unclear root cause
	// 3. Permission errors that might need policy creation
	// 4. Complex dependency failures

	switch failure.Category {
	case FailureNotFound:
		return looksLikeFollowOnOp(failure.Op)
	case FailureValidation:
		return true
	case FailureAccessDenied:
		return true
	case FailureConflict:
		return true
	default:
		// Check output for patterns that suggest complex issues
		lower := strings.ToLower(output)
		complexPatterns := []string{
			"does not have",
			"is not authorized",
			"missing",
			"invalid",
			"cannot be found",
			"must specify",
			"required parameter",
		}
		for _, p := range complexPatterns {
			if strings.Contains(lower, p) {
				return true
			}
		}
	}

	return false
}

// ShellAgenticRemediation runs the agentic loop for shell command failures (docker, git, etc.)
func ShellAgenticRemediation(
	ctx context.Context,
	opts ExecOptions,
	failedCommand string, // Full command as string
	failedOutput string,
	retryFunc func() error, // Function to retry the original operation
) (handled bool, err error) {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return false, nil
	}

	// Parse command into args
	failedArgs := strings.Fields(failedCommand)

	// Initialize state (reuse AgenticRemediationState)
	state := &AgenticRemediationState{
		SessionID:          generateSessionID(),
		StartedAt:          time.Now(),
		FailedCommand:      failedArgs,
		FailedOutput:       failedOutput,
		CommandType:        CommandTypeShell,
		History:            make([]ConversationTurn, 0),
		Phase:              PhaseDiagnose,
		Iteration:          0,
		DiagnosticOutput:   make(map[string]string),
		RemediationActions: make([]RemediationAction, 0),
		LearnedBindings:    make(map[string]string),
		Budget: &RemediationBudget{
			MaxIterations:       DefaultMaxIterations,
			MaxCommandsPerPhase: 4,
			MaxAPICallsTotal:    10,
			MaxDuration:         3 * time.Minute,
		},
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic] starting shell remediation session %s for: %s\n", state.SessionID, failedCommand)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_shell_start", failedCommand, "starting shell remediation loop")
	}

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	conv := ai.NewConversationContext(agenticDockerSystemPrompt())

	// Main ReAct loop (same structure as AgenticRemediation)
	for state.Iteration < state.Budget.MaxIterations {
		state.Iteration++

		if time.Since(state.StartedAt) > state.Budget.MaxDuration {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell remediation budget exhausted: duration limit\n")
			break
		}

		_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell iteration %d/%d, phase: %s\n",
			state.Iteration, state.Budget.MaxIterations, state.Phase)

		switch state.Phase {
		case PhaseDiagnose:
			if err := runShellDiagnosePhase(ctx, client, conv, opts, state); err != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell diagnose phase error: %v\n", err)
			}
			state.Phase = PhaseRemediate

		case PhaseRemediate:
			if err := runShellRemediatePhase(ctx, client, conv, opts, state); err != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell remediate phase error: %v\n", err)
				state.Phase = PhaseFailed
				continue
			}
			state.Phase = PhaseVerify

		case PhaseVerify:
			// Retry original operation using provided retry function
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] retrying original operation...\n")
			if retryErr := retryFunc(); retryErr == nil {
				state.Phase = PhaseComplete
			} else {
				// Update failed output for next iteration
				state.FailedOutput = retryErr.Error()
				_, _ = fmt.Fprintf(opts.Writer, "[agentic][verify] retry failed: %v\n", retryErr)
				state.Phase = PhaseDiagnose
			}

		case PhaseComplete:
			_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell remediation successful after %d iteration(s)\n", state.Iteration)
			if opts.PlanLogger != nil {
				opts.PlanLogger.WriteFixSuccess("agentic_shell_complete", failedCommand, "shell remediation succeeded")
			}
			return true, nil

		case PhaseFailed:
			_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell remediation failed\n")
			if opts.PlanLogger != nil {
				opts.PlanLogger.WriteFix("agentic_shell_failed", failedCommand, "shell remediation failed")
			}
			return false, nil
		}
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic] shell remediation exhausted budget after %d iterations\n", state.Iteration)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_shell_exhausted", failedCommand, "budget exhausted")
	}
	return false, fmt.Errorf("shell agentic remediation exhausted budget after %d iterations", state.Iteration)
}

// runShellDiagnosePhase runs diagnostic shell commands
func runShellDiagnosePhase(ctx context.Context, client *ai.Client, conv *ai.ConversationContext, opts ExecOptions, state *AgenticRemediationState) error {
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] gathering diagnostic information...\n")

	prompt := buildDockerDiagnosePrompt(state)

	state.History = append(state.History, ConversationTurn{
		Role:      "user",
		Content:   prompt,
		Phase:     PhaseDiagnose,
		Timestamp: time.Now(),
	})

	state.Budget.APICallsUsed++
	response, err := client.AskWithContext(ctx, conv, prompt)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	state.History = append(state.History, ConversationTurn{
		Role:      "assistant",
		Content:   response,
		Phase:     PhaseDiagnose,
		Timestamp: time.Now(),
	})

	var diag DiagnosticResponse
	cleaned := client.CleanJSONResponse(response)
	if err := json.Unmarshal([]byte(cleaned), &diag); err != nil {
		return fmt.Errorf("failed to parse diagnostic response: %w", err)
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] hypothesis: %s\n", diag.Hypothesis)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_shell_diagnose", fmt.Sprintf("iteration=%d", state.Iteration), diag.Hypothesis)
	}

	// Execute diagnostic commands
	for i, cmd := range diag.Commands {
		if i >= 3 { // Max 3 diagnostic commands
			break
		}

		cmd.Args = normalizeArgs(cmd.Args)
		if !isShellCommandSafe(cmd.Args) {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] command blocked: %v\n", cmd.Args)
			continue
		}

		cmdStr := strings.Join(cmd.Args, " ")
		_, _ = fmt.Fprintf(opts.Writer, "[agentic][diagnose] running: %s (%s)\n", cmdStr, cmd.Purpose)

		shellCmd := exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
		out, _ := shellCmd.CombinedOutput()
		state.DiagnosticOutput[cmdStr] = string(out)
		state.Budget.CommandsExecuted++
	}

	return nil
}

// runShellRemediatePhase runs remediation shell commands
func runShellRemediatePhase(ctx context.Context, client *ai.Client, conv *ai.ConversationContext, opts ExecOptions, state *AgenticRemediationState) error {
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] determining fix based on diagnostics...\n")

	prompt := buildDockerRemediatePrompt(state)

	state.History = append(state.History, ConversationTurn{
		Role:      "user",
		Content:   prompt,
		Phase:     PhaseRemediate,
		Timestamp: time.Now(),
	})

	state.Budget.APICallsUsed++
	response, err := client.AskWithContext(ctx, conv, prompt)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	state.History = append(state.History, ConversationTurn{
		Role:      "assistant",
		Content:   response,
		Phase:     PhaseRemediate,
		Timestamp: time.Now(),
	})

	var remediation RemediationLLMResponse
	cleaned := client.CleanJSONResponse(response)
	if err := json.Unmarshal([]byte(cleaned), &remediation); err != nil {
		return fmt.Errorf("failed to parse remediation response: %w", err)
	}

	if remediation.Skip {
		_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] LLM says skip (already fixed)\n")
		return nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] root cause: %s\n", remediation.RootCause)
	_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] fix: %s\n", remediation.Fix)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("agentic_shell_remediate", remediation.RootCause, remediation.Fix)
	}

	// Execute remediation commands
	for i, cmd := range remediation.Commands {
		if i >= 4 { // Max 4 remediation commands
			break
		}

		cmd.Args = normalizeArgs(cmd.Args)
		if !isShellCommandSafe(cmd.Args) {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] command %d blocked: %v\n", i+1, cmd.Args)
			continue
		}

		cmdStr := strings.Join(cmd.Args, " ")
		_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] step %d/%d: %s (%s)\n", i+1, len(remediation.Commands), cmdStr, cmd.Reason)

		shellCmd := exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
		out, runErr := shellCmd.CombinedOutput()

		action := RemediationAction{
			Phase:      PhaseRemediate,
			Command:    cmd.Args,
			Reason:     cmd.Reason,
			Output:     string(out),
			Success:    runErr == nil,
			ExecutedAt: time.Now(),
		}
		state.RemediationActions = append(state.RemediationActions, action)
		state.Budget.CommandsExecuted++

		if runErr != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[agentic][remediate] step %d warning: %v\n", i+1, runErr)
		}
	}

	return nil
}

// isShellCommandSafe validates shell commands for safety
func isShellCommandSafe(args []string) bool {
	if len(args) == 0 {
		return false
	}

	// Only allow docker and git commands
	allowed := []string{"docker", "git", "which", "command"}
	cmd := strings.ToLower(args[0])
	for _, a := range allowed {
		if cmd == a {
			return true
		}
	}
	return false
}
