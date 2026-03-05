package deploy

import (
	"regexp"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// ApplyGenericPlanAutofix runs provider-agnostic dedup passes that collapse
// redundant launch/terminate cycles the LLM tends to produce when it "fixes"
// user-data or startup scripts by appending new run-instances commands.

// userDataVariantRe matches LLM-creative USER_DATA variant placeholders
// like <USER_DATA_OPENCLAW_DOCKER_COMPOSE>, <USER_DATA_SCRIPT>, etc.
// Does NOT match the canonical <USER_DATA> since we require at least _X suffix.
var userDataVariantRe = regexp.MustCompile(`<USER_DATA_[A-Z0-9_]+>`)

// normalizeUserDataPlaceholders rewrites creative <USER_DATA_*> variants
// to the canonical <USER_DATA> the maker recognizes.
func normalizeUserDataPlaceholders(plan *maker.Plan) int {
	if plan == nil {
		return 0
	}
	count := 0
	for ci := range plan.Commands {
		cmd := &plan.Commands[ci]
		for ai, arg := range cmd.Args {
			if userDataVariantRe.MatchString(arg) {
				replaced := userDataVariantRe.ReplaceAllString(arg, "<USER_DATA>")
				if replaced != arg {
					cmd.Args[ai] = replaced
					count++
				}
			}
		}
	}
	return count
}

// ApplyGenericPlanAutofix runs provider-agnostic dedup/cleanup passes.
// externalBindings are placeholder names provided externally (e.g. user env vars)
// that should NOT be treated as orphaned even though no command produces them.
func ApplyGenericPlanAutofix(plan *maker.Plan, logf func(string, ...any), externalBindings ...string) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// Deterministic user-data fixups (path typos, garbled ECR login, etc.)
	// Run FIRST so downstream validation/repair never sees these issues.
	plan = FixEC2UserDataScripts(plan, logf)

	// Normalize creative USER_DATA variants (e.g. <USER_DATA_OPENCLAW_DOCKER_COMPOSE>)
	// to <USER_DATA> so the maker's placeholder resolution can handle them.
	udFixed := normalizeUserDataPlaceholders(plan)
	if udFixed > 0 {
		logf("[deploy] generic autofix: normalized %d user-data placeholder variant(s)", udFixed)
	}

	removed := pruneRedundantLaunchCycles(plan)
	if removed > 0 {
		logf("[deploy] generic autofix: collapsed %d redundant launch-cycle command(s)", removed)
	}

	// Dedup read-only describe/get commands targeting the same resource.
	roRemoved := pruneRedundantReadOnly(plan)
	if roRemoved > 0 {
		logf("[deploy] generic autofix: removed %d redundant read-only command(s)", roRemoved)
	}

	// Generic SSM semantic dedup (works for any project, not just OpenClaw).
	ssmRemoved := pruneSSMSemanticDuplicatesGeneric(plan)
	if ssmRemoved > 0 {
		logf("[deploy] generic autofix: removed %d redundant SSM command(s)", ssmRemoved)
	}

	// Remove commands referencing placeholders that no command produces.
	orphanRemoved := pruneOrphanedPlaceholderRefs(plan, externalBindings...)
	if orphanRemoved > 0 {
		logf("[deploy] generic autofix: removed %d orphaned-placeholder command(s)", orphanRemoved)
	}

	return plan
}

// pruneRedundantLaunchCycles detects multiple ec2 run-instances (or ecs
// run-task) commands that target the same project and keeps only the LAST
// one — the most refined version with correct user-data. It also removes
// the terminate→wait→deregister chains for the earlier instances whose
// produced IDs are consumed only by cleanup commands.
// Commands are considered "same project" when they share the same AMI or
// the same Name tag value. Launches with different identities are kept.
func pruneRedundantLaunchCycles(plan *maker.Plan) int {
	if len(plan.Commands) < 2 {
		return 0
	}

	// Identify all run-instances indices grouped by launch identity.
	type launchInfo struct {
		idx        int
		producesID string // e.g. INSTANCE_ID, NEW_INSTANCE_ID
		identity   string // AMI + Name tag — scopes the dedup
	}
	var launches []launchInfo
	for i, cmd := range plan.Commands {
		if !isEC2RunInstances(cmd.Args) {
			continue
		}
		idKey := ""
		for k := range cmd.Produces {
			ku := strings.ToUpper(strings.TrimSpace(k))
			if strings.Contains(ku, "INSTANCE") && strings.Contains(ku, "ID") {
				idKey = k
				break
			}
		}
		launches = append(launches, launchInfo{
			idx:        i,
			producesID: idKey,
			identity:   launchIdentity(cmd.Args),
		})
	}
	if len(launches) < 2 {
		return 0
	}

	// Group by identity; only dedup within same identity.
	groups := map[string][]launchInfo{}
	for _, li := range launches {
		groups[li.identity] = append(groups[li.identity], li)
	}

	drop := make(map[int]struct{})
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		// Keep the last run-instances in this group
		keep := group[len(group)-1]
		for _, li := range group[:len(group)-1] {
			drop[li.idx] = struct{}{}
			if li.producesID == "" {
				continue
			}
			placeholder := "<" + li.producesID + ">"
			for j, cmd := range plan.Commands {
				if j == li.idx || j == keep.idx {
					continue
				}
				if _, already := drop[j]; already {
					continue
				}
				if isLaunchLifecycleCommand(cmd.Args) && argsContain(cmd.Args, placeholder) {
					drop[j] = struct{}{}
				}
			}
		}
	}

	if len(drop) == 0 {
		return 0
	}

	filtered := make([]maker.Command, 0, len(plan.Commands)-len(drop))
	for i, cmd := range plan.Commands {
		if _, ok := drop[i]; ok {
			continue
		}
		filtered = append(filtered, cmd)
	}
	plan.Commands = filtered
	return len(drop)
}

// launchIdentity returns a string that identifies "which project" a
// run-instances command is for, based on AMI + Name tag. Two run-instances
// with the same identity are the LLM duplicating itself; different
// identities are separate resources we must preserve.
func launchIdentity(args []string) string {
	ami := ""
	nameTag := ""
	for i := 0; i < len(args)-1; i++ {
		a := strings.TrimSpace(args[i])
		// --image-id ami-xxx
		if a == "--image-id" {
			ami = strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(a, "--image-id=") {
			ami = strings.TrimPrefix(a, "--image-id=")
		}
		// Name tag inside --tag-specifications
		if a == "--tag-specifications" || strings.HasPrefix(a, "--tag-specifications=") {
			spec := a
			if a == "--tag-specifications" && i+1 < len(args) {
				spec = args[i+1]
			}
			lower := strings.ToLower(spec)
			if idx := strings.Index(lower, "key=name"); idx >= 0 {
				// extract Value=xxx
				rest := spec[idx:]
				if vi := strings.Index(strings.ToLower(rest), "value="); vi >= 0 {
					val := rest[vi+6:]
					// trim until comma, }, ] or end
					for _, sep := range []string{",", "}", "]", "'"} {
						if si := strings.Index(val, sep); si >= 0 {
							val = val[:si]
						}
					}
					nameTag = strings.TrimSpace(val)
				}
			}
		}
	}
	// combine for identity key
	return ami + "|" + nameTag
}

// isEC2RunInstances returns true for ec2 run-instances commands.
func isEC2RunInstances(args []string) bool {
	if len(args) < 2 {
		return false
	}
	svc := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))
	return svc == "ec2" && op == "run-instances"
}

// isLaunchLifecycleCommand returns true for commands that are part of an
// instance launch cycle: terminate, wait, deregister, register, describe-status.
func isLaunchLifecycleCommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	svc := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))

	switch {
	case svc == "ec2" && op == "terminate-instances":
		return true
	case svc == "ec2" && op == "wait":
		// instance-running, instance-terminated
		return true
	case svc == "ec2" && op == "describe-instance-status":
		return true
	case svc == "elbv2" && op == "register-targets":
		return true
	case svc == "elbv2" && op == "deregister-targets":
		return true
	case svc == "elbv2" && op == "wait":
		return true
	case svc == "elbv2" && op == "describe-target-health":
		return true
	}
	return false
}

// argsContain checks if any arg contains the given substring.
func argsContain(args []string, sub string) bool {
	for _, a := range args {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Generic SSM semantic dedup
// ---------------------------------------------------------------------------

// classifySSMScriptGeneric returns a generic semantic category for a shell
// script embedded in an SSM command. No project-specific patterns.
func classifySSMScriptGeneric(script string) string {
	if script == "" {
		return ""
	}
	l := strings.ToLower(script)

	hasStart := strings.Contains(l, "docker compose up") || strings.Contains(l, "docker-compose up") || strings.Contains(l, "docker run")
	hasStop := (strings.Contains(l, "docker compose down") || strings.Contains(l, "docker compose stop") || strings.Contains(l, "docker-compose down")) && !hasStart
	hasEnvCreate := (strings.Contains(l, "cat > ") || strings.Contains(l, "cat >") || strings.Contains(l, "> .env")) && strings.Contains(l, ".env") && !strings.Contains(l, ">> .env")
	hasEnvAppend := strings.Contains(l, ">> .env") || (strings.Contains(l, ">>") && strings.Contains(l, ".env"))
	hasEnvWrite := hasEnvCreate || hasEnvAppend
	hasECRPull := strings.Contains(l, "ecr get-login-password") || (strings.Contains(l, "docker pull") && strings.Contains(l, ".dkr.ecr."))
	hasDiag := (strings.Contains(l, "docker logs") || strings.Contains(l, "docker ps") || strings.Contains(l, "curl -s") || strings.Contains(l, "health")) && !hasStart
	hasClone := strings.Contains(l, "git clone")
	hasMkdir := strings.Contains(l, "mkdir -p") && !hasClone && !hasStart && !hasEnvWrite

	// Priority: clone > start > stop > env > ecr > diag > mkdir
	switch {
	case hasClone:
		return "ssm-clone"
	case hasStart:
		return "ssm-service-start"
	case hasStop:
		return "ssm-service-stop"
	case hasEnvCreate && !hasStart:
		return "ssm-env-create"
	case hasEnvAppend && !hasStart:
		return "ssm-env-append"
	case hasECRPull:
		return "ssm-ecr-pull"
	case hasDiag:
		return "ssm-diagnostics"
	case hasMkdir:
		return "ssm-mkdir"
	}
	return ""
}

// classifySSMIntentGeneric classifies an SSM send-command generically.
func classifySSMIntentGeneric(args []string) string {
	if len(args) < 4 {
		return ""
	}
	svc := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))
	if svc != "ssm" || op != "send-command" {
		return ""
	}
	script := extractSSMScriptFromArgs(args)
	return classifySSMScriptGeneric(script)
}

// pruneSSMSemanticDuplicatesGeneric collapses SSM send-command steps that
// repeat the same generic intent targeting the same instance(s).
// Keeps the LAST of each (category + instance-ids) pair.
func pruneSSMSemanticDuplicatesGeneric(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) < 2 {
		return 0
	}

	// dedup key: category + target instance(s)
	type ssmKey struct {
		category    string
		instanceIDs string
	}

	type tagged struct {
		idx int
		key ssmKey
	}

	items := make([]tagged, len(plan.Commands))
	for i, cmd := range plan.Commands {
		cat := classifySSMIntentGeneric(cmd.Args)
		ids := extractSSMInstanceIDs(cmd.Args)
		items[i] = tagged{idx: i, key: ssmKey{category: cat, instanceIDs: ids}}
	}

	lastOfKey := map[ssmKey]int{}
	for _, t := range items {
		if t.key.category != "" {
			lastOfKey[t.key] = t.idx
		}
	}

	filtered := make([]maker.Command, 0, len(plan.Commands))
	removed := 0
	for i, cmd := range plan.Commands {
		k := items[i].key
		if k.category == "" || items[i].idx == lastOfKey[k] {
			filtered = append(filtered, cmd)
		} else {
			removed++
		}
	}
	if removed > 0 {
		plan.Commands = filtered
	}
	return removed
}

// extractSSMInstanceIDs returns the --instance-ids / --targets value from
// an SSM send-command so we can scope dedup per target instance.
func extractSSMInstanceIDs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		a := strings.TrimSpace(args[i])
		if a == "--instance-ids" || a == "--targets" {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(a, "--instance-ids=") {
			return strings.TrimPrefix(a, "--instance-ids=")
		}
		if strings.HasPrefix(a, "--targets=") {
			return strings.TrimPrefix(a, "--targets=")
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Orphaned placeholder pruning
// ---------------------------------------------------------------------------

// orphanPlaceholderRe matches <UPPER_CASE_KEY> placeholders in command args.
var orphanPlaceholderRe = regexp.MustCompile(`<([A-Z][A-Z0-9_]+)>`)

// isCriticalCommand returns true for commands that should never be removed
// by orphan pruning — instead, their bad placeholders are stripped in-place.
func isCriticalCommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	svc := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))

	// AWS: ec2 run-instances
	if svc == "ec2" && op == "run-instances" {
		return true
	}
	// AWS: elbv2 create-load-balancer, create-target-group, create-listener
	if svc == "elbv2" && strings.HasPrefix(op, "create-") {
		return true
	}
	// AWS: cloudfront create-distribution
	if svc == "cloudfront" && op == "create-distribution" {
		return true
	}
	// DO: compute droplet/reserved-ip/firewall create
	if svc == "compute" && (op == "droplet" || op == "firewall" || op == "reserved-ip") {
		if len(args) >= 3 && strings.ToLower(strings.TrimSpace(args[2])) == "create" {
			return true
		}
	}
	// DO: registry create, apps create, kubernetes cluster create
	if svc == "registry" && op == "create" {
		return true
	}
	if svc == "apps" && op == "create" {
		return true
	}
	if svc == "kubernetes" && op == "cluster" {
		if len(args) >= 3 && strings.ToLower(strings.TrimSpace(args[2])) == "create" {
			return true
		}
	}
	// Docker build/push are critical for image-based deploys
	if svc == "docker" && (op == "build" || op == "push") {
		return true
	}
	// GCP: compute instances create, run deploy
	if svc == "compute" && op == "instances" {
		return true
	}
	if svc == "run" && op == "deploy" {
		return true
	}
	// Azure: vm create, containerapp create
	if (svc == "vm" || svc == "containerapp" || svc == "aks") && op == "create" {
		return true
	}
	// Hetzner: server create
	if svc == "server" && op == "create" {
		return true
	}
	return false
}

// pruneOrphanedPlaceholderRefs removes commands that reference a <KEY>
// placeholder where no command in the plan produces that key. Cascades:
// if a dropped command itself produces something, dependents are also dropped.
// Capped at 25% of the plan to prevent cascade avalanche from one bad ref.
// Critical commands (run-instances, create-load-balancer, etc.) are NEVER
// removed — their orphan placeholders are stripped to empty string instead.
// externalBindings are placeholder names that are injected at execution time
// (e.g. user-provided env vars like ANTHROPIC_API_KEY) — treat them as produced.
func pruneOrphanedPlaceholderRefs(plan *maker.Plan, externalBindings ...string) int {
	if plan == nil || len(plan.Commands) < 2 {
		return 0
	}

	maxDrop := len(plan.Commands) / 4 // never remove more than 25%
	if maxDrop < 1 {
		maxDrop = 1
	}

	// Build set of externally-provided names (user env vars etc.)
	external := map[string]bool{}
	for _, names := range externalBindings {
		for _, n := range strings.Split(names, ",") {
			n = strings.ToUpper(strings.TrimSpace(n))
			if n != "" {
				external[n] = true
			}
		}
	}

	drop := map[int]bool{}

	for changed := true; changed; {
		changed = false
		if len(drop) >= maxDrop {
			break // hit cap, stop cascading
		}
		// Rebuild produced set excluding dropped commands
		produced := map[string]bool{}
		for k := range external {
			produced[k] = true // user-provided bindings are always "produced"
		}
		for i, cmd := range plan.Commands {
			if drop[i] {
				continue
			}
			for k := range cmd.Produces {
				produced[strings.TrimSpace(k)] = true
			}
		}

		for i, cmd := range plan.Commands {
			if drop[i] {
				continue
			}
			if len(drop) >= maxDrop {
				break // cap reached mid-iteration
			}

			// For critical commands: strip orphan placeholders instead of removing
			if isCriticalCommand(cmd.Args) {
				for ai, arg := range cmd.Args {
					matches := orphanPlaceholderRe.FindAllStringSubmatch(arg, -1)
					for _, m := range matches {
						if !produced[m[1]] {
							// Strip the orphan placeholder so downstream resolution can fill it
							cmd.Args[ai] = strings.ReplaceAll(cmd.Args[ai], "<"+m[1]+">", "")
						}
					}
				}
				plan.Commands[i] = cmd
				continue
			}

			for _, arg := range cmd.Args {
				matches := orphanPlaceholderRe.FindAllStringSubmatch(arg, -1)
				for _, m := range matches {
					if !produced[m[1]] {
						drop[i] = true
						changed = true
						break
					}
				}
				if drop[i] {
					break
				}
			}
		}
	}

	if len(drop) == 0 {
		return 0
	}

	filtered := make([]maker.Command, 0, len(plan.Commands)-len(drop))
	for i, cmd := range plan.Commands {
		if !drop[i] {
			filtered = append(filtered, cmd)
		}
	}
	plan.Commands = filtered
	return len(drop)
}

// ---------------------------------------------------------------------------
// Read-only command dedup
// ---------------------------------------------------------------------------

// pruneRedundantReadOnly deduplicates read-only commands (describe-*, get-*)
// that target the same resource. Keeps only the last occurrence per
// {service, operation, target} group. Skips commands with produces.
func pruneRedundantReadOnly(plan *maker.Plan) int {
	if plan == nil || len(plan.Commands) < 2 {
		return 0
	}

	type roKey struct {
		service string
		op      string
		target  string
	}

	isReadOnlyOp := func(op string) bool {
		return strings.HasPrefix(op, "describe-") ||
			strings.HasPrefix(op, "get-") ||
			strings.HasPrefix(op, "list-")
	}

	// Extract primary target resource from args.
	primaryTarget := func(args []string) string {
		targets := []string{"--instance-ids", "--id", "--target-group-arn",
			"--names", "--load-balancer-arn", "--load-balancer-arns"}
		for i := 0; i < len(args)-1; i++ {
			flag := strings.TrimSpace(args[i])
			for _, tf := range targets {
				if strings.EqualFold(flag, tf) {
					return strings.TrimSpace(args[i+1])
				}
			}
		}
		return ""
	}

	groups := map[roKey][]int{}
	for i, cmd := range plan.Commands {
		if len(cmd.Args) < 2 {
			continue
		}
		svc := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
		op := strings.ToLower(strings.TrimSpace(cmd.Args[1]))
		if !isReadOnlyOp(op) {
			continue
		}
		// Skip commands that produce values needed downstream
		if len(cmd.Produces) > 0 {
			continue
		}
		target := primaryTarget(cmd.Args)
		key := roKey{svc, op, target}
		groups[key] = append(groups[key], i)
	}

	drop := map[int]bool{}
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		// Keep only the last occurrence
		for _, idx := range indices[:len(indices)-1] {
			drop[idx] = true
		}
	}

	if len(drop) == 0 {
		return 0
	}

	filtered := make([]maker.Command, 0, len(plan.Commands)-len(drop))
	for i, cmd := range plan.Commands {
		if !drop[i] {
			filtered = append(filtered, cmd)
		}
	}
	plan.Commands = filtered
	return len(drop)
}
