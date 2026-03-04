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
