package maker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ExecuteDigitalOceanPlan runs a plan of doctl commands sequentially.
func ExecuteDigitalOceanPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}

	// pre-flight: check doctl is available
	if _, err := exec.LookPath("doctl"); err != nil {
		return fmt.Errorf("doctl not found in PATH — install via: brew install doctl")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateDoctlCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+6)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)
		args = ensureDoctlJSONOutput(args, cmdSpec.Produces)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		// inject auth token if provided
		doctlArgs := make([]string, 0, len(args)+4)
		doctlArgs = append(doctlArgs, args...)
		if strings.TrimSpace(opts.DOToken) != "" {
			doctlArgs = append(doctlArgs, "--access-token", strings.TrimSpace(opts.DOToken))
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatDoctlArgsForLog(args))

		out, runErr := runDoctlCommandStreaming(ctx, doctlArgs, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("doctl command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateDoctlCommand rejects non-doctl commands and destructive verbs.
func validateDoctlCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	// reject obvious non-doctl commands
	switch {
	case first == "aws" || first == "gcloud" || first == "az" || first == "kubectl" || first == "helm":
		return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
	case first == "python" || strings.HasPrefix(first, "python"):
		return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
	case first == "node" || first == "npm" || first == "npx":
		return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
	case first == "bash" || first == "sh" || first == "zsh" || first == "fish":
		return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
	case first == "curl" || first == "wget":
		return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
	case first == "terraform" || first == "tofu" || first == "make":
		return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
		if allowDestructive {
			continue
		}
		// doctl uses "delete" / "remove" / "destroy" for destructive ops
		if lower == "delete" || lower == "destroy" || lower == "remove" {
			return fmt.Errorf("destructive verbs are blocked")
		}
	}

	return nil
}

// ensureDoctlJSONOutput appends --output json when the command produces bindings.
func ensureDoctlJSONOutput(args []string, produces map[string]string) []string {
	if len(produces) == 0 {
		return args
	}
	if hasDoctlJSONOutput(args) {
		return args
	}
	return append(append([]string{}, args...), "--output", "json")
}

func hasDoctlJSONOutput(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		lower := strings.ToLower(a)
		// --output json (two-arg form)
		if lower == "--output" || lower == "-o" {
			if i+1 < len(args) {
				v := strings.ToLower(strings.Trim(strings.TrimSpace(args[i+1]), "\"'"))
				return strings.Contains(v, "json")
			}
			continue
		}
		// --output=json
		if strings.HasPrefix(lower, "--output=") || strings.HasPrefix(lower, "-o=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.SplitN(a, "=", 2)[1]), "\"'"))
			return strings.Contains(v, "json")
		}
	}
	return false
}

func formatDoctlArgsForLog(args []string) string {
	out := make([]string, 0, len(args)+1)
	out = append(out, "doctl")
	out = append(out, args...)
	return strings.Join(out, " ")
}

func runDoctlCommandStreaming(ctx context.Context, args []string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("doctl")
	if err != nil {
		return "", fmt.Errorf("doctl not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
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
