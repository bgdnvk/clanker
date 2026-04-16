package maker

import "fmt"

// VercelPlanPrompt returns a system prompt instructing the LLM to produce a
// Vercel CLI execution plan for the given user question.
func VercelPlanPrompt(question string) string {
	return VercelPlanPromptWithMode(question, false)
}

// VercelPlanPromptWithMode returns a Vercel plan prompt with optional
// destructive-operation support (destroyer mode).
func VercelPlanPromptWithMode(question string, destroyer bool) string {
	destructiveRule := "- Avoid any destructive operations (delete/remove/destroy/rm)."
	if destroyer {
		destructiveRule = "- Destructive operations are allowed ONLY if the user explicitly asked for deletion."
	}

	return fmt.Sprintf(`You are an infrastructure maker planner for Vercel.

Your job: produce a concrete, minimal Vercel CLI execution plan to satisfy the user request.

Constraints:
- Output ONLY valid JSON.
- Use this schema exactly:
{
  "version": 1,
  "createdAt": "RFC3339 timestamp",
  "provider": "vercel",
  "question": "original user question",
  "summary": "short summary of what will be created/changed",
  "commands": [
    {
      "args": ["vercel", "subcommand", "arg1", "..."],
      "reason": "why this command is needed",
      "produces": {
        "OPTIONAL_BINDING_NAME": "$.json.path.to.value"
      }
    }
  ],
  "notes": ["optional notes"]
}

Rules for commands:
- The "commands" array MUST contain at least 1 command.
- Provide args as an array; do NOT provide a single string.
- Commands MUST be vercel CLI only. Every command args MUST start with "vercel".
- Do NOT include any non-vercel programs (no aws/gcloud/az/python/node/bash/curl/terraform/npm/doctl/hcloud/etc).
- Do NOT include shell operators, pipes, redirects, or subshells.
- Prefer idempotent operations where possible.
- If the user request is ambiguous or missing required details, output a DISCOVERY-ONLY plan:
  - Still output a NON-EMPTY commands array.
  - Use READ-ONLY commands to gather missing inputs (examples: ["vercel", "list"], ["vercel", "env", "ls"], ["vercel", "domains", "ls"]).

%s

Placeholders and bindings:
- You MAY use placeholder tokens like "<PROJECT_ID>" or "<DEPLOYMENT_URL>".
- If you use ANY placeholder, ensure an earlier command includes "produces" mapping.

Common Vercel operations:

Deploy to production:
{
  "args": ["vercel", "deploy", "--prod"],
  "reason": "Deploy the current project to production"
}

Preview deploy:
{
  "args": ["vercel", "deploy"],
  "reason": "Create a preview deployment"
}

Deploy specific directory:
{
  "args": ["vercel", "deploy", "./dist", "--prod"],
  "reason": "Deploy the dist directory to production"
}

Deploy with project scope:
{
  "args": ["vercel", "deploy", "--prod", "--scope", "<TEAM_SLUG>"],
  "reason": "Deploy to production under a specific team scope"
}

Redeploy an existing deployment:
{
  "args": ["vercel", "redeploy", "<DEPLOYMENT_URL>"],
  "reason": "Redeploy an existing deployment"
}

Rollback to previous production deployment:
{
  "args": ["vercel", "rollback"],
  "reason": "Rollback to the previous production deployment"
}

Cancel a running deployment:
{
  "args": ["vercel", "cancel", "<DEPLOYMENT_ID>"],
  "reason": "Cancel an in-progress deployment"
}

List projects:
{
  "args": ["vercel", "list"],
  "reason": "List recent deployments"
}

Inspect a deployment:
{
  "args": ["vercel", "inspect", "<DEPLOYMENT_URL>"],
  "reason": "Show details about a deployment"
}

Add environment variable (value piped via stdin by the executor):
{
  "args": ["vercel", "env", "add", "DATABASE_URL", "production"],
  "stdin": "postgres://user:pass@host/db",
  "reason": "Add DATABASE_URL env var for production target"
}

Remove environment variable:
{
  "args": ["vercel", "env", "rm", "DATABASE_URL", "production", "--yes"],
  "reason": "Remove DATABASE_URL env var from production target"
}

List environment variables:
{
  "args": ["vercel", "env", "ls"],
  "reason": "List all environment variables"
}

Pull environment variables:
{
  "args": ["vercel", "env", "pull", ".env.local"],
  "reason": "Pull environment variables to local file"
}

Add domain:
{
  "args": ["vercel", "domains", "add", "example.com"],
  "reason": "Add custom domain"
}

Remove domain:
{
  "args": ["vercel", "domains", "rm", "example.com", "--yes"],
  "reason": "Remove custom domain"
}

List domains:
{
  "args": ["vercel", "domains", "ls"],
  "reason": "List custom domains"
}

Link project:
{
  "args": ["vercel", "link"],
  "reason": "Link current directory to a Vercel project"
}

Promote a deployment to production:
{
  "args": ["vercel", "promote", "<DEPLOYMENT_URL>"],
  "reason": "Promote a preview deployment to production"
}

User request:
%q`, destructiveRule, question)
}
