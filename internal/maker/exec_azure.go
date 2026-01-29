package maker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func ExecuteAzurePlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateAzCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+10)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)
		args = ensureAzJSONOutput(args, cmdSpec.Produces)
		args = ensureAzSubscription(args, strings.TrimSpace(opts.AzureSubscriptionID))
		args = ensureAzOnlyShowErrors(args)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatAzArgsForLog(args))

		out, runErr := runAzCommandStreaming(ctx, args, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("az command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

func validateAzCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	// Reject obvious non-az plans.
	switch {
	case first == "aws" || first == "gcloud" || first == "kubectl" || first == "helm" || first == "eksctl" || first == "kubeadm":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "python" || strings.HasPrefix(first, "python"):
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "node" || first == "npm" || first == "npx":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "bash" || first == "sh" || first == "zsh" || first == "fish":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "curl" || first == "wget":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	case first == "terraform" || first == "tofu" || first == "make":
		return fmt.Errorf("non-az command is not allowed: %q", args[0])
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
		if allowDestructive {
			continue
		}
		if strings.Contains(lower, "delete") || strings.Contains(lower, "remove") || strings.Contains(lower, "purge") || strings.Contains(lower, "destroy") {
			return fmt.Errorf("destructive verbs are blocked")
		}
	}

	return nil
}

func ensureAzOnlyShowErrors(args []string) []string {
	for i := 0; i < len(args); i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), "--only-show-errors") {
			return args
		}
	}
	return append(append([]string{}, args...), "--only-show-errors")
}

func ensureAzSubscription(args []string, subscriptionID string) []string {
	if subscriptionID == "" {
		return args
	}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		lower := strings.ToLower(a)
		if lower == "--subscription" {
			return args
		}
		if strings.HasPrefix(lower, "--subscription=") {
			return args
		}
	}
	return append(append([]string{}, args...), "--subscription", subscriptionID)
}

func ensureAzJSONOutput(args []string, produces map[string]string) []string {
	if len(produces) == 0 {
		return args
	}
	if hasAzJSONOutput(args) {
		return args
	}
	// Azure CLI supports -o/--output.
	return append(append([]string{}, args...), "--output", "json")
}

func hasAzJSONOutput(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		lower := strings.ToLower(a)
		if lower == "-o" || lower == "--output" {
			if i+1 < len(args) {
				v := strings.ToLower(strings.Trim(strings.TrimSpace(args[i+1]), "\"'"))
				return strings.Contains(v, "json")
			}
			continue
		}
		if strings.HasPrefix(lower, "--output=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "--output=")), "\"'"))
			return strings.Contains(v, "json")
		}
		if strings.HasPrefix(lower, "-o=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "-o=")), "\"'"))
			return strings.Contains(v, "json")
		}
	}
	return false
}

func formatAzArgsForLog(args []string) string {
	out := make([]string, 0, len(args)+1)
	out = append(out, "az")
	out = append(out, args...)
	return strings.Join(out, " ")
}

func runAzCommandStreaming(ctx context.Context, args []string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("az")
	if err != nil {
		return "", fmt.Errorf("az not found in PATH: %w", err)
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
