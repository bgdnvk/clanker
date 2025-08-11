package aws

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

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
