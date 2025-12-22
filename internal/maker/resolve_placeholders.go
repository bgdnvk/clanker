package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
)

// placeholderResolution holds AI-resolved bindings.
type placeholderResolution struct {
	Bindings map[string]string `json:"bindings"`
	Notes    []string          `json:"notes,omitempty"`
}

// hasUnresolvedPlaceholders checks if args still contain <PLACEHOLDER> tokens.
func hasUnresolvedPlaceholders(args []string) bool {
	for _, a := range args {
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
	bindingsJSON, _ := json.MarshalIndent(bindings, "", "  ")

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

	bindingsJSON, _ := json.MarshalIndent(bindings, "", "  ")

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

	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return args, nil
	}

	unresolved := extractUnresolvedPlaceholders(args)
	if len(unresolved) == 0 {
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
				if strings.TrimSpace(v) != "" {
					bindings[k] = v
					_, _ = fmt.Fprintf(opts.Writer, "[maker] AI resolved: %s = %s\n", k, v)
				}
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
				if strings.TrimSpace(v) != "" {
					bindings[k] = v
					_, _ = fmt.Fprintf(opts.Writer, "[maker] AI discovered: %s = %s\n", k, v)
				}
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
