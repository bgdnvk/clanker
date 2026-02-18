package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
)

// placeholderResolution holds AI-resolved bindings.
type placeholderResolution struct {
	Bindings map[string]string `json:"bindings"`
	Notes    []string          `json:"notes,omitempty"`
}

type placeholderSubagentPlan struct {
	Bindings      map[string]string        `json:"bindings,omitempty"`
	Commands      []placeholderSubagentCmd `json:"commands,omitempty"`
	RewrittenArgs []string                 `json:"rewritten_args,omitempty"`
	Notes         []string                 `json:"notes,omitempty"`
}

type placeholderSubagentCmd struct {
	Bind  string   `json:"bind"`
	Args  []string `json:"args"`
	Parse string   `json:"parse,omitempty"` // "text" (default) or "json_object"
}

// hasUnresolvedPlaceholders checks if args still contain <PLACEHOLDER> tokens.
func hasUnresolvedPlaceholders(args []string) bool {
	for _, a := range args {
		// Special-case: EC2 user-data is intentionally handled by maker code (maybeGenerateEC2UserData).
		// Do not let the placeholder resolver “helpfully” replace <USER_DATA> with a no-op script.
		if strings.Contains(a, "<USER_DATA>") {
			continue
		}
		if planPlaceholderTokenRe.MatchString(a) {
			return true
		}
	}
	return false
}

// extractUnresolvedPlaceholders returns all unresolved placeholders in args.
func extractUnresolvedPlaceholders(args []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, a := range args {
		matches := planPlaceholderTokenRe.FindAllString(a, -1)
		for _, m := range matches {
			if strings.EqualFold(m, "<USER_DATA>") {
				continue
			}
			if !seen[m] {
				seen[m] = true
				result = append(result, m)
			}
		}
	}
	return result
}

// placeholderResolutionPrompt generates the prompt for AI to resolve placeholders.
func placeholderResolutionPrompt(failedArgs []string, unresolvedPlaceholders []string, bindings map[string]string, errorOutput string) string {
	// Format current bindings
	bindingsJSON, _ := json.MarshalIndent(bindingsForLLM(bindings), "", "  ")

	return fmt.Sprintf(`You are an AWS CLI placeholder resolver.

Task: Determine the correct values for unresolved placeholders in an AWS CLI command that failed.

The command has placeholders like <PLACEHOLDER_NAME> that need to be resolved to actual AWS resource IDs.

Constraints:
- Output ONLY valid JSON.
- Schema:
{
  "bindings": {
    "PLACEHOLDER_NAME": "actual-value",
    ...
  },
  "notes": ["optional explanation"]
}
- For each unresolved placeholder, provide the most likely correct value based on:
  1. The command context (what service/operation)
  2. Already-known bindings
  3. Common AWS naming patterns
  4. The error output if it mentions expected values

Unresolved placeholders:
%v

Failed command args:
%q

Error output:
%q

Already-known bindings:
%s

Instructions:
- If a placeholder like <SG_RDS_ID> is needed but you see <SG_ID> in bindings, use that value.
- If <LAMBDA_ARN> is needed, look for any *_ARN binding that matches.
- Security group IDs look like sg-xxxxxxxx
- Subnet IDs look like subnet-xxxxxxxx
- VPC IDs look like vpc-xxxxxxxx
- ARNs follow arn:aws:service:region:account:resource pattern
- If you cannot determine a value, provide your best guess with a note.
- ONLY include bindings for the unresolved placeholders listed above.
`, unresolvedPlaceholders, failedArgs, errorOutput, string(bindingsJSON))
}

// resolveWithLLMDiscovery tries to resolve placeholders by asking LLM to run describe commands.
func resolveWithLLMDiscovery(ctx context.Context, opts ExecOptions, failedArgs []string, unresolvedPlaceholders []string, bindings map[string]string) (*placeholderResolution, error) {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return nil, nil
	}

	bindingsJSON, _ := json.MarshalIndent(bindingsForLLM(bindings), "", "  ")

	prompt := fmt.Sprintf(`You are an AWS resource discovery agent.

Task: Generate AWS CLI commands to discover the actual values for unresolved placeholders.

Unresolved placeholders:
%v

Command that needs these placeholders:
%q

Already-known bindings (use these to query):
%s

Output ONLY valid JSON:
{
  "commands": [
    { "args": ["ec2", "describe-security-groups", "--filters", "Name=vpc-id,Values=<VPC_ID>"], "extract": {"SG_RDS_ID": "$.SecurityGroups[?(@.GroupName=='*rds*')].GroupId"} }
  ]
}

Rules:
- Each command should discover one or more placeholder values.
- Use known bindings (VPC_ID, etc.) in filters.
- The "extract" field uses JSONPath to pull values from the response.
- Focus on describe/list commands only.
- Maximum 3 commands.
`, unresolvedPlaceholders, failedArgs, string(bindingsJSON))

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	resp, err := client.AskPrompt(ctx, prompt)
	if err != nil {
		return nil, err
	}

	type discoveryPlan struct {
		Commands []struct {
			Args    []string          `json:"args"`
			Extract map[string]string `json:"extract"`
		} `json:"commands"`
	}

	cleaned := client.CleanJSONResponse(resp)
	var plan discoveryPlan
	if err := json.Unmarshal([]byte(strings.TrimSpace(cleaned)), &plan); err != nil {
		return nil, err
	}

	// Run discovery commands and extract values
	result := &placeholderResolution{Bindings: make(map[string]string)}

	for _, cmd := range plan.Commands {
		cmdArgs := append(cmd.Args, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer)
		if err != nil {
			continue
		}

		// Parse output and try to extract values
		var obj any
		if json.Unmarshal([]byte(out), &obj) == nil {
			for placeholder, jsonPath := range cmd.Extract {
				if val := extractJSONPath(obj, jsonPath); val != "" {
					result.Bindings[placeholder] = val
				}
			}
		}
	}

	if len(result.Bindings) > 0 {
		return result, nil
	}
	return nil, nil
}

func resolveWithSubagentToolPlan(ctx context.Context, opts ExecOptions, args []string, unresolved []string, bindings map[string]string, errorOutput string) (*placeholderSubagentPlan, error) {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return nil, nil
	}

	need := make([]string, 0, len(unresolved))
	for _, raw := range unresolved {
		n := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"))
		if n != "" {
			need = append(need, n)
		}
	}
	sort.Strings(need)

	publicIP := discoverPublicIP(ctx)
	if publicIP != "" {
		// Provide as a fact; agent can decide to set ADMIN_CIDR=ip/32 etc.
		bindings["PUBLIC_IP"] = publicIP
	}

	bindingsJSON, _ := json.MarshalIndent(bindingsForLLM(bindings), "", "  ")

	prompt := fmt.Sprintf(`You are a placeholder resolution subagent for an AWS CLI execution plan.

Goal: Resolve unresolved <PLACEHOLDER> tokens for the current command.

Unresolved placeholder names:
%v

Current command args:
%q

Error output (may be empty):
%q

Known bindings:
%s

Available tools you can request via JSON plan (allowlisted):
1) Bindings: directly set values for placeholders.
2) Read-only AWS discovery command: provide args like ["ec2","describe-key-pairs","--query","KeyPairs[0].KeyName","--output","text"].
   - Must be READ-ONLY: only operations starting with describe-, list-, get-.
   - Max 4 commands.
   - Provide "bind" to indicate which placeholder name the command output should populate.
3) Optionally rewrite args ("rewritten_args") ONLY if you can remove placeholders safely.

Output ONLY valid JSON with this schema:
{
  "bindings": {"PLACEHOLDER_NAME": "value"},
  "commands": [
    {"bind":"PLACEHOLDER_NAME","args":["ec2","describe-..."],"parse":"text"}
  ],
  "rewritten_args": ["..."],
  "notes": ["..."]
}
`, need, args, errorOutput, string(bindingsJSON))

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	resp, err := client.AskPrompt(ctx, prompt)
	if err != nil {
		return nil, err
	}

	cleaned := client.CleanJSONResponse(resp)
	var plan placeholderSubagentPlan
	if err := json.Unmarshal([]byte(strings.TrimSpace(cleaned)), &plan); err != nil {
		return nil, err
	}

	return &plan, nil
}

func executeSubagentToolPlan(ctx context.Context, opts ExecOptions, plan *placeholderSubagentPlan, unresolved []string, bindings map[string]string) bool {
	if plan == nil || bindings == nil {
		return false
	}
	changed := false

	// Apply direct bindings first.
	for k, v := range plan.Bindings {
		key := strings.TrimSpace(strings.ToUpper(k))
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		if strings.TrimSpace(bindings[key]) != "" {
			continue
		}
		bindings[key] = val
		changed = true
		if opts.Writer != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] subagent bound %s\n", key)
		}
	}

	// Execute allowlisted AWS discovery commands.
	max := 4
	if len(plan.Commands) < max {
		max = len(plan.Commands)
	}
	for i := 0; i < max; i++ {
		cmd := plan.Commands[i]
		bindName := strings.TrimSpace(strings.ToUpper(cmd.Bind))
		if bindName == "" {
			continue
		}
		if strings.TrimSpace(bindings[bindName]) != "" {
			continue
		}
		if len(cmd.Args) < 2 {
			continue
		}
		service := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		op := strings.ToLower(strings.TrimSpace(cmd.Args[1]))
		if service == "" || op == "" {
			continue
		}
		if !(strings.HasPrefix(op, "describe") || strings.HasPrefix(op, "list") || strings.HasPrefix(op, "get")) {
			continue
		}
		// Only allow a small set of AWS services for discovery.
		switch service {
		case "ec2", "iam", "ssm", "elbv2", "ecr", "autoscaling", "cloudfront", "sts":
		default:
			continue
		}

		awsArgs := append([]string{}, cmd.Args...)
		awsArgs = append(awsArgs, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		if opts.Writer != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] subagent discover: %s %s\n", service, op)
		}
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			continue
		}
		parsed := strings.TrimSpace(out)
		if strings.EqualFold(strings.TrimSpace(cmd.Parse), "json_object") {
			var obj map[string]any
			if json.Unmarshal([]byte(out), &obj) != nil {
				continue
			}
			for k, v := range obj {
				kk := strings.TrimSpace(strings.ToUpper(k))
				if kk == "" {
					continue
				}
				if strings.TrimSpace(bindings[kk]) != "" {
					continue
				}
				s, ok := v.(string)
				if !ok {
					continue
				}
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				bindings[kk] = s
				changed = true
				if opts.Writer != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] subagent discovered %s\n", kk)
				}
			}
			continue
		}

		if parsed != "" {
			bindings[bindName] = parsed
			changed = true
			if opts.Writer != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] subagent discovered %s\n", bindName)
			}
		}
	}

	// Optional rewrite if it removes placeholders.
	if len(plan.RewrittenArgs) > 0 {
		if !hasUnresolvedPlaceholders(plan.RewrittenArgs) {
			if opts.Writer != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] subagent rewrote args (placeholders cleared)\n")
			}
			changed = true
		}
	}

	_ = unresolved
	return changed
}

func bindingsForLLM(bindings map[string]string) map[string]string {
	if allowUnsafeLLMBindings() {
		out := make(map[string]string)
		for k, v := range bindings {
			key := strings.TrimSpace(strings.ToUpper(k))
			if key == "" {
				continue
			}
			val := strings.TrimSpace(v)
			if val == "" {
				continue
			}
			if len(val) > 2000 {
				val = val[:2000] + "…"
			}
			out[key] = val
		}
		return out
	}
	return safeBindingsForLLM(bindings)
}

func allowUnsafeLLMBindings() bool {
	// Unsafe-by-default: this subagent is used to make plans execute.
	// Opt OUT by setting CLANKER_SAFE_LLM_BINDINGS=1/true/yes.
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CLANKER_SAFE_LLM_BINDINGS")))
	if v == "1" || v == "true" || v == "yes" || v == "y" {
		return false
	}
	return true
}

func safeBindingsForLLM(bindings map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range bindings {
		key := strings.TrimSpace(strings.ToUpper(k))
		if key == "" {
			continue
		}
		// Do not send secrets or forwarded env secrets to LLM.
		if strings.HasPrefix(key, "ENV_") {
			continue
		}
		if strings.Contains(key, "TOKEN") || strings.Contains(key, "PASSWORD") || strings.Contains(key, "SECRET") || strings.Contains(key, "API_KEY") || strings.Contains(key, "ACCESS_KEY") {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		if len(val) > 500 {
			val = val[:500] + "…"
		}
		out[key] = val
	}
	return out
}

// extractJSONPath is a simple JSONPath extractor for basic paths.
func extractJSONPath(obj any, path string) string {
	// Simple implementation: $.Key1.Key2 or $.Array[0].Key
	path = strings.TrimPrefix(path, "$.")
	parts := strings.Split(path, ".")

	cur := obj
	for _, p := range parts {
		// Handle array index: Items[0]
		if idx := strings.Index(p, "["); idx > 0 {
			key := p[:idx]
			indexPart := p[idx:]

			if m, ok := cur.(map[string]any); ok {
				cur = m[key]
			} else {
				return ""
			}

			// Extract index
			re := regexp.MustCompile(`\[(\d+)\]`)
			matches := re.FindStringSubmatch(indexPart)
			if len(matches) >= 2 {
				var i int
				fmt.Sscanf(matches[1], "%d", &i)
				if arr, ok := cur.([]any); ok && i < len(arr) {
					cur = arr[i]
				} else {
					return ""
				}
			}
		} else {
			if m, ok := cur.(map[string]any); ok {
				cur = m[p]
			} else {
				return ""
			}
		}
	}

	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

// maybeResolvePlaceholdersWithAI attempts to resolve unresolved placeholders using AI.
// It uses exponential backoff and retries up to maxAttempts times.
func maybeResolvePlaceholdersWithAI(ctx context.Context, opts ExecOptions, args []string, bindings map[string]string, errorOutput string) ([]string, error) {
	if !hasUnresolvedPlaceholders(args) {
		return args, nil
	}

	unresolved := extractUnresolvedPlaceholders(args)
	if len(unresolved) == 0 {
		return args, nil
	}

	// Tool-based resolution loop (no LLM required):
	// - If env var NAME is set, bind <NAME>
	// - ADMIN_CIDR: derive public IP and use /32
	// - EC2_KEYPAIR_NAME: discover via ec2 describe-key-pairs
	if autoResolvePlaceholdersWithTools(ctx, opts, unresolved, bindings) {
		resolved := applyPlanBindings(args, bindings)
		if !hasUnresolvedPlaceholders(resolved) {
			return resolved, nil
		}
		args = resolved
		unresolved = extractUnresolvedPlaceholders(args)
	}

	// Subagent loop: ask LLM for a tool plan (allowlisted), execute it, retry bindings.
	if strings.TrimSpace(opts.AIProvider) != "" && strings.TrimSpace(opts.AIAPIKey) != "" {
		plan, planErr := resolveWithSubagentToolPlan(ctx, opts, args, unresolved, bindings, errorOutput)
		if planErr != nil {
			// best-effort
			_, _ = fmt.Fprintf(opts.Writer, "[maker] subagent plan failed: %v\n", planErr)
		} else if plan != nil {
			if executeSubagentToolPlan(ctx, opts, plan, unresolved, bindings) {
				resolved := applyPlanBindings(args, bindings)
				if !hasUnresolvedPlaceholders(resolved) {
					return resolved, nil
				}
				args = resolved
				unresolved = extractUnresolvedPlaceholders(args)
			}
			if len(plan.RewrittenArgs) > 0 {
				// Only accept rewrite if it reduces unresolved placeholders.
				before := len(unresolved)
				after := len(extractUnresolvedPlaceholders(plan.RewrittenArgs))
				if after < before {
					args = plan.RewrittenArgs
					unresolved = extractUnresolvedPlaceholders(args)
					if !hasUnresolvedPlaceholders(args) {
						return args, nil
					}
				}
			}
		}
	}

	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return args, nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[maker] unresolved placeholders detected: %v\n", unresolved)

	const maxAttempts = 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Exponential backoff: 1s, 2s, 4s
		if attempt > 1 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			_, _ = fmt.Fprintf(opts.Writer, "[maker] retrying placeholder resolution (attempt %d/%d) after %v...\n", attempt, maxAttempts, backoff)
			select {
			case <-ctx.Done():
				return args, ctx.Err()
			case <-time.After(backoff):
			}
		}

		// First try: direct LLM inference from context
		resolution, err := inferPlaceholdersFromContext(ctx, opts, args, unresolved, bindings, errorOutput)
		if err != nil {
			lastErr = err
			continue
		}

		if resolution != nil && len(resolution.Bindings) > 0 {
			// Apply new bindings
			for k, v := range resolution.Bindings {
				if strings.TrimSpace(v) == "" {
					continue
				}
				if !bindingLooksCompatible(k, v) {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] AI resolved incompatible binding ignored: %s = %s\n", k, v)
					continue
				}
				bindings[k] = v
				_, _ = fmt.Fprintf(opts.Writer, "[maker] AI resolved: %s = %s\n", k, v)
			}

			// Re-apply bindings to args
			resolved := applyPlanBindings(args, bindings)

			// Check if all resolved
			if !hasUnresolvedPlaceholders(resolved) {
				return resolved, nil
			}

			// Update unresolved for next attempt
			unresolved = extractUnresolvedPlaceholders(resolved)
			args = resolved
		}

		// Second try: LLM-guided discovery (run describe commands)
		discovery, err := resolveWithLLMDiscovery(ctx, opts, args, unresolved, bindings)
		if err != nil {
			lastErr = err
			continue
		}

		if discovery != nil && len(discovery.Bindings) > 0 {
			for k, v := range discovery.Bindings {
				if strings.TrimSpace(v) == "" {
					continue
				}
				if !bindingLooksCompatible(k, v) {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] AI discovered incompatible binding ignored: %s = %s\n", k, v)
					continue
				}
				bindings[k] = v
				_, _ = fmt.Fprintf(opts.Writer, "[maker] AI discovered: %s = %s\n", k, v)
			}

			resolved := applyPlanBindings(args, bindings)
			if !hasUnresolvedPlaceholders(resolved) {
				return resolved, nil
			}
			args = resolved
			unresolved = extractUnresolvedPlaceholders(resolved)
		}
	}

	// Still unresolved after all attempts
	remaining := extractUnresolvedPlaceholders(args)
	if len(remaining) > 0 {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: could not resolve placeholders after %d attempts: %v\n", maxAttempts, remaining)
	}

	if lastErr != nil {
		return args, lastErr
	}
	return args, nil
}

func autoResolvePlaceholdersWithTools(ctx context.Context, opts ExecOptions, unresolvedPlaceholders []string, bindings map[string]string) bool {
	if bindings == nil {
		return false
	}
	changed := false

	need := make(map[string]struct{}, len(unresolvedPlaceholders))
	for _, raw := range unresolvedPlaceholders {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"))
		if name == "" {
			continue
		}
		need[name] = struct{}{}
	}

	// 1) Generic: env var with same name satisfies <NAME>.
	for name := range need {
		if strings.TrimSpace(bindings[name]) != "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			bindings[name] = v
			changed = true
			if opts.Writer != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] placeholder tool: bound %s from env\n", name)
			}
		}
	}

	// 2) ADMIN_CIDR: infer from public IP when missing.
	if _, ok := need["ADMIN_CIDR"]; ok {
		if strings.TrimSpace(bindings["ADMIN_CIDR"]) == "" {
			if ip := discoverPublicIP(ctx); ip != "" {
				bindings["ADMIN_CIDR"] = ip + "/32"
				changed = true
				if opts.Writer != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] placeholder tool: inferred ADMIN_CIDR=%s\n", bindings["ADMIN_CIDR"])
				}
			}
		}
	}

	// 3) EC2_KEYPAIR_NAME: discover via AWS (read-only) when missing.
	if _, ok := need["EC2_KEYPAIR_NAME"]; ok {
		if strings.TrimSpace(bindings["EC2_KEYPAIR_NAME"]) == "" && strings.TrimSpace(opts.Profile) != "" && strings.TrimSpace(opts.Region) != "" {
			name := discoverEC2KeyPairName(ctx, opts)
			if name != "" {
				bindings["EC2_KEYPAIR_NAME"] = name
				changed = true
				if opts.Writer != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] placeholder tool: selected EC2_KEYPAIR_NAME=%s\n", name)
				}
			}
		}
	}

	return changed
}

func discoverPublicIP(ctx context.Context) string {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org?format=text", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	ip := strings.TrimSpace(string(b))
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsLoopback() {
		return ""
	}
	return ip
}

func discoverEC2KeyPairName(ctx context.Context, opts ExecOptions) string {
	// Use AWS CLI discovery (read-only).
	// Note: runAWSCommandStreaming already resolves the aws binary and streams output.
	args := []string{
		"ec2", "describe-key-pairs",
		"--query", "KeyPairs[].KeyName",
		"--output", "json",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if err != nil {
		return ""
	}
	var names []string
	if json.Unmarshal([]byte(out), &names) != nil {
		return ""
	}
	clean := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			clean = append(clean, n)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	sort.Strings(clean)
	return clean[0]
}

// inferPlaceholdersFromContext asks LLM to infer placeholder values from existing bindings.
func inferPlaceholdersFromContext(ctx context.Context, opts ExecOptions, args []string, unresolved []string, bindings map[string]string, errorOutput string) (*placeholderResolution, error) {
	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	prompt := placeholderResolutionPrompt(args, unresolved, bindings, errorOutput)

	resp, err := client.AskPrompt(ctx, prompt)
	if err != nil {
		return nil, err
	}

	cleaned := client.CleanJSONResponse(resp)
	var parsed placeholderResolution
	if err := json.Unmarshal([]byte(strings.TrimSpace(cleaned)), &parsed); err != nil {
		return nil, err
	}

	return &parsed, nil
}
