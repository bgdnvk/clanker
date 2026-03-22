package deploy

import "strings"

func isDODropletCreate(args []string) bool {
	if len(args) < 3 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "compute") &&
		strings.EqualFold(strings.TrimSpace(args[1]), "droplet") &&
		strings.EqualFold(strings.TrimSpace(args[2]), "create")
}

func countDOFlagOccurrences(args []string, name string) int {
	count := 0
	for i := 0; i < len(args); i++ {
		trimmed := strings.TrimSpace(args[i])
		if trimmed == name || strings.HasPrefix(trimmed, name+"=") {
			count++
		}
	}
	return count
}

// extractDoctlUserDataScript extracts the --user-data value from doctl args.
func extractDoctlUserDataScript(args []string) string {
	for i, a := range args {
		trimmed := strings.TrimSpace(a)
		if strings.EqualFold(trimmed, "--user-data") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "--user-data=") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "--user-data="))
		}
	}
	return ""
}
