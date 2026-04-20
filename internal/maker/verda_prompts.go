package maker

import "fmt"

// VerdaPlanPrompt returns the Verda planner prompt without destroyer mode.
func VerdaPlanPrompt(question string) string {
	return VerdaPlanPromptWithMode(question, false)
}

// VerdaPlanPromptWithMode returns a Verda (ex-DataCrunch) maker plan prompt.
// Unlike Vercel/Cloudflare whose maker plans shell out to a CLI, Verda plans
// use a `verda-api` verb that the executor maps directly to REST calls via
// the verda.Client. This avoids a hard dependency on the `verda` binary for
// plan execution and reuses the OAuth2 token caching + retry behaviour we
// already trust.
func VerdaPlanPromptWithMode(question string, destroyer bool) string {
	destructiveRule := "- Avoid any destructive operations (delete/discontinue/force_shutdown/delete_stuck)."
	if destroyer {
		destructiveRule = "- Destructive operations are allowed ONLY if the user explicitly asked for deletion."
	}

	return fmt.Sprintf(`You are an infrastructure maker planner for Verda Cloud (ex-DataCrunch), a European GPU/AI cloud.

Your job: produce a concrete, minimal Verda REST API execution plan to satisfy the user request.

Constraints:
- Output ONLY valid JSON.
- Use this schema exactly:
{
  "version": 1,
  "createdAt": "RFC3339 timestamp",
  "provider": "verda",
  "question": "original user question",
  "summary": "short summary of what will be created/changed",
  "commands": [
    {
      "args": ["verda-api", "METHOD", "/v1/path", "{json-body-or-empty-string}"],
      "reason": "why this command is needed",
      "produces": {
        "OPTIONAL_BINDING_NAME": "$.id"
      }
    }
  ],
  "notes": ["optional notes"]
}

Command verbs:
- Use "verda-api" as the first arg for REST calls. Executor calls
  verda.Client.RunAPIWithContext(ctx, METHOD, path, body) which handles
  OAuth2 token acquisition, 429 backoff, and error decoding.
- METHOD is one of: GET, POST, PUT, PATCH, DELETE.
- The path must start with /v1/.
- The body is a JSON string (use "" for GET/DELETE without body).
- Every command args MUST start with "verda-api".
- Do NOT include any non-verda programs (no aws/gcloud/az/curl/terraform/etc).
- Do NOT include shell operators, pipes, redirects, or subshells.
- Prefer idempotent operations where possible.

Rules for commands:
- The "commands" array MUST contain at least 1 command.
- Provide args as an array of exactly 3 or 4 strings: [verb, method, path, body?].
- If the user request is ambiguous or missing required details, output a DISCOVERY-ONLY plan:
  - Still output a NON-EMPTY commands array.
  - Use READ-ONLY GET commands to gather missing inputs (examples:
    ["verda-api", "GET", "/v1/instance-types", ""],
    ["verda-api", "GET", "/v1/locations", ""],
    ["verda-api", "GET", "/v1/ssh-keys", ""]).

%s

Placeholders and bindings:
- You MAY use placeholder tokens like "<INSTANCE_ID>" or "<SSH_KEY_ID>".
- If you use ANY placeholder, ensure an earlier command includes "produces" mapping the field.
- JSONPath for Verda responses: top-level arrays return [{id, ...}], details return {id, ...}. Use "$.id" for single-resource creates, "$[0].id" for first-in-array.

Important Verda surface knowledge:
- Creation calls (POST /v1/instances, /v1/clusters, /v1/volumes) require "location_code" (e.g. "FIN-01", "ICE-01"). Query GET /v1/locations if unknown.
- Instance lifecycle actions go to PUT /v1/instances with body {"action": "start|shutdown|delete|hibernate|...", "id": "<uuid>"}.
  Valid actions: boot, start, shutdown, force_shutdown, delete, discontinue, hibernate, configure_spot, delete_stuck, deploy, transfer.
- Cluster actions go to PUT /v1/clusters with body {"action": "discontinue", "id": "<uuid>"}.
- Volume creation needs a "type" enum (HDD, NVMe, HDD_Shared, NVMe_Shared, NVMe_Local_Storage, NVMe_Shared_Cluster, NVMe_OS_Cluster) plus size in GB.
- Cluster creation requires a shared_volume {name, size} block even if small.
- Serverless: POST /v1/container-deployments and /v1/job-deployments.

Common Verda operations:

Create a single GPU instance:
{
  "args": ["verda-api", "POST", "/v1/instances", "{\"instance_type\":\"1H100.80S.22V\",\"image\":\"ubuntu-22.04-cuda-12.4-docker\",\"hostname\":\"gpu-1\",\"description\":\"training box\",\"location_code\":\"FIN-01\",\"ssh_key_ids\":[\"<SSH_KEY_ID>\"]}"],
  "reason": "Provision one H100 instance in Finland",
  "produces": { "INSTANCE_ID": "$.id" }
}

Query running instances:
{
  "args": ["verda-api", "GET", "/v1/instances", ""],
  "reason": "Discover current inventory"
}

Start an instance:
{
  "args": ["verda-api", "PUT", "/v1/instances", "{\"action\":\"start\",\"id\":\"<INSTANCE_ID>\"}"],
  "reason": "Start the instance we just created"
}

Stop an instance:
{
  "args": ["verda-api", "PUT", "/v1/instances", "{\"action\":\"shutdown\",\"id\":\"<INSTANCE_ID>\"}"],
  "reason": "Stop the instance (keeps billing per Verda policy; use delete to fully release)"
}

Delete an instance:
{
  "args": ["verda-api", "PUT", "/v1/instances", "{\"action\":\"delete\",\"id\":\"<INSTANCE_ID>\"}"],
  "reason": "Delete the instance and stop billing"
}

Create a volume:
{
  "args": ["verda-api", "POST", "/v1/volumes", "{\"type\":\"NVMe\",\"location_code\":\"FIN-01\",\"size\":500,\"name\":\"dataset-disk\"}"],
  "reason": "Create a 500 GB NVMe volume for dataset storage",
  "produces": { "VOLUME_ID": "$.id" }
}

Attach a volume to an existing instance:
{
  "args": ["verda-api", "PUT", "/v1/volumes", "{\"action\":\"attach\",\"id\":\"<VOLUME_ID>\",\"instance_id\":\"<INSTANCE_ID>\"}"],
  "reason": "Attach dataset volume to training instance"
}

Create an SSH key:
{
  "args": ["verda-api", "POST", "/v1/ssh-keys", "{\"name\":\"clanker-key\",\"key\":\"ssh-ed25519 AAAA... user@host\"}"],
  "reason": "Register our public key so we can ssh into instances",
  "produces": { "SSH_KEY_ID": "$.id" }
}

Create a startup script:
{
  "args": ["verda-api", "POST", "/v1/scripts", "{\"name\":\"install-deps\",\"script\":\"#!/bin/bash\\napt-get update && apt-get install -y python3-pip\"}"],
  "reason": "Define first-boot provisioning commands",
  "produces": { "SCRIPT_ID": "$.id" }
}

Create a GPU cluster with Kubernetes orchestrator:
{
  "args": ["verda-api", "POST", "/v1/clusters", "{\"cluster_type\":\"8H100\",\"image\":\"ubuntu-22.04-cuda-kubernetes\",\"hostname\":\"train-cluster\",\"description\":\"multi-node training\",\"location_code\":\"FIN-01\",\"ssh_key_ids\":[\"<SSH_KEY_ID>\"],\"shared_volume\":{\"name\":\"train-sfs\",\"size\":500}}"],
  "reason": "Create an 8x H100 instant cluster",
  "produces": { "CLUSTER_ID": "$.id" }
}

Discontinue a cluster (teardown):
{
  "args": ["verda-api", "PUT", "/v1/clusters", "{\"action\":\"discontinue\",\"id\":\"<CLUSTER_ID>\"}"],
  "reason": "Tear down the cluster and stop billing"
}

Check current balance before committing:
{
  "args": ["verda-api", "GET", "/v1/balance", ""],
  "reason": "Confirm project has enough credit before provisioning GPUs"
}

List instance types (unauth'd, no credit needed):
{
  "args": ["verda-api", "GET", "/v1/instance-types", ""],
  "reason": "Enumerate available GPU SKUs with pricing"
}

User request: %s

Output only the JSON plan. Do NOT wrap in markdown code fences.`, destructiveRule, question)
}
