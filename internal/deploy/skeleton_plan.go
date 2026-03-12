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
	Steps        []SkeletonStep       `json:"steps"`
	Notes        []string             `json:"notes,omitempty"`
	Capabilities SkeletonCapabilities `json:"capabilities,omitempty"`
}

type SkeletonCapabilities struct {
	Provider       string   `json:"provider,omitempty"`
	AppKind        string   `json:"app_kind,omitempty"`
	RuntimeModel   string   `json:"runtime_model,omitempty"`
	RequiredSteps  []string `json:"required_steps,omitempty"`
	ForbiddenSteps []string `json:"forbidden_steps,omitempty"`
	PreferredPorts []string `json:"preferred_ports,omitempty"`
	RequiredEnv    []string `json:"required_env,omitempty"`
	ExecutionNotes []string `json:"execution_notes,omitempty"`
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
	skeleton.Capabilities = mergeSkeletonCapabilities(
		inferSkeletonCapabilities(provider, enrichedPrompt, requiredLaunchOps),
		skeleton.Capabilities,
	)

	// Validate the skeleton
	if err := validateSkeleton(skeleton, requiredLaunchOps); err != nil {
		logf("[deploy] skeleton validation warning: %v", err)
		// Don't fail — harden downstream
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
		Version:      maker.CurrentPlanVersion,
		Provider:     provider,
		Commands:     make([]maker.Command, 0, len(skeleton.Steps)),
		Capabilities: planCapabilitiesFromSkeleton(skeleton.Capabilities),
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
		if err == nil {
			err = validateHydratedBatch(batch, cmds, skeleton.Capabilities)
		}
		if err != nil {
			// Retry once with stricter format hint
			logf("[deploy] hydrate batch %d parse failed (%v), retrying", bi+1, err)
			retryPrompt := prompt + "\n\nIMPORTANT: Return ONLY a JSON array of command objects. No markdown, no prose. The command families must match the requested skeleton steps exactly, and the output must obey the capability hints."
			resp2, err2 := ask(ctx, retryPrompt)
			if err2 != nil {
				return nil, fmt.Errorf("hydrate batch %d retry failed: %w", bi+1, err2)
			}
			cleaned2 := strings.TrimSpace(clean(resp2))
			cmds, err = parseHydrateResponse(cleaned2, len(batch))
			if err == nil {
				err = validateHydratedBatch(batch, cmds, skeleton.Capabilities)
			}
			if err != nil {
				return nil, fmt.Errorf("hydrate batch %d unparseable after retry: %w", bi+1, err)
			}
		}

		plan.Commands = append(plan.Commands, cmds...)
		logf("[deploy] hydrated batch %d/%d: %d commands (total=%d)", bi+1, len(batches), len(cmds), len(plan.Commands))
	}
	if err := validateHydratedPlanCapabilities(plan, skeleton); err != nil {
		return nil, fmt.Errorf("hydrated plan violates skeleton capabilities: %w", err)
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
		b.WriteString("Services: compute (droplet/firewall/ssh-key/reserved-ip), databases\n")
		b.WriteString("Operations: compute ssh-key import, compute droplet create, compute firewall create, compute firewall add-droplets, optional compute reserved-ip create\n")
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
	if summary := formatSkeletonCapabilities(inferSkeletonCapabilities(provider, enrichedPrompt, requiredLaunchOps)); summary != "" {
		b.WriteString(summary)
	}
	if provider == "digitalocean" {
		b.WriteString("- For user-data on Droplet: embed the boot script in compute droplet create --user-data.\n")
		b.WriteString("- A typical Droplet deploy needs: ssh-key import, firewall create, droplet create, firewall add-droplets. reserved-ip create is optional when quota allows.\n")
		b.WriteString("- IMPORTANT: generate ALL infrastructure steps as separate skeleton entries. Do NOT collapse everything into a single droplet create step.\n")
		b.WriteString("- INVALID step families for this provider path: compute ssh-key create, registry docker-login, registry docker-credential, registry docker-config, registry docker build, registry docker-push, __DOCKER_BUILD__, __DOCKER_PUSH__.\n")
		if strings.Contains(strings.ToLower(enrichedPrompt), "openclaw") {
			b.WriteString("- OpenClaw on DigitalOcean needs both droplet runtime steps and App Platform proxy steps; include registry create, registry login, docker build, docker push, and apps create when HTTPS is required.\n")
		} else {
			b.WriteString("- Do NOT include registry create, registry login, docker build, or docker push steps. The image is built on the droplet itself via user-data.\n")
		}
		b.WriteString("- Prefer a fresh dedicated deployment SSH key import step over reusing an unrelated existing SSH key.\n")
		writeLines(&b, openClawDOSkeletonLines()...)
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
		b.WriteString("      \"operation\": \"ssh-key import\",\n")
		b.WriteString("      \"reason\": \"Import a dedicated SSH public key for Droplet authentication\",\n")
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
		b.WriteString("  ],\n")
		b.WriteString("  \"capabilities\": {\n")
		b.WriteString("    \"provider\": \"digitalocean\",\n")
		b.WriteString("    \"app_kind\": \"openclaw\",\n")
		b.WriteString("    \"runtime_model\": \"droplet-compose\"\n")
		b.WriteString("  }\n")
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
		b.WriteString("  \"capabilities\": {\n")
		b.WriteString("    \"provider\": \"aws\"\n")
		b.WriteString("  },\n")
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
	if summary := formatSkeletonCapabilities(skeleton.Capabilities); summary != "" {
		b.WriteString(summary)
	}
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
		b.WriteString("INVALID families to never emit: registry docker-login, registry docker-credential, registry docker-config, __DOCKER_BUILD__, __DOCKER_PUSH__, __LOCAL_DOCKER_BUILD__, __LOCAL_DOCKER_PUSH__, __docker__, compute ssh-key create.\n")
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
	if summary := formatSkeletonCapabilities(batchCapabilities(provider, enrichedPrompt, skeletonSummary)); summary != "" {
		b.WriteString(summary)
	}
	if provider == "digitalocean" {
		b.WriteString("  For example, if 'registry create my-app-abc123' was generated,\n")
		b.WriteString("  the user-data MUST pull from 'registry.digitalocean.com/my-app-abc123/...', NOT a different name.\n")
		b.WriteString("  Use --output json for JSON output (NOT --format json). Use --format for column selection (e.g. --format ID,Name).\n")
		b.WriteString("  For docker build/push: args=['docker','build','-t','<tag>','.'] and args=['docker','push','<tag>'] — plain docker CLI, NOT doctl subcommands.\n")
		b.WriteString("  Never emit compute droplet create --tag; use --tag-name if a tag is needed. Never emit compute firewall create --tag-names.\n")
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
// Max batch size is 5 to keep LLM focus tight.
func batchSkeletonSteps(skeleton *PlanSkeleton) [][]SkeletonStep {
	const maxBatchSize = 5
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

	for _, req := range skeleton.Capabilities.RequiredSteps {
		req = strings.ToLower(strings.TrimSpace(req))
		if req == "" {
			continue
		}
		found := false
		for _, step := range skeleton.Steps {
			key := strings.ToLower(step.Service + " " + step.Operation)
			if strings.Contains(key, req) {
				found = true
				break
			}
		}
		if !found {
			issues = append(issues, fmt.Sprintf("capability-required step %q not found in skeleton", req))
		}
	}
	for _, forbid := range skeleton.Capabilities.ForbiddenSteps {
		forbid = strings.ToLower(strings.TrimSpace(forbid))
		if forbid == "" {
			continue
		}
		for i, step := range skeleton.Steps {
			key := strings.ToLower(step.Service + " " + step.Operation)
			if strings.Contains(key, forbid) {
				issues = append(issues, fmt.Sprintf("step %d (%s %s) violates forbidden capability step %q", i+1, step.Service, step.Operation, forbid))
			}
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

func inferSkeletonCapabilities(provider, enrichedPrompt string, requiredLaunchOps []string) SkeletonCapabilities {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "aws"
	}
	lower := strings.ToLower(enrichedPrompt)
	cap := SkeletonCapabilities{Provider: provider}
	if strings.Contains(lower, "openclaw") || strings.Contains(lower, "docker-setup.sh") {
		cap.AppKind = "openclaw"
		switch provider {
		case "digitalocean":
			cap.RuntimeModel = "droplet-compose"
			cap.RequiredSteps = []string{
				"compute ssh-key import",
				"compute firewall create",
				"compute droplet create",
				"compute firewall add-droplets",
				"registry create",
				"registry login",
				"docker build",
				"docker push",
				"apps create",
			}
			cap.PreferredPorts = []string{"22", "18789", "18790"}
			cap.RequiredEnv = []string{"OPENCLAW_GATEWAY_BIND", "OPENCLAW_GATEWAY_TOKEN|OPENCLAW_GATEWAY_PASSWORD", "AI_PROVIDER_KEY"}
			cap.ExecutionNotes = []string{
				"Build the OpenClaw runtime on the droplet via user-data",
				"Use DOCR and App Platform only for the small managed HTTPS proxy front door",
				"Keep firewall ingress restricted to the OpenClaw ports on the droplet; do not expose 80/443 directly",
				"Reserved IP is optional and quota-dependent",
			}
		default:
			cap.RuntimeModel = "vm-runtime"
			cap.RequiredSteps = appendUniqueFold(cap.RequiredSteps,
				"ec2 run-instances",
				"elbv2 create-load-balancer",
				"elbv2 create-target-group",
				"elbv2 create-listener",
				"cloudfront create-distribution",
				"cloudfront wait distribution-deployed",
			)
			cap.PreferredPorts = appendUniqueFold(cap.PreferredPorts, "18789")
			cap.ExecutionNotes = []string{
				"Keep a valid OpenClaw runtime path in user-data or SSM",
				"Preserve HTTPS front-door requirements when provider architecture needs them",
				"Preserve the CloudFront HTTPS pairing URL for OpenClaw, not only the ALB HTTP endpoint",
				"Preserve EC2 ECR pull viability for the runtime image",
			}
		}
	}
	if strings.Contains(lower, "wordpress") && cap.AppKind == "" {
		cap.AppKind = "wordpress"
		cap.RuntimeModel = "stateful-webapp"
		switch provider {
		case "aws":
			cap.RequiredSteps = appendUniqueFold(cap.RequiredSteps,
				"ec2 run-instances",
				"elbv2 create-load-balancer",
				"elbv2 create-target-group",
				"elbv2 create-listener",
			)
			cap.ForbiddenSteps = appendUniqueFold(cap.ForbiddenSteps,
				"ecr create-repository",
				"docker build",
				"docker push",
			)
			cap.PreferredPorts = appendUniqueFold(cap.PreferredPorts, "80")
			cap.RequiredEnv = appendUniqueFold(cap.RequiredEnv, "WORDPRESS_DB_PASSWORD")
			cap.ExecutionNotes = appendUniqueFold(cap.ExecutionNotes,
				"Run Docker Hub wordpress + mariadb images instead of building an app image",
				"Use ALB health check path /wp-login.php on port 80",
				"Persist DB and wp-content with Docker volumes",
				"Do not store WORDPRESS_DB_PASSWORD in SSM Parameter Store",
			)
		}
	}
	for _, op := range requiredLaunchOps {
		op = strings.TrimSpace(op)
		if op == "" {
			continue
		}
		cap.RequiredSteps = appendUniqueFold(cap.RequiredSteps, op)
	}
	return cap
}

func batchCapabilities(provider, enrichedPrompt, skeletonSummary string) SkeletonCapabilities {
	return inferSkeletonCapabilities(provider, enrichedPrompt+"\n"+skeletonSummary, nil)
}

func mergeSkeletonCapabilities(inferred, provided SkeletonCapabilities) SkeletonCapabilities {
	merged := inferred
	if strings.TrimSpace(provided.Provider) != "" {
		merged.Provider = provided.Provider
	}
	if strings.TrimSpace(provided.AppKind) != "" {
		merged.AppKind = provided.AppKind
	}
	if strings.TrimSpace(provided.RuntimeModel) != "" {
		merged.RuntimeModel = provided.RuntimeModel
	}
	merged.RequiredSteps = appendUniqueFold(merged.RequiredSteps, provided.RequiredSteps...)
	merged.ForbiddenSteps = appendUniqueFold(merged.ForbiddenSteps, provided.ForbiddenSteps...)
	merged.PreferredPorts = appendUniqueFold(merged.PreferredPorts, provided.PreferredPorts...)
	merged.RequiredEnv = appendUniqueFold(merged.RequiredEnv, provided.RequiredEnv...)
	merged.ExecutionNotes = appendUniqueFold(merged.ExecutionNotes, provided.ExecutionNotes...)
	return merged
}

func formatSkeletonCapabilities(cap SkeletonCapabilities) string {
	var b strings.Builder
	if strings.TrimSpace(cap.Provider) == "" && strings.TrimSpace(cap.AppKind) == "" && strings.TrimSpace(cap.RuntimeModel) == "" && len(cap.RequiredSteps) == 0 && len(cap.ForbiddenSteps) == 0 && len(cap.PreferredPorts) == 0 && len(cap.RequiredEnv) == 0 && len(cap.ExecutionNotes) == 0 {
		return ""
	}
	b.WriteString("Capability hints:\n")
	if cap.Provider != "" {
		b.WriteString("- provider: " + cap.Provider + "\n")
	}
	if cap.AppKind != "" {
		b.WriteString("- app_kind: " + cap.AppKind + "\n")
	}
	if cap.RuntimeModel != "" {
		b.WriteString("- runtime_model: " + cap.RuntimeModel + "\n")
	}
	if len(cap.RequiredSteps) > 0 {
		b.WriteString("- required_steps: " + strings.Join(cap.RequiredSteps, ", ") + "\n")
	}
	if len(cap.ForbiddenSteps) > 0 {
		b.WriteString("- forbidden_steps: " + strings.Join(cap.ForbiddenSteps, ", ") + "\n")
	}
	if len(cap.PreferredPorts) > 0 {
		b.WriteString("- preferred_ports: " + strings.Join(cap.PreferredPorts, ", ") + "\n")
	}
	if len(cap.RequiredEnv) > 0 {
		b.WriteString("- required_env: " + strings.Join(cap.RequiredEnv, ", ") + "\n")
	}
	for _, note := range cap.ExecutionNotes {
		b.WriteString("- note: " + note + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func appendUniqueFold(dst []string, values ...string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || containsStringFold(dst, value) {
			continue
		}
		dst = append(dst, value)
	}
	return dst
}

func containsStringFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func planCapabilitiesFromSkeleton(cap SkeletonCapabilities) *maker.PlanCapabilities {
	if strings.TrimSpace(cap.Provider) == "" && strings.TrimSpace(cap.AppKind) == "" && strings.TrimSpace(cap.RuntimeModel) == "" && len(cap.RequiredSteps) == 0 && len(cap.ForbiddenSteps) == 0 && len(cap.PreferredPorts) == 0 && len(cap.RequiredEnv) == 0 && len(cap.ExecutionNotes) == 0 {
		return nil
	}
	return &maker.PlanCapabilities{
		Provider:       strings.TrimSpace(cap.Provider),
		AppKind:        strings.TrimSpace(cap.AppKind),
		RuntimeModel:   strings.TrimSpace(cap.RuntimeModel),
		RequiredSteps:  append([]string(nil), cap.RequiredSteps...),
		ForbiddenSteps: append([]string(nil), cap.ForbiddenSteps...),
		PreferredPorts: append([]string(nil), cap.PreferredPorts...),
		RequiredEnv:    append([]string(nil), cap.RequiredEnv...),
		ExecutionNotes: append([]string(nil), cap.ExecutionNotes...),
	}
}

func InferPlanCapabilities(provider, enrichedPrompt string, requiredLaunchOps []string) *maker.PlanCapabilities {
	return planCapabilitiesFromSkeleton(inferSkeletonCapabilities(provider, enrichedPrompt, requiredLaunchOps))
}

func validateHydratedBatch(batch []SkeletonStep, cmds []maker.Command, cap SkeletonCapabilities) error {
	if len(cmds) != len(batch) {
		return fmt.Errorf("hydrate returned %d commands for %d skeleton steps", len(cmds), len(batch))
	}
	if cap.Provider == "digitalocean" {
		normalizeHydratedDigitalOceanCommands(cmds)
	}
	for i := range batch {
		if err := validateHydratedCommandForStep(batch[i], cmds[i], cap); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	if cap.Provider == "digitalocean" {
		strictOpenClaw := strings.EqualFold(strings.TrimSpace(cap.AppKind), "openclaw")
		for i, cmd := range cmds {
			if msg := validateDigitalOceanCommandBoundary(cmd.Args, strictOpenClaw); msg != "" {
				return fmt.Errorf("step %d: %s", i+1, msg)
			}
		}
	}
	return nil
}

func validateHydratedPlanCapabilities(plan *maker.Plan, skeleton *PlanSkeleton) error {
	if plan == nil || skeleton == nil {
		return nil
	}
	cap := skeleton.Capabilities
	if len(cap.ForbiddenSteps) > 0 {
		for i, cmd := range plan.Commands {
			key := hydratedCommandFamily(cmd.Args)
			for _, forbid := range cap.ForbiddenSteps {
				if forbid == "" {
					continue
				}
				if strings.Contains(key, strings.ToLower(strings.TrimSpace(forbid))) {
					return fmt.Errorf("command %d uses forbidden step family %q", i+1, forbid)
				}
			}
		}
	}
	for _, req := range cap.RequiredSteps {
		req = strings.ToLower(strings.TrimSpace(req))
		if req == "" {
			continue
		}
		found := false
		for _, cmd := range plan.Commands {
			if strings.Contains(hydratedCommandFamily(cmd.Args), req) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing required hydrated step family %q", req)
		}
	}
	return nil
}

func validateHydratedCommandForStep(step SkeletonStep, cmd maker.Command, cap SkeletonCapabilities) error {
	if len(cmd.Args) == 0 {
		return fmt.Errorf("missing args")
	}
	if !hydratedCommandMatchesStep(step, cmd.Args) {
		return fmt.Errorf("hydrated command family %q does not match skeleton step %q %q", hydratedCommandFamily(cmd.Args), step.Service, step.Operation)
	}
	key := hydratedCommandFamily(cmd.Args)
	for _, forbid := range cap.ForbiddenSteps {
		forbid = strings.ToLower(strings.TrimSpace(forbid))
		if forbid != "" && strings.Contains(key, forbid) {
			return fmt.Errorf("hydrated command violates forbidden step family %q", forbid)
		}
	}
	if cap.Provider == "digitalocean" && cap.RuntimeModel == "droplet-compose" && strings.EqualFold(strings.TrimSpace(step.Service), "compute") && strings.EqualFold(strings.TrimSpace(step.Operation), "droplet create") {
		script := extractDoctlUserDataScript(cmd.Args)
		if strings.TrimSpace(script) == "" {
			return fmt.Errorf("droplet create missing --user-data")
		}
		if cap.AppKind == "openclaw" && !hasOpenClawDORuntimeScript(script) {
			return fmt.Errorf("droplet user-data does not match expected OpenClaw runtime model")
		}
	}
	return nil
}

func hydratedCommandMatchesStep(step SkeletonStep, args []string) bool {
	if len(args) == 0 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), strings.TrimSpace(step.Service)) {
		return false
	}
	opTokens := strings.Fields(strings.ToLower(strings.TrimSpace(step.Operation)))
	if len(opTokens) == 0 {
		return true
	}
	if len(args)-1 < len(opTokens) {
		return false
	}
	for i, tok := range opTokens {
		if !strings.EqualFold(strings.TrimSpace(args[i+1]), tok) {
			return false
		}
	}
	return true
}

func hydratedCommandFamily(args []string) string {
	parts := make([]string, 0, 3)
	for i := 0; i < len(args); i++ {
		part := strings.ToLower(strings.TrimSpace(args[i]))
		if part == "" || strings.HasPrefix(part, "-") {
			break
		}
		parts = append(parts, part)
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}

	switch parts[0] {
	case "compute":
		if len(parts) >= 3 {
			switch parts[1] {
			case "ssh-key", "droplet", "firewall", "reserved-ip":
				return strings.Join(parts[:3], " ")
			}
		}
	case "registry":
		if len(parts) >= 3 && parts[1] == "docker" {
			return strings.Join(parts[:3], " ")
		}
	}

	return strings.Join(parts[:2], " ")
}

func normalizeHydratedDigitalOceanCommands(cmds []maker.Command) {
	for i := range cmds {
		args := cmds[i].Args
		if len(args) == 0 {
			continue
		}
		s0 := strings.ToLower(strings.TrimSpace(args[0]))
		s1 := ""
		s2 := ""
		if len(args) > 1 {
			s1 = strings.ToLower(strings.TrimSpace(args[1]))
		}
		if len(args) > 2 {
			s2 = strings.ToLower(strings.TrimSpace(args[2]))
		}
		switch {
		case s0 == "registry" && s1 == "docker-login":
			cmds[i].Args = []string{"registry", "login"}
		case s0 == "registry" && s1 == "docker-credential":
			cmds[i].Args = []string{"registry", "login"}
		case s0 == "registry" && s1 == "docker-config":
			cmds[i].Args = []string{"registry", "login"}
		case s0 == "registry" && s1 == "docker" && (s2 == "build" || s2 == "push"):
			cmds[i].Args = append([]string{"docker"}, args[2:]...)
		case s0 == "registry" && s1 == "docker-push":
			cmds[i].Args = append([]string{"docker", "push"}, args[2:]...)
		case s0 == "__docker_build__":
			cmds[i].Args = append([]string{"docker", "build"}, args[1:]...)
		case s0 == "__docker_push__":
			cmds[i].Args = append([]string{"docker", "push"}, args[1:]...)
		case s0 == "__local_docker_build__":
			cmds[i].Args = append([]string{"docker", "build"}, args[1:]...)
		case s0 == "__local_docker_push__":
			cmds[i].Args = append([]string{"docker", "push"}, args[1:]...)
		case s0 == "__docker__":
			cmds[i].Args = append([]string{"docker"}, args[1:]...)
		case s0 == "compute" && s1 == "ssh-key" && s2 == "create":
			fixed := append([]string(nil), args...)
			fixed[2] = "import"
			cmds[i].Args = fixed
		}
	}
}
