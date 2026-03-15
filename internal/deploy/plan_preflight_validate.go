package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

type deterministicValidation struct {
	Issues   []string
	Fixes    []string
	Warnings []string
}

// ValidatePlanDeterministicFinal re-runs deterministic validation on the final
// post-review/post-autofix plan so late mutations cannot silently reintroduce
// broken DO firewall shapes, missing OpenClaw env vars, or bad user-data.
func ValidatePlanDeterministicFinal(plan *maker.Plan, p *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, runtimeEnvKeys []string) *PlanValidation {
	if plan == nil {
		return &PlanValidation{
			IsValid: false,
			Issues:  []string{"[HARD] final plan is nil"},
			Fixes:   []string{"Regenerate the plan before apply"},
		}
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return &PlanValidation{
			IsValid: false,
			Issues:  []string{"[HARD] failed to serialize final plan for deterministic validation"},
			Fixes:   []string{"Fix the generated plan shape so it can be marshaled to JSON"},
		}
	}
	det := runDeterministicPlanValidation(string(planJSON), p, deep, docker, runtimeEnvKeys)
	v := &PlanValidation{
		IsValid:  len(det.Issues) == 0,
		Issues:   det.Issues,
		Fixes:    det.Fixes,
		Warnings: det.Warnings,
	}
	return normalizeValidation(v)
}

func doFlagValueLocal(args []string, flagName string) string {
	flagName = strings.TrimSpace(flagName)
	if flagName == "" {
		return ""
	}
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		switch {
		case trimmed == flagName && i+1 < len(args):
			return strings.TrimSpace(args[i+1])
		case strings.HasPrefix(trimmed, flagName+"="):
			return strings.TrimSpace(strings.TrimPrefix(trimmed, flagName+"="))
		}
	}
	return ""
}

func runDeterministicPlanValidation(planJSON string, p *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, runtimeEnvKeys []string) deterministicValidation {
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
	// Detect provider from plan JSON for provider-gated checks.
	provider := strings.ToLower(strings.TrimSpace(plan.Provider))
	isAWS := provider == "" || provider == "aws" // default to AWS when unset

	hasLaunch := false
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		service := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		// Launch operations across all providers.
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
		// DigitalOcean
		case "compute":
			if op == "droplet" && len(args) >= 3 && strings.ToLower(strings.TrimSpace(args[2])) == "create" {
				hasLaunch = true
			}
		// GCP
		case "run":
			if op == "deploy" {
				hasLaunch = true
			}
		// Azure
		case "vm":
			if op == "create" {
				hasLaunch = true
			}
		case "containerapp":
			if op == "create" {
				hasLaunch = true
			}
		// Cloudflare (wrangler)
		case "pages", "deploy", "containers":
			hasLaunch = true
		// Hetzner
		case "server":
			if op == "create" {
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

	packChecks := ApplyRulePackDeterministicValidation(&plan, RulePackContext{
		PlanProvider: plan.Provider,
		Profile:      p,
		Deep:         deep,
		Docker:       docker,
		AppPorts:     appPorts,
	})
	out.Issues = append(out.Issues, packChecks.Issues...)
	out.Fixes = append(out.Fixes, packChecks.Fixes...)
	out.Warnings = append(out.Warnings, packChecks.Warnings...)

	if schemaIssues, schemaFixes := validateDigitalOceanCommandSchema(&plan); len(schemaIssues) > 0 {
		out.Issues = append(out.Issues, schemaIssues...)
		out.Fixes = append(out.Fixes, schemaFixes...)
	}
	if bindingIssues := ValidateCommandBindingSequence(nil, plan.Commands, runtimeEnvKeys); len(bindingIssues) > 0 {
		out.Issues = append(out.Issues, bindingIssues...)
		out.Fixes = append(out.Fixes, "Reorder commands so placeholders are consumed only after an earlier command produces them, or add the missing produces binding")
	}

	// EC2 user-data lint — only for AWS plans.
	if isAWS {
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
			// Skip user-data validation when SSM commands handle the runtime path
			// (the exec engine does onboarding + gateway start via SSM after boot).
			if isOpenClaw && !hasOpenClawSSMRuntimePath(&plan) {
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
	} // end isAWS EC2 user-data lint

	// Cross-reference: verify user-data ECR image references match plan-created repos.
	// Generic — catches any project where the LLM invents a different repo name.
	crossRefIssues := crossCheckUserDataVsPlan(&plan)
	out.Issues = append(out.Issues, crossRefIssues.Issues...)
	out.Fixes = append(out.Fixes, crossRefIssues.Fixes...)
	out.Warnings = append(out.Warnings, crossRefIssues.Warnings...)

	out.Issues = uniqueStrings(out.Issues)
	out.Fixes = uniqueStrings(out.Fixes)
	out.Warnings = uniqueStrings(out.Warnings)
	return out
}

// DeterministicValidatePlan runs only the deterministic validation checks and returns a PlanValidation.
// This is used for incremental / checkpointed plan generation to avoid repeated LLM validation calls.
func DeterministicValidatePlan(planJSON string, profile *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, runtimeEnvKeys []string) *PlanValidation {
	det := runDeterministicPlanValidation(planJSON, profile, deep, docker, runtimeEnvKeys)
	if len(det.Issues) > 0 {
		return &PlanValidation{IsValid: false, Issues: det.Issues, Fixes: det.Fixes, Warnings: det.Warnings}
	}
	return &PlanValidation{IsValid: true, Issues: nil, Fixes: nil, Warnings: det.Warnings}
}

func CheckBulkRepairInvariants(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis, runtimeEnvKeys []string) *PlanValidation {
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
	if unresolved := FilterRuntimeInjectedTokens(GetUnresolvedPlaceholders(plan), runtimeEnvKeys); len(unresolved) > 0 {
		issues = append(issues, "[HARD] bulk invariant failed: unresolved placeholders remain")
		fixes = append(fixes, "Resolve placeholder bindings so every <TOKEN> has a concrete produced value")
	}

	hasAddRoleToProfile := false
	hasGetInstanceProfileBeforeRun := false
	seenRunInstances := false
	hasSecretsManagerCreateBeforeRun := true // assume ok until disproven
	hasSMPolicyBeforeRun := true             // assume ok until disproven
	planUsesSecretsManager := false

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
		// Track SM secrets and IAM SM policy relative to EC2 launch.
		if svc == "secretsmanager" && op == "create-secret" {
			planUsesSecretsManager = true
			if seenRunInstances {
				hasSecretsManagerCreateBeforeRun = false
			}
		}
		if svc == "iam" && (op == "attach-role-policy" || op == "put-role-policy") {
			raw := strings.Join(args, " ")
			if strings.Contains(strings.ToLower(raw), "secretsmanager") {
				planUsesSecretsManager = true
				if seenRunInstances {
					hasSMPolicyBeforeRun = false
				}
			}
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
	// Secrets Manager race: secrets + IAM policy must exist before EC2 boots.
	if planUsesSecretsManager && seenRunInstances {
		if !hasSecretsManagerCreateBeforeRun {
			issues = append(issues, "[HARD] bulk invariant failed: secretsmanager create-secret appears AFTER ec2 run-instances (user-data will crash)")
			fixes = append(fixes, "Move all secretsmanager create-secret commands before ec2 run-instances so user-data can fetch them at boot")
		}
		if !hasSMPolicyBeforeRun {
			issues = append(issues, "[HARD] bulk invariant failed: IAM SecretsManager policy attached AFTER ec2 run-instances (Access Denied at boot)")
			fixes = append(fixes, "Attach SecretsManagerReadWrite policy to the IAM role before ec2 run-instances")
		}
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

func CheckOpenClawBulkInvariants(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis, runtimeEnvKeys []string) *PlanValidation {
	return CheckBulkRepairInvariants(plan, profile, deep, runtimeEnvKeys)
}

func checkOpenClawProjectInvariants(plan *maker.Plan) ([]string, []string) {
	issues := make([]string, 0, 8)
	fixes := make([]string, 0, 8)
	hasCFCreate := false
	hasCFWait := false
	hasCFDomainOutput := false
	hasCFHTTPSOutput := false
	hasALB := false
	hasRunnableOpenClawRuntimePath := false

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
		if svc == "cloudfront" && (op == "create-distribution" || op == "create-distribution-with-tags") {
			hasCFCreate = true
			if op == "create-distribution" {
				for i := 2; i < len(args); i++ {
					if strings.EqualFold(strings.TrimSpace(args[i]), "--tags") || strings.HasPrefix(strings.TrimSpace(args[i]), "--tags=") {
						issues = append(issues, "[HARD] OpenClaw invariant failed: cloudfront create-distribution does not accept --tags")
						fixes = append(fixes, "Use cloudfront create-distribution-with-tags (or remove --tags and tag separately)")
						break
					}
				}
			}
		}
		if svc == "cloudfront" && op == "wait" && len(args) >= 3 && strings.EqualFold(strings.TrimSpace(args[2]), "distribution-deployed") {
			hasCFWait = true
		}
		if svc == "ec2" && op == "run-instances" {
			script := extractEC2UserDataScript(args)
			lower := strings.ToLower(script)
			usesCompose := strings.Contains(lower, "docker compose") || strings.Contains(lower, "docker-compose")
			usesDockerRun := strings.Contains(lower, "docker run")
			hasECRRef := strings.Contains(lower, ".dkr.ecr.") || strings.Contains(lower, "<image_uri>") || strings.Contains(lower, "<ecr_uri>")
			hasNonInteractiveBootstrap := hasOpenClawNonInteractiveBootstrap(script)
			onboardIdx := strings.Index(lower, "docker-setup.sh")
			if onboardIdx < 0 {
				onboardIdx = strings.Index(lower, "openclaw-cli onboard")
			}
			startIdx := strings.Index(lower, "up -d openclaw-gateway")
			if startIdx >= 0 && !hasNonInteractiveBootstrap && (onboardIdx < 0 || onboardIdx > startIdx) {
				issues = append(issues, "[HARD] OpenClaw invariant failed: bootstrap initialization must run before starting openclaw-gateway")
				fixes = append(fixes, "Seed a minimal openclaw.json before docker compose up -d openclaw-gateway, or run onboarding first in an interactive environment")
			}
			if usesCompose {
				missing := missingEnvVarsInScript(script, OpenClawComposeHardEnvVars())
				if len(missing) == 0 && startIdx >= 0 && ((onboardIdx >= 0 && onboardIdx < startIdx) || hasNonInteractiveBootstrap) {
					hasRunnableOpenClawRuntimePath = true
				}
			}
			if usesDockerRun && hasECRRef {
				if onboardIdx >= 0 || hasNonInteractiveBootstrap {
					hasRunnableOpenClawRuntimePath = true
				} else {
					issues = append(issues, "[HARD] OpenClaw invariant failed: bootstrap initialization must run before starting openclaw-gateway")
					fixes = append(fixes, "Seed a minimal openclaw.json before docker run, or run onboarding first in an interactive environment")
				}
			}
		}
		for k, v := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			if ku == "CLOUDFRONT_DOMAIN" {
				hasCFDomainOutput = true
			}
			if ku == "HTTPS_URL" {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "https://") {
					hasCFHTTPSOutput = true
				}
			}
		}
	}

	if hasALB && !(hasCFCreate && hasCFWait && hasCFDomainOutput && hasCFHTTPSOutput) {
		issues = append(issues, "[HARD] OpenClaw invariant failed: HTTPS pairing URL must be shipped via CloudFront (create + wait + output)")
		fixes = append(fixes, "Add CloudFront create-distribution(+optional tags variant), cloudfront wait distribution-deployed, produces CLOUDFRONT_DOMAIN, and set HTTPS_URL to full https:// URL")
	}
	// Also accept SSM-based runtime path (exec engine pattern).
	if !hasRunnableOpenClawRuntimePath {
		hasRunnableOpenClawRuntimePath = hasOpenClawSSMRuntimePath(plan)
	}
	if !hasRunnableOpenClawRuntimePath {
		issues = append(issues, "[HARD] OpenClaw invariant failed: missing runnable runtime path (compose onboarding+mount env or docker-run with ECR image)")
		fixes = append(fixes, "Use compose onboarding flow with OPENCLAW_CONFIG_DIR/OPENCLAW_WORKSPACE_DIR or docker pull/run using explicit ECR image")
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
	runInstancesIndex := -1
	instanceWaitIndex := -1
	createLBIndex := -1
	waitLBAvailableIndex := -1
	createListenerIndex := -1
	registerTargetsIndex := -1
	hasSSHAdminCIDRPlaceholder := false
	for idx := range plan.Commands {
		cmd := plan.Commands[idx]
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
			if runInstancesIndex < 0 {
				runInstancesIndex = idx
			}
		}
		if service == "ec2" && op == "wait" && len(args) >= 3 && strings.EqualFold(strings.TrimSpace(args[2]), "instance-running") {
			if instanceWaitIndex < 0 {
				instanceWaitIndex = idx
			}
		}
		if service == "ec2" && op == "authorize-security-group-ingress" {
			if port := parseFlagInt(args, "--port"); port > 0 {
				sgPorts[port] = true
				if port == 22 {
					cidr := strings.TrimSpace(parseFlag(args, "--cidr"))
					if strings.EqualFold(cidr, "<ADMIN_CIDR>") || strings.HasPrefix(cidr, "<") {
						hasSSHAdminCIDRPlaceholder = true
					}
				}
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
		if service == "elbv2" && op == "register-targets" && registerTargetsIndex < 0 {
			registerTargetsIndex = idx
		}
		if service == "elbv2" && op == "create-load-balancer" && createLBIndex < 0 {
			createLBIndex = idx
		}
		if service == "elbv2" && op == "wait" && len(args) >= 3 && strings.EqualFold(strings.TrimSpace(args[2]), "load-balancer-available") {
			if waitLBAvailableIndex < 0 {
				waitLBAvailableIndex = idx
			}
		}
		if service == "elbv2" && op == "create-listener" && createListenerIndex < 0 {
			createListenerIndex = idx
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
		out.Warnings = append(out.Warnings, "suggestion: add iam get-instance-profile before ec2 run-instances to reduce IAM propagation race risk")
	}
	if runInstancesIndex >= 0 && registerTargetsIndex >= 0 {
		if instanceWaitIndex < 0 || instanceWaitIndex <= runInstancesIndex || instanceWaitIndex > registerTargetsIndex {
			out.Warnings = append(out.Warnings, "suggestion: add ec2 wait instance-running between run-instances and register-targets")
		}
	}
	if createLBIndex >= 0 && createListenerIndex >= 0 {
		if waitLBAvailableIndex < 0 || waitLBAvailableIndex <= createLBIndex || waitLBAvailableIndex > createListenerIndex {
			out.Warnings = append(out.Warnings, "suggestion: add elbv2 wait load-balancer-available between create-load-balancer and create-listener")
		}
	}
	if hasSSHAdminCIDRPlaceholder {
		out.Warnings = append(out.Warnings, "suggestion: replace <ADMIN_CIDR> with an explicit trusted CIDR or remove SSH ingress and use SSM-only access")
	}

	return out
}

func validateOpenClawPlanCommands(plan *maker.Plan) awsPlanChecks {
	var out awsPlanChecks
	if plan == nil || len(plan.Commands) == 0 {
		return out
	}

	hasEC2RunInstances := false
	hasCloudFrontCreate := false
	hasCloudFrontWait := false
	hasCloudFrontDomainOutput := false
	hasCloudFrontHTTPSOutput := false
	hasRunnableOpenClawRuntimePath := false
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		service := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if service == "ec2" && op == "run-instances" {
			hasEC2RunInstances = true
			script := strings.ToLower(extractEC2UserDataScript(args))
			usesCompose := strings.Contains(script, "docker compose") || strings.Contains(script, "docker-compose")
			usesDockerRun := strings.Contains(script, "docker run")
			hasECRRef := strings.Contains(script, ".dkr.ecr.") || strings.Contains(script, "<image_uri>") || strings.Contains(script, "<ecr_uri>")
			if usesCompose {
				missing := missingEnvVarsInScript(script, OpenClawComposeHardEnvVars())
				onboarded := strings.Contains(script, "docker-setup.sh") || strings.Contains(script, "openclaw-cli onboard") || hasOpenClawNonInteractiveBootstrap(script)
				started := strings.Contains(script, "up -d openclaw-gateway")
				if len(missing) == 0 && onboarded && started {
					hasRunnableOpenClawRuntimePath = true
				}
			}
			if usesDockerRun && hasECRRef {
				onboarded := strings.Contains(script, "docker-setup.sh") || strings.Contains(script, "openclaw-cli onboard") || hasOpenClawNonInteractiveBootstrap(script)
				if onboarded {
					hasRunnableOpenClawRuntimePath = true
				}
			}
		}
		if service == "cloudfront" && (op == "create-distribution" || op == "create-distribution-with-tags") {
			hasCloudFrontCreate = true
			if op == "create-distribution" {
				for i := 2; i < len(args); i++ {
					if strings.EqualFold(strings.TrimSpace(args[i]), "--tags") || strings.HasPrefix(strings.TrimSpace(args[i]), "--tags=") {
						out.Warnings = append(out.Warnings, "suggestion: cloudfront create-distribution does not accept --tags; use create-distribution-with-tags or tag separately")
						break
					}
				}
			}
		}
		if service == "cloudfront" && op == "wait" {
			if len(args) >= 3 && strings.EqualFold(strings.TrimSpace(args[2]), "distribution-deployed") {
				hasCloudFrontWait = true
			}
		}
		for k, v := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			if ku == "CLOUDFRONT_DOMAIN" {
				hasCloudFrontDomainOutput = true
			}
			if ku == "HTTPS_URL" {
				vv := strings.TrimSpace(v)
				if strings.HasPrefix(strings.ToLower(vv), "https://") {
					hasCloudFrontHTTPSOutput = true
				}
			}
		}
	}

	// Also check SSM send-command scripts — the exec engine uses SSM for
	// onboarding + gateway start after user-data finishes.
	if !hasRunnableOpenClawRuntimePath {
		hasRunnableOpenClawRuntimePath = hasOpenClawSSMRuntimePath(plan)
	}

	if !hasEC2RunInstances {
		out.Issues = append(out.Issues, "[HARD] OpenClaw AWS plan must include ec2 run-instances")
		out.Fixes = append(out.Fixes, "Add ec2 run-instances for OpenClaw workload launch")
	}
	if !(hasCloudFrontCreate && hasCloudFrontWait && hasCloudFrontDomainOutput && hasCloudFrontHTTPSOutput) {
		out.Issues = append(out.Issues, "[HARD] OpenClaw AWS plan is missing required CloudFront HTTPS pairing architecture")
		out.Fixes = append(out.Fixes, "Add CloudFront create-distribution, cloudfront wait distribution-deployed, produce CLOUDFRONT_DOMAIN, and set HTTPS_URL to a full https:// URL")
	}
	if !hasRunnableOpenClawRuntimePath {
		out.Issues = append(out.Issues, "[HARD] OpenClaw AWS plan is missing a valid runtime start path (compose onboarding+mount env or docker-run with ECR image)")
		out.Fixes = append(out.Fixes, "Ensure ec2 user-data starts OpenClaw via compose onboarding flow with OPENCLAW_CONFIG_DIR/OPENCLAW_WORKSPACE_DIR or via docker pull/run using explicit ECR image")
	}

	return out
}

// validateDigitalOceanPlanCommands checks DO-specific plan quality.
func validateDigitalOceanPlanCommands(plan *maker.Plan, appPorts []int, isOpenClaw bool) awsPlanChecks {
	var out awsPlanChecks
	if plan == nil || len(plan.Commands) == 0 {
		return out
	}

	hasDropletCreate := false
	hasFirewallCreate := false
	hasFirewallAttach := false
	hasSSHKeyImport := false
	hasSSHKeyList := false
	hasReservedIP := false
	producesDropletID := false
	hasAppsCreate := false
	hasHTTPSOutput := false
	hasDORegistryCreate := false
	hasDockerBuild := false
	hasDockerPush := false
	proxyBuildImageRef := ""
	proxyPushImageRef := ""
	proxyAppImageRef := ""
	proxyAppRepository := ""
	requiredInboundPorts := map[string]bool{"22": true}
	for _, p := range appPorts {
		if p > 0 {
			requiredInboundPorts[fmt.Sprintf("%d", p)] = true
		}
	}
	if isOpenClaw {
		requiredInboundPorts["18789"] = true
	}

	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 2 {
			continue
		}
		s0 := strings.ToLower(strings.TrimSpace(args[0]))
		s1 := strings.ToLower(strings.TrimSpace(args[1]))
		s2 := ""
		if len(args) >= 3 {
			s2 = strings.ToLower(strings.TrimSpace(args[2]))
		}

		if s0 == "compute" && s1 == "droplet" && s2 == "create" {
			hasDropletCreate = true
			for k := range cmd.Produces {
				ku := strings.ToUpper(strings.TrimSpace(k))
				if strings.Contains(ku, "DROPLET_ID") {
					producesDropletID = true
				}
			}
		}
		if s0 == "apps" && s1 == "create" {
			hasAppsCreate = true
			if isOpenClaw {
				if appImageRef, repository := extractOpenClawDOAppProxyImageRef(args); appImageRef != "" {
					proxyAppImageRef = appImageRef
					proxyAppRepository = repository
				}
			}
			for k, v := range cmd.Produces {
				ku := strings.ToUpper(strings.TrimSpace(k))
				if ku == "HTTPS_URL" || ku == "APP_URL" {
					vv := strings.TrimSpace(v)
					if vv != "" {
						hasHTTPSOutput = true
					}
				}
			}
		}
		if s0 == "registry" && s1 == "create" {
			hasDORegistryCreate = true
		}
		if s0 == "docker" && s1 == "build" {
			hasDockerBuild = true
			if isOpenClaw {
				if imageRef := extractDockerBuildTag(args); imageRef != "" {
					proxyBuildImageRef = imageRef
				}
			}
		}
		if s0 == "docker" && s1 == "push" {
			hasDockerPush = true
			if isOpenClaw {
				if imageRef := extractDockerPushTarget(args); imageRef != "" {
					proxyPushImageRef = imageRef
				}
			}
		}
		if s0 == "compute" && s1 == "firewall" && s2 == "create" {
			hasFirewallCreate = true
			inboundCount := countDOFlagOccurrences(args, "--inbound-rules")
			outboundCount := countDOFlagOccurrences(args, "--outbound-rules")
			firewallSpec := extractDOFirewallSpec(args)
			if strings.TrimSpace(doFlagValueLocal(args, "--name")) == "" {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean firewall create is missing --name <FIREWALL_NAME>")
				out.Fixes = append(out.Fixes, "Rewrite compute firewall create to use --name <FIREWALL_NAME> instead of a positional firewall name")
			}
			if isOpenClaw && countDOFlagOccurrences(args, "--tag-names") > 0 {
				out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean firewall create uses --tag-names before the droplet exists")
				out.Fixes = append(out.Fixes, "Remove --tag-names from compute firewall create and attach the firewall later with compute firewall add-droplets <FIREWALL_ID> --droplet-ids <DROPLET_ID>")
			}
			if inboundCount > 1 {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean firewall create uses repeated --inbound-rules flags; doctl keeps only the last one")
				out.Fixes = append(out.Fixes, "Use exactly ONE --inbound-rules arg containing a quoted string of space-separated rules")
			}
			if outboundCount > 1 {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean firewall create uses repeated --outbound-rules flags; doctl keeps only the last one")
				out.Fixes = append(out.Fixes, "Use exactly ONE --outbound-rules arg containing a quoted string of space-separated rules")
			}

			for port := range requiredInboundPorts {
				if !doFirewallSpecHasInboundPort(firewallSpec, "tcp", port) {
					out.Issues = append(out.Issues, fmt.Sprintf("[HARD] DigitalOcean firewall create is missing inbound TCP port %s", port))
					out.Fixes = append(out.Fixes, fmt.Sprintf("Add inbound rule protocol:tcp,ports:%s,address:0.0.0.0/0 to compute firewall create", port))
				}
			}
			if isOpenClaw {
				for _, forbidden := range []string{"80", "443", "8080"} {
					if doFirewallSpecHasInboundPort(firewallSpec, "tcp", forbidden) {
						out.Issues = append(out.Issues, fmt.Sprintf("[HARD] DigitalOcean firewall create exposes TCP port %s for OpenClaw without an explicit reverse proxy requirement", forbidden))
						out.Fixes = append(out.Fixes, fmt.Sprintf("Remove inbound TCP port %s from compute firewall create; the OpenClaw droplet should expose only 22 and 18789 publicly", forbidden))
					}
				}
			}

			if !doFirewallSpecHasOutboundAll(firewallSpec, "tcp") {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean firewall create is missing outbound TCP all rule")
				out.Fixes = append(out.Fixes, "Add outbound rule protocol:tcp,ports:all,address:0.0.0.0/0 to compute firewall create")
			}
			if !doFirewallSpecHasOutboundAll(firewallSpec, "udp") {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean firewall create is missing outbound UDP all rule")
				out.Fixes = append(out.Fixes, "Add outbound rule protocol:udp,ports:all,address:0.0.0.0/0 to compute firewall create")
			}
		}
		if s0 == "compute" && s1 == "firewall" && s2 == "add-droplets" {
			hasFirewallAttach = true
		}
		if s0 == "compute" && s1 == "ssh-key" && s2 == "import" {
			hasSSHKeyImport = true
		}
		if s0 == "compute" && s1 == "ssh-key" && s2 == "list" {
			hasSSHKeyList = true
			if isOpenClaw {
				out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean plan reuses an existing SSH key via compute ssh-key list")
				out.Fixes = append(out.Fixes, "Replace compute ssh-key list with compute ssh-key import so the executor can create a fresh deploy-scoped SSH key and bind SSH_KEY_ID into droplet create")
			} else {
				out.Warnings = append(out.Warnings, "DigitalOcean plan reuses an existing SSH key via compute ssh-key list — prefer compute ssh-key import for a dedicated deployment key")
			}
		}
		if s0 == "compute" && s1 == "reserved-ip" && s2 == "create" {
			hasReservedIP = true
		}
		if s0 == "compute" && s1 == "droplet" && s2 == "create" {
			if countDOFlagOccurrences(args, "--tag") > 0 {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean droplet create uses invalid --tag flag")
				out.Fixes = append(out.Fixes, "Replace --tag with --tag-name on compute droplet create")
			}
		}
	}

	if !hasDropletCreate {
		out.Issues = append(out.Issues, "[HARD] DigitalOcean plan missing compute droplet create")
		out.Fixes = append(out.Fixes, "Add compute droplet create with --image docker-20-04 --user-data boot script")
	}
	if isOpenClaw && !hasAppsCreate {
		out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean plan missing App Platform HTTPS front door (apps create)")
		out.Fixes = append(out.Fixes, "Add apps create for a small HTTPS proxy app that forwards to the OpenClaw droplet and exposes a managed ondigitalocean.app URL")
	}
	if isOpenClaw && !hasHTTPSOutput {
		out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean plan is missing HTTPS output binding from App Platform")
		out.Fixes = append(out.Fixes, "Produce HTTPS_URL (or APP_URL) from apps create using the App Platform default ingress URL")
	}
	if isOpenClaw && !hasDockerBuild {
		out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean plan missing docker build for the App Platform HTTPS proxy image")
		out.Fixes = append(out.Fixes, "Add docker build for the proxy image; use context __CLANKER_OPENCLAW_DO_PROXY__ when building the managed HTTPS front-door proxy")
	}
	if isOpenClaw && !hasDockerPush {
		out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean plan missing docker push for the App Platform HTTPS proxy image")
		out.Fixes = append(out.Fixes, "Push the proxy image to DOCR before apps create so App Platform can deploy it")
	}
	if isOpenClaw && !hasDORegistryCreate {
		out.Warnings = append(out.Warnings, "OpenClaw DigitalOcean plan does not create a DOCR registry — this is fine only if REGISTRY_NAME already resolves to an existing registry")
	}
	if isOpenClaw {
		if strings.HasPrefix(proxyAppRepository, "<REGISTRY_NAME>/") {
			out.Issues = append(out.Issues, fmt.Sprintf("[HARD] OpenClaw DigitalOcean App Platform proxy repository must not include the registry name prefix: %s", proxyAppRepository))
			out.Fixes = append(out.Fixes, "Set apps create --spec image.repository to the repository name only, for example openclaw-proxy, and keep image.registry as <REGISTRY_NAME>")
		}
		if proxyBuildImageRef != "" && proxyPushImageRef != "" && proxyBuildImageRef != proxyPushImageRef {
			out.Issues = append(out.Issues, fmt.Sprintf("[HARD] OpenClaw DigitalOcean proxy image mismatch: docker build tags %q but docker push uploads %q", proxyBuildImageRef, proxyPushImageRef))
			out.Fixes = append(out.Fixes, "Use the exact same DOCR image ref for both docker build -t and docker push")
		}
		if proxyAppImageRef != "" && proxyBuildImageRef != "" && proxyAppImageRef != proxyBuildImageRef {
			out.Issues = append(out.Issues, fmt.Sprintf("[HARD] OpenClaw DigitalOcean proxy image mismatch: App Platform expects %q but docker build tags %q", proxyAppImageRef, proxyBuildImageRef))
			out.Fixes = append(out.Fixes, "Make apps create --spec image.registry/image.repository/image.tag resolve to the same DOCR image ref used by docker build")
		}
		if proxyAppImageRef != "" && proxyPushImageRef != "" && proxyAppImageRef != proxyPushImageRef {
			out.Issues = append(out.Issues, fmt.Sprintf("[HARD] OpenClaw DigitalOcean proxy image mismatch: App Platform expects %q but docker push uploads %q", proxyAppImageRef, proxyPushImageRef))
			out.Fixes = append(out.Fixes, "Make docker push upload the same DOCR image ref referenced by apps create --spec")
		}
	}

	if !hasFirewallCreate {
		portStr := "22"
		for _, p := range appPorts {
			portStr += fmt.Sprintf(", %d", p)
		}
		out.Issues = append(out.Issues, fmt.Sprintf("[HARD] DigitalOcean plan missing firewall — ports %s will be unreachable", portStr))
		out.Fixes = append(out.Fixes, "Add compute firewall create allowing inbound TCP on required ports, then compute firewall add-droplets to attach")
	}

	if hasFirewallCreate && !hasFirewallAttach {
		out.Warnings = append(out.Warnings, "Firewall created but not attached to droplet — add compute firewall add-droplets <FIREWALL_ID> --droplet-ids <DROPLET_ID>")
	}

	if isOpenClaw && !hasSSHKeyImport {
		out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean plan missing compute ssh-key import for deploy-scoped SSH access")
		out.Fixes = append(out.Fixes, "Add compute ssh-key import before droplet create so the executor can generate a fresh SSH key pair and bind SSH_KEY_ID into compute droplet create")
	} else if !hasSSHKeyImport && !hasSSHKeyList {
		out.Warnings = append(out.Warnings, "No ssh-key import/list step — <SSH_KEY_ID> placeholder will be unresolved unless hardcoded")
	}

	if !hasReservedIP && !isOpenClaw {
		out.Warnings = append(out.Warnings, "No reserved IP — Droplet public IP may change on reboot; consider compute reserved-ip create")
	} else if hasReservedIP && isOpenClaw {
		out.Warnings = append(out.Warnings, "Reserved IP is optional for OpenClaw on DigitalOcean — omit compute reserved-ip create unless quota is available and a pinned IP is required")
	}

	if hasDropletCreate && !producesDropletID {
		out.Warnings = append(out.Warnings, "compute droplet create does not produce DROPLET_ID — downstream steps (firewall attach, reserved IP) need it")
	}

	// Catch invalid doctl subcommands (LLM hallucinations)
	for i, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) == 0 {
			continue
		}
		s0 := strings.ToLower(strings.TrimSpace(args[0]))
		if s0 == "__docker_build__" || s0 == "__docker_push__" || s0 == "__local_docker_build__" || s0 == "__local_docker_push__" {
			out.Issues = append(out.Issues, fmt.Sprintf("[HARD] Step %d uses invalid fake docker command prefix '%s'", i+1, args[0]))
			out.Fixes = append(out.Fixes, fmt.Sprintf("Change step %d to plain docker CLI args starting with 'docker build' or 'docker push'", i+1))
			continue
		}
		if len(args) < 3 {
			continue
		}
		s1 := strings.ToLower(strings.TrimSpace(args[1]))
		if s0 == "registry" && (s1 == "docker" || strings.HasPrefix(s1, "docker-")) {
			out.Issues = append(out.Issues, fmt.Sprintf("[HARD] Step %d uses invalid doctl subcommand 'registry %s' — use plain 'docker build'/'docker push' instead", i+1, strings.Join(args[1:], " ")))
			out.Fixes = append(out.Fixes, fmt.Sprintf("Change step %d to use docker CLI: args=['docker','build','-t','<tag>','.'] or args=['docker','push','<tag>']", i+1))
		}
	}

	// Check for broken DOCR auth in user-data (doctl not pre-installed on Droplets)
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 3 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(args[0])) != "compute" ||
			strings.ToLower(strings.TrimSpace(args[1])) != "droplet" ||
			strings.ToLower(strings.TrimSpace(args[2])) != "create" {
			continue
		}
		script := extractDoctlUserDataScript(args)
		if script == "" {
			continue
		}
		if strings.Contains(script, "/root/.config/doctl/config.yaml") || strings.Contains(script, "cat /root/.config/doctl") {
			out.Issues = append(out.Issues, "[HARD] user-data reads /root/.config/doctl/config.yaml — doctl is NOT pre-installed on Droplets")
			out.Fixes = append(out.Fixes, "Install doctl in user-data, then 'doctl auth init -t $TOKEN && doctl registry login'")
		}
	}

	// Validate user-data in droplet create
	for _, cmd := range plan.Commands {
		args := cmd.Args
		if len(args) < 3 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(args[0])) != "compute" ||
			strings.ToLower(strings.TrimSpace(args[1])) != "droplet" ||
			strings.ToLower(strings.TrimSpace(args[2])) != "create" {
			continue
		}
		script := extractDoctlUserDataScript(args)
		if strings.TrimSpace(script) == "" {
			out.Warnings = append(out.Warnings, "compute droplet create has no --user-data; workload likely will not start")
			continue
		}
		lower := strings.ToLower(script)
		runtimeSpec, hasRuntime := inferOpenClawDORuntimeSpec(script)

		// Unterminated single-quote check (shared with AWS)
		if hasLikelyBrokenSingleQuoteLine(script) {
			out.Issues = append(out.Issues, "[HARD] user-data script contains an unterminated single-quoted string")
			out.Fixes = append(out.Fixes, "Fix quoting in user-data (close trailing single quotes)")
		}

		// Docker daemon check — docker-20-04 image has docker pre-started
		if strings.Contains(lower, "docker") {
			// Check --image flag in command args for docker-ready image
			usesDockerImage := false
			for _, a := range args {
				al := strings.ToLower(strings.TrimSpace(a))
				if strings.Contains(al, "docker-") {
					usesDockerImage = true
					break
				}
			}
			if !usesDockerImage &&
				!strings.Contains(lower, "systemctl start docker") &&
				!strings.Contains(lower, "service docker start") {
				out.Warnings = append(out.Warnings, "user-data uses docker but does not explicitly start the docker daemon")
			}
		}

		// Check OpenClaw-specific user-data requirements
		if isOpenClaw || hasRuntime {
			if strings.Contains(lower, "cloud-init status --wait") {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean droplet user-data runs 'cloud-init status --wait' inside cloud-init and will deadlock")
				out.Fixes = append(out.Fixes, "Remove 'cloud-init status --wait' from user-data; it already runs inside cloud-init")
			}
			if strings.Contains(lower, "docker compose ") && scriptInstallsAPTRequestedPackage(script, "docker-compose") && !scriptInstallsAPTRequestedPackage(script, "docker-compose-plugin") {
				out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean user-data runs 'docker compose' but installs only the standalone docker-compose package")
				out.Fixes = append(out.Fixes, "Install docker-compose-plugin when using 'docker compose', or switch the script to the standalone 'docker-compose' binary consistently")
			}
			if strings.Contains(lower, "git clone ") && !scriptInstallsAPTRequestedPackage(script, "git") {
				out.Warnings = append(out.Warnings, "OpenClaw DigitalOcean user-data runs git clone without explicitly installing git")
			}
			if runtimeSpec.HasComposeBuild {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data uses 'docker compose build' in the DigitalOcean cloud-init flow")
				out.Fixes = append(out.Fixes, "Set OPENCLAW_IMAGE='"+openClawDOImageRef+"', run '"+openClawDOImagePullCommand+"', then '"+openClawDOGatewayComposeCmd+"'")
			}
			if runtimeSpec.HasDockerBuild {
				out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean user-data still uses a local source-build flow instead of the upstream GHCR image")
				out.Fixes = append(out.Fixes, "Remove local 'docker build -t openclaw:local ...' steps, set OPENCLAW_IMAGE='"+openClawDOImageRef+"' in .env, and pull the image before docker compose up")
			}
			if !strings.Contains(lower, "openclaw_image="+strings.ToLower(openClawDOImageRef)) {
				out.Issues = append(out.Issues, "[HARD] OpenClaw DigitalOcean user-data does not pin OPENCLAW_IMAGE to the upstream GHCR image")
				out.Fixes = append(out.Fixes, "Write OPENCLAW_IMAGE='"+openClawDOImageRef+"' into the .env heredoc before starting the gateway")
			}
			if hasRuntime && !runtimeSpec.HasComposeUp && !runtimeSpec.HasDockerRun {
				out.Issues = append(out.Issues, "[HARD] DigitalOcean droplet user-data does not start OpenClaw (missing docker compose up or docker run)")
				out.Fixes = append(out.Fixes, "Add 'docker compose up -d openclaw-gateway' to user-data script")
			}
			if hasRuntime && !runtimeSpec.HasGatewaySecret {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data .env is missing OPENCLAW_GATEWAY_TOKEN or OPENCLAW_GATEWAY_PASSWORD")
				out.Fixes = append(out.Fixes, "Write OPENCLAW_GATEWAY_TOKEN=<OPENCLAW_GATEWAY_TOKEN> or OPENCLAW_GATEWAY_PASSWORD=<...> into the .env heredoc before docker compose up")
			}
			if runtimeSpec.LeaksDOAccessToken {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data writes DIGITALOCEAN_ACCESS_TOKEN into .env")
				out.Fixes = append(out.Fixes, "Remove DIGITALOCEAN_ACCESS_TOKEN from the OpenClaw .env heredoc; it is not an application secret")
			}
			if runtimeSpec.HasCloneSoftFail {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data ignores git clone failure with a shell fallback ('|| ...')")
				out.Fixes = append(out.Fixes, "Remove the shell fallback from git clone so user-data fails fast when repository checkout fails")
			}
			if runtimeSpec.HasDockerSetupSoftFail {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data ignores docker-setup.sh failure with a shell fallback ('|| ...')")
				out.Fixes = append(out.Fixes, "Remove the shell fallback from docker-setup.sh so onboarding failure stops the deployment")
			}
			if runtimeSpec.HasLeakedDoctlFlags {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data includes outer doctl flags like --wait/--output on the docker compose up line")
				out.Fixes = append(out.Fixes, "Keep doctl flags outside user-data; the user-data runtime command must be just 'docker compose up -d openclaw-gateway'")
			}
			if runtimeSpec.HasDummySecrets {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data contains dummy secret values like placeholder_replace_me/changeme")
				out.Fixes = append(out.Fixes, "Preserve provided placeholders such as <OPENCLAW_GATEWAY_TOKEN> and <ANTHROPIC_API_KEY>; never replace them with dummy literals")
			}
			if runtimeSpec.HasGeneratedGateway {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data generates a random gateway token instead of using the user-provided gateway secret")
				out.Fixes = append(out.Fixes, "Write OPENCLAW_GATEWAY_TOKEN=<OPENCLAW_GATEWAY_TOKEN> directly into the .env heredoc; do not generate a random token in user-data")
			}
			if hasRuntime && !runtimeSpec.HasProviderKey {
				out.Issues = append(out.Issues, "[HARD] OpenClaw user-data .env is missing all AI provider keys (ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY)")
				out.Fixes = append(out.Fixes, "Write at least one provider key placeholder into the .env heredoc before docker compose up")
			}
			if hasRuntime && !runtimeSpec.HasBindSetting {
				out.Warnings = append(out.Warnings, "user-data .env missing OPENCLAW_GATEWAY_BIND=lan — gateway may not accept external connections")
			}
			for _, marker := range []string{"discord_bot_token", "telegram_bot_token"} {
				if strings.Contains(lower, marker) && !strings.Contains(lower, marker+"=") {
					out.Warnings = append(out.Warnings, fmt.Sprintf("user-data references %s but does not appear to write it into .env", strings.ToUpper(marker)))
				}
			}
			if size := strings.TrimSpace(parseFlag(args, "--size")); size == "s-1vcpu-2gb" {
				out.Warnings = append(out.Warnings, "OpenClaw build-on-droplet on s-1vcpu-2gb may OOM during docker build; prefer s-2vcpu-4gb")
			}
		}
	}

	return out
}

func scriptInstallsAPTRequestedPackage(script string, pkg string) bool {
	pkg = strings.TrimSpace(strings.ToLower(pkg))
	if pkg == "" {
		return false
	}
	for _, line := range strings.Split(strings.ToLower(script), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "apt-get install") {
			continue
		}
		fields := strings.Fields(trimmed)
		for _, field := range fields {
			if strings.TrimSpace(field) == pkg {
				return true
			}
		}
	}
	return false
}

func extractDockerBuildTag(args []string) string {
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == "-t" || trimmed == "--tag" {
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
			continue
		}
		if strings.HasPrefix(trimmed, "--tag=") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "--tag="))
		}
	}
	return ""
}

func extractDockerPushTarget(args []string) string {
	if len(args) < 3 {
		return ""
	}
	return strings.TrimSpace(args[2])
}

func extractOpenClawDOAppProxyImageRef(args []string) (string, string) {
	specRaw, _ := commandFlagValueLocal(args, "--spec")
	if strings.TrimSpace(specRaw) == "" {
		return "", ""
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(specRaw), &spec); err != nil {
		return "", ""
	}
	services, ok := spec["services"].([]any)
	if !ok || len(services) == 0 {
		return "", ""
	}
	service, ok := services[0].(map[string]any)
	if !ok {
		return "", ""
	}
	image, ok := service["image"].(map[string]any)
	if !ok {
		return "", ""
	}
	registry := strings.TrimSpace(stringMapValue(image, "registry"))
	repository := strings.TrimSpace(stringMapValue(image, "repository"))
	tag := strings.TrimSpace(stringMapValue(image, "tag"))
	if registry == "" || repository == "" {
		return "", repository
	}
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("registry.digitalocean.com/%s/%s:%s", registry, repository, tag), repository
}

// hasOpenClawSSMRuntimePath checks if SSM send-command steps collectively
// provide onboarding + gateway start (the exec engine pattern).
func hasOpenClawSSMRuntimePath(plan *maker.Plan) bool {
	scripts := extractSSMShellScripts(plan)
	if len(scripts) == 0 {
		return false
	}
	merged := strings.ToLower(strings.Join(scripts, "\n"))
	onboarded := strings.Contains(merged, "docker-setup.sh") ||
		strings.Contains(merged, "openclaw-cli onboard") ||
		strings.Contains(merged, "openclaw-cli\" onboard")
	started := strings.Contains(merged, "up -d openclaw-gateway") ||
		strings.Contains(merged, "docker run") ||
		strings.Contains(merged, "docker compose up")
	return onboarded && started
}

// extractSSMShellScripts returns the shell command strings from all
// ssm send-command --parameters {"commands":[...]} in the plan.
func extractSSMShellScripts(plan *maker.Plan) []string {
	if plan == nil {
		return nil
	}
	var out []string
	for _, cmd := range plan.Commands {
		if len(cmd.Args) < 4 {
			continue
		}
		svc := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		op := strings.ToLower(strings.TrimSpace(cmd.Args[1]))
		if svc != "ssm" || op != "send-command" {
			continue
		}
		params := parseFlag(cmd.Args, "--parameters")
		if params == "" {
			continue
		}
		// Parse {"commands":["...","..."]}
		var parsed struct {
			Commands []string `json:"commands"`
		}
		if json.Unmarshal([]byte(params), &parsed) != nil || len(parsed.Commands) == 0 {
			continue
		}
		out = append(out, strings.Join(parsed.Commands, "\n"))
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

// crossCheckUserDataVsPlan verifies that user-data scripts reference the same
// resource names that the plan actually creates. Generic — works for any project.
// Catches: ECR repo name mismatches, security group name typos, etc.
func crossCheckUserDataVsPlan(plan *maker.Plan) deterministicValidation {
	var out deterministicValidation
	if plan == nil || len(plan.Commands) == 0 {
		return out
	}

	// Collect ECR repo names from ecr create-repository commands
	ecrRepoNames := map[string]bool{}
	for _, cmd := range plan.Commands {
		if len(cmd.Args) < 3 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(cmd.Args[0])) != "ecr" ||
			strings.ToLower(strings.TrimSpace(cmd.Args[1])) != "create-repository" {
			continue
		}
		for i := 2; i < len(cmd.Args)-1; i++ {
			if strings.TrimSpace(cmd.Args[i]) == "--repository-name" {
				ecrRepoNames[strings.TrimSpace(cmd.Args[i+1])] = true
			}
		}
	}

	if len(ecrRepoNames) == 0 {
		return out // no ECR repos in plan, nothing to check
	}

	// Check user-data scripts for ECR image references that don't match
	ecrImageRe := regexp.MustCompile(`(?:dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com)/([a-z0-9][-a-z0-9_.]*):`)

	for ci, cmd := range plan.Commands {
		if len(cmd.Args) < 2 {
			continue
		}
		if strings.ToLower(cmd.Args[0]) != "ec2" || strings.ToLower(cmd.Args[1]) != "run-instances" {
			continue
		}
		script := extractEC2UserDataScript(cmd.Args)
		if script == "" {
			continue
		}

		// Find explicit ECR image refs in the script
		matches := ecrImageRe.FindAllStringSubmatch(script, -1)
		for _, m := range matches {
			repoRef := strings.TrimSpace(m[1])
			if repoRef == "" || strings.HasPrefix(repoRef, "$") || strings.Contains(repoRef, "${") {
				continue
			}
			if !ecrRepoNames[repoRef] {
				out.Issues = append(out.Issues, fmt.Sprintf(
					"[HARD] user-data in command %d references ECR repo '%s' but plan only creates: %s",
					ci+1, repoRef, joinMapKeys(ecrRepoNames)))
				out.Fixes = append(out.Fixes, fmt.Sprintf(
					"Update user-data to pull from the correct ECR repository name (%s) or derive it dynamically from ECR_REPOSITORY_URI",
					joinMapKeys(ecrRepoNames)))
			}
		}

		// Flag hardcoded ECR image URIs that use wrong repo name
		hardcodedEcrRe := regexp.MustCompile(`[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/([a-z0-9][-a-z0-9_.]*):`)
		hardcoded := hardcodedEcrRe.FindAllStringSubmatch(script, -1)
		for _, m := range hardcoded {
			repoRef := strings.TrimSpace(m[1])
			if repoRef != "" && !ecrRepoNames[repoRef] {
				out.Issues = append(out.Issues, fmt.Sprintf(
					"[HARD] user-data hardcodes ECR image with repo '%s' but plan creates '%s'",
					repoRef, joinMapKeys(ecrRepoNames)))
				out.Fixes = append(out.Fixes,
					"Derive ECR registry URL dynamically and use the correct repo name from the plan")
			}
		}
	}

	return out
}

// joinMapKeys concatenates map keys as comma-separated string.
func joinMapKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
