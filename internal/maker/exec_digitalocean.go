package maker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
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

	// Import secret-like env vars into bindings so user-data placeholder substitution works.
	// Mirrors AWS executor: clanker-cloud passes user-provided env vars to the CLI process.
	importSecretLikeEnvVarsIntoBindings(bindings)

	// DIGITALOCEAN_ACCESS_TOKEN is needed inside user-data for doctl auth/docker login
	if _, ok := bindings["DIGITALOCEAN_ACCESS_TOKEN"]; !ok {
		bindings["DIGITALOCEAN_ACCESS_TOKEN"] = opts.DigitalOceanAPIToken
	}

	for idx, cmdSpec := range plan.Commands {
		if err := validateDoctlCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+4)
		args = append(args, cmdSpec.Args...)
		// Normalize: strip leading "doctl" if LLM included it (self-heal often does)
		if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "doctl") {
			args = args[1:]
		}
		args = applyPlanBindings(args, bindings)
		args = expandTildeInArgs(args) // doctl doesn't do shell expansion
		if generatedArgs, err := ensureDOSSHImportKeyMaterial(args, opts.Writer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		} else {
			args = generatedArgs
		}
		args = normalizeFirewallRuleFlagsAtExec(args)
		args = stripInvalidICMPPortsAtExec(args)
		args = fixFirewallEmptyAddressAtExec(args)
		if err := validateDOFirewallRulesAtExec(args); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		if err := validateDOUserDataAtExec(args); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		if hasUnresolvedPlaceholders(args) {
			return fmt.Errorf("command %d has unresolved placeholders after substitutions", idx+1)
		}

		// Skip git clone — executor already handles cloning for docker build context
		if len(args) >= 2 && strings.EqualFold(args[0], "git") && strings.EqualFold(args[1], "clone") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] skipping %d/%d: git clone (handled by executor)\n", idx+1, len(plan.Commands))
			continue
		}

		// Docker commands run via docker CLI, not doctl
		if isDockerCommand(args) {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: docker %s\n", idx+1, len(plan.Commands), strings.Join(dockerArgs(args), " "))
			out, runErr := runDockerCommandStreaming(ctx, args, opts, cloneDir, opts.Writer)
			if runErr != nil {
				// "Cannot connect to the Docker daemon" is a local env issue — non-repairable
				if strings.Contains(out, "Cannot connect to the Docker daemon") ||
					strings.Contains(out, "docker daemon running") {
					return fmt.Errorf("docker command %d failed (local-env: Docker Desktop not running): %w", idx+1, runErr)
				}
				return fmt.Errorf("docker command %d failed: %w", idx+1, runErr)
			}
			// Docker build/push output is NOT JSON — learnPlanBindingsFromProduces won't work.
			// Set produces as literal values (e.g. IMAGE_URI = the -t tag value).
			learnDockerProducesLiteral(args, cmdSpec.Produces, bindings)
			learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
			continue
		}

		args = normalizeDoctlOutputFlags(args)
		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: doctl %s\n", idx+1, len(plan.Commands), strings.Join(redactDOCommandArgsForLog(args), " "))

		out, runErr := runDoctlCommandWithRetry(ctx, args, opts, opts.Writer)
		if runErr != nil {
			failure := classifyDOFailure(args, out)
			_, _ = fmt.Fprintf(opts.Writer, "[maker] DO error: category=%s service=%s op=%s\n", failure.Category, failure.Service, failure.Op)

			// Ignorable errors (e.g. "already exists" on create)
			if shouldIgnoreDOFailure(args, failure) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] note: ignoring non-fatal DO error for command %d (%s)\n", idx+1, failure.Category)
				// Error output won't have useful data — recover bindings via GET
				recoverDOBindingsAfterSkip(ctx, args, cmdSpec.Produces, bindings, opts, opts.Writer)
				computeDORuntimeBindings(bindings)
				continue
			}

			return fmt.Errorf("digitalocean command %d failed (%s): %w", idx+1, failure.Category, runErr)
		}

		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
		computeDORuntimeBindings(bindings)
		if err := postCheckDOCommand(args, out); err != nil {
			return fmt.Errorf("digitalocean command %d post-check failed: %w", idx+1, err)
		}
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

	// Allow git clone (executor skips it — cloneRepoForImageBuild handles cloning)
	if first == "git" {
		if len(args) < 2 || !strings.EqualFold(args[1], "clone") {
			return fmt.Errorf("only 'git clone' is allowed, got 'git %s'", strings.Join(args[1:], " "))
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

	// Flags whose values are freeform script/content — exempt from shell operator checks
	scriptFlags := map[string]bool{"--user-data": true}

	// Check for shell operators (skip freeform content args)
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if scriptFlags[strings.ToLower(strings.TrimSpace(a))] {
			skipNext = true // next arg is script content
			continue
		}
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
// Push commands get a 15-min timeout to avoid indefinite hangs (e.g. DOCR storage quota exceeded).
func runDockerCommandStreaming(ctx context.Context, args []string, opts ExecOptions, workDir string, w io.Writer) (string, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("docker not found in PATH: %w", err)
	}

	cmdArgs := dockerArgs(args)

	// Apply a 5-min timeout for docker push — DOCR silently stalls when storage quota is exceeded
	execCtx := ctx
	if len(cmdArgs) > 0 && strings.EqualFold(strings.TrimSpace(cmdArgs[0]), "push") {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, bin, cmdArgs...)
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
		// Detect push timeout — likely DOCR storage quota exceeded
		if execCtx.Err() == context.DeadlineExceeded && len(cmdArgs) > 0 && strings.EqualFold(cmdArgs[0], "push") {
			return out, fmt.Errorf("docker push timed out after 5m (DOCR storage quota may be exceeded — ensure registry uses 'basic' tier or higher): %w", err)
		}
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

// normalizeFirewallRuleFlagsAtExec merges repeated --inbound-rules/--outbound-rules
// into the single doctl form DigitalOcean expects. Repeated flags cause only the
// last rule set to survive, silently dropping SSH/app ports.
func normalizeFirewallRuleFlagsAtExec(args []string) []string {
	if len(args) < 3 {
		return args
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "firewall") {
		return args
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return args
	}

	var inboundVals, outboundVals []string
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		switch {
		case trimmed == "--inbound-rules" && i+1 < len(args):
			if v := strings.TrimSpace(args[i+1]); v != "" {
				inboundVals = append(inboundVals, v)
			}
			i++
		case strings.HasPrefix(trimmed, "--inbound-rules="):
			if v := strings.TrimSpace(strings.TrimPrefix(trimmed, "--inbound-rules=")); v != "" {
				inboundVals = append(inboundVals, v)
			}
		case trimmed == "--outbound-rules" && i+1 < len(args):
			if v := strings.TrimSpace(args[i+1]); v != "" {
				outboundVals = append(outboundVals, v)
			}
			i++
		case strings.HasPrefix(trimmed, "--outbound-rules="):
			if v := strings.TrimSpace(strings.TrimPrefix(trimmed, "--outbound-rules=")); v != "" {
				outboundVals = append(outboundVals, v)
			}
		default:
			cleaned = append(cleaned, args[i])
		}
	}

	insertAt := len(cleaned)
	for i, a := range cleaned {
		if strings.EqualFold(strings.TrimSpace(a), "--output") || strings.HasPrefix(strings.TrimSpace(a), "--output=") {
			insertAt = i
			break
		}
	}
	prefix := append([]string{}, cleaned[:insertAt]...)
	suffix := append([]string{}, cleaned[insertAt:]...)
	if len(inboundVals) > 0 {
		prefix = append(prefix, "--inbound-rules", strings.Join(inboundVals, " "))
	}
	if len(outboundVals) > 0 {
		prefix = append(prefix, "--outbound-rules", strings.Join(outboundVals, " "))
	}
	return append(prefix, suffix...)
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
	if shouldSanitizeDOOutputForLog(args) {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	} else {
		mw := io.MultiWriter(w, &buf)
		cmd.Stdout = mw
		cmd.Stderr = mw
	}

	err = cmd.Run()
	out := buf.String()
	if shouldSanitizeDOOutputForLog(args) && w != nil {
		_, _ = io.WriteString(w, sanitizeDOOutputForLog(out))
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

func shouldSanitizeDOOutputForLog(args []string) bool {
	if len(args) < 3 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "compute") &&
		strings.EqualFold(strings.TrimSpace(args[1]), "ssh-key") &&
		(strings.EqualFold(strings.TrimSpace(args[2]), "list") ||
			strings.EqualFold(strings.TrimSpace(args[2]), "get") ||
			strings.EqualFold(strings.TrimSpace(args[2]), "import"))
}

func sanitizeDOOutputForLog(out string) string {
	publicKeyRe := regexp.MustCompile(`"public_key"\s*:\s*"[^"]*"`)
	return publicKeyRe.ReplaceAllString(out, `"public_key":"<redacted>"`)
}

// ---------------------------------------------------------------------------
// Tilde expansion — doctl/exec.Command don't do shell expansion
// ---------------------------------------------------------------------------

// expandTildeInArgs replaces leading ~ with absolute home dir in all args.
func expandTildeInArgs(args []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return args
	}
	for i, a := range args {
		if strings.HasPrefix(a, "~/") {
			args[i] = filepath.Join(home, a[2:])
		}
	}
	return args
}

// ---------------------------------------------------------------------------
// DO error classification
// ---------------------------------------------------------------------------

// DOFailureCategory classifies a doctl failure
type DOFailureCategory string

const (
	DOFailureUnknown       DOFailureCategory = "unknown"
	DOFailureAlreadyExists DOFailureCategory = "already-exists"
	DOFailureNotFound      DOFailureCategory = "not-found"
	DOFailureRateLimit     DOFailureCategory = "rate-limit"
	DOFailureAuth          DOFailureCategory = "auth"
	DOFailureQuota         DOFailureCategory = "quota"
	DOFailureValidation    DOFailureCategory = "validation"
)

type DOFailure struct {
	Service  string
	Op       string
	Category DOFailureCategory
	Message  string
}

func classifyDOFailure(args []string, output string) DOFailure {
	f := DOFailure{Category: DOFailureUnknown}
	if len(args) >= 1 {
		f.Service = strings.TrimSpace(args[0])
	}
	if len(args) >= 2 {
		f.Op = strings.TrimSpace(args[1])
	}
	msg := strings.TrimSpace(output)
	if len(msg) > 600 {
		msg = msg[:600]
	}
	f.Message = msg

	lower := strings.ToLower(output)

	// Already exists / already in use / already has
	if strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "is already in use") ||
		strings.Contains(lower, "already has a") ||
		strings.Contains(lower, "ssh key already exists") ||
		strings.Contains(lower, "duplicate") {
		f.Category = DOFailureAlreadyExists
		return f
	}

	// Not found
	if strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "could not find") {
		f.Category = DOFailureNotFound
		return f
	}

	// Rate limit
	if strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "429") {
		f.Category = DOFailureRateLimit
		return f
	}

	// Auth
	if strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "unable to authenticate") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "invalid token") {
		f.Category = DOFailureAuth
		return f
	}

	// Quota
	if strings.Contains(lower, "droplet limit") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "limit reached") ||
		strings.Contains(lower, "will exceed your reserved ip limit") ||
		strings.Contains(lower, "exceed your reserved ip limit") {
		f.Category = DOFailureQuota
		return f
	}

	// Validation
	if strings.Contains(lower, "unprocessable") ||
		strings.Contains(lower, "invalid") {
		f.Category = DOFailureValidation
		return f
	}

	return f
}

// shouldIgnoreDOFailure returns true for non-fatal errors on create commands.
func shouldIgnoreDOFailure(args []string, failure DOFailure) bool {
	if failure.Category == DOFailureQuota && len(args) >= 3 && strings.EqualFold(args[0], "compute") && strings.EqualFold(args[1], "reserved-ip") && strings.EqualFold(args[2], "create") {
		return true
	}
	if failure.Category != DOFailureAlreadyExists {
		return false
	}
	// Safe to ignore "already exists" on create/import operations
	lower := strings.ToLower(strings.Join(args, " "))
	return strings.Contains(lower, "create") ||
		strings.Contains(lower, "import")
}

// runDoctlCommandWithRetry wraps runDoctlCommandStreaming with retry for transient errors.
func runDoctlCommandWithRetry(ctx context.Context, args []string, opts ExecOptions, w io.Writer) (string, error) {
	const maxRetries = 3
	var out string
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		out, err = runDoctlCommandStreaming(ctx, args, opts, w)
		if err == nil {
			return out, nil
		}

		failure := classifyDOFailure(args, out)
		if failure.Category != DOFailureRateLimit {
			return out, err // not transient, don't retry
		}

		if attempt < maxRetries {
			backoff := time.Duration(1<<uint(attempt)) * 2 * time.Second // 2s, 4s, 8s
			_, _ = fmt.Fprintf(w, "[maker] rate limited, retrying in %s (attempt %d/%d)\n", backoff, attempt+1, maxRetries)
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Binding recovery & runtime computation
// ---------------------------------------------------------------------------

// recoverDOBindingsAfterSkip fetches existing resource data when a create/import
// is skipped (already exists). Runs the corresponding GET/LIST to populate bindings.
func recoverDOBindingsAfterSkip(ctx context.Context, args []string, produces map[string]string, bindings map[string]string, opts ExecOptions, w io.Writer) {
	if len(args) < 2 {
		return
	}

	lower := strings.ToLower(strings.Join(args, " "))

	var getArgs []string
	switch {
	case strings.HasPrefix(lower, "registry create"):
		getArgs = []string{"registry", "get", "--output", "json"}
	case strings.Contains(lower, "ssh-key import"):
		getArgs = []string{"compute", "ssh-key", "list", "--output", "json"}
	case strings.Contains(lower, "firewall create"):
		getArgs = []string{"compute", "firewall", "list", "--output", "json"}
	default:
		return
	}

	_, _ = fmt.Fprintf(w, "[maker] recovering bindings via: doctl %s\n", strings.Join(getArgs, " "))
	out, err := runDoctlCommandStreaming(ctx, getArgs, opts, w)
	if err != nil {
		_, _ = fmt.Fprintf(w, "[maker] warning: binding recovery failed: %v\n", err)
		return
	}

	// Standard produce extraction (works when autofix has fixed the paths)
	if len(produces) > 0 {
		learnPlanBindingsFromProduces(produces, out, bindings)
	}

	// Direct extraction for registry (produce paths may be hallucinated)
	if strings.HasPrefix(lower, "registry create") {
		extractRegistryBindingsDirect(out, bindings)
		return
	}
	if strings.Contains(lower, "ssh-key import") {
		extractSSHKeyBindingsDirect(args, out, bindings)
		return
	}
	if strings.Contains(lower, "firewall create") {
		extractFirewallBindingsDirect(args, out, bindings)
	}
}

// extractRegistryBindingsDirect parses doctl registry get output and extracts
// REGISTRY_NAME directly, bypassing potentially hallucinated produce paths.
func extractRegistryBindingsDirect(output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}

	var obj any
	if err := json.Unmarshal([]byte(output), &obj); err != nil {
		return
	}

	// Handle array-wrapped response [{ ... }]
	if arr, ok := obj.([]any); ok && len(arr) > 0 {
		obj = arr[0]
	}

	m, ok := obj.(map[string]any)
	if !ok {
		return
	}

	if name, ok := m["name"].(string); ok && name != "" {
		if strings.TrimSpace(bindings["REGISTRY_NAME"]) == "" {
			bindings["REGISTRY_NAME"] = name
		}
	}
}

func extractSSHKeyBindingsDirect(args []string, output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	keyName := ""
	if len(args) >= 4 {
		keyName = strings.TrimSpace(args[3])
	}
	publicKeyPath := expandHomePath(strings.TrimSpace(flagValue(args, "--public-key-file")))
	publicKeyText := ""
	if publicKeyPath != "" {
		if blob, err := os.ReadFile(publicKeyPath); err == nil {
			publicKeyText = strings.TrimSpace(string(blob))
		}
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return
	}
	for _, entry := range entries {
		name := doJSONStringValue(entry["name"])
		pub := doJSONStringValue(entry["public_key"])
		if publicKeyText != "" && pub == publicKeyText {
			if id := doJSONStringValue(entry["id"]); id != "" {
				bindings["SSH_KEY_ID"] = id
			}
			return
		}
		if keyName != "" && name == keyName {
			if id := doJSONStringValue(entry["id"]); id != "" {
				bindings["SSH_KEY_ID"] = id
			}
			return
		}
	}
}

func extractFirewallBindingsDirect(args []string, output string, bindings map[string]string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	firewallName := strings.TrimSpace(flagValue(args, "--name"))
	if firewallName == "" {
		return
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return
	}
	for _, entry := range entries {
		name := doJSONStringValue(entry["name"])
		if name != firewallName {
			continue
		}
		if id := doJSONStringValue(entry["id"]); id != "" {
			bindings["FIREWALL_ID"] = id
		}
		return
	}
}

func doJSONStringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strings.TrimSpace(strconv.FormatFloat(x, 'f', -1, 64))
	case json.Number:
		return strings.TrimSpace(x.String())
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

// computeDORuntimeBindings infers computed bindings that aren't in API output.
func computeDORuntimeBindings(bindings map[string]string) {
	// REGISTRY_ENDPOINT = registry.digitalocean.com/<REGISTRY_NAME>
	if name := strings.TrimSpace(bindings["REGISTRY_NAME"]); name != "" {
		if strings.TrimSpace(bindings["REGISTRY_ENDPOINT"]) == "" {
			bindings["REGISTRY_ENDPOINT"] = "registry.digitalocean.com/" + name
		}
	}
}

// learnDockerProducesLiteral sets produces bindings from docker command args.
// Docker build/push output is NOT JSON so learnPlanBindingsFromProduces won't work.
// Instead, we extract the image tag from the -t flag and set it as a literal binding.
func learnDockerProducesLiteral(args []string, produces map[string]string, bindings map[string]string) {
	// Find the -t tag value from docker build args
	tag := ""
	for i, a := range args {
		if (a == "-t" || a == "--tag") && i+1 < len(args) {
			tag = strings.TrimSpace(args[i+1])
			break
		}
	}
	if tag == "" {
		return
	}

	// Set any IMAGE-related produce to the tag value
	for k := range produces {
		upper := strings.ToUpper(strings.TrimSpace(k))
		if strings.Contains(upper, "IMAGE") {
			if strings.TrimSpace(bindings[k]) == "" {
				bindings[k] = tag
			}
		}
	}

	// Always set IMAGE_URI and IMAGE_TAG as fallback (even without produces)
	// so downstream commands referencing these placeholders work
	if strings.TrimSpace(bindings["IMAGE_URI"]) == "" {
		bindings["IMAGE_URI"] = tag
	}
	if strings.TrimSpace(bindings["IMAGE_TAG"]) == "" {
		bindings["IMAGE_TAG"] = tag
	}
}

func validateDOUserDataAtExec(args []string) error {
	if len(args) < 3 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") ||
		!strings.EqualFold(strings.TrimSpace(args[1]), "droplet") ||
		!strings.EqualFold(strings.TrimSpace(args[2]), "create") {
		return nil
	}

	script := extractDOUserDataArg(args)
	if strings.TrimSpace(script) == "" {
		return nil
	}
	lower := strings.ToLower(script)
	isOpenClaw := strings.Contains(lower, "openclaw") || strings.Contains(lower, "docker-setup.sh")
	if !isOpenClaw {
		return nil
	}
	if strings.Contains(lower, "cloud-init status --wait") {
		return fmt.Errorf("OpenClaw user-data must not run 'cloud-init status --wait' inside cloud-init")
	}
	if strings.Contains(lower, "docker compose build") || strings.Contains(lower, "docker-compose build") {
		return fmt.Errorf("OpenClaw user-data must use 'docker build -t openclaw:local .' instead of 'docker compose build'")
	}
	if !strings.Contains(lower, "openclaw_gateway_token") && !strings.Contains(lower, "openclaw_gateway_password") {
		return fmt.Errorf("OpenClaw user-data is missing OPENCLAW_GATEWAY_TOKEN or OPENCLAW_GATEWAY_PASSWORD in .env")
	}
	if strings.Contains(lower, "digitalocean_access_token=") {
		return fmt.Errorf("OpenClaw user-data must not write DIGITALOCEAN_ACCESS_TOKEN into .env")
	}
	cloneSoftFailRe := regexp.MustCompile(`(?im)^\s*git\s+clone[^\n]*\s*\|\|[^\n]*$`)
	if cloneSoftFailRe.MatchString(script) {
		return fmt.Errorf("OpenClaw user-data must not ignore git clone failure with a shell fallback ('|| ...')")
	}
	setupSoftFailRe := regexp.MustCompile(`(?im)^\s*\./docker-setup\.sh[^\n]*\s*\|\|[^\n]*$`)
	if setupSoftFailRe.MatchString(script) {
		return fmt.Errorf("OpenClaw user-data must not ignore docker-setup.sh failure with a shell fallback ('|| ...')")
	}
	if strings.Contains(lower, "docker compose up -d openclaw-gateway --wait") || strings.Contains(lower, "docker-compose up -d openclaw-gateway --wait") || strings.Contains(lower, "docker compose up -d openclaw-gateway --output") || strings.Contains(lower, "docker-compose up -d openclaw-gateway --output") {
		return fmt.Errorf("OpenClaw user-data must not include outer doctl flags like --wait/--output on the docker compose up line")
	}
	if strings.Contains(lower, "placeholder_replace_me") || strings.Contains(lower, "changeme") || strings.Contains(lower, "replace_me") {
		return fmt.Errorf("OpenClaw user-data contains dummy secret values like placeholder_replace_me/changeme")
	}
	if strings.Contains(lower, "openssl rand") || strings.Contains(lower, "gateway_token=$(") || strings.Contains(lower, "openclaw_gateway_token=${gateway_token}") || strings.Contains(lower, "openclaw_gateway_token=$gateway_token") {
		return fmt.Errorf("OpenClaw user-data must use the provided gateway token, not generate a random one in user-data")
	}
	if !strings.Contains(lower, "anthropic_api_key") && !strings.Contains(lower, "openai_api_key") && !strings.Contains(lower, "gemini_api_key") {
		return fmt.Errorf("OpenClaw user-data is missing all AI provider keys in .env")
	}
	return nil
}

func ensureDOSSHImportKeyMaterial(args []string, w io.Writer) ([]string, error) {
	if len(args) < 3 {
		return args, nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") ||
		!strings.EqualFold(strings.TrimSpace(args[1]), "ssh-key") ||
		!strings.EqualFold(strings.TrimSpace(args[2]), "import") {
		return args, nil
	}
	for i := 0; i < len(args); i++ {
		if strings.TrimSpace(args[i]) != "--public-key-file" || i+1 >= len(args) {
			continue
		}
		pubPath := strings.TrimSpace(args[i+1])
		if pubPath == "" {
			return args, fmt.Errorf("DigitalOcean ssh-key import is missing --public-key-file path")
		}
		pubPath = expandHomePath(pubPath)
		if filepath.Ext(pubPath) != ".pub" {
			pubPath += ".pub"
		}
		args[i+1] = pubPath
		if _, err := os.Stat(pubPath); err == nil {
			return args, nil
		}
		privPath := strings.TrimSuffix(pubPath, ".pub")
		if err := generateLocalSSHKeyPair(privPath); err != nil {
			return args, fmt.Errorf("generate DigitalOcean SSH key pair: %w", err)
		}
		if w != nil {
			_, _ = fmt.Fprintf(w, "[maker] generated dedicated local SSH key pair for DigitalOcean import: %s\n", pubPath)
		}
		return args, nil
	}
	return args, nil
}

func generateLocalSSHKeyPair(privateKeyPath string) error {
	sshDir := filepath.Dir(privateKeyPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}
	privateKeyFile, err := os.OpenFile(privateKeyPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer privateKeyFile.Close()
	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	if err := pem.Encode(privateKeyFile, privateKeyPEM); err != nil {
		return err
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}
	publicKeyPath := privateKeyPath + ".pub"
	if err := os.WriteFile(publicKeyPath, ssh.MarshalAuthorizedKey(publicKey), 0644); err != nil {
		return err
	}
	return nil
}

func expandHomePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func redactDOCommandArgsForLog(args []string) []string {
	if len(args) == 0 {
		return args
	}
	masked := append([]string(nil), args...)
	for i := 0; i < len(masked); i++ {
		trimmed := strings.TrimSpace(masked[i])
		if trimmed == "--user-data" && i+1 < len(masked) {
			masked[i+1] = "<redacted-user-data>"
			i++
			continue
		}
		if strings.HasPrefix(trimmed, "--user-data=") {
			masked[i] = "--user-data=<redacted-user-data>"
		}
	}
	return masked
}

func extractDOUserDataArg(args []string) string {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == "--user-data" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(trimmed, "--user-data=") {
			return strings.TrimPrefix(trimmed, "--user-data=")
		}
	}
	return ""
}

func postCheckDOCommand(args []string, output string) error {
	if len(args) < 3 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") || !strings.EqualFold(strings.TrimSpace(args[1]), "firewall") {
		return nil
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return nil
	}
	expectedInbound := expectedFirewallPortsFromArgs(args, "--inbound-rules", "tcp")
	expectedOutbound := expectedFirewallAllProtocolsFromArgs(args)
	if len(expectedInbound) == 0 && len(expectedOutbound) == 0 {
		return nil
	}
	actual, err := parseDOFirewallOutput(output)
	if err != nil {
		return nil // don't fail if doctl changed output shape; exec success still stands
	}
	for port := range expectedInbound {
		if !actual.InboundTCPPorts[port] {
			return fmt.Errorf("firewall result is missing inbound TCP port %s after create/update", port)
		}
	}
	for proto := range expectedOutbound {
		if !actual.OutboundAllProtocols[proto] {
			return fmt.Errorf("firewall result is missing outbound %s all rule after create/update", proto)
		}
	}
	return nil
}

type doFirewallObserved struct {
	InboundTCPPorts      map[string]bool
	OutboundAllProtocols map[string]bool
}

type doFirewallOutput struct {
	InboundRules  []doFirewallRule `json:"inbound_rules"`
	OutboundRules []doFirewallRule `json:"outbound_rules"`
}

type doFirewallRule struct {
	Protocol string `json:"protocol"`
	Ports    string `json:"ports"`
	Sources  struct {
		Addresses []string `json:"addresses"`
	} `json:"sources"`
	Destinations struct {
		Addresses []string `json:"addresses"`
	} `json:"destinations"`
}

func parseDOFirewallOutput(output string) (doFirewallObserved, error) {
	obs := doFirewallObserved{
		InboundTCPPorts:      map[string]bool{},
		OutboundAllProtocols: map[string]bool{},
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return obs, fmt.Errorf("empty output")
	}
	var single doFirewallOutput
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil && (len(single.InboundRules) > 0 || len(single.OutboundRules) > 0) {
		return observeDOFirewall(single), nil
	}
	var arr []doFirewallOutput
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
		return observeDOFirewall(arr[0]), nil
	}
	return obs, fmt.Errorf("unrecognized firewall output")
}

func observeDOFirewall(fw doFirewallOutput) doFirewallObserved {
	obs := doFirewallObserved{
		InboundTCPPorts:      map[string]bool{},
		OutboundAllProtocols: map[string]bool{},
	}
	for _, rule := range fw.InboundRules {
		if strings.EqualFold(strings.TrimSpace(rule.Protocol), "tcp") {
			if port := strings.TrimSpace(rule.Ports); port != "" {
				obs.InboundTCPPorts[port] = true
			}
		}
	}
	for _, rule := range fw.OutboundRules {
		proto := strings.ToLower(strings.TrimSpace(rule.Protocol))
		ports := strings.ToLower(strings.TrimSpace(rule.Ports))
		if proto != "" && (ports == "all" || ports == "0") {
			obs.OutboundAllProtocols[proto] = true
		}
	}
	return obs
}

func expectedFirewallPortsFromArgs(args []string, flagName string, protocol string) map[string]bool {
	out := map[string]bool{}
	for _, value := range extractFirewallRuleValuesAtExec(args, flagName) {
		for _, rule := range strings.Fields(value) {
			parts := parseFirewallRuleAtExec(rule)
			if !strings.EqualFold(parts["protocol"], protocol) {
				continue
			}
			if port := strings.TrimSpace(parts["ports"]); port != "" {
				out[port] = true
			}
		}
	}
	return out
}

func expectedFirewallAllProtocolsFromArgs(args []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range extractFirewallRuleValuesAtExec(args, "--outbound-rules") {
		for _, rule := range strings.Fields(value) {
			parts := parseFirewallRuleAtExec(rule)
			proto := strings.ToLower(strings.TrimSpace(parts["protocol"]))
			ports := strings.ToLower(strings.TrimSpace(parts["ports"]))
			if proto != "" && (ports == "all" || ports == "0") {
				out[proto] = true
			}
		}
	}
	return out
}

func extractFirewallRuleValuesAtExec(args []string, flagName string) []string {
	var values []string
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == flagName && i+1 < len(args) {
			values = append(values, strings.TrimSpace(args[i+1]))
			i++
			continue
		}
		if strings.HasPrefix(trimmed, flagName+"=") {
			values = append(values, strings.TrimSpace(strings.TrimPrefix(trimmed, flagName+"=")))
		}
	}
	return values
}

func parseFirewallRuleAtExec(rule string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(rule, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// emptyFWAddrExecRe matches "address:" followed by whitespace or end-of-string.
var emptyFWAddrExecRe = regexp.MustCompile(`address:(\s|$)`)

// fixFirewallEmptyAddressAtExec fixes "address:" with no CIDR right before doctl exec.
// Belt-and-suspenders: autofix does this during planning, but LLM repair can reintroduce it.
func fixFirewallEmptyAddressAtExec(args []string) []string {
	if len(args) < 3 {
		return args
	}
	s0 := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	if s0 != "compute" || s1 != "firewall" {
		return args
	}
	for i, arg := range args {
		if strings.Contains(arg, "address:") {
			args[i] = emptyFWAddrExecRe.ReplaceAllString(arg, "address:0.0.0.0/0${1}")
		}
	}
	return args
}

func stripInvalidICMPPortsAtExec(args []string) []string {
	if len(args) < 3 {
		return args
	}
	s0 := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	if s0 != "compute" || s1 != "firewall" {
		return args
	}
	for i, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if !strings.Contains(trimmed, "protocol:icmp") || !strings.Contains(trimmed, "ports:") {
			continue
		}
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:all,", "protocol:icmp,")
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:0,", "protocol:icmp,")
		trimmed = strings.ReplaceAll(trimmed, ",ports:all,address:", ",address:")
		trimmed = strings.ReplaceAll(trimmed, ",ports:0,address:", ",address:")
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:all", "protocol:icmp")
		trimmed = strings.ReplaceAll(trimmed, "protocol:icmp,ports:0", "protocol:icmp")
		args[i] = trimmed
	}
	return args
}

var doFirewallPortSpecRe = regexp.MustCompile(`^(?:all|0|\d+|\d+-\d+)$`)

func validateDOFirewallRulesAtExec(args []string) error {
	if len(args) < 3 {
		return nil
	}
	s0 := strings.ToLower(strings.TrimSpace(args[0]))
	s1 := strings.ToLower(strings.TrimSpace(args[1]))
	if s0 != "compute" || s1 != "firewall" {
		return nil
	}
	verb := strings.ToLower(strings.TrimSpace(args[2]))
	if verb != "create" && verb != "update" {
		return nil
	}
	for _, spec := range []struct {
		flag      string
		direction string
	}{
		{flag: "--inbound-rules", direction: "inbound"},
		{flag: "--outbound-rules", direction: "outbound"},
	} {
		for _, value := range extractFirewallRuleValuesAtExec(args, spec.flag) {
			for idx, rawRule := range strings.Fields(value) {
				parts := parseFirewallRuleAtExec(rawRule)
				if err := validateDOFirewallRuleAtExec(spec.direction, idx+1, rawRule, parts); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateDOFirewallRuleAtExec(direction string, index int, rawRule string, parts map[string]string) error {
	protocol := strings.ToLower(strings.TrimSpace(parts["protocol"]))
	ports := strings.ToLower(strings.TrimSpace(parts["ports"]))
	address := strings.TrimSpace(parts["address"])
	label := fmt.Sprintf("%s rule %d (%s)", direction, index, strings.TrimSpace(rawRule))

	if protocol == "" {
		return fmt.Errorf("firewall %s is missing protocol", label)
	}
	switch protocol {
	case "tcp", "udp":
		if ports == "" {
			return fmt.Errorf("firewall %s is missing ports", label)
		}
		if !doFirewallPortSpecRe.MatchString(ports) {
			return fmt.Errorf("firewall %s has invalid ports value %q", label, ports)
		}
	case "icmp":
		if ports != "" {
			return fmt.Errorf("firewall %s cannot set ports for icmp", label)
		}
	default:
		return fmt.Errorf("firewall %s has unsupported protocol %q", label, protocol)
	}

	if address == "" {
		return fmt.Errorf("firewall %s is missing address", label)
	}
	if _, _, err := net.ParseCIDR(address); err != nil {
		return fmt.Errorf("firewall %s has invalid CIDR %q", label, address)
	}
	return nil
}
