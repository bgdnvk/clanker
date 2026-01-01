# Clanker CLI

Early alpha.  
First agent powering https://clankercloud.ai

Ask questions about your infra (and optionally GitHub/etc). Clanker is read-only: it calls CLIs/APIs and summarizes what it finds.

## Install

```bash
make install
```

### Requirements

-   Go
-   AWS CLI v2 (recommended; v1 breaks `--no-cli-pager`)

```bash
brew install awscli
```

## Config

Copy the example config and edit it for your environments/providers:

```bash
cp .clanker.example.yaml ~/.clanker.yaml
```

alternatively you can do
`clanker config init`

Most providers use env vars for keys (see [.clanker.example.yaml](.clanker.example.yaml)), e.g.:

```bash
export OPENAI_API_KEY="..."
export GEMINI_API_KEY="..."
```

### No config file defaults

If you run without `~/.clanker.yaml`:

-   Default provider: `openai` (unless you pass `--ai-profile`).
-   OpenAI key order: `--openai-key` → `OPENAI_API_KEY` (also supports `ai.providers.openai.api_key` and `ai.providers.openai.api_key_env` if config exists).
-   Gemini API key order (when using `--ai-profile gemini-api`): `--gemini-key` → `GEMINI_API_KEY` (also supports `ai.providers.gemini-api.api_key` and `ai.providers.gemini-api.api_key_env` if config exists).
-   Model: `openai` defaults to `gpt-5`; `gemini`/`gemini-api` defaults to `gemini-3-pro-preview`.

### AWS

Clanker uses your local AWS CLI profiles (not raw access keys in the clanker config).

Create a profile:

```bash
aws configure --profile clankercloud-tekbog | cat
aws sts get-caller-identity --profile clankercloud-tekbog | cat
```

Set the default environment + profile in `~/.clanker.yaml`:

```yaml
infra:
    default_provider: aws
    default_environment: clankercloud

    aws:
        environments:
            clankercloud:
                profile: clankercloud-tekbog
                region: us-east-1
```

Override for a single command:

```bash
clanker ask --aws --profile clankercloud-tekbog "what lambdas do we have?" | cat
```

## Usage

Flags:

-   `--aws`: force AWS context/tooling for the question (uses the default env/profile from `~/.clanker.yaml` unless you pass `--profile`)
-   `--profile <name>`: override the AWS CLI profile for this run
-   `--ai-profile <name>`: select an AI provider profile from `ai.providers.<name>` (overrides `ai.default_provider`)
-   `--maker`: generate an AWS CLI plan (JSON) for infrastructure changes
-   `--destroyer`: allow destructive AWS CLI operations when using `--maker`
-   `--apply`: apply an approved maker plan (reads from stdin unless `--plan-file` is provided)
-   `--plan-file <path>`: optional path to maker plan JSON file for `--apply`
-   `--debug`: print diagnostics (selected tools, AWS CLI calls, prompt sizes)
-   `--agent-trace`: print detailed coordinator/agent lifecycle logs (tool selection + investigation steps)

```bash
clanker ask "what's the status of my chat service lambda?"

clanker ask --profile dev "what's the last error from my big-api-service lambda?"

clanker ask --ai-profile openai "What are the latest logs for our dev Lambda functions?"

clanker ask --agent-trace --profile dev "how can i create an additional lambda and link it to dev?"

# Maker (plan + apply)

# Generate a plan (prints JSON)
clanker ask --aws --maker "create a small ec2 instance and a postgres rds" | cat

# Apply an approved plan from stdin
clanker ask --aws --maker --apply < plan.json | cat

# Apply an approved plan from a file
clanker ask --aws --maker --apply --plan-file plan.json | cat

# Allow destructive operations (only with explicit intent)
clanker ask --aws --maker --destroyer "delete the clanka-postgres rds instance" | cat
```

### Maker apply behavior

When you run with `--maker --apply`, the runner tries to be safe and repeatable:

-   Idempotent “already exists” errors are treated as success when safe (e.g. duplicate SG rules).
-   Some AWS async operations are waited to terminal state (e.g. CloudFormation create/update) so failures surface and can be remediated.
-   If the runner detects common AWS runtime issues (CIDR/subnet/template mismatches), it may rewrite and retry the original AWS CLI command.
-   If built-in retries/glue are exhausted, it can escalate to AI for prerequisite commands, then retry the original command with exponential backoff.

## Troubleshooting

AWS auth:

```bash
aws sts get-caller-identity --profile dev | cat
aws sso login --profile dev | cat
```

Config + debug:

```bash
clanker config show | cat
clanker ask "test" --debug | cat
```

### Debug output

Clanker has a single output flag:

-   `--debug`: prints progress + internal diagnostics (tool selection, AWS CLI calls, prompt sizes, etc).

Examples:

```bash
clanker ask "what ec2 instances are running" --aws --debug | cat
clanker ask "show github actions status" --github --debug | cat
```

## Notes

-   Only tested on macOS.
