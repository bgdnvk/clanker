package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// PlanSkeleton is the high-level structure of a deployment plan.
// Generated in a single LLM call — just service/operation pairs, no full args.
type PlanSkeleton struct {
	Steps []SkeletonStep `json:"steps"`
	Notes []string       `json:"notes,omitempty"`
}

// SkeletonStep is one step in the skeleton — intentionally minimal.
type SkeletonStep struct {
	Service   string   `json:"service"`              // e.g. "iam", "ec2", "elbv2", "ssm", "secretsmanager"
	Operation string   `json:"operation"`            // e.g. "create-role", "run-instances"
	Reason    string   `json:"reason"`               // why this step exists
	Produces  []string `json:"produces,omitempty"`   // placeholder names this step outputs (e.g. ["ROLE_ARN"])
	DependsOn []string `json:"depends_on,omitempty"` // placeholder names required from earlier steps
}

// GeneratePlanSkeleton asks the LLM to produce only the plan structure.
// No CLI args, no JSON payloads — just the ordered list of service/operation pairs
// with placeholder dependency wiring.
func GeneratePlanSkeleton(
	ctx context.Context,
	ask AskFunc,
	clean CleanFunc,
	provider string,
	enrichedPrompt string,
	requiredLaunchOps []string,
	logf func(string, ...any),
) (*PlanSkeleton, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	prompt := buildSkeletonPrompt(provider, enrichedPrompt, requiredLaunchOps)
	resp, err := ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("skeleton generation failed: %w", err)
	}

	cleaned := strings.TrimSpace(clean(resp))
	skeleton, err := parseSkeletonResponse(cleaned)
	if err != nil {
		// One retry with format hint
		logf("[deploy] skeleton parse failed (%v), retrying with format hint", err)
		prompt2 := prompt + "\n\nIMPORTANT: Your previous response was not valid JSON. Return ONLY the JSON object, no markdown fences, no prose."
		resp2, err2 := ask(ctx, prompt2)
		if err2 != nil {
			return nil, fmt.Errorf("skeleton retry failed: %w", err2)
		}
		cleaned2 := strings.TrimSpace(clean(resp2))
		skeleton, err = parseSkeletonResponse(cleaned2)
		if err != nil {
			return nil, fmt.Errorf("skeleton unparseable after retry: %w", err)
		}
	}

	// Validate the skeleton
	if err := validateSkeleton(skeleton, requiredLaunchOps); err != nil {
		logf("[deploy] skeleton validation warning: %v", err)
		// Don't fail for minor issues — harden downstream
	}

	// Fast-fail: a skeleton with fewer than 2 steps is not viable for any
	// real deploy. Fall back to paged generation immediately instead of
	// wasting LLM calls on hydration and validation.
	if len(skeleton.Steps) < 2 {
		return nil, fmt.Errorf("skeleton too small (%d steps); falling back to paged generation", len(skeleton.Steps))
	}

	// Critical check: missing required launch ops means the skeleton is fundamentally incomplete.
	// This MUST cause a fallback to paged plan generation.
	if missingOps := checkMissingLaunchOps(skeleton, requiredLaunchOps); len(missingOps) > 0 {
		return nil, fmt.Errorf("skeleton missing required launch operation(s): %s", strings.Join(missingOps, ", "))
	}

	// Apply topological sort to ensure correct ordering based on produces/depends_on.
	// This provides algorithmic enforcement even if LLM generated out-of-order steps.
	skeleton, sortErr := TopologicalSortSkeleton(skeleton)
	if sortErr != nil {
		logf("[deploy] warning: topological sort failed: %v, using LLM order", sortErr)
	}

	logf("[deploy] skeleton: %d steps, %d unique placeholders", len(skeleton.Steps), countUniquePlaceholders(skeleton))
	return skeleton, nil
}

// HydrateSkeleton fills in the CLI args for each skeleton step via per-step LLM calls.
// Each call gets: the full skeleton (for context), the specific step to detail,
// and all commands generated so far (for consistency).
func HydrateSkeleton(
	ctx context.Context,
	ask AskFunc,
	clean CleanFunc,
	provider string,
	enrichedPrompt string,
	skeleton *PlanSkeleton,
	logf func(string, ...any),
) (*maker.Plan, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	plan := &maker.Plan{
		Version:  maker.CurrentPlanVersion,
		Provider: provider,
		Commands: make([]maker.Command, 0, len(skeleton.Steps)),
	}

	// Build the skeleton summary once (shared across all detail calls)
	skeletonSummary := buildSkeletonSummary(skeleton)

	// Batch related steps to reduce API calls.
	// Group consecutive steps that don't depend on each other.
	batches := batchSkeletonSteps(skeleton)
	logf("[deploy] hydrating %d steps in %d batch(es)", len(skeleton.Steps), len(batches))

	for bi, batch := range batches {
		// Build the "already generated" context
		var generatedSoFar []string
		for i, cmd := range plan.Commands {
			generatedSoFar = append(generatedSoFar, fmt.Sprintf("%d) %s %s", i+1,
				strings.Join(cmd.Args[:min(2, len(cmd.Args))], " "),
				formatProducesShort(cmd.Produces)))
		}

		prompt := buildHydratePrompt(provider, enrichedPrompt, skeletonSummary, batch, generatedSoFar)
		resp, err := ask(ctx, prompt)
		if err != nil {
			return nil, fmt.Errorf("hydrate batch %d failed: %w", bi+1, err)
		}

		cleaned := strings.TrimSpace(clean(resp))
		cmds, err := parseHydrateResponse(cleaned, len(batch))
		if err != nil {
			// Retry once with stricter format hint
			logf("[deploy] hydrate batch %d parse failed (%v), retrying", bi+1, err)
			retryPrompt := prompt + "\n\nIMPORTANT: Return ONLY a JSON array of command objects. No markdown, no prose."
			resp2, err2 := ask(ctx, retryPrompt)
			if err2 != nil {
				return nil, fmt.Errorf("hydrate batch %d retry failed: %w", bi+1, err2)
			}
			cleaned2 := strings.TrimSpace(clean(resp2))
			cmds, err = parseHydrateResponse(cleaned2, len(batch))
			if err != nil {
				return nil, fmt.Errorf("hydrate batch %d unparseable after retry: %w", bi+1, err)
			}
		}

		plan.Commands = append(plan.Commands, cmds...)
		logf("[deploy] hydrated batch %d/%d: %d commands (total=%d)", bi+1, len(batches), len(cmds), len(plan.Commands))
	}

	return plan, nil
}

// buildSkeletonPrompt creates the prompt for skeleton generation.
func buildSkeletonPrompt(provider, enrichedPrompt string, requiredLaunchOps []string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "aws"
	}

	var b strings.Builder

	b.WriteString("You are generating a deployment plan SKELETON — just the structure, not the full CLI args.\n\n")

	switch provider {
	case "cloudflare":
		b.WriteString("Provider: Cloudflare (wrangler/cloudflared commands)\n")
	case "gcp":
		b.WriteString("Provider: GCP (gcloud commands)\n")
	case "azure":
		b.WriteString("Provider: Azure (az commands)\n")
	case "digitalocean":
		b.WriteString("Provider: DigitalOcean (doctl commands, WITHOUT leading 'doctl' prefix)\n")
		b.WriteString("Services: registry, compute (droplet/firewall/ssh-key/reserved-ip), databases\n")
		b.WriteString("Operations: registry create, registry login, compute droplet create, compute firewall create, compute ssh-key list, compute firewall add-droplets, compute reserved-ip create\n")
	case "hetzner":
		b.WriteString("Provider: Hetzner Cloud (hcloud commands)\n")
	default:
		b.WriteString("Provider: AWS (aws CLI commands, WITHOUT leading 'aws' prefix)\n")
	}

	b.WriteString("\nYour task: generate an ordered list of deployment steps.\n")
	b.WriteString("For each step, provide ONLY:\n")
	b.WriteString("- service: the CLI service name (e.g. 'iam', 'ec2', 'elbv2', 'ssm')\n")
	b.WriteString("- operation: the CLI operation (e.g. 'create-role', 'run-instances')\n")
	b.WriteString("- reason: one sentence explaining why\n")
	b.WriteString("- produces: list of placeholder names this step outputs (e.g. ['ROLE_ARN'])\n")
	b.WriteString("- depends_on: list of placeholder names from earlier steps this needs\n\n")

	b.WriteString("DO NOT include:\n")
	b.WriteString("- Full CLI arguments\n")
	b.WriteString("- JSON payloads\n")
	b.WriteString("- Base64 content\n")
	b.WriteString("- Actual secret values\n\n")

	b.WriteString("Rules:\n")
	b.WriteString("- Steps run sequentially. A step can only depend_on placeholders from earlier steps.\n")
	b.WriteString("- Every depends_on placeholder MUST be produced by an earlier step.\n")
	b.WriteString("- Placeholder names must be UPPERCASE_WITH_UNDERSCORES (e.g. INSTANCE_ID, ALB_SG_ID).\n")
	b.WriteString("- Keep the plan minimal — fewest steps that get the job done.\n")
	b.WriteString("- Do NOT add redundant diagnostic/verification steps unless essential for correctness.\n")
	if provider == "digitalocean" {
		b.WriteString("- For user-data on Droplet: embed the boot script in compute droplet create --user-data.\n")
		b.WriteString("- A typical Droplet deploy needs: ssh-key list, registry create, registry login, docker build, docker push, firewall create, droplet create, firewall add-droplets, reserved-ip create.\n")
		b.WriteString("- IMPORTANT: generate ALL infrastructure steps as separate skeleton entries. Do NOT collapse everything into a single droplet create step.\n")
		b.WriteString("- 'docker build' and 'docker push' use the plain docker CLI (service='docker'). Do NOT use 'registry docker build' or 'registry docker-push' — those are NOT valid doctl commands.\n")
		b.WriteString("- For DOCR auth on the Droplet user-data: install doctl, run 'doctl auth init -t $TOKEN && doctl registry login'. Do NOT read /root/.config/doctl/config.yaml.\n")
	} else {
		b.WriteString("- For user-data on EC2: use ONE 'ssm send-command' or embed in run-instances user-data. Do NOT repeat.\n")
	}
	b.WriteString("\n")

	if len(requiredLaunchOps) > 0 {
		b.WriteString("REQUIRED: The plan MUST include at least one of:\n")
		for _, op := range requiredLaunchOps {
			b.WriteString("  - " + strings.TrimSpace(op) + "\n")
		}
		b.WriteString("\n")
	}

	// Provider-specific example
	switch provider {
	case "digitalocean":
		b.WriteString("Output format (JSON only, no markdown):\n")
		b.WriteString("{\n")
		b.WriteString("  \"steps\": [\n")
		b.WriteString("    {\n")
		b.WriteString("      \"service\": \"compute\",\n")
		b.WriteString("      \"operation\": \"ssh-key list\",\n")
		b.WriteString("      \"reason\": \"Retrieve SSH key ID for Droplet authentication\",\n")
		b.WriteString("      \"produces\": [\"SSH_KEY_ID\"],\n")
		b.WriteString("      \"depends_on\": []\n")
		b.WriteString("    },\n")
		b.WriteString("    {\n")
		b.WriteString("      \"service\": \"compute\",\n")
		b.WriteString("      \"operation\": \"firewall create\",\n")
		b.WriteString("      \"reason\": \"Cloud Firewall for inbound traffic\",\n")
		b.WriteString("      \"produces\": [\"FIREWALL_ID\"],\n")
		b.WriteString("      \"depends_on\": []\n")
		b.WriteString("    },\n")
		b.WriteString("    {\n")
		b.WriteString("      \"service\": \"compute\",\n")
		b.WriteString("      \"operation\": \"droplet create\",\n")
		b.WriteString("      \"reason\": \"Create Droplet with Docker pre-installed\",\n")
		b.WriteString("      \"produces\": [\"DROPLET_ID\", \"DROPLET_IP\"],\n")
		b.WriteString("      \"depends_on\": [\"SSH_KEY_ID\"]\n")
		b.WriteString("    }\n")
		b.WriteString("  ]\n")
		b.WriteString("}\n\n")
	default:
		b.WriteString("Output format (JSON only, no markdown):\n")
		b.WriteString("{\n")
		b.WriteString("  \"steps\": [\n")
		b.WriteString("    {\n")
		b.WriteString("      \"service\": \"iam\",\n")
		b.WriteString("      \"operation\": \"create-role\",\n")
		b.WriteString("      \"reason\": \"IAM role for EC2 to access ECR and Secrets Manager\",\n")
		b.WriteString("      \"produces\": [\"ROLE_ARN\"],\n")
		b.WriteString("      \"depends_on\": []\n")
		b.WriteString("    }\n")
		b.WriteString("  ],\n")
		b.WriteString("  \"notes\": [\"optional notes\"]\n")
		b.WriteString("}\n\n")
	}

	b.WriteString("Deployment context:\n")
	b.WriteString(strings.TrimSpace(enrichedPrompt))
	b.WriteString("\n")

	return b.String()
}

// buildSkeletonSummary creates a compact text representation of the skeleton for context.
func buildSkeletonSummary(skeleton *PlanSkeleton) string {
	var b strings.Builder
	b.WriteString("Plan skeleton:\n")
	for i, s := range skeleton.Steps {
		b.WriteString(fmt.Sprintf("  %d) %s %s", i+1, s.Service, s.Operation))
		if len(s.Produces) > 0 {
			b.WriteString(" → " + strings.Join(s.Produces, ", "))
		}
		if len(s.DependsOn) > 0 {
			b.WriteString(" (needs: " + strings.Join(s.DependsOn, ", ") + ")")
		}
		b.WriteString(" — " + s.Reason)
		b.WriteString("\n")
	}
	return b.String()
}

// buildHydratePrompt creates the prompt for detailing a batch of skeleton steps.
func buildHydratePrompt(provider, enrichedPrompt, skeletonSummary string, batch []SkeletonStep, generatedSoFar []string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "aws"
	}

	var b strings.Builder

	b.WriteString("You are filling in exact CLI arguments for deployment plan steps.\n\n")

	switch provider {
	case "cloudflare":
		b.WriteString("Provider: Cloudflare. Commands use wrangler/cloudflared.\n")
	case "gcp":
		b.WriteString("Provider: GCP. Commands start with the gcloud group (e.g. 'run', 'compute').\n")
	case "azure":
		b.WriteString("Provider: Azure. Commands start with the az group (e.g. 'vm', 'containerapp').\n")
	case "digitalocean":
		b.WriteString("Provider: DigitalOcean. Args start with the doctl group (e.g. 'compute', 'registry'), NOT 'doctl'.\n")
		b.WriteString("Use --output json for JSON output (NOT --format json). Use --format for column selection only (e.g. --format ID).\n")
		b.WriteString("IMPORTANT: 'docker build' and 'docker push' are plain docker CLI commands. Args start with 'docker' (e.g. ['docker','build','-t','<tag>','.']).\n")
		b.WriteString("Do NOT use 'registry docker build' or 'registry docker-push' — those are NOT valid doctl commands.\n")
	case "hetzner":
		b.WriteString("Provider: Hetzner Cloud. Commands use hcloud (e.g. 'server', 'firewall', 'network').\n")
	default:
		b.WriteString("Provider: AWS. Args start with the service name (e.g. 'ec2', 'iam'), NOT 'aws'.\n")
	}

	b.WriteString("\nRules:\n")
	b.WriteString("- Use ONLY angle-bracket placeholders: <ROLE_ARN>, <INSTANCE_ID>, etc.\n")
	b.WriteString("- Do NOT invent new placeholder names — use ONLY the names from the skeleton.\n")
	b.WriteString("- produces keys MUST match the skeleton's 'produces' names exactly.\n")
	b.WriteString("- produces values are JMESPath queries on the CLI JSON output.\n")
	b.WriteString("- For user-data: provide the actual bash script (it will be base64-encoded automatically).\n")
	b.WriteString("- CONSISTENCY: resource names in user-data scripts MUST match the names used in earlier commands.\n")
	if provider == "digitalocean" {
		b.WriteString("  For example, if 'registry create my-app-abc123' was generated,\n")
		b.WriteString("  the user-data MUST pull from 'registry.digitalocean.com/my-app-abc123/...', NOT a different name.\n")
		b.WriteString("  Use --output json for JSON output (NOT --format json). Use --format for column selection (e.g. --format ID,Name).\n")
		b.WriteString("  For docker build/push: args=['docker','build','-t','<tag>','.'] and args=['docker','push','<tag>'] — plain docker CLI, NOT doctl subcommands.\n")
		b.WriteString("  CRITICAL: docker build and docker push MUST use the EXACT SAME image tag. If build tags 'registry.digitalocean.com/myapp/img:latest', push MUST push that exact tag.\n")
		b.WriteString("  For DOCR auth in user-data: install doctl via snap/wget, then 'doctl auth init -t $TOKEN && doctl registry login'. Do NOT cat /root/.config/doctl/config.yaml.\n\n")
	} else {
		b.WriteString("  For example, if 'ecr create-repository --repository-name my-app-abc123' was generated,\n")
		b.WriteString("  the user-data MUST pull from 'my-app-abc123', NOT a different name.\n")
		b.WriteString("  Prefer deriving ECR registry URLs dynamically from instance metadata rather than hardcoding.\n\n")
	}

	b.WriteString(skeletonSummary)
	b.WriteString("\n")

	if len(generatedSoFar) > 0 {
		b.WriteString("Already generated commands:\n")
		for _, g := range generatedSoFar {
			b.WriteString("  " + g + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("Generate the EXACT CLI args for these %d step(s):\n", len(batch)))
	for i, s := range batch {
		b.WriteString(fmt.Sprintf("  Step: %s %s — %s\n", s.Service, s.Operation, s.Reason))
		if len(s.Produces) > 0 {
			b.WriteString(fmt.Sprintf("    Must produce: %s\n", strings.Join(s.Produces, ", ")))
		}
		if len(s.DependsOn) > 0 {
			b.WriteString(fmt.Sprintf("    Depends on: %s\n", strings.Join(s.DependsOn, ", ")))
		}
		if i < len(batch)-1 {
			b.WriteString("\n")
		}
	}

	b.WriteString("\nReturn JSON ONLY (no markdown). Array of command objects:\n")
	b.WriteString("[\n")
	b.WriteString("  {\n")
	b.WriteString("    \"args\": [\"service\", \"operation\", \"--flag\", \"value\", ...],\n")
	b.WriteString("    \"reason\": \"...\",\n")
	b.WriteString("    \"produces\": {\"KEY\": \"$.JMESPath\"}\n")
	b.WriteString("  }\n")
	b.WriteString("]\n\n")

	b.WriteString("Deployment context:\n")
	b.WriteString(strings.TrimSpace(enrichedPrompt))
	b.WriteString("\n")

	return b.String()
}

// batchSkeletonSteps groups consecutive steps that can be hydrated together.
// Independent steps (no overlapping dependencies) get batched; dependent chains stay separate.
// Max batch size is 10 to reduce API calls while keeping LLM focus reasonable.
func batchSkeletonSteps(skeleton *PlanSkeleton) [][]SkeletonStep {
	const maxBatchSize = 10
	if skeleton == nil || len(skeleton.Steps) == 0 {
		return nil
	}

	// Build a set of what each step produces
	producedBefore := map[string]bool{}
	var batches [][]SkeletonStep
	var current []SkeletonStep

	for _, step := range skeleton.Steps {
		// Can this step go in the current batch?
		// Yes if: none of its depends_on are produced by steps in the current batch
		canBatch := true
		if len(current) >= maxBatchSize {
			canBatch = false
		}
		for _, dep := range step.DependsOn {
			// Check if any step IN THE CURRENT BATCH produces this
			for _, cs := range current {
				for _, p := range cs.Produces {
					if strings.EqualFold(p, dep) {
						canBatch = false
						break
					}
				}
				if !canBatch {
					break
				}
			}
			if !canBatch {
				break
			}
			// Also check if it's produced at all (if not, it's a dependency from a prior batch)
			if !producedBefore[strings.ToUpper(dep)] {
				// Dependency not yet produced — this step needs a prior batch to complete
				// But it could still batch with other steps that also depend on earlier batches
			}
		}

		if !canBatch && len(current) > 0 {
			batches = append(batches, current)
			for _, cs := range current {
				for _, p := range cs.Produces {
					producedBefore[strings.ToUpper(p)] = true
				}
			}
			current = nil
		}

		current = append(current, step)
	}

	if len(current) > 0 {
		batches = append(batches, current)
	}

	return batches
}

// parseSkeletonResponse parses the LLM's skeleton JSON.
func parseSkeletonResponse(raw string) (*PlanSkeleton, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty skeleton response")
	}

	var skeleton PlanSkeleton
	if err := json.Unmarshal([]byte(raw), &skeleton); err != nil {
		// Try extracting from markdown fence
		if idx := strings.Index(raw, "{"); idx >= 0 {
			if end := strings.LastIndex(raw, "}"); end > idx {
				if err2 := json.Unmarshal([]byte(raw[idx:end+1]), &skeleton); err2 == nil {
					goto parsed
				}
			}
		}
		return nil, fmt.Errorf("skeleton parse error: %w", err)
	}
parsed:
	if len(skeleton.Steps) == 0 {
		return nil, fmt.Errorf("skeleton has no steps")
	}
	return &skeleton, nil
}

// parseHydrateResponse parses the LLM's command detail JSON.
func parseHydrateResponse(raw string, expectedCount int) ([]maker.Command, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty hydrate response")
	}

	// Try as array of commands
	var cmds []maker.Command
	if err := json.Unmarshal([]byte(raw), &cmds); err == nil && len(cmds) > 0 {
		return cmds, nil
	}

	// Try as object with commands array
	var wrapper struct {
		Commands []maker.Command `json:"commands"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil && len(wrapper.Commands) > 0 {
		return wrapper.Commands, nil
	}

	// Try extracting JSON from markdown fences
	if idx := strings.Index(raw, "["); idx >= 0 {
		if end := strings.LastIndex(raw, "]"); end > idx {
			if err := json.Unmarshal([]byte(raw[idx:end+1]), &cmds); err == nil && len(cmds) > 0 {
				return cmds, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse hydrate response (expected %d commands)", expectedCount)
}

// validateSkeleton checks the skeleton for structural issues.
func validateSkeleton(skeleton *PlanSkeleton, requiredLaunchOps []string) error {
	if skeleton == nil || len(skeleton.Steps) == 0 {
		return fmt.Errorf("empty skeleton")
	}

	var issues []string

	// Check that all depends_on are produced by earlier steps
	produced := map[string]bool{}
	for i, step := range skeleton.Steps {
		for _, dep := range step.DependsOn {
			depUpper := strings.ToUpper(dep)
			if !produced[depUpper] {
				issues = append(issues, fmt.Sprintf("step %d (%s %s) depends on %s which is not produced by any earlier step", i+1, step.Service, step.Operation, dep))
			}
		}
		for _, p := range step.Produces {
			produced[strings.ToUpper(p)] = true
		}
	}

	// Check required launch ops are present
	for _, req := range requiredLaunchOps {
		req = strings.ToLower(strings.TrimSpace(req))
		found := false
		for _, step := range skeleton.Steps {
			key := strings.ToLower(step.Service + " " + step.Operation)
			if strings.Contains(key, req) {
				found = true
				break
			}
		}
		if !found {
			issues = append(issues, fmt.Sprintf("required launch operation %q not found in skeleton", req))
		}
	}

	// Check for duplicate service+operation with same produces AND depends_on (likely LLM duplication).
	// Different depends_on = different intent (e.g. attach-role-policy with different policies).
	type stepKey struct {
		svc, op   string
		produces  string
		dependsOn string
	}
	seen := map[stepKey]int{}
	for i, step := range skeleton.Steps {
		k := stepKey{
			svc:       strings.ToLower(step.Service),
			op:        strings.ToLower(step.Operation),
			produces:  strings.ToUpper(strings.Join(step.Produces, ",")),
			dependsOn: strings.ToUpper(strings.Join(step.DependsOn, ",")),
		}
		if prev, ok := seen[k]; ok {
			issues = append(issues, fmt.Sprintf("step %d (%s %s) duplicates step %d", i+1, step.Service, step.Operation, prev+1))
		}
		seen[k] = i
	}

	if len(issues) > 0 {
		return fmt.Errorf("%d issue(s): %s", len(issues), strings.Join(issues, "; "))
	}
	return nil
}

// TopologicalSortSkeleton reorders skeleton steps to satisfy dependency constraints.
// Uses Kahn's algorithm to ensure steps that produce placeholders come before
// steps that depend on those placeholders. Returns error if cycle detected.
func TopologicalSortSkeleton(skeleton *PlanSkeleton) (*PlanSkeleton, error) {
	if skeleton == nil || len(skeleton.Steps) <= 1 {
		return skeleton, nil
	}

	n := len(skeleton.Steps)

	// Build map of placeholder -> step index that produces it
	producedBy := make(map[string]int)
	for i, step := range skeleton.Steps {
		for _, p := range step.Produces {
			producedBy[strings.ToUpper(strings.TrimSpace(p))] = i
		}
	}

	// Build adjacency list and in-degree map
	// Edge: step A -> step B if B depends on something A produces
	inDegree := make([]int, n)
	graph := make([][]int, n)
	for i := range graph {
		graph[i] = make([]int, 0)
	}

	for i, step := range skeleton.Steps {
		for _, dep := range step.DependsOn {
			dep = strings.ToUpper(strings.TrimSpace(dep))
			if producer, ok := producedBy[dep]; ok && producer != i {
				graph[producer] = append(graph[producer], i)
				inDegree[i]++
			}
		}
	}

	// Kahn's algorithm: start with nodes that have no dependencies
	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}

	sorted := make([]SkeletonStep, 0, n)
	for len(queue) > 0 {
		// Pop from front
		curr := queue[0]
		queue = queue[1:]
		sorted = append(sorted, skeleton.Steps[curr])

		// Reduce in-degree for all dependents
		for _, next := range graph[curr] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	// If we couldn't sort all steps, there's a cycle
	if len(sorted) != n {
		return skeleton, fmt.Errorf("dependency cycle detected in skeleton (%d of %d steps sorted)", len(sorted), n)
	}

	skeleton.Steps = sorted
	return skeleton, nil
}

// checkMissingLaunchOps returns a list of required launch operations that are missing from the skeleton.
// This is used to detect fundamentally incomplete plans that should trigger fallback to paged generation.
func checkMissingLaunchOps(skeleton *PlanSkeleton, requiredLaunchOps []string) []string {
	if skeleton == nil || len(skeleton.Steps) == 0 {
		return requiredLaunchOps
	}

	var missing []string
	for _, req := range requiredLaunchOps {
		req = strings.ToLower(strings.TrimSpace(req))
		found := false
		for _, step := range skeleton.Steps {
			key := strings.ToLower(step.Service + " " + step.Operation)
			if strings.Contains(key, req) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, req)
		}
	}
	return missing
}

// countUniquePlaceholders counts unique placeholder names in the skeleton.
func countUniquePlaceholders(skeleton *PlanSkeleton) int {
	seen := map[string]bool{}
	for _, s := range skeleton.Steps {
		for _, p := range s.Produces {
			seen[strings.ToUpper(p)] = true
		}
	}
	return len(seen)
}

// formatProducesShort creates a compact string for produces (for context display).
func formatProducesShort(produces map[string]string) string {
	if len(produces) == 0 {
		return ""
	}
	keys := make([]string, 0, len(produces))
	for k := range produces {
		keys = append(keys, k)
	}
	return "→ " + strings.Join(keys, ", ")
}
