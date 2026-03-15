package deploy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

var openClawDOAllowedCommandFamilies = map[string]struct{}{
	"compute ssh-key import":        {},
	"compute firewall create":       {},
	"compute droplet create":        {},
	"compute firewall add-droplets": {},
	"compute reserved-ip create":    {},
	"registry create":               {},
	"registry login":                {},
	"docker build":                  {},
	"docker push":                   {},
	"apps create":                   {},
}

func isOpenClawDigitalOceanPlan(plan *maker.Plan) bool {
	if plan == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return false
	}
	if plan.Capabilities != nil && strings.EqualFold(strings.TrimSpace(plan.Capabilities.AppKind), "openclaw") {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(plan.Question)), "openclaw")
}

func validateDigitalOceanCommandSchema(plan *maker.Plan) ([]string, []string) {
	if plan == nil || !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return nil, nil
	}

	issues := make([]string, 0, 8)
	fixes := make([]string, 0, 4)
	strictOpenClaw := isOpenClawDigitalOceanPlan(plan)
	for i, cmd := range plan.Commands {
		if msg := validateDigitalOceanCommandBoundary(cmd.Args, strictOpenClaw); msg != "" {
			issues = append(issues, fmt.Sprintf("[HARD] command %d rejected: %s", i+1, msg))
		}
	}
	if len(issues) == 0 {
		return nil, nil
	}

	fixes = append(fixes,
		"Use only real DigitalOcean command families; plain Docker steps must start with docker and registry auth must use registry login",
		"For OpenClaw on DigitalOcean, keep the plan inside this deploy schema: compute ssh-key import, compute firewall create, compute droplet create, compute firewall add-droplets, optional compute reserved-ip create, registry create, registry login, docker build, docker push, apps create",
		"Use --tag-name on compute droplet create and never --tag-names on compute firewall create",
	)
	return uniqueStrings(issues), uniqueStrings(fixes)
}

func CheckStrictPlanCandidateRegression(baseline *maker.Plan, candidate *maker.Plan) error {
	if baseline == nil || candidate == nil {
		return nil
	}
	if !isOpenClawDigitalOceanPlan(baseline) && !isOpenClawDigitalOceanPlan(candidate) {
		return nil
	}

	baselineIssues := make(map[string]struct{}, 8)
	for _, issue := range strictSchemaIssueSignatures(baseline) {
		baselineIssues[issue] = struct{}{}
	}
	for _, issue := range strictSchemaIssueSignatures(candidate) {
		if _, ok := baselineIssues[issue]; ok {
			continue
		}
		return fmt.Errorf("candidate introduced strict deploy-schema violation: %s", issue)
	}
	return nil
}

func ValidatePlanPageBoundary(currentPlan *maker.Plan, page *PlanPage, envVars []string) error {
	if page == nil || len(page.Commands) == 0 {
		return nil
	}

	provider := ""
	strictOpenClaw := false
	commandOffset := 0
	if currentPlan != nil {
		provider = strings.ToLower(strings.TrimSpace(currentPlan.Provider))
		strictOpenClaw = isOpenClawDigitalOceanPlan(currentPlan)
		commandOffset = len(currentPlan.Commands)
	}
	if provider == "digitalocean" {
		for i, cmd := range page.Commands {
			if msg := validateDigitalOceanCommandBoundary(cmd.Args, strictOpenClaw); msg != "" {
				return fmt.Errorf("command %d rejected: %s", commandOffset+i+1, msg)
			}
		}
	}

	if issues := ValidateCommandBindingSequence(currentPlan, page.Commands, envVars); len(issues) > 0 {
		return fmt.Errorf("%s", strings.TrimSpace(issues[0]))
	}
	return nil
}

func ValidateCommandBindingSequence(existingPlan *maker.Plan, commands []maker.Command, envVars []string) []string {
	if len(commands) == 0 {
		return nil
	}

	available := make(map[string]struct{}, 32)
	providedEnv := make(map[string]struct{}, len(envVars))
	for _, kv := range envVars {
		key := strings.TrimSpace(kv)
		if k, _, ok := strings.Cut(key, "="); ok {
			key = k
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		if key != "" {
			providedEnv[key] = struct{}{}
		}
	}
	if existingPlan != nil {
		for _, cmd := range existingPlan.Commands {
			for key := range cmd.Produces {
				upper := strings.ToUpper(strings.TrimSpace(key))
				if upper != "" {
					available[upper] = struct{}{}
				}
			}
		}
	}

	issues := make([]string, 0, 4)
	commandOffset := 0
	if existingPlan != nil {
		commandOffset = len(existingPlan.Commands)
	}
	for i, cmd := range commands {
		missing := missingCommandBindings(cmd.Args, available, providedEnv)
		if len(missing) > 0 {
			issues = append(issues, fmt.Sprintf("[HARD] command %d rejected: placeholder(s) used before production: %s", commandOffset+i+1, strings.Join(missing, ", ")))
		}
		for key := range cmd.Produces {
			upper := strings.ToUpper(strings.TrimSpace(key))
			if upper != "" {
				available[upper] = struct{}{}
			}
		}
	}
	return uniqueStrings(issues)
}

func validateDigitalOceanCommandBoundary(args []string, strictOpenClaw bool) string {
	if len(args) == 0 {
		return "empty command args"
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
	case s0 == "__docker__":
		return "fake docker prefix __docker__ is not allowed; use docker"
	case s0 == "__docker_build__":
		return "fake docker prefix __DOCKER_BUILD__ is not allowed; use docker build"
	case s0 == "__docker_push__":
		return "fake docker prefix __DOCKER_PUSH__ is not allowed; use docker push"
	case s0 == "__local_docker_build__":
		return "fake docker prefix __LOCAL_DOCKER_BUILD__ is not allowed; use docker build"
	case s0 == "__local_docker_push__":
		return "fake docker prefix __LOCAL_DOCKER_PUSH__ is not allowed; use docker push"
	case s0 == "registry" && s1 == "docker-login":
		return "registry docker-login is not a valid doctl command; use registry login"
	case s0 == "registry" && s1 == "docker-credential":
		return "registry docker-credential is not a valid doctl command; use registry login"
	case s0 == "registry" && s1 == "docker-config":
		return "registry docker-config is not a valid doctl command; use registry login"
	case s0 == "registry" && s1 == "docker" && (s2 == "build" || s2 == "push"):
		return fmt.Sprintf("registry docker %s is not valid; use docker %s", s2, s2)
	case s0 == "registry" && s1 == "docker-push":
		return "registry docker-push is not valid; use docker push"
	case s0 == "compute" && s1 == "ssh-key" && s2 == "create":
		return "compute ssh-key create is not valid for this flow; use compute ssh-key import"
	}

	family := hydratedCommandFamily(args)
	if family == "" {
		return "empty command family"
	}
	if strictOpenClaw {
		if _, ok := openClawDOAllowedCommandFamilies[family]; !ok {
			return fmt.Sprintf("DigitalOcean/OpenClaw command family %q is outside the allowed deploy schema", family)
		}
	}
	if family == "compute droplet create" && countDOFlagOccurrences(args, "--tag") > 0 {
		return "compute droplet create does not support --tag; use --tag-name"
	}
	if family == "compute firewall create" && countDOFlagOccurrences(args, "--tag-names") > 0 {
		return "compute firewall create does not support --tag-names; attach the firewall with compute firewall add-droplets"
	}
	return ""
}

func missingCommandBindings(args []string, available map[string]struct{}, providedEnv map[string]struct{}) []string {
	missing := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, arg := range args {
		matches := placeholderRe.FindAllStringSubmatch(arg, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			token := strings.ToUpper(strings.TrimSpace(match[1]))
			if token == "" {
				continue
			}
			if _, ok := available[token]; ok {
				continue
			}
			if _, ok := providedEnv[token]; ok {
				continue
			}
			if isProviderCredentialToken(token) {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			missing = append(missing, "<"+token+">")
		}
	}
	sort.Strings(missing)
	return missing
}

func strictSchemaIssueSignatures(plan *maker.Plan) []string {
	issues, _ := validateDigitalOceanCommandSchema(plan)
	if len(issues) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(issues))
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		sig := normalizeSchemaIssueSignature(issue)
		if sig == "" {
			continue
		}
		if _, ok := seen[sig]; ok {
			continue
		}
		seen[sig] = struct{}{}
		out = append(out, sig)
	}
	sort.Strings(out)
	return out
}

func normalizeSchemaIssueSignature(issue string) string {
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return ""
	}
	if _, after, ok := strings.Cut(issue, " rejected: "); ok {
		issue = strings.TrimSpace(after)
	}
	return issue
}
