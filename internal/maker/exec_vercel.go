package maker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ExecuteVercelPlan executes a Vercel infrastructure plan by shelling out to
// the `vercel` CLI. The pattern mirrors ExecuteCloudflarePlan / ExecuteHetznerPlan.
func ExecuteVercelPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.VercelAPIToken == "" {
		return fmt.Errorf("missing vercel API token")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if err := validateVercelCommand(args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected after binding: %w", idx+1, err)
		}

		stdinData := cmdSpec.Stdin
		// Auto-detect env add commands that need stdin piping even
		// when the plan was generated without the stdin field.
		if stdinData == "" && isVercelEnvAddCommand(args) && len(args) >= 5 {
			// Legacy format: ["vercel", "env", "add", KEY, VALUE, ...]
			// Extract the value and rewrite args to remove it.
			stdinData = args[4] + "\n"
			args = append(args[:4], args[5:]...)
		} else if stdinData != "" && !strings.HasSuffix(stdinData, "\n") {
			stdinData += "\n"
		}

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatVercelArgsForLog(args))

		out, runErr := runVercelCommandStreamingWithStdin(ctx, args, stdinData, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("vercel command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateVercelCommand validates that a command is a legitimate vercel CLI
// invocation and not an attempt to escape into other tools. Only commands
// whose first token is literally "vercel" are allowed — everything else is
// rejected unconditionally.
func validateVercelCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "vercel" {
		return fmt.Errorf("only vercel commands are allowed, got: %q", args)
	}

	// Check for shell operators and destructive verbs.
	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") ||
			strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("shell operators are not allowed")
		}

		// Block destructive operations unless destroyer mode is enabled
		if !allowDestructive {
			destructiveVerbs := []string{"delete", "remove", "destroy", "rm"}
			for _, verb := range destructiveVerbs {
				if lower == verb {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
		}
	}

	return nil
}

// runVercelCommandStreamingWithStdin executes a vercel CLI command with
// streaming output and optional stdin data.
func runVercelCommandStreamingWithStdin(ctx context.Context, args []string, stdinData string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("vercel")
	if err != nil {
		return "", fmt.Errorf("vercel not found in PATH (install with: npm i -g vercel): %w", err)
	}

	// Strip "vercel" from args if present (the binary name is already the executable)
	cmdArgs := args
	if len(args) > 0 && strings.ToLower(strings.TrimSpace(args[0])) == "vercel" {
		cmdArgs = args[1:]
	}

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)

	// Inject authentication via environment variables
	cmd.Env = append(os.Environ(), fmt.Sprintf("VERCEL_TOKEN=%s", opts.VercelAPIToken))
	if opts.VercelTeamID != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("VERCEL_ORG_ID=%s", opts.VercelTeamID))
	}

	if stdinData != "" {
		cmd.Stdin = strings.NewReader(stdinData)
	}

	var buf bytes.Buffer
	mw := io.MultiWriter(w, &buf)
	cmd.Stdout = mw
	cmd.Stderr = mw

	err = cmd.Run()
	out := buf.String()
	if err != nil {
		return out, err
	}
	return out, nil
}

// isVercelEnvAddCommand returns true if the args represent a `vercel env add` command.
func isVercelEnvAddCommand(args []string) bool {
	if len(args) < 4 {
		return false
	}
	lower := make([]string, 0, 3)
	for _, a := range args[:3] {
		lower = append(lower, strings.ToLower(strings.TrimSpace(a)))
	}
	return lower[0] == "vercel" && lower[1] == "env" && lower[2] == "add"
}

// formatVercelArgsForLog formats command args for logging, masking env variable
// values and other sensitive data so they don't appear in plan logs.
func formatVercelArgsForLog(args []string) string {
	if len(args) == 0 {
		return ""
	}

	// Mask: for `env add KEY VALUE ...` never log the value.
	// The value is piped via stdin now, but legacy plans might still have it
	// as a positional arg.
	masked := make([]string, len(args))
	copy(masked, args)
	if isVercelEnvAddCommand(masked) && len(masked) >= 5 {
		masked[4] = "***"
	}

	if strings.ToLower(strings.TrimSpace(masked[0])) == "vercel" {
		return strings.Join(masked, " ")
	}
	return "vercel " + strings.Join(masked, " ")
}
