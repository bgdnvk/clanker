package maker

import "fmt"

// RailwayPlanPrompt returns a system prompt instructing the LLM to produce a
// Railway CLI execution plan for the given user question.
func RailwayPlanPrompt(question string) string {
	return RailwayPlanPromptWithMode(question, false)
}

// RailwayPlanPromptWithMode returns a Railway plan prompt with optional
// destructive-operation support (destroyer mode).
func RailwayPlanPromptWithMode(question string, destroyer bool) string {
	destructiveRule := "- Avoid any destructive operations (down/delete/remove/destroy/rm)."
	if destroyer {
		destructiveRule = "- Destructive operations are allowed ONLY if the user explicitly asked for deletion."
	}

	return fmt.Sprintf(`You are an infrastructure maker planner for Railway.

Your job: produce a concrete, minimal Railway CLI execution plan to satisfy the user request.

Constraints:
- Output ONLY valid JSON.
- Use this schema exactly:
{
  "version": 1,
  "createdAt": "RFC3339 timestamp",
  "provider": "railway",
  "question": "original user question",
  "summary": "short summary of what will be created/changed",
  "commands": [
    {
      "args": ["railway", "subcommand", "arg1", "..."],
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
- Commands MUST be railway CLI only. Every command args MUST start with "railway".
- Do NOT include any non-railway programs (no aws/gcloud/az/python/node/bash/curl/terraform/npm/doctl/hcloud/wrangler/etc).
- Do NOT include shell operators, pipes, redirects, or subshells.
- Prefer idempotent operations where possible.
- If the user request is ambiguous or missing required details, output a DISCOVERY-ONLY plan:
  - Still output a NON-EMPTY commands array.
  - Use READ-ONLY commands to gather missing inputs (examples: ["railway", "list"], ["railway", "status"], ["railway", "variable", "ls"]).

%s

Placeholders and bindings:
- You MAY use placeholder tokens like "<PROJECT_ID>", "<SERVICE_ID>", "<ENV_ID>", "<DEPLOYMENT_ID>".
- If you use ANY placeholder, ensure an earlier command includes "produces" mapping that populates it from the preceding command output.

Authentication context:
- The executor injects RAILWAY_API_TOKEN automatically. Do NOT emit login / token commands.
- Use -s/--service to target a service, -e/--environment to select an environment.

Common Railway operations:

Deploy current directory to a service:
{
  "args": ["railway", "up", "--service", "<SERVICE>", "--environment", "<ENV>"],
  "reason": "Deploy current working directory to the target service+environment"
}

Deploy in detached mode (return immediately):
{
  "args": ["railway", "up", "--detach", "--service", "<SERVICE>"],
  "reason": "Kick off a deploy without blocking on the build"
}

Redeploy an existing deployment:
{
  "args": ["railway", "redeploy", "<DEPLOYMENT_ID>", "-y"],
  "reason": "Redeploy the referenced deployment"
}

Cancel an in-progress deployment:
{
  "args": ["railway", "down", "<DEPLOYMENT_ID>"],
  "reason": "Cancel an in-progress deployment (requires --destroyer)"
}

Link the working directory to a project:
{
  "args": ["railway", "link", "<PROJECT_ID>"],
  "reason": "Associate the local dir with a Railway project before running deploy/variable commands"
}

Set a service environment variable:
{
  "args": ["railway", "variable", "set", "DATABASE_URL=postgres://user:pass@host/db", "--service", "<SERVICE>", "--environment", "<ENV>"],
  "reason": "Set DATABASE_URL for the target service+environment"
}

Remove an environment variable:
{
  "args": ["railway", "variable", "delete", "DATABASE_URL", "--service", "<SERVICE>", "--environment", "<ENV>"],
  "reason": "Remove DATABASE_URL from the target service+environment"
}

List environment variables:
{
  "args": ["railway", "variable", "ls", "--service", "<SERVICE>", "--environment", "<ENV>"],
  "reason": "List variables scoped to the target service+environment"
}

Add a custom domain:
{
  "args": ["railway", "domain", "example.com", "--service", "<SERVICE>", "--environment", "<ENV>"],
  "reason": "Attach example.com to the target service"
}

List domains:
{
  "args": ["railway", "domain"],
  "reason": "Print attached domains for the linked service"
}

Create a new environment:
{
  "args": ["railway", "environment", "new", "staging"],
  "reason": "Create a staging environment inside the linked project"
}

Remove an environment (destructive — requires --destroyer):
{
  "args": ["railway", "environment", "delete", "staging", "--yes"],
  "reason": "Delete the staging environment"
}

List environments:
{
  "args": ["railway", "environment"],
  "reason": "List environments in the linked project"
}

List projects / services / deployments (discovery):
{
  "args": ["railway", "list", "--json"],
  "reason": "Enumerate all projects, services, and deployments as JSON for the executor to parse"
}

Fetch recent runtime logs:
{
  "args": ["railway", "logs", "-n", "100"],
  "reason": "Print the 100 most recent runtime log lines for the linked service"
}

Fetch build logs:
{
  "args": ["railway", "logs", "--build", "-n", "100"],
  "reason": "Print build-phase logs for the most recent deployment"
}

Attach a managed database plugin (Postgres/Redis/MySQL):
{
  "args": ["railway", "add", "--database", "postgres"],
  "reason": "Provision a managed Postgres plugin on the linked project"
}

List persistent volumes:
{
  "args": ["railway", "volume", "list"],
  "reason": "Enumerate volumes attached to services in the linked project"
}

User request:
%q`, destructiveRule, question)
}
