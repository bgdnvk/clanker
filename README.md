# Clanker CLI

Early alpha.

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
-   `--debug`: print diagnostics (selected tools, AWS CLI calls, prompt sizes)
-   `--agent-trace`: print detailed coordinator/agent lifecycle logs (tool selection + investigation steps)

```bash
clanker ask "what's the status of my chat service lambda?"

clanker ask --profile dev "what's the last error from my big-api-service lambda?"

clanker ask --ai-profile openai "What are the latest logs for our dev Lambda functions?"

clanker ask --agent-trace --profile dev "how can i create an additional lambda and link it to dev?"
```

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
