package deploy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// PlanPage is a paginated chunk of commands used to build a full maker.Plan incrementally.
// It is intentionally small to avoid LLM output truncation.
type PlanPage struct {
	Done     bool            `json:"done"`
	Commands []maker.Command `json:"commands"`
	Summary  string          `json:"summary,omitempty"`
	Notes    []string        `json:"notes,omitempty"`
}

func ParsePlanPage(cleanedJSON string) (*PlanPage, error) {
	trimmed := strings.TrimSpace(cleanedJSON)
	if trimmed == "" {
		return nil, fmt.Errorf("empty response")
	}
	var page PlanPage
	if err := json.Unmarshal([]byte(trimmed), &page); err != nil {
		// Common fallback: model returns a plain array of commands.
		var cmds []maker.Command
		if err2 := json.Unmarshal([]byte(trimmed), &cmds); err2 == nil {
			if len(cmds) == 0 {
				return nil, fmt.Errorf("page has no commands")
			}
			return &PlanPage{Done: false, Commands: cmds}, nil
		}
		return nil, err
	}
	// Allow an empty commands array only if done=true (means: nothing more to add).
	if len(page.Commands) == 0 && !page.Done {
		return nil, fmt.Errorf("page has no commands")
	}
	return &page, nil
}

func AppendPlanPage(plan *maker.Plan, page *PlanPage) int {
	if plan == nil || page == nil {
		return 0
	}
	if len(page.Commands) == 0 {
		return 0
	}

	seen := make(map[string]struct{}, len(plan.Commands))
	for _, cmd := range plan.Commands {
		seen[commandKey(cmd.Args)] = struct{}{}
	}

	added := 0
	for _, cmd := range page.Commands {
		key := commandKey(cmd.Args)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		plan.Commands = append(plan.Commands, cmd)
		added++
	}

	if strings.TrimSpace(plan.Summary) == "" && strings.TrimSpace(page.Summary) != "" {
		plan.Summary = strings.TrimSpace(page.Summary)
	}
	for _, n := range page.Notes {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		plan.Notes = append(plan.Notes, n)
	}

	return added
}

func BuildPlanPagePrompt(provider string, enrichedPrompt string, currentPlan *maker.Plan, requiredLaunchOps []string, mustFixIssues []string, maxCommands int) string {
	if maxCommands <= 0 {
		maxCommands = 8
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "aws"
	}

	// Keep the incremental context compact.
	cmdCount := 0
	var tail []string
	produces := make([]string, 0, 16)
	if currentPlan != nil {
		cmdCount = len(currentPlan.Commands)
		start := cmdCount - 6
		if start < 0 {
			start = 0
		}
		for i := start; i < cmdCount; i++ {
			c := currentPlan.Commands[i]
			if len(c.Args) == 0 {
				continue
			}
			tail = append(tail, fmt.Sprintf("%d) %s", i+1, strings.Join(c.Args, " ")))
			for k := range c.Produces {
				k = strings.TrimSpace(k)
				if k != "" {
					produces = append(produces, k)
				}
			}
		}
	}
	produces = uniqueStrings(produces)

	var b strings.Builder
	// Provider-specific guard rails.
	switch provider {
	case "cloudflare":
		b.WriteString("You are generating a Cloudflare deployment command plan in small pages.\n")
		b.WriteString("Only use Cloudflare tooling: wrangler or cloudflared, or Cloudflare API tuples like [\"GET\",\"/zones\"].\n")
		b.WriteString("Do NOT use npx.\n\n")
	case "gcp":
		b.WriteString("You are generating a GCP deployment command plan in small pages.\n")
		b.WriteString("Use gcloud commands; args may start with 'gcloud' or directly with the group (e.g. 'run', 'compute').\n\n")
	case "azure":
		b.WriteString("You are generating an Azure deployment command plan in small pages.\n")
		b.WriteString("Use az commands; args may start with 'az' or directly with the group (e.g. 'vm', 'containerapp').\n\n")
	default:
		b.WriteString("You are generating an AWS deployment command plan in small pages.\n")
		b.WriteString("Use AWS CLI command args WITHOUT the leading 'aws' program name (start with the service, e.g. ['ec2','run-instances',...]).\n\n")
	}

	b.WriteString("Return JSON ONLY. No markdown, no prose.\n\n")
	b.WriteString("Output schema:\n")
	b.WriteString("{\n")
	b.WriteString("  \"done\": boolean,\n")
	b.WriteString("  \"summary\": string (optional),\n")
	b.WriteString("  \"notes\": [string] (optional),\n")
	b.WriteString("  \"commands\": [\n")
	b.WriteString("    { \"args\": [string,...], \"reason\": string, \"produces\": {string:string}? }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")

	b.WriteString(fmt.Sprintf("Generate the NEXT up to %d commands to CONTINUE the plan.\n", maxCommands))
	b.WriteString("Do not repeat existing commands.\n")
	if len(mustFixIssues) > 0 {
		b.WriteString("IMPORTANT: You are NOT allowed to finish yet. HARD issues remain.\n")
		b.WriteString("- Set done=false.\n")
		b.WriteString("- commands MUST be a NON-EMPTY array that addresses the issues.\n\n")
	} else {
		b.WriteString("If the plan is already complete, output done=true and commands=[].\n\n")
	}

	b.WriteString("Current plan state:\n")
	b.WriteString(fmt.Sprintf("- existingCommands: %d\n", cmdCount))
	if len(tail) > 0 {
		b.WriteString("- lastCommands:\n")
		for _, t := range tail {
			b.WriteString("  - " + t + "\n")
		}
	}
	if len(produces) > 0 {
		b.WriteString("- producedBindings: " + strings.Join(produces, ", ") + "\n")
	}
	b.WriteString("\n")

	if len(requiredLaunchOps) > 0 {
		b.WriteString("This deployment method REQUIRES at least one of these launch operations somewhere in the final plan:\n")
		for _, op := range requiredLaunchOps {
			op = strings.TrimSpace(op)
			if op != "" {
				b.WriteString("- " + op + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(mustFixIssues) > 0 {
		b.WriteString("You MUST address these HARD issues in the next commands you generate (or by reaching the missing step):\n")
		max := 10
		if len(mustFixIssues) < max {
			max = len(mustFixIssues)
		}
		for i := 0; i < max; i++ {
			b.WriteString("- " + strings.TrimSpace(mustFixIssues[i]) + "\n")
		}
		b.WriteString("\n")
	}

	// The full intelligence context.
	b.WriteString("Deployment context:\n")
	b.WriteString(strings.TrimSpace(enrichedPrompt))
	b.WriteString("\n")

	return b.String()
}

func commandKey(args []string) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		parts = append(parts, a)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\x00")
}
