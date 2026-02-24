package deploy

import (
	"context"
	"fmt"
	"strings"
)

type PlanRepairContext struct {
	Provider string
	Method   string
	RepoURL  string

	GCPProject          string
	AzureSubscriptionID string
	CloudflareAccountID string

	// App/runtime hints (kept compact)
	Ports               []int
	ComposeHardEnvVars  []string
	RequiredEnvVarNames []string
	// RequiredLaunchOps constrains the repair to include at least one workload "launch" step.
	// Each entry is "<service> <operation>" (e.g. "ec2 run-instances", "ecs create-service").
	RequiredLaunchOps []string

	// AWS infra hints (optional)
	Region  string
	Account string
	VPCID   string
	Subnets []string
	AMIID   string
}

type PlanRepairAgent struct {
	ask   AskFunc
	clean CleanFunc
	logf  func(string, ...any)
}

func NewPlanRepairAgent(ask AskFunc, clean CleanFunc, logf func(string, ...any)) *PlanRepairAgent {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &PlanRepairAgent{ask: ask, clean: clean, logf: logf}
}

func (a *PlanRepairAgent) Repair(ctx context.Context, planJSON string, v *PlanValidation, c PlanRepairContext) (string, error) {
	if a == nil || a.ask == nil || a.clean == nil {
		return "", fmt.Errorf("plan repair agent not configured")
	}
	if strings.TrimSpace(planJSON) == "" {
		return "", fmt.Errorf("missing plan json")
	}
	if v == nil {
		return "", fmt.Errorf("missing validation")
	}

	prompt := a.buildPrompt(planJSON, v, c)
	resp, err := a.ask(ctx, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(a.clean(resp)), nil
}

func (a *PlanRepairAgent) buildPrompt(planJSON string, v *PlanValidation, c PlanRepairContext) string {
	issues := uniqueStrings(v.Issues)
	fixes := uniqueStrings(v.Fixes)
	warnings := uniqueStrings(v.Warnings)
	if len(issues) > 20 {
		issues = issues[:20]
	}
	if len(fixes) > 20 {
		fixes = fixes[:20]
	}
	if len(warnings) > 10 {
		warnings = warnings[:10]
	}

	// Cap embedded plan size to avoid truncation.
	p := strings.TrimSpace(planJSON)
	if len(p) > 16000 {
		p = p[:16000] + "…"
	}

	var b strings.Builder
	prov := strings.ToLower(strings.TrimSpace(c.Provider))
	if prov == "" {
		prov = "aws"
	}
	b.WriteString("You are a deployment plan repair agent. Your job is to REWRITE the given deployment plan JSON into a corrected plan.\n\n")
	b.WriteString("Hard rules:\n")
	b.WriteString("- Output ONLY one JSON object (no markdown, no prose, no code fences).\n")
	b.WriteString("- Top-level MUST be a plan object with a NON-EMPTY commands array.\n")
	b.WriteString("- Keep plan.question and plan.summary SHORT (<=200 chars each).\n")
	b.WriteString("- Preserve intent but fix missing steps, ordering, and placeholder binding.\n")
	b.WriteString("- If you use any placeholder token like <X>, an earlier command MUST have produces { \"X\": \"$.path\" }.\n")
	switch prov {
	case "cloudflare":
		b.WriteString("- Commands must be Cloudflare-only: args start with 'wrangler' or 'cloudflared', OR API calls as [METHOD, /endpoint, optional-json-body].\n")
		b.WriteString("- Do NOT use npx/node/npm/curl or any shell operators/pipes/redirects.\n\n")
	case "gcp":
		b.WriteString("- Commands must be gcloud-only (args may start with 'gcloud' or start at the group like 'compute').\n")
		b.WriteString("- Do NOT use terraform/curl or any shell operators/pipes/redirects.\n\n")
	case "azure":
		b.WriteString("- Commands must be az-only (args may start with 'az' or start at the group like 'vm').\n")
		b.WriteString("- Do NOT use terraform/curl or any shell operators/pipes/redirects.\n\n")
	default:
		b.WriteString("- Commands must be AWS CLI only (no shell operators/pipes/redirects).\n")
		b.WriteString("- Do NOT include --profile/--region/--no-cli-pager in args (runner injects).\n\n")
	}

	if len(c.RequiredLaunchOps) > 0 {
		b.WriteString("Launch requirement:\n")
		b.WriteString("- The repaired plan MUST include at least one command whose first two args match one of: " + strings.Join(c.RequiredLaunchOps, " | ") + "\n\n")
	}

	// Targeted repair hints for common AWS launch patterns.
	if prov == "aws" {
		needsEC2Run := false
		for _, op := range c.RequiredLaunchOps {
			if strings.EqualFold(strings.TrimSpace(op), "ec2 run-instances") {
				needsEC2Run = true
				break
			}
		}
		if needsEC2Run {
			b.WriteString("EC2 launch hint (MUST include):\n")
			b.WriteString("- Include an 'ec2 run-instances' command with produces {\"INSTANCE_ID\":\"$.Instances[0].InstanceId\"}.\n")
			b.WriteString("- If you reference an instance profile, include the IAM role + instance profile creation commands first.\n")
			b.WriteString("- If using docker-compose, ensure required env vars are set in user-data or a written .env file.\n")
			b.WriteString("Example shape (adjust placeholders/args as needed; JSON plan only in your output):\n")
			b.WriteString("{\n")
			b.WriteString("  \"args\": [\"ec2\",\"run-instances\",\"--image-id\",\"<AMI_ID>\",\"--instance-type\",\"t3.small\",\"--subnet-id\",\"<SUBNET_ID>\",\"--security-group-ids\",\"<EC2_SG_ID>\",\"--iam-instance-profile\",\"Name=<INSTANCE_PROFILE_NAME>\",\"--user-data\",\"<USER_DATA>\",\"--tag-specifications\",\"ResourceType=instance,Tags=[{Key=Name,Value=<NAME>},{Key=Project,Value=<PROJECT>}]\"],\n")
			b.WriteString("  \"reason\": \"Launch compute to run the application\",\n")
			b.WriteString("  \"produces\": { \"INSTANCE_ID\": \"$.Instances[0].InstanceId\" }\n")
			b.WriteString("}\n\n")
		}
	}

	b.WriteString("Repair context (use as constraints, keep consistent):\n")
	if strings.TrimSpace(c.Provider) != "" {
		b.WriteString("- provider: " + strings.TrimSpace(c.Provider) + "\n")
	}
	if strings.TrimSpace(c.Method) != "" {
		b.WriteString("- method: " + strings.TrimSpace(c.Method) + "\n")
	}
	if strings.TrimSpace(c.RepoURL) != "" {
		b.WriteString("- repo: " + strings.TrimSpace(c.RepoURL) + "\n")
	}
	if strings.TrimSpace(c.GCPProject) != "" {
		b.WriteString("- gcp project: " + strings.TrimSpace(c.GCPProject) + "\n")
	}
	if strings.TrimSpace(c.AzureSubscriptionID) != "" {
		b.WriteString("- azure subscription: " + strings.TrimSpace(c.AzureSubscriptionID) + "\n")
	}
	if strings.TrimSpace(c.CloudflareAccountID) != "" {
		b.WriteString("- cloudflare account: " + strings.TrimSpace(c.CloudflareAccountID) + "\n")
	}
	if len(c.Ports) > 0 {
		b.WriteString(fmt.Sprintf("- ports: %v\n", c.Ports))
	}
	if len(c.ComposeHardEnvVars) > 0 {
		b.WriteString("- docker-compose hard-required env vars: " + strings.Join(c.ComposeHardEnvVars, ", ") + "\n")
	}
	if len(c.RequiredEnvVarNames) > 0 {
		b.WriteString("- required env var names: " + strings.Join(c.RequiredEnvVarNames, ", ") + "\n")
	}
	if len(c.RequiredLaunchOps) > 0 {
		b.WriteString("- MUST include a workload launch step matching one of: " + strings.Join(c.RequiredLaunchOps, " | ") + "\n")
	}
	if prov == "aws" {
		if strings.TrimSpace(c.Region) != "" {
			b.WriteString("- aws region: " + strings.TrimSpace(c.Region) + "\n")
		}
		if strings.TrimSpace(c.Account) != "" {
			b.WriteString("- aws account: " + strings.TrimSpace(c.Account) + "\n")
		}
		if strings.TrimSpace(c.VPCID) != "" {
			b.WriteString("- vpc: " + strings.TrimSpace(c.VPCID) + "\n")
		}
		if len(c.Subnets) > 0 {
			b.WriteString("- subnets: " + strings.Join(c.Subnets, ", ") + "\n")
		}
		if strings.TrimSpace(c.AMIID) != "" {
			b.WriteString("- ami: " + strings.TrimSpace(c.AMIID) + " (prefer using this directly)\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("Plan schema (JSON):\n")
	b.WriteString("{\"version\":1,\"createdAt\":\"RFC3339\",\"provider\":\"" + prov + "\",\"question\":\"...\",\"summary\":\"...\",\"commands\":[{\"args\":[...],\"reason\":\"...\",\"produces\":{}}],\"notes\":[...]}\n\n")

	b.WriteString("Plan to repair:\n")
	b.WriteString(p)
	b.WriteString("\n\n")

	if len(issues) > 0 {
		b.WriteString("Issues:\n")
		for _, s := range issues {
			b.WriteString("- " + strings.TrimSpace(s) + "\n")
		}
		b.WriteString("\n")
	}
	if len(fixes) > 0 {
		b.WriteString("Suggested fixes:\n")
		for _, s := range fixes {
			b.WriteString("- " + strings.TrimSpace(s) + "\n")
		}
		b.WriteString("\n")
	}
	if len(warnings) > 0 {
		b.WriteString("Warnings (non-blocking):\n")
		for _, s := range warnings {
			b.WriteString("- " + strings.TrimSpace(s) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Now output the corrected plan JSON object ONLY.\n")
	return b.String()
}
