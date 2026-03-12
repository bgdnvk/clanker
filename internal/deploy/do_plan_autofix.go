package deploy

import (
	"os"
	"path/filepath"
	"regexp"
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

	// FIRST: strip leading "doctl" from args — LLM sometimes includes it
	// and all downstream checks expect args[0] to be the subcommand
	prefixFixed := fixDONormalizeDoctlPrefix(plan)
	if prefixFixed > 0 {
		logf("[deploy] do autofix: stripped leading 'doctl' from %d command(s)", prefixFixed)
	}

	// Strip DOCR / docker-build / docker-push commands for plain droplet plans.
	// OpenClaw on DigitalOcean now keeps a small App Platform HTTPS proxy image flow.
	docrStripped := fixDOStripDOCRCommands(plan)
	if docrStripped > 0 {
		logf("[deploy] do autofix: stripped %d DOCR/docker-build/docker-push command(s)", docrStripped)
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

	malformedFixed := fixDOMalformedCommandPrefixes(plan)
	if malformedFixed > 0 {
		logf("[deploy] do autofix: normalized %d malformed DigitalOcean command prefix(es)", malformedFixed)
	}

	tagFlagFixed := fixDODropletTagFlag(plan)
	if tagFlagFixed > 0 {
		logf("[deploy] do autofix: rewrote %d invalid droplet tag flag(s) to --tag-name", tagFlagFixed)
	}

	// Replace hardcoded DOCR names in user-data with <REGISTRY_NAME> placeholder
	regUserDataFixed := fixDOHardcodedRegistryInUserData(plan)
	if regUserDataFixed > 0 {
		logf("[deploy] do autofix: replaced %d hardcoded registry name(s) in user-data with <REGISTRY_NAME>", regUserDataFixed)
	}

	// Fix bare "build -t ..." / "push ..." → "docker build -t ..." / "docker push ..."
	barePrefixed := fixDOBareDockerPrefix(plan)
	if barePrefixed > 0 {
		logf("[deploy] do autofix: prepended 'docker' to %d bare build/push command(s)", barePrefixed)
	}

	// Inject missing docker build + push when user-data references DOCR images
	dockerInjected := fixDOMissingDockerBuildPush(plan)
	if dockerInjected > 0 {
		logf("[deploy] do autofix: injected %d missing docker build/push step(s)", dockerInjected)
	}

	// Fix docker push tag to use <IMAGE_TAG> from docker build
	if fixDODockerTagConsistency(plan) {
		logf("[deploy] do autofix: rewrote docker push to use <IMAGE_TAG> from build step")
	}

	// Fix "registry login <REGISTRY_NAME>" → "registry login" (no arg needed)
	regLoginFixed := fixDORegistryLoginArgs(plan)
	if regLoginFixed > 0 {
		logf("[deploy] do autofix: stripped registry name arg from %d 'registry login' step(s)", regLoginFixed)
	}

	// Fix firewall create JMESPath "[0].id" → "id"
	fwFixed := fixDOFirewallJMESPath(plan)
	if fwFixed > 0 {
		logf("[deploy] do autofix: fixed %d firewall JMESPath produce(s)", fwFixed)
	}

	sshProdFixed := fixDOSSHKeyProducePaths(plan)
	if sshProdFixed > 0 {
		logf("[deploy] do autofix: fixed %d ssh-key produce path(s)", sshProdFixed)
	}

	// Generic: sanitize all JMESPath produces (strip $ prefix, fix @ syntax)
	jmesFixed := fixDOProducesJMESPath(plan)
	if jmesFixed > 0 {
		logf("[deploy] do autofix: sanitized %d JMESPath produce expression(s)", jmesFixed)
	}

	outputFixed := fixDOJSONOutputFlags(plan)
	if outputFixed > 0 {
		logf("[deploy] do autofix: rewrote %d invalid doctl --format json flag(s) to --output json", outputFixed)
	}

	// Generic: strip unsupported flags from doctl commands
	flagFixed := fixDOUnsupportedFlags(plan)
	if flagFixed > 0 {
		logf("[deploy] do autofix: stripped %d unsupported flag(s)", flagFixed)
	}

	// Ensure registry create uses basic tier (starter=500MiB causes silent push stalls)
	tierFixed := fixDORegistrySubscriptionTier(plan)
	if tierFixed > 0 {
		logf("[deploy] do autofix: ensured --subscription-tier basic on %d registry create(s)", tierFixed)
	}

	// Fix reserved-ip create: --region conflicts with --droplet-id
	ripFixed := fixDOReservedIPFlags(plan)
	if ripFixed > 0 {
		logf("[deploy] do autofix: stripped --region from %d reserved-ip create with --droplet-id", ripFixed)
	}

	// Fix firewall rules with empty address field
	fwAddrFixed := fixDOFirewallEmptyAddress(plan)
	if fwAddrFixed > 0 {
		logf("[deploy] do autofix: filled %d empty firewall address field(s) with 0.0.0.0/0", fwAddrFixed)
	}

	// Ensure user-data has export HOME=/root for doctl / cloud-init compat
	homeFixed := fixDOUserDataMissingHome(plan)
	if homeFixed > 0 {
		logf("[deploy] do autofix: injected 'export HOME=/root' in %d user-data script(s)", homeFixed)
	}

	// Sanitize inline user-data: remove broken compose, empty volumes/env
	udFixed := fixDOUserDataScript(plan)
	if udFixed > 0 {
		logf("[deploy] do autofix: fixed %d user-data compose issue(s)", udFixed)
	}

	// OpenClaw: replace 'docker compose build' with 'docker build -t openclaw:local .'
	// because compose file uses 'image: openclaw:local' (no build: directive)
	ocBuildFixed := fixDOOpenClawComposeBuild(plan)
	if ocBuildFixed > 0 {
		logf("[deploy] do autofix: replaced %d 'docker compose build' → 'docker build -t openclaw:local .' in user-data", ocBuildFixed)
	}

	// OpenClaw: remove DO token leakage and masked clone failures from user-data
	ocUserDataFixed := fixDOOpenClawUserDataSafety(plan)
	if ocUserDataFixed > 0 {
		logf("[deploy] do autofix: fixed %d OpenClaw user-data safety issue(s)", ocUserDataFixed)
	}

	// OpenClaw: strip unnecessary 80/443/8080 firewall ports on plain droplet plans
	ocFirewallFixed := fixDOOpenClawFirewallSpec(plan)
	if ocFirewallFixed > 0 {
		logf("[deploy] do autofix: canonicalized %d OpenClaw firewall command(s)", ocFirewallFixed)
	}

	// Fix ssh-key import pointing at nonexistent local key file
	sshFixed := fixDOSSHKeyPath(plan)
	if sshFixed > 0 {
		logf("[deploy] do autofix: corrected %d ssh-key import path(s)", sshFixed)
	}

	// Strip git clone steps — executor already handles cloning for docker build context
	gitFixed := fixDOStripGitClone(plan)
	if gitFixed > 0 {
		logf("[deploy] do autofix: stripped %d git clone step(s) (executor handles clone)", gitFixed)
	}

	// Normalize <REGISTRY_ENDPOINT>/ → registry.digitalocean.com/<REGISTRY_NAME>/
	// and fix bare /image:tag patterns in docker build -t tags
	repFixed := fixDORegistryEndpointHallucination(plan)
	if repFixed > 0 {
		logf("[deploy] do autofix: normalized %d REGISTRY_ENDPOINT reference(s) to REGISTRY_NAME", repFixed)
	}

	// Fix registry create produce paths (doctl output doesn't have 'endpoint' field)
	regProdFixed := fixDORegistryProducePaths(plan)
	if regProdFixed > 0 {
		logf("[deploy] do autofix: fixed %d registry produce path(s)", regProdFixed)
	}

	return plan
}

// ---------------------------------------------------------------------------
// Doctl prefix normalizer — must run FIRST
// ---------------------------------------------------------------------------

// fixDONormalizeDoctlPrefix strips leading "doctl" from all command args.
// LLM/self-heal often generates ["doctl","compute","firewall",...] but every
// autofix expects args[0] to be the subcommand (e.g. "compute").
func fixDONormalizeDoctlPrefix(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) > 1 && strings.EqualFold(strings.TrimSpace(args[0]), "doctl") {
			plan.Commands[ci].Args = args[1:]
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Hardcoded DOCR registry name → <REGISTRY_NAME> placeholder
// ---------------------------------------------------------------------------

// docrHardcodedNameRe matches registry.digitalocean.com/<hardcoded-name>/
// but NOT when the name is already a <PLACEHOLDER>.
var docrHardcodedNameRe = regexp.MustCompile(`registry\.digitalocean\.com/([^/<>\s]+)/`)

// fixDOHardcodedRegistryInUserData replaces hardcoded DOCR registry names
// in user-data, docker build/push args, and produces values with <REGISTRY_NAME>.
func fixDOHardcodedRegistryInUserData(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args

		// Fix docker build/push -t args and produces
		if isDockerSubcommand(args, "build") || isDockerSubcommand(args, "push") {
			for ai, arg := range args {
				fixed := docrHardcodedNameRe.ReplaceAllString(arg, "registry.digitalocean.com/<REGISTRY_NAME>/")
				if fixed != arg {
					plan.Commands[ci].Args[ai] = fixed
					count++
				}
			}
			for k, v := range plan.Commands[ci].Produces {
				fixed := docrHardcodedNameRe.ReplaceAllString(v, "registry.digitalocean.com/<REGISTRY_NAME>/")
				if fixed != v {
					plan.Commands[ci].Produces[k] = fixed
					count++
				}
			}
			continue
		}

		// Fix user-data in droplet create
		if !isDODropletCreate(args) {
			continue
		}
		for ai, arg := range args {
			if arg != "--user-data" || ai+1 >= len(args) {
				continue
			}
			script := args[ai+1]
			fixed := docrHardcodedNameRe.ReplaceAllString(script, "registry.digitalocean.com/<REGISTRY_NAME>/")
			if fixed != script {
				plan.Commands[ci].Args[ai+1] = fixed
				count++
			}
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Inject missing docker build + push
// ---------------------------------------------------------------------------

// fixDOStripDOCRCommands removes all DOCR-related commands from the plan.
// Plain droplet plans do not need local registry/build/push steps, but the
// OpenClaw DigitalOcean HTTPS proxy flow does keep them.
func fixDOStripDOCRCommands(plan *maker.Plan) int {
	if shouldPreserveOpenClawDOProxyImageFlow(plan) {
		return 0
	}
	count := 0
	kept := make([]maker.Command, 0, len(plan.Commands))
	for _, cmd := range plan.Commands {
		if isDOCRCommand(cmd.Args) {
			count++
			continue
		}
		kept = append(kept, cmd)
	}
	plan.Commands = kept
	return count
}

func shouldPreserveOpenClawDOProxyImageFlow(plan *maker.Plan) bool {
	if plan == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Provider), "digitalocean") {
		return false
	}
	if plan.Capabilities != nil {
		if strings.EqualFold(strings.TrimSpace(plan.Capabilities.AppKind), "openclaw") && strings.EqualFold(strings.TrimSpace(plan.Capabilities.RuntimeModel), "droplet-compose") {
			for _, cmd := range plan.Commands {
				if len(cmd.Args) >= 2 && strings.EqualFold(cmd.Args[0], "apps") && strings.EqualFold(cmd.Args[1], "create") {
					return true
				}
			}
		}
	}
	lowerQ := strings.ToLower(strings.TrimSpace(plan.Question))
	if strings.Contains(lowerQ, "openclaw") {
		for _, cmd := range plan.Commands {
			if len(cmd.Args) >= 2 && strings.EqualFold(cmd.Args[0], "apps") && strings.EqualFold(cmd.Args[1], "create") {
				return true
			}
		}
	}
	return false
}

// isDOCRCommand returns true for registry create/login, docker build, docker push.
func isDOCRCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	a0 := strings.ToLower(strings.TrimSpace(args[0]))
	// "registry create" / "registry login" / "registry ..."
	if a0 == "registry" {
		return true
	}
	// "docker build ..." or "docker push ..."
	if a0 == "docker" && len(args) >= 2 {
		sub := strings.ToLower(strings.TrimSpace(args[1]))
		if sub == "build" || sub == "push" {
			return true
		}
	}
	// bare "build -t ..." or "push ..."
	if a0 == "build" || a0 == "push" {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------

// fixDOMissingDockerBuildPush injects docker build + docker push when the plan
// references DOCR images (in user-data or notes) but has no docker build/push.
// DEPRECATED: no-op since we build on the droplet now. Kept for compatibility.
func fixDOMissingDockerBuildPush(plan *maker.Plan) int {
	return 0 // build on droplet — no local docker build/push needed
}

// docrImageRefRe matches DOCR image references in text
var docrImageRefRe = regexp.MustCompile(`registry\.digitalocean\.com/[^/\s"']+/[^\s"']+`)

// extractDOCRImageFromPlan finds a DOCR image ref in user-data or notes.
func extractDOCRImageFromPlan(plan *maker.Plan) string {
	// Check user-data first (most reliable)
	for _, cmd := range plan.Commands {
		if !isDODropletCreate(cmd.Args) {
			continue
		}
		for ai, arg := range cmd.Args {
			if arg == "--user-data" && ai+1 < len(cmd.Args) {
				if m := docrImageRefRe.FindString(cmd.Args[ai+1]); m != "" {
					return m
				}
			}
		}
	}
	// Fallback: check notes
	for _, note := range plan.Notes {
		if m := docrImageRefRe.FindString(note); m != "" {
			// Normalize hardcoded names in notes too
			return docrHardcodedNameRe.ReplaceAllString(m, "registry.digitalocean.com/<REGISTRY_NAME>/")
		}
	}
	return ""
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

		// "registry docker-credential ..." → "registry login" (no args needed)
		if s0 == "registry" && s1 == "docker-credential" {
			plan.Commands[ci].Args = []string{"registry", "login"}
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

// fixDOMalformedCommandPrefixes repairs a few recurring malformed command families
// that show up in DigitalOcean plans and should be normalized before validation.
func fixDOMalformedCommandPrefixes(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) == 0 {
			continue
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
			plan.Commands[ci].Args = append([]string{"docker"}, args[1:]...)
			count++
		case s0 == "__docker_build__":
			plan.Commands[ci].Args = append([]string{"docker", "build"}, args[1:]...)
			count++
		case s0 == "__docker_push__":
			plan.Commands[ci].Args = append([]string{"docker", "push"}, args[1:]...)
			count++
		case s0 == "__local_docker_build__":
			plan.Commands[ci].Args = append([]string{"docker", "build"}, args[1:]...)
			count++
		case s0 == "__local_docker_push__":
			plan.Commands[ci].Args = append([]string{"docker", "push"}, args[1:]...)
			count++
		case s0 == "registry" && s1 == "docker-login":
			plan.Commands[ci].Args = []string{"registry", "login"}
			plan.Commands[ci].Reason = "Authenticate Docker CLI with DigitalOcean Container Registry"
			count++
		case s0 == "registry" && s1 == "docker-config":
			plan.Commands[ci].Args = []string{"registry", "login"}
			plan.Commands[ci].Reason = "Authenticate Docker CLI with DigitalOcean Container Registry"
			count++
		case s0 == "registry" && s1 == "docker-credential":
			plan.Commands[ci].Args = []string{"registry", "login"}
			plan.Commands[ci].Reason = "Authenticate Docker CLI with DigitalOcean Container Registry"
			count++
		case s0 == "compute" && s1 == "ssh-key" && s2 == "create":
			hasPublicKeyFile := false
			for _, arg := range args[3:] {
				if strings.EqualFold(strings.TrimSpace(arg), "--public-key-file") {
					hasPublicKeyFile = true
					break
				}
			}
			if hasPublicKeyFile {
				fixed := append([]string(nil), args...)
				fixed[2] = "import"
				plan.Commands[ci].Args = fixed
				count++
			}
		}
	}
	return count
}

func fixDODropletTagFlag(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(args[0]), "compute") ||
			!strings.EqualFold(strings.TrimSpace(args[1]), "droplet") ||
			!strings.EqualFold(strings.TrimSpace(args[2]), "create") {
			continue
		}
		for ai := range args {
			trimmed := strings.TrimSpace(args[ai])
			if strings.EqualFold(trimmed, "--tag") {
				plan.Commands[ci].Args[ai] = "--tag-name"
				count++
				continue
			}
			if strings.HasPrefix(strings.ToLower(trimmed), "--tag=") {
				plan.Commands[ci].Args[ai] = "--tag-name=" + strings.TrimSpace(trimmed[len("--tag="):])
				count++
			}
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

// isBareDockerCommand detects bare "build -t ..." / "push ..." without "docker" prefix.
func isBareDockerCommand(args []string, sub string) bool {
	if len(args) < 1 {
		return false
	}
	if !strings.EqualFold(args[0], sub) {
		return false
	}
	// For "build" require "-t" flag to avoid false positives (e.g. terraform build)
	if strings.EqualFold(sub, "build") {
		for _, a := range args {
			if a == "-t" {
				return true
			}
		}
		return false
	}
	// For "push" require a registry.digitalocean.com ref
	if strings.EqualFold(sub, "push") {
		for _, a := range args {
			if strings.Contains(a, "registry.digitalocean.com") || strings.Contains(a, "<IMAGE") {
				return true
			}
		}
		return false
	}
	return false
}

// fixDOBareDockerPrefix prepends "docker" to bare "build -t" / "push" commands
// that are clearly docker commands missing the prefix.
func fixDOBareDockerPrefix(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if isBareDockerCommand(args, "build") || isBareDockerCommand(args, "push") {
			plan.Commands[ci].Args = append([]string{"docker"}, args...)
			count++
		}
	}
	return count
}

// fixDORegistryLoginArgs strips the registry name arg from "registry login <NAME>".
// doctl registry login takes no positional args.
func fixDORegistryLoginArgs(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		if !strings.EqualFold(args[0], "registry") || !strings.EqualFold(args[1], "login") {
			continue
		}
		// Keep only "registry login" — strip any trailing args that aren't flags
		newArgs := []string{"registry", "login"}
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "-") {
				newArgs = append(newArgs, a)
			}
		}
		plan.Commands[ci].Args = newArgs
		count++
	}
	return count
}

// fixDOFirewallJMESPath corrects firewall create produces from "[0].id" to "id".
// doctl compute firewall create returns a single object, not an array.
func fixDOFirewallJMESPath(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		if !strings.EqualFold(args[0], "compute") || !strings.EqualFold(args[1], "firewall") || !strings.EqualFold(args[2], "create") {
			continue
		}
		for k, v := range plan.Commands[ci].Produces {
			norm := strings.TrimSpace(v)
			if norm == "[0].id" || norm == "firewall.id" {
				plan.Commands[ci].Produces[k] = "id"
				count++
			}
		}
	}
	return count
}

func fixDOSSHKeyProducePaths(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		if !strings.EqualFold(args[0], "compute") || !strings.EqualFold(args[1], "ssh-key") || !strings.EqualFold(args[2], "import") {
			continue
		}
		for k, v := range plan.Commands[ci].Produces {
			norm := strings.TrimSpace(v)
			if strings.EqualFold(strings.TrimSpace(k), "SSH_KEY_ID") && (norm == "ssh_key.id" || norm == "[0].id") {
				plan.Commands[ci].Produces[k] = "id"
				count++
			}
		}
	}
	return count
}

// jsonPathDollarRe matches JSONPath-style $[0] or $. prefixes that aren't valid JMESPath.
var jsonPathDollarRe = regexp.MustCompile(`^\$\.?`)

// jsonPathAtFilterRe matches JSONPath filter syntax ?(@.field=='value') → JMESPath ?field=='value'
var jsonPathAtFilterRe = regexp.MustCompile(`\?\(@\.([^)]+)\)`)

// fixDOProducesJMESPath sanitizes all produces expressions across every command.
// LLMs often emit JSONPath ($[0].id, $[0].networks...) instead of JMESPath.
// jmesFilterRe matches JMESPath filter expressions [?field==`value`] etc.
var jmesFilterRe = regexp.MustCompile(`\[\?[^\]]+\]`)

// jmesPipeRe matches JMESPath pipe " | [0]" suffix.
var jmesPipeRe = regexp.MustCompile(`\s*\|\s*\[0\]\s*$`)

func fixDOProducesJMESPath(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		for k, v := range plan.Commands[ci].Produces {
			orig := v
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}

			// Strip leading $ (JSONPath root)
			v = jsonPathDollarRe.ReplaceAllString(v, "")

			// Fix ?(@.type=='public') → ?type=='public'
			v = jsonPathAtFilterRe.ReplaceAllString(v, "?$1")

			// Fix backtick-less string comparisons: =='value' → ==`value`
			v = fixJMESPathStringLiterals(v)

			// Normalize JMESPath filter [?...] → [0] since our parser does simple indexing
			v = jmesFilterRe.ReplaceAllString(v, "[0]")

			// Strip trailing pipe " | [0]" — redundant after filter→[0] rewrite
			v = jmesPipeRe.ReplaceAllString(v, "")

			if v != orig {
				plan.Commands[ci].Produces[k] = v
				count++
			}
		}
	}
	return count
}

func fixDOJSONOutputFlags(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) == 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(args[0]), "docker") || strings.EqualFold(strings.TrimSpace(args[0]), "git") {
			continue
		}
		for ai := 0; ai < len(args)-1; ai++ {
			if !strings.EqualFold(strings.TrimSpace(args[ai]), "--format") || !strings.EqualFold(strings.TrimSpace(args[ai+1]), "json") {
				continue
			}
			args[ai] = "--output"
			args[ai+1] = "json"
			count++
		}
		plan.Commands[ci].Args = args
	}
	return count
}

// jmesStringLiteralRe matches =='value' or =="value" (quoted comparisons that should use backticks).
var jmesStringLiteralRe = regexp.MustCompile(`==\s*['"]([^'"]+)['"]`)

// fixJMESPathStringLiterals replaces =='value' with ==`value` in filter expressions.
// JMESPath uses backticks for literal string values.
func fixJMESPathStringLiterals(expr string) string {
	return jmesStringLiteralRe.ReplaceAllStringFunc(expr, func(match string) string {
		sub := jmesStringLiteralRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return "==" + string(rune(96)) + sub[1] + string(rune(96))
	})
}

// doRegistryUnsupportedFlags lists flags that doctl registry create doesn't accept.
var doRegistryUnsupportedFlags = map[string]bool{
	"--region": true,
}

// doDOCRCreateSingletonFlags lists doctl subcommands that are global singletons (no region).
var doDOCRCommands = map[string]bool{
	"registry create": true,
}

// fixDOUnsupportedFlags strips flags that specific doctl commands don't support.
// e.g. registry create doesn't accept --region.
func fixDOUnsupportedFlags(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 2 {
			continue
		}
		cmdKey := strings.ToLower(strings.TrimSpace(args[0])) + " " + strings.ToLower(strings.TrimSpace(args[1]))

		var unsupported map[string]bool
		if doDOCRCommands[cmdKey] {
			unsupported = doRegistryUnsupportedFlags
		}
		if unsupported == nil {
			continue
		}

		newArgs := make([]string, 0, len(args))
		skipNext := false
		for i, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			low := strings.ToLower(strings.TrimSpace(a))
			if unsupported[low] {
				// Skip this flag and its value (if next arg isn't another flag)
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					skipNext = true
				}
				count++
				continue
			}
			newArgs = append(newArgs, a)
		}
		plan.Commands[ci].Args = newArgs
	}
	return count
}

// fixDOReservedIPFlags strips --region (and its value) from "reserved-ip create"
// when --droplet-id is also present. doctl only allows one of the two.
func fixDOReservedIPFlags(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		// Match: doctl compute reserved-ip create ...
		norm := make([]string, len(args))
		for i, a := range args {
			norm[i] = strings.ToLower(strings.TrimSpace(a))
		}
		// Find "reserved-ip" + "create"
		hasCreate := false
		for i := 0; i < len(norm)-1; i++ {
			if (norm[i] == "reserved-ip" || norm[i] == "floating-ip") && norm[i+1] == "create" {
				hasCreate = true
				break
			}
		}
		if !hasCreate {
			continue
		}
		// Check if --droplet-id is present
		hasDropletID := false
		for _, a := range norm {
			if a == "--droplet-id" {
				hasDropletID = true
				break
			}
		}
		if !hasDropletID {
			continue
		}
		// Strip --region and its value
		cleaned := make([]string, 0, len(args))
		skip := false
		for _, a := range args {
			if skip {
				skip = false
				continue
			}
			if strings.EqualFold(strings.TrimSpace(a), "--region") {
				skip = true // skip the flag and its next value
				continue
			}
			cleaned = append(cleaned, a)
		}
		if len(cleaned) < len(args) {
			plan.Commands[ci].Args = cleaned
			count++
		}
	}
	return count
}

// FilterDOValidationNoise removes LLM validator false positives for DO plans.
// The executor supports both doctl and docker commands, but the LLM validator
// doesn't know this and flags docker build/push as broken.
func FilterDOValidationNoise(v *PlanValidation, logf func(string, ...any)) *PlanValidation {
	if v == nil {
		return v
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	isDONoise := func(s string) bool {
		l := strings.ToLower(strings.TrimSpace(s))
		// docker commands flagged as "won't work as doctl subcommands"
		if strings.Contains(l, "docker") && strings.Contains(l, "doctl") {
			return true
		}
		if (strings.Contains(l, "docker build") || strings.Contains(l, "docker push")) && strings.Contains(l, "won't work") {
			return true
		}
		// registry login doesn't need a registry name arg
		if strings.Contains(l, "registry login") && strings.Contains(l, "registry name") {
			return true
		}
		// firewall JMESPath — already fixed by autofix
		if strings.Contains(l, "firewall") && (strings.Contains(l, "[0].id") || strings.Contains(l, "jmespath") || strings.Contains(l, "not an array")) {
			return true
		}
		// ICMP rules are optional
		if strings.Contains(l, "icmp") && strings.Contains(l, "firewall") {
			return true
		}
		// Token exposure in user-data — unavoidable on DO (no secret injection like AWS SSM)
		if strings.Contains(l, "token") && strings.Contains(l, "user-data") && (strings.Contains(l, "security") || strings.Contains(l, "plain text") || strings.Contains(l, "visible") || strings.Contains(l, "metadata")) {
			return true
		}
		if strings.Contains(l, "access_token") && (strings.Contains(l, "expos") || strings.Contains(l, "security risk")) {
			return true
		}
		// Registry region flag — already stripped by autofix
		if strings.Contains(l, "registry") && strings.Contains(l, "region") && (strings.Contains(l, "global") || strings.Contains(l, "doesn't accept") || strings.Contains(l, "does not accept")) {
			return true
		}
		// JMESPath / JSONPath issues — already fixed by autofix
		if strings.Contains(l, "jmespath") || strings.Contains(l, "jsonpath") || (strings.Contains(l, "$[") && strings.Contains(l, "produces")) {
			return true
		}
		if strings.Contains(l, "droplet_ip") && (strings.Contains(l, "jmespath") || strings.Contains(l, "jsonpath") || strings.Contains(l, "syntax")) {
			return true
		}
		return false
	}

	filtered := make([]string, 0, len(v.Issues))
	removed := 0
	for _, issue := range v.Issues {
		if isDONoise(issue) {
			removed++
			continue
		}
		filtered = append(filtered, issue)
	}
	if removed > 0 {
		logf("[deploy] do noise filter: suppressed %d LLM validator false positive(s)", removed)
	}

	v.Issues = filtered
	if len(v.Issues) == 0 {
		v.IsValid = true
		v.Fixes = nil
	}
	return v
}

// fixDOFirewallEmptyAddress fixes firewall inbound-rules where the LLM
// leaves the address: field empty (e.g. "protocol:tcp,ports:22,address:").
// doctl requires a CIDR — default to 0.0.0.0/0.
func fixDOFirewallEmptyAddress(plan *maker.Plan) int {
	if plan == nil {
		return 0
	}
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 4 {
			continue
		}
		s0 := strings.ToLower(strings.TrimSpace(args[0]))
		s1 := strings.ToLower(strings.TrimSpace(args[1]))
		if s0 != "compute" || s1 != "firewall" {
			continue
		}
		for ai, arg := range args {
			if !strings.Contains(arg, "address:") {
				continue
			}
			// Fix empty address fields: "address:" followed by space or end
			// $1 preserves the space separator between rules
			fixed := emptyFWAddrRe.ReplaceAllString(arg, "address:0.0.0.0/0${1}")
			if fixed != arg {
				plan.Commands[ci].Args[ai] = fixed
				count++
			}
		}
	}
	return count
}

// emptyFWAddrRe matches "address:" followed by whitespace or end-of-string
var emptyFWAddrRe = regexp.MustCompile(`address:(\s|$)`)

// ---------------------------------------------------------------------------
// User-data compose sanitizer
// ---------------------------------------------------------------------------

// fixDOUserDataMissingHome injects "export HOME=/root" into user-data
// scripts that call doctl. Cloud-init runs without $HOME set, which
// causes "neither $XDG_CONFIG_HOME nor $HOME are defined" from doctl.
func fixDOUserDataMissingHome(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if !isDODropletCreate(args) {
			continue
		}
		for ai, arg := range args {
			if arg == "--user-data" && ai+1 < len(args) {
				script := args[ai+1]
				// only fix if doctl is referenced and HOME isn't already exported
				if !strings.Contains(script, "doctl") {
					continue
				}
				if strings.Contains(script, "export HOME=") || strings.Contains(script, "HOME=/root") {
					continue
				}
				// inject after "set -e" or after shebang
				if idx := strings.Index(script, "set -e\n"); idx >= 0 {
					insert := idx + len("set -e\n")
					script = script[:insert] + "export HOME=/root\n" + script[insert:]
				} else if strings.HasPrefix(script, "#!/bin/bash\n") {
					script = "#!/bin/bash\nexport HOME=/root\n" + script[len("#!/bin/bash\n"):]
				} else {
					script = "export HOME=/root\n" + script
				}
				plan.Commands[ci].Args[ai+1] = script
				count++
			}
		}
	}
	return count
}

// fixDOUserDataScript sanitizes inline --user-data on compute droplet create.
// Reuses the generic fixUserDataScript (path typos, shebang) then applies
// compose-specific fixes (empty volumes, empty env, redundant heredoc).
func fixDOUserDataScript(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if !isDODropletCreate(args) {
			continue
		}
		for ai, arg := range args {
			if arg == "--user-data" && ai+1 < len(args) {
				script := args[ai+1]
				// Generic path typos + shebang (shared with AWS)
				script, n := fixUserDataScript(script)
				count += n
				// Compose-specific sanitize
				script, n2 := sanitizeInlineUserData(script)
				count += n2
				if count > 0 {
					plan.Commands[ci].Args[ai+1] = script
				}
			}
		}
	}
	return count
}

// fixDOOpenClawComposeBuild replaces 'docker compose build ...' with
// 'docker build -t openclaw:local .' in user-data scripts.
// OpenClaw's docker-compose.yml uses 'image: openclaw:local' (no build: directive),
// so 'docker compose build' silently does nothing — we need a direct docker build.
func fixDOOpenClawComposeBuild(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if !isDODropletCreate(args) {
			continue
		}
		for ai, arg := range args {
			if arg == "--user-data" && ai+1 < len(args) {
				script := args[ai+1]
				// Only apply to scripts that mention openclaw
				if !strings.Contains(strings.ToLower(script), "openclaw") {
					continue
				}
				newScript := script
				// Replace 'docker compose build ...' with 'docker build -t openclaw:local .'
				// Handles: docker compose build, docker compose build openclaw-gateway, docker-compose build, etc.
				composeBuildRe := regexp.MustCompile(`docker[\s-]+compose\s+build\s*[^\n]*`)
				if composeBuildRe.MatchString(newScript) {
					newScript = composeBuildRe.ReplaceAllString(newScript, "docker build -t openclaw:local .")
					count++
				}
				// Also fix 'docker pull openclaw:local' or 'docker pull <IMAGE_URI>' → remove (we build locally)
				pullLocalRe := regexp.MustCompile(`(?m)^.*docker\s+pull\s+openclaw:local.*\n?`)
				if pullLocalRe.MatchString(newScript) {
					newScript = pullLocalRe.ReplaceAllString(newScript, "")
					count++
				}
				// Fix 'docker tag <IMAGE_URI> openclaw:latest' → remove (we build locally as openclaw:local)
				tagLocalRe := regexp.MustCompile(`(?m)^.*docker\s+tag\s+\S+\s+openclaw:(local|latest).*\n?`)
				if tagLocalRe.MatchString(newScript) {
					newScript = tagLocalRe.ReplaceAllString(newScript, "")
					count++
				}
				if newScript != script {
					plan.Commands[ci].Args[ai+1] = newScript
				}
			}
		}
	}
	return count
}

func fixDOOpenClawUserDataSafety(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if !isDODropletCreate(args) {
			continue
		}
		for ai, arg := range args {
			if arg != "--user-data" || ai+1 >= len(args) {
				continue
			}
			script := args[ai+1]
			if !hasOpenClawDORuntimeScript(script) {
				continue
			}
			newScript := script
			doTokenRe := regexp.MustCompile(`(?m)^\s*DIGITALOCEAN_ACCESS_TOKEN=.*\n?`)
			if doTokenRe.MatchString(newScript) {
				newScript = doTokenRe.ReplaceAllString(newScript, "")
				count++
			}
			if fixed, changed := stripOpenClawCloneSoftFail(newScript); changed {
				newScript = fixed
				count++
			}
			if fixed, changed := stripOpenClawDockerSetupSoftFail(newScript); changed {
				newScript = fixed
				count++
			}
			composeUpFlagRe := regexp.MustCompile(`(?m)(docker(?:-compose|\s+compose)\s+up\s+-d\s+openclaw-gateway)\s+--wait(?:\s+--output\s+\S+)?(\s*)$`)
			if composeUpFlagRe.MatchString(newScript) {
				newScript = composeUpFlagRe.ReplaceAllString(newScript, `$1$2`)
				count++
			}
			composeUpOutputOnlyRe := regexp.MustCompile(`(?m)(docker(?:-compose|\s+compose)\s+up\s+-d\s+openclaw-gateway)\s+--output\s+\S+(\s*)$`)
			if composeUpOutputOnlyRe.MatchString(newScript) {
				newScript = composeUpOutputOnlyRe.ReplaceAllString(newScript, `$1$2`)
				count++
			}
			if canonical, changed := normalizeOpenClawDOBootstrapScript(newScript); changed {
				newScript = canonical
				count++
			}
			if newScript != script {
				plan.Commands[ci].Args[ai+1] = newScript
			}
		}
	}
	return count
}

func fixDOOpenClawFirewallSpec(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) == 0 {
		return 0
	}
	hasOpenClawDroplet := false
	for _, cmd := range plan.Commands {
		if !isDODropletCreate(cmd.Args) {
			continue
		}
		script := extractDoctlUserDataScript(cmd.Args)
		if hasOpenClawDORuntimeScript(script) {
			hasOpenClawDroplet = true
			break
		}
	}
	if !hasOpenClawDroplet {
		return 0
	}
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 || !strings.EqualFold(args[0], "compute") || !strings.EqualFold(args[1], "firewall") {
			continue
		}
		fixed, changed := canonicalizeOpenClawDOFirewallArgs(args)
		if !changed {
			continue
		}
		plan.Commands[ci].Args = fixed
		count++
	}
	return count
}

// Regex for user-data compose sanitization
var (
	// "      - :/container/path" — volume mount with no host path
	udEmptyVolSrcRe = regexp.MustCompile(`^\s*-\s*:/\S+`)
	// "      MY_VAR: " — CAPS env key with empty value in compose environment
	udEmptyCapsEnvRe = regexp.MustCompile(`^\s+[A-Z][A-Z0-9_]*:\s*$`)
	// "cat > /path/docker-compose.yml << 'MARKER'"
	udComposeHeredocRe = regexp.MustCompile(`cat\s*>\s*\S*docker-compose[.\w]*\s*<<\s*['"]?(\w+)['"]?`)
)

// sanitizeInlineUserData fixes common LLM hallucinations in user-data scripts:
// 1. Removes inline compose heredoc when repo compose is already cloned+copied
// 2. Strips empty volume sources (- :/path)
// 3. Strips empty CAPS env values (KEY: ) in compose blocks
func sanitizeInlineUserData(script string) (string, int) {
	lines := strings.Split(script, "\n")
	count := 0

	// Pre-scan: repo clone + compose copy → prefer repo's compose
	hasClone, hasCpCompose := false, false
	for _, l := range lines {
		if strings.Contains(l, "git clone") {
			hasClone = true
		}
		if strings.Contains(l, "cp") && strings.Contains(l, "docker-compose") {
			hasCpCompose = true
		}
	}
	preferRepo := hasClone && hasCpCompose

	var out []string
	inHeredoc := false
	heredocEnd := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if inHeredoc {
			if trimmed == heredocEnd {
				inHeredoc = false
				if preferRepo {
					count++
					continue // drop heredoc end marker
				}
			}
			if preferRepo {
				continue // drop inline compose lines
			}
			// Keeping heredoc — fix broken lines inside it
			if udEmptyVolSrcRe.MatchString(line) {
				count++
				continue
			}
			if udEmptyCapsEnvRe.MatchString(line) {
				count++
				continue
			}
			out = append(out, line)
			continue
		}

		// Detect compose heredoc start
		m := udComposeHeredocRe.FindStringSubmatch(line)
		if m != nil {
			heredocEnd = m[1]
			inHeredoc = true
			if preferRepo {
				// Remove preceding comment about compose if present
				if len(out) > 0 {
					prev := strings.ToLower(strings.TrimSpace(out[len(out)-1]))
					if strings.HasPrefix(prev, "#") && strings.Contains(prev, "compose") {
						out = out[:len(out)-1]
					}
				}
				continue // skip the cat > line
			}
		}

		out = append(out, line)
	}

	result := strings.Join(out, "\n")
	// Collapse triple+ blank lines from removals
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}

	return result, count
}

// ---------------------------------------------------------------------------
// SSH key path autofix
// ---------------------------------------------------------------------------

// fixDOSSHKeyPath expands ~ in --public-key-file and swaps to a valid
// key if the specified file doesn't exist.
func fixDOSSHKeyPath(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 3 {
			continue
		}
		if !strings.EqualFold(args[0], "compute") ||
			!strings.EqualFold(args[1], "ssh-key") ||
			!strings.EqualFold(args[2], "import") {
			continue
		}
		for ai, arg := range args {
			if arg != "--public-key-file" || ai+1 >= len(args) {
				continue
			}
			keyPath := args[ai+1]
			// Always expand ~ — doctl doesn't do shell expansion
			expanded := expandHome(keyPath)
			if _, err := os.Stat(expanded); err == nil {
				// File exists, just ensure absolute path (no tilde)
				if expanded != keyPath {
					args[ai+1] = expanded
					count++
				}
				continue
			}
			// File doesn't exist — find an alternative
			alt := findBestLocalPubKey()
			if alt == "" {
				continue
			}
			args[ai+1] = alt
			count++
		}
	}
	return count
}

// expandHome replaces leading ~ with the user home directory.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// findBestLocalPubKey returns the first ~/.ssh/*.pub absolute path.
func findBestLocalPubKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
	if len(matches) > 0 {
		return matches[0] // absolute path — doctl won't expand ~
	}
	return ""
}

// fixDOStripGitClone removes git clone commands from the plan.
// The executor already handles cloning for docker build context.
func fixDOStripGitClone(plan *maker.Plan) int {
	count := 0
	var kept []maker.Command
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 && strings.EqualFold(cmd.Args[0], "git") && strings.EqualFold(cmd.Args[1], "clone") {
			count++
			continue
		}
		kept = append(kept, cmd)
	}
	if count > 0 {
		plan.Commands = kept
	}
	return count
}

// fixDORegistryProducePaths fixes hallucinated produce paths on registry create.
// doctl returns {"name":"..."} (no "endpoint" field); LLMs often emit "registry.name"
// or "registry.endpoint" which don't match the flat JSON structure.
// fixDORegistryEndpointHallucination replaces <REGISTRY_ENDPOINT>/ with
// registry.digitalocean.com/<REGISTRY_NAME>/ in docker build/push args and
// produces values. Also fixes bare "/image:tag" patterns in docker build -t.
// The LLM sometimes invents REGISTRY_ENDPOINT which doctl never produces.
func fixDORegistryEndpointHallucination(plan *maker.Plan) int {
	count := 0
	const repl = "registry.digitalocean.com/<REGISTRY_NAME>/"

	for ci := range plan.Commands {
		args := plan.Commands[ci].Args

		// Fix docker build -t and docker push args
		if isDockerSubcommand(args, "build") || isDockerSubcommand(args, "push") {
			for ai, arg := range args {
				orig := arg
				// <REGISTRY_ENDPOINT>/image:tag → registry.digitalocean.com/<REGISTRY_NAME>/image:tag
				arg = strings.ReplaceAll(arg, "<REGISTRY_ENDPOINT>/", repl)
				// Bare /image:tag (LLM stripped the placeholder entirely)
				if isDockerSubcommand(args, "build") && ai > 0 && args[ai-1] == "-t" {
					if strings.HasPrefix(arg, "/") && strings.Contains(arg, ":") {
						arg = repl + strings.TrimPrefix(arg, "/")
					}
				}
				if arg != orig {
					plan.Commands[ci].Args[ai] = arg
					count++
				}
			}
		}

		// Fix produces values
		for k, v := range plan.Commands[ci].Produces {
			orig := v
			v = strings.ReplaceAll(v, "<REGISTRY_ENDPOINT>/", repl)
			if strings.HasPrefix(v, "/") && strings.Contains(v, ":") {
				v = repl + strings.TrimPrefix(v, "/")
			}
			if v != orig {
				plan.Commands[ci].Produces[k] = v
				count++
			}
		}

		// Fix user-data references
		if isDODropletCreate(args) {
			for ai, arg := range args {
				if arg == "--user-data" && ai+1 < len(args) {
					ud := args[ai+1]
					fixed := strings.ReplaceAll(ud, "<REGISTRY_ENDPOINT>/", repl)
					if fixed != ud {
						plan.Commands[ci].Args[ai+1] = fixed
						count++
					}
				}
			}
		}
	}

	// Also strip any "registry get" command that produces REGISTRY_ENDPOINT
	// (doctl registry get doesn't have server_url or endpoint field)
	var cleaned []maker.Command
	for _, cmd := range plan.Commands {
		hasEndpoint := false
		if len(cmd.Args) >= 2 && strings.EqualFold(cmd.Args[0], "registry") &&
			strings.EqualFold(cmd.Args[1], "get") {
			for k := range cmd.Produces {
				if strings.Contains(strings.ToUpper(k), "ENDPOINT") {
					hasEndpoint = true
					break
				}
			}
		}
		if hasEndpoint {
			count++
			continue // drop this command
		}
		cleaned = append(cleaned, cmd)
	}
	if len(cleaned) < len(plan.Commands) {
		plan.Commands = cleaned
	}

	return count
}

// fixDORegistrySubscriptionTier ensures registry create uses --subscription-tier basic.
// Starter tier (free) has only 500 MiB storage which causes docker push to silently hang
// when the image exceeds the quota.
func fixDORegistrySubscriptionTier(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 2 {
			continue
		}
		if !strings.EqualFold(args[0], "registry") || !strings.EqualFold(args[1], "create") {
			continue
		}
		updated := append([]string(nil), args...)
		hasTier := false
		for ai := 0; ai < len(updated); ai++ {
			if strings.TrimSpace(updated[ai]) != "--subscription-tier" {
				continue
			}
			hasTier = true
			if ai+1 < len(updated) && !strings.EqualFold(strings.TrimSpace(updated[ai+1]), "basic") {
				updated[ai+1] = "basic"
				count++
			}
			break
		}
		if !hasTier {
			updated = append(updated, "--subscription-tier", "basic")
			count++
		}
		plan.Commands[ci].Args = updated
	}
	return count
}

func fixDORegistryProducePaths(plan *maker.Plan) int {
	count := 0
	for ci := range plan.Commands {
		args := plan.Commands[ci].Args
		if len(args) < 2 {
			continue
		}
		if !strings.EqualFold(args[0], "registry") || !strings.EqualFold(args[1], "create") {
			continue
		}
		for k, v := range plan.Commands[ci].Produces {
			upper := strings.ToUpper(k)
			// REGISTRY_NAME: "registry.name" → "name"
			if strings.Contains(upper, "NAME") {
				cleaned := strings.TrimPrefix(v, "registry.")
				if cleaned != v {
					plan.Commands[ci].Produces[k] = cleaned
					count++
				}
			}
			// REGISTRY_ENDPOINT: no such field in doctl output — remove
			// (computed at runtime from REGISTRY_NAME)
			if strings.Contains(upper, "ENDPOINT") {
				delete(plan.Commands[ci].Produces, k)
				count++
			}
		}
	}
	return count
}
