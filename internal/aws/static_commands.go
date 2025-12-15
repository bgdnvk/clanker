package aws

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// CreateAWSCommands creates the AWS command tree for static commands
func CreateAWSCommands() *cobra.Command {
	awsCmd := &cobra.Command{
		Use:   "aws",
		Short: "Query AWS infrastructure directly",
		Long:  `Query your AWS infrastructure without AI interpretation. Useful for getting raw data.`,
	}

	awsListCmd := &cobra.Command{
		Use:   "list [resource]",
		Short: "List AWS resources",
		Long: `List AWS resources of a specific type.
		
Supported resources:
  COMPUTE:
    ec2, instances       - EC2 instances
    ecs, clusters        - ECS clusters and services
    batch                - AWS Batch jobs
    asg                  - Auto Scaling Groups
    
  SERVERLESS:
    lambda, lambdas, functions    - Lambda functions
    layers                        - Lambda layers
    
  CONTAINER:
    ecr, repositories    - ECR repositories
    eks                  - EKS clusters
    
  STORAGE:
    s3, buckets          - S3 buckets
    ebs, volumes         - EBS volumes
    efs                  - EFS file systems
    
  DATABASE:
    rds, databases       - RDS instances
    rds-clusters         - RDS Aurora clusters
    dynamodb, tables     - DynamoDB tables
    
  NETWORKING:
    vpcs                 - VPCs
    subnets              - Subnets
    security-groups      - Security groups
    load-balancers, elb  - Load balancers (ALB/NLB/CLB)
    route-tables         - Route tables
    
  MESSAGING:
    sqs, queues          - SQS queues
    sns, topics          - SNS topics
    eventbridge, events  - EventBridge rules
    
  MONITORING:
    logs, cloudwatch     - CloudWatch log groups
    alarms               - CloudWatch alarms
    
  SECURITY:
    iam-roles            - IAM roles
		iam-groups           - IAM groups
    iam-users            - IAM users
    kms, keys            - KMS keys
    certificates, acm    - ACM certificates
    secrets              - Secrets Manager secrets
    
  DEVOPS:
    codebuild            - CodeBuild projects
    codepipeline         - CodePipeline pipelines
    codecommit           - CodeCommit repositories
    
  AI/ML:
    bedrock-models       - Bedrock foundation models
    bedrock-custom       - Bedrock custom models
    bedrock-agents       - Bedrock agents
    bedrock-kb, knowledge-bases - Bedrock knowledge bases
    bedrock-guardrails   - Bedrock guardrails
    sagemaker-endpoints  - SageMaker endpoints
    sagemaker-models     - SageMaker models
    sagemaker-jobs       - SageMaker training jobs
    sagemaker-notebooks  - SageMaker notebook instances
    comprehend-jobs      - Comprehend analysis jobs
    textract-jobs        - Textract analysis jobs
    rekognition-collections - Rekognition collections
    
  OTHER:
    api-gateways         - API Gateway APIs
    cloudfront           - CloudFront distributions
    route53              - Route53 hosted zones`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := args[0]
			profile, _ := cmd.Flags().GetString("profile")
			environment, _ := cmd.Flags().GetString("environment")

			// If no profile provided, use the default infrastructure profile from config
			if profile == "" {
				// Get environment (from flag or config)
				if environment == "" {
					environment = viper.GetString("infra.default_environment")
					if environment == "" {
						environment = "dev" // fallback to dev
					}
				}

				defaultProvider := viper.GetString("infra.default_provider")
				if defaultProvider == "" {
					defaultProvider = "aws" // fallback to aws
				}

				// Try new environment-based structure first
				profileKey := fmt.Sprintf("infra.%s.environments.%s.profile", defaultProvider, environment)
				envProfile := viper.GetString(profileKey)
				if envProfile != "" {
					profile = envProfile
				} else {
					// Fallback to legacy structure
					legacyProfile := viper.GetString("infra.default_profile")
					if legacyProfile == "" {
						legacyProfile = viper.GetString("infra.aws.default_profile")
					}
					if legacyProfile != "" {
						profile = legacyProfile
					}
				}
			}

			ctx := context.Background()

			var client *Client
			var err error

			if profile != "" {
				client, err = NewClientWithProfile(ctx, profile)
			} else {
				client, err = NewClient(ctx)
			}

			if err != nil {
				return fmt.Errorf("failed to create AWS client: %w", err)
			}

			// Get region for the profile (if needed)
			region := "us-east-1" // default fallback
			if profile != "" {
				// Get environment (from flag or config)
				if environment == "" {
					environment = viper.GetString("infra.default_environment")
					if environment == "" {
						environment = "dev"
					}
				}

				defaultProvider := viper.GetString("infra.default_provider")
				if defaultProvider == "" {
					defaultProvider = "aws"
				}

				// Try new environment-based structure first
				regionKey := fmt.Sprintf("infra.%s.environments.%s.region", defaultProvider, environment)
				envRegion := viper.GetString(regionKey)
				if envRegion != "" {
					region = envRegion
				} else {
					// Fallback to legacy structure
					profileKey := fmt.Sprintf("infra.aws.profiles.%s.region", profile)
					if configRegion := viper.GetString(profileKey); configRegion != "" {
						region = configRegion
					}
				}
			}

			// Helper function to execute AWS operations
			executeOp := func(operation string) (string, error) {
				return client.executeAWSOperation(ctx, operation, map[string]interface{}{}, &AIProfile{AWSProfile: profile, Region: region})
			}

			switch resourceType {
			// COMPUTE
			case "ec2", "instances":
				info, err := client.GetRelevantContext(ctx, "ec2 instances")
				if err != nil {
					return err
				}
				fmt.Print(info)
			case "ecs", "clusters":
				info, err := client.GetRelevantContext(ctx, "ecs services")
				if err != nil {
					return err
				}
				fmt.Print(info)
			case "batch":
				result, err := executeOp("list_batch_jobs")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "asg":
				result, err := executeOp("list_auto_scaling_groups")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// SERVERLESS
			case "lambda", "lambdas", "functions":
				info, err := client.GetRelevantContext(ctx, "lambda functions")
				if err != nil {
					return err
				}
				fmt.Print(info)
			case "layers":
				result, err := executeOp("list_lambda_layers")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// CONTAINER
			case "ecr", "repositories":
				result, err := executeOp("list_ecr_repositories")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "eks":
				result, err := executeOp("list_eks_clusters")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// STORAGE
			case "s3", "buckets":
				info, err := client.GetRelevantContext(ctx, "s3 buckets")
				if err != nil {
					return err
				}
				fmt.Print(info)
			case "ebs", "volumes":
				result, err := executeOp("list_ebs_volumes")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "efs":
				result, err := executeOp("list_efs_filesystems")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// DATABASE
			case "rds", "databases":
				result, err := executeOp("list_rds_instances")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "rds-clusters":
				result, err := executeOp("list_rds_clusters")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "dynamodb", "tables":
				result, err := executeOp("list_dynamodb_tables")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// NETWORKING
			case "vpcs":
				result, err := executeOp("list_vpcs")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "subnets":
				result, err := executeOp("list_subnets")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "security-groups":
				result, err := executeOp("list_security_groups")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "load-balancers", "elb":
				result, err := executeOp("list_load_balancers")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "route-tables":
				result, err := executeOp("list_route_tables")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// MESSAGING
			case "sqs", "queues":
				result, err := executeOp("list_sqs_queues")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sns", "topics":
				result, err := executeOp("list_sns_topics")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "eventbridge", "events":
				result, err := executeOp("list_eventbridge_rules")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// MONITORING
			case "logs", "cloudwatch":
				result, err := executeOp("list_cloudwatch_log_groups")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "alarms":
				result, err := executeOp("list_cloudwatch_alarms")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// SECURITY
			case "iam-roles":
				result, err := executeOp("list_iam_roles")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "iam-groups":
				result, err := executeOp("list_iam_groups")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "iam-users":
				result, err := executeOp("list_iam_users")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "kms", "keys":
				result, err := executeOp("list_kms_keys")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "certificates", "acm":
				result, err := executeOp("list_acm_certificates")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "secrets":
				result, err := executeOp("list_secrets_manager_secrets")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// DEVOPS
			case "codebuild":
				result, err := executeOp("list_codebuild_projects")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "codepipeline":
				result, err := executeOp("list_codepipeline_pipelines")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "codecommit":
				result, err := executeOp("list_codecommit_repositories")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// AI/ML
			case "bedrock-models":
				result, err := executeOp("list_bedrock_foundation_models")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "bedrock-custom":
				result, err := executeOp("list_bedrock_custom_models")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "bedrock-agents":
				result, err := executeOp("list_bedrock_agents")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "bedrock-kb", "knowledge-bases":
				result, err := executeOp("list_bedrock_knowledge_bases")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "bedrock-guardrails":
				result, err := executeOp("list_bedrock_guardrails")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sagemaker-endpoints":
				result, err := executeOp("list_sagemaker_endpoints")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sagemaker-models":
				result, err := executeOp("list_sagemaker_models")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sagemaker-jobs":
				result, err := executeOp("list_sagemaker_training_jobs")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "sagemaker-notebooks":
				result, err := executeOp("list_sagemaker_notebook_instances")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "comprehend-jobs":
				result, err := executeOp("list_comprehend_jobs")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "textract-jobs":
				result, err := executeOp("list_textract_jobs")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "rekognition-collections":
				result, err := executeOp("list_rekognition_collections")
				if err != nil {
					return err
				}
				fmt.Print(result)

			// OTHER
			case "api-gateways":
				result, err := executeOp("list_api_gateways")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "cloudfront":
				result, err := executeOp("list_cloudfront_distributions")
				if err != nil {
					return err
				}
				fmt.Print(result)
			case "route53":
				result, err := executeOp("list_route53_hosted_zones")
				if err != nil {
					return err
				}
				fmt.Print(result)

			default:
				return fmt.Errorf("unsupported resource type: %s", resourceType)
			}

			return nil
		},
	}

	awsListCmd.Flags().StringP("profile", "p", "", "AWS profile to use")
	awsListCmd.Flags().StringP("environment", "e", "", "Environment to use (dev/stage/prod)")
	awsCmd.AddCommand(awsListCmd)

	return awsCmd
}
