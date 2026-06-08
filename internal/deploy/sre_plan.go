package deploy

import (
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// IsSREObserverPlan identifies the one-click Clanker SRE observer path.
// Keep the signals specific so ordinary app plans that mention "SRE" in prose
// do not lose normal app-deploy fixups.
func IsSREObserverPlan(plan *maker.Plan) bool {
	if plan == nil {
		return false
	}
	if containsSREObserverSignal(plan.Question) || containsSREObserverSignal(plan.Summary) || containsSREObserverSignal(strings.Join(plan.Notes, " ")) {
		return true
	}
	for _, cmd := range plan.Commands {
		if containsSREObserverSignal(cmd.Reason) || containsSREObserverSignal(strings.Join(cmd.Args, " ")) {
			return true
		}
	}
	return false
}

func containsSREObserverSignal(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "clanker sre run") ||
		strings.Contains(lower, "one-click sre deploy") ||
		strings.Contains(lower, "clanker_sre_") ||
		strings.Contains(lower, "clanker-sre") ||
		strings.Contains(lower, "clanker sre observer")
}
