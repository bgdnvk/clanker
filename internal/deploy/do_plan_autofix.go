package deploy

import (
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// ApplyDigitalOceanPlanAutofix runs DO-specific deterministic fixes.
// Only call this when provider == "digitalocean".
func ApplyDigitalOceanPlanAutofix(plan *maker.Plan, logf func(string, ...any)) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// Fix "registry docker-credential configure" → "registry login <REGISTRY_NAME>"
	credFixed := fixDORegistryCredentialHallucination(plan)
	if credFixed > 0 {
		logf("[deploy] do autofix: replaced %d 'registry docker-credential' → 'registry login'", credFixed)
	}

	// Fix "registry docker build" / "registry docker-push" → "docker build" / "docker push"
	dockerFixed := fixDORegistryDockerHallucination(plan)
	if dockerFixed > 0 {
		logf("[deploy] do autofix: replaced %d 'registry docker ...' → 'docker ...'", dockerFixed)
	}

	// Fix docker push tag to use <IMAGE_TAG> from docker build
	if fixDODockerTagConsistency(plan) {
		logf("[deploy] do autofix: rewrote docker push to use <IMAGE_TAG> from build step")
	}

	return plan
}

// fixDORegistryCredentialHallucination replaces the invalid
// "registry docker-credential configure" with "registry login <REGISTRY_NAME>".
func fixDORegistryCredentialHallucination(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 2 {
			continue
		}
		s0 := strings.ToLower(strings.TrimSpace(args[0]))
		s1 := strings.ToLower(strings.TrimSpace(args[1]))

		// "registry docker-credential ..." → "registry login <REGISTRY_NAME>"
		if s0 == "registry" && s1 == "docker-credential" {
			// Find if there's a REGISTRY_NAME placeholder in the plan
			regName := findProducedPlaceholder(plan, "REGISTRY_NAME")
			if regName == "" {
				regName = "<REGISTRY_NAME>"
			}
			plan.Commands[ci].Args = []string{"registry", "login", regName}
			plan.Commands[ci].Reason = "Authenticate Docker CLI with DigitalOcean Container Registry"
			count++
		}
	}
	return count
}

// fixDORegistryDockerHallucination replaces invalid "registry docker build" /
// "registry docker-push" doctl subcommands with plain docker CLI commands.
func fixDORegistryDockerHallucination(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		s0 := strings.ToLower(strings.TrimSpace(args[0]))
		s1 := strings.ToLower(strings.TrimSpace(args[1]))

		if s0 != "registry" {
			continue
		}

		// "registry docker build ..." → "docker build ..."
		if s1 == "docker" {
			sub := strings.ToLower(strings.TrimSpace(args[2]))
			if sub == "build" || sub == "push" {
				plan.Commands[ci].Args = append([]string{"docker"}, args[2:]...)
				count++
			}
		}

		// "registry docker-push ..." → "docker push ..."
		if s1 == "docker-push" {
			plan.Commands[ci].Args = append([]string{"docker", "push"}, args[2:]...)
			count++
		}
	}
	return count
}

// findProducedPlaceholder checks if any command produces a given placeholder name.
func findProducedPlaceholder(plan *maker.Plan, name string) string {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, cmd := range plan.Commands {
		for k := range cmd.Produces {
			if strings.ToUpper(strings.TrimSpace(k)) == upper {
				return "<" + upper + ">"
			}
		}
	}
	return ""
}

// fixDODockerTagConsistency ensures docker push uses <IMAGE_TAG> from docker build.
// The LLM often constructs an independent tag for the push step instead of
// referencing the placeholder produced by the build step.
func fixDODockerTagConsistency(plan *maker.Plan) bool {
	// Find the docker build step that produces IMAGE_TAG
	buildIdx := -1
	for i, cmd := range plan.Commands {
		if !isDockerSubcommand(cmd.Args, "build") {
			continue
		}
		for k := range cmd.Produces {
			if strings.EqualFold(k, "IMAGE_TAG") {
				buildIdx = i
				break
			}
		}
		if buildIdx >= 0 {
			break
		}
	}
	if buildIdx < 0 {
		return false // no docker build step producing IMAGE_TAG
	}

	// Find the docker push step(s) after the build
	fixed := false
	for i := buildIdx + 1; i < len(plan.Commands); i++ {
		cmd := &plan.Commands[i]
		if !isDockerSubcommand(cmd.Args, "push") {
			continue
		}
		// Check if it already references <IMAGE_TAG>
		already := false
		for _, a := range cmd.Args {
			if strings.Contains(strings.ToUpper(a), "<IMAGE_TAG>") {
				already = true
				break
			}
		}
		if already {
			continue
		}
		// Rewrite: the push target is typically the last non-flag arg.
		// docker push <image> — just replace the image arg with <IMAGE_TAG>
		for j := len(cmd.Args) - 1; j >= 0; j-- {
			arg := cmd.Args[j]
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if strings.EqualFold(arg, "docker") || strings.EqualFold(arg, "push") {
				continue
			}
			// This is the image ref — replace it
			cmd.Args[j] = "<IMAGE_TAG>"
			fixed = true
			break
		}
	}
	return fixed
}

// isDockerSubcommand checks if args represent "docker <sub>" (e.g. "docker build", "docker push").
func isDockerSubcommand(args []string, sub string) bool {
	if len(args) < 2 {
		return false
	}
	return strings.EqualFold(args[0], "docker") && strings.EqualFold(args[1], sub)
}
