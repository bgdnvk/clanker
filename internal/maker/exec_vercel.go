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
		if err := validateVercelCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatVercelArgsForLog(args))

		out, runErr := runVercelCommandStreaming(ctx, args, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("vercel command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateVercelCommand validates that a command is a legitimate vercel CLI
// invocation and not an attempt to escape into other tools.
func validateVercelCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))

	// Only allow vercel commands
	if first != "vercel" {
		blockedCommands := []string{
			"aws", "gcloud", "az", "kubectl", "helm", "eksctl", "kubeadm",
			"python", "node", "npm", "npx",
			"bash", "sh", "zsh", "fish",
			"terraform", "tofu", "make",
			"wrangler", "cloudflared", "curl",
			"doctl", "hcloud",
		}
		for _, blocked := range blockedCommands {
			if first == blocked || strings.HasPrefix(first, blocked) {
				return fmt.Errorf("non-vercel command is not allowed: %q", args[0])
			}
		}
	}

	// Check for shell operators
	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
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

// runVercelCommandStreaming executes a vercel CLI command with streaming output.
func runVercelCommandStreaming(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
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

// formatVercelArgsForLog formats command args for logging, masking sensitive data.
func formatVercelArgsForLog(args []string) string {
	if len(args) == 0 {
		return ""
	}
	// If the first arg is "vercel", show it as-is; otherwise prefix for clarity.
	if strings.ToLower(strings.TrimSpace(args[0])) == "vercel" {
		return strings.Join(args, " ")
	}
	return "vercel " + strings.Join(args, " ")
}
