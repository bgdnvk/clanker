package aws

import "fmt"

// GetLLMAnalysisPrompt returns the prompt for LLM to analyze what AWS operations are needed
func GetLLMAnalysisPrompt(question string) string {
	return fmt.Sprintf(`Analyze this user query and determine what AWS operations would be needed to answer it accurately. 

User Query: "%s"

Available AWS READ-ONLY operations (all are safe and never modify/delete anything):

INFRASTRUCTURE DISCOVERY (New Enhanced Operations):
- discover_all_active_services: Automatically discover all active AWS services by running service checks in parallel
- get_infrastructure_overview: Get a comprehensive overview of the entire infrastructure across all services
- check_all_services_parallel: Run all service availability checks in parallel to map the infrastructure

TERRAFORM INTEGRATION:
- get_terraform_outputs: Get terraform outputs from the configured workspace
- get_terraform_state_summary: Get a summary of terraform state resources

SERVICE EXISTENCE CHECKS (Quick checks to see if services exist and their basic counts):
- check_sqs_service: Check if SQS service is available and count queues
- check_eventbridge_service: Check if EventBridge service is available and count rules
- check_lambda_service: Check if Lambda service is available and count functions
- check_sns_service: Check if SNS service is available and count topics
- check_dynamodb_service: Check if DynamoDB service is available and count tables
- check_s3_service: Check if S3 service is available and count buckets
- check_rds_service: Check if RDS service is available and count instances
- check_ec2_service: Check if EC2 service is available and count instances
- check_ecs_service: Check if ECS service is available and count clusters
- check_ecr_service: Check if ECR service is available and count repositories

COMPUTE:
- list_ec2_instances: List EC2 instances with state, type, and details
- describe_instance: Get detailed info about a specific EC2 instance
- list_ecs_clusters: List ECS clusters and their running services/tasks
- describe_ecs_service: Get details about a specific ECS service
- list_batch_jobs: List AWS Batch jobs and their status
- list_auto_scaling_groups: List Auto Scaling Groups with instance counts and capacity
- describe_auto_scaling_group: Get detailed ASG configuration and instances
- list_launch_templates: List EC2 Launch Templates and their versions
- describe_launch_template: Get detailed Launch Template configuration

SERVERLESS:
- list_lambda_functions: List Lambda functions with runtime and last modified
- describe_lambda_function: Get detailed config for a specific Lambda function
- list_lambda_layers: List Lambda layers available

CONTAINER SERVICES:
- list_ecr_repositories: List ECR repositories with URIs and creation dates
- describe_ecr_repository: Get images and details for a specific ECR repository
- list_eks_clusters: List EKS Kubernetes clusters with status and details
- describe_eks_cluster: Get detailed EKS cluster configuration

STORAGE:
- list_s3_buckets: List S3 buckets with creation dates and regions
- describe_s3_bucket: Get details about a specific S3 bucket (size, objects, etc.)
- list_ebs_volumes: List EBS volumes and their attachments, size, type
- describe_ebs_volume: Get detailed info about a specific EBS volume
- list_efs_filesystems: List EFS file systems with performance modes
- describe_efs_filesystem: Get detailed EFS configuration and mount targets

DATABASE:
- list_rds_instances: List RDS database instances with status and config
- describe_rds_instance: Get detailed info about a specific RDS instance
- list_rds_clusters: List RDS Aurora clusters with engine and status
- list_dynamodb_tables: List DynamoDB tables
- describe_dynamodb_table: Get detailed DynamoDB table schema and settings

NETWORKING:
- list_vpcs: List VPCs and their CIDR blocks
- list_subnets: List subnets across VPCs
- list_security_groups: List security groups and their rules
- describe_load_balancers: List and describe load balancers (ALB/NLB/CLB)
- list_route_tables: List route tables and their routes

MESSAGE QUEUING & EVENTS:
- list_sqs_queues: List SQS queues with URLs and attributes
- describe_sqs_queue: Get detailed SQS queue configuration and metrics
- list_sns_topics: List SNS topics and their ARNs
- describe_sns_topic: Get SNS topic configuration and subscriptions
- list_eventbridge_rules: List EventBridge rules with schedules and targets
- list_eventbridge_buses: List custom EventBridge event buses

MONITORING & LOGS:
- get_recent_logs: Get recent CloudWatch logs and errors
- list_cloudwatch_alarms: List CloudWatch alarms and their status
- describe_cloudwatch_metrics: Get CloudWatch metrics for resources
- list_log_groups: List CloudWatch log groups

SECURITY & IAM:
- list_iam_roles: List IAM roles (names only, no sensitive data)
- list_iam_groups: List IAM groups (names only, no sensitive data)
- list_iam_users: List IAM users (names only, no sensitive data)
- describe_security_groups: Get security group rules and associations
- list_kms_keys: List KMS encryption keys
- describe_kms_key: Get KMS key details and policies
- list_acm_certificates: List SSL/TLS certificates with status
- describe_acm_certificate: Get certificate details and validation
- list_waf_webacls: List WAF Web ACLs for regional and CloudFront

DEVOPS & CI/CD:
- list_codebuild_projects: List CodeBuild projects with build history
- list_codepipelines: List CodePipeline pipelines with status
- describe_codepipeline: Get detailed pipeline configuration and stages
- list_codecommit_repositories: List CodeCommit Git repositories

ANALYTICS & BIG DATA:
- list_kinesis_streams: List Kinesis data streams
- describe_kinesis_stream: Get Kinesis stream shards and throughput
- list_glue_jobs: List AWS Glue ETL jobs and schedules
- list_glue_databases: List Glue Data Catalog databases
- list_emr_clusters: List EMR big data clusters

MACHINE LEARNING:
- list_sagemaker_endpoints: List SageMaker model endpoints
- list_sagemaker_models: List trained SageMaker models
- list_sagemaker_notebook_instances: List SageMaker notebook instances

CACHING:
- list_elasticache_clusters: List ElastiCache Redis/Memcached clusters
- describe_elasticache_cluster: Get cache cluster configuration and nodes

APPLICATION INTEGRATION:
- list_step_functions: List Step Functions state machines
- describe_step_function: Get Step Function workflow definition

COST & BILLING:
- get_cost_and_usage: Get cost information and usage metrics
- list_budgets: List AWS Budgets and spending alerts

AI/ML SERVICES:
- list_bedrock_foundation_models: List available Bedrock foundation models
- list_bedrock_custom_models: List custom Bedrock models
- list_bedrock_agents: List Bedrock agents
- list_bedrock_knowledge_bases: List Bedrock knowledge bases
- list_bedrock_guardrails: List Bedrock guardrails
- list_sagemaker_endpoints: List SageMaker model endpoints
- list_sagemaker_models: List trained SageMaker models
- list_sagemaker_notebook_instances: List SageMaker notebook instances
- list_comprehend_jobs: List Comprehend analysis jobs
- list_textract_jobs: List Textract document analysis jobs
- list_rekognition_collections: List Rekognition face collections

OTHER SERVICES:
- list_api_gateways: List API Gateway REST and HTTP APIs
- list_cloudfront_distributions: List CloudFront distributions
- list_route53_zones: List Route53 hosted zones
- list_secrets: List AWS Secrets Manager secrets (names only)
- list_ssm_parameters: List Systems Manager parameters (names only)

Respond with ONLY a JSON object in this format:
{
  "operations": [
    {
      "operation": "operation_name",
      "reason": "why this operation is needed",
      "parameters": {"key": "value"}
    }
  ],
  "analysis": "brief explanation of what the user wants to know"
}

If no AWS operations are needed, return: {"operations": [], "analysis": "explanation"}`, question)
}
