package aws

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	tfclient "github.com/bgdnvk/clanker/internal/terraform"
	"github.com/spf13/viper"
)

// LLMOperationResult represents the result of an AWS operation for LLM processing
type LLMOperationResult struct {
	Operation string
	Result    string
	Error     error
	Index     int
}

// LLMOperation represents an AWS operation requested by the LLM
type LLMOperation struct {
	Operation  string                 `json:"operation"`
	Reason     string                 `json:"reason"`
	Parameters map[string]interface{} `json:"parameters"`
}

// LLMAnalysis represents the LLM's analysis of what AWS operations are needed
type LLMAnalysis struct {
	Operations []LLMOperation `json:"operations"`
	Analysis   string         `json:"analysis"`
}

// AIProfile represents an AI provider configuration
type AIProfile struct {
	Provider   string `mapstructure:"provider"`
	AWSProfile string `mapstructure:"aws_profile"`
	Model      string `mapstructure:"model"`
	Region     string `mapstructure:"region"`
	APIKeyEnv  string `mapstructure:"api_key_env"`
}

// GetAIProfile returns the AI configuration for the given provider name
func GetAIProfile(providerName string) (*AIProfile, error) {
	if providerName == "" {
		providerName = viper.GetString("ai.default_provider")
		if providerName == "" {
			providerName = "bedrock"
		}
	}

	profileKey := fmt.Sprintf("ai.providers.%s", providerName)
	if !viper.IsSet(profileKey) {
		return nil, fmt.Errorf("AI provider '%s' not found in configuration", providerName)
	}

	var profile AIProfile
	if err := viper.UnmarshalKey(profileKey, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse AI provider '%s': %w", providerName, err)
	}

	// Set the provider name
	profile.Provider = providerName

	return &profile, nil
}

// ExecuteOperationsConcurrently executes multiple AWS operations concurrently for LLM processing
func (c *Client) ExecuteOperationsConcurrently(ctx context.Context, operations []LLMOperation, aiProfile string) (string, error) {
	if len(operations) == 0 {
		return "", nil
	}

	// Get AI profile configuration
	profile, err := GetAIProfile(aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile: %w", err)
	}

	return c.executeOperationsWithProfile(ctx, operations, profile)
}

// ExecuteOperationsWithAWSProfile executes multiple AWS operations concurrently using a direct AWS profile
func (c *Client) ExecuteOperationsWithAWSProfile(ctx context.Context, operations []LLMOperation, awsProfile, region string) (string, error) {
	if len(operations) == 0 {
		return "", nil
	}

	// Create a temporary AI profile with the specified AWS profile
	profile := &AIProfile{
		Provider:   "bedrock", // Not used for AWS operations
		AWSProfile: awsProfile,
		Region:     region,
	}

	return c.executeOperationsWithProfile(ctx, operations, profile)
}

// executeOperationsWithProfile executes operations with a given profile
func (c *Client) executeOperationsWithProfile(ctx context.Context, operations []LLMOperation, profile *AIProfile) (string, error) {

	// Create channels for results
	resultChan := make(chan LLMOperationResult, len(operations))
	var wg sync.WaitGroup

	// Execute all operations concurrently
	for i, op := range operations {
		wg.Add(1)
		go func(index int, operation string, params map[string]interface{}) {
			defer wg.Done()
			result, err := c.executeAWSOperation(ctx, operation, params, profile)
			resultChan <- LLMOperationResult{
				Operation: operation,
				Result:    result,
				Error:     err,
				Index:     index,
			}
		}(i, op.Operation, op.Parameters)
	}

	// Wait for all operations to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results in order
	results := make([]LLMOperationResult, len(operations))
	for result := range resultChan {
		results[result.Index] = result
	}

	// Build results string in original order
	var awsResults strings.Builder
	for _, result := range results {
		if result.Error != nil {
			awsResults.WriteString(fmt.Sprintf("❌ %s failed: %v\n", result.Operation, result.Error))
		} else {
			awsResults.WriteString(fmt.Sprintf("✅ %s:\n%s\n\n", result.Operation, result.Result))
		}
	}

	return awsResults.String(), nil
}

// executeAWSOperation executes a specific AWS operation with the given parameters
func (c *Client) executeAWSOperation(ctx context.Context, toolName string, input map[string]interface{}, profile *AIProfile) (string, error) {
	// All operations are read-only and safe - no modifications or deletions possible
	switch toolName {
	// SERVICE EXISTENCE CHECKS - Quick checks to see if services exist/are configured
	case "check_sqs_service":
		args := []string{"sqs", "list-queues", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SQS service not available or no access", nil
		}
		queueCountArgs := []string{"sqs", "list-queues", "--output", "json", "--query", "length(QueueUrls)"}
		countResult, _ := c.execAWSCLI(ctx, queueCountArgs, profile)
		return fmt.Sprintf("✅ SQS service is available. Queue count: %s", strings.TrimSpace(countResult)), nil

	case "check_eventbridge_service":
		args := []string{"events", "list-event-buses", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ EventBridge service not available or no access", nil
		}
		// Count rules on default bus
		ruleArgs := []string{"events", "list-rules", "--output", "json", "--query", "length(Rules)"}
		ruleCount, _ := c.execAWSCLI(ctx, ruleArgs, profile)
		return fmt.Sprintf("✅ EventBridge service is available. Rule count: %s", strings.TrimSpace(ruleCount)), nil

	case "check_lambda_service":
		args := []string{"lambda", "list-functions", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lambda service not available or no access", nil
		}
		// Get actual count
		countArgs := []string{"lambda", "list-functions", "--output", "json", "--query", "length(Functions)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Lambda service is available. Function count: %s", strings.TrimSpace(countResult)), nil

	case "check_sns_service":
		args := []string{"sns", "list-topics", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SNS service not available or no access", nil
		}
		countArgs := []string{"sns", "list-topics", "--output", "json", "--query", "length(Topics)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ SNS service is available. Topic count: %s", strings.TrimSpace(countResult)), nil

	case "check_dynamodb_service":
		args := []string{"dynamodb", "list-tables", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ DynamoDB service not available or no access", nil
		}
		countArgs := []string{"dynamodb", "list-tables", "--output", "json", "--query", "length(TableNames)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ DynamoDB service is available. Table count: %s", strings.TrimSpace(countResult)), nil

	case "check_s3_service":
		args := []string{"s3api", "list-buckets", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ S3 service not available or no access", nil
		}
		countArgs := []string{"s3api", "list-buckets", "--output", "json", "--query", "length(Buckets)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ S3 service is available. Bucket count: %s", strings.TrimSpace(countResult)), nil

	case "check_rds_service":
		args := []string{"rds", "describe-db-instances", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ RDS service not available or no access", nil
		}
		countArgs := []string{"rds", "describe-db-instances", "--output", "json", "--query", "length(DBInstances)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ RDS service is available. Instance count: %s", strings.TrimSpace(countResult)), nil

	case "check_ec2_service":
		args := []string{"ec2", "describe-instances", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ EC2 service not available or no access", nil
		}
		countArgs := []string{"ec2", "describe-instances", "--output", "json", "--query", "length(Reservations[].Instances[])"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ EC2 service is available. Instance count: %s", strings.TrimSpace(countResult)), nil

	case "check_ecs_service":
		args := []string{"ecs", "list-clusters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ ECS service not available or no access", nil
		}
		countArgs := []string{"ecs", "list-clusters", "--output", "json", "--query", "length(clusterArns)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ ECS service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_ecr_service":
		args := []string{"ecr", "describe-repositories", "--max-items", "1", "--output", "table"}
		fmt.Printf("🔍 ECR: Checking service availability with: aws %s\n", strings.Join(args, " "))
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			fmt.Printf("❌ ECR: Service check failed: %v\n", err)
			return "❌ ECR service not available or no access", nil
		}
		fmt.Printf("✅ ECR: Service is available, getting count...\n")
		countArgs := []string{"ecr", "describe-repositories", "--output", "json", "--query", "length(repositories)"}
		fmt.Printf("🔍 ECR: Getting count with: aws %s\n", strings.Join(countArgs, " "))
		countResult, err := c.execAWSCLI(ctx, countArgs, profile)
		if err != nil {
			fmt.Printf("❌ ECR: Count query failed: %v\n", err)
			return "❌ ECR count query failed", nil
		}
		fmt.Printf("📊 ECR: Raw count result: '%s'\n", countResult)
		return fmt.Sprintf("✅ ECR service is available. Repository count: %s", strings.TrimSpace(countResult)), nil

	// ADDITIONAL AWS SERVICES
	case "check_elasticsearch_service":
		args := []string{"es", "list-domain-names", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Elasticsearch service not available or no access", nil
		}
		countArgs := []string{"es", "list-domain-names", "--output", "json", "--query", "length(DomainNames)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Elasticsearch service is available. Domain count: %s", strings.TrimSpace(countResult)), nil

	case "check_opensearch_service":
		args := []string{"opensearch", "list-domain-names", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ OpenSearch service not available or no access", nil
		}
		countArgs := []string{"opensearch", "list-domain-names", "--output", "json", "--query", "length(DomainNames)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ OpenSearch service is available. Domain count: %s", strings.TrimSpace(countResult)), nil

	case "check_eks_service":
		args := []string{"eks", "list-clusters", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ EKS service not available or no access", nil
		}
		countArgs := []string{"eks", "list-clusters", "--output", "json", "--query", "length(clusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ EKS service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_elasticache_service":
		args := []string{"elasticache", "describe-cache-clusters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ ElastiCache service not available or no access", nil
		}
		countArgs := []string{"elasticache", "describe-cache-clusters", "--output", "json", "--query", "length(CacheClusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ ElastiCache service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_redshift_service":
		args := []string{"redshift", "describe-clusters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Redshift service not available or no access", nil
		}
		countArgs := []string{"redshift", "describe-clusters", "--output", "json", "--query", "length(Clusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Redshift service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_kinesis_service":
		args := []string{"kinesis", "list-streams", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Kinesis service not available or no access", nil
		}
		countArgs := []string{"kinesis", "list-streams", "--output", "json", "--query", "length(StreamNames)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Kinesis service is available. Stream count: %s", strings.TrimSpace(countResult)), nil

	case "check_cloudformation_service":
		args := []string{"cloudformation", "list-stacks", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudFormation service not available or no access", nil
		}
		countArgs := []string{"cloudformation", "list-stacks", "--output", "json", "--query", "length(StackSummaries)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CloudFormation service is available. Stack count: %s", strings.TrimSpace(countResult)), nil

	case "check_kms_service":
		args := []string{"kms", "list-keys", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ KMS service not available or no access", nil
		}
		countArgs := []string{"kms", "list-keys", "--output", "json", "--query", "length(Keys)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ KMS service is available. Key count: %s", strings.TrimSpace(countResult)), nil

	case "check_secretsmanager_service":
		args := []string{"secretsmanager", "list-secrets", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Secrets Manager service not available or no access", nil
		}
		countArgs := []string{"secretsmanager", "list-secrets", "--output", "json", "--query", "length(SecretList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Secrets Manager service is available. Secret count: %s", strings.TrimSpace(countResult)), nil

	case "check_route53_service":
		args := []string{"route53", "list-hosted-zones", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Route53 service not available or no access", nil
		}
		countArgs := []string{"route53", "list-hosted-zones", "--output", "json", "--query", "length(HostedZones)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Route53 service is available. Hosted zone count: %s", strings.TrimSpace(countResult)), nil

	case "check_cloudfront_service":
		// Try standard endpoint first, fallback if FIPS endpoint fails
		args := []string{"cloudfront", "list-distributions", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			// Check if it's a FIPS endpoint issue
			if strings.Contains(err.Error(), "cloudfront-fips") {
				return "❌ CloudFront service not available (FIPS endpoint not supported in this region)", nil
			}
			return "❌ CloudFront service not available or no access", nil
		}
		countArgs := []string{"cloudfront", "list-distributions", "--output", "json", "--query", "length(DistributionList.Items || `[]`)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CloudFront service is available. Distribution count: %s", strings.TrimSpace(countResult)), nil

	case "check_apigateway_service":
		args := []string{"apigateway", "get-rest-apis", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ API Gateway service not available or no access", nil
		}
		countArgs := []string{"apigateway", "get-rest-apis", "--output", "json", "--query", "length(items)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ API Gateway service is available. REST API count: %s", strings.TrimSpace(countResult)), nil

	case "check_bedrock_service":
		// Bedrock doesn't support max-results, but we can check if service is available
		args := []string{"bedrock", "list-foundation-models", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			if strings.Contains(err.Error(), "not supported") || strings.Contains(err.Error(), "not available") {
				return "❌ Bedrock service not available in this region", nil
			}
			return "❌ Bedrock service not available or no access", nil
		}
		countArgs := []string{"bedrock", "list-foundation-models", "--output", "json", "--query", "length(modelSummaries || `[]`)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Bedrock service is available. Model count: %s", strings.TrimSpace(countResult)), nil

	case "check_codecommit_service":
		args := []string{"codecommit", "list-repositories", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodeCommit service not available or no access", nil
		}
		countArgs := []string{"codecommit", "list-repositories", "--output", "json", "--query", "length(repositories)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CodeCommit service is available. Repository count: %s", strings.TrimSpace(countResult)), nil

	case "check_codebuild_service":
		args := []string{"codebuild", "list-projects", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodeBuild service not available or no access", nil
		}
		countArgs := []string{"codebuild", "list-projects", "--output", "json", "--query", "length(projects)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CodeBuild service is available. Project count: %s", strings.TrimSpace(countResult)), nil

	case "check_codepipeline_service":
		args := []string{"codepipeline", "list-pipelines", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodePipeline service not available or no access", nil
		}
		countArgs := []string{"codepipeline", "list-pipelines", "--output", "json", "--query", "length(pipelines)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CodePipeline service is available. Pipeline count: %s", strings.TrimSpace(countResult)), nil

	case "check_sagemaker_service":
		args := []string{"sagemaker", "list-endpoints", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SageMaker service not available or no access", nil
		}
		countArgs := []string{"sagemaker", "list-endpoints", "--output", "json", "--query", "length(Endpoints)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ SageMaker service is available. Endpoint count: %s", strings.TrimSpace(countResult)), nil

	case "check_glue_service":
		args := []string{"glue", "get-databases", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Glue service not available or no access", nil
		}
		countArgs := []string{"glue", "get-databases", "--output", "json", "--query", "length(DatabaseList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Glue service is available. Database count: %s", strings.TrimSpace(countResult)), nil

	case "check_athena_service":
		args := []string{"athena", "list-work-groups", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Athena service not available or no access", nil
		}
		countArgs := []string{"athena", "list-work-groups", "--output", "json", "--query", "length(WorkGroups)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Athena service is available. Work group count: %s", strings.TrimSpace(countResult)), nil

	case "check_emr_service":
		args := []string{"emr", "list-clusters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ EMR service not available or no access", nil
		}
		countArgs := []string{"emr", "list-clusters", "--output", "json", "--query", "length(Clusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ EMR service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_stepfunctions_service":
		args := []string{"stepfunctions", "list-state-machines", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Step Functions service not available or no access", nil
		}
		countArgs := []string{"stepfunctions", "list-state-machines", "--output", "json", "--query", "length(stateMachines)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Step Functions service is available. State machine count: %s", strings.TrimSpace(countResult)), nil

	case "check_cloudwatch_service":
		args := []string{"cloudwatch", "list-metrics", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudWatch service not available or no access", nil
		}
		countArgs := []string{"cloudwatch", "list-metrics", "--max-items", "10", "--output", "json", "--query", "length(Metrics || `[]`)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CloudWatch service is available. Sample metric count: %s", strings.TrimSpace(countResult)), nil

	case "check_logs_service":
		args := []string{"logs", "describe-log-groups", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudWatch Logs service not available or no access", nil
		}
		countArgs := []string{"logs", "describe-log-groups", "--output", "json", "--query", "length(logGroups)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CloudWatch Logs service is available. Log group count: %s", strings.TrimSpace(countResult)), nil

	case "check_xray_service":
		// X-Ray requires time range, use a minimal recent range (max 6 hours)
		endTime := time.Now()
		startTime := endTime.Add(-5 * time.Hour) // Use 5 hours to stay well under 6 hour limit
		args := []string{"xray", "get-service-graph",
			"--start-time", startTime.Format("2006-01-02T15:04:05Z"),
			"--end-time", endTime.Format("2006-01-02T15:04:05Z"),
			"--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			if strings.Contains(err.Error(), "Time range") {
				return "❌ X-Ray service available but time range validation failed", nil
			}
			return "❌ X-Ray service not available or no access", nil
		}
		return "✅ X-Ray service is available", nil

	case "check_cognito_service":
		args := []string{"cognito-idp", "list-user-pools", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Cognito service not available or no access", nil
		}
		countArgs := []string{"cognito-idp", "list-user-pools", "--output", "json", "--query", "length(UserPools)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Cognito service is available. User pool count: %s", strings.TrimSpace(countResult)), nil

	case "check_wafv2_service":
		args := []string{"wafv2", "list-web-acls", "--scope", "REGIONAL", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ WAFv2 service not available or no access", nil
		}
		countArgs := []string{"wafv2", "list-web-acls", "--scope", "REGIONAL", "--output", "json", "--query", "length(WebACLs)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ WAFv2 service is available. Web ACL count: %s", strings.TrimSpace(countResult)), nil

	case "check_acm_service":
		args := []string{"acm", "list-certificates", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ ACM service not available or no access", nil
		}
		countArgs := []string{"acm", "list-certificates", "--output", "json", "--query", "length(CertificateSummaryList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ ACM service is available. Certificate count: %s", strings.TrimSpace(countResult)), nil

	case "check_cloudtrail_service":
		args := []string{"cloudtrail", "describe-trails", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudTrail service not available or no access", nil
		}
		countArgs := []string{"cloudtrail", "describe-trails", "--output", "json", "--query", "length(trailList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CloudTrail service is available. Trail count: %s", strings.TrimSpace(countResult)), nil

	case "check_config_service":
		args := []string{"configservice", "describe-configuration-recorders", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Config service not available or no access", nil
		}
		countArgs := []string{"configservice", "describe-configuration-recorders", "--output", "json", "--query", "length(ConfigurationRecorders)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Config service is available. Recorder count: %s", strings.TrimSpace(countResult)), nil

	case "check_guardduty_service":
		args := []string{"guardduty", "list-detectors", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ GuardDuty service not available or no access", nil
		}
		countArgs := []string{"guardduty", "list-detectors", "--output", "json", "--query", "length(DetectorIds)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ GuardDuty service is available. Detector count: %s", strings.TrimSpace(countResult)), nil

	case "check_ssm_service":
		args := []string{"ssm", "describe-parameters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SSM service not available or no access", nil
		}
		countArgs := []string{"ssm", "describe-parameters", "--output", "json", "--query", "length(Parameters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ SSM service is available. Parameter count: %s", strings.TrimSpace(countResult)), nil

	case "check_batch_service":
		args := []string{"batch", "describe-job-queues", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Batch service not available or no access", nil
		}
		countArgs := []string{"batch", "describe-job-queues", "--output", "json", "--query", "length(jobQueues)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Batch service is available. Job queue count: %s", strings.TrimSpace(countResult)), nil

	case "check_appsync_service":
		args := []string{"appsync", "list-graphql-apis", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ AppSync service not available or no access", nil
		}
		countArgs := []string{"appsync", "list-graphql-apis", "--output", "json", "--query", "length(graphqlApis)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ AppSync service is available. GraphQL API count: %s", strings.TrimSpace(countResult)), nil

	case "check_amplify_service":
		args := []string{"amplify", "list-apps", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Amplify service not available or no access", nil
		}
		countArgs := []string{"amplify", "list-apps", "--output", "json", "--query", "length(apps)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Amplify service is available. App count: %s", strings.TrimSpace(countResult)), nil

	case "check_comprehend_service":
		args := []string{"comprehend", "list-sentiment-detection-jobs", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Comprehend service not available or no access", nil
		}
		return "✅ Comprehend service is available", nil

	case "check_textract_service":
		args := []string{"textract", "list-adapters", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Textract service not available or no access", nil
		}
		return "✅ Textract service is available", nil

	case "check_rekognition_service":
		args := []string{"rekognition", "list-collections", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Rekognition service not available or no access", nil
		}
		countArgs := []string{"rekognition", "list-collections", "--output", "json", "--query", "length(CollectionIds)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Rekognition service is available. Collection count: %s", strings.TrimSpace(countResult)), nil

	case "check_polly_service":
		args := []string{"polly", "describe-voices", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Polly service not available or no access", nil
		}
		countArgs := []string{"polly", "describe-voices", "--output", "json", "--query", "length(Voices)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Polly service is available. Voice count: %s", strings.TrimSpace(countResult)), nil

	case "check_transcribe_service":
		args := []string{"transcribe", "list-transcription-jobs", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Transcribe service not available or no access", nil
		}
		return "✅ Transcribe service is available", nil

	case "check_translate_service":
		args := []string{"translate", "list-languages", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Translate service not available or no access", nil
		}
		countArgs := []string{"translate", "list-languages", "--output", "json", "--query", "length(Languages)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Translate service is available. Language count: %s", strings.TrimSpace(countResult)), nil

	case "check_personalize_service":
		args := []string{"personalize", "list-datasets", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Personalize service not available or no access", nil
		}
		return "✅ Personalize service is available", nil

	case "check_kendra_service":
		args := []string{"kendra", "list-indices", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Kendra service not available or no access", nil
		}
		countArgs := []string{"kendra", "list-indices", "--output", "json", "--query", "length(IndexConfigurationSummaryItems)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Kendra service is available. Index count: %s", strings.TrimSpace(countResult)), nil

	case "check_lex_service":
		args := []string{"lexv2-models", "list-bots", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lex service not available or no access", nil
		}
		countArgs := []string{"lexv2-models", "list-bots", "--output", "json", "--query", "length(botSummaries)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Lex service is available. Bot count: %s", strings.TrimSpace(countResult)), nil

	case "check_apprunner_service":
		args := []string{"apprunner", "list-services", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ App Runner service not available or no access", nil
		}
		countArgs := []string{"apprunner", "list-services", "--output", "json", "--query", "length(ServiceSummaryList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ App Runner service is available. Service count: %s", strings.TrimSpace(countResult)), nil

	case "check_documentdb_service":
		args := []string{"docdb", "describe-db-clusters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ DocumentDB service not available or no access", nil
		}
		countArgs := []string{"docdb", "describe-db-clusters", "--output", "json", "--query", "length(DBClusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ DocumentDB service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_neptune_service":
		args := []string{"neptune", "describe-db-clusters", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Neptune service not available or no access", nil
		}
		countArgs := []string{"neptune", "describe-db-clusters", "--output", "json", "--query", "length(DBClusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Neptune service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_timestream_service":
		args := []string{"timestream-query", "list-databases", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Timestream service not available or no access", nil
		}
		countArgs := []string{"timestream-query", "list-databases", "--output", "json", "--query", "length(Databases)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Timestream service is available. Database count: %s", strings.TrimSpace(countResult)), nil

	case "check_inspector_service":
		args := []string{"inspector2", "list-findings", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Inspector service not available or no access", nil
		}
		return "✅ Inspector service is available", nil

	case "check_macie_service":
		args := []string{"macie2", "get-macie-session", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Macie service not available or no access", nil
		}
		return "✅ Macie service is available", nil

	case "check_backup_service":
		args := []string{"backup", "list-backup-vaults", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Backup service not available or no access", nil
		}
		countArgs := []string{"backup", "list-backup-vaults", "--output", "json", "--query", "length(BackupVaultList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Backup service is available. Vault count: %s", strings.TrimSpace(countResult)), nil

	case "check_organizations_service":
		args := []string{"organizations", "describe-organization", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Organizations service not available or no access", nil
		}
		return "✅ Organizations service is available", nil

	case "check_quicksight_service":
		args := []string{"quicksight", "list-users", "--aws-account-id", "$(aws sts get-caller-identity --query Account --output text)", "--namespace", "default", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ QuickSight service not available or no access", nil
		}
		return "✅ QuickSight service is available", nil

	case "check_msk_service":
		args := []string{"kafka", "list-clusters", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MSK service not available or no access", nil
		}
		countArgs := []string{"kafka", "list-clusters", "--output", "json", "--query", "length(ClusterInfoList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ MSK service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_transitgateway_service":
		args := []string{"ec2", "describe-transit-gateways", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Transit Gateway service not available or no access", nil
		}
		countArgs := []string{"ec2", "describe-transit-gateways", "--output", "json", "--query", "length(TransitGateways)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Transit Gateway service is available. Gateway count: %s", strings.TrimSpace(countResult)), nil

	case "check_securityhub_service":
		args := []string{"securityhub", "get-enabled-standards", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Security Hub service not available or no access", nil
		}
		return "✅ Security Hub service is available", nil

	case "check_servicecatalog_service":
		args := []string{"servicecatalog", "list-portfolios", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Service Catalog service not available or no access", nil
		}
		countArgs := []string{"servicecatalog", "list-portfolios", "--output", "json", "--query", "length(PortfolioDetails)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Service Catalog service is available. Portfolio count: %s", strings.TrimSpace(countResult)), nil

	case "check_lakeformation_service":
		args := []string{"lakeformation", "list-permissions", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lake Formation service not available or no access", nil
		}
		return "✅ Lake Formation service is available", nil

	case "check_mq_service":
		args := []string{"mq", "list-brokers", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MQ service not available or no access", nil
		}
		countArgs := []string{"mq", "list-brokers", "--output", "json", "--query", "length(BrokerSummaries)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ MQ service is available. Broker count: %s", strings.TrimSpace(countResult)), nil

	case "check_fsx_service":
		args := []string{"fsx", "describe-file-systems", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ FSx service not available or no access", nil
		}
		countArgs := []string{"fsx", "describe-file-systems", "--output", "json", "--query", "length(FileSystems)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ FSx service is available. File system count: %s", strings.TrimSpace(countResult)), nil

	case "check_directconnect_service":
		args := []string{"directconnect", "describe-connections", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Direct Connect service not available or no access", nil
		}
		countArgs := []string{"directconnect", "describe-connections", "--output", "json", "--query", "length(connections)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Direct Connect service is available. Connection count: %s", strings.TrimSpace(countResult)), nil

	case "check_dms_service":
		args := []string{"dms", "describe-replication-instances", "--max-records", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ DMS service not available or no access", nil
		}
		countArgs := []string{"dms", "describe-replication-instances", "--output", "json", "--query", "length(ReplicationInstances)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ DMS service is available. Replication instance count: %s", strings.TrimSpace(countResult)), nil

	case "check_globalaccelerator_service":
		args := []string{"globalaccelerator", "list-accelerators", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Global Accelerator service not available or no access", nil
		}
		countArgs := []string{"globalaccelerator", "list-accelerators", "--output", "json", "--query", "length(Accelerators)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Global Accelerator service is available. Accelerator count: %s", strings.TrimSpace(countResult)), nil

	case "check_workspaces_service":
		args := []string{"workspaces", "describe-workspaces", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ WorkSpaces service not available or no access", nil
		}
		countArgs := []string{"workspaces", "describe-workspaces", "--output", "json", "--query", "length(Workspaces)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ WorkSpaces service is available. Workspace count: %s", strings.TrimSpace(countResult)), nil

	case "check_connect_service":
		args := []string{"connect", "list-instances", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Connect service not available or no access", nil
		}
		countArgs := []string{"connect", "list-instances", "--output", "json", "--query", "length(InstanceSummaryList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Connect service is available. Instance count: %s", strings.TrimSpace(countResult)), nil

	case "check_iot_service":
		args := []string{"iot", "list-things", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT service not available or no access", nil
		}
		countArgs := []string{"iot", "list-things", "--output", "json", "--query", "length(things)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ IoT service is available. Thing count: %s", strings.TrimSpace(countResult)), nil

	case "check_codeartifact_service":
		args := []string{"codeartifact", "list-domains", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodeArtifact service not available or no access", nil
		}
		countArgs := []string{"codeartifact", "list-domains", "--output", "json", "--query", "length(domains)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CodeArtifact service is available. Domain count: %s", strings.TrimSpace(countResult)), nil

	case "check_codeguru_service":
		args := []string{"codeguru-reviewer", "list-repository-associations", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodeGuru service not available or no access", nil
		}
		return "✅ CodeGuru service is available", nil

	case "check_devicefarm_service":
		args := []string{"devicefarm", "list-projects", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Device Farm service not available or no access", nil
		}
		countArgs := []string{"devicefarm", "list-projects", "--output", "json", "--query", "length(projects)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Device Farm service is available. Project count: %s", strings.TrimSpace(countResult)), nil

	case "check_pinpoint_service":
		args := []string{"pinpoint", "get-apps", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Pinpoint service not available or no access", nil
		}
		countArgs := []string{"pinpoint", "get-apps", "--output", "json", "--query", "length(ApplicationsResponse.Item)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Pinpoint service is available. App count: %s", strings.TrimSpace(countResult)), nil

	case "check_storagegateway_service":
		args := []string{"storagegateway", "list-gateways", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Storage Gateway service not available or no access", nil
		}
		countArgs := []string{"storagegateway", "list-gateways", "--output", "json", "--query", "length(Gateways)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Storage Gateway service is available. Gateway count: %s", strings.TrimSpace(countResult)), nil

	case "check_transferfamily_service":
		args := []string{"transfer", "list-servers", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Transfer Family service not available or no access", nil
		}
		countArgs := []string{"transfer", "list-servers", "--output", "json", "--query", "length(Servers)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Transfer Family service is available. Server count: %s", strings.TrimSpace(countResult)), nil

	case "check_appmesh_service":
		args := []string{"appmesh", "list-meshes", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ App Mesh service not available or no access", nil
		}
		countArgs := []string{"appmesh", "list-meshes", "--output", "json", "--query", "length(meshes)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ App Mesh service is available. Mesh count: %s", strings.TrimSpace(countResult)), nil

	case "check_privatelink_service":
		args := []string{"ec2", "describe-vpc-endpoints", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ PrivateLink service not available or no access", nil
		}
		countArgs := []string{"ec2", "describe-vpc-endpoints", "--output", "json", "--query", "length(VpcEndpoints)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ PrivateLink service is available. Endpoint count: %s", strings.TrimSpace(countResult)), nil

	case "check_controltower_service":
		args := []string{"controltower", "list-enabled-controls", "--target-identifier", "ou-root", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Control Tower service not available or no access", nil
		}
		return "✅ Control Tower service is available", nil

	case "check_licensemanager_service":
		args := []string{"license-manager", "list-licenses", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ License Manager service not available or no access", nil
		}
		return "✅ License Manager service is available", nil

	case "check_resourcegroups_service":
		args := []string{"resource-groups", "list-groups", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Resource Groups service not available or no access", nil
		}
		countArgs := []string{"resource-groups", "list-groups", "--output", "json", "--query", "length(GroupIdentifiers)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Resource Groups service is available. Group count: %s", strings.TrimSpace(countResult)), nil

	case "check_directoryservice_service":
		args := []string{"ds", "describe-directories", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Directory Service not available or no access", nil
		}
		countArgs := []string{"ds", "describe-directories", "--output", "json", "--query", "length(DirectoryDescriptions)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Directory Service is available. Directory count: %s", strings.TrimSpace(countResult)), nil

	case "check_sso_service":
		args := []string{"sso-admin", "list-instances", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SSO service not available or no access", nil
		}
		return "✅ SSO service is available", nil

	case "check_privateca_service":
		args := []string{"acm-pca", "list-certificate-authorities", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Private CA service not available or no access", nil
		}
		countArgs := []string{"acm-pca", "list-certificate-authorities", "--output", "json", "--query", "length(CertificateAuthorities)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Private CA service is available. CA count: %s", strings.TrimSpace(countResult)), nil

	case "check_memorydb_service":
		args := []string{"memorydb", "describe-clusters", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MemoryDB service not available or no access", nil
		}
		countArgs := []string{"memorydb", "describe-clusters", "--output", "json", "--query", "length(Clusters)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ MemoryDB service is available. Cluster count: %s", strings.TrimSpace(countResult)), nil

	case "check_keyspaces_service":
		args := []string{"cassandra", "list-keyspaces", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Keyspaces service not available or no access", nil
		}
		countArgs := []string{"cassandra", "list-keyspaces", "--output", "json", "--query", "length(keyspaces)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Keyspaces service is available. Keyspace count: %s", strings.TrimSpace(countResult)), nil

	case "check_qldb_service":
		args := []string{"qldb", "list-ledgers", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ QLDB service not available or no access", nil
		}
		countArgs := []string{"qldb", "list-ledgers", "--output", "json", "--query", "length(Ledgers)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ QLDB service is available. Ledger count: %s", strings.TrimSpace(countResult)), nil

	case "check_swf_service":
		args := []string{"swf", "list-domains", "--registration-status", "REGISTERED", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SWF service not available or no access", nil
		}
		countArgs := []string{"swf", "list-domains", "--registration-status", "REGISTERED", "--output", "json", "--query", "length(domainInfos)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ SWF service is available. Domain count: %s", strings.TrimSpace(countResult)), nil

	case "check_costexplorer_service":
		args := []string{"ce", "get-usage-and-costs", "--time-period", "Start=2025-08-01,End=2025-08-02", "--granularity", "DAILY", "--metrics", "BlendedCost", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Cost Explorer service not available or no access", nil
		}
		return "✅ Cost Explorer service is available", nil

	case "check_budgets_service":
		args := []string{"budgets", "describe-budgets", "--account-id", "$(aws sts get-caller-identity --query Account --output text)", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Budgets service not available or no access", nil
		}
		return "✅ Budgets service is available", nil

	case "check_datasync_service":
		args := []string{"datasync", "list-tasks", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ DataSync service not available or no access", nil
		}
		countArgs := []string{"datasync", "list-tasks", "--output", "json", "--query", "length(Tasks)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ DataSync service is available. Task count: %s", strings.TrimSpace(countResult)), nil

	case "check_migrationhub_service":
		args := []string{"mgh", "list-created-artifacts", "--progress-update-stream", "test", "--migration-task-name", "test", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Migration Hub service not available or no access", nil
		}
		return "✅ Migration Hub service is available", nil

	case "check_elasticbeanstalk_service":
		args := []string{"elasticbeanstalk", "describe-applications", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Elastic Beanstalk service not available or no access", nil
		}
		countArgs := []string{"elasticbeanstalk", "describe-applications", "--output", "json", "--query", "length(Applications)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Elastic Beanstalk service is available. Application count: %s", strings.TrimSpace(countResult)), nil

	case "check_cloudshell_service":
		args := []string{"cloudshell", "describe-environments", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudShell service not available or no access", nil
		}
		return "✅ CloudShell service is available", nil

	case "check_autoscaling_service":
		args := []string{"autoscaling", "describe-auto-scaling-groups", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Auto Scaling service not available or no access", nil
		}
		countArgs := []string{"autoscaling", "describe-auto-scaling-groups", "--output", "json", "--query", "length(AutoScalingGroups)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Auto Scaling service is available. ASG count: %s", strings.TrimSpace(countResult)), nil

	case "check_elb_service":
		args := []string{"elb", "describe-load-balancers", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ ELB Classic service not available or no access", nil
		}
		countArgs := []string{"elb", "describe-load-balancers", "--output", "json", "--query", "length(LoadBalancerDescriptions)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ ELB Classic service is available. Load balancer count: %s", strings.TrimSpace(countResult)), nil

	case "check_elbv2_service":
		args := []string{"elbv2", "describe-load-balancers", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ ELBv2 (ALB/NLB) service not available or no access", nil
		}
		countArgs := []string{"elbv2", "describe-load-balancers", "--output", "json", "--query", "length(LoadBalancers)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ ELBv2 (ALB/NLB) service is available. Load balancer count: %s", strings.TrimSpace(countResult)), nil

	case "check_efs_service":
		args := []string{"efs", "describe-file-systems", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ EFS service not available or no access", nil
		}
		countArgs := []string{"efs", "describe-file-systems", "--output", "json", "--query", "length(FileSystems)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ EFS service is available. File system count: %s", strings.TrimSpace(countResult)), nil

	case "check_glacier_service":
		args := []string{"glacier", "list-vaults", "--account-id", "-", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Glacier service not available or no access", nil
		}
		countArgs := []string{"glacier", "list-vaults", "--account-id", "-", "--output", "json", "--query", "length(VaultList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Glacier service is available. Vault count: %s", strings.TrimSpace(countResult)), nil

	case "check_lightsail_service":
		args := []string{"lightsail", "get-instances", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lightsail service not available or no access", nil
		}
		countArgs := []string{"lightsail", "get-instances", "--output", "json", "--query", "length(instances)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Lightsail service is available. Instance count: %s", strings.TrimSpace(countResult)), nil

	case "check_ses_service":
		args := []string{"ses", "list-identities", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ SES service not available or no access", nil
		}
		countArgs := []string{"ses", "list-identities", "--output", "json", "--query", "length(Identities)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ SES service is available. Identity count: %s", strings.TrimSpace(countResult)), nil

	case "check_codedeploy_service":
		args := []string{"deploy", "list-applications", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodeDeploy service not available or no access", nil
		}
		countArgs := []string{"deploy", "list-applications", "--output", "json", "--query", "length(applications)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ CodeDeploy service is available. Application count: %s", strings.TrimSpace(countResult)), nil

	case "check_codestar_service":
		args := []string{"codestar", "list-projects", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CodeStar service not available or no access", nil
		}
		return "✅ CodeStar service is available", nil

	case "check_cloud9_service":
		args := []string{"cloud9", "list-environments", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Cloud9 service not available or no access", nil
		}
		countArgs := []string{"cloud9", "list-environments", "--output", "json", "--query", "length(environmentIds)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Cloud9 service is available. Environment count: %s", strings.TrimSpace(countResult)), nil

	case "check_emrserverless_service":
		args := []string{"emr-serverless", "list-applications", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ EMR Serverless service not available or no access", nil
		}
		countArgs := []string{"emr-serverless", "list-applications", "--output", "json", "--query", "length(applications)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ EMR Serverless service is available. Application count: %s", strings.TrimSpace(countResult)), nil

	case "check_datapipeline_service":
		args := []string{"datapipeline", "list-pipelines", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Data Pipeline service not available or no access", nil
		}
		countArgs := []string{"datapipeline", "list-pipelines", "--output", "json", "--query", "length(pipelineIdList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Data Pipeline service is available. Pipeline count: %s", strings.TrimSpace(countResult)), nil

	case "check_firehose_service":
		args := []string{"firehose", "list-delivery-streams", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Kinesis Firehose service not available or no access", nil
		}
		countArgs := []string{"firehose", "list-delivery-streams", "--output", "json", "--query", "length(DeliveryStreamNames)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Kinesis Firehose service is available. Stream count: %s", strings.TrimSpace(countResult)), nil

	case "check_kinesisanalytics_service":
		args := []string{"kinesisanalytics", "list-applications", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Kinesis Analytics service not available or no access", nil
		}
		countArgs := []string{"kinesisanalytics", "list-applications", "--output", "json", "--query", "length(ApplicationSummaries)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Kinesis Analytics service is available. Application count: %s", strings.TrimSpace(countResult)), nil

	case "check_elastictranscoder_service":
		args := []string{"elastictranscoder", "list-pipelines", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Elastic Transcoder service not available or no access", nil
		}
		countArgs := []string{"elastictranscoder", "list-pipelines", "--output", "json", "--query", "length(Pipelines)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Elastic Transcoder service is available. Pipeline count: %s", strings.TrimSpace(countResult)), nil

	case "check_kinesisvideo_service":
		args := []string{"kinesisvideo", "list-streams", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Kinesis Video service not available or no access", nil
		}
		countArgs := []string{"kinesisvideo", "list-streams", "--output", "json", "--query", "length(StreamInfoList)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Kinesis Video service is available. Stream count: %s", strings.TrimSpace(countResult)), nil

	case "check_mediaconvert_service":
		args := []string{"mediaconvert", "list-jobs", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MediaConvert service not available or no access", nil
		}
		return "✅ MediaConvert service is available", nil

	case "check_medialive_service":
		args := []string{"medialive", "list-channels", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MediaLive service not available or no access", nil
		}
		countArgs := []string{"medialive", "list-channels", "--output", "json", "--query", "length(Channels)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ MediaLive service is available. Channel count: %s", strings.TrimSpace(countResult)), nil

	case "check_iotanalytics_service":
		args := []string{"iotanalytics", "list-channels", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT Analytics service not available or no access", nil
		}
		countArgs := []string{"iotanalytics", "list-channels", "--output", "json", "--query", "length(channelSummaries)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ IoT Analytics service is available. Channel count: %s", strings.TrimSpace(countResult)), nil

	case "check_iotevents_service":
		args := []string{"iotevents", "list-detector-models", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT Events service not available or no access", nil
		}
		return "✅ IoT Events service is available", nil

	case "check_iotsitewise_service":
		args := []string{"iotsitewise", "list-assets", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT SiteWise service not available or no access", nil
		}
		countArgs := []string{"iotsitewise", "list-assets", "--output", "json", "--query", "length(assetSummaries)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ IoT SiteWise service is available. Asset count: %s", strings.TrimSpace(countResult)), nil

	case "check_greengrass_service":
		args := []string{"greengrass", "list-core-definitions", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Greengrass service not available or no access", nil
		}
		return "✅ Greengrass service is available", nil

	case "check_auditmanager_service":
		args := []string{"auditmanager", "get-assessments", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Audit Manager service not available or no access", nil
		}
		return "✅ Audit Manager service is available", nil

	case "check_wellarchitected_service":
		args := []string{"wellarchitected", "list-workloads", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Well-Architected Tool service not available or no access", nil
		}
		return "✅ Well-Architected Tool service is available", nil

	case "check_support_service":
		args := []string{"support", "describe-cases", "--include-resolved-cases", "false", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Support service not available or no access", nil
		}
		return "✅ Support (Trusted Advisor) service is available", nil

	case "check_braket_service":
		args := []string{"braket", "search-devices", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Braket (Quantum) service not available or no access", nil
		}
		return "✅ Braket (Quantum) service is available", nil

	case "check_robomaker_service":
		args := []string{"robomaker", "list-robots", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ RoboMaker service not available or no access", nil
		}
		return "✅ RoboMaker service is available", nil

	case "check_groundstation_service":
		args := []string{"groundstation", "list-ground-stations", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Ground Station service not available or no access", nil
		}
		return "✅ Ground Station service is available", nil

	case "check_gamelift_service":
		args := []string{"gamelift", "list-fleets", "--limit", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ GameLift service not available or no access", nil
		}
		countArgs := []string{"gamelift", "list-fleets", "--output", "json", "--query", "length(FleetIds)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ GameLift service is available. Fleet count: %s", strings.TrimSpace(countResult)), nil

	case "check_workmail_service":
		args := []string{"workmail", "list-organizations", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ WorkMail service not available or no access", nil
		}
		return "✅ WorkMail service is available", nil

	case "check_workdocs_service":
		args := []string{"workdocs", "describe-users", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ WorkDocs service not available or no access", nil
		}
		return "✅ WorkDocs service is available", nil

	case "check_chime_service":
		args := []string{"chime", "list-accounts", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Chime service not available or no access", nil
		}
		return "✅ Chime service is available", nil

	case "check_mediapackage_service":
		args := []string{"mediapackage", "list-channels", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MediaPackage service not available or no access", nil
		}
		return "✅ MediaPackage service is available", nil

	case "check_mediastore_service":
		args := []string{"mediastore", "list-containers", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MediaStore service not available or no access", nil
		}
		countArgs := []string{"mediastore", "list-containers", "--output", "json", "--query", "length(Containers)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ MediaStore service is available. Container count: %s", strings.TrimSpace(countResult)), nil

	case "check_mediatailor_service":
		args := []string{"mediatailor", "list-playback-configurations", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ MediaTailor service not available or no access", nil
		}
		return "✅ MediaTailor service is available", nil

	case "check_ivs_service":
		args := []string{"ivs", "list-channels", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Interactive Video Service (IVS) not available or no access", nil
		}
		return "✅ Interactive Video Service (IVS) is available", nil

	case "check_appflow_service":
		args := []string{"appflow", "list-flows", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ AppFlow service not available or no access", nil
		}
		return "✅ AppFlow service is available", nil

	case "check_cleanrooms_service":
		args := []string{"cleanrooms", "list-collaborations", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Clean Rooms service not available or no access", nil
		}
		return "✅ Clean Rooms service is available", nil

	case "check_cloudsearch_service":
		args := []string{"cloudsearch", "list-domain-names", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudSearch service not available or no access", nil
		}
		return "✅ CloudSearch service is available", nil

	case "check_dataexchange_service":
		args := []string{"dataexchange", "list-data-sets", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Data Exchange service not available or no access", nil
		}
		return "✅ Data Exchange service is available", nil

	case "check_finspace_service":
		args := []string{"finspace", "list-environments", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ FinSpace service not available or no access", nil
		}
		return "✅ FinSpace service is available", nil

	case "check_forecast_service":
		args := []string{"forecast", "list-datasets", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Forecast service not available or no access", nil
		}
		return "✅ Forecast service is available", nil

	case "check_frauddetector_service":
		args := []string{"frauddetector", "get-detectors", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Fraud Detector service not available or no access", nil
		}
		return "✅ Fraud Detector service is available", nil

	case "check_lookoutequipment_service":
		args := []string{"lookoutequipment", "list-datasets", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lookout for Equipment service not available or no access", nil
		}
		return "✅ Lookout for Equipment service is available", nil

	case "check_lookoutmetrics_service":
		args := []string{"lookoutmetrics", "list-anomaly-detectors", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lookout for Metrics service not available or no access", nil
		}
		return "✅ Lookout for Metrics service is available", nil

	case "check_lookoutvision_service":
		args := []string{"lookoutvision", "list-projects", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Lookout for Vision service not available or no access", nil
		}
		return "✅ Lookout for Vision service is available", nil

	case "check_monitron_service":
		args := []string{"monitron", "list-projects", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Monitron service not available or no access", nil
		}
		return "✅ Monitron service is available", nil

	case "check_detective_service":
		args := []string{"detective", "list-graphs", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Detective service not available or no access", nil
		}
		return "✅ Detective service is available", nil

	case "check_signer_service":
		args := []string{"signer", "list-signing-jobs", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Signer service not available or no access", nil
		}
		return "✅ Signer service is available", nil

	case "check_artifact_service":
		args := []string{"artifact", "list-reports", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Artifact service not available or no access", nil
		}
		return "✅ Artifact service is available", nil

	case "check_chatbot_service":
		args := []string{"chatbot", "describe-slack-channel-configurations", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Chatbot service not available or no access", nil
		}
		return "✅ Chatbot service is available", nil

	case "check_computeoptimizer_service":
		args := []string{"compute-optimizer", "get-enrollment-status", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Compute Optimizer service not available or no access", nil
		}
		return "✅ Compute Optimizer service is available", nil

	case "check_launchwizard_service":
		args := []string{"launch-wizard", "list-deployments", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Launch Wizard service not available or no access", nil
		}
		return "✅ Launch Wizard service is available", nil

	case "check_managedservices_service":
		// Note: Managed Services doesn't have public CLI operations
		return "❌ Managed Services has no public CLI operations available", nil

	case "check_proton_service":
		args := []string{"proton", "list-services", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Proton service not available or no access", nil
		}
		return "✅ Proton service is available", nil

	case "check_resiliencehub_service":
		args := []string{"resiliencehub", "list-apps", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Resilience Hub service not available or no access", nil
		}
		return "✅ Resilience Hub service is available", nil

	case "check_resourceexplorer_service":
		args := []string{"resource-explorer-2", "list-indexes", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Resource Explorer service not available or no access", nil
		}
		return "✅ Resource Explorer service is available", nil

	case "check_snowball_service":
		args := []string{"snowball", "list-jobs", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Snowball service not available or no access", nil
		}
		return "✅ Snowball service is available", nil

	case "check_mgn_service":
		args := []string{"mgn", "describe-source-servers", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Application Migration Service (MGN) not available or no access", nil
		}
		return "✅ Application Migration Service (MGN) is available", nil

	case "check_m2_service":
		args := []string{"m2", "list-applications", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Mainframe Modernization service not available or no access", nil
		}
		return "✅ Mainframe Modernization service is available", nil

	case "check_discovery_service":
		args := []string{"discovery", "describe-agents", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Application Discovery Service not available or no access", nil
		}
		return "✅ Application Discovery Service is available", nil

	case "check_cur_service":
		args := []string{"cur", "describe-report-definitions", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Cost and Usage Report service not available or no access", nil
		}
		return "✅ Cost and Usage Report service is available", nil

	case "check_applicationcostprofiler_service":
		args := []string{"application-cost-profiler", "list-report-definitions", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Application Cost Profiler service not available or no access", nil
		}
		return "✅ Application Cost Profiler service is available", nil

	case "check_managedblockchain_service":
		args := []string{"managedblockchain", "list-networks", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Managed Blockchain service not available or no access", nil
		}
		return "✅ Managed Blockchain service is available", nil

	case "check_alexaforbusiness_service":
		args := []string{"alexaforbusiness", "list-skills", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Alexa for Business service not available or no access", nil
		}
		return "✅ Alexa for Business service is available", nil

	case "check_outposts_service":
		args := []string{"outposts", "list-outposts", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Outposts service not available or no access", nil
		}
		return "✅ Outposts service is available", nil

	case "check_serverlessrepo_service":
		args := []string{"serverlessrepo", "list-applications", "--max-items", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Serverless Application Repository not available or no access", nil
		}
		return "✅ Serverless Application Repository is available", nil

	case "check_wavelength_service":
		args := []string{"ec2", "describe-carrier-gateways", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Wavelength service not available or no access", nil
		}
		return "✅ Wavelength service is available", nil

	case "check_redhatopenshiftaws_service":
		// ROSA uses the 'rosa' CLI, not AWS CLI
		return "❌ Red Hat OpenShift on AWS uses separate 'rosa' CLI tool", nil

	case "check_location_service":
		args := []string{"location", "list-maps", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Location Service not available or no access", nil
		}
		return "✅ Location Service is available", nil

	case "check_iot1click_service":
		args := []string{"iot1click-projects", "list-projects", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT 1-Click service not available or no access", nil
		}
		return "✅ IoT 1-Click service is available", nil

	case "check_iotfleetwise_service":
		args := []string{"iotfleetwise", "list-campaigns", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT FleetWise service not available or no access", nil
		}
		return "✅ IoT FleetWise service is available", nil

	case "check_iotthingsgraph_service":
		args := []string{"iotthingsgraph", "list-flow-templates", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT Things Graph service not available or no access", nil
		}
		return "✅ IoT Things Graph service is available", nil

	case "check_iottwinmaker_service":
		args := []string{"iottwinmaker", "list-workspaces", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ IoT TwinMaker service not available or no access", nil
		}
		return "✅ IoT TwinMaker service is available", nil

	case "check_cloudhsm_service":
		args := []string{"cloudhsm", "list-hapgs", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ CloudHSM service not available or no access", nil
		}
		return "✅ CloudHSM service is available", nil

	case "check_fms_service":
		args := []string{"fms", "list-policies", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Firewall Manager service not available or no access", nil
		}
		return "✅ Firewall Manager service is available", nil

	case "check_inspector2_service":
		args := []string{"inspector2", "list-findings", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Inspector v2 service not available or no access", nil
		}
		return "✅ Inspector v2 service is available", nil

	case "check_networkfirewall_service":
		args := []string{"network-firewall", "list-firewalls", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Network Firewall service not available or no access", nil
		}
		countArgs := []string{"network-firewall", "list-firewalls", "--output", "json", "--query", "length(Firewalls)"}
		countResult, _ := c.execAWSCLI(ctx, countArgs, profile)
		return fmt.Sprintf("✅ Network Firewall service is available. Firewall count: %s", strings.TrimSpace(countResult)), nil

	case "check_shield_service":
		args := []string{"shield", "list-subscriptions", "--max-results", "1", "--output", "table"}
		_, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "❌ Shield service not available or no access", nil
		}
		return "✅ Shield service is available", nil

	// COMPUTE operations
	case "list_ec2_instances":
		args := []string{"ec2", "describe-instances", "--output", "table", "--query", "Reservations[*].Instances[*].{ID:InstanceId,Type:InstanceType,State:State.Name,Name:Tags[?Key=='Name'].Value|[0]}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_instance":
		instanceID, ok := input["instance_id"].(string)
		if !ok {
			return "", fmt.Errorf("instance_id parameter required")
		}
		args := []string{"ec2", "describe-instances", "--instance-ids", instanceID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_ecs_clusters":
		args := []string{"ecs", "list-clusters", "--output", "table"}
		clusters, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "", err
		}

		// Also get services for each cluster
		serviceArgs := []string{"ecs", "list-services", "--output", "table"}
		services, _ := c.execAWSCLI(ctx, serviceArgs, profile)
		return fmt.Sprintf("Clusters:\n%s\n\nServices:\n%s", clusters, services), nil

	// SERVERLESS operations
	case "list_lambda_functions":
		args := []string{"lambda", "list-functions", "--output", "table", "--query", "Functions[*].{Name:FunctionName,Runtime:Runtime,LastModified:LastModified}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_lambda_function":
		functionName, ok := input["function_name"].(string)
		if !ok {
			return "", fmt.Errorf("function_name parameter required")
		}
		args := []string{"lambda", "get-function", "--function-name", functionName, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	// STORAGE operations
	case "list_s3_buckets":
		args := []string{"s3api", "list-buckets", "--output", "table", "--query", "Buckets[*].{Name:Name,Created:CreationDate}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_s3_bucket":
		bucketName, ok := input["bucket_name"].(string)
		if !ok {
			return "", fmt.Errorf("bucket_name parameter required")
		}
		args := []string{"s3api", "head-bucket", "--bucket", bucketName}
		return c.execAWSCLI(ctx, args, profile)

	// DATABASE operations
	case "list_rds_instances":
		args := []string{"rds", "describe-db-instances", "--output", "table", "--query", "DBInstances[*].{ID:DBInstanceIdentifier,Engine:Engine,Status:DBInstanceStatus,Class:DBInstanceClass}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_rds_instance":
		instanceID, ok := input["instance_id"].(string)
		if !ok {
			return "", fmt.Errorf("instance_id parameter required")
		}
		args := []string{"rds", "describe-db-instances", "--db-instance-identifier", instanceID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_dynamodb_tables":
		args := []string{"dynamodb", "list-tables", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_dynamodb_table":
		tableName, ok := input["table_name"].(string)
		if !ok {
			return "", fmt.Errorf("table_name parameter required")
		}
		args := []string{"dynamodb", "describe-table", "--table-name", tableName, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_rds_clusters":
		args := []string{"rds", "describe-db-clusters", "--output", "table", "--query", "DBClusters[*].{ID:DBClusterIdentifier,Engine:Engine,Status:Status,MultiAZ:MultiAZ}"}
		return c.execAWSCLI(ctx, args, profile)

	// NETWORKING operations
	case "list_vpcs":
		args := []string{"ec2", "describe-vpcs", "--output", "table", "--query", "Vpcs[*].{VpcId:VpcId,CidrBlock:CidrBlock,State:State,IsDefault:IsDefault}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_subnets":
		args := []string{"ec2", "describe-subnets", "--output", "table", "--query", "Subnets[*].{SubnetId:SubnetId,VpcId:VpcId,CidrBlock:CidrBlock,AZ:AvailabilityZone}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_security_groups":
		args := []string{"ec2", "describe-security-groups", "--output", "table", "--query", "SecurityGroups[*].{GroupId:GroupId,GroupName:GroupName,VpcId:VpcId,Description:Description}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_load_balancers":
		// Try both ALB and NLB
		albArgs := []string{"elbv2", "describe-load-balancers", "--output", "table"}
		albResult, _ := c.execAWSCLI(ctx, albArgs, profile)

		clbArgs := []string{"elb", "describe-load-balancers", "--output", "table"}
		clbResult, _ := c.execAWSCLI(ctx, clbArgs, profile)

		return fmt.Sprintf("Application/Network Load Balancers:\n%s\n\nClassic Load Balancers:\n%s", albResult, clbResult), nil

	case "list_route_tables":
		args := []string{"ec2", "describe-route-tables", "--output", "table", "--query", "RouteTables[*].{RouteTableId:RouteTableId,VpcId:VpcId,Main:Associations[?Main].Main|[0]}"}
		return c.execAWSCLI(ctx, args, profile)

	// MONITORING & LOGS operations
	case "get_recent_logs":
		// Use the existing log functionality
		return c.getRecentErrorLogs(ctx, "recent logs")

	case "list_cloudwatch_alarms":
		args := []string{"cloudwatch", "describe-alarms", "--output", "table", "--query", "MetricAlarms[*].{Name:AlarmName,State:StateValue,Reason:StateReason}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_log_groups":
		args := []string{"logs", "describe-log-groups", "--output", "table", "--query", "logGroups[*].{Name:logGroupName,Size:storedBytes,Retention:retentionInDays}"}
		return c.execAWSCLI(ctx, args, profile)

	// SECURITY & IAM operations (safe, names only)
	case "list_iam_roles":
		args := []string{"iam", "list-roles", "--output", "table", "--query", "Roles[*].{RoleName:RoleName,CreateDate:CreateDate}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_iam_users":
		args := []string{"iam", "list-users", "--output", "table", "--query", "Users[*].{UserName:UserName,CreateDate:CreateDate}"}
		return c.execAWSCLI(ctx, args, profile)

	// OTHER SERVICES operations
	case "list_api_gateways":
		restArgs := []string{"apigateway", "get-rest-apis", "--output", "table"}
		restResult, _ := c.execAWSCLI(ctx, restArgs, profile)

		httpArgs := []string{"apigatewayv2", "get-apis", "--output", "table"}
		httpResult, _ := c.execAWSCLI(ctx, httpArgs, profile)

		return fmt.Sprintf("REST APIs:\n%s\n\nHTTP APIs:\n%s", restResult, httpResult), nil

	case "list_cloudfront_distributions":
		args := []string{"cloudfront", "list-distributions", "--output", "table", "--query", "DistributionList.Items[*].{Id:Id,DomainName:DomainName,Status:Status}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_route53_zones":
		args := []string{"route53", "list-hosted-zones", "--output", "table", "--query", "HostedZones[*].{Id:Id,Name:Name,Type:Config.PrivateZone}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_secrets":
		args := []string{"secretsmanager", "list-secrets", "--output", "table", "--query", "SecretList[*].{Name:Name,LastChanged:LastChangedDate}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_ssm_parameters":
		args := []string{"ssm", "describe-parameters", "--output", "table", "--query", "Parameters[*].{Name:Name,Type:Type,LastModified:LastModifiedDate}"}
		return c.execAWSCLI(ctx, args, profile)

	// CONTAINER SERVICES operations
	case "list_ecr_repositories":
		args := []string{"ecr", "describe-repositories", "--output", "table", "--query", "repositories[*].{Name:repositoryName,URI:repositoryUri,Created:createdAt}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_eks_clusters":
		args := []string{"eks", "list-clusters", "--output", "table"}
		clusters, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "", err
		}

		// Also get cluster details
		detailArgs := []string{"eks", "describe-cluster", "--output", "json"}
		details, _ := c.execAWSCLI(ctx, detailArgs, profile)
		return fmt.Sprintf("EKS Clusters:\n%s\n\nCluster Details:\n%s", clusters, details), nil

	case "describe_ecr_repository":
		repoName, ok := input["repository_name"].(string)
		if !ok {
			return "", fmt.Errorf("repository_name parameter required")
		}
		args := []string{"ecr", "describe-images", "--repository-name", repoName, "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	// MESSAGE QUEUING & EVENTS operations
	case "list_sqs_queues":
		args := []string{"sqs", "list-queues", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_sqs_queue":
		queueURL, ok := input["queue_url"].(string)
		if !ok {
			return "", fmt.Errorf("queue_url parameter required")
		}
		args := []string{"sqs", "get-queue-attributes", "--queue-url", queueURL, "--attribute-names", "All", "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_sns_topics":
		args := []string{"sns", "list-topics", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_sns_topic":
		topicArn, ok := input["topic_arn"].(string)
		if !ok {
			return "", fmt.Errorf("topic_arn parameter required")
		}
		args := []string{"sns", "get-topic-attributes", "--topic-arn", topicArn, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_eventbridge_rules":
		args := []string{"events", "list-rules", "--output", "table", "--query", "Rules[*].{Name:Name,State:State,ScheduleExpression:ScheduleExpression}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_eventbridge_buses":
		args := []string{"events", "list-event-buses", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	// AUTO SCALING & OPTIMIZATION operations
	case "list_auto_scaling_groups":
		args := []string{"autoscaling", "describe-auto-scaling-groups", "--output", "table", "--query", "AutoScalingGroups[*].{Name:AutoScalingGroupName,Instances:Instances|length,Min:MinSize,Max:MaxSize,Desired:DesiredCapacity}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_auto_scaling_group":
		asgName, ok := input["asg_name"].(string)
		if !ok {
			return "", fmt.Errorf("asg_name parameter required")
		}
		args := []string{"autoscaling", "describe-auto-scaling-groups", "--auto-scaling-group-names", asgName, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_launch_templates":
		args := []string{"ec2", "describe-launch-templates", "--output", "table", "--query", "LaunchTemplates[*].{Name:LaunchTemplateName,ID:LaunchTemplateId,Version:LatestVersionNumber,Created:CreateTime}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_launch_template":
		templateID, ok := input["template_id"].(string)
		if !ok {
			return "", fmt.Errorf("template_id parameter required")
		}
		args := []string{"ec2", "describe-launch-template-versions", "--launch-template-id", templateID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	// ADDITIONAL STORAGE operations
	case "list_ebs_volumes":
		args := []string{"ec2", "describe-volumes", "--output", "table", "--query", "Volumes[*].{ID:VolumeId,Size:Size,Type:VolumeType,State:State,Device:Attachments[0].Device}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_ebs_volume":
		volumeID, ok := input["volume_id"].(string)
		if !ok {
			return "", fmt.Errorf("volume_id parameter required")
		}
		args := []string{"ec2", "describe-volumes", "--volume-ids", volumeID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_efs_filesystems":
		args := []string{"efs", "describe-file-systems", "--output", "table", "--query", "FileSystems[*].{ID:FileSystemId,State:LifeCycleState,Performance:PerformanceMode,Throughput:ThroughputMode}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_efs_filesystem":
		fsID, ok := input["filesystem_id"].(string)
		if !ok {
			return "", fmt.Errorf("filesystem_id parameter required")
		}
		args := []string{"efs", "describe-file-systems", "--file-system-id", fsID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	// ENHANCED SECURITY operations
	case "list_kms_keys":
		args := []string{"kms", "list-keys", "--output", "table", "--query", "Keys[*].{KeyId:KeyId}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_kms_key":
		keyID, ok := input["key_id"].(string)
		if !ok {
			return "", fmt.Errorf("key_id parameter required")
		}
		args := []string{"kms", "describe-key", "--key-id", keyID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_acm_certificates":
		args := []string{"acm", "list-certificates", "--output", "table", "--query", "CertificateSummaryList[*].{Domain:DomainName,Status:Status,Arn:CertificateArn}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_acm_certificate":
		certArn, ok := input["certificate_arn"].(string)
		if !ok {
			return "", fmt.Errorf("certificate_arn parameter required")
		}
		args := []string{"acm", "describe-certificate", "--certificate-arn", certArn, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_waf_webacls":
		args := []string{"wafv2", "list-web-acls", "--scope", "REGIONAL", "--output", "table"}
		regionalWAF, _ := c.execAWSCLI(ctx, args, profile)

		globalArgs := []string{"wafv2", "list-web-acls", "--scope", "CLOUDFRONT", "--output", "table"}
		globalWAF, _ := c.execAWSCLI(ctx, globalArgs, profile)

		return fmt.Sprintf("Regional WAF ACLs:\n%s\n\nCloudFront WAF ACLs:\n%s", regionalWAF, globalWAF), nil

	// DEVOPS & CI/CD operations
	case "list_codebuild_projects":
		args := []string{"codebuild", "list-projects", "--output", "table"}
		projects, err := c.execAWSCLI(ctx, args, profile)
		if err != nil {
			return "", err
		}

		// Get project details
		detailArgs := []string{"codebuild", "batch-get-projects", "--output", "json"}
		details, _ := c.execAWSCLI(ctx, detailArgs, profile)
		return fmt.Sprintf("CodeBuild Projects:\n%s\n\nProject Details:\n%s", projects, details), nil

	case "list_codepipelines":
		args := []string{"codepipeline", "list-pipelines", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_codepipeline":
		pipelineName, ok := input["pipeline_name"].(string)
		if !ok {
			return "", fmt.Errorf("pipeline_name parameter required")
		}
		args := []string{"codepipeline", "get-pipeline", "--name", pipelineName, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_codecommit_repositories":
		args := []string{"codecommit", "list-repositories", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	// ANALYTICS & BIG DATA operations
	case "list_kinesis_streams":
		args := []string{"kinesis", "list-streams", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_kinesis_stream":
		streamName, ok := input["stream_name"].(string)
		if !ok {
			return "", fmt.Errorf("stream_name parameter required")
		}
		args := []string{"kinesis", "describe-stream", "--stream-name", streamName, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_glue_jobs":
		args := []string{"glue", "get-jobs", "--output", "table", "--query", "Jobs[*].{Name:Name,Role:Role,CreatedOn:CreatedOn}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_glue_databases":
		args := []string{"glue", "get-databases", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_emr_clusters":
		args := []string{"emr", "list-clusters", "--output", "table", "--query", "Clusters[*].{ID:Id,Name:Name,State:Status.State}"}
		return c.execAWSCLI(ctx, args, profile)

	// MACHINE LEARNING operations
	case "list_sagemaker_endpoints":
		args := []string{"sagemaker", "list-endpoints", "--output", "table", "--query", "Endpoints[*].{Name:EndpointName,Status:EndpointStatus,Created:CreationTime}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_sagemaker_models":
		args := []string{"sagemaker", "list-models", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_sagemaker_notebook_instances":
		args := []string{"sagemaker", "list-notebook-instances", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	// CACHING operations
	case "list_elasticache_clusters":
		args := []string{"elasticache", "describe-cache-clusters", "--output", "table", "--query", "CacheClusters[*].{ID:CacheClusterId,Engine:Engine,Status:CacheClusterStatus,NodeType:CacheNodeType}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_elasticache_cluster":
		clusterID, ok := input["cluster_id"].(string)
		if !ok {
			return "", fmt.Errorf("cluster_id parameter required")
		}
		args := []string{"elasticache", "describe-cache-clusters", "--cache-cluster-id", clusterID, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	// APPLICATION INTEGRATION operations
	case "list_step_functions":
		args := []string{"stepfunctions", "list-state-machines", "--output", "table", "--query", "stateMachines[*].{Name:name,Status:status,Created:creationDate}"}
		return c.execAWSCLI(ctx, args, profile)

	case "describe_step_function":
		stateMachineArn, ok := input["state_machine_arn"].(string)
		if !ok {
			return "", fmt.Errorf("state_machine_arn parameter required")
		}
		args := []string{"stepfunctions", "describe-state-machine", "--state-machine-arn", stateMachineArn, "--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	// COST MANAGEMENT operations
	case "get_cost_and_usage":
		// Get cost for last 30 days
		args := []string{"ce", "get-cost-and-usage",
			"--time-period", "Start=2024-07-01,End=2024-08-01",
			"--granularity", "MONTHLY",
			"--metrics", "BlendedCost",
			"--output", "json"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_budgets":
		args := []string{"budgets", "describe-budgets", "--output", "table"}
		return c.execAWSCLI(ctx, args, profile)

	// INFRASTRUCTURE DISCOVERY operations
	case "discover_all_active_services":
		return c.discoverAllActiveServices(ctx, profile)

	case "get_infrastructure_overview":
		return c.getInfrastructureOverview(ctx, profile)

	case "check_all_services_parallel":
		return c.checkAllServicesParallel(ctx, profile)

	// TERRAFORM INTEGRATION operations
	case "get_terraform_outputs":
		return c.getTerraformOutputs(ctx, profile)

	case "get_terraform_state_summary":
		return c.getTerraformStateSummary(ctx, profile)

	// AI/ML SERVICES operations
	case "list_bedrock_foundation_models":
		args := []string{"bedrock", "list-foundation-models", "--output", "table", "--query", "modelSummaries[*].{ModelId:modelId,Provider:providerName,Name:modelName,Status:modelLifecycle.status}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_bedrock_custom_models":
		args := []string{"bedrock", "list-custom-models", "--output", "table", "--query", "modelSummaries[*].{ModelName:modelName,ModelArn:modelArn,Status:status,CreationTime:creationTime}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_bedrock_agents":
		args := []string{"bedrock-agent", "list-agents", "--output", "table", "--query", "agentSummaries[*].{AgentId:agentId,AgentName:agentName,Status:agentStatus,UpdatedAt:updatedAt}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_bedrock_knowledge_bases":
		args := []string{"bedrock-agent", "list-knowledge-bases", "--output", "table", "--query", "knowledgeBaseSummaries[*].{KnowledgeBaseId:knowledgeBaseId,Name:name,Status:status,UpdatedAt:updatedAt}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_bedrock_guardrails":
		args := []string{"bedrock", "list-guardrails", "--output", "table", "--query", "guardrails[*].{GuardrailId:id,Name:name,Status:status,Version:version}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_comprehend_jobs":
		args := []string{"comprehend", "list-sentiment-detection-jobs", "--output", "table", "--query", "SentimentDetectionJobPropertiesList[*].{JobName:JobName,Status:JobStatus,SubmitTime:SubmitTime,EndTime:EndTime}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_textract_jobs":
		args := []string{"textract", "list-document-analysis-jobs", "--output", "table", "--query", "DocumentAnalysisJobs[*].{JobId:JobId,Status:JobStatus,SubmissionTime:SubmissionTime,CompletionTime:CompletionTime}"}
		return c.execAWSCLI(ctx, args, profile)

	case "list_rekognition_collections":
		args := []string{"rekognition", "list-collections", "--output", "table", "--query", "CollectionIds"}
		return c.execAWSCLI(ctx, args, profile)

	// Fallback to existing methods for unsupported operations
	default:
		return c.GetRelevantContext(ctx, fmt.Sprintf("operation %s", toolName))
	}
}

// execAWSCLI executes AWS CLI commands directly
func (c *Client) execAWSCLI(ctx context.Context, args []string, profile *AIProfile) (string, error) {
	// Build AWS CLI command
	cmd := exec.CommandContext(ctx, "aws")
	cmd.Args = append(cmd.Args, args...)
	cmd.Args = append(cmd.Args, "--profile", profile.AWSProfile, "--region", profile.Region, "--no-cli-pager")

	if c.debug {
		fmt.Printf("🚀 Executing: %s\n", strings.Join(cmd.Args, " "))
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		if c.debug {
			fmt.Printf("❌ Command failed: %v, output: %s\n", err, string(output))
		}
		return "", fmt.Errorf("AWS CLI command failed: %w, output: %s", err, string(output))
	}

	if c.debug {
		fmt.Printf("✅ Command output (%d bytes): %s\n", len(output), string(output))
	}
	return string(output), nil
}

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

// discoverAllActiveServices discovers all active AWS services by running service checks in parallel
func (c *Client) discoverAllActiveServices(ctx context.Context, profile *AIProfile) (string, error) {
	serviceChecks := []string{
		"check_ec2_service",
		"check_ecs_service",
		"check_lambda_service",
		"check_rds_service",
		"check_s3_service",
		"check_dynamodb_service",
		"check_sqs_service",
		"check_sns_service",
		"check_eventbridge_service",
		"check_ecr_service",
		"check_elasticsearch_service",
		"check_opensearch_service",
		"check_eks_service",
		"check_elasticache_service",
		"check_redshift_service",
		"check_kinesis_service",
		"check_cloudformation_service",
		"check_kms_service",
		"check_secretsmanager_service",
		"check_route53_service",
		"check_cloudfront_service",
		"check_apigateway_service",
		"check_bedrock_service",
		"check_codecommit_service",
		"check_codebuild_service",
		"check_codepipeline_service",
		"check_sagemaker_service",
		"check_glue_service",
		"check_athena_service",
		"check_emr_service",
		"check_stepfunctions_service",
		"check_cloudwatch_service",
		"check_logs_service",
		"check_xray_service",
		"check_cognito_service",
		"check_wafv2_service",
		"check_shield_service",
		"check_acm_service",
		"check_cloudtrail_service",
		"check_config_service",
		"check_guardduty_service",
		"check_ssm_service",
		"check_batch_service",
		"check_appsync_service",
		"check_amplify_service",
		"check_comprehend_service",
		"check_textract_service",
		"check_rekognition_service",
		"check_polly_service",
		"check_transcribe_service",
		"check_translate_service",
		"check_personalize_service",
		"check_kendra_service",
		"check_lex_service",
		"check_apprunner_service",
		"check_documentdb_service",
		"check_neptune_service",
		"check_timestream_service",
		"check_inspector_service",
		"check_macie_service",
		"check_backup_service",
		"check_organizations_service",
		"check_quicksight_service",
		"check_msk_service",
		"check_transitgateway_service",
		"check_securityhub_service",
		"check_servicecatalog_service",
		"check_lakeformation_service",
		"check_mq_service",
		"check_fsx_service",
		"check_directconnect_service",
		"check_dms_service",
		"check_globalaccelerator_service",
		"check_networkfirewall_service",
		"check_workspaces_service",
		"check_connect_service",
		"check_iot_service",
		"check_codeartifact_service",
		"check_codeguru_service",
		"check_devicefarm_service",
		"check_pinpoint_service",
		"check_storagegateway_service",
		"check_transferfamily_service",
		"check_appmesh_service",
		"check_privatelink_service",
		"check_controltower_service",
		"check_licensemanager_service",
		"check_resourcegroups_service",
		"check_directoryservice_service",
		"check_sso_service",
		"check_privateca_service",
		"check_memorydb_service",
		"check_keyspaces_service",
		"check_qldb_service",
		"check_swf_service",
		"check_costexplorer_service",
		"check_budgets_service",
		"check_datasync_service",
		"check_migrationhub_service",
		"check_elasticbeanstalk_service",
		"check_cloudshell_service",
		"check_autoscaling_service",
		"check_elb_service",
		"check_elbv2_service",
		"check_efs_service",
		"check_glacier_service",
		"check_lightsail_service",
		"check_ses_service",
		"check_codedeploy_service",
		"check_codestar_service",
		"check_cloud9_service",
		"check_emrserverless_service",
		"check_datapipeline_service",
		"check_firehose_service",
		"check_kinesisanalytics_service",
		"check_elastictranscoder_service",
		"check_kinesisvideo_service",
		"check_mediaconvert_service",
		"check_medialive_service",
		"check_iotanalytics_service",
		"check_iotevents_service",
		"check_iotsitewise_service",
		"check_greengrass_service",
		"check_auditmanager_service",
		"check_wellarchitected_service",
		"check_support_service",
		"check_braket_service",
		"check_robomaker_service",
		"check_groundstation_service",
		"check_gamelift_service",
		"check_workmail_service",
		"check_workdocs_service",
		"check_chime_service",
		"check_mediapackage_service",
		"check_mediastore_service",
		"check_mediatailor_service",
		"check_ivs_service",
		"check_appflow_service",
		"check_cleanrooms_service",
		"check_cloudsearch_service",
		"check_dataexchange_service",
		"check_finspace_service",
		"check_forecast_service",
		"check_frauddetector_service",
		"check_lookoutequipment_service",
		"check_lookoutmetrics_service",
		"check_lookoutvision_service",
		"check_monitron_service",
		"check_detective_service",
		"check_signer_service",
		"check_artifact_service",
		"check_chatbot_service",
		"check_computeoptimizer_service",
		"check_launchwizard_service",
		"check_managedservices_service",
		"check_proton_service",
		"check_resiliencehub_service",
		"check_resourceexplorer_service",
		"check_snowball_service",
		"check_mgn_service",
		"check_m2_service",
		"check_discovery_service",
		"check_cur_service",
		"check_applicationcostprofiler_service",
		"check_managedblockchain_service",
		"check_alexaforbusiness_service",
		"check_outposts_service",
		"check_serverlessrepo_service",
		"check_wavelength_service",
		"check_redhatopenshiftaws_service",
		"check_location_service",
		"check_iot1click_service",
		"check_iotfleetwise_service",
		"check_iotthingsgraph_service",
		"check_iottwinmaker_service",
		"check_cloudhsm_service",
		"check_fms_service",
		"check_inspector2_service",
		"check_networkfirewall_service",
		"check_shield_service",
	}

	operations := make([]LLMOperation, len(serviceChecks))
	for i, check := range serviceChecks {
		operations[i] = LLMOperation{
			Operation:  check,
			Reason:     "Infrastructure discovery",
			Parameters: make(map[string]interface{}),
		}
	}

	return c.executeOperationsWithProfile(ctx, operations, profile)
}

// getInfrastructureOverview gets a comprehensive overview of the entire infrastructure
func (c *Client) getInfrastructureOverview(ctx context.Context, profile *AIProfile) (string, error) {
	// Core infrastructure operations to run in parallel
	overviewOps := []LLMOperation{
		{Operation: "list_ec2_instances", Reason: "Get EC2 overview", Parameters: make(map[string]interface{})},
		{Operation: "list_ecs_clusters", Reason: "Get ECS overview", Parameters: make(map[string]interface{})},
		{Operation: "list_lambda_functions", Reason: "Get Lambda overview", Parameters: make(map[string]interface{})},
		{Operation: "list_rds_instances", Reason: "Get RDS overview", Parameters: make(map[string]interface{})},
		{Operation: "list_s3_buckets", Reason: "Get S3 overview", Parameters: make(map[string]interface{})},
		{Operation: "list_dynamodb_tables", Reason: "Get DynamoDB overview", Parameters: make(map[string]interface{})},
		{Operation: "list_vpcs", Reason: "Get VPC overview", Parameters: make(map[string]interface{})},
		{Operation: "list_load_balancers", Reason: "Get Load Balancer overview", Parameters: make(map[string]interface{})},
		{Operation: "list_api_gateways", Reason: "Get API Gateway overview", Parameters: make(map[string]interface{})},
		{Operation: "list_cloudfront_distributions", Reason: "Get CloudFront overview", Parameters: make(map[string]interface{})},
		{Operation: "list_sqs_queues", Reason: "Get SQS overview", Parameters: make(map[string]interface{})},
		{Operation: "list_sns_topics", Reason: "Get SNS overview", Parameters: make(map[string]interface{})},
		{Operation: "list_ecr_repositories", Reason: "Get ECR overview", Parameters: make(map[string]interface{})},
		{Operation: "list_batch_jobs", Reason: "Get Batch overview", Parameters: make(map[string]interface{})},
		{Operation: "list_cloudwatch_alarms", Reason: "Get monitoring overview", Parameters: make(map[string]interface{})},
	}

	return c.executeOperationsWithProfile(ctx, overviewOps, profile)
}

// checkAllServicesParallel runs all service availability checks in parallel
func (c *Client) checkAllServicesParallel(ctx context.Context, profile *AIProfile) (string, error) {
	allServiceChecks := []LLMOperation{
		{Operation: "check_ec2_service", Reason: "Check EC2 availability", Parameters: make(map[string]interface{})},
		{Operation: "check_ecs_service", Reason: "Check ECS availability", Parameters: make(map[string]interface{})},
		{Operation: "check_lambda_service", Reason: "Check Lambda availability", Parameters: make(map[string]interface{})},
		{Operation: "check_rds_service", Reason: "Check RDS availability", Parameters: make(map[string]interface{})},
		{Operation: "check_s3_service", Reason: "Check S3 availability", Parameters: make(map[string]interface{})},
		{Operation: "check_dynamodb_service", Reason: "Check DynamoDB availability", Parameters: make(map[string]interface{})},
		{Operation: "check_sqs_service", Reason: "Check SQS availability", Parameters: make(map[string]interface{})},
		{Operation: "check_sns_service", Reason: "Check SNS availability", Parameters: make(map[string]interface{})},
		{Operation: "check_eventbridge_service", Reason: "Check EventBridge availability", Parameters: make(map[string]interface{})},
		{Operation: "check_ecr_service", Reason: "Check ECR availability", Parameters: make(map[string]interface{})},
	}

	return c.executeOperationsWithProfile(ctx, allServiceChecks, profile)
}

// getTerraformOutputs gets terraform outputs from the configured workspace
func (c *Client) getTerraformOutputs(ctx context.Context, profile *AIProfile) (string, error) {
	// Get default workspace
	workspace := viper.GetString("terraform.default_workspace")
	if workspace == "" {
		workspace = "dev"
	}

	// Try to create terraform client
	tfClient, err := tfclient.NewClient(workspace)
	if err != nil {
		return fmt.Sprintf("❌ Unable to get terraform outputs: %v", err), nil
	}

	outputs, err := tfClient.GetTerraformOutputs(ctx)
	if err != nil {
		return fmt.Sprintf("❌ Failed to get terraform outputs: %v", err), nil
	}

	if len(outputs) == 0 {
		return "No terraform outputs available", nil
	}

	var result strings.Builder
	result.WriteString("Terraform Outputs:\n")
	for key, value := range outputs {
		result.WriteString(fmt.Sprintf("  %s: %v\n", key, value))
	}

	return result.String(), nil
}

// getTerraformStateSummary gets a summary of terraform state resources
func (c *Client) getTerraformStateSummary(ctx context.Context, profile *AIProfile) (string, error) {
	// Get default workspace
	workspace := viper.GetString("terraform.default_workspace")
	if workspace == "" {
		workspace = "dev"
	}

	// Try to create terraform client
	tfClient, err := tfclient.NewClient(workspace)
	if err != nil {
		return fmt.Sprintf("❌ Unable to get terraform state: %v", err), nil
	}

	context, err := tfClient.GetRelevantContext(ctx, "terraform state summary")
	if err != nil {
		return fmt.Sprintf("❌ Failed to get terraform state: %v", err), nil
	}

	return context, nil
}
