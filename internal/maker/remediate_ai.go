package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
)

type remediationPlan struct {
	Commands []Command `json:"commands"`
	Notes    []string  `json:"notes,omitempty"`
}

func remediationPrompt(destroyer bool, deploymentIntent string, failedAWSArgs []string, combinedOutput string) string {
	// failedAWSArgs includes injected flags; we strip those in the prompt for clarity.
	trimmed := make([]string, 0, len(failedAWSArgs))
	for i := 0; i < len(failedAWSArgs); i++ {
		if failedAWSArgs[i] == "--profile" || failedAWSArgs[i] == "--region" || failedAWSArgs[i] == "--no-cli-pager" {
			i++
			continue
		}
		trimmed = append(trimmed, failedAWSArgs[i])
	}

	return fmt.Sprintf(`You are an AWS CLI remediation planner.

Task: propose a minimal set of additional AWS CLI commands (args arrays only) that will make the failing command succeed.

Deployment intent (preserve this objective while fixing):
%q

Constraints:
- Output ONLY valid JSON.
- Schema:
{
  "commands": [
    { "args": ["<service>", "<operation>", "..."], "reason": "why" }
  ],
  "notes": ["optional"]
}
- Commands MUST be AWS CLI subcommands only (NO leading "aws").
- Do NOT include shell operators.
- Do NOT include --profile/--region/--no-cli-pager.
- Prefer READ (describe/list/get) commands first if needed.
- Destructive commands are allowed ONLY if destroyer mode is enabled.
- Only propose destructive commands if the failure is clearly caused by dependencies that must be removed.

Destroyer mode enabled: %t

Failing command args:
%q

Failure output (combined stdout/stderr):
%q
`, deploymentIntent, destroyer, trimmed, strings.TrimSpace(combinedOutput))
}

func maybeRemediateWithAI(ctx context.Context, opts ExecOptions, deploymentIntent string, failedArgs []string, failedOutput string) (*remediationPlan, error) {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return nil, nil
	}

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	resp, err := client.AskPrompt(ctx, remediationPrompt(opts.Destroyer, deploymentIntent, failedArgs, failedOutput))
	if err != nil {
		return nil, err
	}

	cleaned := client.CleanJSONResponse(resp)
	var parsed remediationPlan
	if err := json.Unmarshal([]byte(strings.TrimSpace(cleaned)), &parsed); err != nil {
		return nil, err
	}

	for i := range parsed.Commands {
		parsed.Commands[i].Args = normalizeArgs(parsed.Commands[i].Args)
	}

	if len(parsed.Commands) == 0 {
		return nil, nil
	}

	return &parsed, nil
}
