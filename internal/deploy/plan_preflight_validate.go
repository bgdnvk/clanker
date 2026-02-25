package deploy

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

type deterministicValidation struct {
	Issues   []string
	Fixes    []string
	Warnings []string
}

func runDeterministicPlanValidation(planJSON string, p *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis) deterministicValidation {
	var out deterministicValidation

	if containsSecretLikeText(planJSON) {
		out.Issues = append(out.Issues, "[HARD] plan appears to inline secrets (token/API key/private key) in args")
		out.Fixes = append(out.Fixes, "Replace any secret literals with placeholders; store secrets in a secret manager and inject at runtime")
	}

	var plan maker.Plan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return out
	}

	isOpenClaw := IsOpenClawRepo(p, deep)
	preflight := BuildPreflightReport(p, docker, deep)

	// Generic sanity check: a deploy plan should actually launch *something*.
	// This is intentionally conservative and only triggers when no obvious launch op is present.
	hasLaunch := false
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		service := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		// Common launch operations across AWS deployment targets.
		switch service {
		case "ec2":
			if op == "run-instances" {
				hasLaunch = true
			}
		case "ecs":
			if op == "create-service" || op == "run-task" {
				hasLaunch = true
			}
		case "apprunner":
			if op == "create-service" {
				hasLaunch = true
			}
		case "lambda":
			if op == "create-function" {
				hasLaunch = true
			}
		case "lightsail":
			if op == "create-container-service" || op == "create-instances" {
				hasLaunch = true
			}
		}
		if hasLaunch {
			break
		}
	}
	if !hasLaunch {
		out.Issues = append(out.Issues, "[HARD] deploy plan does not launch any workload (missing EC2/ECS/AppRunner/Lambda/Lightsail launch step)")
		// Keep the fix generic; provider-specific repair agent will use method constraints.
		if isOpenClaw {
			out.Fixes = append(out.Fixes, "Add an ec2 run-instances command that starts the workload via user-data + Docker and captures INSTANCE_ID via produces")
		} else {
			out.Fixes = append(out.Fixes, "Add a launch command appropriate for the chosen method (e.g., ec2 run-instances, ecs create-service, apprunner create-service, lambda create-function, or lightsail create-container-service)")
		}
	}
	appPorts := uniqueInts(nil)
	if p != nil {
		for _, port := range p.Ports {
			if port > 0 {
				appPorts = append(appPorts, port)
			}
		}
	}
	if docker != nil && docker.PrimaryPort > 0 {
		appPorts = append(appPorts, docker.PrimaryPort)
	}
	if deep != nil && deep.ListeningPort > 0 {
		appPorts = append(appPorts, deep.ListeningPort)
	}
	appPorts = uniqueInts(appPorts)

	// Plan-wide AWS checks (ports/health checks) for ALB-based EC2 deploys.
	awsChecks := validateAWSPlanCommands(&plan, appPorts, deep)
	out.Issues = append(out.Issues, awsChecks.Issues...)
	out.Fixes = append(out.Fixes, awsChecks.Fixes...)
	out.Warnings = append(out.Warnings, awsChecks.Warnings...)

	if isOpenClaw {
		openClawChecks := validateOpenClawPlanCommands(&plan)
		out.Issues = append(out.Issues, openClawChecks.Issues...)
		out.Fixes = append(out.Fixes, openClawChecks.Fixes...)
		out.Warnings = append(out.Warnings, openClawChecks.Warnings...)
	}

	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		if strings.TrimSpace(args[0]) != "ec2" || strings.TrimSpace(args[1]) != "run-instances" {
			continue
		}

		script := extractEC2UserDataScript(args)
		if strings.TrimSpace(script) == "" {
			out.Warnings = append(out.Warnings, "ec2 run-instances has no user-data; workload likely will not start")
			continue
		}

		if containsSecretLikeText(script) {
			out.Issues = append(out.Issues, "[HARD] user-data script appears to inline secrets")
			out.Fixes = append(out.Fixes, "Do not inline secrets in user-data; fetch them from Secrets Manager/SSM at boot")
		}
		if hasLikelyBrokenSingleQuoteLine(script) {
			out.Issues = append(out.Issues, "[HARD] user-data script appears to contain an unterminated single-quoted string")
			out.Fixes = append(out.Fixes, "Fix user-data quoting (e.g., close trailing single quotes such as echo '...') before execution")
		}

		lower := strings.ToLower(script)
		usesCompose := strings.Contains(lower, "docker compose") || strings.Contains(lower, "docker-compose")
		usesDockerRun := strings.Contains(lower, "docker run")
		usesDockerBuild := strings.Contains(lower, "docker build")
		usesPkgBuild := strings.Contains(lower, "npm run build") || strings.Contains(lower, "pnpm build") || strings.Contains(lower, "yarn build") || strings.Contains(lower, "bun run build")

		// SSM/bootstrap lint: common breakages on AL2023.
		if strings.Contains(lower, "amazon-linux-extras") && strings.Contains(lower, "docker") {
			out.Issues = append(out.Issues, "[HARD] user-data uses amazon-linux-extras to install docker (breaks on AL2023)")
			out.Fixes = append(out.Fixes, "Use dnf/yum install docker on AL2023, then systemctl enable/start docker")
		}
		if strings.Contains(lower, "docker") {
			if !strings.Contains(lower, "systemctl start docker") && !strings.Contains(lower, "service docker start") {
				out.Warnings = append(out.Warnings, "user-data uses docker but does not explicitly start the docker daemon")
			}
		}
		// ECR pull requires login.
		if strings.Contains(lower, ".dkr.ecr.") && strings.Contains(lower, "docker pull") {
			if !strings.Contains(lower, "aws ecr get-login-password") || !strings.Contains(lower, "docker login") {
				out.Issues = append(out.Issues, "[HARD] user-data pulls from ECR but does not perform ECR docker login")
				out.Fixes = append(out.Fixes, "Add: aws ecr get-login-password | docker login ... before docker pull")
			}
		}

		// Build-on-EC2 risk: avoid building large images/apps on small instances.
		if (usesDockerBuild || usesPkgBuild) && strings.Contains(strings.ToLower(findInstanceTypeInPlan(&plan)), "t3.") {
			out.Warnings = append(out.Warnings, "user-data builds artifacts on a small t3 instance; prefer building locally/CI and pushing to ECR")
		}

		// Compose hard-required env vars must be set or generated.
		if usesCompose && preflight != nil && len(preflight.ComposeHardEnvVars) > 0 {
			missing := missingEnvVarsInScript(script, preflight.ComposeHardEnvVars)
			if len(missing) > 0 {
				out.Issues = append(out.Issues, "[HARD] docker compose uses required env vars that are not set in user-data: "+strings.Join(missing, ", "))
				out.Fixes = append(out.Fixes, "Ensure user-data exports these env vars or writes a .env file with values before running docker compose")
			}
		}

		// OpenClaw special cases: if compose is used, onboarding/bootstrap must happen.
		if isOpenClaw {
			applyOpenClawUserDataValidation(&out, script, usesCompose, usesDockerRun)
		}

		// Package manager correctness (only if we see installs happening in user-data).
		if preflight != nil && preflight.PackageManager != "" {
			if scriptRunsNodeInstall(script) {
				pmIssues := validatePackageManagerUsage(script, preflight.PackageManager, preflight.LockFiles)
				out.Issues = append(out.Issues, pmIssues.Issues...)
				out.Fixes = append(out.Fixes, pmIssues.Fixes...)
				out.Warnings = append(out.Warnings, pmIssues.Warnings...)
			}
		}

		// Corepack/pnpm ordering lint.
		if preflight != nil && strings.EqualFold(preflight.PackageManager, "pnpm") {
			if strings.Contains(lower, "pnpm install") && !strings.Contains(lower, "corepack enable") {
				out.Warnings = append(out.Warnings, "pnpm detected without corepack enable; fresh VM may not have pnpm available")
			}
		}

		// Migrations warning: if repo suggests migrations and script doesn't mention migrate.
		if preflight != nil && len(preflight.MigrationHints) > 0 {
			if !strings.Contains(lower, "migrate") && !strings.Contains(lower, "prisma") && !strings.Contains(lower, "alembic") && !strings.Contains(lower, "goose") {
				out.Warnings = append(out.Warnings, "migration tooling detected but user-data does not mention migrations; first boot may fail")
			}
		}
	}

	out.Issues = uniqueStrings(out.Issues)
	out.Fixes = uniqueStrings(out.Fixes)
	out.Warnings = uniqueStrings(out.Warnings)
	return out
}

// DeterministicValidatePlan runs only the deterministic validation checks and returns a PlanValidation.
// This is used for incremental / checkpointed plan generation to avoid repeated LLM validation calls.
func DeterministicValidatePlan(planJSON string, profile *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis) *PlanValidation {
	det := runDeterministicPlanValidation(planJSON, profile, deep, docker)
	if len(det.Issues) > 0 {
		return &PlanValidation{IsValid: false, Issues: det.Issues, Fixes: det.Fixes, Warnings: det.Warnings}
	}
	return &PlanValidation{IsValid: true, Issues: nil, Fixes: nil, Warnings: det.Warnings}
}

func CheckBulkRepairInvariants(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis) *PlanValidation {
	if plan == nil {
		return &PlanValidation{IsValid: true}
	}

	issues := make([]string, 0, 12)
	fixes := make([]string, 0, 12)
	warnings := make([]string, 0, 8)

	if len(plan.Commands) == 0 {
		issues = append(issues, "[HARD] bulk invariant failed: plan has no commands")
		fixes = append(fixes, "Ensure repaired plan keeps a non-empty commands array")
	}
	if unresolved := GetUnresolvedPlaceholders(plan); len(unresolved) > 0 {
		issues = append(issues, "[HARD] bulk invariant failed: unresolved placeholders remain")
		fixes = append(fixes, "Resolve placeholder bindings so every <TOKEN> has a concrete produced value")
	}

	hasAddRoleToProfile := false
	hasGetInstanceProfileBeforeRun := false
	seenRunInstances := false

	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		svc := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if svc == "iam" && op == "add-role-to-instance-profile" {
			hasAddRoleToProfile = true
		}
		if !seenRunInstances && svc == "iam" && op == "get-instance-profile" {
			hasGetInstanceProfileBeforeRun = true
		}
		if svc == "ec2" && op == "run-instances" {
			seenRunInstances = true
			script := extractEC2UserDataScript(args)
			if hasLikelyBrokenSingleQuoteLine(script) {
				issues = append(issues, "[HARD] bulk invariant failed: user-data quote sanity check failed")
				fixes = append(fixes, "Fix unterminated single-quoted strings in user-data (for example closing echo '...')")
			}
		}
	}

	if hasAddRoleToProfile && seenRunInstances && !hasGetInstanceProfileBeforeRun {
		issues = append(issues, "[HARD] bulk invariant failed: missing IAM profile readiness check before ec2 run-instances")
		fixes = append(fixes, "Insert iam get-instance-profile after add-role-to-instance-profile and before ec2 run-instances")
	}

	if IsOpenClawRepo(profile, deep) {
		ocIssues, ocFixes := checkOpenClawProjectInvariants(plan)
		issues = append(issues, ocIssues...)
		fixes = append(fixes, ocFixes...)
	}

	issues = uniqueStrings(issues)
	fixes = uniqueStrings(fixes)
	warnings = uniqueStrings(warnings)
	return &PlanValidation{IsValid: len(issues) == 0, Issues: issues, Fixes: fixes, Warnings: warnings}
}

func CheckOpenClawBulkInvariants(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis) *PlanValidation {
	return CheckBulkRepairInvariants(plan, profile, deep)
}

func checkOpenClawProjectInvariants(plan *maker.Plan) ([]string, []string) {
	issues := make([]string, 0, 8)
	fixes := make([]string, 0, 8)
	hasCFCreate := false
	hasCFWait := false
	hasCFOutput := false
	hasALB := false

	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		svc := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if svc == "elbv2" && op == "create-load-balancer" {
			hasALB = true
		}
		if svc == "cloudfront" && op == "create-distribution" {
			hasCFCreate = true
		}
		if svc == "cloudfront" && op == "wait" && len(args) >= 3 && strings.EqualFold(strings.TrimSpace(args[2]), "distribution-deployed") {
			hasCFWait = true
		}
		if svc == "ec2" && op == "run-instances" {
			script := extractEC2UserDataScript(args)
			lower := strings.ToLower(script)
			onboardIdx := strings.Index(lower, "docker-setup.sh")
			if onboardIdx < 0 {
				onboardIdx = strings.Index(lower, "openclaw-cli onboard")
			}
			startIdx := strings.Index(lower, "up -d openclaw-gateway")
			if startIdx >= 0 && (onboardIdx < 0 || onboardIdx > startIdx) {
				issues = append(issues, "[HARD] OpenClaw invariant failed: onboarding must run before starting openclaw-gateway")
				fixes = append(fixes, "Run docker-setup.sh or openclaw-cli onboard before docker compose up -d openclaw-gateway")
			}
		}
		for k := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			if ku == "CLOUDFRONT_DOMAIN" || ku == "HTTPS_URL" {
				hasCFOutput = true
			}
		}
	}

	if hasALB && !(hasCFCreate && hasCFWait && hasCFOutput) {
		issues = append(issues, "[HARD] OpenClaw invariant failed: HTTPS pairing URL must be shipped via CloudFront (create + wait + output)")
		fixes = append(fixes, "Add CloudFront create-distribution, cloudfront wait distribution-deployed, and produces for CLOUDFRONT_DOMAIN/HTTPS_URL")
	}

	return uniqueStrings(issues), uniqueStrings(fixes)
}

type awsPlanChecks struct {
	Issues   []string
	Fixes    []string
	Warnings []string
}

func validateAWSPlanCommands(plan *maker.Plan, appPorts []int, deep *DeepAnalysis) awsPlanChecks {
	var out awsPlanChecks
	if plan == nil || len(plan.Commands) == 0 {
		return out
	}
	if len(appPorts) == 0 {
		return out
	}
	primaryPort := appPorts[0]

	// Collect SG ingress ports and target group ports.
	sgPorts := map[int]bool{}
	tgPort := 0
	healthPath := ""
	hasAddRoleToProfile := false
	hasGetInstanceProfileBeforeRun := false
	seenRunInstances := false
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		service := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if service == "iam" && op == "add-role-to-instance-profile" {
			hasAddRoleToProfile = true
		}
		if !seenRunInstances && service == "iam" && op == "get-instance-profile" {
			hasGetInstanceProfileBeforeRun = true
		}
		if service == "ec2" && op == "run-instances" {
			seenRunInstances = true
		}
		if service == "ec2" && op == "authorize-security-group-ingress" {
			if port := parseFlagInt(args, "--port"); port > 0 {
				sgPorts[port] = true
			}
			// If using ip-permissions, we can't reliably parse; ignore.
		}
		if service == "elbv2" && op == "create-target-group" {
			if port := parseFlagInt(args, "--port"); port > 0 {
				tgPort = port
			}
			hp := strings.TrimSpace(parseFlag(args, "--health-check-path"))
			if strings.HasPrefix(hp, "/") {
				healthPath = hp
			}
		}
	}

	if tgPort > 0 && tgPort != primaryPort {
		out.Warnings = append(out.Warnings, "ALB target group port does not match detected app port")
	}
	if len(sgPorts) > 0 {
		if !sgPorts[primaryPort] {
			out.Warnings = append(out.Warnings, "security group ingress may be missing the primary app port")
		}
	}
	// Health path sanity.
	if deep != nil {
		want := strings.TrimSpace(deep.HealthEndpoint)
		if strings.HasPrefix(want, "/") && healthPath != "" && want != healthPath {
			out.Warnings = append(out.Warnings, "target group health check path does not match detected health endpoint")
		}
		if strings.HasPrefix(want, "/") && healthPath == "" {
			out.Warnings = append(out.Warnings, "detected health endpoint but plan does not set --health-check-path")
		}
	}
	if hasAddRoleToProfile && seenRunInstances && !hasGetInstanceProfileBeforeRun {
		out.Issues = append(out.Issues, "[HARD] missing IAM instance-profile readiness check before ec2 run-instances")
		out.Fixes = append(out.Fixes, "Insert iam get-instance-profile (or equivalent retry/wait step) after add-role-to-instance-profile and before ec2 run-instances")
	}

	return out
}

func validateOpenClawPlanCommands(plan *maker.Plan) awsPlanChecks {
	var out awsPlanChecks
	if plan == nil || len(plan.Commands) == 0 {
		return out
	}

	hasALB := false
	hasCloudFrontCreate := false
	hasCloudFrontWait := false
	hasCloudFrontOutput := false
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		service := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if service == "elbv2" && op == "create-load-balancer" {
			hasALB = true
		}
		if service == "cloudfront" && op == "create-distribution" {
			hasCloudFrontCreate = true
		}
		if service == "cloudfront" && op == "wait" {
			if len(args) >= 3 && strings.EqualFold(strings.TrimSpace(args[2]), "distribution-deployed") {
				hasCloudFrontWait = true
			}
		}
		for k := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			if ku == "CLOUDFRONT_DOMAIN" || ku == "HTTPS_URL" {
				hasCloudFrontOutput = true
			}
		}
	}

	if hasALB && !(hasCloudFrontCreate && hasCloudFrontWait && hasCloudFrontOutput) {
		out.Issues = append(out.Issues, "[HARD] OpenClaw AWS plan with ALB is missing complete CloudFront HTTPS pairing steps")
		out.Fixes = append(out.Fixes, "Add cloudfront create-distribution + cloudfront wait distribution-deployed and produce CLOUDFRONT_DOMAIN/HTTPS_URL")
	}

	return out
}

func parseFlag(args []string, name string) string {
	name = strings.TrimSpace(name)
	for i := 0; i < len(args); i++ {
		if strings.TrimSpace(args[i]) == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(strings.TrimSpace(args[i]), name+"=") {
			return strings.TrimPrefix(strings.TrimSpace(args[i]), name+"=")
		}
	}
	return ""
}

func parseFlagInt(args []string, name string) int {
	v := strings.TrimSpace(parseFlag(args, name))
	if v == "" {
		return 0
	}
	// cheap parse; ints in flags are small
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func findInstanceTypeInPlan(plan *maker.Plan) string {
	if plan == nil {
		return ""
	}
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(args[0]), "ec2") && strings.EqualFold(strings.TrimSpace(args[1]), "run-instances") {
			if it := parseFlag(args, "--instance-type"); strings.TrimSpace(it) != "" {
				return strings.TrimSpace(it)
			}
		}
	}
	return ""
}

func uniqueInts(values []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(values))
	for _, v := range values {
		if v <= 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func extractEC2UserDataScript(args []string) string {
	// Supports: --user-data <v> and --user-data=<v>
	val := ""
	for i := 0; i < len(args); i++ {
		if strings.TrimSpace(args[i]) == "--user-data" && i+1 < len(args) {
			val = args[i+1]
			break
		}
		if strings.HasPrefix(strings.TrimSpace(args[i]), "--user-data=") {
			val = strings.TrimPrefix(strings.TrimSpace(args[i]), "--user-data=")
			break
		}
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}
	if decoded, ok := tryDecodeBase64UserData(val); ok {
		return decoded
	}
	return val
}

func tryDecodeBase64UserData(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if len(v) < 16 {
		return "", false
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return "", false
	}
	if len(v)%4 != 0 {
		return "", false
	}
	for _, ch := range v {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '+' || ch == '/' || ch == '=' {
			continue
		}
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(b) == 0 {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "#!") || strings.HasPrefix(strings.ToLower(s), "#cloud-config") {
		return s, true
	}
	return "", false
}

func missingEnvVarsInScript(script string, envVars []string) []string {
	if strings.TrimSpace(script) == "" || len(envVars) == 0 {
		return nil
	}
	lower := strings.ToLower(script)
	missing := make([]string, 0, len(envVars))
	for _, key := range envVars {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		kl := strings.ToLower(k)
		// crude detection: export KEY=, KEY=, or writing KEY= into .env
		if strings.Contains(lower, "export "+kl+"=") || strings.Contains(lower, "\n"+kl+"=") || strings.Contains(lower, " "+kl+"=") {
			continue
		}
		missing = append(missing, k)
	}
	return uniqueStrings(missing)
}

func hasLikelyBrokenSingleQuoteLine(script string) bool {
	if strings.TrimSpace(script) == "" {
		return false
	}
	lines := strings.Split(strings.ReplaceAll(script, "\r", ""), "\n")
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Count(l, "'")%2 != 0 {
			if strings.Contains(strings.ToLower(l), "echo '") || strings.Contains(strings.ToLower(l), "printf '") {
				return true
			}
		}
	}
	return false
}

func scriptRunsNodeInstall(script string) bool {
	l := strings.ToLower(script)
	return strings.Contains(l, "npm install") || strings.Contains(l, "pnpm install") || strings.Contains(l, "yarn install") || strings.Contains(l, "bun install")
}

type pmValidation struct {
	Issues   []string
	Fixes    []string
	Warnings []string
}

func validatePackageManagerUsage(script string, expectedPM string, lockFiles []string) pmValidation {
	var out pmValidation
	l := strings.ToLower(script)
	exp := strings.ToLower(strings.TrimSpace(expectedPM))
	hasLock := len(lockFiles) > 0

	usesNpm := strings.Contains(l, "npm install") || strings.Contains(l, "npm ci")
	usesPnpm := strings.Contains(l, "pnpm install")
	usesYarn := strings.Contains(l, "yarn install")
	usesBun := strings.Contains(l, "bun install")

	used := ""
	switch {
	case usesPnpm:
		used = "pnpm"
	case usesYarn:
		used = "yarn"
	case usesBun:
		used = "bun"
	case usesNpm:
		used = "npm"
	}

	if used != "" && exp != "" && used != exp {
		msg := "package manager mismatch in user-data: expected " + exp + " but found " + used
		if hasLock {
			out.Issues = append(out.Issues, "[HARD] "+msg)
			out.Fixes = append(out.Fixes, "Use "+exp+" for install/build to match lockfiles")
		} else {
			out.Warnings = append(out.Warnings, msg)
		}
	}
	return out
}
