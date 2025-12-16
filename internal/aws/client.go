package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/viper"
)

type Client struct {
	cfg            aws.Config
	profile        string
	debug          bool
	ec2            *ec2.Client
	ecs            *ecs.Client
	iam            *iam.Client
	lambda         *lambda.Client
	rds            *rds.Client
	s3             *s3.Client
	batch          *batch.Client
	cloudwatch     *cloudwatch.Client
	cloudwatchlogs *cloudwatchlogs.Client
}

func NewClient(ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config: %w", err)
	}

	return &Client{
		cfg:            cfg,
		ec2:            ec2.NewFromConfig(cfg),
		ecs:            ecs.NewFromConfig(cfg),
		iam:            iam.NewFromConfig(cfg),
		lambda:         lambda.NewFromConfig(cfg),
		rds:            rds.NewFromConfig(cfg),
		s3:             s3.NewFromConfig(cfg),
		batch:          batch.NewFromConfig(cfg),
		cloudwatch:     cloudwatch.NewFromConfig(cfg),
		cloudwatchlogs: cloudwatchlogs.NewFromConfig(cfg),
	}, nil
}

// awsCredentialsFromCLI represents AWS credentials returned by CLI
type awsCredentialsFromCLI struct {
	Version         int    `json:"Version"`
	AccessKeyId     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

// getCredentialsFromCLI uses AWS CLI to get fresh credentials for the profile
func getCredentialsFromCLI(ctx context.Context, profile string) (*awsCredentialsFromCLI, error) {
	// For SSO profiles, use export-credentials with process format
	cmd := exec.CommandContext(ctx, "aws", "configure", "export-credentials", "--profile", profile, "--format", "process")
	cmd.Env = append(os.Environ(), fmt.Sprintf("AWS_PROFILE=%s", profile))

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials from AWS CLI: %w", err)
	}

	var creds awsCredentialsFromCLI
	if err := json.Unmarshal(output, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse AWS CLI credentials response: %w", err)
	}

	return &creds, nil
}

func NewClientWithProfile(ctx context.Context, profile string) (*Client, error) {
	return NewClientWithProfileAndDebug(ctx, profile, false)
}

func NewClientWithProfileAndDebug(ctx context.Context, profile string, debug bool) (*Client, error) {
	// Try to get credentials from AWS CLI first (works better with SSO)
	creds, err := getCredentialsFromCLI(ctx, profile)
	if err != nil {
		// Fallback to standard SDK approach
		cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile))
		if err != nil {
			return nil, fmt.Errorf("unable to load SDK config for profile %s: %w", profile, err)
		}

		return &Client{
			cfg:            cfg,
			profile:        profile,
			debug:          debug,
			ec2:            ec2.NewFromConfig(cfg),
			ecs:            ecs.NewFromConfig(cfg),
			iam:            iam.NewFromConfig(cfg),
			lambda:         lambda.NewFromConfig(cfg),
			rds:            rds.NewFromConfig(cfg),
			s3:             s3.NewFromConfig(cfg),
			batch:          batch.NewFromConfig(cfg),
			cloudwatch:     cloudwatch.NewFromConfig(cfg),
			cloudwatchlogs: cloudwatchlogs.NewFromConfig(cfg),
		}, nil
	}

	// Create AWS config with CLI-provided credentials
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			creds.AccessKeyId,
			creds.SecretAccessKey,
			creds.SessionToken,
		)),
		config.WithSharedConfigProfile(profile),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config with CLI credentials for profile %s: %w", profile, err)
	}

	return &Client{
		cfg:            cfg,
		profile:        profile,
		debug:          debug,
		ec2:            ec2.NewFromConfig(cfg),
		ecs:            ecs.NewFromConfig(cfg),
		iam:            iam.NewFromConfig(cfg),
		lambda:         lambda.NewFromConfig(cfg),
		rds:            rds.NewFromConfig(cfg),
		s3:             s3.NewFromConfig(cfg),
		batch:          batch.NewFromConfig(cfg),
		cloudwatch:     cloudwatch.NewFromConfig(cfg),
		cloudwatchlogs: cloudwatchlogs.NewFromConfig(cfg),
	}, nil
}

func (c *Client) GetRelevantContext(ctx context.Context, question string) (string, error) {
	var context strings.Builder

	// Analyze question to determine what AWS services to query
	questionLower := strings.ToLower(question)

	if strings.Contains(questionLower, "ec2") || strings.Contains(questionLower, "instance") {
		ec2Info, err := c.getEC2Info(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get EC2 info: %w", err)
		}
		context.WriteString("EC2 Instances:\n")
		context.WriteString(ec2Info)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "lambda") || strings.Contains(questionLower, "function") {
		lambdaInfo, err := c.getLambdaInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get Lambda info: %w", err)
		}
		context.WriteString("Lambda Functions:\n")
		context.WriteString(lambdaInfo)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "rds") || strings.Contains(questionLower, "database") {
		rdsInfo, err := c.getRDSInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get RDS info: %w", err)
		}
		context.WriteString("RDS Instances:\n")
		context.WriteString(rdsInfo)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "s3") || strings.Contains(questionLower, "bucket") {
		s3Info, err := c.getS3Info(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get S3 info: %w", err)
		}
		context.WriteString("S3 Buckets:\n")
		context.WriteString(s3Info)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "ecs") || strings.Contains(questionLower, "container") {
		ecsInfo, err := c.getECSInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get ECS info: %w", err)
		}
		context.WriteString("ECS Services:\n")
		context.WriteString(ecsInfo)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "iam") || strings.Contains(questionLower, "role") {
		rolesInfo, err := c.getIAMRolesInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get IAM roles info: %w", err)
		}
		context.WriteString("IAM Roles:\n")
		context.WriteString(rolesInfo)
		context.WriteString("\n\n")
	}

	if strings.Contains(questionLower, "log") || strings.Contains(questionLower, "cloudwatch") || strings.Contains(questionLower, "error") {
		logsInfo, err := c.getCloudWatchLogsInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get CloudWatch logs info: %w", err)
		}
		context.WriteString("CloudWatch Log Groups:\n")
		context.WriteString(logsInfo)
		context.WriteString("\n\n")

		// If asking specifically for errors, fetch recent error logs
		if strings.Contains(questionLower, "error") || strings.Contains(questionLower, "last error") {
			errorLogs, err := c.getRecentErrorLogs(ctx, questionLower)
			if err != nil {
				// Don't fail completely if error logs can't be fetched
				context.WriteString(fmt.Sprintf("Note: Could not fetch recent error logs: %v\n\n", err))
			} else if errorLogs != "" {
				context.WriteString("Recent Error Logs:\n")
				context.WriteString(errorLogs)
				context.WriteString("\n\n")
			}
		}

		// If asking for log stream of specific service, fetch recent logs
		if strings.Contains(questionLower, "log stream") || strings.Contains(questionLower, "recent log") ||
			(strings.Contains(questionLower, "last") && (strings.Contains(questionLower, "chat") ||
				strings.Contains(questionLower, "api") || strings.Contains(questionLower, "writer") ||
				strings.Contains(questionLower, "clip") || strings.Contains(questionLower, "lambda"))) {
			serviceLogs, err := c.getServiceLogs(ctx, questionLower)
			if err != nil {
				context.WriteString(fmt.Sprintf("Note: Could not fetch service logs: %v\n\n", err))
			} else if serviceLogs != "" {
				context.WriteString("Recent Service Logs:\n")
				context.WriteString(serviceLogs)
				context.WriteString("\n\n")
			}
		}
	}

	if strings.Contains(questionLower, "alarm") || strings.Contains(questionLower, "alert") || strings.Contains(questionLower, "cloudwatch") {
		alarmInfo, err := c.GetRecentAlarms(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get alarm info: %w", err)
		}
		context.WriteString("CloudWatch Alarms:\n")
		context.WriteString(alarmInfo)
		context.WriteString("\n\n")
	}

	return context.String(), nil
}

func (c *Client) getIAMRolesInfo(ctx context.Context) (string, error) {
	// IAM is global; this lists roles in the account. Keep output bounded.
	const maxRoles = 100

	var info strings.Builder
	count := 0
	var marker *string

	for {
		out, err := c.iam.ListRoles(ctx, &iam.ListRolesInput{Marker: marker})
		if err != nil {
			return "", err
		}

		for _, role := range out.Roles {
			info.WriteString(fmt.Sprintf("- Role: %s, Arn: %s, Created: %s\n",
				aws.ToString(role.RoleName),
				aws.ToString(role.Arn),
				aws.ToTime(role.CreateDate).Format(time.RFC3339)))
			count++
			if count >= maxRoles {
				info.WriteString(fmt.Sprintf("(showing first %d roles)\n", maxRoles))
				return info.String(), nil
			}
		}

		if out.IsTruncated && out.Marker != nil {
			marker = out.Marker
			continue
		}
		break
	}

	if count == 0 {
		return "(no roles found)\n", nil
	}
	return info.String(), nil
}

func (c *Client) getEC2Info(ctx context.Context) (string, error) {
	result, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			info.WriteString(fmt.Sprintf("- Instance ID: %s, Type: %s, State: %s\n",
				aws.ToString(instance.InstanceId),
				string(instance.InstanceType),
				string(instance.State.Name)))
		}
	}

	return info.String(), nil
}

func (c *Client) getLambdaInfo(ctx context.Context) (string, error) {
	result, err := c.lambda.ListFunctions(ctx, &lambda.ListFunctionsInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, function := range result.Functions {
		url := ""
		auth := ""
		urlCfg, err := c.lambda.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{
			FunctionName: function.FunctionName,
		})
		if err == nil {
			url = aws.ToString(urlCfg.FunctionUrl)
			auth = string(urlCfg.AuthType)
		}

		if url != "" {
			info.WriteString(fmt.Sprintf("- Function: %s, Runtime: %s, URL: %s, Auth: %s, Last Modified: %s\n",
				aws.ToString(function.FunctionName),
				string(function.Runtime),
				url,
				auth,
				aws.ToString(function.LastModified)))
		} else {
			info.WriteString(fmt.Sprintf("- Function: %s, Runtime: %s, Last Modified: %s\n",
				aws.ToString(function.FunctionName),
				string(function.Runtime),
				aws.ToString(function.LastModified)))
		}
	}

	return info.String(), nil
}

func (c *Client) getRDSInfo(ctx context.Context) (string, error) {
	result, err := c.rds.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, instance := range result.DBInstances {
		info.WriteString(fmt.Sprintf("- DB Instance: %s, Engine: %s, Status: %s\n",
			aws.ToString(instance.DBInstanceIdentifier),
			aws.ToString(instance.Engine),
			aws.ToString(instance.DBInstanceStatus)))
	}

	return info.String(), nil
}

func (c *Client) getS3Info(ctx context.Context) (string, error) {
	result, err := c.s3.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, bucket := range result.Buckets {
		info.WriteString(fmt.Sprintf("- Bucket: %s, Created: %s\n",
			aws.ToString(bucket.Name),
			bucket.CreationDate.Format("2006-01-02")))
	}

	return info.String(), nil
}

func (c *Client) getECSInfo(ctx context.Context) (string, error) {
	clusters, err := c.ecs.ListClusters(ctx, &ecs.ListClustersInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, cluster := range clusters.ClusterArns {
		services, err := c.ecs.ListServices(ctx, &ecs.ListServicesInput{
			Cluster: aws.String(cluster),
		})
		if err != nil {
			continue
		}

		info.WriteString(fmt.Sprintf("Cluster: %s\n", cluster))
		for _, service := range services.ServiceArns {
			info.WriteString(fmt.Sprintf("  - Service: %s\n", service))
		}
	}

	return info.String(), nil
}

func (c *Client) getECSTasksInfo(ctx context.Context, clusterFilter string) (string, error) {
	// Get clusters
	clusters, err := c.ecs.ListClusters(ctx, &ecs.ListClustersInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, clusterArn := range clusters.ClusterArns {
		clusterName := clusterArn
		if idx := strings.LastIndex(clusterArn, "/"); idx != -1 {
			clusterName = clusterArn[idx+1:]
		}

		// Filter by cluster if specified
		if clusterFilter != "" && !strings.Contains(strings.ToLower(clusterName), strings.ToLower(clusterFilter)) {
			continue
		}

		// Get tasks for this cluster
		tasks, err := c.ecs.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(clusterArn),
		})
		if err != nil {
			continue
		}

		if len(tasks.TaskArns) > 0 {
			info.WriteString(fmt.Sprintf("Cluster: %s\n", clusterName))

			// Get task details
			taskDetails, err := c.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(clusterArn),
				Tasks:   tasks.TaskArns,
			})
			if err == nil {
				for _, task := range taskDetails.Tasks {
					info.WriteString(fmt.Sprintf("  - Task: %s\n", aws.ToString(task.TaskArn)))
					info.WriteString(fmt.Sprintf("    Status: %s\n", aws.ToString(task.LastStatus)))
					if task.TaskDefinitionArn != nil {
						info.WriteString(fmt.Sprintf("    Definition: %s\n", aws.ToString(task.TaskDefinitionArn)))
					}
				}
			}
		}
	}

	return info.String(), nil
}

func (c *Client) getBatchJobsInfo(ctx context.Context, limit int) (string, error) {
	if limit == 0 {
		limit = 5
	}

	// Get job queues first
	queues, err := c.batch.DescribeJobQueues(ctx, &batch.DescribeJobQueuesInput{})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, queue := range queues.JobQueues {
		if queue.JobQueueName == nil {
			continue
		}

		// List jobs in this queue
		jobs, err := c.batch.ListJobs(ctx, &batch.ListJobsInput{
			JobQueue:   queue.JobQueueName,
			MaxResults: aws.Int32(int32(limit)),
		})
		if err != nil {
			continue
		}

		if len(jobs.JobSummaryList) > 0 {
			info.WriteString(fmt.Sprintf("Queue: %s\n", aws.ToString(queue.JobQueueName)))
			for _, job := range jobs.JobSummaryList {
				info.WriteString(fmt.Sprintf("  - Job: %s\n", aws.ToString(job.JobName)))
				info.WriteString(fmt.Sprintf("    ID: %s\n", aws.ToString(job.JobId)))
				info.WriteString(fmt.Sprintf("    Status: %s\n", job.Status))
				if job.CreatedAt != nil {
					info.WriteString(fmt.Sprintf("    Created: %v\n", *job.CreatedAt))
				}
			}
		}
	}

	return info.String(), nil
}

func (c *Client) getCloudWatchLogsInfo(ctx context.Context) (string, error) {
	groups, err := c.cloudwatchlogs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		Limit: aws.Int32(50), // Limit to 50 most recent log groups
	})
	if err != nil {
		return "", err
	}

	var info strings.Builder
	for _, group := range groups.LogGroups {
		info.WriteString(fmt.Sprintf("- Log Group: %s", aws.ToString(group.LogGroupName)))
		if group.CreationTime != nil {
			info.WriteString(fmt.Sprintf(", Created: %d", *group.CreationTime))
		}
		if group.StoredBytes != nil {
			info.WriteString(fmt.Sprintf(", Size: %d bytes", *group.StoredBytes))
		}
		if group.RetentionInDays != nil {
			info.WriteString(fmt.Sprintf(", Retention: %d days", *group.RetentionInDays))
		}
		info.WriteString("\n")
	}

	return info.String(), nil
}

func (c *Client) getRecentErrorLogs(ctx context.Context, question string) (string, error) {
	// Get log groups first
	groups, err := c.cloudwatchlogs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		Limit: aws.Int32(20), // Check top 20 log groups
	})
	if err != nil {
		return "", err
	}

	// Determine search parameters from question
	var duration time.Duration = 1 * time.Hour // default
	var maxErrors int = 10                     // default
	questionLower := strings.ToLower(question)

	// Check if user wants specific number of errors
	if strings.Contains(questionLower, "last 6 error") || strings.Contains(questionLower, "6 error") {
		maxErrors = 6
		duration = 24 * time.Hour // Search longer period for specific error count
	} else if strings.Contains(questionLower, "last 10 error") || strings.Contains(questionLower, "10 error") {
		maxErrors = 10
		duration = 24 * time.Hour
	} else if strings.Contains(questionLower, "last 5 error") || strings.Contains(questionLower, "5 error") {
		maxErrors = 5
		duration = 12 * time.Hour
	} else if strings.Contains(questionLower, "last 3 error") || strings.Contains(questionLower, "3 error") {
		maxErrors = 3
		duration = 6 * time.Hour
	}

	// Time window detection (overrides above if both specified)
	if strings.Contains(questionLower, "6 hour") || strings.Contains(questionLower, "six hour") {
		duration = 6 * time.Hour
	} else if strings.Contains(questionLower, "12 hour") {
		duration = 12 * time.Hour
	} else if strings.Contains(questionLower, "24 hour") || strings.Contains(questionLower, "day") {
		duration = 24 * time.Hour
	} else if strings.Contains(questionLower, "2 hour") || strings.Contains(questionLower, "two hour") {
		duration = 2 * time.Hour
	} else if strings.Contains(questionLower, "30 min") || strings.Contains(questionLower, "half hour") {
		duration = 30 * time.Minute
	}

	// Look for recent errors in the specified time window
	endTime := time.Now()
	startTime := endTime.Add(-duration)

	var errorLogs strings.Builder
	errorCount := 0

	// Prioritize log groups that are likely to contain application errors
	priorityGroups := []string{
		"/aws/lambda/",
		"API-Gateway-Execution-Logs",
		"/aws/ecs/",
		"/aws/batch/",
	}

	// Check priority groups first
	for _, group := range groups.LogGroups {
		if errorCount >= maxErrors {
			break
		}

		groupName := aws.ToString(group.LogGroupName)

		// Check if this is a priority group
		isPriority := false
		for _, priority := range priorityGroups {
			if strings.Contains(groupName, priority) {
				isPriority = true
				break
			}
		}

		if !isPriority {
			continue
		}

		// Search for error logs in this group
		result, err := c.cloudwatchlogs.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName:  aws.String(groupName),
			StartTime:     aws.Int64(startTime.UnixMilli()),
			EndTime:       aws.Int64(endTime.UnixMilli()),
			FilterPattern: aws.String("ERROR"),
			Limit:         aws.Int32(int32(maxErrors)), // Use dynamic limit
		})

		if err != nil {
			// Skip this group if we can't access it
			continue
		}

		// Add found errors to our output
		for _, event := range result.Events {
			if errorCount >= maxErrors {
				break
			}

			timestamp := time.UnixMilli(*event.Timestamp).Format("2006-01-02 15:04:05")
			message := aws.ToString(event.Message)

			// Truncate very long messages
			if len(message) > 500 {
				message = message[:500] + "..."
			}

			errorLogs.WriteString(fmt.Sprintf("[%s] %s: %s\n", timestamp, groupName, message))
			errorCount++
		}
	}

	if errorCount == 0 {
		timeDesc := "hour"
		if duration >= 24*time.Hour {
			timeDesc = "day"
		} else if duration >= 6*time.Hour {
			timeDesc = fmt.Sprintf("%.0f hours", duration.Hours())
		} else if duration >= 2*time.Hour {
			timeDesc = "2 hours"
		} else if duration < time.Hour {
			timeDesc = "30 minutes"
		}
		return fmt.Sprintf("No recent error logs found in the last %s.\n", timeDesc), nil
	}

	return errorLogs.String(), nil
}

func (c *Client) getServiceLogs(ctx context.Context, question string) (string, error) {
	// Get log groups first
	groups, err := c.cloudwatchlogs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		Limit: aws.Int32(50),
	})
	if err != nil {
		return "", err
	}

	// Determine which service the user is asking about
	questionLower := strings.ToLower(question)
	var targetService string
	var matchingGroups []string

	// Get service keywords from config
	serviceKeywords := viper.GetStringMapStringSlice("aws.service_keywords")

	// Find which service is being requested
	for service, keywords := range serviceKeywords {
		for _, keyword := range keywords {
			if strings.Contains(questionLower, keyword) {
				targetService = service
				break
			}
		}
		if targetService != "" {
			break
		}
	}

	// Find matching log groups for the service
	for _, group := range groups.LogGroups {
		groupName := aws.ToString(group.LogGroupName)
		groupNameLower := strings.ToLower(groupName)

		if targetService != "" {
			// Look for the specific service with various patterns
			// This will match /aws/lambda/abel-dev-chat when searching for "chat"
			patterns := []string{
				targetService,       // exact: "chat"
				"-" + targetService, // suffix: "-chat"
				"_" + targetService, // suffix: "_chat"
				"/" + targetService, // path: "/chat"
				targetService + "-", // prefix: "chat-"
				targetService + "_", // prefix: "chat_"
				targetService + "/", // prefix: "chat/"
			}

			isMatch := false
			for _, pattern := range patterns {
				if strings.Contains(groupNameLower, pattern) {
					isMatch = true
					break
				}
			}

			if isMatch {
				matchingGroups = append(matchingGroups, groupName)
			}
		} else {
			// If no specific service, check for any application log groups
			if strings.Contains(groupNameLower, "/aws/lambda/") ||
				strings.Contains(groupNameLower, "/aws/ecs/") ||
				strings.Contains(groupNameLower, "/aws/batch/") ||
				strings.Contains(groupNameLower, "api-gateway") {
				matchingGroups = append(matchingGroups, groupName)
			}
		}
	}

	if len(matchingGroups) == 0 {
		if targetService != "" {
			return fmt.Sprintf("No log groups found for service: %s\n", targetService), nil
		}
		return "No relevant service log groups found.\n", nil
	}

	// Determine how many log entries to fetch
	logLimit := 10 // default
	if strings.Contains(questionLower, "last 5") || strings.Contains(questionLower, "5 log") {
		logLimit = 5
	} else if strings.Contains(questionLower, "last 20") || strings.Contains(questionLower, "20 log") {
		logLimit = 20
	}

	// Look for recent logs - extend time window to actually find logs
	endTime := time.Now()
	var startTime time.Time

	// Use a longer time window to ensure we find logs
	if strings.Contains(questionLower, "last hour") {
		startTime = endTime.Add(-1 * time.Hour)
	} else if strings.Contains(questionLower, "last day") || strings.Contains(questionLower, "24 hour") {
		startTime = endTime.Add(-24 * time.Hour)
	} else {
		// Default: search last 24 hours to actually find logs
		startTime = endTime.Add(-24 * time.Hour)
	}

	var serviceLogs strings.Builder
	logCount := 0

	// Check each matching group (limit to first 3 to avoid too much output)
	maxGroups := 3
	if len(matchingGroups) < maxGroups {
		maxGroups = len(matchingGroups)
	}

	for i := 0; i < maxGroups && logCount < logLimit; i++ {
		groupName := matchingGroups[i]

		// Get recent log events (all levels, not just errors)
		result, err := c.cloudwatchlogs.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName: aws.String(groupName),
			StartTime:    aws.Int64(startTime.UnixMilli()),
			EndTime:      aws.Int64(endTime.UnixMilli()),
			Limit:        aws.Int32(int32(logLimit - logCount)),
		})

		if err != nil {
			continue // Skip this group if we can't access it
		}

		// Add found logs to our output
		for _, event := range result.Events {
			if logCount >= logLimit {
				break
			}

			timestamp := time.UnixMilli(*event.Timestamp).Format("2006-01-02 15:04:05")
			message := aws.ToString(event.Message)

			// Truncate very long messages
			if len(message) > 800 {
				message = message[:800] + "..."
			}

			serviceLogs.WriteString(fmt.Sprintf("[%s] %s:\n%s\n\n", timestamp, groupName, message))
			logCount++
		}
	}

	if logCount == 0 {
		timeDesc := "24 hours"
		if strings.Contains(questionLower, "last hour") {
			timeDesc = "hour"
		} else if strings.Contains(questionLower, "last day") {
			timeDesc = "day"
		}

		// If no logs found, try to get the most recent log stream regardless of time
		if len(matchingGroups) > 0 {
			groupName := matchingGroups[0]

			// Try to get log streams for this group
			streamsResult, err := c.cloudwatchlogs.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
				LogGroupName: aws.String(groupName),
				OrderBy:      "LastEventTime",
				Descending:   aws.Bool(true),
				Limit:        aws.Int32(1), // Just get the most recent stream
			})

			if err == nil && len(streamsResult.LogStreams) > 0 {
				// Get logs from the most recent stream
				stream := streamsResult.LogStreams[0]
				eventsResult, err := c.cloudwatchlogs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
					LogGroupName:  aws.String(groupName),
					LogStreamName: stream.LogStreamName,
					Limit:         aws.Int32(int32(logLimit)),
					StartFromHead: aws.Bool(false), // Get most recent logs
				})

				if err == nil && len(eventsResult.Events) > 0 {
					var serviceLogs strings.Builder
					serviceLogs.WriteString(fmt.Sprintf("Most recent logs from %s (from stream: %s):\n\n",
						groupName, aws.ToString(stream.LogStreamName)))

					for _, event := range eventsResult.Events {
						timestamp := time.UnixMilli(*event.Timestamp).Format("2006-01-02 15:04:05")
						message := aws.ToString(event.Message)

						// Truncate very long messages
						if len(message) > 800 {
							message = message[:800] + "..."
						}

						serviceLogs.WriteString(fmt.Sprintf("[%s] %s\n\n", timestamp, message))
					}

					return serviceLogs.String(), nil
				}
			}
		}

		return fmt.Sprintf("No recent logs found for %s in the last %s.\n", targetService, timeDesc), nil
	}

	return serviceLogs.String(), nil
}

// GetRecentAlarms gets recent CloudWatch alarm information
func (c *Client) GetRecentAlarms(ctx context.Context) (string, error) {
	// Get alarm history for the last 24 hours
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour)

	historyInput := &cloudwatch.DescribeAlarmHistoryInput{
		StartDate:  &startTime,
		EndDate:    &endTime,
		MaxRecords: aws.Int32(50), // Get last 50 alarm events
	}

	historyResult, err := c.cloudwatch.DescribeAlarmHistory(ctx, historyInput)
	if err != nil {
		return "", fmt.Errorf("failed to get alarm history: %w", err)
	}

	// Also get current alarm states
	alarmInput := &cloudwatch.DescribeAlarmsInput{
		MaxRecords: aws.Int32(100),
	}

	alarmResult, err := c.cloudwatch.DescribeAlarms(ctx, alarmInput)
	if err != nil {
		return "", fmt.Errorf("failed to get alarms: %w", err)
	}

	var result strings.Builder

	// Show recent alarm history
	if len(historyResult.AlarmHistoryItems) > 0 {
		result.WriteString("## Recent Alarm Activity (Last 24 Hours)\n\n")
		for _, item := range historyResult.AlarmHistoryItems {
			timestamp := item.Timestamp.Format("2006-01-02 15:04:05 MST")
			result.WriteString(fmt.Sprintf("**%s** - %s\n", *item.AlarmName, timestamp))
			result.WriteString(fmt.Sprintf("Type: %s\n", item.HistoryItemType))
			if item.HistorySummary != nil {
				result.WriteString(fmt.Sprintf("Summary: %s\n", *item.HistorySummary))
			}
			result.WriteString("\n")
		}
	}

	// Show current alarm states
	if len(alarmResult.MetricAlarms) > 0 {
		result.WriteString("## Current Alarm States\n\n")
		for _, alarm := range alarmResult.MetricAlarms {
			result.WriteString(fmt.Sprintf("**%s**\n", *alarm.AlarmName))
			result.WriteString(fmt.Sprintf("State: %s\n", alarm.StateValue))
			if alarm.StateReason != nil {
				result.WriteString(fmt.Sprintf("Reason: %s\n", *alarm.StateReason))
			}
			if alarm.StateUpdatedTimestamp != nil {
				timestamp := alarm.StateUpdatedTimestamp.Format("2006-01-02 15:04:05 MST")
				result.WriteString(fmt.Sprintf("Last Updated: %s\n", timestamp))
			}
			result.WriteString("\n")
		}
	}

	if result.Len() == 0 {
		return "No alarms found or no recent alarm activity.", nil
	}

	return result.String(), nil
}

// GetAIProfiles returns all AI profiles from the configuration
func (c *Client) GetAIProfiles() map[string]AIProfile {
	profiles := make(map[string]AIProfile)

	// First try to get from ai.profiles (new structure)
	allProfiles := viper.GetStringMap("ai.profiles")
	for name, profileData := range allProfiles {
		if profileMap, ok := profileData.(map[string]interface{}); ok {
			profile := AIProfile{}

			// Extract profile fields
			if provider, ok := profileMap["provider"].(string); ok {
				profile.Provider = provider
			}
			if awsProfile, ok := profileMap["aws_profile"].(string); ok {
				profile.AWSProfile = awsProfile
			}
			if model, ok := profileMap["model"].(string); ok {
				profile.Model = model
			}
			if region, ok := profileMap["region"].(string); ok {
				profile.Region = region
			}
			if apiKeyEnv, ok := profileMap["api_key_env"].(string); ok {
				profile.APIKeyEnv = apiKeyEnv
			}

			profiles[name] = profile
		}
	}

	// If no profiles found, convert from ai.providers (legacy structure)
	if len(profiles) == 0 {
		allProviders := viper.GetStringMap("ai.providers")
		for name, providerData := range allProviders {
			if providerMap, ok := providerData.(map[string]interface{}); ok {
				profile := AIProfile{
					Provider: name, // Use the provider name as the provider type
				}

				// Extract provider fields and map to profile
				if awsProfile, ok := providerMap["aws_profile"].(string); ok {
					profile.AWSProfile = awsProfile
				}
				if model, ok := providerMap["model"].(string); ok {
					profile.Model = model
				}
				if region, ok := providerMap["region"].(string); ok {
					profile.Region = region
				}
				if apiKeyEnv, ok := providerMap["api_key_env"].(string); ok {
					profile.APIKeyEnv = apiKeyEnv
				}

				profiles[name] = profile
			}
		}
	}

	return profiles
}

// executeOperation executes an AWS operation using the default infrastructure profile
func (c *Client) executeOperation(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
	// Get infrastructure profile from config
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	awsProfile := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
	if awsProfile == "" {
		awsProfile = "govcloud-dev"
	}
	awsRegion := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", defaultEnv))
	if awsRegion == "" {
		awsRegion = "us-gov-west-1"
	}

	profile := &AIProfile{
		Provider:   "aws-cli",
		AWSProfile: awsProfile,
		Region:     awsRegion,
	}
	return c.executeAWSOperation(ctx, toolName, input, profile)
}

// ExecuteOperation exposes the default single-operation execution helper.
func (c *Client) ExecuteOperation(ctx context.Context, toolName string, input map[string]interface{}) (string, error) {
	return c.executeOperation(ctx, toolName, input)
}

// executeOperations executes multiple AWS operations using the default infrastructure profile
func (c *Client) executeOperations(ctx context.Context, operations []LLMOperation) (string, error) {
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	awsProfile := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
	if awsProfile == "" {
		awsProfile = "govcloud-dev"
	}
	awsRegion := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", defaultEnv))
	if awsRegion == "" {
		awsRegion = "us-gov-west-1"
	}

	profile := &AIProfile{
		Provider:   "aws-cli",
		AWSProfile: awsProfile,
		Region:     awsRegion,
	}
	return c.executeOperationsWithProfile(ctx, operations, profile)
}

// ExecuteOperations exposes the default batch execution helper.
func (c *Client) ExecuteOperations(ctx context.Context, operations []LLMOperation) (string, error) {
	return c.executeOperations(ctx, operations)
}

// execCLI executes AWS CLI commands using the default infrastructure profile
func (c *Client) execCLI(ctx context.Context, args []string) (string, error) {
	defaultEnv := viper.GetString("infra.default_environment")
	if defaultEnv == "" {
		defaultEnv = "dev"
	}
	awsProfile := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.profile", defaultEnv))
	if awsProfile == "" {
		awsProfile = "govcloud-dev"
	}
	awsRegion := viper.GetString(fmt.Sprintf("infra.aws.environments.%s.region", defaultEnv))
	if awsRegion == "" {
		awsRegion = "us-gov-west-1"
	}

	profile := &AIProfile{
		Provider:   "aws-cli",
		AWSProfile: awsProfile,
		Region:     awsRegion,
	}
	return c.execAWSCLI(ctx, args, profile)
}

// ExecCLI exposes the CLI helper to other packages.
func (c *Client) ExecCLI(ctx context.Context, args []string) (string, error) {
	return c.execCLI(ctx, args)
}
