package maker

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const CurrentPlanVersion = 1

type Plan struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
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
		return nil, err
	}

	if p.Version == 0 {
		p.Version = CurrentPlanVersion
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

func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		out = append(out, a)
	}

	if len(out) > 0 && strings.EqualFold(out[0], "aws") {
		out = out[1:]
	}

	return out
}
