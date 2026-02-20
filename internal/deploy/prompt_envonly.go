package deploy

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{1,127}$`)

// PromptForEnvVarValues prompts the user for values for the given env var names.
// It is used as a fallback when DeepAnalysis does not provide requiredEnvVars.
func PromptForEnvVarValues(names []string) (map[string]string, error) {
	clean := make([]string, 0, len(names))
	seen := make(map[string]struct{})
	for _, n := range names {
		n = strings.TrimSpace(strings.ToUpper(n))
		if n == "" {
			continue
		}
		if !strings.Contains(n, "_") {
			continue
		}
		if !envNameRe.MatchString(n) {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		clean = append(clean, n)
	}
	sort.Strings(clean)
	if len(clean) == 0 {
		return map[string]string{}, nil
	}

	reader := bufio.NewReader(os.Stdin)
	out := make(map[string]string)
	fmt.Fprintf(os.Stderr, "\n[deploy] Required configuration:\n")
	for _, name := range clean {
		for {
			fmt.Fprintf(os.Stderr, "\n  %s\n  Enter value: ", name)
			value, err := reader.ReadString('\n')
			if err != nil {
				return nil, fmt.Errorf("failed to read input: %w", err)
			}
			value = strings.TrimSpace(value)
			if value == "" {
				fmt.Fprintf(os.Stderr, "  [!] This value is required\n")
				continue
			}
			out[name] = value
			break
		}
	}
	return out, nil
}
