# Clanker CLI（简体中文）

<p>
  <img src="assets/clanker-logo.png" alt="Clanker logo" width="96" height="96">
</p>

[English](README.md) | 简体中文

Clanker CLI 是支撑 [Clanker Cloud](https://clankercloud.ai) 的 agent swarm。Clanker Cloud 是面向智能体和人的 AI DevOps IDE。

文档地址：[docs.clankercloud.ai](https://docs.clankercloud.ai/)

你可以用 Clanker 询问基础设施状态，也可以接入 GitHub 等上下文。Clanker 能检查现有环境，并通过 maker 和 deploy 流程生成或执行基础设施与部署计划。

仓库：[bgdnvk/clanker](https://github.com/bgdnvk/clanker)

Homebrew tap：[clankercloud/homebrew-tap](https://github.com/clankercloud/homebrew-tap)

## 安装

### Homebrew（可能滞后；要最新可从 master 构建）

```bash
brew tap clankercloud/tap
brew install clanker
```

### 从源码安装

```bash
make install
```

### 自更新

```bash
clanker update
```

默认情况下，`clanker update` 会用 `bgdnvk/clanker` 的最新 GitHub release 替换当前二进制文件。若要跟踪仓库默认分支的最新提交，可在初始化时设置更新通道：

```bash
clanker config init --update-channel main
```

或编辑 `~/.clanker.yaml`：

```yaml
update:
    channel: main # release or main
```

也可以在单次运行中覆盖：

```bash
clanker update --channel release
clanker update --channel main
```

### 依赖

- Go
- 只需为你想让 Clanker 检查或管理的云提供商安装对应 CLI

```bash
clanker onboarding scan
clanker onboarding scan --provider aws,gcp,azure,kubernetes,flyio,linear,notion --format json
clanker onboarding install --dry-run --provider aws,gcp,azure,kubernetes,flyio
clanker onboarding install --yes aws gcloud az kubectl flyctl
```

`clanker onboarding scan` 会检测本地凭据和缺失的 provider CLI，并返回官方安装文档、认证命令、token/account URL，方便用户和智能体继续操作。只安装所选或检测到的 provider 所需工具即可。例如：AWS 使用官方 AWS CLI 安装器和本地 profile，GCP 使用官方 Google Cloud CLI 与 Application Default Credentials，Azure 使用官方 Azure CLI 登录流程。被要求安装或使用 Clanker Cloud 的智能体应先运行安装步骤，再重新扫描，并和用户一起完成剩余的浏览器登录、SSO、sudo 或官方 API token 步骤。

## 配置

复制示例配置并按你的环境/provider 修改：

```bash
cp .clanker.example.yaml ~/.clanker.yaml
```

也可以运行：

```bash
clanker config init
```

大多数 provider 使用环境变量保存 key（见 [.clanker.example.yaml](.clanker.example.yaml)）：

```bash
export OPENAI_API_KEY="..."
export GEMINI_API_KEY="..."
export COHERE_API_KEY="..."
```

### 没有配置文件时的默认值

如果没有 `~/.clanker.yaml`：

- 默认 AI provider：`openai`（除非传入 `--ai-profile`）。
- OpenAI key 顺序：`--openai-key` → `OPENAI_API_KEY`（如果存在配置，也支持 `ai.providers.openai.api_key` 和 `ai.providers.openai.api_key_env`）。
- Gemini API key 顺序（使用 `--ai-profile gemini-api` 时）：`--gemini-key` → `GEMINI_API_KEY`（如果存在配置，也支持 `ai.providers.gemini-api.api_key` 和 `ai.providers.gemini-api.api_key_env`）。
- Cohere API key 顺序（使用 `--ai-profile cohere` 时）：`--cohere-key` → `COHERE_API_KEY`（如果存在配置，也支持 `ai.providers.cohere.api_key` 和 `ai.providers.cohere.api_key_env`）。
- 模型：`openai` 默认 `gpt-5`；`gemini`/`gemini-api` 默认 `gemini-2.5-flash`；`cohere` 默认 `command-a-03-2025`。

### AWS

Clanker 使用你的本地 AWS CLI profile，不会把原始 access key 放进 Clanker 配置。

使用官方 AWS CLI 创建 profile。账号支持 SSO 时优先使用 SSO；只有在账号要求时才使用 access key。

```bash
aws configure sso
aws sso login --profile clankercloud-tekbog
aws configure --profile clankercloud-tekbog | cat
aws sts get-caller-identity --profile clankercloud-tekbog | cat
```

在 `~/.clanker.yaml` 中设置默认环境和 profile：

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

单次命令覆盖：

```bash
clanker ask --aws --profile clankercloud-tekbog "what lambdas do we have?" | cat
```

### 云资源盘点示例

使用静态 `list` 命令进行只读盘点，不经过 AI 解释：

```bash
clanker aws list resources
clanker aws list all-services
clanker aws list bedrock-kb
clanker gcp list services
clanker gcp list resources
clanker gcp list run-worker-pools
clanker azure list resource-graph
clanker azure list private-endpoints
clanker azure list ai-search
clanker cf list ai-search
clanker cf list browser-sessions
clanker cf list secrets-stores
clanker cf list pipelines
```

每个 provider 的完整资源列表可通过帮助查看：

```bash
clanker aws list --help
clanker gcp list --help
clanker azure list --help
clanker cf list --help
```

## 使用

### 自然语言询问

```bash
clanker ask "what's the status of my chat service lambda?"
clanker ask --profile dev "what's the last error from my big-api-service lambda?"
clanker ask --ai-profile openai "What are the latest logs for our dev Lambda functions?"
clanker ask --ai-profile cohere --cohere-model command-a-03-2025 "Summarize the current deployment risks in dev."
clanker ask --agent-trace --profile dev "how can i create an additional lambda and link it to dev?"
```

常用 flag：

- `--aws`：强制使用 AWS 上下文/工具（默认读取 `~/.clanker.yaml` 的环境/profile，除非传入 `--profile`）。
- `--tencent`：强制使用腾讯云上下文/工具（读取 `tencent.*` 配置或 `TENCENTCLOUD_*` 环境变量）。
- `--profile <name>`：覆盖本次运行的 AWS CLI profile。
- `--ai-profile <name>`：从 `ai.providers.<name>` 选择 AI provider profile（覆盖 `ai.default_provider`）。
- `--maker`：为基础设施变更生成 provider 执行计划（JSON）。
- `--destroyer`：使用 `--maker` 时允许破坏性云操作。
- `--apply`：执行已批准的 maker plan（默认从 stdin 读取，除非提供 `--plan-file`）。
- `--plan-file <path>`：为 `--apply` 指定 maker plan JSON 文件。
- `--debug`：打印诊断信息（所选工具、AWS CLI 调用、prompt 大小等）。
- `--agent-trace`：打印详细 coordinator/agent 生命周期日志（工具选择与调查步骤）。

### Maker（生成计划 + 执行）

```bash
# 生成计划（打印 JSON）
clanker ask --aws --maker "create a small ec2 instance and a postgres rds" | cat

# 从 stdin 执行已批准计划
clanker ask --aws --maker --apply < plan.json | cat

# 从文件执行已批准计划
clanker ask --aws --maker --apply --plan-file plan.json | cat

# 允许破坏性操作（必须明确表达意图）
clanker ask --aws --maker --destroyer "delete the clanka-postgres rds instance" | cat

# 腾讯云 maker 直接调用腾讯云 API
clanker ask --tencent --maker "create a VPC named clanker-demo in ap-singapore" | cat
```

执行 `--maker --apply` 时，runner 会尽量保证安全和可重复：

- 安全情况下，幂等的 "already exists" 错误会被视为成功，例如重复的安全组规则。
- 部分 AWS 异步操作会等待到终态，例如 CloudFormation create/update，以便暴露失败并继续修复。
- 如果检测到常见 AWS runtime 问题（CIDR/subnet/template 不匹配），runner 可能会改写并重试原 AWS CLI 命令。
- 如果内置重试/粘合逻辑耗尽，可以升级给 AI 生成前置命令，再用指数退避重试原命令。

### Security

最小安全扫描命令：

```bash
# 使用当前本地 provider 权限做 best-effort 扫描
clanker security | cat

# 聚焦某个服务或攻击面
clanker security "review public APIs, IAM blast radius, and auth gaps around clanker-auth" | cat

# 将 provider 侧 helper 固定到特定账号、项目或 workspace
clanker security --profile prod --gcp-project my-gcp-project --workspace prod | cat

# 为探测集附加 runtime auth，重新检查需要认证的路由
export CLANKER_RUNTIME_SECURITY_BEARER_TOKEN="your-token"
clanker security "verify which routes unlock with auth" | cat
```

说明：

- 如果没有 `CLANKER_RUNTIME_DEEP_RESEARCH_ESTATE_JSON`，扫描仍会在 best-effort 模式下使用实时 provider 上下文运行。
- DigitalOcean 实时覆盖支持 `digitalocean.api_token`、`DO_API_TOKEN`、`DIGITALOCEAN_ACCESS_TOKEN` 或已认证的 `doctl` session。
- Supabase 实时覆盖需要配置 `vendor: supabase` 的 `databases.connections` 条目，或运行时 `CLANKER_RUNTIME_DB_CONNECTION_JSON` 连接。
- Verda 实时覆盖需要 `verda.client_id` / `verda.client_secret`、`VERDA_CLIENT_ID` / `VERDA_CLIENT_SECRET`，或 `verda auth login`。

### SRE Bot

Clanker 可以运行轻量 SRE bot。它会根据发现的基础设施自动调整检查项，并向 Clanker Cloud Cerebro 上报 heartbeat/discovery 事件。Docker 是默认运行时，也可以按需生成 local foreground、launchd、systemd、Kubernetes 和最小云 VM 安装资产。

```bash
# 安装前先查看 SRE bot 能看到什么
clanker sre discover | cat
clanker sre discover --format json | cat

# 规划默认 Docker 安装
clanker sre plan --sre | cat

# 如果不使用发布镜像，可从本仓库构建本地 Docker 镜像
docker build -f Dockerfile.sre -t clanker-sre:local . | cat
clanker sre plan --sre --image clanker-sre:local | cat

# 在 ~/.clanker/sre/install 下生成 Docker 安装资产
export CLANKER_CEREBRO_URL="http://127.0.0.1:8080/api"
export CLANKER_CEREBRO_INGEST_TOKEN="..."
clanker sre install --sre --target docker --image clanker-sre:local --apply | cat

# 在前台本地运行，而不是使用 Docker
clanker sre run --sre --target local --interval 60s | cat

# 显式请求其他安装配方
clanker sre install --sre --target launchd --apply | cat
clanker sre install --sre --target systemd --apply | cat
clanker sre install --sre --target k8s --apply | cat
clanker sre install --sre --target cloud-vm --apply | cat
```

SRE bot 不默认假设 Kubernetes、Helm 或 OpenTelemetry 存在。它会检测 Docker、kubeconfig、provider CLI/token、数据库配置、CI/CD 信号、Terraform、OTel collector/env var，然后只启用匹配的检查。如果 Cerebro 在本地运行，`clanker sre run` 会尝试在 `8080` 到 `8084` 端口自动发现桌面后端；远程 ingestion 需要后端设置 `CLANKER_CEREBRO_INGEST_TOKEN`，bot 环境也使用同一个 token。

### MCP

Clanker 也通过 CLI 命令暴露自己的 MCP surface。

HTTP 方式运行：

```bash
clanker mcp --transport http --listen 127.0.0.1:39393 | cat
```

面向直接启动命令的 MCP client，也可以使用 stdio：

```bash
clanker mcp --transport stdio | cat
```

CLI MCP 当前可以：

- 返回已安装的 Clanker 版本。
- 返回 Clanker 对 prompt 的路由决策。
- 通过 MCP 运行本地 `clanker` 命令，包括 `ask`、`openclaw` 和其他子命令。
- 启动并访问 Clanker Cloud 桌面 app 的本地后端。
- 通过原生 `clanker_k8s_*` MCP 工具检查 Kubernetes 集群并与其对话，包括 `clanker_k8s_ask_cluster`。

示例：

```bash
clanker ask --route-only "use clanker cloud mcp to show my saved settings" | cat
clanker ask --route-only "ask clanker cloud about the running app backend" | cat
clanker mcp --transport http --listen 127.0.0.1:39393 | cat
```

独立 Clanker CLI MCP server 的示例调用：

```bash
# 启动 HTTP MCP server
clanker mcp --transport http --listen 127.0.0.1:39393 | cat

# 初始化 client session
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"local-cli","version":"1.0"}}}' | jq

# 列出可用 CLI MCP 工具
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | jq

# 返回已安装的 clanker 版本
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"clanker_version","arguments":{}}}' | jq

# 返回某个 prompt 的内部路由决策
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"clanker_route_question","arguments":{"question":"use clanker cloud mcp to show my saved settings"}}}' | jq

# 通过 MCP 运行真实 clanker 命令
curl -sS -X POST http://127.0.0.1:39393/mcp \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"clanker_run_command","arguments":{"args":["ask","--route-only","use clanker cloud mcp to show my saved settings"]}}}' | jq
```

## Kubernetes 命令

Clanker 提供完整的 Kubernetes 集群管理和监控能力。

### 集群管理

```bash
# 创建 EKS 集群
clanker k8s create eks my-cluster --nodes 2 --node-type t3.small
clanker k8s create eks my-cluster --plan  # 只展示计划

# 在 EC2 上创建 kubeadm 集群
clanker k8s create kubeadm my-cluster --workers 2 --key-pair my-key
clanker k8s create kubeadm my-cluster --plan  # 只展示计划

# 列出集群
clanker k8s list eks
clanker k8s list kubeadm

# 删除集群
clanker k8s delete eks my-cluster
clanker k8s delete kubeadm my-cluster

# 获取集群 kubeconfig
clanker k8s kubeconfig eks my-cluster
clanker k8s kubeconfig kubeadm my-cluster
```

### 部署应用

```bash
# 部署容器镜像
clanker k8s deploy nginx --name my-nginx --port 80
clanker k8s deploy nginx --replicas 3 --namespace production
clanker k8s deploy nginx --plan  # 只展示计划
```

### 获取集群资源

```bash
# 获取特定集群的全部资源（JSON 输出）
clanker k8s resources --cluster my-cluster

# 以 YAML 格式获取资源
clanker k8s resources --cluster my-cluster -o yaml

# 获取全部 EKS 集群的资源
clanker k8s resources
```

### Pod 日志

```bash
# 获取 pod 日志
clanker k8s logs my-pod

# 获取指定容器日志
clanker k8s logs my-pod -c my-container

# 实时跟随日志
clanker k8s logs my-pod -f

# 获取最近 N 行
clanker k8s logs my-pod --tail 100

# 获取某个时间段的日志
clanker k8s logs my-pod --since 1h

# 输出时间戳
clanker k8s logs my-pod --timestamps

# 获取 pod 中所有容器日志
clanker k8s logs my-pod --all-containers

# 获取上一次容器日志（重启后）
clanker k8s logs my-pod -p

# 组合选项
clanker k8s logs my-pod -n kube-system --tail 50 --since 30m
```

### 资源指标和统计

```bash
# 获取 node 指标
clanker k8s stats nodes
clanker k8s stats nodes --sort-by cpu
clanker k8s stats nodes --sort-by memory
clanker k8s stats nodes -o json
clanker k8s stats nodes -o yaml

# 获取 pod 指标
clanker k8s stats pods
clanker k8s stats pods -n kube-system
clanker k8s stats pods -A  # 所有 namespace
clanker k8s stats pods --sort-by memory
clanker k8s stats pods -o json

# 获取指定 pod 的指标
clanker k8s stats pod my-pod
clanker k8s stats pod my-pod -n production
clanker k8s stats pod my-pod --containers  # 展示容器级指标
clanker k8s stats pod my-pod -o json

# 获取集群级聚合指标
clanker k8s stats cluster
clanker k8s stats cluster -o json
```

### K8s Ask：自然语言查询

`k8s ask` 可以用 AI 对 Kubernetes 集群进行自然语言查询。它使用与 AWS ask 模式类似的三阶段 LLM pipeline：

1. **阶段 1**：LLM 分析问题并判断需要哪些 kubectl 操作。
2. **阶段 2**：并行执行 kubectl 操作。
3. **阶段 3**：结合结果和集群上下文生成 markdown 响应。

每个集群会保留对话历史，以支持追问。

```bash
# 基础查询
clanker k8s ask "how many pods are running"
clanker k8s ask "how many nodes do I have"
clanker k8s ask "list all deployments and their replica counts"
clanker k8s ask "tell me the health of my cluster"

# 指定集群和 profile（EKS）
clanker k8s ask --cluster my-cluster --profile myaws "show me all pods"
clanker k8s ask --cluster prod --profile prod-aws "how many replicas do I have"

# namespace 查询
clanker k8s ask -n kube-system "show me all pods"

# 资源指标
clanker k8s ask "which pods are using the most memory"
clanker k8s ask "show node resource usage"
clanker k8s ask "top 10 pods by cpu usage"

# 日志与故障排查
clanker k8s ask "show me recent logs from nginx"
clanker k8s ask "why is my pod crashing"
clanker k8s ask "show me pods that are not running"
clanker k8s ask "get warning events from the cluster"

# 追问（使用对话上下文）
clanker k8s ask "show me the nginx deployment"
clanker k8s ask "now show me its logs"

# debug 模式（展示 LLM 操作）
clanker k8s ask --debug "how many pods are running"
```

MCP agent 在启动 `clanker mcp` 后，也可以通过 `clanker_k8s_ask_cluster` 使用同一个 pipeline。该工具接受 `question`，以及可选的 `cluster`、`context`、`namespace`、`kubeconfig`、`profile`、`provider`、`gcpProject`、`gcpRegion`、`aiProfile`、`model` 字段，因此 agent 可以询问任何可访问的 EKS、GKE 或 kubeconfig-backed 集群，而不必通过通用命令工具 shell out。

#### K8s Ask flags

| Flag              | 描述                                           |
| ----------------- | ---------------------------------------------- |
| `--cluster`       | EKS 集群名称（自动更新 kubeconfig）            |
| `--profile`       | EKS 集群使用的 AWS profile                     |
| `--kubeconfig`    | kubeconfig 文件路径（默认：~/.kube/config）    |
| `--context`       | 要使用的 kubectl context（覆盖 --cluster）     |
| `-n, --namespace` | 查询的默认 namespace                           |
| `--ai-profile`    | LLM 查询使用的 AI profile                      |
| `--model`         | 所选 AI profile 使用的 AI 模型                 |
| `--debug`         | 展示详细 debug 输出，包括 LLM 操作             |

主 `ask` 命令也会通过自动上下文检测支持 Kubernetes 查询：

```bash
# 这些查询会自动路由到 K8s 处理
clanker ask "show cpu usage for all nodes"
clanker ask "list all pods in kube-system namespace"
clanker ask "why is pod nginx failing"
```

## DigitalOcean

Clanker 通过 `doctl` CLI 支持 DigitalOcean 基础设施查询。

### 设置

```bash
# macOS
brew install doctl

# Linux (snap)
sudo snap install doctl
```

设置 API token：

```bash
export DO_API_TOKEN="your-token-here"
# 或
export DIGITALOCEAN_ACCESS_TOKEN="your-token-here"
```

或写入 `~/.clanker.yaml`：

```yaml
digitalocean:
    api_token: "your-token-here"
```

### 静态命令

```bash
# 直接列出资源（不使用 AI）
clanker do list account
clanker do list droplets
clanker do list droplet-autoscale
clanker do list kubernetes
clanker do list databases
clanker do list apps
clanker do list functions
clanker do list serverless-inference-models
clanker do list dedicated-inference
clanker do list gradient-agents --region tor1
clanker do list load-balancers
clanker do list cdns
clanker do list volumes
clanker do list nfs --region nyc3
clanker do list vpcs
clanker do list vpc-peerings
clanker do list vpc-nat-gateways
clanker do list domains
clanker do list firewalls
clanker do list reserved-ips
clanker do list certificates
clanker do list monitoring-alerts
clanker do list registries
clanker do list spaces

# 查看当前 doctl-backed 覆盖列表
clanker do list --help
```

### AI 查询

```bash
clanker ask --digitalocean "what droplets are running?"
clanker ask --digitalocean "show me my kubernetes clusters"
clanker ask --digitalocean "list all managed databases"
```

### Maker（生成计划 + 执行）

```bash
# 生成计划
clanker ask --digitalocean --maker "create a small droplet in nyc1" | cat

# 执行已批准计划
clanker ask --apply --plan-file plan.json | cat

# 允许破坏性操作
clanker ask --digitalocean --maker --destroyer "delete the test droplet" | cat
```

## Hetzner Cloud

Clanker 通过 `hcloud` CLI 支持 Hetzner Cloud 基础设施查询。

### 设置

```bash
# macOS
brew install hcloud

# Linux
# 从 https://github.com/hetznercloud/cli/releases 下载
```

设置 API token：

```bash
export HCLOUD_TOKEN="your-token-here"
```

或写入 `~/.clanker.yaml`：

```yaml
hetzner:
    api_token: "your-token-here"
```

### 静态命令

```bash
# 直接列出资源（不使用 AI）
clanker hetzner list servers
clanker hetzner list load-balancers
clanker hetzner list volumes
clanker hetzner list networks
clanker hetzner list firewalls
clanker hetzner list floating-ips
clanker hetzner list primary-ips
clanker hetzner list ssh-keys
clanker hetzner list images
clanker hetzner list isos
clanker hetzner list certificates
clanker hetzner list placement-groups
clanker hetzner list server-types
clanker hetzner list locations
clanker hetzner list datacenters

# 查看完整 hcloud-backed 覆盖列表
clanker hetzner list --help
```

### AI 查询

```bash
clanker ask --hetzner "what servers are running?"
clanker ask --hetzner "show me my load balancers"
clanker ask --hetzner "list all volumes"
```

### Maker（生成计划 + 执行）

```bash
# 生成计划
clanker ask --hetzner --maker "create a cx22 server in fsn1" | cat

# 执行已批准计划
clanker ask --apply --plan-file plan.json | cat

# 允许破坏性操作
clanker ask --hetzner --maker --destroyer "delete the test server" | cat
```

## 腾讯云

Clanker 通过直接 SDK/API 调用支持腾讯云，不需要安装单独的腾讯云 CLI。`clanker tencent` 命令树适合原始资源盘点、安全扫描、账单检查、续费提醒和 TKE kubeconfig 导出；`clanker ask --tencent` 会在这些盘点数据之上提供自然语言回答。

### 设置

创建一个具备目标服务权限的腾讯云 CAM secret ID / secret key。

用环境变量设置凭据：

```bash
export TENCENTCLOUD_SECRET_ID="AKID..."
export TENCENTCLOUD_SECRET_KEY="..."
export TENCENTCLOUD_REGION="ap-singapore"
```

或写入 `~/.clanker.yaml`：

```yaml
tencent:
    secret_id: "AKID..."
    secret_key: "..."
    region: ap-singapore
```

也支持旧环境变量别名：`TENCENT_SECRET_ID`、`TENCENT_SECRET_KEY`、`TENCENT_REGION`。如果未设置 region，Clanker 默认使用 `ap-singapore`。静态命令也支持 `--region`。

### 静态命令

```bash
# 直接列出资源（不使用 AI）
clanker tencent list cvm
clanker tencent list vpc --region ap-singapore
clanker tencent list security-groups --all-regions
clanker tencent list tke --format json
clanker tencent regions

# 别名
clanker tc list cvm
clanker tencentcloud list cos
```

常见 `list` 资源包括 `cvm`、`vpc`、`subnets`、`security-groups`、`mysql`、`postgres`、`cos`、`tke`、`clb`、`eip`、`cbs`、`ssl`、`cam`、`redis`、`mongodb`、`cynosdb`、`cdn`、`edgeone`、`waf`、`antiddos`、`nat`、`vpn`、`ccn`、`dc`、`monitor`、`cls` 和 `cloudaudit`。

### AI 查询

```bash
clanker ask --tencent "what CVMs are running?"
clanker ask --tencent "which security groups expose SSH to the internet?"
clanker ask --tencent "show me TKE clusters and their node counts"
```

### 安全、成本和 TKE helper

```bash
# 安全组审计
clanker tencent sg-rules sg-abc12345 --region ap-singapore

# JSON 安全扫描
clanker tencent security public-exposure --region ap-singapore
clanker tencent security db-exposure --region ap-singapore
clanker tencent security all --region ap-singapore

# 账单和代金券
clanker tencent cost by-product --month 2026-05
clanker tencent cost top --month 2026-05 --limit 20
clanker tencent cost vouchers --status unUsed

# 预付费资源续费提醒
clanker tencent expiry --regions=ap-singapore,ap-jakarta --threshold=14 --format=json

# TKE kubeconfig 导出
clanker tencent kubeconfig cls-abc123 --region ap-singapore > ~/.kube/tencent
```

### Maker（生成计划 + 执行）

腾讯云 maker plan 内部使用 `tencent-api` verb，因此会通过签名后的腾讯云 API 请求直接执行。破坏性 API 操作需要 `--destroyer`。

```bash
# 生成计划
clanker ask --tencent --maker "create a security group that allows HTTPS" | tee plan.json

# 执行已批准计划
clanker ask --apply --plan-file plan.json | cat

# 允许破坏性操作
clanker ask --tencent --maker --destroyer "delete the unused test CVM" | cat
```

## Fly.io

Clanker 通过 Machines REST API 支持 Fly.io apps、machines、volumes、secrets 和 addons，并通过 legacy GraphQL endpoint 支持 orgs、Postgres、Wireguard、tokens 和 marketplace extensions。只有 `deploy`、`ssh`、`proxy`、`secrets set` 需要 `flyctl`（也叫 `fly`）CLI；`secrets set` 通过 stdin 传值，值不会出现在命令行中。

### 设置

```bash
# macOS
brew install flyctl

# Linux / WSL
curl -L https://fly.io/install.sh | sh

# Windows
iwr https://fly.io/install.ps1 -useb | iex
```

设置 API token（可用 `flyctl auth token` 生成，或在 fly.io/dashboard/personal/tokens 创建）：

```bash
export FLY_API_TOKEN="your-token-here"
```

或写入 `~/.clanker.yaml`：

```yaml
flyio:
    api_token: "your-token-here"
    org_slug: "personal" # 可选；只筛选一个 org
```

### 静态命令

```bash
# Apps + machines + volumes
clanker fly list apps
clanker fly list machines --app my-app
clanker fly list volumes --app my-app
clanker fly get app my-app
clanker fly get machine 1234abcd --app my-app

# 生命周期
clanker fly restart machine 1234abcd --app my-app
clanker fly stop 1234abcd --app my-app
clanker fly start 1234abcd --app my-app
clanker fly destroy machine 1234abcd --app my-app --force

# Secrets（只列名称和 digest；不会回显值）
clanker fly list secrets --app my-app
clanker fly secrets set DATABASE_URL=... --app my-app
clanker fly secrets unset OLD_KEY --app my-app

# 网络
clanker fly list ips --app my-app
clanker fly ips allocate --app my-app --type v4
clanker fly list certs --app my-app
clanker fly certs add example.com --app my-app

# Addons
clanker fly list postgres
clanker fly list redis
clanker fly list tigris
clanker fly list extensions

# 平台
clanker fly list regions
clanker fly list orgs
clanker fly auth whoami
```

### AI 查询

```bash
clanker ask --flyio "what apps are running and in which regions?"
clanker ask --flyio "which machines are using the most memory?"
clanker ask --flyio "do I have any unattached volumes?"
```

对话历史按 org 保存在 `~/.clanker/conversations/flyio_<org>.json`，便于后续追问保留上下文。

### 部署 + 扩缩容（通过 flyctl）

```bash
# 从当前工作目录部署
clanker fly deploy --app my-app --region iad

# 调整规模
clanker fly scale count 3 --app my-app
clanker fly scale vm performance-2x --app my-app

# 回滚 release
clanker fly rollback --app my-app
```

## Verda Cloud

Clanker 支持 [Verda Cloud](https://verda.com)（原 DataCrunch），这是欧洲 GPU/AI 云。所有操作都直接调用 Verda REST API；`verda` CLI 二进制是可选项，只在 `verda auth login` 和 `verda skills install` 中需要。

### 设置

Verda 使用 OAuth2 Client Credentials。在 [console.verda.com/account/api-keys](https://console.verda.com/account/api-keys) 生成 `client_id` / `client_secret`（scope 为 `cloud-api-v1`），然后选择以下任一路径。

方式 1：安装 Verda CLI 并登录：

```bash
brew install verda-cloud/tap/verda-cli
verda auth login   # 写入 clanker 会读取的 ~/.verda/credentials
```

方式 2：环境变量：

```bash
export VERDA_CLIENT_ID="..."
export VERDA_CLIENT_SECRET="..."
export VERDA_PROJECT_ID="..."   # 可选
```

方式 3：`~/.clanker.yaml`：

```yaml
verda:
    client_id: ""
    client_secret: ""
    default_project_id: ""
    default_location: "FIN-01"
    default_ssh_key_id: ""
    ssh_key_path: "~/.ssh/id_ed25519"
```

方式 4：存进 clanker backend，让其他机器自动获取：

```bash
clanker credentials store verda --client-id "$VERDA_CLIENT_ID" --client-secret "$VERDA_CLIENT_SECRET"
clanker credentials test verda    # 用已存凭据请求 /v1/balance
```

### 静态命令

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
clanker verda action delete <uuid|hostname>   # 破坏性操作，需要确认
```

### AI 查询

```bash
# 显式 flag
clanker ask --verda "what GPU instances are running?"
clanker ask --verda "how much am I spending this month?"

# 关键词路由（查询提到 verda/datacrunch 时不需要 flag）
clanker ask "list my verda clusters"

# 默认 provider：在 ~/.clanker.yaml 中设置 infra.default_provider: verda
# 可让普通 `clanker ask "..."` 查询通过 Verda 路由。
```

### Maker（生成计划 + 执行）

Verda plan 使用 `verda-api` verb，因此会直接通过 REST client 执行（不依赖 CLI）。破坏性操作，例如 `DELETE`、`action=delete|discontinue|force_shutdown|delete_stuck|hibernate`，需要 `--destroyer`。

```bash
# 生成计划
clanker ask --verda --maker "spin up one H100 in FIN-01 with my default ssh key" | tee plan.json

# 执行已批准计划
clanker ask --apply --plan-file plan.json

# 允许破坏性操作（删除实例 / discontinue cluster）
clanker ask --verda --maker --destroyer "delete the training instance" | cat
```

### Kubernetes（Instant Clusters）

Verda 没有托管 K8s control plane，但 Instant Clusters 预装了 Kubernetes。Clanker 在自己的 K8s agent 下注册了 `verda-instant` provider，用来创建集群并从 head node 拉取 kubeconfig。在桌面 app 中，点击 Verda cluster 资源列表上的 "kubeconfig →" 按钮，即可获得可直接粘贴的 `ssh | sed` one-liner。

### MCP

Verda 通过 MCP 暴露为 `clanker_verda_ask` 和 `clanker_verda_list`，因此任何 MCP-compatible agent（Claude Desktop、Cursor、Zed 等）都能访问同一能力面：

```bash
clanker mcp --transport http --listen :39393
```

## 故障排查

AWS auth：

```bash
aws sts get-caller-identity --profile dev | cat
aws sso login --profile dev | cat
```

配置和 debug：

```bash
clanker config show | cat
clanker ask "test" --debug | cat
```

### Debug 输出

Clanker 有一个统一输出 flag：

- `--debug`：打印进度和内部诊断信息，例如工具选择、AWS CLI 调用、prompt 大小等。

示例：

```bash
clanker ask "what ec2 instances are running" --aws --debug | cat
clanker ask "show github actions status" --github --debug | cat
```

## 说明

- 支持 macOS、Linux 和 Windows；如有问题欢迎反馈。
