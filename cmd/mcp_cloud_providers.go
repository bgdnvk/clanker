package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/aws"
	"github.com/bgdnvk/clanker/internal/azure"
	"github.com/bgdnvk/clanker/internal/cloudflare"
	"github.com/bgdnvk/clanker/internal/digitalocean"
	"github.com/bgdnvk/clanker/internal/gcp"
	"github.com/bgdnvk/clanker/internal/hetzner"
	"github.com/mark3labs/mcp-go/mcp"
	mcptransport "github.com/mark3labs/mcp-go/server"
)

type cloudProviderMCPConfig struct {
	Key          string
	DisplayName  string
	Command      string
	ResourceHelp string
}

var cloudProviderMCPConfigs = []cloudProviderMCPConfig{
	{
		Key:          "aws",
		DisplayName:  "AWS",
		Command:      "aws",
		ResourceHelp: "all-services, resources, tagged-resources, ec2, ecs, batch, app-runner, asg, launch-templates, lambda, layers, step-functions, ecr, eks, s3, ebs, efs, rds, rds-clusters, dynamodb, elasticache, vpcs, subnets, security-groups, load-balancers, route-tables, sqs, sns, eventbridge, eventbridge-buses, eventbridge-schedules, eventbridge-pipes, kinesis, logs, alarms, iam-roles, iam-groups, iam-users, kms, certificates, secrets, ssm-parameters, waf-webacls, verified-permissions, security-lake, codebuild, codepipeline, codecommit, cloudformation, glue-jobs, glue-databases, emr-clusters, bedrock-models, bedrock-custom, bedrock-agents, bedrock-kb, bedrock-guardrails, qbusiness, datazone, sagemaker-endpoints, sagemaker-models, sagemaker-jobs, sagemaker-notebooks, comprehend-jobs, textract-jobs, rekognition-collections, budgets, api-gateways, cloudfront, route53",
	},
	{
		Key:          "gcp",
		DisplayName:  "Google Cloud",
		Command:      "gcp",
		ResourceHelp: "services, available-services, resources, iam, iam-roles, cloudrun, run-jobs, run-revisions, run-worker-pools, run-domain-mappings, run-multi-region, workflows, batch-jobs, vertex-endpoints, vertex-indexes, firestore, firebase-apps, compute, instance-groups, networks, subnets, firewall, load-balancers, armor, dns, gke, cloudsql, alloydb, bigquery, spanner, bigtable, redis, memcache, composer, gcs, artifacts, functions, functions-gen2, pubsub, subscriptions, tasks, scheduler, eventarc-triggers, secrets, kms, build-triggers, deploy-pipelines, logging-sinks, alert-policies, api-gateway",
	},
	{
		Key:          "azure",
		DisplayName:  "Azure",
		Command:      "azure",
		ResourceHelp: "account, groups, resources, resource-graph, vms, managed-disks, snapshots, containers, aks, containerapps, webapps, functionapps, static-webapps, acr, storage, keyvaults, cosmosdb, sql-servers, sql-databases, postgres, mysql, redis, ai-services, ai-search, servicebus, eventhubs, eventgrid, apim, log-analytics, app-insights, front-door, vnets, private-endpoints, nsgs, route-tables, app-gateways, waf-policies, dns-zones, private-dns-zones, public-ips, load-balancers, logic-apps, data-factories, ml-workspaces",
	},
	{
		Key:          "cloudflare",
		DisplayName:  "Cloudflare",
		Command:      "cf",
		ResourceHelp: "zones, records, workers, pages, kv-namespaces, d1-databases, r2-buckets, queues, vectorize, hyperdrive, ai-gateways, ai-gateway-logs, ai-gateway-datasets, ai-gateway-evals, ai-gateway-providers, ai-gateway-routes, ai-search, ai-search-instances, durable-objects, browser-sessions, images, stream, secrets-stores, pipelines, pipeline-sinks, pipeline-streams, turnstile, tunnels, workflows, logpush-jobs, rules-lists, account-roles, account-members, firewall-rules, page-rules",
	},
	{
		Key:          "digitalocean",
		DisplayName:  "DigitalOcean",
		Command:      "do",
		ResourceHelp: "account, actions, droplets, droplet-autoscale, kubernetes, databases, spaces, spaces-keys, apps, functions, function-namespaces, serverless-inference-models, dedicated-inference, dedicated-inference-sizes, gradient-agents, gradient-models, gradient-regions, gradient-knowledge-bases, gradient-openai-keys, load-balancers, cdns, volumes, nfs, nfs-snapshots, vpcs, vpc-peerings, vpc-nat-gateways, domains, firewalls, reserved-ips, reserved-ipv6, certificates, images, snapshots, sizes, regions, ssh-keys, tags, one-clicks, monitoring-alerts, uptime-checks, uptime-alerts, network-attachments, byoip-prefixes, security-scans, projects, project-resources, registries",
	},
	{
		Key:          "hetzner",
		DisplayName:  "Hetzner Cloud",
		Command:      "hetzner",
		ResourceHelp: "servers, load-balancers, volumes, networks, firewalls, floating-ips, primary-ips, ssh-keys, images, isos, certificates, placement-groups, server-types, locations, datacenters",
	},
}

func registerCloudProviderMCPTools(server *mcptransport.MCPServer) {
	registerCloudMCPCatalogTool(server)
	for _, cfg := range cloudProviderMCPConfigs {
		registerCloudProviderAskTool(server, cfg)
		registerCloudProviderListTool(server, cfg)
	}
}

func registerCloudMCPCatalogTool(server *mcptransport.MCPServer) {
	server.AddTool(
		mcp.NewTool(
			"clanker_cloud_mcp_catalog",
			mcp.WithDescription("Return the local Clanker cloud-provider MCP tools plus official provider MCP servers that can complement Clanker's local inventory coverage."),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultJSON(map[string]any{
				"local_clanker_tools": []map[string]string{
					{"provider": "aws", "ask_tool": "clanker_aws_ask", "list_tool": "clanker_aws_list"},
					{"provider": "gcp", "ask_tool": "clanker_gcp_ask", "list_tool": "clanker_gcp_list"},
					{"provider": "azure", "ask_tool": "clanker_azure_ask", "list_tool": "clanker_azure_list"},
					{"provider": "cloudflare", "ask_tool": "clanker_cloudflare_ask", "list_tool": "clanker_cloudflare_list"},
					{"provider": "digitalocean", "ask_tool": "clanker_digitalocean_ask", "list_tool": "clanker_digitalocean_list"},
					{"provider": "hetzner", "ask_tool": "clanker_hetzner_ask", "list_tool": "clanker_hetzner_list"},
				},
				"official_remote_mcps": []map[string]string{
					{"provider": "Cloudflare", "name": "Cloudflare API MCP server", "transport": "streamable-http", "endpoint": "https://mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare Docs MCP server", "transport": "streamable-http", "endpoint": "https://docs.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare Workers Bindings MCP server", "transport": "streamable-http", "endpoint": "https://bindings.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare Workers Builds MCP server", "transport": "streamable-http", "endpoint": "https://builds.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare Observability MCP server", "transport": "streamable-http", "endpoint": "https://observability.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare Radar MCP server", "transport": "streamable-http", "endpoint": "https://radar.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare Browser Run MCP server", "transport": "streamable-http", "endpoint": "https://browser.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare AI Gateway MCP server", "transport": "streamable-http", "endpoint": "https://ai-gateway.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare AI Search MCP server", "transport": "streamable-http", "endpoint": "https://autorag.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "Cloudflare", "name": "Cloudflare GraphQL MCP server", "transport": "streamable-http", "endpoint": "https://graphql.mcp.cloudflare.com/mcp", "docs": "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
					{"provider": "AWS", "name": "AWS MCP Servers", "transport": "varies by server", "endpoint": "https://awslabs.github.io/mcp/", "docs": "https://awslabs.github.io/mcp/"},
					{"provider": "Azure", "name": "Azure MCP Server", "transport": "stdio/local server", "endpoint": "https://learn.microsoft.com/en-us/azure/developer/azure-mcp-server/", "docs": "https://learn.microsoft.com/en-us/azure/developer/azure-mcp-server/"},
					{"provider": "Google Cloud", "name": "Google Cloud remote MCP servers", "transport": "streamable-http", "endpoint": "https://docs.cloud.google.com/mcp", "docs": "https://docs.cloud.google.com/mcp"},
					{"provider": "Microsoft Learn", "name": "Microsoft Learn MCP Server", "transport": "streamable-http", "endpoint": "https://learn.microsoft.com/api/mcp", "docs": "https://learn.microsoft.com/en-us/training/support/mcp"},
					{"provider": "DigitalOcean", "name": "DigitalOcean MCP services", "transport": "remote MCP", "endpoint": "https://docs.digitalocean.com/reference/mcp/mcp-tools/", "docs": "https://docs.digitalocean.com/reference/mcp/mcp-tools/"},
				},
			})
		},
	)
}

func registerCloudProviderAskTool(server *mcptransport.MCPServer, cfg cloudProviderMCPConfig) {
	server.AddTool(
		mcp.NewTool(
			"clanker_"+cfg.Key+"_ask",
			mcp.WithDescription(fmt.Sprintf("Ask a natural language question about %s resources. Gathers read-only local provider context and answers using the configured Clanker AI provider.", cfg.DisplayName)),
			mcp.WithString("question", mcp.Required(), mcp.Description("Natural language question about this provider's infrastructure.")),
			mcp.WithString("profile", mcp.Description("AWS profile for AWS tools.")),
			mcp.WithString("region", mcp.Description("AWS region or DigitalOcean Gradient region for region-scoped resources.")),
			mcp.WithString("project", mcp.Description("Google Cloud project ID.")),
			mcp.WithString("subscription", mcp.Description("Azure subscription ID.")),
			mcp.WithString("account_id", mcp.Description("Cloudflare account ID.")),
			mcp.WithString("token", mcp.Description("Provider API token for Cloudflare, DigitalOcean, or Hetzner.")),
			mcp.WithBoolean("debug", mcp.Description("Enable provider debug output.")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPCloudProviderAsk(ctx, req, cfg.Key, cfg.DisplayName)
		},
	)
}

func registerCloudProviderListTool(server *mcptransport.MCPServer, cfg cloudProviderMCPConfig) {
	server.AddTool(
		mcp.NewTool(
			"clanker_"+cfg.Key+"_list",
			mcp.WithDescription(fmt.Sprintf("List %s resources through Clanker's local CLI provider. Supported resources: %s.", cfg.DisplayName, cfg.ResourceHelp)),
			mcp.WithString("resource", mcp.Required(), mcp.Description("Resource type to list. Supported resources: "+cfg.ResourceHelp)),
			mcp.WithString("profile", mcp.Description("AWS profile for AWS list commands.")),
			mcp.WithString("environment", mcp.Description("AWS environment name from Clanker config.")),
			mcp.WithString("project", mcp.Description("Google Cloud project ID.")),
			mcp.WithString("location", mcp.Description("Google Cloud location/region for region-scoped resources.")),
			mcp.WithString("subscription", mcp.Description("Azure subscription ID.")),
			mcp.WithString("zone", mcp.Description("Cloudflare zone ID for zone-scoped resources.")),
			mcp.WithString("zone_name", mcp.Description("Cloudflare zone name to resolve for zone-scoped resources.")),
			mcp.WithString("gateway_id", mcp.Description("Cloudflare AI Gateway ID for gateway-scoped resources.")),
			mcp.WithString("namespace", mcp.Description("Cloudflare AI Search namespace for namespace-scoped resources.")),
			mcp.WithString("region", mcp.Description("DigitalOcean Gradient region for region-scoped resources.")),
			mcp.WithString("project_id", mcp.Description("DigitalOcean project ID for project-scoped resources.")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handleMCPCloudProviderList(ctx, req, cfg)
		},
	)
}

func handleMCPCloudProviderAsk(ctx context.Context, req mcp.CallToolRequest, provider string, displayName string) (*mcp.CallToolResult, error) {
	question := strings.TrimSpace(strParam(req, "question"))
	if question == "" {
		return mcp.NewToolResultError("question is required"), nil
	}
	debug := boolParam(req, "debug", false)

	providerCtx, err := collectMCPCloudProviderContext(ctx, req, provider, debug)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	prompt := buildMCPCloudProviderPrompt(displayName, question, providerCtx)
	response, err := newConfiguredAIClient(debug).AskPrompt(ctx, prompt)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("AI query failed: %v", err)), nil
	}
	return mcp.NewToolResultText(response), nil
}

func collectMCPCloudProviderContext(ctx context.Context, req mcp.CallToolRequest, provider string, debug bool) (string, error) {
	question := mcpCloudProviderContextQuestion(provider, strParam(req, "question"))
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	switch provider {
	case "aws":
		profile := strParam(req, "profile")
		if profile == "" {
			profile = resolveAWSProfile("")
		}
		client, err := aws.NewClientWithProfileAndDebug(ctx, profile, debug)
		if err != nil {
			return "", fmt.Errorf("failed to create AWS client: %w", err)
		}
		return client.GetRelevantContext(ctx, question)
	case "gcp":
		projectID := strParam(req, "project")
		if projectID == "" {
			projectID = gcp.ResolveProjectID()
		}
		client, err := gcp.NewClient(projectID, debug)
		if err != nil {
			return "", fmt.Errorf("failed to create GCP client: %w", err)
		}
		return client.GetRelevantContext(ctx, question)
	case "azure":
		subscriptionID := strParam(req, "subscription")
		if subscriptionID == "" {
			subscriptionID = azure.ResolveSubscriptionID()
		}
		client, err := azure.NewClient(subscriptionID, debug)
		if err != nil {
			return "", fmt.Errorf("failed to create Azure client: %w", err)
		}
		return client.GetRelevantContext(ctx, question)
	case "cloudflare":
		accountID := strParam(req, "account_id")
		if accountID == "" {
			accountID = cloudflare.ResolveAccountID()
		}
		token := strParam(req, "token")
		if token == "" {
			token = cloudflare.ResolveAPIToken()
		}
		client, err := cloudflare.NewClient(accountID, token, debug)
		if err != nil {
			return "", fmt.Errorf("failed to create Cloudflare client: %w", err)
		}
		return client.GetRelevantContext(ctx, question)
	case "digitalocean":
		token := strParam(req, "token")
		if token == "" {
			token = digitalocean.ResolveAPIToken()
		}
		client, err := digitalocean.NewClient(token, debug)
		if err != nil {
			return "", fmt.Errorf("failed to create DigitalOcean client: %w", err)
		}
		return client.GetRelevantContext(ctx, question)
	case "hetzner":
		token := strParam(req, "token")
		if token == "" {
			token = hetzner.ResolveAPIToken()
		}
		client, err := hetzner.NewClient(token, debug)
		if err != nil {
			return "", fmt.Errorf("failed to create Hetzner client: %w", err)
		}
		return client.GetRelevantContext(ctx, question)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func mcpCloudProviderContextQuestion(provider, question string) string {
	base := strings.TrimSpace(question)
	hints := map[string]string{
		"aws":          "resource explorer tagged resources all services ec2 lambda layers step functions rds s3 ecs ecr eks app runner cloudformation glue emr sqs sns eventbridge scheduler pipes kinesis dynamodb elasticache bedrock qbusiness datazone verified permissions security lake cloudwatch logs alarms iam kms secrets ssm waf route53 cloudfront api gateway",
		"gcp":          "services cloud asset inventory cloud run revisions worker pools multi region workflows batch vertex ai firestore firebase compute gke cloud sql alloydb bigquery spanner bigtable memorystore storage artifact registry functions pubsub eventarc cloud build cloud deploy api gateway",
		"azure":        "resource groups resource graph vms managed disks snapshots container apps static web apps app service functions storage key vault cosmos db azure sql postgres mysql redis azure ai search cognitive services service bus event hubs event grid api management log analytics application insights front door private endpoints dns zones logic apps data factory ml workspaces vnet nsg aks",
		"cloudflare":   "zones workers pages kv d1 r2 queues vectorize hyperdrive ai gateway logs datasets evaluations provider configs ai search namespaces browser rendering images stream secrets store pipelines durable objects turnstile tunnels workflows logpush dns waf firewall rules page rules account roles members",
		"digitalocean": "droplets autoscale kubernetes databases spaces app platform serverless functions serverless inference dedicated inference registries gradient agents gradient models gradient knowledge bases load balancers cdns volumes nfs vpcs vpc peerings nat gateways domains firewalls reserved ips certificates images snapshots sizes regions ssh keys tags monitoring uptime security scans projects",
		"hetzner":      "servers load balancers volumes networks firewalls floating ips primary ips ssh keys images isos certificates placement groups server types locations datacenters",
	}
	if base == "" {
		return hints[provider]
	}
	return base + "\n\nProvider coverage hints: " + hints[provider]
}

func buildMCPCloudProviderPrompt(displayName string, question string, providerCtx string) string {
	var b strings.Builder
	b.WriteString("You are Clanker's ")
	b.WriteString(displayName)
	b.WriteString(" cloud provider assistant.\n\n")
	b.WriteString("Use only the read-only evidence below. Separate observed facts from recommendations. If a service is missing from the evidence, say it was not available from current credentials, permissions, CLI support, or live resources. Do not invent resources, regions, IDs, costs, or state.\n\n")
	b.WriteString("Evidence:\n")
	if strings.TrimSpace(providerCtx) == "" {
		b.WriteString("No live provider evidence was collected.\n")
	} else {
		b.WriteString(providerCtx)
		b.WriteString("\n")
	}
	b.WriteString("\nUser Question: ")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\nAnswer as a concise operator report.")
	return b.String()
}

func handleMCPCloudProviderList(ctx context.Context, req mcp.CallToolRequest, cfg cloudProviderMCPConfig) (*mcp.CallToolResult, error) {
	resource := strParam(req, "resource")
	if resource == "" {
		return mcp.NewToolResultError("resource is required"), nil
	}

	args := []string{cfg.Command, "list", resource}
	switch cfg.Key {
	case "aws":
		args = mcpAppendIf(args, "--profile", strParam(req, "profile"))
		args = mcpAppendIf(args, "--environment", strParam(req, "environment"))
	case "gcp":
		args = mcpAppendIf(args, "--project", strParam(req, "project"))
		args = mcpAppendIf(args, "--location", strParam(req, "location"))
	case "azure":
		args = mcpAppendIf(args, "--subscription", strParam(req, "subscription"))
	case "cloudflare":
		args = mcpAppendIf(args, "--zone", strParam(req, "zone"))
		args = mcpAppendIf(args, "--zone-name", strParam(req, "zone_name"))
		args = mcpAppendIf(args, "--gateway-id", strParam(req, "gateway_id"))
		args = mcpAppendIf(args, "--namespace", strParam(req, "namespace"))
	case "digitalocean":
		args = mcpAppendIf(args, "--region", strParam(req, "region"))
		args = mcpAppendIf(args, "--project-id", strParam(req, "project_id"))
	}

	result, err := runClankerCommand(ctx, commandArgs{Args: args})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(result)
}
