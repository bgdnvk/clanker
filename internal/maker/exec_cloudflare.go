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

// ExecuteCloudflarePlan executes a Cloudflare infrastructure plan
func ExecuteCloudflarePlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}
	if opts.CloudflareAPIToken == "" {
		return fmt.Errorf("missing cloudflare API token")
	}

	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateCloudflareCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+6)
		args = append(args, cmdSpec.Args...)
		args = applyPlanBindings(args, bindings)

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		// Determine the tool type from the first argument
		tool := detectCloudflareTool(args)

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatCloudflareArgsForLog(tool, args))

		out, runErr := runCloudflareCommand(ctx, tool, args, opts, opts.Writer)
		if runErr != nil {
			return fmt.Errorf("cloudflare command %d failed: %w", idx+1, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
	}

	return nil
}

// detectCloudflareTool determines which Cloudflare tool to use based on the command
func detectCloudflareTool(args []string) string {
	if len(args) == 0 {
		return "api"
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))
	switch first {
	case "wrangler":
		return "wrangler"
	case "cloudflared":
		return "cloudflared"
	default:
		return "api"
	}
}

// validateCloudflareCommand validates a Cloudflare command
func validateCloudflareCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	first := strings.ToLower(strings.TrimSpace(args[0]))

	// Reject non-Cloudflare commands
	blockedCommands := []string{
		"aws", "gcloud", "kubectl", "helm", "eksctl", "kubeadm",
		"python", "node", "npm", "npx",
		"bash", "sh", "zsh", "fish",
		"terraform", "tofu", "make",
	}

	for _, blocked := range blockedCommands {
		if first == blocked || strings.HasPrefix(first, blocked) {
			return fmt.Errorf("non-cloudflare command is not allowed: %q", args[0])
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
			destructiveVerbs := []string{"delete", "remove", "destroy", "purge"}
			for _, verb := range destructiveVerbs {
				if strings.Contains(lower, verb) {
					return fmt.Errorf("destructive verbs are blocked (use --destroyer to allow)")
				}
			}
		}
	}

	return nil
}

// runCloudflareCommand executes a Cloudflare command using the appropriate tool
func runCloudflareCommand(ctx context.Context, tool string, args []string, opts ExecOptions, w io.Writer) (string, error) {
	switch tool {
	case "wrangler":
		return runWranglerCommand(ctx, args[1:], opts, w) // Skip "wrangler" from args
	case "cloudflared":
		return runCloudflaredCommand(ctx, args[1:], opts, w) // Skip "cloudflared" from args
	case "api":
		return runCloudflareAPICommand(ctx, args, opts, w)
	default:
		return "", fmt.Errorf("unknown cloudflare tool: %s", tool)
	}
}

// runWranglerCommand executes a wrangler CLI command
func runWranglerCommand(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("wrangler")
	if err != nil {
		return "", fmt.Errorf("wrangler not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, args...)

	// Set environment variables for authentication
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CLOUDFLARE_API_TOKEN=%s", opts.CloudflareAPIToken),
	)
	if opts.CloudflareAccountID != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("CLOUDFLARE_ACCOUNT_ID=%s", opts.CloudflareAccountID))
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

// runCloudflaredCommand executes a cloudflared CLI command
func runCloudflaredCommand(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return "", fmt.Errorf("cloudflared not found in PATH: %w", err)
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

// runCloudflareAPICommand executes a Cloudflare API command via curl
func runCloudflareAPICommand(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	// Parse API command: METHOD ENDPOINT [BODY]
	if len(args) < 2 {
		return "", fmt.Errorf("API command requires at least METHOD and ENDPOINT")
	}

	method := strings.ToUpper(args[0])
	endpoint := args[1]
	body := ""
	if len(args) > 2 {
		body = strings.Join(args[2:], " ")
	}

	curlArgs := []string{
		"-s",
		"-X", method,
		fmt.Sprintf("https://api.cloudflare.com/client/v4%s", endpoint),
		"-H", fmt.Sprintf("Authorization: Bearer %s", opts.CloudflareAPIToken),
		"-H", "Content-Type: application/json",
	}

	if body != "" {
		curlArgs = append(curlArgs, "-d", body)
	}

	bin, err := exec.LookPath("curl")
	if err != nil {
		return "", fmt.Errorf("curl not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, curlArgs...)

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

// formatCloudflareArgsForLog formats command args for logging
func formatCloudflareArgsForLog(tool string, args []string) string {
	switch tool {
	case "wrangler":
		return "wrangler " + strings.Join(args[1:], " ")
	case "cloudflared":
		return "cloudflared " + strings.Join(args[1:], " ")
	case "api":
		if len(args) >= 2 {
			return fmt.Sprintf("curl -X %s https://api.cloudflare.com/client/v4%s", args[0], args[1])
		}
		return strings.Join(args, " ")
	default:
		return strings.Join(args, " ")
	}
}
