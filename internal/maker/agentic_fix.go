package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
)

// agenticFix represents an AI-proposed fix for a failed command.
type agenticFix struct {
	// RewrittenArgs: if non-nil, use these args instead of the original
	RewrittenArgs []string `json:"rewritten_args,omitempty"`
	// PreCommands: commands to run before retrying the original
	PreCommands []Command `json:"pre_commands,omitempty"`
	// Bindings: new placeholder bindings discovered
	Bindings map[string]string `json:"bindings,omitempty"`
	// Skip: if true, skip this command (it's already done or not needed)
	Skip bool `json:"skip,omitempty"`
	// Notes: explanation
	Notes []string `json:"notes,omitempty"`
}

// agenticFixPrompt generates a comprehensive prompt for fixing failed commands.
func agenticFixPrompt(destroyer bool, failedArgs []string, errorOutput string, bindings map[string]string) string {
	// Strip injected flags for clarity
	trimmed := make([]string, 0, len(failedArgs))
	for i := 0; i < len(failedArgs); i++ {
		if failedArgs[i] == "--profile" || failedArgs[i] == "--region" || failedArgs[i] == "--no-cli-pager" {
			i++
			continue
		}
		trimmed = append(trimmed, failedArgs[i])
	}

	bindingsJSON, _ := json.MarshalIndent(bindings, "", "  ")

	return fmt.Sprintf(`You are an AWS CLI agent that fixes failed commands.

Task: Analyze the failed AWS CLI command and propose a fix.

Output ONLY valid JSON with this schema:
{
  "rewritten_args": ["service", "operation", "args..."],  // null if no rewrite needed
  "pre_commands": [{"args": ["service", "op", "..."], "reason": "why"}],  // commands to run first
  "bindings": {"PLACEHOLDER": "value"},  // any discovered values for placeholders
  "skip": false,  // true if command should be skipped (already done)
  "notes": ["explanation"]
}

Rules:
1. If error contains "already exists" or "duplicate" -> set skip=true
2. If command has unresolved <PLACEHOLDER> tokens -> provide bindings or rewritten_args
3. If error is missing dependency -> provide pre_commands to create it
4. If syntax is wrong -> provide corrected rewritten_args
5. Commands must be AWS CLI subcommands only (NO leading "aws")
6. Do NOT include --profile/--region/--no-cli-pager in args
7. Destructive commands allowed only if destroyer=%t

Failing command:
%q

Error output:
%q

Current bindings (use these to resolve placeholders):
%s

Common fixes:
- <SG_RDS_ID> unresolved but SG_ID exists -> use SG_ID value
- SecurityGroup already exists -> skip=true
- InvalidParameterValue for role ARN -> extract role name from ARN
- Resource not found -> create it in pre_commands
- Permission denied -> add IAM policy in pre_commands
`, destroyer, trimmed, strings.TrimSpace(errorOutput), string(bindingsJSON))
}

// maybeAgenticFix attempts to fix a failed command using AI with exponential backoff.
// This is the main entry point for the agentic failure handling flow.
func maybeAgenticFix(
	ctx context.Context,
	opts ExecOptions,
	args []string,
	awsArgs []string,
	stdinBytes []byte,
	errorOutput string,
	bindings map[string]string,
) (handled bool, err error) {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] agentic fix skipped: no AI provider configured\n")
		return false, nil
	}

	const maxAttempts = 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Exponential backoff: 1s, 2s, 4s
		if attempt > 1 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			_, _ = fmt.Fprintf(opts.Writer, "[maker] agentic retry %d/%d after %v...\n", attempt, maxAttempts, backoff)
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(backoff):
			}
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] asking AI to fix failed command (attempt %d/%d)...\n", attempt, maxAttempts)

		fix, fixErr := getAgenticFix(ctx, opts, args, errorOutput, bindings)
		if fixErr != nil {
			lastErr = fixErr
			_, _ = fmt.Fprintf(opts.Writer, "[maker] AI fix error: %v\n", fixErr)
			continue
		}

		if fix == nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] AI returned no fix\n")
			continue
		}

		// Handle skip
		if fix.Skip {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] AI says: skip this command (already done)\n")
			return true, nil
		}

		// Apply new bindings
		if len(fix.Bindings) > 0 {
			for k, v := range fix.Bindings {
				if strings.TrimSpace(v) == "" {
					continue
				}
				if !bindingLooksCompatible(k, v) {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] AI binding incompatible, ignored: %s = %s\n", k, v)
					continue
				}
				bindings[k] = v
				_, _ = fmt.Fprintf(opts.Writer, "[maker] AI binding: %s = %s\n", k, v)
			}
		}

		// Run pre-commands
		if len(fix.PreCommands) > 0 {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d AI-proposed pre-commands...\n", len(fix.PreCommands))
			for i, cmd := range fix.PreCommands {
				if err := validateCommand(cmd.Args, opts.Destroyer); err != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] pre-command %d rejected: %v\n", i+1, err)
					continue
				}
				cmdArgs := buildAWSExecArgs(cmd.Args, opts, opts.Writer)
				out, runErr := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer)
				if runErr != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] pre-command %d failed: %v\n", i+1, runErr)
					continue
				}
				// Learn from pre-command output
				learnPlanBindings(cmd.Args, out, bindings)
			}
		}

		// Determine which args to use for retry
		retryArgs := args
		if len(fix.RewrittenArgs) > 0 {
			retryArgs = fix.RewrittenArgs
			_, _ = fmt.Fprintf(opts.Writer, "[maker] using AI-rewritten command\n")
		}

		// Re-apply bindings (in case we got new ones)
		retryArgs = applyPlanBindings(retryArgs, bindings)

		// Build final AWS args
		finalArgs := buildAWSExecArgs(retryArgs, opts, opts.Writer)

		_, _ = fmt.Fprintf(opts.Writer, "[maker] retrying: %s\n", formatAWSArgsForLog(finalArgs))

		out, retryErr := runAWSCommandStreaming(ctx, finalArgs, stdinBytes, opts.Writer)
		if retryErr == nil {
			// Success! Learn from output
			learnPlanBindings(retryArgs, out, bindings)
			return true, nil
		}

		// Still failed - update error for next attempt
		errorOutput = out
		_, _ = fmt.Fprintf(opts.Writer, "[maker] retry failed: %v\n", retryErr)
		lastErr = retryErr
	}

	return false, lastErr
}

// getAgenticFix asks the AI for a fix.
func getAgenticFix(ctx context.Context, opts ExecOptions, args []string, errorOutput string, bindings map[string]string) (*agenticFix, error) {
	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	prompt := agenticFixPrompt(opts.Destroyer, args, errorOutput, bindings)

	resp, err := client.AskPrompt(ctx, prompt)
	if err != nil {
		return nil, err
	}

	cleaned := client.CleanJSONResponse(resp)
	var fix agenticFix
	if err := json.Unmarshal([]byte(strings.TrimSpace(cleaned)), &fix); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	// Normalize args
	if len(fix.RewrittenArgs) > 0 {
		fix.RewrittenArgs = normalizeArgs(fix.RewrittenArgs)
	}
	for i := range fix.PreCommands {
		fix.PreCommands[i].Args = normalizeArgs(fix.PreCommands[i].Args)
	}

	return &fix, nil
}
