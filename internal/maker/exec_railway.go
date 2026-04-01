package maker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecuteRailwayPlan executes a Railway deployment or management plan.
func ExecuteRailwayPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if strings.TrimSpace(opts.RailwayAPIToken) == "" && strings.TrimSpace(opts.RailwayToken) == "" {
		return fmt.Errorf("missing railway token (set RAILWAY_API_TOKEN and/or RAILWAY_TOKEN)")
	}

	workDir, cleanup, err := resolveRailwayWorkDir(ctx, plan, opts)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	bindings := make(map[string]string)
	for idx, cmdSpec := range plan.Commands {
		if err := validateRailwayCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args))
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), strings.Join(args, " "))

		out, runErr := runRailwayCommandStreaming(ctx, args, workDir, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("railway command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

func validateRailwayCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	if first != "railway" {
		blockedCommands := []string{
			"aws", "gcloud", "az", "wrangler", "cloudflared", "doctl", "hcloud",
			"kubectl", "helm", "eksctl", "kubeadm",
			"python", "node", "npm", "npx",
			"bash", "sh", "zsh", "fish",
			"terraform", "tofu", "make", "curl",
		}
		for _, blocked := range blockedCommands {
			if first == blocked || strings.HasPrefix(first, blocked) {
				return fmt.Errorf("non-railway command is not allowed: %q", args[0])
			}
		}
		return fmt.Errorf("railway plans must use railway CLI commands only")
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
	}

	if !allowDestructive {
		for _, a := range args[1:] {
			lower := strings.ToLower(strings.TrimSpace(a))
			for _, verb := range []string{"delete", "remove", "down", "unlink"} {
				if lower == verb || strings.Contains(lower, verb) {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
		}
	}

	return nil
}

func runRailwayCommandStreaming(ctx context.Context, args []string, workDir string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("railway")
	if err != nil {
		return "", fmt.Errorf("railway not found in PATH: %w", err)
	}

	cmdArgs := args
	if len(cmdArgs) > 0 && strings.EqualFold(strings.TrimSpace(cmdArgs[0]), "railway") {
		cmdArgs = cmdArgs[1:]
	}

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}
	cmd.Env = os.Environ()
	if strings.TrimSpace(opts.RailwayAPIToken) != "" {
		cmd.Env = append(cmd.Env, "RAILWAY_API_TOKEN="+strings.TrimSpace(opts.RailwayAPIToken))
	}
	if strings.TrimSpace(opts.RailwayToken) != "" {
		cmd.Env = append(cmd.Env, "RAILWAY_TOKEN="+strings.TrimSpace(opts.RailwayToken))
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

func resolveRailwayWorkDir(ctx context.Context, plan *Plan, opts ExecOptions) (string, func(), error) {
	if strings.TrimSpace(opts.WorkDir) != "" {
		return opts.WorkDir, nil, nil
	}

	repoURL := extractRepoURLFromQuestion("")
	if plan != nil {
		repoURL = extractRepoURLFromQuestion(plan.Question)
		if repoURL == "" && len(plan.Notes) > 0 {
			for _, note := range plan.Notes {
				if repoURL = extractRepoURLFromQuestion(note); repoURL != "" {
					break
				}
			}
		}
	}
	if repoURL == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("failed to resolve railway working directory: %w", err)
		}
		return cwd, nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "clanker-railway-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create railway temp dir: %w", err)
	}

	cloneDir := filepath.Join(tmpDir, "repo")
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", repoURL, cloneDir)
	cloneOut, cloneErr := cloneCmd.CombinedOutput()
	if cloneErr != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("failed to clone repo for railway deploy: %w: %s", cloneErr, strings.TrimSpace(string(cloneOut)))
	}

	return cloneDir, func() { _ = os.RemoveAll(tmpDir) }, nil
}
