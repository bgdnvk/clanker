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

// ExecuteHetznerPlan executes a Hetzner Cloud infrastructure plan
func ExecuteHetznerPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.HetznerAPIToken == "" {
		return fmt.Errorf("missing hetzner API token")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateHcloudCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: hcloud %s\n", idx+1, len(plan.Commands), strings.Join(args[1:], " "))

		out, runErr := runHcloudCommandStreaming(ctx, args, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("hetzner command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateHcloudCommand validates an hcloud command
func validateHcloudCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))

	// Only allow hcloud commands
	if first != "hcloud" {
		blockedCommands := []string{
			"aws", "gcloud", "az", "kubectl", "helm", "eksctl", "kubeadm",
			"python", "node", "npm", "npx",
			"bash", "sh", "zsh", "fish",
			"terraform", "tofu", "make",
			"wrangler", "cloudflared", "curl",
			"doctl",
		}

		for _, blocked := range blockedCommands {
			if first == blocked || strings.HasPrefix(first, blocked) {
				return fmt.Errorf("non-hcloud command is not allowed: %q", args[0])
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
			destructiveVerbs := []string{"delete", "remove", "destroy"}
			for _, verb := range destructiveVerbs {
				if strings.Contains(lower, verb) {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
		}
	}

	return nil
}

// runHcloudCommandStreaming executes an hcloud command with streaming output
func runHcloudCommandStreaming(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("hcloud")
	if err != nil {
		return "", fmt.Errorf("hcloud not found in PATH: %w", err)
	}

	// Strip "hcloud" from args if present
	cmdArgs := args
	if len(args) > 0 && strings.ToLower(strings.TrimSpace(args[0])) == "hcloud" {
		cmdArgs = args[1:]
	}

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	cmd.Env = append(os.Environ(), "HCLOUD_TOKEN="+opts.HetznerAPIToken)

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
