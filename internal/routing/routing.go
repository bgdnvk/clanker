// Package routing provides query routing and classification for cloud services.
package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
	"github.com/spf13/viper"
)

// ServiceContext represents which services were detected in a query
type ServiceContext struct {
	AWS        bool
	GitHub     bool
	Terraform  bool
	K8s        bool
	GCP        bool
	Cloudflare bool
	Code       bool
}

// Classification represents the result of LLM-based query classification
type Classification struct {
	Service    string `json:"service"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
}

// InferContext analyzes a question and determines which cloud service contexts are relevant.
// Returns a ServiceContext with boolean flags for each detected service.
func InferContext(question string) ServiceContext {
	ctx := ServiceContext{}

	awsKeywords := []string{
		// Core services
		"ec2", "lambda", "rds", "s3", "ecs", "cloudwatch", "logs", "batch",
		"sqs", "sns", "dynamodb", "elasticache", "elb", "alb", "nlb", "route53",
		"cloudfront", "api-gateway", "cognito", "iam", "vpc", "subnet",
		"security-group", "nacl", "nat", "igw", "vpn", "direct-connect",
		// General terms that strongly indicate AWS context
		"instance", "bucket", "database", "aws", "resources", "infrastructure",
		"running", "account", "error", "log", "job", "queue", "compute",
		"storage", "network", "cdn", "load-balancer", "auto-scaling", "scaling",
		"health", "metric", "alarm", "notification", "backup", "snapshot",
		"ami", "volume", "ebs", "efs", "fsx",
		// ML/GPU
		"gpu", "cuda", "ml", "machine-learning", "training", "inference",
		"p2", "p3", "p4", "g3", "g4", "g5", "spot", "reserved", "dedicated",
		// Status keywords
		"status", "state", "healthy", "unhealthy", "available", "pending",
		"stopping", "stopped", "terminated", "creating", "deleting", "modifying",
		"active", "inactive", "enabled", "disabled",
		// Cost keywords
		"cost", "billing", "price", "usage", "spend", "budget",
		// Monitoring keywords
		"monitor", "trace", "debug", "performance", "latency", "throughput",
		"error-rate", "failure", "timeout", "retry",
		// Discovery keywords
		"services", "active", "deployed", "discovery", "overview", "summary",
		"list-all", "what's-running", "what-services", "infrastructure-overview",
	}

	githubKeywords := []string{
		// Platform
		"github", "git", "repository", "repo", "fork", "clone", "branch", "tag", "release",
		"issue", "discussion",
		// CI/CD
		"action", "workflow", "ci", "cd", "build", "deploy", "deployment",
		"pipeline", "job", "step", "runner", "artifact",
		// Collaboration
		"pr", "pull", "request", "merge", "commit", "push", "pull-request",
		"review", "approve", "comment", "assignee", "reviewer",
		// Project management
		"milestone", "project", "board", "epic", "story", "task", "bug",
		"feature", "enhancement", "label", "status",
		// Security
		"security", "vulnerability", "dependabot", "secret", "token",
		"permission", "access", "audit",
	}

	terraformKeywords := []string{
		// Core
		"terraform", "tf ", "hcl", "plan", "apply", "destroy", "init",
		"workspace", "state", "backend", "provider", "resource", "data",
		"module", "variable", "output", "local",
		// Operations
		"infrastructure-as-code", "iac", "provisioning", "deployment",
		"environment", "stack", "configuration", "template",
		// State management
		"tfstate", "state-file", "remote-state", "lock", "unlock",
		"drift", "refresh", "import", "taint", "untaint",
		// Environments
		"dev", "stage", "staging", "prod", "production", "qa", "environment", "workspace",
	}

	k8sKeywords := []string{
		// Core K8s terms
		"kubernetes", "k8s", "kubectl", "kube",
		// Workloads
		"pod", "pods", "deployment", "deployments", "replicaset", "statefulset",
		"daemonset", "job", "cronjob",
		// Networking
		"service", "services", "ingress", "loadbalancer", "nodeport", "clusterip",
		"networkpolicy", "endpoint",
		// Storage
		"pv", "pvc", "persistentvolume", "storageclass", "configmap", "secret",
		// Cluster
		"node", "nodes", "namespace", "cluster", "kubeconfig", "context",
		// Tools
		"helm", "chart", "release", "tiller",
		// Providers
		"eks", "kubeadm", "kops", "k3s", "minikube",
		// Operations
		"rollout", "scale", "drain", "cordon", "taint",
	}

	gcpKeywords := []string{
		"gcp", "google cloud", "cloud run", "cloudrun", "cloud sql", "cloudsql", "gke", "gcs", "cloud storage",
		"pubsub", "pub/sub", "cloud functions", "cloud function", "compute engine", "gce", "iam service account",
		"workload identity", "artifact registry", "secret manager", "bigquery", "spanner", "bigtable",
		"cloud build", "cloud deploy", "cloud dns", "cloud armor", "cloud load balancing", "api gateway",
	}

	cloudflareKeywords := []string{
		// Only match if Cloudflare is explicitly mentioned
		"cloudflare",
		// Cloudflare-specific CLI tools (unique to Cloudflare)
		"wrangler",
		"cloudflared",
	}

	questionLower := strings.ToLower(question)

	for _, keyword := range awsKeywords {
		if contains(questionLower, keyword) {
			ctx.AWS = true
			break
		}
	}

	for _, keyword := range githubKeywords {
		if contains(questionLower, keyword) {
			ctx.GitHub = true
			break
		}
	}

	for _, keyword := range terraformKeywords {
		if contains(questionLower, keyword) {
			ctx.Terraform = true
			break
		}
	}

	for _, keyword := range k8sKeywords {
		if contains(questionLower, keyword) {
			ctx.K8s = true
			break
		}
	}

	for _, keyword := range gcpKeywords {
		if contains(questionLower, keyword) {
			ctx.GCP = true
			break
		}
	}

	for _, keyword := range cloudflareKeywords {
		if contains(questionLower, keyword) {
			ctx.Cloudflare = true
			break
		}
	}

	// Default to AWS and GitHub context if nothing is detected
	if !ctx.AWS && !ctx.GitHub && !ctx.Terraform && !ctx.K8s && !ctx.GCP && !ctx.Cloudflare {
		ctx.AWS = true
		ctx.GitHub = true
	}

	return ctx
}

// GetClassificationPrompt returns a prompt for LLM to classify which service a query is about
func GetClassificationPrompt(question string) string {
	return fmt.Sprintf(`Classify which cloud service or platform this user query is about.

User Query: "%s"

Available services:
- cloudflare: Cloudflare CDN, DNS, Workers, KV, D1, R2, Pages, WAF, Tunnels, Zero Trust, Analytics
- aws: Amazon Web Services (EC2, Lambda, S3, RDS, VPC, Route53, CloudFront, IAM, ECS, etc.)
- k8s: Kubernetes clusters, pods, deployments, services, helm, kubectl
- gcp: Google Cloud Platform (Cloud Run, GKE, Cloud SQL, BigQuery, etc.)
- github: GitHub repositories, PRs, issues, actions, workflows
- terraform: Infrastructure as code, Terraform plans, state, modules
- general: General questions not specific to any cloud platform

IMPORTANT RULES:
1. Only classify as "cloudflare" if the query EXPLICITLY mentions Cloudflare, wrangler, cloudflared, or Cloudflare-specific products
2. Generic terms like "cdn", "cache", "dns", "worker", "waf", "rate limit", "tunnel" should default to AWS unless Cloudflare is explicitly mentioned
3. If the query mentions AWS services (EC2, Lambda, S3, CloudFront, Route53, etc.), classify as "aws"
4. If uncertain, classify as "aws" (the default cloud provider)

Respond with ONLY a JSON object:
{
    "service": "cloudflare|aws|k8s|gcp|github|terraform|general",
    "confidence": "high|medium|low",
    "reason": "brief explanation of why this classification"
}`, question)
}

// ClassifyWithLLM uses the AI client to determine which service a query is about.
// Returns the service name and any error encountered.
func ClassifyWithLLM(ctx context.Context, question string, debug bool) (string, error) {
	// Get provider config
	provider := viper.GetString("ai.default_provider")
	if provider == "" {
		provider = "openai"
	}

	var apiKey string
	switch provider {
	case "openai":
		apiKey = os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			apiKey = viper.GetString("ai.providers.openai.api_key")
		}
	case "anthropic":
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			apiKey = viper.GetString("ai.providers.anthropic.api_key")
		}
	case "gemini", "gemini-api":
		apiKey = os.Getenv("GEMINI_API_KEY")
	}

	// Create minimal AI client for classification
	aiClient := ai.NewClient(provider, apiKey, debug, "")

	prompt := GetClassificationPrompt(question)
	response, err := aiClient.AskPrompt(ctx, prompt)
	if err != nil {
		if debug {
			fmt.Printf("[routing] LLM classification failed: %v, falling back to keyword matching\n", err)
		}
		return "", err
	}

	// Parse the JSON response
	var classification Classification

	// Clean response and parse JSON
	cleaned := aiClient.CleanJSONResponse(response)
	if err := json.Unmarshal([]byte(cleaned), &classification); err != nil {
		if debug {
			fmt.Printf("[routing] Failed to parse classification response: %v\n", err)
		}
		return "", err
	}

	if debug {
		fmt.Printf("[routing] LLM classification: service=%s, confidence=%s, reason=%s\n",
			classification.Service, classification.Confidence, classification.Reason)
	}

	return classification.Service, nil
}

// NeedsLLMClassification determines if a query needs LLM classification
// based on ambiguity (multiple services detected) or Cloudflare being inferred.
func NeedsLLMClassification(ctx ServiceContext) bool {
	// Count how many services were inferred
	count := 0
	if ctx.AWS {
		count++
	}
	if ctx.K8s {
		count++
	}
	if ctx.GCP {
		count++
	}
	if ctx.Cloudflare {
		count++
	}

	// Use LLM classification if:
	// 1. Multiple services inferred (ambiguous)
	// 2. Cloudflare was inferred (verify it's actually Cloudflare-related)
	return count > 1 || ctx.Cloudflare
}

// ApplyLLMClassification updates the ServiceContext based on LLM classification result
func ApplyLLMClassification(ctx *ServiceContext, llmService string) {
	switch llmService {
	case "cloudflare":
		ctx.Cloudflare = true
		ctx.K8s = false
		ctx.GCP = false
		ctx.AWS = false
	case "k8s":
		ctx.K8s = true
		ctx.Cloudflare = false
		ctx.GCP = false
	case "gcp":
		ctx.GCP = true
		ctx.Cloudflare = false
		ctx.K8s = false
	case "aws":
		ctx.AWS = true
		ctx.Cloudflare = false
		ctx.K8s = false
		ctx.GCP = false
	case "terraform":
		ctx.Terraform = true
		ctx.Cloudflare = false
	case "github":
		ctx.GitHub = true
		ctx.Cloudflare = false
	default:
		// "general" - default to AWS
		ctx.AWS = true
		ctx.Cloudflare = false
		ctx.K8s = false
	}
}

// contains checks if s contains substr (case-insensitive)
func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
