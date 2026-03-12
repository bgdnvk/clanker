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

// ExecuteDigitalOceanPlan executes a Digital Ocean infrastructure plan
func ExecuteDigitalOceanPlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.DigitalOceanAPIToken == "" {
		return fmt.Errorf("missing digitalocean API token")
	}

	// Clone the repo if any step is a docker build — docker needs a build context
	var cloneDir string
	if planHasDockerBuild(plan) {
		repoURL := extractRepoURLFromQuestion(plan.Question)
		if repoURL != "" {
			path, cleanup, err := cloneRepoForImageBuild(ctx, repoURL)
			if err != nil {
				return fmt.Errorf("clone for docker build: %w", err)
			}
			defer cleanup()
			cloneDir = path
			fmt.Fprintf(opts.Writer, "[maker] cloned %s for docker build context\n", repoURL)
		}
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateDoctlCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		// Docker commands run via docker CLI, not doctl
		if isDockerCommand(args) {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: docker %s\n", idx+1, len(plan.Commands), strings.Join(dockerArgs(args), " "))
			out, runErr := runDockerCommandStreaming(ctx, args, opts, cloneDir, opts.Writer)
			if runErr != nil {
				return fmt.Errorf("docker command %d failed: %w", idx+1, runErr)
			}
			learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
			continue
		}

		args = normalizeDoctlOutputFlags(args)
		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: doctl %s\n", idx+1, len(plan.Commands), strings.Join(args[1:], " "))

		out, runErr := runDoctlCommandStreaming(ctx, args, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("digitalocean command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// validateDoctlCommand validates a doctl or docker command
func validateDoctlCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))

	// Allow docker build/push alongside doctl
	if first == "docker" {
		if len(args) < 2 {
			return fmt.Errorf("docker command missing subcommand")
		}
		sub := strings.ToLower(strings.TrimSpace(args[1]))
		allowed := map[string]bool{"build": true, "push": true, "tag": true, "login": true}
		if !allowed[sub] {
			return fmt.Errorf("docker subcommand %q is not allowed (only build/push/tag/login)", sub)
		}
		return nil
	}

	// Only allow doctl commands
	if first != "doctl" {
		blockedCommands := []string{
			"aws", "gcloud", "az", "kubectl", "helm", "eksctl", "kubeadm",
			"python", "node", "npm", "npx",
			"bash", "sh", "zsh", "fish",
			"terraform", "tofu", "make",
			"wrangler", "cloudflared", "curl",
		}

		for _, blocked := range blockedCommands {
			if first == blocked || strings.HasPrefix(first, blocked) {
				return fmt.Errorf("non-doctl command is not allowed: %q", args[0])
			}
		}

		// If it doesn't start with "doctl" but isn't a blocked command,
		// treat it as a doctl subcommand (normalize)
	}

	// Check for shell operators
	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}

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

// isDockerCommand returns true if args represent a docker CLI command
func isDockerCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return strings.ToLower(strings.TrimSpace(args[0])) == "docker"
}

// dockerArgs strips the leading "docker" from args
func dockerArgs(args []string) []string {
	if len(args) > 1 && strings.ToLower(strings.TrimSpace(args[0])) == "docker" {
		return args[1:]
	}
	return args
}

// runDockerCommandStreaming executes a docker CLI command with streaming output.
// workDir is set as cmd.Dir for build commands so the "." context resolves to the cloned repo.
func runDockerCommandStreaming(ctx context.Context, args []string, opts ExecOptions, workDir string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("docker not found in PATH: %w", err)
	}

	cmdArgs := dockerArgs(args)
	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	cmd.Env = os.Environ()

	// Set working dir for build/tag — the "." build context needs to point at the repo
	if workDir != "" && isDockerCommand(args) {
		dArgs := dockerArgs(args)
		if len(dArgs) > 0 {
			sub := strings.ToLower(strings.TrimSpace(dArgs[0]))
			if sub == "build" || sub == "tag" {
				cmd.Dir = workDir
			}
		}
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

// planHasDockerBuild returns true if any command in the plan is a docker build
func planHasDockerBuild(plan *Plan) bool {
	for _, cmd := range plan.Commands {
		if isDockerCommand(cmd.Args) {
			dArgs := dockerArgs(cmd.Args)
			if len(dArgs) > 0 && strings.EqualFold(dArgs[0], "build") {
				return true
			}
		}
	}
	return false
}

// normalizeDoctlOutputFlags rewrites --format json → --output json.
// doctl uses --format for column selection (e.g. --format ID,Name) and
// --output for format type (json/text). LLMs sometimes mix them up.
func normalizeDoctlOutputFlags(args []string) []string {
	for i := 0; i < len(args)-1; i++ {
		if strings.EqualFold(strings.TrimSpace(args[i]), "--format") &&
			strings.EqualFold(strings.TrimSpace(args[i+1]), "json") {
			args[i] = "--output"
		}
	}
	return args
}

// runDoctlCommandStreaming executes a doctl command with streaming output
func runDoctlCommandStreaming(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("doctl")
	if err != nil {
		return "", fmt.Errorf("doctl not found in PATH: %w", err)
	}

	// Strip "doctl" from args if present
	cmdArgs := args
	if len(args) > 0 && strings.ToLower(strings.TrimSpace(args[0])) == "doctl" {
		cmdArgs = args[1:]
	}

	// Inject access token
	fullArgs := append([]string{"--access-token", opts.DigitalOceanAPIToken}, cmdArgs...)

	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	cmd.Env = os.Environ()

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
