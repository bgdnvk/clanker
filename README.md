# Clanker CLI

Instead of losing your mind about why your infra isn't working just ask clanker what's the issue.  
EARLY ALPHA

DevOps Observability ChatOps and that kind of stuff.
Clanker only reads, and judges you, never modifies anything (yet).

## how to use

make install
add a yaml file with AWS bedrock or OpenAI key as LLM calls and your AWS infra profile
`clanker ask "what's the status of my chat service lambda?"`

## WHAT YOU NEED

Go, **AWS CLI v2** (v1 will give "Unknown options: --no-cli-pager" errors), OpenAI key or Claude on Bedrock (Anthropic and Gemini coming soon)

Install AWS CLI v2:

```bash
brew install awscli
```

## Read the .clanker.example.yaml

This tool is made to help move and check infra in different environments and clouds, we have AI LLM call provider and the INFRA we want to analyze, define those in your yaml!! READ THE YAML EXAMPLE!!!!  
We can change both with --ai-profile (for what AI provider to use to make LLM calls) and --profile (what infra/env you want to analyze) read the examples below.

After you read the yaml example just do make install.

ONLY TESTED ON MAC

## WHAT WORKS AND WHAT IVE TESTED

ask command works rly well, analyzing AWS infra on different AWS environments is pretty cool, OpenAI works and so does Claude 4 on AWS Bedrock (use claude 4 with bedrock if you can)

once you add your AI and INFRA profiles in the yaml you can just do `clanker ask "what's the last error from my big-api-service lambda?"`

I also have added keywords you can establish with your services, this will become a matrix later on for dependencies we modify.

## TOP Priority and what ill be working on

1. Terraform with workspaces
2. Github Actions
3. GCP and Azure support
4. Adding more integration and make it run with local models
5. Make Clanker available as a service so it can send you live updates on errors and so on
6. Memory and more context
7. More parallel calls to gather more AWS context and better prompts
8. Better security (the yaml should be fine but who knows)
9. Better code, ikr?

## Why build this?

I'm lazy and I want my job to be easier

## Who is this for?

Anyone dealing with infra/cloud stuff - especially those with multiple environments and different clouds. The last part is also why we do AWS CLI calls directly and don't use Golang packages

## contact

@ tekbog on twitter

## ----- Below is an AI slop summary with examples ------

## Features

-   🤖 **AI-Powered Natural Language Queries**: Ask questions about your AWS infrastructure, codebase, and GitHub repositories using multiple AI providers
-   ☁️ **Multi-Provider AI Support**: Choose between AWS Bedrock (Claude), OpenAI (GPT), Anthropic, and Google Gemini
-   🌍 **Multi-Environment AWS Integration**: Query different AWS environments (dev/stage/prod) with environment-based configuration
-   📁 **Intelligent Code Analysis**: Analyze local codebases with context-aware insights across multiple programming languages
-   🚀 **GitHub Integration**: Query workflows, pull requests, and repository information
-   🐘 **PostgreSQL Support**: Direct database operations and queries
-   🏗️ **Terraform Integration**: Infrastructure-as-code analysis and operations
-   🎛️ **Debug Mode**: Detailed logging for development and troubleshooting
-   📋 **Static Commands**: Direct AWS/GitHub queries without AI interpretation
-   � **Flexible Configuration**: Environment-based setup with comprehensive customization

## Installation

### Quick Install (Recommended)

```bash
git clone https://github.com/bgdnvk/clanker.git
cd clanker
make install  # Builds and installs to /usr/local/bin
```

## Quick Start

1. **Initialize configuration**:

    ```bash
    clanker config init
    ```

2. **Configure your environments** in `~/.clanker.yaml`

3. **Start asking questions**:

    ```bash
    # Basic AWS queries
    clanker ask "What EC2 instances are running?" --profile your-profile-here --aws

    # Multi-provider AI queries
    clanker ask "Analyze my S3 buckets" --ai-profile openai --aws

    # Combined infrastructure and code analysis
    clanker ask "How does my code connect to AWS?" --aws --code

    # Debug mode for detailed insights
    clanker ask "What are my VPCs?" --aws --debug
    ```

## The Ask Command

The `ask` command is the heart of Clanker, providing intelligent AI-powered queries with comprehensive context analysis. It supports multiple AI providers and can analyze AWS infrastructure, codebases, GitHub repositories, and more.

### Syntax

```bash
clanker ask "<your question>" [flags]
```

### Core Flags

-   `--ai-profile`: Choose AI provider (`openai`, `bedrock`, `anthropic`, `gemini`)
-   `--profile`: Select AWS profile for infrastructure queries
-   `--aws`: Include AWS infrastructure context
-   `--code`: Include local codebase analysis
-   `--github`: Include GitHub repository analysis
-   `--debug`: Enable detailed logging for troubleshooting

### Provider-Specific Examples

#### AWS Bedrock (Claude)

```bash
# Infrastructure analysis with debug
clanker ask "What security groups need attention?" --ai-profile bedrock --aws --debug

# Multi-environment infrastructure query
clanker ask "Compare EC2 instances across environments" --ai-profile bedrock --profile your-dev-profile-name --aws

# Combined code and infrastructure analysis
clanker ask "How does my code deploy to AWS?" --ai-profile bedrock --aws --code
```

#### OpenAI (GPT-5)

```bash
# Code analysis with OpenAI
clanker ask "Review my Go code for best practices" --ai-profile openai --code

# AWS architecture review
clanker ask "Analyze my Lambda functions" --ai-profile openai --aws

# GitHub workflow analysis
clanker ask "What's wrong with my CI/CD pipeline?" --ai-profile openai --github
```

#### Anthropic Claude

```bash
# Complex infrastructure reasoning
clanker ask "Design improvements for my AWS architecture" --ai-profile anthropic --aws

# Detailed code review
clanker ask "Identify security issues in my codebase" --ai-profile anthropic --code --debug
```

#### Google Gemini

```bash
# Performance analysis
clanker ask "Optimize my AWS costs" --ai-profile gemini --aws

# Code quality assessment
clanker ask "Suggest improvements for my Go project" --ai-profile gemini --code
```

### Advanced Query Examples

#### Multi-Context Analysis

```bash
# Combine all contexts for comprehensive analysis
clanker ask "How can I improve my deployment pipeline?" --ai-profile bedrock --aws --code --github

# Environment-specific queries
clanker ask "Why is staging slow?" --ai-profile openai --profile your-stage-profile --aws --code
```

#### Debug Mode Deep Dives

```bash
# Detailed AWS resource investigation
clanker ask "Investigate high costs" --ai-profile bedrock --aws --debug

# Code analysis with full context
clanker ask "Find performance bottlenecks" --ai-profile openai --code --debug
```

#### Targeted Analysis

```bash
# Focus on specific services
clanker ask "Review my Lambda cold starts" --ai-profile bedrock --aws

# Security-focused queries
clanker ask "Audit IAM policies" --ai-profile anthropic --aws

# Cost optimization
clanker ask "Find unused resources" --ai-profile gemini --aws
```

### Three-Stage Analysis Process

When using the `ask` command, Clanker employs a sophisticated three-stage analysis:

1. **Context Gathering**: Analyzes your query to determine required data sources
2. **Data Collection**: Executes AWS CLI commands, code analysis, or GitHub queries
3. **AI Processing**: Sends context and data to your chosen AI provider for intelligent analysis

### Tips for Better Results

-   **Be Specific**: "Show me EC2 instances with high CPU" vs "Show me servers"
-   **Try Different Providers**: Each AI has strengths (Bedrock for AWS, OpenAI for code, etc.)
-   **Use Debug Mode**: Add `--debug` to understand what data is being analyzed
-   **Environment Awareness**: Use `--profile` to query specific AWS environments
-   **COMING SOON: Use Context Flags**: Combine `--aws --code` for infrastructure-code relationship analysis

## Static Commands

For direct queries without AI interpretation:

### AWS Commands

```bash
# Infrastructure queries
clanker aws list ec2                    # List EC2 instances
clanker aws list lambda                 # List Lambda functions
clanker aws list rds                   # List RDS instances
clanker aws list s3                    # List S3 buckets
clanker aws list ecs                   # List ECS services
clanker aws list logs                  # List CloudWatch log groups

# AI/ML specific commands
clanker aws list bedrock-models        # List Bedrock foundation models
clanker aws list sagemaker-endpoints   # List SageMaker endpoints
clanker aws list sagemaker-models      # List SageMaker models
clanker aws list ecr-repositories      # List ECR repositories
clanker aws list lambda-ai             # List AI/ML Lambda functions

# With specific profiles
clanker aws list ec2 --profile your-stage-profile
clanker aws list lambda --profile your-prod-profile
```

### GitHub Commands

```bash
clanker github list workflows          # List GitHub Actions workflows
clanker github list runs              # List recent workflow runs
clanker github list prs               # List recent pull requests
clanker github status "CI/CD"         # Get status of specific workflow
```

### Code Analysis Commands

```bash
clanker code scan                      # Scan current directory
clanker code scan /path/to/project     # Scan specific path
clanker code search "authentication"   # Search for specific patterns
```

### Configuration Commands

```bash
clanker config init                    # Create default config
clanker config show                   # Show current configuration
clanker profiles                      # List available AWS profiles
```

## Configuration

### Complete Configuration Example

Create `~/.clanker.yaml` with your environment setup:

```yaml
# Infrastructure configuration per environment
infra:
    aws:
        environments:
            dev:
                profile: dev1
                region: us-west-1
            stage:
                profile: stage1
                region: us-west-1
            prod:
                profile: prod1
                region: us-east-1

# AI provider configuration
ai:
    openai:
        api_key: sk-your-openai-key-here
        model: gpt-5
    bedrock:
        region: us-east-1
        model: anthropic.claude-3-5-sonnet-20241022-v2:0
    anthropic:
        api_key: sk-ant-your-anthropic-key-here
        model: claude-3-5-sonnet-20241022
    gemini:
        api_key: your-gemini-key-here
        model: gemini-1.5-pro

# GitHub integration
github:
    token: ghp_your-github-token-here
    owner: your-username
    repo: your-repo-name

# PostgreSQL configuration (optional)
postgres:
    host: localhost
    port: 5432
    user: postgres
    password: your-password
    database: your-database

# Service keywords for intelligent routing
services:
    keywords:
        containers: [docker, k8s, kubernetes, ecs, fargate]
        serverless: [lambda, step-functions, api-gateway]
        storage: [s3, dynamodb, rds, aurora]
        networking: [vpc, subnet, security-group, load-balancer]
        monitoring: [cloudwatch, logs, metrics, alarms]
        cicd: [github-actions, workflow, pipeline, deploy]
```

## Development and Building

### Build Commands

```bash
make build      # Build binary to ./bin/clanker
make install    # Install to /usr/local/bin (system-wide)
make uninstall  # Remove from system
make help       # Show all available targets
```

## Troubleshooting

### AWS Authentication

```bash
# Check profile configuration
aws configure list --profile your-aws-profile

# Test AWS access
aws sts get-caller-identity --profile your-aws-profile

# Re-login if needed
aws sso login --profile your-aws-profile
```

### AI Provider Issues

```bash
# Verify configuration
clanker config show

# Test with debug mode
clanker ask "test" --ai-profile openai --debug
```

### Debug Mode

Add `--debug` to any command for detailed logging:

```bash
clanker ask "show me my lambdas" --aws --debug
```

## Troubleshooting

### AWS Authentication Issues

Make sure your AWS credentials are properly configured:

```bash
# Check configured profiles
aws configure list-profiles

# Check current credentials
aws sts get-caller-identity

# Check specific profile
aws sts get-caller-identity --profile dev

# Re-login if needed
aws sso login --profile commercial-dev
```

### AI Provider Issues

For Bedrock (recommended):

```bash
# Verify your commercial account has Bedrock access
aws bedrock list-foundation-models --profile commercial-dev

# Check if Claude 4 inference profile is available
aws bedrock list-inference-profiles --profile commercial-dev
```

For other providers:

```bash
# Verify your API key is correctly set
./bin/clanker config show
```

### Multi-Profile Issues

```bash
# List available profiles
aws configure list-profiles

# Test profile access
./bin/clanker aws list s3 --profile dev1
./bin/clanker aws list s3 --profile stage1

# Check profile configuration
cat ~/.aws/config
```

### Code Analysis Issues

Ensure the target directory contains source code files and is readable:

```bash
# Check directory permissions
ls -la /path/to/your/code

# Test code analysis
./bin/clanker code scan --help
```

### Build Issues

```bash
# Check Go version (requires 1.21+)
go version

# Clean and rebuild
make clean
make build

# Check dependencies
go mod tidy
go mod verify
```

Ask natural language questions about your infrastructure and code:

```bash
# AWS Infrastructure queries
./clanker ask "What EC2 instances are running?"
./clanker ask "Show me Lambda functions with high error rates"
./clanker ask "What's the current RDS instance status?"
./clanker ask "List all S3 buckets and their creation dates"

# Codebase queries
./clanker ask "Find all functions that use the user service"
./clanker ask "Show me error handling patterns in the code"
./clanker ask "What APIs are exposed by this service?"

# Combined queries
./clanker ask "How does this codebase connect to AWS services?"
```

### Direct AWS Queries

Get raw AWS data without AI interpretation:

```bash
./clanker aws list ec2         # List EC2 instances
./clanker aws list lambda      # List Lambda functions
./clanker aws list rds         # List RDS instances
./clanker aws list s3          # List S3 buckets
./clanker aws list ecs         # List ECS services
```

### Configuration Management

```bash
./clanker config init    # Create default config
./clanker config show    # Show current configuration
```

## Configuration

The configuration file is located at `~/.clanker.yaml`:

```yaml
# AI Provider Configuration
ai:
    provider: "openai" # Options: openai, anthropic
    api_key: "your-api-key-here"

# AWS Configuration
aws:
    profile: "default" # AWS profile to use
    region: "us-east-1" # Default AWS region

# Codebase Analysis
codebase:
    paths: # Paths to scan
        - "."
    exclude: # Patterns to exclude
        - "node_modules"
        - ".git"
        - "vendor"
        - "__pycache__"

# Logging
verbose: false
```

## Examples

### Infrastructure Monitoring

```bash
# Check system health
./clanker ask "Are there any failed services or unhealthy instances?"

# Resource utilization
./clanker ask "What's the current state of our Lambda functions?"

# Security audit
./clanker ask "Show me S3 buckets and their creation dates"
```

### Code Analysis

```bash
# Architecture understanding
./clanker ask "What's the overall architecture of this service?"

# Dependency analysis
./clanker ask "What external services does this code depend on?"

# Security review
./clanker ask "Find all database queries and API calls"
```
