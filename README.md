# Clanker CLI

Beta version.  
Main agent powering [Clanker Cloud](https://clankercloud.ai), currently in beta.

Docs available at [docs.clankercloud.ai](https://docs.clankercloud.ai/)

Ask questions about your infra (and optionally GitHub/etc). Clanker can inspect existing environments and also generate or apply infrastructure and deploy plans through its maker and deploy flows.

Repo: [bgdnvk/clanker](https://github.com/bgdnvk/clanker)

Interactive docs: [Clanker Cloud: How It Works](https://codexsims.com/explainers/clanker-cloud-how-it-works/)  
Courtesy of [@cto_junior](https://x.com/cto_junior)

Homebrew tap: [clankercloud/homebrew-tap](https://github.com/clankercloud/homebrew-tap)

## Install

### Homebrew (can be outdated, the best is to build from master)

```bash
brew tap clankercloud/tap
brew install clanker
```

### From source

```bash
make install
```

### Self-update

```bash
clanker update
```

By default, `clanker update` replaces the current binary with the latest GitHub
release from `bgdnvk/clanker`. To track the latest commit on the repository's
default branch instead, set the update channel during setup:

```bash
clanker config init --update-channel main
```

or edit `~/.clanker.yaml`:

```yaml
update:
    channel: main # release or main
```

You can also override it for one run:

```bash
clanker update --channel release
clanker update --channel main
```

### Requirements

- Go
- AWS CLI v2 (recommended; v1 breaks `--no-cli-pager`)

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
export COHERE_API_KEY="..."
```

### No config file defaults

If you run without `~/.clanker.yaml`:

- Default provider: `openai` (unless you pass `--ai-profile`).
- OpenAI key order: `--openai-key` → `OPENAI_API_KEY` (also supports `ai.providers.openai.api_key` and `ai.providers.openai.api_key_env` if config exists).
- Gemini API key order (when using `--ai-profile gemini-api`): `--gemini-key` → `GEMINI_API_KEY` (also supports `ai.providers.gemini-api.api_key` and `ai.providers.gemini-api.api_key_env` if config exists).
- Cohere API key order (when using `--ai-profile cohere`): `--cohere-key` → `COHERE_API_KEY` (also supports `ai.providers.cohere.api_key` and `ai.providers.cohere.api_key_env` if config exists).
- Model: `openai` defaults to `gpt-5`; `gemini`/`gemini-api` defaults to `gemini-3-pro-preview`; `cohere` defaults to `command-a-03-2025`.

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

### MCP

Clanker also exposes its own MCP surface as a CLI command.

Run it over HTTP:

```bash
clanker mcp --transport http --listen 127.0.0.1:39393 | cat
```

Or over stdio for MCP clients that launch commands directly:

```bash
clanker mcp --transport stdio | cat
```

The CLI MCP currently exposes tools to:

- return the installed Clanker version
- return Clanker routing decisions for a prompt
- run local `clanker` commands through MCP, including `ask`, `openclaw`, and other subcommands
- launch and talk to the Clanker Cloud desktop app through its local backend

Clanker chat routing also recognizes Clanker Cloud app questions now. If you use `clanker talk` and ask about the running desktop app or its saved settings, it will try the local Clanker Cloud backend first and fall back to Hermes if the app is not running.

Examples:

```bash
clanker ask --route-only "use clanker cloud mcp to show my saved settings" | cat
clanker ask --route-only "ask clanker cloud about the running app backend" | cat
clanker mcp --transport http --listen 127.0.0.1:39393 | cat
```

Example MCP calls against the standalone Clanker CLI server:

```bash
# Start the HTTP MCP server
clanker mcp --transport http --listen 127.0.0.1:39393 | cat

# Initialize a client session
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"local-cli","version":"1.0"}}}' | jq

# List available CLI MCP tools
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | jq

# Return the installed clanker version
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"clanker_version","arguments":{}}}' | jq

# Return the internal route decision for a prompt
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"clanker_route_question","arguments":{"question":"use clanker cloud mcp to show my saved settings"}}}' | jq

# Run a real clanker command through MCP
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"clanker_run_command","arguments":{"args":["ask","--route-only","use clanker cloud mcp to show my saved settings"]}}}' | jq
```

The standalone CLI MCP currently exposes these tools:

- `clanker_version`
- `clanker_route_question`
- `clanker_run_command`
- `clanker_cloud_app_status`
- `clanker_cloud_launch_app`
- `clanker_cloud_ask_app`
- `clanker_cloud_call_backend_api`

Flags:

- `--aws`: force AWS context/tooling for the question (uses the default env/profile from `~/.clanker.yaml` unless you pass `--profile`)
- `--profile <name>`: override the AWS CLI profile for this run
- `--ai-profile <name>`: select an AI provider profile from `ai.providers.<name>` (overrides `ai.default_provider`)
- `--maker`: generate an AWS CLI plan (JSON) for infrastructure changes
- `--destroyer`: allow destructive AWS CLI operations when using `--maker`
- `--apply`: apply an approved maker plan (reads from stdin unless `--plan-file` is provided)
- `--plan-file <path>`: optional path to maker plan JSON file for `--apply`
- `--debug`: print diagnostics (selected tools, AWS CLI calls, prompt sizes)
- `--agent-trace`: print detailed coordinator/agent lifecycle logs (tool selection + investigation steps)

```bash
clanker ask "what's the status of my chat service lambda?"

clanker ask --profile dev "what's the last error from my big-api-service lambda?"

clanker ask --ai-profile openai "What are the latest logs for our dev Lambda functions?"

clanker ask --ai-profile cohere --cohere-model command-a-03-2025 "Summarize the current deployment risks in dev."

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

### Security

Minimal security scan commands:

```bash
# Best-effort scan using whatever local provider access is already configured
clanker security | cat

# Focus the scan on a specific service or attack surface
clanker security "review public APIs, IAM blast radius, and auth gaps around clanker-auth" | cat

# Pin provider-side helpers to a specific account, project, or workspace
clanker security --profile prod --gcp-project my-gcp-project --workspace prod | cat

# Re-check auth-gated routes with runtime auth attached to the probe set
export CLANKER_RUNTIME_SECURITY_BEARER_TOKEN="your-token"
clanker security "verify which routes unlock with auth" | cat
```

Notes:

- Without `CLANKER_RUNTIME_DEEP_RESEARCH_ESTATE_JSON`, the scan still runs in best-effort mode using live provider context only.
- DigitalOcean live coverage works with either `digitalocean.api_token` / `DO_API_TOKEN` / `DIGITALOCEAN_ACCESS_TOKEN` or an authenticated `doctl` session.
- Supabase live coverage needs a configured `databases.connections` entry with `vendor: supabase`, or a runtime `CLANKER_RUNTIME_DB_CONNECTION_JSON` connection.
- Verda live coverage needs `verda.client_id` / `verda.client_secret`, `VERDA_CLIENT_ID` / `VERDA_CLIENT_SECRET`, or `verda auth login`.

### SRE Bot

Clanker can run a lightweight SRE bot that adapts to the infrastructure it finds and reports heartbeat/discovery events into Clanker Cloud Cerebro. Docker is the default runtime, but local foreground, launchd, systemd, Kubernetes, and minimal cloud VM install assets are available on request.

```bash
# Inspect what the SRE bot can see before installing anything
clanker sre discover | cat
clanker sre discover --format json | cat

# Plan the default Docker install
clanker sre plan --sre | cat

# Build a local Docker image from this repository if you are not using a published image
docker build -f Dockerfile.sre -t clanker-sre:local . | cat
clanker sre plan --sre --image clanker-sre:local | cat

# Generate Docker install assets under ~/.clanker/sre/install
export CLANKER_CEREBRO_URL="http://127.0.0.1:8080/api"
export CLANKER_CEREBRO_INGEST_TOKEN="..."
clanker sre install --sre --target docker --image clanker-sre:local --apply | cat

# Run locally in the foreground instead of Docker
clanker sre run --sre --target local --interval 60s | cat

# Request other install recipes explicitly
clanker sre install --sre --target launchd --apply | cat
clanker sre install --sre --target systemd --apply | cat
clanker sre install --sre --target k8s --apply | cat
clanker sre install --sre --target cloud-vm --apply | cat
```

The SRE bot does not assume Kubernetes, Helm, or OpenTelemetry. It detects Docker, kubeconfig, provider CLIs/tokens, database config, CI/CD signals, Terraform, and OTel collectors/env vars, then only enables the matching checks. If Cerebro is running locally, `clanker sre run` can auto-detect the desktop backend on ports `8080` to `8084`; remote ingestion requires `CLANKER_CEREBRO_INGEST_TOKEN` on the backend and the same token in the bot environment.

### Maker apply behavior

When you run with `--maker --apply`, the runner tries to be safe and repeatable:

- Idempotent "already exists" errors are treated as success when safe (e.g. duplicate SG rules).
- Some AWS async operations are waited to terminal state (e.g. CloudFormation create/update) so failures surface and can be remediated.
- If the runner detects common AWS runtime issues (CIDR/subnet/template mismatches), it may rewrite and retry the original AWS CLI command.
- If built-in retries/glue are exhausted, it can escalate to AI for prerequisite commands, then retry the original command with exponential backoff.

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

| Flag              | Description                                         |
| ----------------- | --------------------------------------------------- |
| `--cluster`       | EKS cluster name (updates kubeconfig automatically) |
| `--profile`       | AWS profile for EKS clusters                        |
| `--kubeconfig`    | Path to kubeconfig file (default: ~/.kube/config)   |
| `--context`       | kubectl context to use (overrides --cluster)        |
| `-n, --namespace` | Default namespace for queries                       |
| `--ai-profile`    | AI profile to use for LLM queries                   |
| `--debug`         | Show detailed debug output including LLM operations |

### Legacy Natural Language Queries (via `clanker ask`)

The main `ask` command also supports Kubernetes queries through automatic context detection:

```bash
# These queries are automatically routed to K8s handling
clanker ask "show cpu usage for all nodes"
clanker ask "list all pods in kube-system namespace"
clanker ask "why is pod nginx failing"
```

## Digital Ocean

Clanker supports Digital Ocean infrastructure queries via the `doctl` CLI.

### Setup

Install the doctl CLI:

```bash
# macOS
brew install doctl

# Linux (snap)
sudo snap install doctl
```

Set your API token:

```bash
export DO_API_TOKEN="your-token-here"
# or
export DIGITALOCEAN_ACCESS_TOKEN="your-token-here"
```

Or configure in `~/.clanker.yaml`:

```yaml
digitalocean:
    api_token: "your-token-here"
```

### Static Commands

```bash
# List resources directly (no AI)
clanker do list droplets
clanker do list kubernetes
clanker do list databases
clanker do list apps
clanker do list load-balancers
clanker do list volumes
clanker do list vpcs
clanker do list domains
clanker do list firewalls
clanker do list registries
clanker do list spaces
```

### AI Queries

```bash
# Ask questions about your Digital Ocean infrastructure
clanker ask --digitalocean "what droplets are running?"
clanker ask --digitalocean "show me my kubernetes clusters"
clanker ask --digitalocean "list all managed databases"
```

### Maker (Plan + Apply)

```bash
# Generate a plan
clanker ask --digitalocean --maker "create a small droplet in nyc1" | cat

# Apply an approved plan
clanker ask --apply --plan-file plan.json | cat

# Allow destructive operations
clanker ask --digitalocean --maker --destroyer "delete the test droplet" | cat
```

## Hetzner Cloud

Clanker supports Hetzner Cloud infrastructure queries via the `hcloud` CLI.

### Setup

Install the hcloud CLI:

```bash
# macOS
brew install hcloud

# Linux
# Download from https://github.com/hetznercloud/cli/releases
```

Set your API token:

```bash
export HCLOUD_TOKEN="your-token-here"
```

Or configure in `~/.clanker.yaml`:

```yaml
hetzner:
    api_token: "your-token-here"
```

### Static Commands

```bash
# List resources directly (no AI)
clanker hetzner list servers
clanker hetzner list load-balancers
clanker hetzner list volumes
clanker hetzner list networks
clanker hetzner list firewalls
clanker hetzner list floating-ips
clanker hetzner list primary-ips
clanker hetzner list ssh-keys
clanker hetzner list images
clanker hetzner list certificates
```

### AI Queries

```bash
# Ask questions about your Hetzner Cloud infrastructure
clanker ask --hetzner "what servers are running?"
clanker ask --hetzner "show me my load balancers"
clanker ask --hetzner "list all volumes"
```

### Maker (Plan + Apply)

```bash
# Generate a plan
clanker ask --hetzner --maker "create a cx22 server in fsn1" | cat

# Apply an approved plan
clanker ask --apply --plan-file plan.json | cat

# Allow destructive operations
clanker ask --hetzner --maker --destroyer "delete the test server" | cat
```

## Fly.io

Clanker supports Fly.io apps, machines, volumes, secrets, and addons via the Machines REST API plus the legacy GraphQL endpoint for orgs, Postgres, Wireguard, tokens, and marketplace extensions. The `flyctl` (a.k.a. `fly`) CLI is required only for `deploy`, `ssh`, `proxy`, and `secrets set` (which pipes values over stdin so they never appear on the command line).

### Setup

Install the flyctl CLI:

```bash
# macOS
brew install flyctl

# Linux / WSL
curl -L https://fly.io/install.sh | sh

# Windows
iwr https://fly.io/install.ps1 -useb | iex
```

Set your API token (generate one with `flyctl auth token` or at fly.io/dashboard/personal/tokens):

```bash
export FLY_API_TOKEN="your-token-here"
```

Or configure in `~/.clanker.yaml`:

```yaml
flyio:
    api_token: "your-token-here"
    org_slug: "personal" # optional — filter to one org
```

### Static Commands

```bash
# Apps + machines + volumes
clanker fly list apps
clanker fly list machines --app my-app
clanker fly list volumes --app my-app
clanker fly get app my-app
clanker fly get machine 1234abcd --app my-app

# Lifecycle
clanker fly restart machine 1234abcd --app my-app
clanker fly stop 1234abcd --app my-app
clanker fly start 1234abcd --app my-app
clanker fly destroy machine 1234abcd --app my-app --force

# Secrets (names + digests only; values never echoed)
clanker fly list secrets --app my-app
clanker fly secrets set DATABASE_URL=... --app my-app
clanker fly secrets unset OLD_KEY --app my-app

# Networking
clanker fly list ips --app my-app
clanker fly ips allocate --app my-app --type v4
clanker fly list certs --app my-app
clanker fly certs add example.com --app my-app

# Addons
clanker fly list postgres
clanker fly list redis
clanker fly list tigris
clanker fly list extensions

# Platform
clanker fly list regions
clanker fly list orgs
clanker fly auth whoami
```

### AI Queries

```bash
# Ask questions about your Fly.io infrastructure
clanker ask --flyio "what apps are running and in which regions?"
clanker ask --flyio "which machines are using the most memory?"
clanker ask --flyio "do I have any unattached volumes?"
```

Conversation history is preserved per-org at `~/.clanker/conversations/flyio_<org>.json` so follow-ups stay in context.

### Deploy + Scale (via flyctl)

```bash
# Deploy from the working directory
clanker fly deploy --app my-app --region iad

# Adjust scale
clanker fly scale count 3 --app my-app
clanker fly scale vm performance-2x --app my-app

# Roll back a release
clanker fly rollback --app my-app
```

## Verda Cloud

Clanker supports [Verda Cloud](https://verda.com) (ex-DataCrunch), a European GPU/AI cloud. Every operation runs against Verda's REST API directly — the `verda` CLI binary is optional and only needed for `verda auth login` and `verda skills install`.

### Setup

Verda uses OAuth2 Client Credentials. Generate a `client_id` / `client_secret` pair at [console.verda.com/account/api-keys](https://console.verda.com/account/api-keys) (scope `cloud-api-v1`), then pick one of the resolution paths below.

Option 1 — install the Verda CLI and log in:

```bash
brew install verda-cloud/tap/verda-cli
verda auth login   # writes ~/.verda/credentials which clanker reads
```

Option 2 — environment variables:

```bash
export VERDA_CLIENT_ID="..."
export VERDA_CLIENT_SECRET="..."
export VERDA_PROJECT_ID="..."   # optional
```

Option 3 — `~/.clanker.yaml`:

```yaml
verda:
    client_id: ""
    client_secret: ""
    default_project_id: ""
    default_location: "FIN-01"
    default_ssh_key_id: ""
    ssh_key_path: "~/.ssh/id_ed25519"
```

Option 4 — store in the clanker backend so other machines pick it up automatically:

```bash
clanker credentials store verda --client-id "$VERDA_CLIENT_ID" --client-secret "$VERDA_CLIENT_SECRET"
clanker credentials test verda    # hits /v1/balance through the stored creds
```

### Static Commands

```bash
clanker verda list instances
clanker verda list clusters
clanker verda list volumes
clanker verda list instance-types
clanker verda list locations
clanker verda list containers         # serverless container deployments
clanker verda list jobs               # serverless job deployments
clanker verda balance

clanker verda get instance <uuid|hostname>
clanker verda action start <uuid|hostname>
clanker verda action shutdown <uuid|hostname>
clanker verda action delete <uuid|hostname>   # destructive, requires confirmation
```

### AI Queries

```bash
# Explicit flag
clanker ask --verda "what GPU instances are running?"
clanker ask --verda "how much am I spending this month?"

# Keyword routing (no flag needed when the query mentions verda/datacrunch)
clanker ask "list my verda clusters"

# Default provider — set infra.default_provider: verda in ~/.clanker.yaml
# to route bare `clanker ask "..."` queries through Verda.
```

### Maker (Plan + Apply)

Verda plans use a `verda-api` verb so execution goes through the REST client directly (no CLI dependency). Destructive actions — `DELETE`, `action=delete|discontinue|force_shutdown|delete_stuck|hibernate` — require `--destroyer`.

```bash
# Generate a plan
clanker ask --verda --maker "spin up one H100 in FIN-01 with my default ssh key" | tee plan.json

# Apply an approved plan
clanker ask --apply --plan-file plan.json

# Allow destructive operations (delete instances / discontinue clusters)
clanker ask --verda --maker --destroyer "delete the training instance" | cat
```

### Kubernetes (Instant Clusters)

Verda doesn't have a managed K8s control plane, but its Instant Clusters ship with Kubernetes preinstalled. Clanker registers a `verda-instant` provider under its K8s agent that provisions a cluster and pulls kubeconfig off the head node. From the desktop app, click the "kubeconfig →" button on a Verda cluster in the resource list to get a ready-to-paste `ssh | sed` one-liner.

### MCP

Verda is exposed over MCP as `clanker_verda_ask` and `clanker_verda_list` so any MCP-compatible agent (Claude Desktop, Cursor, Zed, etc) can reach the same surface:

```bash
clanker mcp --transport http --listen :39393
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

- `--debug`: prints progress + internal diagnostics (tool selection, AWS CLI calls, prompt sizes, etc).

Examples:

```bash
clanker ask "what ec2 instances are running" --aws --debug | cat
clanker ask "show github actions status" --github --debug | cat
```

## Notes

- Works on MacOS, Linux and Windows, please report any issues.
