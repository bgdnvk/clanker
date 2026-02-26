package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

func RepairPlanPageWithLLM(
	ctx context.Context,
	ask AskFunc,
	clean CleanFunc,
	provider string,
	deploymentIntent string,
	projectSummary string,
	raw string,
	formatHint string,
	logf func(string, ...any),
) (*PlanPage, string, error) {
	if ask == nil || clean == nil {
		return nil, "", fmt.Errorf("repair functions are not configured")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "aws"
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", fmt.Errorf("empty input for page repair")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	const maxAttempts = 2
	candidate := raw
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		prompt := buildPlanPageJSONRepairPrompt(provider, deploymentIntent, projectSummary, candidate, formatHint)
		resp, err := ask(ctx, prompt)
		if err != nil {
			return nil, "", err
		}
		cleaned := strings.TrimSpace(clean(resp))
		page, pErr := ParsePlanPage(cleaned)
		if pErr == nil {
			return page, cleaned, nil
		}
		logf("[deploy] warning: plan page JSON-fix attempt %d failed (%v)", attempt, pErr)
		candidate = cleaned
	}

	return nil, candidate, fmt.Errorf("page remains unparseable after JSON-fix attempts")
}

func RepairPlanJSONWithLLM(
	ctx context.Context,
	ask AskFunc,
	clean CleanFunc,
	deploymentIntent string,
	projectSummary string,
	raw string,
	baselinePlanJSON string,
	issues []string,
	requiredLaunchOps []string,
	logf func(string, ...any),
) (*maker.Plan, error) {
	if ask == nil || clean == nil {
		return nil, fmt.Errorf("repair functions are not configured")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty input for plan repair")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	const maxAttempts = 3
	candidate := raw
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		prompt := buildPlanJSONRepairPrompt(deploymentIntent, projectSummary, candidate, baselinePlanJSON, issues, requiredLaunchOps)
		resp, err := ask(ctx, prompt)
		if err != nil {
			return nil, err
		}
		cleaned := strings.TrimSpace(clean(resp))
		plan, pErr := maker.ParsePlan(cleaned)
		if pErr == nil {
			return plan, nil
		}
		logf("[deploy] warning: plan JSON-fix attempt %d failed (%v)", attempt, pErr)
		candidate = cleaned
	}

	return nil, fmt.Errorf("plan remains unparseable after JSON-fix attempts")
}

func RunGenericPlanIntegrityPassWithLLM(
	ctx context.Context,
	ask AskFunc,
	clean CleanFunc,
	plan *maker.Plan,
	deploymentIntent string,
	projectSummary string,
	requiredLaunchOps []string,
	logf func(string, ...any),
) (*maker.Plan, error) {
	if plan == nil || len(plan.Commands) == 0 {
		return nil, nil
	}
	if ask == nil || clean == nil {
		return nil, fmt.Errorf("integrity pass is not configured")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	baseJSONBytes, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	baseJSON := string(baseJSONBytes)

	const maxAttempts = 2
	candidate := baseJSON
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		prompt := buildGenericPlanIntegrityPrompt(deploymentIntent, projectSummary, candidate, baseJSON, requiredLaunchOps)
		resp, askErr := ask(ctx, prompt)
		if askErr != nil {
			return nil, askErr
		}
		fixedRaw := strings.TrimSpace(clean(resp))
		fixedPlan, parseErr := maker.ParsePlan(fixedRaw)
		if parseErr == nil {
			fixedPlan.Provider = plan.Provider
			fixedPlan.Question = plan.Question
			if fixedPlan.CreatedAt.IsZero() {
				fixedPlan.CreatedAt = plan.CreatedAt
			}
			if fixedPlan.Version == 0 {
				fixedPlan.Version = maker.CurrentPlanVersion
			}
			return fixedPlan, nil
		}
		logf("[deploy] warning: generic integrity pass parse failed (attempt %d/%d): %v", attempt, maxAttempts, parseErr)
		candidate = fixedRaw
	}

	return nil, fmt.Errorf("generic integrity pass remained unparseable")
}

func buildPlanPageJSONRepairPrompt(provider, deploymentIntent, projectSummary, raw, formatHint string) string {
	var b strings.Builder
	b.WriteString("You are a JSON repair assistant for deployment plan pages.\n")
	b.WriteString("Rewrite the input into ONE valid JSON object matching this exact schema:\n")
	b.WriteString("{\"done\": boolean, \"summary\": string?, \"notes\": [string]?, \"commands\": [{\"args\": [string,...], \"reason\": string, \"produces\": {string:string}?}]}\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Output JSON object only. No prose/markdown.\n")
	b.WriteString("- Preserve deployment intent.\n")
	b.WriteString("- Keep command ordering from input where possible.\n")
	b.WriteString("- Keep command args as CLI tokens only; no shell wrappers.\n")
	b.WriteString("- For provider=aws, args must not include leading 'aws'.\n")
	b.WriteString("- Keep placeholders in <NAME> format, never ${NAME}.\n")
	b.WriteString("- If uncertain, keep done=false and include at least one command.\n")
	b.WriteString("- provider: " + provider + "\n")
	if strings.TrimSpace(deploymentIntent) != "" {
		b.WriteString("- deployment_intent: " + strings.TrimSpace(deploymentIntent) + "\n")
	}
	if strings.TrimSpace(projectSummary) != "" {
		b.WriteString("- project_summary: " + strings.TrimSpace(projectSummary) + "\n")
	}
	if strings.TrimSpace(formatHint) != "" {
		b.WriteString("- format_hint: " + strings.TrimSpace(formatHint) + "\n")
	}
	b.WriteString("\nInput to repair:\n")
	b.WriteString(truncateForLLMRepair(raw, 18000))
	b.WriteString("\n\nReturn repaired JSON object only.\n")
	return b.String()
}

func buildPlanJSONRepairPrompt(deploymentIntent, projectSummary, raw, baselinePlanJSON string, issues, requiredLaunchOps []string) string {
	var b strings.Builder
	b.WriteString("You are a JSON repair assistant for deployment plans.\n")
	b.WriteString("Rewrite the broken response into ONE valid plan JSON object with schema:\n")
	b.WriteString("{\"version\":1,\"createdAt\":\"RFC3339\",\"provider\":\"...\",\"question\":\"...\",\"summary\":\"...\",\"commands\":[{\"args\":[...],\"reason\":\"...\",\"produces\":{}}],\"notes\":[...]}\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Output JSON object only. No prose/markdown.\n")
	b.WriteString("- Preserve the baseline plan's existing valid commands and order unless an ISSUE requires a change.\n")
	b.WriteString("- Do not drop unrelated commands.\n")
	b.WriteString("- Keep command args as CLI tokens only; no shell wrappers.\n")
	b.WriteString("- Keep placeholders in <NAME> format, never ${NAME}.\n")
	if strings.TrimSpace(deploymentIntent) != "" {
		b.WriteString("- Deployment intent: " + strings.TrimSpace(deploymentIntent) + "\n")
	}
	if strings.TrimSpace(projectSummary) != "" {
		b.WriteString("- Project summary: " + strings.TrimSpace(projectSummary) + "\n")
	}
	if len(requiredLaunchOps) > 0 {
		b.WriteString("- Must retain at least one launch op matching: " + strings.Join(requiredLaunchOps, " | ") + "\n")
	}
	if len(issues) > 0 {
		b.WriteString("- Address these issues while preserving the rest:\n")
		max := len(issues)
		if max > 20 {
			max = 20
		}
		for i := 0; i < max; i++ {
			issue := strings.TrimSpace(issues[i])
			if issue == "" {
				continue
			}
			b.WriteString("  - " + issue + "\n")
		}
	}
	b.WriteString("\nBaseline valid plan (preserve as much as possible):\n")
	b.WriteString(truncateForLLMRepair(strings.TrimSpace(baselinePlanJSON), 22000))
	b.WriteString("\n\nBroken response to repair:\n")
	b.WriteString(truncateForLLMRepair(strings.TrimSpace(raw), 16000))
	b.WriteString("\n\nReturn repaired plan JSON object only.\n")
	return b.String()
}

func buildGenericPlanIntegrityPrompt(deploymentIntent, projectSummary, candidatePlanJSON string, baselinePlanJSON string, requiredLaunchOps []string) string {
	var b strings.Builder
	b.WriteString("You are a deployment plan command-integrity reviewer.\n")
	b.WriteString("This plan runs sequentially in one-click deployment.\n")
	b.WriteString("Balanced mode: fix malformed command integrity and small execution-safety issues, but do NOT redesign architecture.\n")
	b.WriteString("Review and fix across ANY provider/environment (AWS/GCP/Azure/Cloudflare/etc).\n\n")
	if strings.TrimSpace(deploymentIntent) != "" {
		b.WriteString("Deployment intent:\n")
		b.WriteString(strings.TrimSpace(deploymentIntent))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(projectSummary) != "" {
		b.WriteString("Project summary:\n")
		b.WriteString(strings.TrimSpace(projectSummary))
		b.WriteString("\n\n")
	}
	b.WriteString("Output rules:\n")
	b.WriteString("- Output ONLY one valid plan JSON object. No prose/markdown/code fences.\n")
	b.WriteString("- Keep command order and deployment intent.\n")
	b.WriteString("- Do NOT change architecture/provider/method.\n")
	b.WriteString("- Do NOT remove unrelated commands.\n")
	b.WriteString("- Prefer minimal diff. Keep question/summary concise and aligned with baseline intent.\n")
	b.WriteString("- Fix malformed command arguments or invalid command tokenization.\n")
	b.WriteString("- Also apply small safety corrections when they are direct command-level fixes (not architecture changes).\n")
	b.WriteString("  Examples of allowed small safety corrections:\n")
	b.WriteString("  * Correct malformed waiters/filters and obvious no-op wait commands.\n")
	b.WriteString("  * Keep flags outside scripts (e.g., do not merge CLI flags into --user-data body).\n")
	b.WriteString("  * Keep run-instances user-data as script content only; preserve separate CLI flags.\n")
	b.WriteString("  * Replace obvious placeholder secrets like 'changeme' with placeholders (<OPENCLAW_GATEWAY_TOKEN>) if needed.\n")
	b.WriteString("  * Keep existing launch chain; do not drop required front-door steps if already present.\n")
	b.WriteString("- NOT allowed: swapping deployment method, introducing unrelated services, or broad resource churn.\n")
	b.WriteString("- Keep placeholders in angle form <NAME>; never ${NAME} or $NAME.\n")
	b.WriteString("- Ensure top-level has non-empty commands array.\n")
	b.WriteString("- For ec2 run-instances, keep --tag-specifications as a CLI arg, never merged inside --user-data script content.\n")
	b.WriteString("- For waiters, ensure service wait commands are syntactically valid and not placeholder/fake filters.\n")
	b.WriteString("- For cloudfront create-distribution, keep --distribution-config as one valid JSON string arg.\n")
	b.WriteString("- cloudfront create-distribution does not support --tags; if tagging is required use create-distribution-with-tags or a separate tagging command.\n")
	b.WriteString("\nAcceptance checklist (MUST satisfy before you return):\n")
	b.WriteString("- If command args include ec2 run-instances, verify no trailing CLI flags are embedded into user-data script text.\n")
	b.WriteString("- If any ALB-related SG rule exists (ALB SG or source-group to app SG), ensure ALB chain exists in commands: create-load-balancer + create-target-group + create-listener.\n")
	b.WriteString("- If ALB exists and plan references OpenClaw, include CloudFront HTTPS chain (create-distribution + wait distribution-deployed + produces CLOUDFRONT_DOMAIN/HTTPS_URL) unless already present.\n")
	b.WriteString("- If producing HTTPS_URL for CloudFront, it must be a full URL (https://...) not just a bare domain.\n")
	b.WriteString("- If create-load-balancer and create-listener both exist, include elbv2 wait load-balancer-available in between.\n")
	b.WriteString("- If run-instances and register-targets both exist, include ec2 wait instance-running in between.\n")
	b.WriteString("- If SSH ingress is 0.0.0.0/0, replace with <ADMIN_CIDR> placeholder in that command (do not keep world-open SSH).\n")
	b.WriteString("- For EC2 bootstrap connectivity, prefer --associate-public-ip-address on run-instances when no explicit NAT/private-network path is present in plan.\n")
	b.WriteString("- Normalize known docker-compose binary URL typo: use docker-compose-linux-x86_64 (lowercase linux).\n")
	b.WriteString("- Preserve produced bindings used by downstream commands (do not break placeholder chains).\n")
	b.WriteString("- If uncertain, preserve baseline command and only repair syntax/shape around it.\n")
	if len(requiredLaunchOps) > 0 {
		b.WriteString("- Must retain at least one launch op matching: " + strings.Join(requiredLaunchOps, " | ") + "\n")
	}
	b.WriteString("\nBaseline plan (preserve as much as possible):\n")
	b.WriteString(truncateForLLMRepair(strings.TrimSpace(baselinePlanJSON), 26000))
	b.WriteString("\n\nCandidate plan to integrity-check/fix:\n")
	b.WriteString(truncateForLLMRepair(strings.TrimSpace(candidatePlanJSON), 26000))
	b.WriteString("\n\nReturn corrected plan JSON object only.\n")
	return b.String()
}

func truncateForLLMRepair(input string, max int) string {
	v := strings.TrimSpace(input)
	if max <= 0 || len(v) <= max {
		return v
	}
	return strings.TrimSpace(v[:max]) + "…"
}
