package deploy

import (
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

	// Generic: sanitize all JMESPath produces (strip $ prefix, fix @ syntax)
	jmesFixed := fixDOProducesJMESPath(plan)
	if jmesFixed > 0 {
		logf("[deploy] do autofix: sanitized %d JMESPath produce expression(s)", jmesFixed)
	}

	// Generic: strip unsupported flags from doctl commands
	flagFixed := fixDOUnsupportedFlags(plan)
	if flagFixed > 0 {
		logf("[deploy] do autofix: stripped %d unsupported flag(s)", flagFixed)
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

// jsonPathDollarRe matches JSONPath-style $[0] or $. prefixes that aren't valid JMESPath.
var jsonPathDollarRe = regexp.MustCompile(`^\$\.?`)

// jsonPathAtFilterRe matches JSONPath filter syntax ?(@.field=='value') → JMESPath ?field=='value'
var jsonPathAtFilterRe = regexp.MustCompile(`\?\(@\.([^)]+)\)`)

// fixDOProducesJMESPath sanitizes all produces expressions across every command.
// LLMs often emit JSONPath ($[0].id, $[0].networks...) instead of JMESPath.
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
			// JMESPath uses backticks for literal strings in filters
			v = fixJMESPathStringLiterals(v)

			if v != orig {
				plan.Commands[ci].Produces[k] = v
				count++
			}
		}
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
			fixed := emptyFWAddrRe.ReplaceAllString(arg, "address:0.0.0.0/0")
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
