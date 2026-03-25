package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// placeholderRe matches placeholder tokens like <VPC_ID>, <SUBNET_1A_ID>, etc.
var placeholderRe = regexp.MustCompile(`<([A-Z0-9_]+)>`)

// variantPlaceholderRes matches LLM-hallucinated placeholder formats we normalize to <KEY>
var variantPlaceholderRes = []*regexp.Regexp{
	regexp.MustCompile(`\{\{([A-Z][A-Z0-9_]{2,})\}\}`),      // {{KEY}} — Mustache/Jinja
	regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]{2,})\}`),        // ${KEY} — shell variable
	regexp.MustCompile(`<<([A-Z][A-Z0-9_]{2,})>>`),          // <<KEY>> — double angle
	regexp.MustCompile(`%([A-Z][A-Z0-9_]{2,})%`),            // %KEY% — Windows batch
	regexp.MustCompile(`\$([A-Z][A-Z0-9_]{2,})(?:\b|[^(])`), // $KEY — bare shell var (3+ uppercase chars)
}

// normalizePlaceholderFormats rewrites all variant placeholder patterns to canonical <KEY>
func normalizePlaceholderFormats(plan *maker.Plan) *maker.Plan {
	if plan == nil {
		return plan
	}
	changed := false
	newPlan := &maker.Plan{
		Version:   plan.Version,
		CreatedAt: plan.CreatedAt,
		Provider:  plan.Provider,
		Question:  plan.Question,
		Summary:   plan.Summary,
		Notes:     plan.Notes,
		Commands:  make([]maker.Command, len(plan.Commands)),
	}
	for i, cmd := range plan.Commands {
		newCmd := maker.Command{
			Reason:   cmd.Reason,
			Produces: cmd.Produces,
			Args:     make([]string, len(cmd.Args)),
		}
		for j, arg := range cmd.Args {
			normalized := normalizeArg(arg)
			if normalized != arg {
				changed = true
			}
			newCmd.Args[j] = normalized
		}
		newPlan.Commands[i] = newCmd
	}
	if !changed {
		return plan
	}
	return newPlan
}

// normalizeArg rewrites variant placeholder patterns to <KEY> in a single arg
func normalizeArg(arg string) string {
	for _, re := range variantPlaceholderRes {
		arg = re.ReplaceAllStringFunc(arg, func(match string) string {
			sub := re.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			key := sub[1]
			// bare $KEY regex may capture trailing char — reconstruct cleanly
			if strings.HasPrefix(match, "$") && !strings.HasPrefix(match, "${") {
				trailing := strings.TrimPrefix(match, "$"+key)
				return "<" + key + ">" + trailing
			}
			return "<" + key + ">"
		})
	}
	return arg
}

// ResolvePlanPlaceholders attempts to replace placeholder tokens in the plan with actual values.
// It first tries to map from the InfraSnapshot, then calls the LLM for any remaining placeholders.
// Returns the modified plan and a list of any placeholders that could not be resolved.
func ResolvePlanPlaceholders(
	ctx context.Context,
	plan *maker.Plan,
	infraSnap *InfraSnapshot,
	ask AskFunc,
	clean CleanFunc,
	logf func(string, ...any),
) (*maker.Plan, []string, error) {
	if plan == nil {
		return nil, nil, fmt.Errorf("nil plan")
	}

	// normalize variant placeholder formats ({{KEY}}, ${KEY}, etc.) to canonical <KEY>
	plan = normalizePlaceholderFormats(plan)

	// Build initial bindings from infrastructure snapshot
	bindings := buildInfraBindings(infraSnap)

	// Extract all placeholders from the plan
	allPlaceholders := extractPlaceholdersFromPlan(plan)
	if len(allPlaceholders) == 0 {
		return plan, nil, nil
	}

	logf("[deploy] found %d unique placeholders in plan", len(allPlaceholders))

	// Apply known bindings first
	plan = applyBindingsToPlan(plan, bindings)

	// Check what is still unresolved
	remaining := extractPlaceholdersFromPlan(plan)
	if len(remaining) == 0 {
		logf("[deploy] all placeholders resolved from infrastructure")
		return plan, nil, nil
	}
	if AllPlaceholdersAreProduced(plan, remaining) {
		logf("[deploy] %d placeholders are produced by earlier commands; skipping LLM resolution", len(remaining))
		return plan, remaining, nil
	}

	logf("[deploy] %d placeholders remain after infra mapping, calling LLM...", len(remaining))

	// Call LLM to resolve remaining placeholders
	resolved, err := resolvePlaceholdersWithLLM(ctx, plan, remaining, infraSnap, ask, clean)
	if err != nil {
		logf("[deploy] warning: LLM placeholder resolution failed: %v", err)
		return plan, remaining, nil
	}

	// Merge LLM-resolved bindings
	for k, v := range resolved {
		bindings[k] = v
	}

	// Apply all bindings again
	plan = applyBindingsToPlan(plan, bindings)

	// Final check for any still-unresolved placeholders
	stillUnresolved := extractPlaceholdersFromPlan(plan)
	return plan, stillUnresolved, nil
}

// buildInfraBindings creates a mapping from placeholder names to actual values from the infrastructure snapshot.
func buildInfraBindings(snap *InfraSnapshot) map[string]string {
	bindings := make(map[string]string)
	if snap == nil {
		return bindings
	}

	// Account ID
	if snap.AccountID != "" {
		bindings["ACCOUNT_ID"] = snap.AccountID
		bindings["AWS_ACCOUNT_ID"] = snap.AccountID
	}

	// Region
	if snap.Region != "" {
		bindings["REGION"] = snap.Region
		bindings["AWS_REGION"] = snap.Region
	}

	// Latest AMI (Amazon Linux 2023)
	if snap.LatestAMI != "" {
		bindings["AMI_ID"] = snap.LatestAMI
		bindings["AMI"] = snap.LatestAMI
		bindings["IMAGE_ID"] = snap.LatestAMI
	}

	// VPC and subnets
	if snap.VPC != nil {
		if snap.VPC.VPCID != "" {
			bindings["VPC_ID"] = snap.VPC.VPCID
			bindings["DEFAULT_VPC_ID"] = snap.VPC.VPCID
		}

		// Map subnets by position and common placeholder patterns
		for i, subnetID := range snap.VPC.Subnets {
			if subnetID == "" {
				continue
			}

			// Generic positional: SUBNET_1, SUBNET_2, etc.
			bindings[fmt.Sprintf("SUBNET_%d", i+1)] = subnetID
			bindings[fmt.Sprintf("SUBNET_%d_ID", i+1)] = subnetID

			// Common patterns for first two subnets (for ALB which needs 2 AZs)
			switch i {
			case 0:
				bindings["SUBNET_ID"] = subnetID
				bindings["SUBNET_1A_ID"] = subnetID
				bindings["SUBNET_A_ID"] = subnetID
				bindings["PUBLIC_SUBNET_1"] = subnetID
				bindings["SUBNET_PUB_1_ID"] = subnetID
			case 1:
				bindings["SUBNET_1B_ID"] = subnetID
				bindings["SUBNET_B_ID"] = subnetID
				bindings["PUBLIC_SUBNET_2"] = subnetID
				bindings["SUBNET_PUB_2_ID"] = subnetID
			case 2:
				bindings["SUBNET_1C_ID"] = subnetID
				bindings["SUBNET_C_ID"] = subnetID
			}
		}
	}

	// Security groups by name matching
	for _, sg := range snap.SecurityGroups {
		if sg.ID == "" {
			continue
		}

		nameLower := strings.ToLower(sg.Name)

		// Default security group
		if nameLower == "default" {
			bindings["DEFAULT_SG_ID"] = sg.ID
			// Only set SG_ID if not already set
			if _, ok := bindings["SG_ID"]; !ok {
				bindings["SG_ID"] = sg.ID
			}
		}

		// ALB security groups
		if strings.Contains(nameLower, "alb") || strings.Contains(nameLower, "load") {
			bindings["ALB_SG_ID"] = sg.ID
			bindings["SG_ALB_ID"] = sg.ID
		}

		// EC2/web security groups
		if strings.Contains(nameLower, "ec2") || strings.Contains(nameLower, "web") || strings.Contains(nameLower, "app") {
			bindings["EC2_SG_ID"] = sg.ID
			bindings["SG_EC2_ID"] = sg.ID
			bindings["WEB_SG_ID"] = sg.ID
			bindings["SG_WEB_ID"] = sg.ID
		}

		// RDS security groups
		if strings.Contains(nameLower, "rds") || strings.Contains(nameLower, "database") || strings.Contains(nameLower, "db") {
			bindings["RDS_SG_ID"] = sg.ID
			bindings["SG_RDS_ID"] = sg.ID
			bindings["DB_SG_ID"] = sg.ID
		}

		// ECS security groups
		if strings.Contains(nameLower, "ecs") || strings.Contains(nameLower, "fargate") {
			bindings["ECS_SG_ID"] = sg.ID
			bindings["SG_ECS_ID"] = sg.ID
		}

		// Lambda security groups
		if strings.Contains(nameLower, "lambda") {
			bindings["LAMBDA_SG_ID"] = sg.ID
			bindings["SG_LAMBDA_ID"] = sg.ID
		}
	}

	// ECR repositories - map first one as default
	if len(snap.ECRRepos) > 0 {
		bindings["ECR_REPO"] = snap.ECRRepos[0]
		bindings["ECR_REPO_NAME"] = snap.ECRRepos[0]
	}

	// ECS clusters - map first one as default
	if len(snap.ECSClusters) > 0 {
		bindings["ECS_CLUSTER"] = snap.ECSClusters[0]
		bindings["ECS_CLUSTER_NAME"] = snap.ECSClusters[0]
		bindings["CLUSTER_NAME"] = snap.ECSClusters[0]
	}

	return bindings
}

// extractPlaceholdersFromPlan finds all unique placeholder tokens in the plan.
func extractPlaceholdersFromPlan(plan *maker.Plan) []string {
	seen := make(map[string]bool)
	var placeholders []string

	for _, cmd := range plan.Commands {
		for _, arg := range cmd.Args {
			matches := placeholderRe.FindAllStringSubmatch(arg, -1)
			for _, m := range matches {
				if len(m) >= 2 {
					token := m[1]
					if !seen[token] {
						seen[token] = true
						placeholders = append(placeholders, token)
					}
				}
			}
		}
	}

	return placeholders
}

// applyBindingsToPlan replaces placeholder tokens in the plan with their resolved values.
func applyBindingsToPlan(plan *maker.Plan, bindings map[string]string) *maker.Plan {
	if len(bindings) == 0 {
		return plan
	}

	// Deep copy the plan to avoid mutating the original
	newPlan := &maker.Plan{
		Version:   plan.Version,
		CreatedAt: plan.CreatedAt,
		Provider:  plan.Provider,
		Question:  plan.Question,
		Summary:   plan.Summary,
		Notes:     plan.Notes,
		Commands:  make([]maker.Command, len(plan.Commands)),
	}

	for i, cmd := range plan.Commands {
		newCmd := maker.Command{
			Reason:   cmd.Reason,
			Produces: cmd.Produces,
			Args:     make([]string, len(cmd.Args)),
		}

		for j, arg := range cmd.Args {
			newCmd.Args[j] = placeholderRe.ReplaceAllStringFunc(arg, func(match string) string {
				// Extract the placeholder name (without angle brackets)
				token := strings.TrimSuffix(strings.TrimPrefix(match, "<"), ">")
				if val, ok := bindings[token]; ok && val != "" {
					return val
				}
				return match // Keep original if not found
			})
		}

		newPlan.Commands[i] = newCmd
	}

	return newPlan
}

// resolvePlaceholdersWithLLM asks the LLM to figure out what values the placeholders should have.
func resolvePlaceholdersWithLLM(
	ctx context.Context,
	plan *maker.Plan,
	placeholders []string,
	infraSnap *InfraSnapshot,
	ask AskFunc,
	clean CleanFunc,
) (map[string]string, error) {
	prompt := buildPlaceholderResolutionPrompt(plan, placeholders, infraSnap)

	resp, err := ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse the response as JSON mapping
	cleaned := clean(resp)
	var result map[string]string
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w", err)
	}

	// Filter out empty values
	filtered := make(map[string]string)
	for k, v := range result {
		v = strings.TrimSpace(v)
		if v != "" && !strings.HasPrefix(v, "<") {
			filtered[k] = v
		}
	}

	return filtered, nil
}

// buildPlaceholderResolutionPrompt creates the prompt for the LLM to resolve placeholders.
func buildPlaceholderResolutionPrompt(plan *maker.Plan, placeholders []string, infraSnap *InfraSnapshot) string {
	var b strings.Builder

	b.WriteString("You are analyzing a cloud deployment plan that has unresolved placeholder values.\n")
	b.WriteString("Your task is to determine the correct values for these placeholders based on the plan context and existing infrastructure.\n\n")

	// Show existing infrastructure
	if infraSnap != nil {
		b.WriteString("## Existing AWS Infrastructure\n")
		if infraSnap.AccountID != "" {
			b.WriteString(fmt.Sprintf("- Account ID: %s\n", infraSnap.AccountID))
		}
		if infraSnap.Region != "" {
			b.WriteString(fmt.Sprintf("- Region: %s\n", infraSnap.Region))
		}
		if infraSnap.VPC != nil {
			b.WriteString(fmt.Sprintf("- VPC ID: %s\n", infraSnap.VPC.VPCID))
			if len(infraSnap.VPC.Subnets) > 0 {
				b.WriteString(fmt.Sprintf("- Subnets: %s\n", strings.Join(infraSnap.VPC.Subnets, ", ")))
			}
		}
		if len(infraSnap.SecurityGroups) > 0 {
			sgList := make([]string, 0, len(infraSnap.SecurityGroups))
			for _, sg := range infraSnap.SecurityGroups {
				sgList = append(sgList, fmt.Sprintf("%s (%s)", sg.Name, sg.ID))
			}
			b.WriteString(fmt.Sprintf("- Security Groups: %s\n", strings.Join(sgList, ", ")))
		}
		b.WriteString("\n")
	}

	// Show the plan summary
	b.WriteString("## Deployment Plan Summary\n")
	b.WriteString(fmt.Sprintf("%s\n\n", plan.Summary))

	// Show the unresolved placeholders
	b.WriteString("## Unresolved Placeholders\n")
	for _, p := range placeholders {
		b.WriteString(fmt.Sprintf("- <%s>\n", p))
	}
	b.WriteString("\n")

	// Show relevant command context
	b.WriteString("## Plan Commands (showing placeholders in context)\n")
	for i, cmd := range plan.Commands {
		argsStr := strings.Join(cmd.Args, " ")
		if strings.Contains(argsStr, "<") {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, argsStr))
			if cmd.Reason != "" {
				b.WriteString(fmt.Sprintf("   Reason: %s\n", cmd.Reason))
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("## Instructions\n")
	b.WriteString("Analyze the plan and determine values for each placeholder.\n")
	b.WriteString("Rules:\n")
	b.WriteString("- If a placeholder refers to existing infrastructure (VPC_ID, SUBNET_ID), use the actual IDs from above.\n")
	b.WriteString("- If a placeholder will be created by an earlier command in the plan (via 'produces'), leave it as-is (return empty string).\n")
	b.WriteString("- If a placeholder needs a generated value (like a unique name), generate an appropriate one.\n")
	b.WriteString("- NEVER make up resource IDs that do not exist.\n\n")

	b.WriteString("## Response Format (JSON only, no markdown fences)\n")
	b.WriteString("Return a JSON object mapping placeholder names to their values:\n")
	b.WriteString("{\n")
	for i, p := range placeholders {
		if i < len(placeholders)-1 {
			b.WriteString(fmt.Sprintf("  \"%s\": \"<value or empty string>\",\n", p))
		} else {
			b.WriteString(fmt.Sprintf("  \"%s\": \"<value or empty string>\"\n", p))
		}
	}
	b.WriteString("}\n")

	return b.String()
}

// HasUnresolvedPlaceholders checks if a plan still has unresolved placeholder tokens.
func HasUnresolvedPlaceholders(plan *maker.Plan) bool {
	return len(extractPlaceholdersFromPlan(plan)) > 0
}

// AllPlaceholdersAreProduced returns true when every placeholder token is expected
// to be provided by an earlier command via `produces` bindings.
func AllPlaceholdersAreProduced(plan *maker.Plan, placeholders []string) bool {
	if plan == nil || len(placeholders) == 0 {
		return false
	}

	produced := make(map[string]struct{})
	for _, cmd := range plan.Commands {
		for key := range cmd.Produces {
			k := strings.TrimSpace(key)
			if k == "" {
				continue
			}
			produced[k] = struct{}{}
		}
	}

	if len(produced) == 0 {
		return false
	}

	for _, placeholder := range placeholders {
		token := strings.TrimSpace(placeholder)
		if token == "" {
			continue
		}
		if _, ok := produced[token]; !ok {
			return false
		}
	}

	return true
}

// ApplyStaticInfraBindings applies only the "static" infrastructure bindings that
// are always known regardless of whether a new VPC is being created. This includes
// AMI_ID, ACCOUNT_ID, and REGION which come from the infra scan.
// This function should be called even when --new-vpc is used.
func ApplyStaticInfraBindings(plan *maker.Plan, infraSnap *InfraSnapshot) *maker.Plan {
	if plan == nil || infraSnap == nil {
		return plan
	}

	bindings := make(map[string]string)

	// Account ID - always known
	if infraSnap.AccountID != "" {
		bindings["ACCOUNT_ID"] = infraSnap.AccountID
		bindings["AWS_ACCOUNT_ID"] = infraSnap.AccountID
	}

	// Region - always known
	if infraSnap.Region != "" {
		bindings["REGION"] = infraSnap.Region
		bindings["AWS_REGION"] = infraSnap.Region
	}

	// AMI ID - always known (fetched from SSM during infra scan)
	if infraSnap.LatestAMI != "" {
		bindings["AMI_ID"] = infraSnap.LatestAMI
		bindings["AMI"] = infraSnap.LatestAMI
		bindings["IMAGE_ID"] = infraSnap.LatestAMI
	}

	if len(bindings) == 0 {
		return plan
	}

	return applyBindingsToPlan(plan, bindings)
}

// ApplyEnvVarBindings deterministically resolves env-var placeholders like
// <DISCORD_BOT_TOKEN> using the actual values the user provided. This avoids
// relying on the LLM to map them (which fails when the API times out).
func ApplyEnvVarBindings(plan *maker.Plan, envVars map[string]string) *maker.Plan {
	if plan == nil || len(envVars) == 0 {
		return plan
	}
	// normalize {{KEY}}, ${KEY}, etc. to <KEY> before matching
	plan = normalizePlaceholderFormats(plan)

	bindings := make(map[string]string, len(envVars))
	for k, v := range envVars {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			bindings[k] = v
		}
	}
	if len(bindings) == 0 {
		return plan
	}
	return applyBindingsToPlan(plan, bindings)
}

// GetUnresolvedPlaceholders returns the list of unresolved placeholder tokens in a plan.
func GetUnresolvedPlaceholders(plan *maker.Plan) []string {
	return extractPlaceholdersFromPlan(plan)
}

// FilterRuntimeInjectedTokens removes placeholder tokens that will be resolved
// at runtime via env var injection (importSecretLikeEnvVarsIntoBindings) or
// explicit executor bindings (e.g. DIGITALOCEAN_ACCESS_TOKEN).
// envVars is the list of user-provided env var keys from the deploy request.
func FilterRuntimeInjectedTokens(placeholders []string, envVars []string) []string {
	// Build lookup of env var keys the user provided
	provided := make(map[string]bool, len(envVars))
	for _, kv := range envVars {
		k, _, ok := strings.Cut(kv, "=")
		if ok {
			provided[strings.ToUpper(strings.TrimSpace(k))] = true
		}
	}

	var out []string
	for _, tok := range placeholders {
		upper := strings.ToUpper(strings.TrimSpace(tok))
		if provided[upper] {
			continue // user-provided env var, resolved at runtime
		}
		// Provider credential tokens are always injected by the executor
		if isProviderCredentialToken(upper) {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// isProviderCredentialToken returns true for tokens that are injected by
// the executor at runtime (provider API creds). Generic pattern so new
// providers work without code changes.
func isProviderCredentialToken(tok string) bool {
	// Exact known tokens
	switch tok {
	case "DIGITALOCEAN_ACCESS_TOKEN", "DO_API_TOKEN":
		return true
	case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN":
		return true
	case "HCLOUD_TOKEN", "HETZNER_API_TOKEN":
		return true
	case "RAILWAY_API_TOKEN", "RAILWAY_TOKEN":
		return true
	}
	// Generic suffix patterns: *_ACCESS_TOKEN, *_API_TOKEN, *_API_KEY for provider prefixes
	for _, suffix := range []string{"_ACCESS_TOKEN", "_API_TOKEN"} {
		if strings.HasSuffix(tok, suffix) {
			prefix := strings.TrimSuffix(tok, suffix)
			if isCloudProviderPrefix(prefix) {
				return true
			}
		}
	}
	return false
}

// isCloudProviderPrefix returns true for known cloud provider env var prefixes.
func isCloudProviderPrefix(prefix string) bool {
	switch prefix {
	case "DIGITALOCEAN", "DO", "AWS", "GOOGLE", "GCP", "AZURE",
		"HCLOUD", "HETZNER", "CLOUDFLARE", "RAILWAY", "LINODE", "VULTR":
		return true
	}
	return false
}
