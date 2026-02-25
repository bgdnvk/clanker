package maker

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var dollarPlaceholderRe = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

const CurrentPlanVersion = 1

type Plan struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	Provider  string    `json:"provider,omitempty"`
	Question  string    `json:"question"`
	Summary   string    `json:"summary"`
	Commands  []Command `json:"commands"`
	Notes     []string  `json:"notes,omitempty"`
}

type Command struct {
	Args     []string          `json:"args"`
	Reason   string            `json:"reason,omitempty"`
	Produces map[string]string `json:"produces,omitempty"`
}

func ParsePlan(raw string) (*Plan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty plan")
	}

	var p Plan
	if err := json.Unmarshal([]byte(trimmed), &p); err != nil {
		// Try accepting alternative shapes the LLM sometimes returns.
		// 1) A single command object
		// 2) An array of command objects
		wrapped, wrapErr := parsePlanFromAlternativeShapes(trimmed)
		if wrapErr == nil {
			p = *wrapped
		} else {
			return nil, err
		}
	}

	// If JSON unmarshalling succeeded but commands are missing, we still might have received a
	// command-shaped object (unknown fields ignored by json.Unmarshal into Plan).
	if len(p.Commands) == 0 {
		wrapped, wrapErr := parsePlanFromAlternativeShapes(trimmed)
		if wrapErr == nil {
			p = *wrapped
		}
	}

	if p.Version == 0 {
		p.Version = CurrentPlanVersion
	}

	if strings.TrimSpace(p.Provider) == "" {
		p.Provider = "aws"
	}

	if len(p.Commands) == 0 {
		return nil, fmt.Errorf("plan has no commands")
	}

	for i := range p.Commands {
		p.Commands[i].Args = normalizeArgs(p.Commands[i].Args)
		if len(p.Commands[i].Args) == 0 {
			return nil, fmt.Errorf("command %d has empty args", i)
		}
		if len(p.Commands[i].Produces) == 0 {
			p.Commands[i].Produces = nil
		}
	}

	return &p, nil
}

func parsePlanFromAlternativeShapes(trimmed string) (*Plan, error) {
	// 1) Single command object
	{
		var cmd Command
		if err := json.Unmarshal([]byte(trimmed), &cmd); err == nil {
			cmd.Args = normalizeArgs(cmd.Args)
			if len(cmd.Args) > 0 {
				out := &Plan{
					Version:   CurrentPlanVersion,
					CreatedAt: time.Now().UTC(),
					Provider:  "aws",
					Question:  "",
					Summary:   "generated plan",
					Commands:  []Command{cmd},
				}
				return out, nil
			}
		}
	}

	// 2) Array of command objects
	{
		var cmds []Command
		if err := json.Unmarshal([]byte(trimmed), &cmds); err == nil {
			filtered := make([]Command, 0, len(cmds))
			for i := range cmds {
				cmds[i].Args = normalizeArgs(cmds[i].Args)
				if len(cmds[i].Args) == 0 {
					continue
				}
				if len(cmds[i].Produces) == 0 {
					cmds[i].Produces = nil
				}
				filtered = append(filtered, cmds[i])
			}
			if len(filtered) > 0 {
				out := &Plan{
					Version:   CurrentPlanVersion,
					CreatedAt: time.Now().UTC(),
					Provider:  "aws",
					Question:  "",
					Summary:   "generated plan",
					Commands:  filtered,
				}
				return out, nil
			}
		}
	}

	return nil, fmt.Errorf("unrecognized plan shape")
}

func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		a = normalizePlaceholderSyntaxArg(a)
		out = append(out, a)
	}

	if len(out) > 0 {
		switch {
		case strings.EqualFold(out[0], "aws"):
			out = out[1:]
		case strings.EqualFold(out[0], "gcloud"):
			out = out[1:]
		case strings.EqualFold(out[0], "az"):
			out = out[1:]
		}
	}

	return out
}

func normalizePlaceholderSyntaxArg(arg string) string {
	v := strings.TrimSpace(arg)
	if v == "" {
		return v
	}
	if strings.Contains(v, "\n") || strings.HasPrefix(v, "#!") || strings.HasPrefix(strings.ToLower(v), "#cloud-config") {
		return v
	}
	if !strings.Contains(v, "${") {
		return v
	}
	return dollarPlaceholderRe.ReplaceAllString(v, "<$1>")
}
