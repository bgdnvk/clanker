package deploy

import (
	"fmt"
	"strings"
)

func IsOpenClawRepo(p *RepoProfile, deep *DeepAnalysis) bool {
	if p == nil {
		return false
	}
	repo := strings.ToLower(strings.TrimSpace(p.RepoURL))
	if strings.Contains(repo, "openclaw/openclaw") {
		return true
	}
	if strings.Contains(strings.ToLower(p.Summary), "openclaw") {
		return true
	}
	for name := range p.KeyFiles {
		if strings.EqualFold(strings.TrimSpace(name), "openclaw.mjs") {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(name), "docker-setup.sh") {
			return true
		}
	}
	if deep != nil {
		if strings.Contains(strings.ToLower(deep.AppDescription), "openclaw") {
			return true
		}
		for _, s := range deep.Services {
			if strings.Contains(strings.ToLower(s), "openclaw") {
				return true
			}
		}
	}
	return false
}

func OpenClawPreferredBootstrapScripts() []string {
	return []string{"docker-setup.sh", "setup-podman.sh"}
}

func OpenClawComposeHardEnvVars() []string {
	return []string{"OPENCLAW_CONFIG_DIR", "OPENCLAW_WORKSPACE_DIR"}
}

func OpenClawArchitectPromptGCP() string {
	return `
## GCP Options to Consider
1. **gcp-compute-engine** — VM + Docker Compose (best for long-running gateway services) (~$8-30/mo)
2. **cloud-run** — managed containers (good for stateless HTTP apps, less ideal for stateful local-first gateway workflows)
3. **gke** — Kubernetes (overkill unless explicitly requested)

## GCP Services
- Compute Engine for always-on gateway
- Persistent disk for OpenClaw state/workspace
- Secret Manager for API keys and channel tokens
- Cloud DNS + HTTPS LB only if public exposure is required

## Deployment CLI
All commands must use gcloud CLI only.

## Cost Estimation
Estimate the MONTHLY cost in USD.

## Response Format (JSON only, no markdown fences)
{
	"provider": "gcp",
	"method": "gcp-compute-engine",
	"reasoning": "OpenClaw is a long-running gateway with persistent state and channel credentials. A Compute Engine VM with Docker Compose is the most reliable and simplest path.",
	"alternatives": [
		{"method": "cloud-run", "why_not": "Less suitable for persistent workspace/state and interactive gateway workflows"},
		{"method": "gke", "why_not": "Operationally complex for this use case"}
	],
	"buildSteps": [
		"Create Compute Engine VM",
		"Install Docker + Docker Compose",
		"Clone repo and configure .env",
		"Run docker compose build && docker compose up -d"
	],
	"runCmd": "docker compose up -d openclaw-gateway",
	"notes": ["Expose only required ports", "Persist OpenClaw config/workspace on disk"],
	"cpuMemory": "e2-standard-2",
	"needsAlb": false,
	"useApiGateway": false,
	"needsDb": false,
	"dbService": "",
	"estMonthly": "$12-25",
	"costBreakdown": ["Compute Engine VM", "Persistent disk", "Network egress"]
}`
}

func OpenClawArchitectPromptAzure() string {
	return `
## Azure Options to Consider
1. **azure-vm** — VM + Docker Compose (best for long-running gateway services) (~$10-35/mo)
2. **azure-container-apps** — managed containers (good for stateless services)
3. **aks** — Kubernetes (overkill unless explicitly requested)

## Azure Services
- VM for always-on gateway runtime
- Managed disk for persistent OpenClaw state/workspace
- Key Vault for API keys and channel tokens

## Deployment CLI
All commands must use az CLI only.

## Cost Estimation
Estimate the MONTHLY cost in USD.

## Response Format (JSON only, no markdown fences)
{
	"provider": "azure",
	"method": "azure-vm",
	"reasoning": "OpenClaw runs best as an always-on gateway with persistent local state. Azure VM with Docker Compose is the most direct and operationally simple option.",
	"alternatives": [
		{"method": "azure-container-apps", "why_not": "Less ideal for persistent local-first runtime patterns"},
		{"method": "aks", "why_not": "Unnecessary complexity for this workload"}
	],
	"buildSteps": [
		"Create resource group and VM",
		"Install Docker + Docker Compose",
		"Clone repo and configure .env",
		"Run docker compose build && docker compose up -d"
	],
	"runCmd": "docker compose up -d openclaw-gateway",
	"notes": ["Persist OpenClaw directories on disk", "Restrict inbound network rules"],
	"cpuMemory": "Standard_B2s",
	"needsAlb": false,
	"useApiGateway": false,
	"needsDb": false,
	"dbService": "",
	"estMonthly": "$12-30",
	"costBreakdown": ["VM", "Managed disk", "Public IP/Bandwidth"]
}`
}

func OpenClawGCPComputeEnginePrompt(p *RepoProfile, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy OpenClaw using GCP Compute Engine (VM + Docker Compose):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for VM/network/firewall resources\n", resourcePrefix))
	b.WriteString("1. Create a VPC firewall rule for required inbound ports\n")
	b.WriteString("2. Create a Compute Engine VM (Ubuntu LTS)\n")
	b.WriteString("3. Install Docker and Docker Compose on the VM\n")
	b.WriteString(fmt.Sprintf("4. Clone repository: %s\n", p.RepoURL))
	b.WriteString("5. Create .env with gateway token and required secrets\n")
	b.WriteString("6. Create persistent directories for OpenClaw config/workspace\n")
	b.WriteString("7. Build and start with: docker compose build && docker compose up -d openclaw-gateway\n")
	b.WriteString("8. Verify gateway health and endpoint readiness\n")
	return b.String()
}

func OpenClawAzureVMPrompt(p *RepoProfile, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy OpenClaw using Azure VM (Docker Compose):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for resource group/NSG/VM resources\n", resourcePrefix))
	b.WriteString("1. Create resource group and network security group with least-privilege inbound rules\n")
	b.WriteString("2. Create Ubuntu VM with managed disk\n")
	b.WriteString("3. Install Docker and Docker Compose\n")
	b.WriteString(fmt.Sprintf("4. Clone repository: %s\n", p.RepoURL))
	b.WriteString("5. Create .env with gateway token and required secrets\n")
	b.WriteString("6. Create persistent directories for OpenClaw config/workspace\n")
	b.WriteString("7. Build and start with: docker compose build && docker compose up -d openclaw-gateway\n")
	b.WriteString("8. Verify gateway health and endpoint readiness\n")
	return b.String()
}

func ApplyOpenClawArchitectureDefaults(targetProvider string, opts *DeployOptions, p *RepoProfile, deep *DeepAnalysis, arch *ArchitectDecision) bool {
	if arch == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(targetProvider))
	if provider != "" && provider != "aws" {
		return false
	}
	if !IsOpenClawRepo(p, deep) {
		return false
	}

	// Only override when the user didn't explicitly request a different target.
	// (Historically, opts.Target defaulted to "fargate" when unspecified.)
	if opts == nil {
		arch.Provider = "aws"
		arch.Method = "ec2"
		arch.Reasoning = "OpenClaw is a stateful, long-running gateway; EC2 is the safest default on AWS for persistent local state + websocket workloads"
		return true
	}
	target := strings.TrimSpace(opts.Target)
	if target == "" || target == "fargate" {
		arch.Provider = "aws"
		arch.Method = "ec2"
		arch.Reasoning = "OpenClaw is a stateful, long-running gateway; EC2 is the safest default on AWS for persistent local state + websocket workloads"
		return true
	}
	return false
}

func AppendOpenClawDeploymentRequirements(b *strings.Builder, p *RepoProfile, deep *DeepAnalysis) bool {
	if b == nil {
		return false
	}
	if !IsOpenClawRepo(p, deep) {
		return false
	}
	b.WriteString("\n## OpenClaw Deployment Requirements\n")
	b.WriteString("- Runtime must be Node.js 22+\n")
	b.WriteString("- Prefer Docker-based gateway deployment\n")
	b.WriteString("- Persist OpenClaw state and workspace directories\n")
	b.WriteString("- Configure gateway token via environment variable\n")
	b.WriteString("- Expose gateway port (default 18789) intentionally and securely\n")
	b.WriteString("- For AWS EC2+ALB deployments, ALWAYS create CloudFront in front of ALB and set HTTPS as the primary endpoint\n")
	b.WriteString("- Plan output must include HTTPS URL (CloudFront domain) used for pairing; ALB HTTP URL is fallback/debug only\n")
	b.WriteString("- Use environment variables for channel/provider secrets; avoid committing tokens\n")
	if p != nil && len(p.BootstrapScripts) > 0 {
		b.WriteString("- This repo has bootstrap scripts; for first-run, run docker onboarding/setup before starting the gateway\n")
	}
	return true
}

func applyOpenClawUserDataValidation(out *deterministicValidation, script string, usesCompose bool, usesDockerRun bool) {
	if out == nil {
		return
	}
	if strings.TrimSpace(script) == "" {
		return
	}

	lower := strings.ToLower(script)
	if usesCompose {
		// Expect either docker-setup.sh or onboard.
		if !strings.Contains(lower, "docker-setup.sh") && !strings.Contains(lower, "openclaw-cli") && !strings.Contains(lower, " onboar") {
			out.Warnings = append(out.Warnings, "suggestion: include onboarding command /opt/openclaw/bin/clawctl onboard in user-data")
			out.Fixes = append(out.Fixes, "Run ./docker-setup.sh (or docker compose run --rm openclaw-cli onboard) before docker compose up -d openclaw-gateway")
		}

		// OpenClaw compose expects config/workspace host dirs.
		missing := missingEnvVarsInScript(script, OpenClawComposeHardEnvVars())
		if len(missing) > 0 {
			out.Warnings = append(out.Warnings, "suggestion: include required OpenClaw mount env var "+strings.Join(missing, ", ")+" in user-data")
			out.Fixes = append(out.Fixes, "Set OPENCLAW_CONFIG_DIR and OPENCLAW_WORKSPACE_DIR to real host paths before docker compose up")
		}
	}

	// If neither compose nor docker run shows up, still probably broken.
	if !usesCompose && !usesDockerRun {
		out.Warnings = append(out.Warnings, "OpenClaw repo detected but user-data does not appear to start the gateway")
	}
}
