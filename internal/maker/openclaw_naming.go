package maker

import "strings"

func openClawContainerName(bindings map[string]string) string {
	if bindings == nil {
		return "openclaw"
	}
	if v := strings.TrimSpace(bindings["OPENCLAW_CONTAINER_NAME"]); v != "" {
		return v
	}
	deployID := strings.TrimSpace(bindings["DEPLOY_ID"])
	if deployID == "" {
		return "openclaw"
	}
	name := "openclaw-" + deployID
	bindings["OPENCLAW_CONTAINER_NAME"] = name
	return name
}
