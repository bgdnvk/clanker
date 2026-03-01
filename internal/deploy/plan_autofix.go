package deploy

import (
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// ApplyGenericPlanAutofix runs provider-agnostic dedup passes that collapse
// redundant launch/terminate cycles the LLM tends to produce when it "fixes"
// user-data or startup scripts by appending new run-instances commands.
func ApplyGenericPlanAutofix(plan *maker.Plan, logf func(string, ...any)) *maker.Plan {
	if plan == nil || len(plan.Commands) == 0 {
		return plan
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	removed := pruneRedundantLaunchCycles(plan)
	if removed > 0 {
		logf("[deploy] generic autofix: collapsed %d redundant launch-cycle command(s)", removed)
	}

	return plan
}

// pruneRedundantLaunchCycles detects multiple ec2 run-instances (or ecs
// run-task) commands that target the same project and keeps only the LAST
// one — the most refined version with correct user-data. It also removes
// the terminate→wait→deregister chains for the earlier instances whose
// produced IDs are consumed only by cleanup commands.
func pruneRedundantLaunchCycles(plan *maker.Plan) int {
	if len(plan.Commands) < 2 {
		return 0
	}

	// Identify all run-instances indices and the placeholder name each produces.
	type launchInfo struct {
		idx        int
		producesID string // e.g. INSTANCE_ID, NEW_INSTANCE_ID, FIXED_INSTANCE_ID
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
		launches = append(launches, launchInfo{idx: i, producesID: idKey})
	}
	if len(launches) < 2 {
		return 0
	}

	// Keep only the LAST run-instances. Mark earlier ones + their lifecycle
	// commands (terminate, wait terminated, deregister for that instance ID)
	// for removal.
	keep := launches[len(launches)-1]
	drop := make(map[int]struct{})

	for _, li := range launches[:len(launches)-1] {
		drop[li.idx] = struct{}{}
		if li.producesID == "" {
			continue
		}
		// Find commands that only reference this instance ID placeholder
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
