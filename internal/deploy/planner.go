package deploy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DeployStrategy controls how we deploy
type DeployStrategy struct {
	Provider  string // aws, cloudflare
	Method    string // ecs-fargate, ec2, lambda, s3-cloudfront, cf-pages, cf-workers, cf-containers
	Region    string
	Reasoning string // LLM's reasoning for the choice
}

// ArchitectDecision is the structured JSON response from the architect LLM call
type ArchitectDecision struct {
	Provider      string   `json:"provider"`                // aws, cloudflare
	Method        string   `json:"method"`                  // ecs-fargate, ec2, eks, lambda, s3-cloudfront, cf-pages, cf-workers, cf-containers
	Reasoning     string   `json:"reasoning"`               // why this architecture
	BuildSteps    []string `json:"buildSteps"`              // how to build it
	RunCmd        string   `json:"runCmd"`                  // simplest way to start it locally
	Notes         []string `json:"notes"`                   // gotchas, warnings
	CpuMemory     string   `json:"cpuMemory"`               // e.g. "256/512", "512/1024", or instance type for EC2
	NeedsALB      bool     `json:"needsAlb"`                // whether to put an ALB in front
	UseAPIGateway bool     `json:"useApiGateway"`           // whether to use API Gateway instead of ALB
	NeedsDB       bool     `json:"needsDb"`                 // whether to provision a managed DB
	DBService     string   `json:"dbService"`               // rds-postgres, elasticache-redis, etc
	EstMonthly    string   `json:"estMonthly"`              // estimated monthly cost e.g. "$15-25"
	CostBreakdown []string `json:"costBreakdown,omitempty"` // per-service cost breakdown
}

// ArchitectPrompt builds the prompt for the architect LLM call
func ArchitectPrompt(p *RepoProfile) string {
	profileJSON, _ := json.MarshalIndent(p, "", "  ")

	return fmt.Sprintf(`You are an expert cloud architect. Given this repo analysis, decide the simplest and cheapest way to deploy and run this application on AWS.

## Repo Analysis
%s

## Your Task
1. Pick the best AWS deployment method (ecs-fargate, ec2, lambda, s3-cloudfront, app-runner, lightsail)
2. Explain WHY in 1-2 sentences
3. List the build steps needed (clone, install deps, build, containerize, etc.)
4. Give the simplest command to run it locally for testing
5. Note any gotchas (monorepo quirks, env vars that MUST be set, ports, etc.)
6. Suggest CPU/memory sizing
7. Decide if it needs an ALB and/or managed database

## Rules
- PREFER the simplest option that works. Don't over-engineer.
- If it has a Dockerfile, use it. Don't reinvent the build.
- If it's a static site (React/Vite SPA with no server), use S3+CloudFront.
- If it needs a server (API, WebSocket, SSR), use ECS Fargate or App Runner.
- For tiny scripts or cron jobs, consider Lambda.
- ALWAYS respond with valid JSON only, no markdown fences.

## Response Format
{
  "provider": "aws",
  "method": "ecs-fargate",
  "reasoning": "...",
  "buildSteps": ["step1", "step2"],
  "runCmd": "docker compose up",
  "notes": ["note1"],
  "cpuMemory": "256/512",
  "needsAlb": true,
  "needsDb": false,
  "dbService": ""
}`, string(profileJSON))
}

// ParseArchitectDecision parses the LLM response into an ArchitectDecision
func ParseArchitectDecision(raw string) (*ArchitectDecision, error) {
	// strip markdown fences if present
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var d ArchitectDecision
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("failed to parse architect response: %w", err)
	}

	// validate
	if d.Method == "" {
		d.Method = "ecs-fargate"
	}
	if d.Provider == "" {
		// infer provider from method prefix
		if strings.HasPrefix(d.Method, "cf-") {
			d.Provider = "cloudflare"
		} else {
			d.Provider = "aws"
		}
	}

	return &d, nil
}

// StrategyFromArchitect converts an ArchitectDecision into a DeployStrategy
func StrategyFromArchitect(d *ArchitectDecision) DeployStrategy {
	return DeployStrategy{
		Provider:  d.Provider,
		Method:    d.Method,
		Region:    "us-east-1", // region resolved separately
		Reasoning: d.Reasoning,
	}
}

// DefaultStrategy picks the best deployment method based on the repo profile
func DefaultStrategy(p *RepoProfile) DeployStrategy {
	s := DeployStrategy{
		Provider: "aws",
		Region:   "us-east-1",
	}

	// if wrangler.toml exists, default to cloudflare
	for _, hint := range p.DeployHints {
		if hint == "cloudflare" {
			s.Provider = "cloudflare"
			if p.HasDocker {
				s.Method = "cf-containers"
			} else if len(p.Ports) == 0 || p.Framework == "react" || p.Framework == "vite" {
				s.Method = "cf-pages"
			} else {
				s.Method = "cf-workers"
			}
			return s
		}
	}

	// Dockerized apps → ECS Fargate (most common, zero servers)
	if p.HasDocker {
		s.Method = "ecs-fargate"
		return s
	}

	// static frontends / SPAs
	switch p.Framework {
	case "react", "vite", "nuxt":
		s.Method = "s3-cloudfront"
		return s
	case "nextjs":
		s.Method = "ecs-fargate" // SSR needs a server
		return s
	}

	// anything with a port → containerize + fargate
	if len(p.Ports) > 0 {
		s.Method = "ecs-fargate"
		return s
	}

	// fallback
	s.Method = "ecs-fargate"
	return s
}

// BuildPrompt generates the enriched natural-language prompt that feeds into maker.PlanPrompt.
// If archDecision is non-nil, it incorporates the LLM architect's reasoning.
func BuildPrompt(p *RepoProfile, strat DeployStrategy, archDecision *ArchitectDecision) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Deploy the application from %s to AWS.\n\n", p.RepoURL))

	// architect's reasoning (if available)
	if archDecision != nil {
		b.WriteString("## Architecture Decision (from analysis)\n")
		b.WriteString(fmt.Sprintf("- Method: %s\n", archDecision.Method))
		b.WriteString(fmt.Sprintf("- Reasoning: %s\n", archDecision.Reasoning))
		if archDecision.RunCmd != "" {
			b.WriteString(fmt.Sprintf("- Local run command: %s\n", archDecision.RunCmd))
		}
		if len(archDecision.BuildSteps) > 0 {
			b.WriteString("- Build steps:\n")
			for _, step := range archDecision.BuildSteps {
				b.WriteString(fmt.Sprintf("  - %s\n", step))
			}
		}
		if archDecision.CpuMemory != "" {
			b.WriteString(fmt.Sprintf("- Suggested sizing: %s (cpu/memory)\n", archDecision.CpuMemory))
		}
		if len(archDecision.Notes) > 0 {
			b.WriteString("- Notes:\n")
			for _, n := range archDecision.Notes {
				b.WriteString(fmt.Sprintf("  - %s\n", n))
			}
		}
		b.WriteString("\n")
	}

	// app description
	b.WriteString("## Application Analysis\n")
	if p.Language != "" && p.Language != "unknown" {
		b.WriteString(fmt.Sprintf("- Language: %s\n", p.Language))
	}
	if p.Framework != "" {
		b.WriteString(fmt.Sprintf("- Framework: %s\n", p.Framework))
	}
	if p.PackageManager != "" {
		b.WriteString(fmt.Sprintf("- Package manager: %s\n", p.PackageManager))
	}
	if p.IsMonorepo {
		b.WriteString("- Monorepo: yes (has workspace config)\n")
	}
	if p.HasDocker {
		b.WriteString("- Has Dockerfile: yes\n")
	}
	if p.HasCompose {
		b.WriteString("- Has docker-compose: yes\n")
	}
	if p.EntryPoint != "" {
		b.WriteString(fmt.Sprintf("- Entry point: %s\n", p.EntryPoint))
	}
	if p.BuildCmd != "" {
		b.WriteString(fmt.Sprintf("- Build command: %s\n", p.BuildCmd))
	}
	if p.StartCmd != "" {
		b.WriteString(fmt.Sprintf("- Start command: %s\n", p.StartCmd))
	}
	if len(p.Ports) > 0 {
		portStrs := make([]string, len(p.Ports))
		for i, port := range p.Ports {
			portStrs[i] = fmt.Sprintf("%d", port)
		}
		b.WriteString(fmt.Sprintf("- Exposed ports: %s\n", strings.Join(portStrs, ", ")))
	}
	if len(p.EnvVars) > 0 {
		b.WriteString(fmt.Sprintf("- Required env vars: %s\n", strings.Join(p.EnvVars, ", ")))
	}
	if p.HasDB {
		b.WriteString(fmt.Sprintf("- Database dependency: %s\n", p.DBType))
	}
	if len(p.DeployHints) > 0 {
		b.WriteString(fmt.Sprintf("- Existing deploy configs: %s\n", strings.Join(p.DeployHints, ", ")))
	}

	// monorepo guidance
	if p.IsMonorepo {
		b.WriteString("\n## Monorepo Notes\n")
		b.WriteString("- This is a monorepo. The Dockerfile should handle the full build.\n")
		b.WriteString("- If there's a Dockerfile, use it as-is — it already knows the workspace structure.\n")
		b.WriteString(fmt.Sprintf("- Package manager is %s, use it for all install/build commands.\n", p.PackageManager))
	}

	b.WriteString("\n## Deployment Requirements\n")

	switch strat.Method {
	case "ecs-fargate":
		b.WriteString(ecsPrompt(p))
	case "s3-cloudfront":
		b.WriteString(s3CloudfrontPrompt(p))
	default:
		b.WriteString(ecsPrompt(p))
	}

	// db provisioning
	if p.HasDB {
		b.WriteString("\n## Database\n")
		switch p.DBType {
		case "postgres":
			b.WriteString("- Create an RDS PostgreSQL instance (db.t3.micro, 20GB gp3)\n")
			b.WriteString("- Place in private subnets with security group allowing port 5432 from ECS tasks only\n")
			b.WriteString("- Use --manage-master-user-password for Secrets Manager integration\n")
		case "mysql":
			b.WriteString("- Create an RDS MySQL instance (db.t3.micro, 20GB gp3)\n")
			b.WriteString("- Place in private subnets with security group allowing port 3306 from ECS tasks only\n")
		case "redis":
			b.WriteString("- Create an ElastiCache Redis cluster (cache.t3.micro, 1 node)\n")
			b.WriteString("- Place in private subnets with security group allowing port 6379 from ECS tasks only\n")
		case "mongo":
			b.WriteString("- NOTE: AWS does not have managed MongoDB. Use DocumentDB (compatible) or skip DB provisioning.\n")
			b.WriteString("- If using DocumentDB: create cluster in private subnets, port 27017\n")
		}
	}

	// env var handling
	if len(p.EnvVars) > 0 {
		b.WriteString("\n## Environment Variables\n")
		b.WriteString("- Store sensitive env vars in AWS Secrets Manager\n")
		b.WriteString("- Pass them to the ECS task definition via secrets mapping\n")
		b.WriteString(fmt.Sprintf("- Required vars: %s\n", strings.Join(p.EnvVars, ", ")))
	}

	b.WriteString("\n## Important\n")
	b.WriteString("- Use the default VPC and its existing subnets when possible\n")
	b.WriteString("- Tag all resources with Project=clanker-deploy\n")
	b.WriteString("- Prefer minimal, cost-effective resource sizes\n")
	b.WriteString("- The plan must be fully executable with AWS CLI only\n")

	return b.String()
}

func ecsPrompt(p *RepoProfile) string {
	var b strings.Builder
	b.WriteString("Deploy using ECS Fargate (serverless containers):\n")
	b.WriteString("1. Create an ECR repository and note the URI\n")
	b.WriteString("2. Clone the repo, build and push the Docker image to ECR (use ecr get-login-password)\n")

	if !p.HasDocker {
		b.WriteString("   NOTE: No Dockerfile found — generate a multi-stage Dockerfile first:\n")
		b.WriteString(fmt.Sprintf("   - Use %s as the base image\n", dockerBaseImage(p)))
		b.WriteString(fmt.Sprintf("   - Install: %s\n", p.BuildCmd))
		b.WriteString(fmt.Sprintf("   - Start: %s\n", p.StartCmd))
	} else {
		b.WriteString("   The repo already has a Dockerfile — use it as-is for the build.\n")
		if p.IsMonorepo {
			b.WriteString("   This is a monorepo — the Dockerfile handles workspace dependencies.\n")
		}
	}

	b.WriteString("3. Create an ECS cluster (Fargate)\n")
	b.WriteString("4. Create a task execution IAM role with AmazonECSTaskExecutionRolePolicy\n")
	b.WriteString("5. Register a Fargate task definition with:\n")

	// port mappings for all detected ports
	if len(p.Ports) > 0 {
		for _, port := range p.Ports {
			b.WriteString(fmt.Sprintf("   - Container port: %d\n", port))
		}
	}

	b.WriteString("   - CPU: 256, Memory: 512 (minimal)\n")
	b.WriteString("   - Image from ECR\n")

	// env vars in task definition
	if len(p.EnvVars) > 0 {
		b.WriteString("   - Environment variables in container definition:\n")
		for _, v := range p.EnvVars {
			b.WriteString(fmt.Sprintf("     - %s (placeholder value, user must fill in)\n", v))
		}
	}

	b.WriteString("6. Create a security group allowing inbound traffic on the app port(s)\n")
	b.WriteString("7. Create an ECS service with desired count 1, assign public IP\n")

	if len(p.Ports) > 0 && (p.Framework != "" || p.HasDB) {
		b.WriteString("8. Create an ALB with target group pointing to the ECS service\n")
		b.WriteString("9. Output the ALB DNS name as the access URL\n")
	}

	return b.String()
}

// dockerBaseImage picks a reasonable base image for generating a Dockerfile
func dockerBaseImage(p *RepoProfile) string {
	switch p.Language {
	case "node":
		return "node:22-slim"
	case "python":
		return "python:3.12-slim"
	case "go":
		return "golang:1.22-alpine"
	case "rust":
		return "rust:1.78-slim"
	case "java":
		return "eclipse-temurin:21-jre"
	default:
		return "ubuntu:24.04"
	}
}

func s3CloudfrontPrompt(p *RepoProfile) string {
	var b strings.Builder
	b.WriteString("Deploy as a static site using S3 + CloudFront:\n")
	b.WriteString("1. Create an S3 bucket with static website hosting enabled\n")
	b.WriteString("2. Set bucket policy for public read access\n")
	b.WriteString("3. Create a CloudFront distribution with the S3 bucket as origin\n")
	b.WriteString("4. Output the CloudFront domain as the access URL\n")
	b.WriteString("   NOTE: The actual file upload (npm build + s3 sync) is outside the AWS CLI plan scope.\n")
	return b.String()
}
