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

// ExecuteFlyioPlan executes a Fly.io infrastructure plan by shelling out to
// the `flyctl` (or `fly` alias) CLI. The pattern mirrors ExecuteVercelPlan /
// ExecuteHetznerPlan.
//
// Fly differs from Vercel in two notable ways:
//   - flyctl is the binary name, with `fly` as a documented alias. We resolve
//     either at runtime.
//   - `flyctl secrets set KEY=VALUE` exposes the value on the command line and
//     in the process table; we strip it to stdin so the LLM-generated plan
//     never logs or transmits the value through any pipeline that records
//     argv.
func ExecuteFlyioPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.FlyioAPIToken == "" {
		return fmt.Errorf("missing flyio API token")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if err := validateFlyioCommand(args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected after binding: %w", idx+1, err)
		}

		stdinData := cmdSpec.Stdin
		// Auto-detect `flyctl secrets set` commands that embed values in argv
		// and lift those values into stdin so they never leak.
		if stdinData == "" && isFlyioSecretsSetCommand(args) {
			extracted, scrubbed := extractFlyioSecretValues(args)
			if extracted != "" {
				stdinData = extracted
				args = scrubbed
			}
		} else if stdinData != "" && !strings.HasSuffix(stdinData, "\n") {
			stdinData += "\n"
		}

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatFlyioArgsForLog(args))

		out, runErr := runFlyioCommandStreamingWithStdin(ctx, args, stdinData, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("flyio command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateFlyioCommand validates that a command is a legitimate flyctl
// invocation. Only commands whose first token is literally "flyctl" or "fly"
// are allowed; shell operators, raw `auth logout`, and destructive verbs
// without --destroyer are rejected.
func validateFlyioCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	head := strings.ToLower(strings.TrimSpace(args[0]))
	if head != "flyctl" && head != "fly" {
		return fmt.Errorf("only flyctl/fly commands are allowed, got: %q", args)
	}

	// Never permit `flyctl auth logout` from a plan — the user's CLI session
	// must outlive any plan, and a logout would also strand any subsequent
	// commands relying on the token in the env.
	if len(args) >= 3 {
		s1 := strings.ToLower(strings.TrimSpace(args[1]))
		s2 := strings.ToLower(strings.TrimSpace(args[2]))
		if s1 == "auth" && s2 == "logout" {
			return fmt.Errorf("flyctl auth logout is blocked in plans")
		}
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") ||
			strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("shell operators are not allowed")
		}

		// Block destructive verbs unless destroyer mode is enabled. Fly's
		// destructive subcommands are `destroy`, `delete`, `remove`, plus the
		// `--force` flag that bypasses graceful shutdown.
		if !allowDestructive {
			destructiveVerbs := []string{"destroy", "delete", "remove"}
			for _, verb := range destructiveVerbs {
				if lower == verb {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
			if lower == "--force" {
				return fmt.Errorf("--force is blocked outside destroyer mode")
			}
		}
	}

	return nil
}

// runFlyioCommandStreamingWithStdin executes flyctl with streaming output and
// optional stdin data. FLY_API_TOKEN + FLY_ORG are injected via env vars so
// nothing token-shaped is ever on the command line.
func runFlyioCommandStreamingWithStdin(ctx context.Context, args []string, stdinData string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := resolveFlyctlBin()
	if err != nil {
		return "", err
	}

	// Strip the leading "flyctl"/"fly" token — the binary is already the executable.
	cmdArgs := args
	if len(args) > 0 {
		head := strings.ToLower(strings.TrimSpace(args[0]))
		if head == "flyctl" || head == "fly" {
			cmdArgs = args[1:]
		}
	}

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)

	cmd.Env = append(os.Environ(), fmt.Sprintf("FLY_API_TOKEN=%s", opts.FlyioAPIToken))
	if opts.FlyioOrgSlug != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("FLY_ORG=%s", opts.FlyioOrgSlug))
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

// resolveFlyctlBin returns the path to flyctl, trying the `flyctl` binary
// first and falling back to the `fly` alias. The install hint matches the
// documented Fly install commands.
func resolveFlyctlBin() (string, error) {
	if path, err := exec.LookPath("flyctl"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("fly"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("flyctl not found in PATH (install with: brew install flyctl | curl -L https://fly.io/install.sh | sh)")
}

// isFlyioSecretsSetCommand returns true if args represent `flyctl secrets set …`.
func isFlyioSecretsSetCommand(args []string) bool {
	if len(args) < 3 {
		return false
	}
	head := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	s2 := strings.ToLower(strings.TrimSpace(args[2]))
	return (head == "flyctl" || head == "fly") && s1 == "secrets" && s2 == "set"
}

// extractFlyioSecretValues pulls any `KEY=VALUE` positional args off the end
// of a `flyctl secrets set …` command and reformats them as stdin lines so
// the values never appear in argv. Flags (anything that begins with `-`) and
// `--app <name>` pairs are kept on the command line as positionals.
//
// Returns the stdin payload (or "" when nothing was lifted) plus the scrubbed
// args. The stdin uses one KEY=VALUE per line because flyctl accepts both:
//
//	echo "FOO=bar" | flyctl secrets set --app X
//	flyctl secrets set FOO=bar --app X
//
// Lifting also handles the legacy two-arg form `KEY VALUE` (no equals sign)
// by collapsing back to KEY=VALUE on the stdin side. The flyctl CLI does not
// itself support the unjoined form, so we only ever produce the joined form
// on stdin.
func extractFlyioSecretValues(args []string) (string, []string) {
	if len(args) < 3 {
		return "", args
	}
	var kept []string
	var values []string

	// Always preserve the first three tokens (flyctl secrets set).
	kept = append(kept, args[0], args[1], args[2])

	i := 3
	for i < len(args) {
		a := args[i]
		// Flags pass through, including their value pair (`--app foo`).
		if strings.HasPrefix(a, "-") {
			kept = append(kept, a)
			// `--flag=value` is self-contained. `--flag value` consumes one more.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				kept = append(kept, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}

		// KEY=VALUE → lift to stdin.
		if eq := strings.Index(a, "="); eq > 0 {
			values = append(values, a)
			i++
			continue
		}

		// Bare KEY (legacy two-positional shape): peek next token, treat as VALUE.
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.Contains(args[i+1], "=") {
			values = append(values, a+"="+args[i+1])
			i += 2
			continue
		}

		// Otherwise pass through — probably a flag-shaped positional we don't recognize.
		kept = append(kept, a)
		i++
	}

	if len(values) == 0 {
		return "", args
	}
	return strings.Join(values, "\n") + "\n", kept
}

// formatFlyioArgsForLog formats command args for logging. Any `KEY=VALUE`
// positional under `secrets set` is masked.
func formatFlyioArgsForLog(args []string) string {
	if len(args) == 0 {
		return ""
	}

	masked := make([]string, len(args))
	copy(masked, args)

	if isFlyioSecretsSetCommand(masked) {
		for i := 3; i < len(masked); i++ {
			if eq := strings.Index(masked[i], "="); eq > 0 {
				masked[i] = masked[i][:eq] + "=***"
			}
		}
	}

	if len(masked) > 0 {
		head := strings.ToLower(strings.TrimSpace(masked[0]))
		if head == "flyctl" || head == "fly" {
			return strings.Join(masked, " ")
		}
	}
	return "flyctl " + strings.Join(masked, " ")
}
