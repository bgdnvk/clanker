package maker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func ExecuteGCPPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateGCloudCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+6)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)
		args = ensureGCloudJSONFormat(args, cmdSpec.Produces)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		gcloudArgs := make([]string, 0, len(args)+6)
		gcloudArgs = append(gcloudArgs, args...)
		if strings.TrimSpace(opts.GCPProject) != "" {
			gcloudArgs = append(gcloudArgs, "--project", strings.TrimSpace(opts.GCPProject))
		}
		gcloudArgs = append(gcloudArgs, "--quiet")

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatGCloudArgsForLog(gcloudArgs))

		out, runErr := runGCloudCommandStreaming(ctx, gcloudArgs, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("gcloud command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

func validateGCloudCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	// Reject obvious non-gcloud plans (we always execute via gcloud binary).
	switch {
	case first == "aws" || first == "kubectl" || first == "helm" || first == "eksctl" || first == "kubeadm":
		return fmt.Errorf("non-gcloud command is not allowed: %q", args[0])
	case first == "python" || strings.HasPrefix(first, "python"):
		return fmt.Errorf("non-gcloud command is not allowed: %q", args[0])
	case first == "node" || first == "npm" || first == "npx":
		return fmt.Errorf("non-gcloud command is not allowed: %q", args[0])
	case first == "bash" || first == "sh" || first == "zsh" || first == "fish":
		return fmt.Errorf("non-gcloud command is not allowed: %q", args[0])
	case first == "curl" || first == "wget":
		return fmt.Errorf("non-gcloud command is not allowed: %q", args[0])
	case first == "terraform" || first == "tofu" || first == "make":
		return fmt.Errorf("non-gcloud command is not allowed: %q", args[0])
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
		if allowDestructive {
			continue
		}
		// gcloud deletion usually uses "delete"; keep this conservative.
		if lower == "delete" || strings.Contains(lower, " delete") || strings.Contains(lower, "delete-") || strings.Contains(lower, "delete") || strings.Contains(lower, "remove") || strings.Contains(lower, "destroy") {
			return fmt.Errorf("destructive verbs are blocked")
		}
	}

	return nil
}

func ensureGCloudJSONFormat(args []string, produces map[string]string) []string {
	if len(produces) == 0 {
		return args
	}
	if hasGCloudJSONFormat(args) {
		return args
	}
	// gcloud supports a global --format flag on most commands.
	return append(append([]string{}, args...), "--format=json")
}

func hasGCloudJSONFormat(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		lower := strings.ToLower(a)
		if lower == "--format" {
			if i+1 < len(args) {
				v := strings.ToLower(strings.Trim(strings.TrimSpace(args[i+1]), "\"'"))
				return strings.Contains(v, "json")
			}
			continue
		}
		if strings.HasPrefix(lower, "--format=") {
			v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "--format=")), "\"'"))
			return strings.Contains(v, "json")
		}
	}
	return false
}

func formatGCloudArgsForLog(args []string) string {
	out := make([]string, 0, len(args)+1)
	out = append(out, "gcloud")
	out = append(out, args...)
	return strings.Join(out, " ")
}

func runGCloudCommandStreaming(ctx context.Context, args []string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("gcloud")
	if err != nil {
		return "", fmt.Errorf("gcloud not found in PATH: %w", err)
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
