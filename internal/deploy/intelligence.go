package deploy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

func repoResourcePrefix(repoURL string, deployID string) string {
	clean := strings.TrimSpace(repoURL)
	if clean == "" {
		return "app-000000"
	}
	clean = strings.TrimSuffix(clean, "/")
	clean = strings.TrimSuffix(clean, ".git")

	path := clean
	if strings.Contains(clean, "://") {
		if u, err := url.Parse(clean); err == nil {
			path = strings.Trim(u.Path, "/")
		}
	} else if strings.HasPrefix(clean, "git@") {
		// git@github.com:owner/repo
		if parts := strings.SplitN(clean, ":", 2); len(parts) == 2 {
			path = strings.Trim(parts[1], "/")
		}
	}

	segments := strings.Split(path, "/")
	slug := strings.TrimSpace(segments[len(segments)-1])
	if slug == "" {
		slug = "app"
	}

	slug = strings.ToLower(slug)
	var out strings.Builder
	lastDash := false
	for _, r := range slug {
		isAZ := r >= 'a' && r <= 'z'
		is09 := r >= '0' && r <= '9'
		if isAZ || is09 {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	slug = strings.Trim(out.String(), "-")
	if slug == "" {
		slug = "app"
	}

	seed := strings.ToLower(clean)
	if strings.TrimSpace(deployID) != "" {
		seed += "|" + strings.ToLower(strings.TrimSpace(deployID))
	}
	sum := sha1.Sum([]byte(seed))
	suffix := hex.EncodeToString(sum[:])
	if len(suffix) > 6 {
		suffix = suffix[:6]
	}
	return slug + "-" + suffix
}

func awsName(prefix string, suffix string, maxLen int) string {
	prefix = strings.TrimSpace(prefix)
	suffix = strings.TrimSpace(suffix)
	if prefix == "" {
		prefix = "app-000000"
	}
	if maxLen <= 0 {
		return strings.Trim(prefix+suffix, "-")
	}

	name := prefix + suffix
	if len(name) <= maxLen {
		return strings.Trim(name, "-")
	}

	keep := maxLen - len(suffix)
	if keep <= 0 {
		trimmed := suffix
		if len(trimmed) > maxLen {
			trimmed = trimmed[:maxLen]
		}
		return strings.Trim(trimmed, "-")
	}
	if keep > len(prefix) {
		keep = len(prefix)
	}
	trimmedPrefix := strings.Trim(prefix[:keep], "-")
	if trimmedPrefix == "" {
		trimmedPrefix = "app"
	}
	return strings.Trim(trimmedPrefix+suffix, "-")
}

func kubeName(prefix string, maxLen int) string {
	name := strings.ToLower(strings.TrimSpace(prefix))
	name = strings.Trim(name, "-")
	if name == "" {
		name = "app"
	}
	if maxLen > 0 && len(name) > maxLen {
		name = strings.Trim(name[:maxLen], "-")
		if name == "" {
			name = "app"
		}
	}
	return name
}

// AskFunc is the LLM call interface — matches ai.Client.AskPrompt signature
type AskFunc func(ctx context.Context, prompt string) (string, error)

// CleanFunc strips markdown fences from LLM JSON responses
type CleanFunc func(response string) string

// IntelligenceResult is the final output of the multi-phase reasoning pipeline
type IntelligenceResult struct {
	Exploration  *ExplorationResult `json:"exploration,omitempty"`
	DeepAnalysis *DeepAnalysis      `json:"deepAnalysis"`
	Docker       *DockerAnalysis    `json:"docker,omitempty"`
	Preflight    *PreflightReport   `json:"preflight,omitempty"`
	InfraSnap    *InfraSnapshot     `json:"infraSnapshot,omitempty"`
	CFInfraSnap  *CFInfraSnapshot   `json:"cfInfraSnapshot,omitempty"`
	Architecture *ArchitectDecision `json:"architecture"`
	Validation   *PlanValidation    `json:"validation,omitempty"`
	// final enriched prompt for maker pipeline
	EnrichedPrompt string `json:"enrichedPrompt"`
}

// DeployOptions contains user-specified deployment preferences
type DeployOptions struct {
	Target       string // fargate, ec2, eks
	InstanceType string // for ec2: t3.small, t3.medium, etc.
	NewVPC       bool   // create new VPC instead of using default
	DeployID     string // run-specific id for unique resource naming
}

// shouldUseAPIGateway determines whether to use API Gateway or ALB based on app characteristics.
// API Gateway is better for: pure REST APIs, low traffic, pay-per-request pricing
// ALB is better for: web apps with frontend, WebSockets, high traffic, static content
func shouldUseAPIGateway(p *RepoProfile, deep *DeepAnalysis) bool {
	// Web apps with frontend should use ALB
	frontendFrameworks := map[string]bool{
		"react": true, "nextjs": true, "nuxt": true, "vue": true,
		"angular": true, "svelte": true, "vite": true, "gatsby": true,
		"remix": true, "astro": true,
	}
	if frontendFrameworks[p.Framework] {
		return false // ALB for frontend apps
	}

	// Apps with docker-compose usually have multiple services - use ALB
	if p.HasCompose {
		return false
	}

	// Check deep analysis for WebSocket mentions
	if deep != nil {
		descLower := strings.ToLower(deep.AppDescription)
		for _, service := range deep.Services {
			serviceLower := strings.ToLower(service)
			if strings.Contains(serviceLower, "websocket") ||
				strings.Contains(serviceLower, "socket") ||
				strings.Contains(serviceLower, "gateway") ||
				strings.Contains(serviceLower, "realtime") {
				return false // ALB for WebSocket apps
			}
		}
		if strings.Contains(descLower, "websocket") ||
			strings.Contains(descLower, "real-time") ||
			strings.Contains(descLower, "realtime") {
			return false
		}
	}

	// Pure API frameworks should use API Gateway
	apiFrameworks := map[string]bool{
		"fastapi": true, "flask": true, "gin": true, "echo": true,
		"fiber": true, "chi": true, "express": true, "fastify": true,
		"actix": true, "axum": true, "rocket": true, "spring-boot": true,
	}
	if apiFrameworks[p.Framework] {
		// But if it has static files or templates, use ALB
		for filename := range p.KeyFiles {
			if strings.Contains(filename, "static") ||
				strings.Contains(filename, "templates") ||
				strings.Contains(filename, "public") ||
				strings.Contains(filename, "views") {
				return false
			}
		}
		return true // API Gateway for pure API apps
	}

	// Default to ALB for unknown cases (safer, more features)
	return false
}

// EnvVarSpec describes a required or optional environment variable
type EnvVarSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default"` // default value if known
	Example     string `json:"example"`
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

	// Node.js app characteristics (detected from README and code)
	ListeningPort int    `json:"listeningPort"` // port the app listens on
	StartCommand  string `json:"startCommand"`  // how to start: "npm start", "node index.js"
	BuildCommand  string `json:"buildCommand"`  // build step if needed: "npm run build"
	NodeVersion   string `json:"nodeVersion"`   // required Node version

	// Config requirements (extracted from README and .env files)
	RequiredEnvVars []EnvVarSpec `json:"requiredEnvVars"` // MUST have values to run
	OptionalEnvVars []EnvVarSpec `json:"optionalEnvVars"` // nice to have, has defaults

	// Health verification
	HealthEndpoint string `json:"healthEndpoint"` // e.g., "/health", "/api/status"
	ExposesHTTP    bool   `json:"exposesHTTP"`    // does it serve HTTP?

	// Deployment method
	PreferDocker  bool   `json:"preferDocker"`  // true if Dockerfile exists and is recommended
	GlobalInstall string `json:"globalInstall"` // e.g., "npm install -g appname"
}

// PlanValidation is the LLM's review of its own generated plan
type PlanValidation struct {
	IsValid                bool     `json:"isValid"`
	Issues                 []string `json:"issues"`                 // problems found
	Fixes                  []string `json:"fixes"`                  // suggested fixes
	Warnings               []string `json:"warnings"`               // non-blocking warnings
	UnresolvedPlaceholders []string `json:"unresolvedPlaceholders"` // placeholders that need resolution
}

// RunIntelligence executes the multi-phase recursive reasoning pipeline.
// Phase 0: Agentic file exploration (LLM requests files it needs)
// Phase 1: Deep Understanding (LLM analyzes all gathered context)
// Phase 1.5: AWS infra scan (query account for existing resources)
// Phase 2: Architecture Decision + Cost Estimation (LLM picks best option)
// Both phases feed into the final enriched prompt for the maker plan generator.
func RunIntelligence(ctx context.Context, profile *RepoProfile, ask AskFunc, clean CleanFunc, debug bool, targetProvider, awsProfile, awsRegion string, opts *DeployOptions, logf func(string, ...any)) (*IntelligenceResult, error) {
	// default options if nil
	if opts == nil {
		opts = &DeployOptions{Target: "fargate", InstanceType: "t3.small"}
	}
	if opts.Target == "" {
		opts.Target = "fargate"
	}
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

	var deep *DeepAnalysis
	var infraSnap *InfraSnapshot
	var cfInfraSnap *CFInfraSnapshot
	var deepErr error

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		logf("[intelligence] phase 1: deep understanding (%d files)...", len(profile.KeyFiles))
		deepPrompt := buildDeepAnalysisPrompt(profile)
		deepResp, callErr := ask(ctx, deepPrompt)
		if callErr != nil {
			deepErr = fmt.Errorf("phase 1 (deep analysis) failed: %w", callErr)
			return
		}

		parsed, parseErr := parseDeepAnalysis(clean(deepResp))
		if parseErr != nil {
			logf("[intelligence] warning: deep analysis parse failed (%v), continuing with static analysis", parseErr)
			parsed = &DeepAnalysis{
				AppDescription: profile.Summary,
				Complexity:     "unknown",
			}
			if exploration.Analysis != "" {
				parsed.AppDescription = exploration.Analysis
			}
		}
		deep = parsed
	}()

	go func() {
		defer wg.Done()
		logf("[intelligence] phase 1.25: docker-agent analysis (parallel)...")
		docker := AnalyzeDockerAgent(profile)
		result.Docker = docker
		if docker != nil {
			logf("[docker-agent] dockerfile=%t compose=%t services=%d primaryPort=%d", docker.HasDockerfile, docker.HasCompose, len(docker.ComposeServices), docker.PrimaryPort)
		}
	}()

	go func() {
		defer wg.Done()
		switch strings.ToLower(strings.TrimSpace(targetProvider)) {
		case "cloudflare":
			logf("[intelligence] phase 1.5: scanning Cloudflare infrastructure...")
			cfInfraSnap = ScanCFInfra(ctx, logf)
		case "aws", "":
			logf("[intelligence] phase 1.5: scanning AWS infrastructure...")
			infraSnap = ScanInfra(ctx, awsProfile, awsRegion, logf)
		default:
			logf("[intelligence] phase 1.5: skipping infrastructure scan for provider=%s", targetProvider)
		}
	}()

	wg.Wait()
	if deepErr != nil {
		return nil, deepErr
	}
	result.DeepAnalysis = deep
	result.Preflight = BuildPreflightReport(profile, result.Docker, deep)

	// CRITICAL: Update profile.Ports with detected listening port from deep analysis
	// This ensures the port is used correctly in EC2/ECS prompts for target groups
	if deep.ListeningPort > 0 && (len(profile.Ports) == 0 || profile.Ports[0] != deep.ListeningPort) {
		logf("[intelligence] detected listening port from README/code: %d", deep.ListeningPort)
		profile.Ports = []int{deep.ListeningPort}
	}

	if debug {
		logf("[intelligence] deep analysis: %s (complexity: %s)", deep.AppDescription, deep.Complexity)
	}

	result.InfraSnap = infraSnap
	result.CFInfraSnap = cfInfraSnap

	// Phase 2: Architecture Decision + Cost Estimation
	logf("[intelligence] phase 2: architecture + cost estimation (target: %s)...", opts.Target)
	archPrompt := buildSmartArchitectPrompt(profile, deep, targetProvider, opts)
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

	// Deterministic override: OpenClaw is stateful + long-lived websocket gateway.
	// When deploying to AWS and the target is default/unspecified, prefer EC2.
	ApplyOpenClawArchitectureDefaults(targetProvider, opts, profile, deep, arch)
	// Deterministic override: WordPress one-click deploy uses EC2 + ALB + Docker Hub images.
	ApplyWordPressArchitectureDefaults(targetProvider, opts, profile, deep, arch)
	result.Architecture = arch

	// Deterministic override: static sites should prefer static hosting unless user explicitly requested EC2/EKS.
	if strings.EqualFold(strings.TrimSpace(targetProvider), "aws") || strings.TrimSpace(targetProvider) == "" {
		if result.Preflight != nil && result.Preflight.IsStaticSite {
			if opts == nil || strings.TrimSpace(opts.Target) == "" || strings.TrimSpace(opts.Target) == "fargate" {
				if arch.Method != "s3-cloudfront" {
					arch.Method = "s3-cloudfront"
					arch.Provider = "aws"
					arch.Reasoning = "Static site detected; S3+CloudFront is simpler and cheaper than running servers"
				}
			}
		}
	}

	logf("[intelligence] architecture: %s — %s", arch.Method, arch.Reasoning)
	if arch.EstMonthly != "" {
		logf("[intelligence] estimated cost: %s/month", arch.EstMonthly)
	}

	// Override architecture method if user specified a target
	if opts.Target != "" && opts.Target != "fargate" {
		switch opts.Target {
		case "ec2":
			arch.Method = "ec2"
			arch.CpuMemory = opts.InstanceType
		case "eks":
			arch.Method = "eks"
		}
	}

	// Auto-detect API Gateway vs ALB based on app type
	arch.UseAPIGateway = shouldUseAPIGateway(profile, deep)

	// build the final enriched prompt with all intelligence + infra context
	strat := StrategyFromArchitect(arch)
	result.EnrichedPrompt = buildIntelligentPrompt(profile, deep, result.Docker, arch, strat, infraSnap, cfInfraSnap, opts)

	return result, nil
}

// ValidatePlan runs the LLM validation phase on a generated plan.
// Call this AFTER the maker plan is generated.
// Returns validation result and an optional revised prompt if issues were found.
func ValidatePlan(ctx context.Context, planJSON string, profile *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, requireDockerCommandsInPlan bool, ask AskFunc, clean CleanFunc, logf func(string, ...any)) (*PlanValidation, string, error) {
	logf("[intelligence] phase 4: plan validation...")

	// Deterministic checks first: catch hard failures like missing compose-required env vars,
	// missing onboarding scripts for known repos, and secret inlining.
	det := runDeterministicPlanValidation(planJSON, profile, deep, docker)
	if len(det.Issues) > 0 {
		v := &PlanValidation{IsValid: false, Issues: det.Issues, Fixes: det.Fixes, Warnings: det.Warnings}
		return v, buildFixPrompt(v), nil
	}

	prompt := buildValidationPrompt(planJSON, profile, deep, docker, requireDockerCommandsInPlan)
	resp, err := ask(ctx, prompt)
	if err != nil {
		return nil, "", fmt.Errorf("validation failed: %w", err)
	}

	v, err := parseValidation(clean(resp))
	if err != nil {
		logf("[intelligence] warning: validation parse failed (%v)", err)
		v := &PlanValidation{
			IsValid: false,
			Issues: []string{
				"Validator returned an unparseable response (must be a single JSON object with keys: isValid/issues/fixes/warnings)",
			},
			Fixes: []string{
				"Re-run validation and respond with JSON ONLY (top-level object, not an array, no markdown/code fences)",
			},
		}
		return v, buildFixPrompt(v), nil
	}
	v = normalizeValidation(v)

	if !v.IsValid && len(v.Fixes) > 0 {
		// build a fix prompt that the caller can feed back into plan generation
		fixPrompt := buildFixPrompt(v)
		// keep deterministic warnings even when LLM finds issues
		if len(det.Warnings) > 0 {
			v.Warnings = append(v.Warnings, det.Warnings...)
			v.Warnings = uniqueStrings(v.Warnings)
		}
		return v, fixPrompt, nil
	}

	if len(det.Warnings) > 0 {
		v.Warnings = append(v.Warnings, det.Warnings...)
		v.Warnings = uniqueStrings(v.Warnings)
	}
	return v, "", nil
}

func normalizeValidation(v *PlanValidation) *PlanValidation {
	if v == nil {
		return v
	}
	keepIssue := func(s string) bool {
		l := strings.ToLower(strings.TrimSpace(s))
		if l == "" {
			return false
		}
		if strings.Contains(l, "disregard") {
			return false
		}
		if strings.Contains(l, "cloudfront does not") && strings.Contains(l, "websocket") {
			return false
		}
		if strings.Contains(l, "iam policy arn is malformed") && strings.Contains(l, "arn:aws:iam::aws:policy/") {
			return false
		}
		return true
	}

	issues := make([]string, 0, len(v.Issues))
	for _, item := range v.Issues {
		if keepIssue(item) {
			issues = append(issues, strings.TrimSpace(item))
		}
	}
	warnings := make([]string, 0, len(v.Warnings))
	for _, item := range v.Warnings {
		if keepIssue(item) {
			warnings = append(warnings, strings.TrimSpace(item))
		}
	}

	v.Issues = uniqueStrings(issues)
	v.Warnings = uniqueStrings(warnings)
	if len(v.Issues) == 0 {
		v.IsValid = true
		v.Fixes = nil
	}
	return v
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
		Language         string   `json:"language"`
		Framework        string   `json:"framework"`
		PackageManager   string   `json:"packageManager"`
		IsMonorepo       bool     `json:"isMonorepo"`
		HasDocker        bool     `json:"hasDocker"`
		HasCompose       bool     `json:"hasCompose"`
		Ports            []int    `json:"ports"`
		EnvVars          []string `json:"envVars"`
		HasDB            bool     `json:"hasDb"`
		DBType           string   `json:"dbType"`
		BootstrapScripts []string `json:"bootstrapScripts,omitempty"`
		EnvExampleFiles  []string `json:"envExampleFiles,omitempty"`
		MigrationHints   []string `json:"migrationHints,omitempty"`
		NativeDeps       []string `json:"nativeDeps,omitempty"`
		BuildOutputDir   string   `json:"buildOutputDir,omitempty"`
		IsStaticSite     bool     `json:"isStaticSite"`
		LockFiles        []string `json:"lockFiles,omitempty"`
	}{
		Language:         p.Language,
		Framework:        p.Framework,
		PackageManager:   p.PackageManager,
		IsMonorepo:       p.IsMonorepo,
		HasDocker:        p.HasDocker,
		HasCompose:       p.HasCompose,
		Ports:            p.Ports,
		EnvVars:          p.EnvVars,
		HasDB:            p.HasDB,
		DBType:           p.DBType,
		BootstrapScripts: p.BootstrapScripts,
		EnvExampleFiles:  p.EnvExampleFiles,
		MigrationHints:   p.MigrationHints,
		NativeDeps:       p.NativeDeps,
		BuildOutputDir:   p.BuildOutputDir,
		IsStaticSite:     p.IsStaticSite,
		LockFiles:        p.LockFiles,
	}, "", "  ")
	b.WriteString("## Static Analysis\n```json\n")
	b.WriteString(string(profileJSON))
	b.WriteString("\n```\n\n")

	b.WriteString(`## Your Task
Analyze the repo deeply and respond with JSON. READ THE README AND PACKAGE.JSON CAREFULLY.

### Core Analysis
1. What does this application DO? (1-2 sentences)
2. What services/components does it have? (e.g. "API server", "WebSocket gateway", "React frontend", "worker")
3. What external dependencies does it need? (databases, APIs, message queues)
4. What's the actual build pipeline? (real commands, not guesses)
5. What's the simplest way to run it locally?
6. How complex is this to deploy? (simple/moderate/complex)
7. What could go wrong during deployment?

### Node.js App Analysis (CRITICAL - read README carefully!)
8. listeningPort: What port does this app listen on?
   - Check README for "port", "PORT", "listens on", ":3000"
   - Check package.json scripts for --port flags
   - Check source code for .listen() calls
   - Default to 3000 ONLY if truly unknown

9. startCommand: How do you start this app?
   - Check package.json "scripts.start"
   - Check README for startup instructions

10. buildCommand: Does it need a build step?
    - Check package.json "scripts.build"
    - TypeScript apps need "npm run build"

11. nodeVersion: What Node.js version is required?
    - Check package.json "engines.node"
    - Check README for version requirements

12. requiredEnvVars: What environment variables are REQUIRED to run?
    - Check README for "configuration", "environment variables", "setup"
    - Check .env.example, .env.sample files
    - Include: name, description, required=true, example

13. optionalEnvVars: What env vars are optional with defaults?

14. healthEndpoint: Does it have a health check endpoint?
    - Check for /health, /healthz, /api/health routes
    - Leave empty if none

15. exposesHTTP: Does this app serve HTTP requests?
    - true for web servers, APIs, frontends
    - false for CLI tools, workers, bots that don't serve HTTP

16. preferDocker: Should we use Docker?
    - true if Dockerfile exists and README recommends it
    - false if README shows simpler install

17. globalInstall: Can it be installed globally?
    - e.g., "npm install -g packagename"

## Response Format (JSON only, no markdown fences)
{
  "appDescription": "...",
  "services": ["service1", "service2"],
  "externalDeps": ["postgres", "redis"],
  "buildPipeline": "npm install && npm run build",
  "runLocally": "npm start",
  "complexity": "moderate",
  "concerns": ["needs API_KEY"],
  "listeningPort": 3000,
  "startCommand": "npm start",
  "buildCommand": "npm run build",
  "nodeVersion": ">=18",
  "requiredEnvVars": [
    {"name": "API_KEY", "description": "API authentication key", "required": true, "example": "sk-xxx"}
  ],
  "optionalEnvVars": [
    {"name": "LOG_LEVEL", "description": "Logging verbosity", "required": false, "default": "info"}
  ],
  "healthEndpoint": "/health",
  "exposesHTTP": true,
  "preferDocker": false,
  "globalInstall": ""
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

func buildSmartArchitectPrompt(p *RepoProfile, deep *DeepAnalysis, targetProvider string, opts *DeployOptions) string {
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

	switch strings.ToLower(strings.TrimSpace(targetProvider)) {
	case "cloudflare":
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
	case "gcp":
		if IsOpenClawRepo(p, deep) {
			b.WriteString(OpenClawArchitectPromptGCP())
			break
		}
		b.WriteString(`
## GCP Options to Consider
1. **gcp-compute-engine** — VM + Docker Compose (best for stateful or always-on services) (~$8-30/mo)
2. **cloud-run** — managed containers (best for stateless HTTP apps)
3. **gke** — Kubernetes (overkill unless explicitly requested)

## GCP Services
- Compute Engine VM for always-on runtime
- Persistent disk for any stateful data
- Secret Manager for API keys and app secrets
- Cloud DNS + HTTPS LB only if public exposure is required

## Deployment CLI
All commands must use gcloud CLI only.

## Cost Estimation
Estimate the MONTHLY cost in USD.

## Response Format (JSON only, no markdown fences)
{
	"provider": "gcp",
	"method": "gcp-compute-engine",
	"reasoning": "A VM with Docker Compose is the most reliable and simplest path for this app.",
	"alternatives": [
		{"method": "cloud-run", "why_not": "Less suitable if this app requires local persistent state or long-lived connections"},
		{"method": "gke", "why_not": "Operationally complex for this use case"}
	],
	"buildSteps": [
		"Create Compute Engine VM",
		"Install Docker + Docker Compose",
		"Clone repo and configure .env",
		"Run docker compose build && docker compose up -d"
	],
	"runCmd": "docker compose up -d",
	"notes": ["Expose only required ports", "Persist any required state on disk"],
	"cpuMemory": "e2-standard-2",
	"needsAlb": false,
	"useApiGateway": false,
	"needsDb": false,
	"dbService": "",
	"estMonthly": "$12-25",
	"costBreakdown": ["Compute Engine VM", "Persistent disk", "Network egress"]
}`)
	case "azure":
		if IsOpenClawRepo(p, deep) {
			b.WriteString(OpenClawArchitectPromptAzure())
			break
		}
		b.WriteString(`
## Azure Options to Consider
1. **azure-vm** — VM + Docker Compose (best for stateful or always-on services) (~$10-35/mo)
2. **azure-container-apps** — managed containers (good for stateless services)
3. **aks** — Kubernetes (overkill unless explicitly requested)

## Azure Services
- VM for always-on runtime
- Managed disk for any stateful data
- Key Vault for API keys and app secrets

## Deployment CLI
All commands must use az CLI only.

## Cost Estimation
Estimate the MONTHLY cost in USD.

## Response Format (JSON only, no markdown fences)
{
	"provider": "azure",
	"method": "azure-vm",
	"reasoning": "A VM with Docker Compose is the most direct and operationally simple option.",
	"alternatives": [
		{"method": "azure-container-apps", "why_not": "Less ideal if this app requires local persistent state or long-lived connections"},
		{"method": "aks", "why_not": "Unnecessary complexity for this workload"}
	],
	"buildSteps": [
		"Create resource group and VM",
		"Install Docker + Docker Compose",
		"Clone repo and configure .env",
		"Run docker compose build && docker compose up -d"
	],
	"runCmd": "docker compose up -d",
	"notes": ["Persist any required state on disk", "Restrict inbound network rules"],
	"cpuMemory": "Standard_B2s",
	"needsAlb": false,
	"useApiGateway": false,
	"needsDb": false,
	"dbService": "",
	"estMonthly": "$12-30",
	"costBreakdown": ["VM", "Managed disk", "Public IP/Bandwidth"]
}`)
	default:
		// Add user's deployment target preference
		if opts != nil && opts.Target != "" && opts.Target != "fargate" {
			b.WriteString(fmt.Sprintf("\n## USER PREFERENCE: Deploy to %s", strings.ToUpper(opts.Target)))
			if opts.Target == "ec2" && opts.InstanceType != "" {
				b.WriteString(fmt.Sprintf(" (%s instance)\n", opts.InstanceType))
			} else {
				b.WriteString("\n")
			}
			b.WriteString("The user has explicitly requested this deployment target. Respect their choice.\n")
		}

		b.WriteString(`
## AWS Options to Consider
1. **ECS Fargate** — serverless containers, good for any Dockerized app (~$12-30/mo)
2. **EC2** — full control, SSH access, good for stateful apps or custom requirements (~$4-30/mo depending on instance)
3. **EKS** — Kubernetes, good if user already has EKS cluster (~$73/mo for control plane + nodes)
4. **App Runner** — even simpler than Fargate, auto-scales, good for web apps (~$5-25/mo)
5. **Lambda** — event-driven/cron, not for long-running servers (~$0-5/mo)
6. **S3 + CloudFront** — static sites only, nearly free (~$1-3/mo)
7. **Lightsail** — cheapest for simple apps (~$3.50-10/mo)

## Load Balancer / API Gateway Decision
Choose based on the application type:
- **API Gateway** — best for REST/HTTP APIs, serverless backends, pay-per-request pricing, built-in throttling/auth
- **ALB** — best for web apps with WebSockets, high traffic, sticky sessions, HTTP/2

## Cost Estimation
Estimate the MONTHLY cost in USD for your recommended architecture.
Break it down by service (compute, storage, networking, database).

## Response Format (JSON only, no markdown fences)
{
  "provider": "aws",
  "method": "ec2",
  "reasoning": "User requested EC2 deployment. This is a Dockerized Node.js app that will run well on a t3.small instance with docker compose.",
  "alternatives": [
    {"method": "ecs-fargate", "why_not": "User prefers EC2 for direct control"},
    {"method": "app-runner", "why_not": "User explicitly requested EC2"}
  ],
  "buildSteps": [
    "Create EC2 instance with Docker pre-installed",
    "Clone repo to instance",
    "Run docker compose up"
  ],
  "runCmd": "docker compose up",
  "notes": ["Port 3000 must be exposed in security group", "SSH access on port 22"],
  "cpuMemory": "t3.small",
  "needsAlb": false,
  "useApiGateway": false,
  "needsDb": false,
  "dbService": "",
  "estMonthly": "$15-25",
  "costBreakdown": ["EC2 t3.small: ~$15/mo", "EBS 20GB: ~$2/mo"]
}`)
	}

	return b.String()
}

// --- Phase 4: Validation ---

func buildValidationPrompt(planJSON string, p *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, requireDockerCommandsInPlan bool) string {
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
	if docker != nil {
		if docker.PrimaryPort > 0 {
			b.WriteString(fmt.Sprintf("- Docker primary port: %d\n", docker.PrimaryPort))
		}
		if docker.RunCommand != "" {
			b.WriteString(fmt.Sprintf("- Docker recommended run: %s\n", docker.RunCommand))
		}
		if len(docker.Warnings) > 0 {
			b.WriteString(fmt.Sprintf("- Docker warnings: %s\n", strings.Join(docker.Warnings, "; ")))
		}
	}

	b.WriteString("\n## Generated Plan\n```json\n")
	b.WriteString(planJSON)
	b.WriteString("\n```\n\n")

	b.WriteString("## Check For\n")
	b.WriteString("1. Are commands in the right ORDER?\n")
	b.WriteString("2. Are ALL required ports exposed in security groups and task definitions?\n")
	b.WriteString("3. Are environment variables passed to the container?\n")
	b.WriteString("4. Does the plan use the existing Dockerfile (not try to build from scratch)?\n")
	b.WriteString("5. Are IAM roles and policies created BEFORE they're referenced?\n")
	b.WriteString("6. Will the app actually be accessible from the internet after deployment?\n")
	b.WriteString("7. Are there any missing steps?\n")
	b.WriteString("8. Are resource references (ARNs, IDs) properly chained?\n")
	if requireDockerCommandsInPlan {
		b.WriteString("9. If Docker deployment is used, does the plan include docker build/push steps in the command list (and before workload launch)?\n")
	} else {
		b.WriteString("9. If Docker build/push is handled outside the command list by the deploy orchestrator, do NOT mark missing docker build/push commands as an issue.\n")
	}
	b.WriteString(`

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
		// Common failure mode: model returns an array (often a list of issues).
		var issues []string
		if err2 := json.Unmarshal([]byte(raw), &issues); err2 == nil {
			return &PlanValidation{IsValid: false, Issues: uniqueStrings(issues)}, nil
		}

		// Another failure mode: array of objects (each an issue/fix/warning item).
		var items []map[string]any
		if err3 := json.Unmarshal([]byte(raw), &items); err3 == nil {
			var out PlanValidation
			out.IsValid = false
			for _, it := range items {
				if s, ok := it["issue"].(string); ok && strings.TrimSpace(s) != "" {
					out.Issues = append(out.Issues, strings.TrimSpace(s))
				}
				if s, ok := it["fix"].(string); ok && strings.TrimSpace(s) != "" {
					out.Fixes = append(out.Fixes, strings.TrimSpace(s))
				}
				if s, ok := it["warning"].(string); ok && strings.TrimSpace(s) != "" {
					out.Warnings = append(out.Warnings, strings.TrimSpace(s))
				}
			}
			out.Issues = uniqueStrings(out.Issues)
			out.Fixes = uniqueStrings(out.Fixes)
			out.Warnings = uniqueStrings(out.Warnings)
			if len(out.Issues) > 0 || len(out.Fixes) > 0 || len(out.Warnings) > 0 {
				return &out, nil
			}
		}

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
func buildIntelligentPrompt(p *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, arch *ArchitectDecision, strat DeployStrategy, infraSnap *InfraSnapshot, cfInfraSnap *CFInfraSnapshot, opts *DeployOptions) string {
	var b strings.Builder
	resourcePrefix := repoResourcePrefix(p.RepoURL, opts.DeployID)

	providerLabel := "AWS"
	switch strings.ToLower(strings.TrimSpace(strat.Provider)) {
	case "cloudflare":
		providerLabel = "Cloudflare"
	case "gcp":
		providerLabel = "GCP"
	case "azure":
		providerLabel = "Azure"
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
	if docker != nil {
		b.WriteString("\n")
		b.WriteString(docker.FormatForPrompt())
		if docker.PrimaryPort > 0 {
			b.WriteString(fmt.Sprintf("- Use Docker agent primary port (%d) for load balancer target groups and health checks.\n", docker.PrimaryPort))
		}
		if docker.BuildCommand != "" {
			b.WriteString(fmt.Sprintf("- Prefer Docker build command: %s\n", docker.BuildCommand))
		}
		if docker.RunCommand != "" {
			b.WriteString(fmt.Sprintf("- Prefer Docker runtime command: %s\n", docker.RunCommand))
		}
	}
	AppendOpenClawDeploymentRequirements(&b, p, deep)
	AppendWordPressDeploymentRequirements(&b, p, deep)
	if pf := BuildPreflightReport(p, docker, deep); pf != nil {
		ctx := pf.FormatForPrompt()
		if strings.TrimSpace(ctx) != "" {
			b.WriteString("\n")
			b.WriteString(ctx)
		}
		if pf.IsStaticSite {
			b.WriteString("\n## Static Site Notes\n")
			b.WriteString("- If this is an SPA, configure routing so deep links work (e.g. CloudFront custom error response 404->200 /index.html, or platform-specific redirect rules).\n")
		}
	}
	if len(p.BootstrapScripts) > 0 {
		b.WriteString("\n## Bootstrap Scripts (If deploying on a VM)\n")
		b.WriteString("- This repo includes bootstrap/onboarding scripts. If the workload depends on them, run them BEFORE starting services.\n")
		b.WriteString("- If a bootstrap script is interactive, include it as an explicit interactive step (do not silently skip it).\n")
	}

	b.WriteString("\n## Naming\n")
	b.WriteString(fmt.Sprintf("- Use resource prefix: %s (repo name + short hash)\n", resourcePrefix))

	// deployment instructions based on chosen method
	b.WriteString("\n## Deployment Instructions\n")
	switch strat.Method {
	case "ec2":
		// WordPress uses Docker Hub images directly (no ECR build)
		if IsWordPressRepo(p, deep) {
			b.WriteString(WordPressEC2Prompt(p, opts))
		} else {
			b.WriteString(ec2Prompt(p, arch, deep, opts))
		}
	case "eks":
		b.WriteString(eksPrompt(p, arch, deep, opts))
	case "ecs-fargate":
		b.WriteString(smartECSPrompt(p, arch, deep, opts))
	case "app-runner":
		b.WriteString(appRunnerPrompt(p, arch, opts))
	case "s3-cloudfront":
		b.WriteString(s3CloudfrontIntelligentPrompt(p))
	case "lightsail":
		b.WriteString(lightsailPrompt(p, arch, opts))
	case "cf-pages":
		b.WriteString(cfPagesPrompt(p, deep, opts))
	case "cf-workers":
		b.WriteString(cfWorkersPrompt(p, deep, opts))
	case "cf-containers":
		b.WriteString(cfContainersPrompt(p, arch, deep, opts))
	case "gcp-compute-engine":
		b.WriteString(gcpComputeEnginePrompt(p, deep, opts))
	case "azure-vm":
		b.WriteString(azureVMPrompt(p, deep, opts))
	default:
		switch strings.ToLower(strings.TrimSpace(strat.Provider)) {
		case "cloudflare":
			b.WriteString(cfWorkersPrompt(p, deep, opts))
		case "gcp":
			b.WriteString(gcpComputeEnginePrompt(p, deep, opts))
		case "azure":
			b.WriteString(azureVMPrompt(p, deep, opts))
		default:
			b.WriteString(smartECSPrompt(p, arch, deep, opts))
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
		switch strings.ToLower(strings.TrimSpace(strat.Provider)) {
		case "cloudflare":
			b.WriteString("- Store sensitive values via Wrangler secrets\n")
		case "gcp":
			b.WriteString("- Store sensitive values in GCP Secret Manager\n")
		case "azure":
			b.WriteString("- Store sensitive values in Azure Key Vault\n")
		default:
			b.WriteString("- Store sensitive env vars in AWS Secrets Manager or SSM Parameter Store\n")
		}
		b.WriteString("- Pass them to the runtime via secure environment injection\n")
		b.WriteString(fmt.Sprintf("- Required: %s\n", strings.Join(p.EnvVars, ", ")))
	}

	b.WriteString("\n## Rules\n")
	switch strings.ToLower(strings.TrimSpace(strat.Provider)) {
	case "cloudflare":
		b.WriteString("- All commands use npx wrangler CLI\n")
		b.WriteString("- Auth via CLOUDFLARE_API_TOKEN env var (already set)\n")
		b.WriteString(fmt.Sprintf("- Name projects/resources with prefix %s\n", resourcePrefix))
		b.WriteString("- Prefer free tier where possible\n")
		b.WriteString("- The plan must be fully executable with npx wrangler commands only\n")
		b.WriteString("- Commands must be in the correct dependency order\n")
		b.WriteString("- Every resource that's referenced must be created first\n")
	case "gcp":
		b.WriteString("- The plan must be fully executable with gcloud CLI only\n")
		b.WriteString("- Prefer minimal, cost-effective machine sizes\n")
		b.WriteString("- Persist state/workspace on disk\n")
		b.WriteString("- Commands must be in dependency order\n")
		b.WriteString(fmt.Sprintf("- Name resources with prefix %s\n", resourcePrefix))
	case "azure":
		b.WriteString("- The plan must be fully executable with az CLI only\n")
		b.WriteString("- Prefer minimal, cost-effective VM sizes\n")
		b.WriteString("- Persist state/workspace on managed disk\n")
		b.WriteString("- Commands must be in dependency order\n")
		b.WriteString(fmt.Sprintf("- Name resources with prefix %s\n", resourcePrefix))
	default:
		b.WriteString("- Use the default VPC and its existing subnets when possible\n")
		b.WriteString(fmt.Sprintf("- Name resources with prefix %s\n", resourcePrefix))
		b.WriteString(fmt.Sprintf("- Tag all resources with Project=%s\n", resourcePrefix))
		b.WriteString("- Prefer minimal, cost-effective resource sizes\n")
		b.WriteString("- The plan must be fully executable with AWS CLI only\n")
		b.WriteString("- Commands must be in the correct dependency order\n")
		b.WriteString("- Every resource that's referenced must be created first\n")
	}

	return b.String()
}

// --- Method-specific prompts ---

func smartECSPrompt(p *RepoProfile, arch *ArchitectDecision, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy using ECS Fargate (serverless containers):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for ECR repo, cluster, service, security group, and ALB (if used)\n", resourcePrefix))
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

func gcpComputeEnginePrompt(p *RepoProfile, deep *DeepAnalysis, opts *DeployOptions) string {
	if IsOpenClawRepo(p, deep) {
		return OpenClawGCPComputeEnginePrompt(p, deep, opts)
	}
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy using GCP Compute Engine (VM + Docker Compose):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for VM/network/firewall resources\n", resourcePrefix))
	b.WriteString("1. Create a VPC firewall rule for required inbound ports\n")
	b.WriteString("2. Create a Compute Engine VM (Ubuntu LTS)\n")
	b.WriteString("3. Install Docker and Docker Compose on the VM\n")
	b.WriteString(fmt.Sprintf("4. Clone repository: %s\n", p.RepoURL))
	b.WriteString("5. Create .env with required env vars and secrets\n")
	b.WriteString("6. If the app is stateful, create persistent directories/volumes on disk\n")
	b.WriteString("7. Build and start with: docker compose build && docker compose up -d\n")
	b.WriteString("8. Verify service health and endpoint readiness\n")
	return b.String()
}

func azureVMPrompt(p *RepoProfile, deep *DeepAnalysis, opts *DeployOptions) string {
	if IsOpenClawRepo(p, deep) {
		return OpenClawAzureVMPrompt(p, deep, opts)
	}
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy using Azure VM (Docker Compose):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for resource group/NSG/VM resources\n", resourcePrefix))
	b.WriteString("1. Create resource group and network security group with least-privilege inbound rules\n")
	b.WriteString("2. Create Ubuntu VM with managed disk\n")
	b.WriteString("3. Install Docker and Docker Compose\n")
	b.WriteString(fmt.Sprintf("4. Clone repository: %s\n", p.RepoURL))
	b.WriteString("5. Create .env with required env vars and secrets\n")
	b.WriteString("6. If the app is stateful, create persistent directories/volumes on disk\n")
	b.WriteString("7. Build and start with: docker compose build && docker compose up -d\n")
	b.WriteString("8. Verify service health and endpoint readiness\n")
	return b.String()
}

// (OpenClaw helpers are in openclaw.go)

func appRunnerPrompt(p *RepoProfile, arch *ArchitectDecision, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy using AWS App Runner (simplest container hosting):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for ECR repo + App Runner service\n", resourcePrefix))

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

func lightsailPrompt(p *RepoProfile, arch *ArchitectDecision, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy using AWS Lightsail (cheapest option, $3.50/mo):\n")
	b.WriteString(fmt.Sprintf("Naming: use prefix %s for the Lightsail service/container\n", resourcePrefix))
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

func cfPagesPrompt(p *RepoProfile, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
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
	b.WriteString(fmt.Sprintf("3. Create Pages project: npx wrangler pages project create %s --production-branch main\n", resourcePrefix))
	b.WriteString(fmt.Sprintf("4. Deploy built assets: npx wrangler pages deploy %s --project-name %s\n", outputDir, resourcePrefix))
	b.WriteString("5. Pages provides an automatic *.pages.dev URL + HTTPS\n")

	if len(p.EnvVars) > 0 {
		b.WriteString(fmt.Sprintf("6. Set environment variables: npx wrangler pages secret put <KEY> --project-name %s\n", resourcePrefix))
	}

	return b.String()
}

func cfWorkersPrompt(p *RepoProfile, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
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
		b.WriteString(fmt.Sprintf("   - name = \"%s\"\n", resourcePrefix))
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

func cfContainersPrompt(p *RepoProfile, arch *ArchitectDecision, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	b.WriteString("Deploy as a Cloudflare Container (Docker on Cloudflare edge):\n")

	if p.HasDocker {
		b.WriteString("1. Build Docker image using existing Dockerfile:\n")
		b.WriteString(fmt.Sprintf("   npx wrangler containers build . -t %s:latest\n", resourcePrefix))
	} else {
		b.WriteString("1. Generate a Dockerfile for the application, then build:\n")
		b.WriteString(fmt.Sprintf("   - Base image: %s\n", dockerBaseImage(p)))
		b.WriteString(fmt.Sprintf("   npx wrangler containers build . -t %s:latest\n", resourcePrefix))
	}

	b.WriteString("2. Push image to Cloudflare registry:\n")
	b.WriteString(fmt.Sprintf("   npx wrangler containers push %s:latest\n", resourcePrefix))
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

// ec2Prompt generates deployment instructions for EC2
func ec2Prompt(p *RepoProfile, arch *ArchitectDecision, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	resourcePrefix := repoResourcePrefix(p.RepoURL, opts.DeployID)
	projectTag := resourcePrefix

	// Tag values have generous limits; some AWS resource names do not.
	vpcTagName := awsName(resourcePrefix, "-vpc", 128)
	subnet1aTagName := awsName(resourcePrefix, "-public-1a", 128)
	subnet1bTagName := awsName(resourcePrefix, "-public-1b", 128)
	igwTagName := awsName(resourcePrefix, "-igw", 128)
	rtTagName := awsName(resourcePrefix, "-public-rt", 128)

	albSGName := awsName(resourcePrefix, "-alb-sg", 255)
	ec2SGName := awsName(resourcePrefix, "-ec2-sg", 255)

	roleName := awsName(resourcePrefix, "-ec2-role", 64)
	profileName := awsName(resourcePrefix, "-ec2-profile", 128)

	ecrRepoName := awsName(resourcePrefix, "", 256)
	localImageName := awsName(resourcePrefix, "", 128)
	instanceName := awsName(resourcePrefix, "", 128)
	albName := awsName(resourcePrefix, "-alb", 32)
	tgName := awsName(resourcePrefix, "-tg", 32)

	instanceType := "t3.small"
	if opts != nil && opts.InstanceType != "" {
		instanceType = opts.InstanceType
	}

	b.WriteString(fmt.Sprintf("Deploy to EC2 instance (%s) with Docker.\n\n", instanceType))
	b.WriteString("IMPORTANT: Generate AWS CLI commands that are fully executable. Use elbv2 (not ec2) for load balancers.\n\n")

	// VPC setup - be very explicit
	if opts != nil && opts.NewVPC {
		b.WriteString("## MANDATORY: Create a NEW VPC (do NOT use default VPC)\n")
		b.WriteString("You MUST create all these resources in order:\n\n")
		b.WriteString("1. Create VPC:\n")
		b.WriteString(fmt.Sprintf("   aws ec2 create-vpc --cidr-block 10.0.0.0/16 --tag-specifications 'ResourceType=vpc,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]'\n", vpcTagName, projectTag))
		b.WriteString("   Save the VpcId from output.\n\n")
		b.WriteString("2. Enable DNS hostnames on VPC:\n")
		b.WriteString("   aws ec2 modify-vpc-attribute --vpc-id <VPC_ID> --enable-dns-hostnames '{\"Value\":true}'\n\n")
		b.WriteString("3. Create public subnet in us-east-1a:\n")
		b.WriteString(fmt.Sprintf("   aws ec2 create-subnet --vpc-id <VPC_ID> --cidr-block 10.0.1.0/24 --availability-zone us-east-1a --tag-specifications 'ResourceType=subnet,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]'\n", subnet1aTagName, projectTag))
		b.WriteString("   Save the SubnetId.\n\n")
		b.WriteString("4. Create public subnet in us-east-1b (for ALB):\n")
		b.WriteString(fmt.Sprintf("   aws ec2 create-subnet --vpc-id <VPC_ID> --cidr-block 10.0.2.0/24 --availability-zone us-east-1b --tag-specifications 'ResourceType=subnet,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]'\n", subnet1bTagName, projectTag))
		b.WriteString("   Save the SubnetId.\n\n")
		b.WriteString("5. Create Internet Gateway:\n")
		b.WriteString(fmt.Sprintf("   aws ec2 create-internet-gateway --tag-specifications 'ResourceType=internet-gateway,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]'\n", igwTagName, projectTag))
		b.WriteString("   Save the InternetGatewayId.\n\n")
		b.WriteString("6. Attach IGW to VPC:\n")
		b.WriteString("   aws ec2 attach-internet-gateway --internet-gateway-id <IGW_ID> --vpc-id <VPC_ID>\n\n")
		b.WriteString("7. Create route table:\n")
		b.WriteString(fmt.Sprintf("   aws ec2 create-route-table --vpc-id <VPC_ID> --tag-specifications 'ResourceType=route-table,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]'\n", rtTagName, projectTag))
		b.WriteString("   Save the RouteTableId.\n\n")
		b.WriteString("8. Add route to Internet Gateway:\n")
		b.WriteString("   aws ec2 create-route --route-table-id <RT_ID> --destination-cidr-block 0.0.0.0/0 --gateway-id <IGW_ID>\n\n")
		b.WriteString("9. Associate route table with subnets:\n")
		b.WriteString("   aws ec2 associate-route-table --route-table-id <RT_ID> --subnet-id <SUBNET_1A_ID>\n")
		b.WriteString("   aws ec2 associate-route-table --route-table-id <RT_ID> --subnet-id <SUBNET_1B_ID>\n\n")
		b.WriteString("10. Enable auto-assign public IP on subnets:\n")
		b.WriteString("    aws ec2 modify-subnet-attribute --subnet-id <SUBNET_1A_ID> --map-public-ip-on-launch\n")
		b.WriteString("    aws ec2 modify-subnet-attribute --subnet-id <SUBNET_1B_ID> --map-public-ip-on-launch\n\n")
	}

	b.WriteString("## Security Groups (CRITICAL: Follow least-privilege)\n")
	b.WriteString("Create TWO security groups:\n\n")
	b.WriteString("1. ALB Security Group (allows public HTTP/HTTPS):\n")
	b.WriteString(fmt.Sprintf("   aws ec2 create-security-group --group-name %s --description 'ALB security group' --vpc-id <VPC_ID>\n", albSGName))
	b.WriteString("   aws ec2 authorize-security-group-ingress --group-id <ALB_SG_ID> --protocol tcp --port 80 --cidr 0.0.0.0/0\n")
	b.WriteString("   aws ec2 authorize-security-group-ingress --group-id <ALB_SG_ID> --protocol tcp --port 443 --cidr 0.0.0.0/0\n\n")
	b.WriteString("2. EC2 Security Group (ONLY allows traffic from ALB, NOT from internet):\n")
	b.WriteString(fmt.Sprintf("   aws ec2 create-security-group --group-name %s --description 'EC2 security group' --vpc-id <VPC_ID>\n", ec2SGName))
	if len(p.Ports) > 0 {
		for _, port := range p.Ports {
			b.WriteString(fmt.Sprintf("   aws ec2 authorize-security-group-ingress --group-id <EC2_SG_ID> --protocol tcp --port %d --source-group <ALB_SG_ID>\n", port))
		}
	}
	b.WriteString("   DO NOT open SSH (22) to 0.0.0.0/0. Use SSM Session Manager instead.\n\n")

	b.WriteString("## IAM Role and Instance Profile\n")
	b.WriteString("Create IAM role AND instance profile (both required for EC2):\n\n")
	b.WriteString("1. Create role:\n")
	b.WriteString(fmt.Sprintf("   aws iam create-role --role-name %s --assume-role-policy-document '{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"ec2.amazonaws.com\"},\"Action\":\"sts:AssumeRole\"}]}'\n\n", roleName))
	b.WriteString("2. Attach policies (include ECR read for pulling images):\n")
	b.WriteString(fmt.Sprintf("   aws iam attach-role-policy --role-name %s --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore\n", roleName))
	b.WriteString(fmt.Sprintf("   aws iam attach-role-policy --role-name %s --policy-arn arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy\n", roleName))
	b.WriteString(fmt.Sprintf("   aws iam attach-role-policy --role-name %s --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly\n\n", roleName))
	b.WriteString("3. Create instance profile:\n")
	b.WriteString(fmt.Sprintf("   aws iam create-instance-profile --instance-profile-name %s\n\n", profileName))
	b.WriteString("4. Add role to instance profile:\n")
	b.WriteString(fmt.Sprintf("   aws iam add-role-to-instance-profile --instance-profile-name %s --role-name %s\n\n", profileName, roleName))

	// ECR image build approach - build locally/CI and push to ECR
	b.WriteString("## Container Image (Build locally and push to ECR)\n")
	b.WriteString("IMPORTANT: Do NOT build Docker images on the EC2 instance. Small instances run out of memory.\n")
	b.WriteString("Instead, build locally or use CI, then push to ECR and pull on EC2.\n\n")
	b.WriteString("1. Create ECR repository:\n")
	b.WriteString(fmt.Sprintf("   aws ecr create-repository --repository-name %s --image-scanning-configuration scanOnPush=true\n", ecrRepoName))
	b.WriteString("   Save the repositoryUri as <ECR_URI>.\n\n")
	b.WriteString("2. Get ECR login token (for local build machine):\n")
	b.WriteString("   aws ecr get-login-password --region <REGION> | docker login --username AWS --password-stdin <ACCOUNT_ID>.dkr.ecr.<REGION>.amazonaws.com\n\n")
	b.WriteString("3. Build and push image (run these commands on local machine or CI):\n")
	b.WriteString(fmt.Sprintf("   git clone %s /tmp/app && cd /tmp/app\n", p.RepoURL))
	b.WriteString(fmt.Sprintf("   docker build -t %s .\n", localImageName))
	b.WriteString(fmt.Sprintf("   docker tag %s:latest <ECR_URI>:latest\n", localImageName))
	b.WriteString("   docker push <ECR_URI>:latest\n\n")

	b.WriteString("## Launch EC2 Instance\n")
	b.WriteString(fmt.Sprintf("Launch %s instance with Amazon Linux 2023:\n\n", instanceType))
	b.WriteString("Get latest AL2023 AMI ID:\n")
	b.WriteString("   aws ssm get-parameters --names /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-6.1-x86_64 --query 'Parameters[0].Value' --output text\n\n")

	// User-data is handled automatically by the maker using the <USER_DATA> placeholder
	b.WriteString("Launch the instance with user-data placeholder (the maker will auto-generate the Docker startup script):\n\n")
	b.WriteString("   aws ec2 run-instances \\\n")
	b.WriteString(fmt.Sprintf("     --instance-type %s \\\n", instanceType))
	b.WriteString("     --image-id <AMI_ID> \\\n")
	b.WriteString("     --subnet-id <SUBNET_1A_ID> \\\n")
	b.WriteString("     --security-group-ids <EC2_SG_ID> \\\n")
	b.WriteString(fmt.Sprintf("     --iam-instance-profile Name=%s \\\n ", profileName))
	b.WriteString(fmt.Sprintf("     --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=%s},{Key=Project,Value=%s}]' \\\n ", instanceName, projectTag))
	b.WriteString("     --metadata-options 'HttpTokens=required,HttpPutResponseHopLimit=2,HttpEndpoint=enabled' \\\n")
	b.WriteString("     --user-data <USER_DATA>\n\n")
	b.WriteString("NOTE: The <USER_DATA> placeholder will be automatically replaced with a base64-encoded script that:\n")
	b.WriteString("  - Installs Docker\n")
	b.WriteString("  - Logs into ECR\n")
	b.WriteString("  - Pulls and runs the container image\n\n")

	b.WriteString("IMPORTANT: The run-instances command MUST include a produces field to capture INSTANCE_ID:\n")
	b.WriteString("   \"produces\": {\"INSTANCE_ID\": \"$.Instances[0].InstanceId\"}\n\n")

	b.WriteString("Wait for instance to be running:\n")
	b.WriteString("   aws ec2 wait instance-running --instance-ids <INSTANCE_ID>\n\n")

	// env vars
	if len(p.EnvVars) > 0 {
		b.WriteString("## Environment Variables (Secrets Manager)\n")
		b.WriteString("Store application secrets:\n")
		for _, v := range p.EnvVars {
			b.WriteString(fmt.Sprintf("   aws secretsmanager create-secret --name %s/%s --secret-string '<value>'\n", resourcePrefix, v))
		}
		b.WriteString("\n")
	}

	// ALB setup - be very explicit about using elbv2
	b.WriteString("## Application Load Balancer (use elbv2 commands, NOT ec2)\n")
	b.WriteString("1. Create ALB:\n")
	b.WriteString("   aws elbv2 create-load-balancer \\\n")
	b.WriteString(fmt.Sprintf("     --name %s \\\n ", albName))
	b.WriteString("     --subnets <SUBNET_1A_ID> <SUBNET_1B_ID> \\\n")
	b.WriteString("     --security-groups <ALB_SG_ID> \\\n")
	b.WriteString("     --scheme internet-facing \\\n")
	b.WriteString("     --type application\n")
	b.WriteString("   Save the LoadBalancerArn and DNSName.\n\n")

	appPort := 80
	if len(p.Ports) > 0 {
		appPort = p.Ports[0]
	}
	b.WriteString("2. Create target group:\n")
	b.WriteString("   aws elbv2 create-target-group \\\n")
	b.WriteString(fmt.Sprintf("     --name %s \\\n ", tgName))
	b.WriteString(fmt.Sprintf("     --protocol HTTP --port %d \\\n", appPort))
	b.WriteString("     --vpc-id <VPC_ID> \\\n")
	b.WriteString("     --target-type instance \\\n")
	healthPath := "/"
	if deep != nil {
		if hp := strings.TrimSpace(deep.HealthEndpoint); strings.HasPrefix(hp, "/") {
			healthPath = hp
		}
	}
	b.WriteString(fmt.Sprintf("     --health-check-path %s --health-check-port %d\n", healthPath, appPort))
	b.WriteString("   Save the TargetGroupArn.\n\n")

	b.WriteString("3. Register EC2 instance with target group:\n")
	b.WriteString("   aws elbv2 register-targets --target-group-arn <TG_ARN> --targets Id=<INSTANCE_ID>\n\n")

	b.WriteString("4. Create listener:\n")
	b.WriteString("   aws elbv2 create-listener \\\n")
	b.WriteString("     --load-balancer-arn <ALB_ARN> \\\n")
	b.WriteString("     --protocol HTTP --port 80 \\\n")
	b.WriteString("     --default-actions Type=forward,TargetGroupArn=<TG_ARN>\n\n")

	b.WriteString("5. Wait for target to be healthy:\n")
	b.WriteString("   aws elbv2 wait target-in-service --target-group-arn <TG_ARN> --targets Id=<INSTANCE_ID>\n\n")

	if IsOpenClawRepo(p, deep) {
		b.WriteString("## OpenClaw HTTPS Requirement (CloudFront)")
		b.WriteString("\nFor OpenClaw pairing, HTTPS is required. Create CloudFront in front of ALB:\n")
		b.WriteString("6. Create CloudFront distribution with ALB DNS as origin:\n")
		b.WriteString("   aws cloudfront create-distribution --distribution-config '{...}'\n")
		b.WriteString("   - Origin DomainName: <ALB_DNS>\n")
		b.WriteString("   - ViewerProtocolPolicy: redirect-to-https\n")
		b.WriteString("   Save CloudFront DomainName as <CLOUDFRONT_DOMAIN>.\n\n")
		b.WriteString("7. Wait for distribution deployment:\n")
		b.WriteString("   aws cloudfront wait distribution-deployed --id <CLOUDFRONT_ID>\n\n")
		b.WriteString("8. Output HTTPS pairing URL:\n")
		b.WriteString("   https://<CLOUDFRONT_DOMAIN>\n\n")
	}

	b.WriteString("## Output\n")
	if IsOpenClawRepo(p, deep) {
		b.WriteString("Primary URL (required for pairing): https://<CLOUDFRONT_DOMAIN>\n")
		b.WriteString("Fallback debug URL: http://<ALB_DNS>\n")
	} else {
		b.WriteString("The application will be accessible at the ALB DNS name (from step 1).\n")
		b.WriteString("URLs for this app:\n")
		b.WriteString("- Settings: http://<ALB_DNS>/settings\n")
		b.WriteString("- Chat: http://<ALB_DNS>/chat\n")
	}
	b.WriteString("DO NOT create any Lambda or CloudWatch Lambda alarms - this is an EC2 deployment.\n")

	return b.String()
}

// eksPrompt generates deployment instructions for EKS
func eksPrompt(p *RepoProfile, arch *ArchitectDecision, deep *DeepAnalysis, opts *DeployOptions) string {
	var b strings.Builder
	deployID := ""
	if opts != nil {
		deployID = opts.DeployID
	}
	resourcePrefix := repoResourcePrefix(p.RepoURL, deployID)
	namespace := kubeName(resourcePrefix, 63)
	b.WriteString("Deploy to existing EKS cluster:\n\n")

	b.WriteString("## Prerequisites\n")
	b.WriteString("- Existing EKS cluster with kubectl configured\n")
	b.WriteString("- ECR repository for container images\n\n")

	b.WriteString("## Step 1: Build and Push Image\n")
	b.WriteString("1. Create ECR repository if not exists\n")
	b.WriteString("2. Authenticate Docker to ECR\n")
	if p.HasDocker {
		b.WriteString("3. Build using existing Dockerfile: docker build -t <ecr-uri>:latest .\n")
	} else {
		b.WriteString(fmt.Sprintf("3. Generate Dockerfile with base image %s, then build\n", dockerBaseImage(p)))
	}
	b.WriteString("4. Push to ECR: docker push <ecr-uri>:latest\n\n")

	b.WriteString("## Step 2: Create Kubernetes Resources\n")
	b.WriteString("Generate and apply these K8s manifests:\n\n")

	b.WriteString("### Namespace\n")
	b.WriteString("```yaml\n")
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Namespace\n")
	b.WriteString("metadata:\n")
	b.WriteString(fmt.Sprintf("  name: %s\n", namespace))
	b.WriteString("```\n\n")

	b.WriteString("### Deployment\n")
	b.WriteString("```yaml\n")
	b.WriteString("apiVersion: apps/v1\n")
	b.WriteString("kind: Deployment\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: app\n")
	b.WriteString(fmt.Sprintf("  namespace: %s\n", namespace))
	b.WriteString("spec:\n")
	b.WriteString("  replicas: 2\n")
	b.WriteString("  selector:\n")
	b.WriteString("    matchLabels:\n")
	b.WriteString("      app: app\n")
	b.WriteString("  template:\n")
	b.WriteString("    metadata:\n")
	b.WriteString("      labels:\n")
	b.WriteString("        app: app\n")
	b.WriteString("    spec:\n")
	b.WriteString("      containers:\n")
	b.WriteString("      - name: app\n")
	b.WriteString("        image: <ecr-uri>:latest\n")
	if len(p.Ports) > 0 {
		b.WriteString("        ports:\n")
		for _, port := range p.Ports {
			b.WriteString(fmt.Sprintf("        - containerPort: %d\n", port))
		}
	}
	if len(p.EnvVars) > 0 {
		b.WriteString("        envFrom:\n")
		b.WriteString("        - secretRef:\n")
		b.WriteString("            name: app-secrets\n")
	}
	b.WriteString("```\n\n")

	b.WriteString("### Service\n")
	b.WriteString("```yaml\n")
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Service\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: app\n")
	b.WriteString(fmt.Sprintf("  namespace: %s\n", namespace))
	b.WriteString("spec:\n")
	b.WriteString("  type: LoadBalancer\n")
	b.WriteString("  selector:\n")
	b.WriteString("    app: app\n")
	b.WriteString("  ports:\n")
	if len(p.Ports) > 0 {
		b.WriteString(fmt.Sprintf("  - port: 80\n    targetPort: %d\n", p.Ports[0]))
	} else {
		b.WriteString("  - port: 80\n    targetPort: 8080\n")
	}
	b.WriteString("```\n\n")

	b.WriteString("## Step 3: Apply Resources\n")
	b.WriteString("```bash\n")
	b.WriteString("kubectl apply -f namespace.yaml\n")
	b.WriteString("kubectl apply -f deployment.yaml\n")
	b.WriteString("kubectl apply -f service.yaml\n")
	b.WriteString(fmt.Sprintf("kubectl get svc -n %s  # Get LoadBalancer URL\n", namespace))
	b.WriteString("```\n")

	return b.String()
}
