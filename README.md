# Clanker CLI

Early alpha.  
First agent powering https://clankercloud.ai

Ask questions about your infra (and optionally GitHub/etc). Clanker is read-only: it calls CLIs/APIs and summarizes what it finds.

Repo: https://github.com/bgdnvk/clanker

Homebrew tap: https://github.com/clankercloud/homebrew-tap

## Install

### Homebrew (recommended)

```bash
brew tap clankercloud/tap
brew install clanker
```

### From source

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

-   Idempotent "already exists" errors are treated as success when safe (e.g. duplicate SG rules).
-   Some AWS async operations are waited to terminal state (e.g. CloudFormation create/update) so failures surface and can be remediated.
-   If the runner detects common AWS runtime issues (CIDR/subnet/template mismatches), it may rewrite and retry the original AWS CLI command.
-   If built-in retries/glue are exhausted, it can escalate to AI for prerequisite commands, then retry the original command with exponential backoff.

## Kubernetes Commands

Clanker provides comprehensive Kubernetes cluster management and monitoring capabilities.

### Cluster Management

```bash
# Create an EKS cluster
clanker k8s create eks my-cluster --nodes 2 --node-type t3.small
clanker k8s create eks my-cluster --plan  # Show plan only

# Create a kubeadm cluster on EC2
clanker k8s create kubeadm my-cluster --workers 2 --key-pair my-key
clanker k8s create kubeadm my-cluster --plan  # Show plan only

# List clusters
clanker k8s list eks
clanker k8s list kubeadm

# Delete a cluster
clanker k8s delete eks my-cluster
clanker k8s delete kubeadm my-cluster

# Get kubeconfig for a cluster
clanker k8s kubeconfig eks my-cluster
clanker k8s kubeconfig kubeadm my-cluster
```

### Deploy Applications

```bash
# Deploy a container image
clanker k8s deploy nginx --name my-nginx --port 80
clanker k8s deploy nginx --replicas 3 --namespace production
clanker k8s deploy nginx --plan  # Show plan only
```

### Get Cluster Resources

```bash
# Get all resources from a specific cluster (JSON output)
clanker k8s resources --cluster my-cluster

# Get resources in YAML format
clanker k8s resources --cluster my-cluster -o yaml

# Get resources from all EKS clusters
clanker k8s resources
```

### Pod Logs

```bash
# Get logs from a pod
clanker k8s logs my-pod

# Get logs from a specific container
clanker k8s logs my-pod -c my-container

# Follow logs in real-time
clanker k8s logs my-pod -f

# Get last N lines
clanker k8s logs my-pod --tail 100

# Get logs from a specific time period
clanker k8s logs my-pod --since 1h

# Get logs with timestamps
clanker k8s logs my-pod --timestamps

# Get logs from all containers in a pod
clanker k8s logs my-pod --all-containers

# Get previous container logs (after restart)
clanker k8s logs my-pod -p

# Combine options
clanker k8s logs my-pod -n kube-system --tail 50 --since 30m
```

### Resource Metrics and Statistics

```bash
# Get node metrics
clanker k8s stats nodes
clanker k8s stats nodes --sort-by cpu
clanker k8s stats nodes --sort-by memory
clanker k8s stats nodes -o json
clanker k8s stats nodes -o yaml

# Get pod metrics
clanker k8s stats pods
clanker k8s stats pods -n kube-system
clanker k8s stats pods -A  # All namespaces
clanker k8s stats pods --sort-by memory
clanker k8s stats pods -o json

# Get metrics for a specific pod
clanker k8s stats pod my-pod
clanker k8s stats pod my-pod -n production
clanker k8s stats pod my-pod --containers  # Show container-level metrics
clanker k8s stats pod my-pod -o json

# Get cluster-wide aggregated metrics
clanker k8s stats cluster
clanker k8s stats cluster -o json
```

### K8s Ask: Natural Language Queries

The `k8s ask` command enables natural language queries against your Kubernetes cluster using AI. It uses a three-stage LLM pipeline similar to the AWS ask mode:

1. **Stage 1**: LLM analyzes your question and determines which kubectl operations are needed
2. **Stage 2**: Execute the kubectl operations in parallel
3. **Stage 3**: Combine results with cluster context and generate a markdown response

Conversation history is maintained per cluster for follow-up questions.

```bash
# Basic queries
clanker k8s ask "how many pods are running"
clanker k8s ask "how many nodes do I have"
clanker k8s ask "list all deployments and their replica counts"
clanker k8s ask "tell me the health of my cluster"

# With cluster and profile specification (for EKS)
clanker k8s ask --cluster my-cluster --profile myaws "show me all pods"
clanker k8s ask --cluster prod --profile prod-aws "how many replicas do I have"

# Namespace-specific queries
clanker k8s ask -n kube-system "show me all pods"

# Resource metrics
clanker k8s ask "which pods are using the most memory"
clanker k8s ask "show node resource usage"
clanker k8s ask "top 10 pods by cpu usage"

# Logs and troubleshooting
clanker k8s ask "show me recent logs from nginx"
clanker k8s ask "why is my pod crashing"
clanker k8s ask "show me pods that are not running"
clanker k8s ask "get warning events from the cluster"

# Follow-up questions (uses conversation context)
clanker k8s ask "show me the nginx deployment"
clanker k8s ask "now show me its logs"

# Debug mode (shows LLM operations)
clanker k8s ask --debug "how many pods are running"
```

#### K8s Ask Flags

| Flag | Description |
|------|-------------|
| `--cluster` | EKS cluster name (updates kubeconfig automatically) |
| `--profile` | AWS profile for EKS clusters |
| `--kubeconfig` | Path to kubeconfig file (default: ~/.kube/config) |
| `--context` | kubectl context to use (overrides --cluster) |
| `-n, --namespace` | Default namespace for queries |
| `--ai-profile` | AI profile to use for LLM queries |
| `--debug` | Show detailed debug output including LLM operations |

### Legacy Natural Language Queries (via `clanker ask`)

The main `ask` command also supports Kubernetes queries through automatic context detection:

```bash
# These queries are automatically routed to K8s handling
clanker ask "show cpu usage for all nodes"
clanker ask "list all pods in kube-system namespace"
clanker ask "why is pod nginx failing"
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
