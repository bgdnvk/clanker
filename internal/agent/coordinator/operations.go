package coordinator

import (
	"strings"

	"github.com/bgdnvk/clanker/internal/agent/model"
	awsclient "github.com/bgdnvk/clanker/internal/aws"
)

type operationGenerator func(*model.AgentContext, model.AWSData) []awsclient.LLMOperation

var operationCatalog = map[string]operationGenerator{
	"log":            generateLogOperations,
	"metrics":        generateMetricsOperations,
	"infrastructure": generateInfrastructureOperations,
	"security":       generateSecurityOperations,
	"cost":           generateCostOperations,
	"performance":    generatePerformanceOperations,
	"deployment":     generateDeploymentOperations,
	"datapipeline":   generateDataPipelineOperations,
	"queue":          generateQueueOperations,
	"availability":   generateAvailabilityOperations,
	"llm":            generateLLMOperations,
}

func (c *Coordinator) operationsFor(agentType AgentType, params model.AWSData) []awsclient.LLMOperation {
	if generator, ok := operationCatalog[agentType.Name]; ok {
		return generator(c.MainContext, params)
	}
	return nil
}

func generateLogOperations(ctx *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	if len(ctx.ServiceData) > 0 {
		return []awsclient.LLMOperation{
			{Operation: "investigate_service_logs", Reason: "Investigate logs for already discovered services", Parameters: map[string]any{"query": ctx.OriginalQuery, "skip_discovery": true}},
		}
	}
	return []awsclient.LLMOperation{
		{Operation: "discover_services", Reason: "Discover services matching the query", Parameters: map[string]any{"query": ctx.OriginalQuery}},
		{Operation: "investigate_service_logs", Reason: "Investigate logs for discovered services", Parameters: map[string]any{"query": ctx.OriginalQuery}},
	}
}

func generateMetricsOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{{Operation: "list_cloudwatch_alarms", Reason: "Get CloudWatch alarms for performance issues", Parameters: map[string]any{}}}
}

func generateInfrastructureOperations(ctx *model.AgentContext, params model.AWSData) []awsclient.LLMOperation {
	priority := "medium"
	if p, ok := params["priority"].(string); ok {
		priority = p
	}

	// Check semantic analysis for service-specific operations
	query := ""
	if ctx != nil {
		query = strings.ToLower(ctx.OriginalQuery)
	}

	// EC2/Instance queries
	if strings.Contains(query, "ec2") || strings.Contains(query, "instance") {
		return []awsclient.LLMOperation{
			{Operation: "list_ec2_instances", Reason: "List EC2 instances", Parameters: map[string]any{}},
			{Operation: "describe_auto_scaling_groups", Reason: "Check Auto Scaling groups", Parameters: map[string]any{}},
		}
	}

	// RDS queries
	if strings.Contains(query, "rds") || strings.Contains(query, "database") {
		return []awsclient.LLMOperation{
			{Operation: "list_rds_instances", Reason: "List RDS instances", Parameters: map[string]any{}},
		}
	}

	// ECS queries
	if strings.Contains(query, "ecs") || strings.Contains(query, "container") {
		return []awsclient.LLMOperation{
			{Operation: "describe_ecs_clusters", Reason: "List ECS clusters", Parameters: map[string]any{}},
		}
	}

	// S3 queries
	if strings.Contains(query, "s3") || strings.Contains(query, "bucket") {
		return []awsclient.LLMOperation{
			{Operation: "list_s3_buckets", Reason: "List S3 buckets", Parameters: map[string]any{}},
		}
	}

	// VPC/Network queries
	if strings.Contains(query, "vpc") || strings.Contains(query, "subnet") || strings.Contains(query, "network") {
		return []awsclient.LLMOperation{
			{Operation: "list_vpcs", Reason: "List VPCs", Parameters: map[string]any{}},
			{Operation: "list_subnets", Reason: "List subnets", Parameters: map[string]any{}},
		}
	}

	// Default infrastructure operations (include EC2 for general queries)
	if priority == "high" || priority == "critical" {
		return []awsclient.LLMOperation{
			{Operation: "list_ec2_instances", Reason: "Quick EC2 discovery", Parameters: map[string]any{}},
			{Operation: "list_lambda_functions", Reason: "Quick Lambda discovery", Parameters: map[string]any{}},
			{Operation: "describe_log_groups", Reason: "Get log groups", Parameters: map[string]any{}},
		}
	}
	return []awsclient.LLMOperation{
		{Operation: "list_ec2_instances", Reason: "List EC2 instances", Parameters: map[string]any{}},
		{Operation: "list_lambda_functions", Reason: "Broader Lambda discovery", Parameters: map[string]any{}},
		{Operation: "describe_log_groups", Reason: "Discover log groups", Parameters: map[string]any{}},
		{Operation: "describe_ecs_clusters", Reason: "Check ECS clusters", Parameters: map[string]any{}},
	}
}

func generateSecurityOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{{Operation: "describe_guardduty_findings", Reason: "Check GuardDuty alerts", Parameters: map[string]any{}}}
}

func generateCostOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{{Operation: "get_cost_and_usage", Reason: "Analyze spending", Parameters: map[string]any{}}}
}

func generatePerformanceOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{{Operation: "describe_auto_scaling_groups", Reason: "Check scaling state", Parameters: map[string]any{}}}
}

func generateDeploymentOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{
		{Operation: "list_codepipelines", Reason: "List active deployment pipelines", Parameters: map[string]any{}},
		{Operation: "list_codebuild_projects", Reason: "Check build projects for recent failures", Parameters: map[string]any{}},
	}
}

func generateDataPipelineOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{
		{Operation: "list_glue_jobs", Reason: "Inspect Glue/ETL jobs", Parameters: map[string]any{}},
		{Operation: "list_step_functions", Reason: "Check orchestration state machines", Parameters: map[string]any{}},
		{Operation: "list_kinesis_streams", Reason: "Review streaming pipelines", Parameters: map[string]any{}},
	}
}

func generateQueueOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{
		{Operation: "list_sqs_queues", Reason: "List queues and their attributes", Parameters: map[string]any{}},
		{Operation: "list_sns_topics", Reason: "Review SNS topics feeding queues", Parameters: map[string]any{}},
	}
}

func generateAvailabilityOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{
		{Operation: "check_route53_service", Reason: "Check DNS health", Parameters: map[string]any{}},
		{Operation: "list_route53_zones", Reason: "Inspect hosted zones for issues", Parameters: map[string]any{}},
	}
}

func generateLLMOperations(_ *model.AgentContext, _ model.AWSData) []awsclient.LLMOperation {
	return []awsclient.LLMOperation{
		{Operation: "list_bedrock_foundation_models", Reason: "Review Bedrock model status", Parameters: map[string]any{}},
		{Operation: "list_sagemaker_endpoints", Reason: "Check SageMaker endpoint health", Parameters: map[string]any{}},
		{Operation: "list_sagemaker_models", Reason: "Reference deployed models", Parameters: map[string]any{}},
	}
}
