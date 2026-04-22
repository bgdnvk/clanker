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

// ExecuteRailwayPlan executes a Railway infrastructure plan by shelling out
// to the `railway` CLI. The pattern mirrors ExecuteVercelPlan.
func ExecuteRailwayPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.RailwayAPIToken == "" {
		return fmt.Errorf("missing railway API token")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if err := validateRailwayCommand(args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected after binding: %w", idx+1, err)
		}

		stdinData := cmdSpec.Stdin
		if stdinData != "" && !strings.HasSuffix(stdinData, "\n") {
			stdinData += "\n"
		}

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatRailwayArgsForLog(args))

		out, runErr := runRailwayCommandStreamingWithStdin(ctx, args, stdinData, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("railway command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateRailwayCommand validates that a command is a legitimate railway CLI
// invocation and not an attempt to escape into other tools. Only commands
// whose first token is literally "railway" are allowed — everything else is
// rejected unconditionally.
func validateRailwayCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "railway" {
		return fmt.Errorf("only railway commands are allowed, got: %q", args)
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") ||
			strings.ContainsAny(a, "\n\r") ||
			strings.Contains(a, "`") || strings.Contains(a, "$(") {
			return fmt.Errorf("shell operators are not allowed")
		}

		if !allowDestructive {
			destructiveVerbs := []string{"down", "delete", "remove", "destroy", "rm"}
			for _, verb := range destructiveVerbs {
				if lower == verb {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
		}
	}

	return nil
}

// runRailwayCommandStreamingWithStdin executes a railway CLI command with
// streaming output and optional stdin data.
func runRailwayCommandStreamingWithStdin(ctx context.Context, args []string, stdinData string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("railway")
	if err != nil {
		return "", fmt.Errorf("railway CLI not found in PATH (install from https://docs.railway.com/guides/cli): %w", err)
	}

	// Strip "railway" from args if present (the binary name is already the
	// executable).
	cmdArgs := args
	if len(args) > 0 && strings.ToLower(strings.TrimSpace(args[0])) == "railway" {
		cmdArgs = args[1:]
	}

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)

	// Inject authentication via environment variables. v1 account tokens
	// belong to RAILWAY_API_TOKEN; RAILWAY_TOKEN is project-scoped and NOT
	// what we want here.
	cmd.Env = append(os.Environ(), fmt.Sprintf("RAILWAY_API_TOKEN=%s", opts.RailwayAPIToken))
	if opts.RailwayWorkspaceID != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RAILWAY_WORKSPACE_ID=%s", opts.RailwayWorkspaceID))
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

// isRailwayVariableSetCommand returns true if the args represent a `railway
// variable set` command. These commands accept `KEY=VALUE` positional args,
// so we may want to mask the value in logs.
func isRailwayVariableSetCommand(args []string) bool {
	if len(args) < 3 {
		return false
	}
	lower := make([]string, 0, 3)
	for _, a := range args[:3] {
		lower = append(lower, strings.ToLower(strings.TrimSpace(a)))
	}
	// `railway variable set KEY=VAL` or `railway variables set KEY=VAL`
	if lower[0] != "railway" {
		return false
	}
	if lower[1] != "variable" && lower[1] != "variables" && lower[1] != "var" && lower[1] != "vars" {
		return false
	}
	return lower[2] == "set"
}

// formatRailwayArgsForLog formats command args for logging, masking secret
// values so they don't appear in plan logs.
func formatRailwayArgsForLog(args []string) string {
	if len(args) == 0 {
		return ""
	}

	masked := make([]string, len(args))
	copy(masked, args)

	// `railway variable set KEY=VAL` -> mask the value portion of KEY=VAL.
	if isRailwayVariableSetCommand(masked) && len(masked) >= 4 {
		for i := 3; i < len(masked); i++ {
			if k, _, ok := strings.Cut(masked[i], "="); ok && k != "" {
				masked[i] = k + "=***"
			}
		}
	}

	if strings.ToLower(strings.TrimSpace(masked[0])) == "railway" {
		return strings.Join(masked, " ")
	}
	return "railway " + strings.Join(masked, " ")
}
