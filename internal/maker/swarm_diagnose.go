package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
)

type swarmResult struct {
	name string
	text string
	err  error
}

func maybeSwarmDiagnose(ctx context.Context, opts ExecOptions, summary string, failingArgs []string, failingOutput string, bindings map[string]string) error {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return nil
	}
	if opts.Writer == nil {
		return nil
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "deploy diagnosis"
	}

	// Keep context tight: only include the keys that matter for runtime + ALB health.
	ctxBindings := map[string]string{}
	for _, k := range []string{
		"PLAN_QUESTION",
		"DEPLOY_ID",
		"ACCOUNT_ID",
		"AWS_ACCOUNT_ID",
		"REGION",
		"AWS_REGION",
		"INSTANCE_ID",
		"TG_ARN",
		"ALB_DNS",
		"APP_PORT",
		"ECR_URI",
		"IMAGE_URI",
		"IMAGE_TAG",
		"START_COMMAND",
	} {
		if v := strings.TrimSpace(bindings[k]); v != "" {
			ctxBindings[k] = v
		}
	}
	bindingsJSON, _ := json.MarshalIndent(ctxBindings, "", "  ")

	trimmedArgs := stripAWSRuntimeFlags(failingArgs)
	trimmedOut := strings.TrimSpace(failingOutput)
	if len(trimmedOut) > 2000 {
		trimmedOut = trimmedOut[:2000] + "…"
	}

	basePrompt := fmt.Sprintf(`Context: You are helping debug an AWS one-click deploy that is stuck or failing.

Summary: %s

Failing command args (no injected flags): %q

Failure output (truncated):
%s

Known bindings (selected):
%s

Rules:
- Be concrete and actionable.
- Prefer the minimal root cause.
- If you suggest a command, show it.
- Assume EC2 + Docker + ALB target group health checks.
`, summary, trimmedArgs, trimmedOut, string(bindingsJSON))

	prompts := []struct {
		name   string
		prompt string
	}{
		{
			name:   "runtime",
			prompt: basePrompt + "\nTask: Determine what is missing on the EC2 instance/container for the ALB health check to pass. Focus on Docker install/start, container running, port binding (0.0.0.0 vs 127.0.0.1), and persistent volume mounts. Output 3-8 bullets.\n",
		},
		{
			name:   "aws",
			prompt: basePrompt + "\nTask: Determine AWS-side causes for ALB target health check failures (SGs, target group port/path, listener/forwarding, instance reachability). Output 3-8 bullets.\n",
		},
		{
			name:   "pipeline",
			prompt: basePrompt + "\nTask: Determine clanker/maker pipeline fixes that would prevent this from getting stuck again (e.g., user-data placeholder handling, when to run SSM remediation, retries). Output 3-8 bullets.\n",
		},
	}

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)

	swarmCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	results := make([]swarmResult, len(prompts))
	var wg sync.WaitGroup
	wg.Add(len(prompts))
	for i := range prompts {
		i := i
		go func() {
			defer wg.Done()
			resp, err := client.AskPrompt(swarmCtx, prompts[i].prompt)
			results[i] = swarmResult{name: prompts[i].name, text: strings.TrimSpace(resp), err: err}
		}()
	}
	wg.Wait()

	_, _ = fmt.Fprintf(opts.Writer, "[swarm] diagnosis: %s\n", summary)
	for _, r := range results {
		if r.err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[swarm][%s] error: %v\n", r.name, r.err)
			continue
		}
		if strings.TrimSpace(r.text) == "" {
			continue
		}
		_, _ = fmt.Fprintf(opts.Writer, "[swarm][%s]\n%s\n", r.name, r.text)
	}

	return nil
}
