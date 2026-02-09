package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AskFunc is the LLM call interface — matches ai.Client.AskPrompt signature
type AskFunc func(ctx context.Context, prompt string) (string, error)

// CleanFunc strips markdown fences from LLM JSON responses
type CleanFunc func(response string) string

// IntelligenceResult is the final output of the multi-phase reasoning pipeline
type IntelligenceResult struct {
	Exploration  *ExplorationResult `json:"exploration,omitempty"`
	DeepAnalysis *DeepAnalysis      `json:"deepAnalysis"`
	InfraSnap    *InfraSnapshot     `json:"infraSnapshot,omitempty"`
	CFInfraSnap  *CFInfraSnapshot   `json:"cfInfraSnapshot,omitempty"`
	Architecture *ArchitectDecision `json:"architecture"`
	Validation   *PlanValidation    `json:"validation,omitempty"`
	// final enriched prompt for maker pipeline
	EnrichedPrompt string `json:"enrichedPrompt"`
}

// DeepAnalysis is the LLM's understanding of what the app actually does
type DeepAnalysis struct {
	AppDescription string   `json:"appDescription"` // what does this app do
	Services       []string `json:"services"`       // list of services/components
	ExternalDeps   []string `json:"externalDeps"`   // external APIs, databases, etc
	BuildPipeline  string   `json:"buildPipeline"`  // how to actually build this thing
	RunLocally     string   `json:"runLocally"`     // simplest way to run locally
	Complexity     string   `json:"complexity"`     // simple, moderate, complex
	Concerns       []string `json:"concerns"`       // things that could go wrong
}

// PlanValidation is the LLM's review of its own generated plan
type PlanValidation struct {
	IsValid  bool     `json:"isValid"`
	Issues   []string `json:"issues"`   // problems found
	Fixes    []string `json:"fixes"`    // suggested fixes
	Warnings []string `json:"warnings"` // non-blocking warnings
}

// RunIntelligence executes the multi-phase recursive reasoning pipeline.
// Phase 0: Agentic file exploration (LLM requests files it needs)
// Phase 1: Deep Understanding (LLM analyzes all gathered context)
// Phase 1.5: AWS infra scan (query account for existing resources)
// Phase 2: Architecture Decision + Cost Estimation (LLM picks best option)
// Phase 2: Architecture Decision + Cost Estimation
// Both phases feed into the final enriched prompt for the maker plan generator.
func RunIntelligence(ctx context.Context, profile *RepoProfile, ask AskFunc, clean CleanFunc, debug bool, targetProvider, awsProfile, awsRegion string, logf func(string, ...any)) (*IntelligenceResult, error) {
	result := &IntelligenceResult{}

	// Phase 0: Agentic file exploration — LLM asks for files it needs
	logf("[intelligence] phase 0: exploring repository...")
	exploration, err := ExploreRepo(ctx, profile, ask, clean, logf)
	if err != nil {
		logf("[intelligence] warning: exploration failed (%v), using static files only", err)
		exploration = &ExplorationResult{FilesRead: profile.KeyFiles}
	}
	result.Exploration = exploration

	// merge explored files into profile for downstream phases
	if profile.KeyFiles == nil {
		profile.KeyFiles = make(map[string]string)
	}
	for name, content := range exploration.FilesRead {
		profile.KeyFiles[name] = content
	}

	// Phase 1: Deep Understanding — LLM reads all gathered files
	logf("[intelligence] phase 1: deep understanding (%d files)...", len(profile.KeyFiles))
	deepPrompt := buildDeepAnalysisPrompt(profile)
	deepResp, err := ask(ctx, deepPrompt)
	if err != nil {
		return nil, fmt.Errorf("phase 1 (deep analysis) failed: %w", err)
	}

	deep, err := parseDeepAnalysis(clean(deepResp))
	if err != nil {
		logf("[intelligence] warning: deep analysis parse failed (%v), continuing with static analysis", err)
		deep = &DeepAnalysis{
			AppDescription: profile.Summary,
			Complexity:     "unknown",
		}
		// use exploration analysis as fallback
		if exploration.Analysis != "" {
			deep.AppDescription = exploration.Analysis
		}
	}
	result.DeepAnalysis = deep

	if debug {
		logf("[intelligence] deep analysis: %s (complexity: %s)", deep.AppDescription, deep.Complexity)
	}

	// Phase 1.5: Infra scan — query cloud provider for existing resources
	var infraSnap *InfraSnapshot
	var cfInfraSnap *CFInfraSnapshot
	if targetProvider == "cloudflare" {
		logf("[intelligence] phase 1.5: scanning Cloudflare infrastructure...")
		cfInfraSnap = ScanCFInfra(ctx, logf)
		result.CFInfraSnap = cfInfraSnap
	} else {
		logf("[intelligence] phase 1.5: scanning AWS infrastructure...")
		infraSnap = ScanInfra(ctx, awsProfile, awsRegion, logf)
		result.InfraSnap = infraSnap
	}

	// Phase 2: Architecture Decision + Cost Estimation
	logf("[intelligence] phase 2: architecture + cost estimation...")
	archPrompt := buildSmartArchitectPrompt(profile, deep, targetProvider)
	archResp, err := ask(ctx, archPrompt)
	if err != nil {
		return nil, fmt.Errorf("phase 2 (architecture) failed: %w", err)
	}

	arch, err := ParseArchitectDecision(clean(archResp))
	if err != nil {
		logf("[intelligence] warning: architect parse failed (%v), using heuristic", err)
		strat := DefaultStrategy(profile)
		arch = &ArchitectDecision{
			Provider:  strat.Provider,
			Method:    strat.Method,
			Reasoning: "fallback heuristic",
		}
	}
	result.Architecture = arch

	logf("[intelligence] architecture: %s — %s", arch.Method, arch.Reasoning)
	if arch.EstMonthly != "" {
		logf("[intelligence] estimated cost: %s/month", arch.EstMonthly)
	}

	// build the final enriched prompt with all intelligence + infra context
	strat := StrategyFromArchitect(arch)
	result.EnrichedPrompt = buildIntelligentPrompt(profile, deep, arch, strat, infraSnap, cfInfraSnap)

	return result, nil
}

// ValidatePlan runs the LLM validation phase on a generated plan.
// Call this AFTER the maker plan is generated.
// Returns validation result and an optional revised prompt if issues were found.
func ValidatePlan(ctx context.Context, planJSON string, profile *RepoProfile, deep *DeepAnalysis, ask AskFunc, clean CleanFunc, logf func(string, ...any)) (*PlanValidation, string, error) {
	logf("[intelligence] phase 4: plan validation...")

	prompt := buildValidationPrompt(planJSON, profile, deep)
	resp, err := ask(ctx, prompt)
	if err != nil {
		return nil, "", fmt.Errorf("validation failed: %w", err)
	}

	v, err := parseValidation(clean(resp))
	if err != nil {
		logf("[intelligence] warning: validation parse failed (%v), assuming plan is ok", err)
		return &PlanValidation{IsValid: true}, "", nil
	}

	if !v.IsValid && len(v.Fixes) > 0 {
		// build a fix prompt that the caller can feed back into plan generation
		fixPrompt := buildFixPrompt(v)
		return v, fixPrompt, nil
	}

	return v, "", nil
}

// --- Phase 1: Deep Understanding ---

func buildDeepAnalysisPrompt(p *RepoProfile) string {
	var b strings.Builder

	b.WriteString("You are an expert software engineer. Analyze this repository and explain what it does and how to run it.\n\n")

	// file tree
	if p.FileTree != "" {
		b.WriteString("## Repository Structure\n```\n")
		b.WriteString(p.FileTree)
		b.WriteString("```\n\n")
	}

	// actual file contents
	if len(p.KeyFiles) > 0 {
		b.WriteString("## Key Files\n")
		for name, content := range p.KeyFiles {
			b.WriteString(fmt.Sprintf("\n### %s\n```\n%s\n```\n", name, content))
		}
		b.WriteString("\n")
	}

	// static analysis results for context
	profileJSON, _ := json.MarshalIndent(struct {
		Language       string   `json:"language"`
		Framework      string   `json:"framework"`
		PackageManager string   `json:"packageManager"`
		IsMonorepo     bool     `json:"isMonorepo"`
		HasDocker      bool     `json:"hasDocker"`
		HasCompose     bool     `json:"hasCompose"`
		Ports          []int    `json:"ports"`
		EnvVars        []string `json:"envVars"`
		HasDB          bool     `json:"hasDb"`
		DBType         string   `json:"dbType"`
	}{
		Language:       p.Language,
		Framework:      p.Framework,
		PackageManager: p.PackageManager,
		IsMonorepo:     p.IsMonorepo,
		HasDocker:      p.HasDocker,
		HasCompose:     p.HasCompose,
		Ports:          p.Ports,
		EnvVars:        p.EnvVars,
		HasDB:          p.HasDB,
		DBType:         p.DBType,
	}, "", "  ")
	b.WriteString("## Static Analysis\n```json\n")
	b.WriteString(string(profileJSON))
	b.WriteString("\n```\n\n")

	b.WriteString(`## Your Task
Analyze the repo deeply and respond with JSON:
1. What does this application DO? (1-2 sentences)
2. What services/components does it have? (e.g. "API server", "WebSocket gateway", "React frontend", "worker")
3. What external dependencies does it need? (databases, APIs, message queues)
4. What's the actual build pipeline? (real commands, not guesses)
5. What's the simplest way to run it locally?
6. How complex is this to deploy? (simple/moderate/complex)
7. What could go wrong during deployment?

Think step by step. READ the Dockerfile, package.json, README carefully.

## Response Format (JSON only, no markdown fences)
{
  "appDescription": "...",
  "services": ["service1", "service2"],
  "externalDeps": ["postgres", "redis", "openai-api"],
  "buildPipeline": "git clone && docker build -t app .",
  "runLocally": "docker compose up",
  "complexity": "moderate",
  "concerns": ["needs OPENAI_API_KEY", "WebSocket requires sticky sessions"]
}`)

	return b.String()
}

func parseDeepAnalysis(raw string) (*DeepAnalysis, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var d DeepAnalysis
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("failed to parse deep analysis: %w", err)
	}
	return &d, nil
}

// --- Phase 2: Smart Architecture ---

func buildSmartArchitectPrompt(p *RepoProfile, deep *DeepAnalysis, targetProvider string) string {
	var b strings.Builder

	b.WriteString("You are a senior cloud architect. Based on this deep analysis, decide the BEST way to deploy this application.\n\n")

	// deep analysis context
	b.WriteString("## Application Understanding\n")
	b.WriteString(fmt.Sprintf("- Description: %s\n", deep.AppDescription))
	if len(deep.Services) > 0 {
		b.WriteString(fmt.Sprintf("- Services: %s\n", strings.Join(deep.Services, ", ")))
	}
	if len(deep.ExternalDeps) > 0 {
		b.WriteString(fmt.Sprintf("- External deps: %s\n", strings.Join(deep.ExternalDeps, ", ")))
	}
	b.WriteString(fmt.Sprintf("- Build pipeline: %s\n", deep.BuildPipeline))
	b.WriteString(fmt.Sprintf("- Run locally: %s\n", deep.RunLocally))
	b.WriteString(fmt.Sprintf("- Complexity: %s\n", deep.Complexity))
	if len(deep.Concerns) > 0 {
		b.WriteString(fmt.Sprintf("- Concerns: %s\n", strings.Join(deep.Concerns, "; ")))
	}

	// static profile
	b.WriteString(fmt.Sprintf("\n## Stack: %s", p.Summary))
	if p.HasDocker {
		b.WriteString("\n- Has Dockerfile (USE IT)")
	}
	if p.HasCompose {
		b.WriteString("\n- Has docker-compose (reference for multi-service setup)")
	}
	if p.IsMonorepo {
		b.WriteString(fmt.Sprintf("\n- Monorepo (%s workspaces)", p.PackageManager))
	}
	if len(p.Ports) > 0 {
		portStrs := make([]string, len(p.Ports))
		for i, port := range p.Ports {
			portStrs[i] = fmt.Sprintf("%d", port)
		}
		b.WriteString(fmt.Sprintf("\n- Ports: %s", strings.Join(portStrs, ", ")))
	}

	// key file contents for informed decisions
	if dockerfile, ok := p.KeyFiles["Dockerfile"]; ok {
		b.WriteString("\n\n## Dockerfile\n```\n" + dockerfile + "\n```")
	}
	if compose, ok := p.KeyFiles["docker-compose.yml"]; ok {
		b.WriteString("\n\n## docker-compose.yml\n```\n" + compose + "\n```")
	} else if compose, ok := p.KeyFiles["docker-compose.yaml"]; ok {
		b.WriteString("\n\n## docker-compose.yaml\n```\n" + compose + "\n```")
	}

	b.WriteString(`

## Your Task
Evaluate AT LEAST 2 deployment options and pick the best one.

Think about:
- Cost (prefer cheap/free tier)
- Simplicity (fewer moving parts = better)
- Will it ACTUALLY work? (ports, env vars, build steps)
- Does it need persistent storage, WebSockets, cron jobs?
- If it has a Dockerfile, use it — don't reinvent the wheel
`)

	if targetProvider == "cloudflare" {
		b.WriteString(`
## Cloudflare Options to Consider
1. **cf-pages** — Static sites, SPAs, SSR with Workers Functions. Deploy via wrangler pages deploy. (~$0-5/mo free tier)
2. **cf-workers** — Edge serverless functions via wrangler deploy. Need wrangler.toml. Great for APIs, Hono, Remix on CF. (~$5/mo for paid plan, free tier generous)
3. **cf-containers** — Docker containers on Cloudflare. Use wrangler containers build + push. For full server apps. (~$10-30/mo)

## Cloudflare Services
- D1 = SQLite database (free tier: 5GB)
- KV = Key-value store (free tier: 100k reads/day)
- R2 = Object storage, S3-compatible (free tier: 10GB)
- Queues = Message queues
- Hyperdrive = Postgres connection pooler

## Deployment CLI
All commands use npx wrangler. Auth via CLOUDFLARE_API_TOKEN env var.

## Cost Estimation
Estimate the MONTHLY cost in USD. Most small apps fit in free tier.

## Response Format (JSON only, no markdown fences)
{
  "provider": "cloudflare",
  "method": "cf-pages",
  "reasoning": "This is a Vite React SPA with no server-side rendering. CF Pages handles static site deployment perfectly with automatic CDN, preview deployments, and generous free tier.",
  "alternatives": [
    {"method": "cf-workers", "why_not": "Overkill for a static site"},
    {"method": "cf-containers", "why_not": "No need for containers with a static site"}
  ],
  "buildSteps": [
    "Install dependencies with npm install",
    "Build with npm run build",
    "Create Pages project with npx wrangler pages project create",
    "Deploy dist/ with npx wrangler pages deploy dist/"
  ],
  "runCmd": "npm run dev",
  "notes": ["SPA routing needs _redirects file or _routes.json"],
  "cpuMemory": "",
  "needsAlb": false,
  "needsDb": false,
  "dbService": "",
  "estMonthly": "$0 (free tier)",
  "costBreakdown": ["Pages: free", "Bandwidth: free up to 100GB/mo"]
}`)
	} else {
		b.WriteString(`
## AWS Options to Consider
1. **ECS Fargate** — serverless containers, good for any Dockerized app (~$12-30/mo)
2. **App Runner** — even simpler than Fargate, auto-scales, good for web apps (~$5-25/mo)
3. **EC2** — full control, cheap with spot/t3.micro (~$4-15/mo)
4. **Lambda** — event-driven/cron, not for long-running servers (~$0-5/mo)
5. **S3 + CloudFront** — static sites only, nearly free (~$1-3/mo)
6. **Lightsail** — cheapest for simple apps (~$3.50-10/mo)

## Cost Estimation
Estimate the MONTHLY cost in USD for your recommended architecture.
Break it down by service (compute, storage, networking, database).

## Response Format (JSON only, no markdown fences)
{
  "provider": "aws",
  "method": "ecs-fargate",
  "reasoning": "This is a Dockerized Node.js monorepo with WebSocket support. ECS Fargate handles the Docker build natively and supports long-running connections. App Runner would be simpler but doesn't support WebSocket sticky sessions well.",
  "alternatives": [
    {"method": "app-runner", "why_not": "No WebSocket sticky session support"},
    {"method": "ec2", "why_not": "More ops overhead for a simple deployment"}
  ],
  "buildSteps": [
    "Create ECR repository",
    "Clone repo and docker build using existing Dockerfile",
    "Push image to ECR",
    "Create ECS cluster + task definition + service"
  ],
  "runCmd": "docker compose up",
  "notes": ["Port 18789 must be exposed", "OPENAI_API_KEY env var required"],
  "cpuMemory": "512/1024",
  "needsAlb": true,
  "needsDb": false,
  "dbService": "",
  "estMonthly": "$15-25",
  "costBreakdown": ["Fargate 0.25vCPU/0.5GB: ~$9/mo", "ALB: ~$16/mo", "ECR: ~$1/mo"]
}`)
	}

	return b.String()
}

// --- Phase 4: Validation ---

func buildValidationPrompt(planJSON string, p *RepoProfile, deep *DeepAnalysis) string {
	var b strings.Builder

	b.WriteString("You are a deployment QA engineer. Review this cloud deployment plan and check for correctness.\n\n")

	b.WriteString("## Application Context\n")
	b.WriteString(fmt.Sprintf("- %s\n", deep.AppDescription))
	if len(deep.Services) > 0 {
		b.WriteString(fmt.Sprintf("- Services: %s\n", strings.Join(deep.Services, ", ")))
	}
	if len(deep.ExternalDeps) > 0 {
		b.WriteString(fmt.Sprintf("- Needs: %s\n", strings.Join(deep.ExternalDeps, ", ")))
	}
	if p.HasDocker {
		b.WriteString("- Has Dockerfile\n")
	}
	if len(p.Ports) > 0 {
		portStrs := make([]string, len(p.Ports))
		for i, port := range p.Ports {
			portStrs[i] = fmt.Sprintf("%d", port)
		}
		b.WriteString(fmt.Sprintf("- Ports: %s\n", strings.Join(portStrs, ", ")))
	}
	if len(p.EnvVars) > 0 {
		b.WriteString(fmt.Sprintf("- Required env vars: %s\n", strings.Join(p.EnvVars, ", ")))
	}

	b.WriteString("\n## Generated Plan\n```json\n")
	b.WriteString(planJSON)
	b.WriteString("\n```\n\n")

	b.WriteString(`## Check For
1. Are commands in the right ORDER? (e.g. ECR repo before docker push)
2. Are ALL required ports exposed in security groups and task definitions?
3. Are environment variables passed to the container?
4. Does the plan use the existing Dockerfile (not try to build from scratch)?
5. Are IAM roles and policies created BEFORE they're referenced?
6. Will the app actually be accessible from the internet after deployment?
7. Are there any missing steps?
8. Are resource references (ARNs, IDs) properly chained?

## Response Format (JSON only, no markdown fences)
{
  "isValid": false,
  "issues": ["Security group doesn't allow port 18789", "Missing ECR login before push"],
  "fixes": ["Add inbound rule for port 18789", "Add ecr get-login-password command before docker push"],
  "warnings": ["No health check configured — service may restart unnecessarily"]
}`)

	return b.String()
}

func parseValidation(raw string) (*PlanValidation, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var v PlanValidation
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("failed to parse validation: %w", err)
	}
	return &v, nil
}

func buildFixPrompt(v *PlanValidation) string {
	var b strings.Builder
	b.WriteString("\n\n## CRITICAL: Fix these issues from the previous plan\n")
	for _, issue := range v.Issues {
		b.WriteString(fmt.Sprintf("- ISSUE: %s\n", issue))
	}
	for _, fix := range v.Fixes {
		b.WriteString(fmt.Sprintf("- FIX: %s\n", fix))
	}
	if len(v.Warnings) > 0 {
		b.WriteString("\nAlso address these warnings if possible:\n")
		for _, w := range v.Warnings {
			b.WriteString(fmt.Sprintf("- WARNING: %s\n", w))
		}
	}
	return b.String()
}

// --- Intelligent Prompt Builder ---

// buildIntelligentPrompt creates the final enriched prompt using all intelligence phases
func buildIntelligentPrompt(p *RepoProfile, deep *DeepAnalysis, arch *ArchitectDecision, strat DeployStrategy, infraSnap *InfraSnapshot, cfInfraSnap *CFInfraSnapshot) string {
	var b strings.Builder

	providerLabel := "AWS"
	if strat.Provider == "cloudflare" {
		providerLabel = "Cloudflare"
	}
	b.WriteString(fmt.Sprintf("Deploy the application from %s to %s.\n\n", p.RepoURL, providerLabel))

	// inject existing infra context so LLM reuses resources
	if infraSnap != nil {
		infraCtx := infraSnap.FormatForPrompt()
		if infraCtx != "" {
			b.WriteString("## Existing AWS Infrastructure\n")
			b.WriteString(infraCtx)
			b.WriteString("\n\n")
		}
	}
	if cfInfraSnap != nil {
		cfCtx := cfInfraSnap.FormatCFForPrompt()
		if cfCtx != "" {
			b.WriteString("## Existing Cloudflare Infrastructure\n")
			b.WriteString(cfCtx)
			b.WriteString("\n\n")
		}
	}

	// cost context
	if arch.EstMonthly != "" {
		b.WriteString(fmt.Sprintf("## Estimated Monthly Cost: %s\n", arch.EstMonthly))
		if len(arch.CostBreakdown) > 0 {
			for _, c := range arch.CostBreakdown {
				b.WriteString(fmt.Sprintf("  - %s\n", c))
			}
		}
		b.WriteString("\n")
	}

	// LLM understanding
	b.WriteString("## What This App Does\n")
	b.WriteString(deep.AppDescription + "\n")
	if len(deep.Services) > 0 {
		b.WriteString(fmt.Sprintf("Services: %s\n", strings.Join(deep.Services, ", ")))
	}
	if len(deep.ExternalDeps) > 0 {
		b.WriteString(fmt.Sprintf("External dependencies: %s\n", strings.Join(deep.ExternalDeps, ", ")))
	}
	if deep.BuildPipeline != "" {
		b.WriteString(fmt.Sprintf("Build pipeline: %s\n", deep.BuildPipeline))
	}

	// architecture decision with reasoning
	b.WriteString("\n## Architecture Decision\n")
	b.WriteString(fmt.Sprintf("Method: %s\n", arch.Method))
	b.WriteString(fmt.Sprintf("Reasoning: %s\n", arch.Reasoning))
	if len(arch.BuildSteps) > 0 {
		b.WriteString("Build steps:\n")
		for _, step := range arch.BuildSteps {
			b.WriteString(fmt.Sprintf("  - %s\n", step))
		}
	}
	if arch.CpuMemory != "" {
		b.WriteString(fmt.Sprintf("Sizing: %s (cpu/memory)\n", arch.CpuMemory))
	}
	if len(arch.Notes) > 0 {
		b.WriteString("Important notes:\n")
		for _, n := range arch.Notes {
			b.WriteString(fmt.Sprintf("  - %s\n", n))
		}
	}

	// concerns from deep analysis
	if len(deep.Concerns) > 0 {
		b.WriteString("\n## Known Concerns\n")
		for _, c := range deep.Concerns {
			b.WriteString(fmt.Sprintf("- %s\n", c))
		}
	}

	// technical details
	b.WriteString("\n## Technical Details\n")
	b.WriteString(fmt.Sprintf("- Language: %s\n", p.Language))
	if p.Framework != "" {
		b.WriteString(fmt.Sprintf("- Framework: %s\n", p.Framework))
	}
	b.WriteString(fmt.Sprintf("- Package manager: %s\n", p.PackageManager))
	if p.IsMonorepo {
		b.WriteString("- Monorepo: yes\n")
	}
	if p.HasDocker {
		b.WriteString("- Dockerfile: YES — use it for the build, do NOT create a new one\n")
	}
	if p.HasCompose {
		b.WriteString("- docker-compose: yes (reference for service topology)\n")
	}
	if len(p.Ports) > 0 {
		portStrs := make([]string, len(p.Ports))
		for i, port := range p.Ports {
			portStrs[i] = fmt.Sprintf("%d", port)
		}
		b.WriteString(fmt.Sprintf("- Ports: %s\n", strings.Join(portStrs, ", ")))
	}
	if len(p.EnvVars) > 0 {
		b.WriteString(fmt.Sprintf("- Required env vars: %s\n", strings.Join(p.EnvVars, ", ")))
	}
	if p.HasDB {
		b.WriteString(fmt.Sprintf("- Database: %s\n", p.DBType))
	}

	// deployment instructions based on chosen method
	b.WriteString("\n## Deployment Instructions\n")
	switch strat.Method {
	case "ecs-fargate":
		b.WriteString(smartECSPrompt(p, arch, deep))
	case "app-runner":
		b.WriteString(appRunnerPrompt(p, arch))
	case "s3-cloudfront":
		b.WriteString(s3CloudfrontIntelligentPrompt(p))
	case "lightsail":
		b.WriteString(lightsailPrompt(p, arch))
	case "cf-pages":
		b.WriteString(cfPagesPrompt(p, deep))
	case "cf-workers":
		b.WriteString(cfWorkersPrompt(p, deep))
	case "cf-containers":
		b.WriteString(cfContainersPrompt(p, arch, deep))
	default:
		if strat.Provider == "cloudflare" {
			b.WriteString(cfWorkersPrompt(p, deep))
		} else {
			b.WriteString(smartECSPrompt(p, arch, deep))
		}
	}

	// db provisioning
	if arch.NeedsDB || p.HasDB {
		b.WriteString("\n## Database Provisioning\n")
		dbType := p.DBType
		if arch.DBService != "" {
			dbType = arch.DBService
		}
		b.WriteString(dbPrompt(dbType))
	}

	// env vars
	if len(p.EnvVars) > 0 {
		b.WriteString("\n## Environment Variables\n")
		b.WriteString("- Store sensitive env vars in AWS Secrets Manager or SSM Parameter Store\n")
		b.WriteString("- Pass them to the container via secrets/environment mapping in task definition\n")
		b.WriteString(fmt.Sprintf("- Required: %s\n", strings.Join(p.EnvVars, ", ")))
	}

	b.WriteString("\n## Rules\n")
	if strat.Provider == "cloudflare" {
		b.WriteString("- All commands use npx wrangler CLI\n")
		b.WriteString("- Auth via CLOUDFLARE_API_TOKEN env var (already set)\n")
		b.WriteString("- Tag/name resources with clanker-deploy prefix\n")
		b.WriteString("- Prefer free tier where possible\n")
		b.WriteString("- The plan must be fully executable with npx wrangler commands only\n")
		b.WriteString("- Commands must be in the correct dependency order\n")
		b.WriteString("- Every resource that's referenced must be created first\n")
	} else {
		b.WriteString("- Use the default VPC and its existing subnets when possible\n")
		b.WriteString("- Tag all resources with Project=clanker-deploy\n")
		b.WriteString("- Prefer minimal, cost-effective resource sizes\n")
		b.WriteString("- The plan must be fully executable with AWS CLI only\n")
		b.WriteString("- Commands must be in the correct dependency order\n")
		b.WriteString("- Every resource that's referenced must be created first\n")
	}

	return b.String()
}

// --- Method-specific prompts ---

func smartECSPrompt(p *RepoProfile, arch *ArchitectDecision, deep *DeepAnalysis) string {
	var b strings.Builder
	b.WriteString("Deploy using ECS Fargate (serverless containers):\n")
	b.WriteString("1. Create an ECR repository\n")

	if p.HasDocker {
		b.WriteString("2. Clone the repo, build the Docker image using the existing Dockerfile, push to ECR\n")
		b.WriteString("   - Use `aws ecr get-login-password` to authenticate\n")
		b.WriteString("   - Tag image with ECR URI and push\n")
	} else {
		b.WriteString("2. Generate a Dockerfile, build and push to ECR:\n")
		b.WriteString(fmt.Sprintf("   - Base image: %s\n", dockerBaseImage(p)))
		b.WriteString(fmt.Sprintf("   - Install: %s\n", deep.BuildPipeline))
	}

	b.WriteString("3. Create ECS cluster\n")
	b.WriteString("4. Create task execution IAM role (AmazonECSTaskExecutionRolePolicy)\n")
	b.WriteString("5. Register Fargate task definition:\n")

	// sizing from architect
	cpu := "256"
	mem := "512"
	if arch.CpuMemory != "" {
		parts := strings.SplitN(arch.CpuMemory, "/", 2)
		if len(parts) == 2 {
			cpu = strings.TrimSpace(parts[0])
			mem = strings.TrimSpace(parts[1])
		}
	}
	b.WriteString(fmt.Sprintf("   - CPU: %s, Memory: %s\n", cpu, mem))

	// all ports
	if len(p.Ports) > 0 {
		for _, port := range p.Ports {
			b.WriteString(fmt.Sprintf("   - Container port mapping: %d\n", port))
		}
	}

	b.WriteString("6. Create security group with inbound rules for ALL app ports\n")
	b.WriteString("7. Create ECS service (desired count 1, assign public IP, use awsvpc network mode)\n")

	if arch.NeedsALB {
		b.WriteString("8. Create ALB + target group + listener for the primary port\n")
		b.WriteString("9. Output the ALB DNS name as the access URL\n")
	} else {
		b.WriteString("8. Output the task's public IP as the access URL\n")
	}

	return b.String()
}

func appRunnerPrompt(p *RepoProfile, arch *ArchitectDecision) string {
	var b strings.Builder
	b.WriteString("Deploy using AWS App Runner (simplest container hosting):\n")

	if p.HasDocker {
		b.WriteString("1. Create ECR repository, build and push Docker image\n")
		b.WriteString("2. Create App Runner service from ECR image\n")
	} else {
		b.WriteString("1. Create App Runner service from source repository\n")
	}

	if len(p.Ports) > 0 {
		b.WriteString(fmt.Sprintf("3. Configure port: %d\n", p.Ports[0]))
	}

	cpu := "0.25"
	mem := "0.5"
	if arch.CpuMemory != "" {
		parts := strings.SplitN(arch.CpuMemory, "/", 2)
		if len(parts) == 2 {
			cpu = strings.TrimSpace(parts[0])
			mem = strings.TrimSpace(parts[1])
		}
	}
	b.WriteString(fmt.Sprintf("4. CPU: %s vCPU, Memory: %s GB\n", cpu, mem))
	b.WriteString("5. App Runner provides HTTPS endpoint automatically\n")

	return b.String()
}

func lightsailPrompt(p *RepoProfile, arch *ArchitectDecision) string {
	var b strings.Builder
	b.WriteString("Deploy using AWS Lightsail (cheapest option, $3.50/mo):\n")
	b.WriteString("1. Create a Lightsail container service (nano plan)\n")

	if p.HasDocker {
		b.WriteString("2. Build Docker image locally and push to Lightsail\n")
	} else {
		b.WriteString("2. Generate Dockerfile, build and push to Lightsail\n")
	}

	if len(p.Ports) > 0 {
		b.WriteString(fmt.Sprintf("3. Configure public endpoint on port %d\n", p.Ports[0]))
	}
	b.WriteString("4. Lightsail provides a public domain automatically\n")

	return b.String()
}

func s3CloudfrontIntelligentPrompt(p *RepoProfile) string {
	var b strings.Builder
	b.WriteString("Deploy as static site (S3 + CloudFront):\n")
	b.WriteString(fmt.Sprintf("1. Build the static assets: %s\n", p.BuildCmd))
	b.WriteString("2. Create S3 bucket with static website hosting\n")
	b.WriteString("3. Upload built assets to S3 (aws s3 sync)\n")
	b.WriteString("4. Create CloudFront distribution\n")
	b.WriteString("5. Output CloudFront domain as access URL\n")
	return b.String()
}

func dbPrompt(dbType string) string {
	switch dbType {
	case "postgres", "rds-postgres":
		return "- Create RDS PostgreSQL (db.t3.micro, 20GB gp3)\n- Private subnets, security group port 5432 from app only\n- Use --manage-master-user-password for Secrets Manager\n"
	case "mysql", "rds-mysql":
		return "- Create RDS MySQL (db.t3.micro, 20GB gp3)\n- Private subnets, security group port 3306 from app only\n"
	case "redis", "elasticache-redis":
		return "- Create ElastiCache Redis (cache.t3.micro, 1 node)\n- Private subnets, security group port 6379 from app only\n"
	case "mongo", "documentdb":
		return "- Create DocumentDB cluster (MongoDB-compatible)\n- Private subnets, port 27017\n"
	case "d1", "sqlite":
		return "- Create D1 database: npx wrangler d1 create <name>\n- Add binding to wrangler.toml\n"
	default:
		return fmt.Sprintf("- Provision managed %s service (pick cheapest tier)\n", dbType)
	}
}

// --- Cloudflare method-specific prompts ---

func cfPagesPrompt(p *RepoProfile, deep *DeepAnalysis) string {
	var b strings.Builder
	b.WriteString("Deploy as a Cloudflare Pages project (static site + optional Workers Functions):\n")

	buildCmd := p.BuildCmd
	if buildCmd == "" {
		buildCmd = "npm run build"
	}

	// detect output dir
	outputDir := "dist"
	switch p.Framework {
	case "nextjs":
		outputDir = ".vercel/output/static"
	case "remix":
		outputDir = "build/client"
	case "gatsby":
		outputDir = "public"
	case "astro":
		outputDir = "dist"
	}

	b.WriteString(fmt.Sprintf("1. Install dependencies: %s install\n", p.PackageManager))
	b.WriteString(fmt.Sprintf("2. Build the project: %s\n", buildCmd))
	b.WriteString(fmt.Sprintf("3. Create Pages project: npx wrangler pages project create <project-name> --production-branch main\n"))
	b.WriteString(fmt.Sprintf("4. Deploy built assets: npx wrangler pages deploy %s --project-name <project-name>\n", outputDir))
	b.WriteString("5. Pages provides an automatic *.pages.dev URL + HTTPS\n")

	if len(p.EnvVars) > 0 {
		b.WriteString("6. Set environment variables: npx wrangler pages secret put <KEY> --project-name <project-name>\n")
	}

	return b.String()
}

func cfWorkersPrompt(p *RepoProfile, deep *DeepAnalysis) string {
	var b strings.Builder
	b.WriteString("Deploy as a Cloudflare Worker (edge serverless):\n")

	// check if wrangler.toml exists
	hasWranglerConfig := false
	for name := range p.KeyFiles {
		if name == "wrangler.toml" || name == "wrangler.jsonc" || name == "wrangler.json" {
			hasWranglerConfig = true
			break
		}
	}

	if hasWranglerConfig {
		b.WriteString("1. The project already has a wrangler config — use it as-is\n")
		b.WriteString("2. Install dependencies\n")
		b.WriteString("3. Deploy with: npx wrangler deploy\n")
	} else {
		b.WriteString("1. Generate a wrangler.toml configuration file:\n")
		b.WriteString("   - name = \"<project-name>\"\n")
		b.WriteString("   - main = \"src/index.ts\" (or appropriate entry point)\n")
		b.WriteString("   - compatibility_date = current date\n")
		b.WriteString("2. Install dependencies\n")
		b.WriteString("3. Deploy with: npx wrangler deploy\n")
	}

	b.WriteString("4. Worker provides an automatic *.workers.dev URL + HTTPS\n")

	// secrets
	if len(p.EnvVars) > 0 {
		b.WriteString("5. Set secrets: npx wrangler secret put <KEY>\n")
		b.WriteString(fmt.Sprintf("   Required: %s\n", strings.Join(p.EnvVars, ", ")))
	}

	// bindings
	if p.HasDB {
		switch p.DBType {
		case "postgres":
			b.WriteString("6. Set up Hyperdrive for Postgres: npx wrangler hyperdrive create <name> --connection-string <postgres-url>\n")
		default:
			b.WriteString("6. Create D1 database: npx wrangler d1 create <name>\n")
		}
	}

	return b.String()
}

func cfContainersPrompt(p *RepoProfile, arch *ArchitectDecision, deep *DeepAnalysis) string {
	var b strings.Builder
	b.WriteString("Deploy as a Cloudflare Container (Docker on Cloudflare edge):\n")

	if p.HasDocker {
		b.WriteString("1. Build Docker image using existing Dockerfile:\n")
		b.WriteString("   npx wrangler containers build . -t <project-name>:latest\n")
	} else {
		b.WriteString("1. Generate a Dockerfile for the application, then build:\n")
		b.WriteString(fmt.Sprintf("   - Base image: %s\n", dockerBaseImage(p)))
		b.WriteString("   npx wrangler containers build . -t <project-name>:latest\n")
	}

	b.WriteString("2. Push image to Cloudflare registry:\n")
	b.WriteString("   npx wrangler containers push <project-name>:latest\n")
	b.WriteString("3. Create a Worker that references the container\n")
	b.WriteString("4. Deploy with: npx wrangler deploy\n")

	if len(p.Ports) > 0 {
		b.WriteString(fmt.Sprintf("5. Container exposes port %d\n", p.Ports[0]))
	}

	if len(p.EnvVars) > 0 {
		b.WriteString(fmt.Sprintf("6. Set container secrets via wrangler for: %s\n", strings.Join(p.EnvVars, ", ")))
	}

	return b.String()
}
