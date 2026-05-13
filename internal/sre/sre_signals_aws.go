package sre

import (
	"context"
	"strings"
	"time"
)

func collectAWSSignals(ctx context.Context) map[string]any {
	out := map[string]any{}

	// --- CloudWatch alarms in ALARM state ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "describe-alarms",
		"--state-value", "ALARM",
		"--query", "MetricAlarms[*].{name:AlarmName,reason:StateReason,metric:MetricName,namespace:Namespace}",
		"--output", "json",
	); err == nil {
		out["cloudwatchAlarms"] = jsonParseList(v)
	}

	// --- CloudWatch billing alarm check (us-east-1 only) ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "cloudwatch", "describe-alarms",
		"--region", "us-east-1",
		"--alarm-name-prefix", "Billing",
		"--state-value", "ALARM",
		"--query", "MetricAlarms[*].AlarmName",
		"--output", "json",
	); err == nil {
		out["billingAlarms"] = jsonParseList(v)
	}

	// --- Lambda functions + error count last 5m ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "lambda", "list-functions",
		"--query", "Functions[*].{name:FunctionName,runtime:Runtime,state:State,lastModified:LastModified}",
		"--output", "json",
	); err == nil {
		out["lambdaFunctions"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/Lambda",
		"--metric-name", "Errors",
		"--dimensions", "Name=FunctionName,Value=ALL",
		"--statistics", "Sum",
		"--period", "300",
		"--start-time", utcMinus(5*time.Minute),
		"--end-time", utcNow(),
		"--query", "Datapoints[*].{time:Timestamp,sum:Sum}",
		"--output", "json",
	); err == nil {
		out["lambdaErrorsLast5m"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/Lambda",
		"--metric-name", "Throttles",
		"--statistics", "Sum",
		"--period", "300",
		"--start-time", utcMinus(5*time.Minute),
		"--end-time", utcNow(),
		"--query", "Datapoints[*].{time:Timestamp,sum:Sum}",
		"--output", "json",
	); err == nil {
		out["lambdaThrottlesLast5m"] = jsonParseList(v)
	}

	// --- ECS clusters + services ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "ecs", "list-clusters",
		"--query", "clusterArns",
		"--output", "json",
	); err == nil {
		out["ecsClusters"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "ecs", "list-services",
		"--query", "serviceArns",
		"--output", "json",
	); err == nil && jsonListLen(v) > 0 {
		out["ecsServices"] = splitLinesLimited(v, 60)
	}

	// --- EKS clusters ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "eks", "list-clusters",
		"--query", "clusters",
		"--output", "json",
	); err == nil {
		out["eksClusters"] = jsonParseList(v)
	}

	// --- RDS instances + high-CPU check ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "rds", "describe-db-instances",
		"--query", "DBInstances[*].{id:DBInstanceIdentifier,engine:Engine,status:DBInstanceStatus,class:DBInstanceClass,multi_az:MultiAZ}",
		"--output", "json",
	); err == nil {
		out["rdsInstances"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/RDS",
		"--metric-name", "CPUUtilization",
		"--statistics", "Average",
		"--period", "300",
		"--start-time", utcMinus(5*time.Minute),
		"--end-time", utcNow(),
		"--query", "Datapoints[*].{time:Timestamp,avg:Average}",
		"--output", "json",
	); err == nil {
		out["rdsCPULast5m"] = jsonParseList(v)
	}

	// --- ElastiCache clusters ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "elasticache", "describe-cache-clusters",
		"--query", "CacheClusters[*].{id:CacheClusterId,engine:Engine,status:CacheClusterStatus,nodeType:CacheNodeType}",
		"--output", "json",
	); err == nil {
		out["elastiCacheClusters"] = jsonParseList(v)
	}

	// --- DynamoDB tables + throttle metrics ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "dynamodb", "list-tables",
		"--query", "TableNames",
		"--output", "json",
	); err == nil {
		out["dynamoTables"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/DynamoDB",
		"--metric-name", "SystemErrors",
		"--statistics", "Sum",
		"--period", "300",
		"--start-time", utcMinus(5*time.Minute),
		"--end-time", utcNow(),
		"--query", "Datapoints[*].{time:Timestamp,sum:Sum}",
		"--output", "json",
	); err == nil {
		out["dynamoErrorsLast5m"] = jsonParseList(v)
	}

	// --- SQS queues + message depth ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "sqs", "list-queues",
		"--query", "QueueUrls",
		"--output", "json",
	); err == nil {
		out["sqsQueues"] = jsonParseList(v)
	}

	// --- API Gateway 4xx/5xx (REST APIs) ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "apigateway", "get-rest-apis",
		"--query", "items[*].{id:id,name:name,created:createdDate}",
		"--output", "json",
	); err == nil {
		out["apiGatewayRestAPIs"] = jsonParseList(v)
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/ApiGateway",
		"--metric-name", "5XXError",
		"--statistics", "Sum",
		"--period", "300",
		"--start-time", utcMinus(5*time.Minute),
		"--end-time", utcNow(),
		"--query", "Datapoints[*].{time:Timestamp,sum:Sum}",
		"--output", "json",
	); err == nil {
		out["apiGateway5xxLast5m"] = jsonParseList(v)
	}

	// --- ALB 5xx rates ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/ApplicationELB",
		"--metric-name", "HTTPCode_Target_5XX_Count",
		"--statistics", "Sum",
		"--period", "300",
		"--start-time", utcMinus(5*time.Minute),
		"--end-time", utcNow(),
		"--query", "Datapoints[*].{time:Timestamp,sum:Sum}",
		"--output", "json",
	); err == nil {
		out["alb5xxLast5m"] = jsonParseList(v)
	}

	// --- EC2 running instances ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "ec2", "describe-instances",
		"--filters", "Name=instance-state-name,Values=running",
		"--query", "Reservations[*].Instances[*].{id:InstanceId,type:InstanceType,az:Placement.AvailabilityZone,state:State.Name}",
		"--output", "json",
	); err == nil {
		out["ec2RunningInstances"] = jsonParseList(v)
	}

	// --- S3 bucket count ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "s3api", "list-buckets",
		"--query", "Buckets[*].Name",
		"--output", "json",
	); err == nil {
		out["s3Buckets"] = jsonParseList(v)
	}

	// --- Step Functions failed executions ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "stepfunctions", "list-state-machines",
		"--query", "stateMachines[*].{name:name,arn:stateMachineArn}",
		"--output", "json",
	); err == nil {
		out["stepFunctions"] = jsonParseList(v)
	}

	// --- CloudTrail recent error events (last 15m) ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=ReadOnly,AttributeValue=false",
		"--start-time", utcMinus(15*time.Minute),
		"--query", "Events[?contains(CloudTrailEvent, 'errorCode')].{time:EventTime,name:EventName,user:Username,error:ErrorCode}",
		"--output", "json",
	); err == nil {
		out["cloudtrailRecentErrors"] = jsonParseList(v)
	}

	// --- IAM: users without MFA ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "iam", "generate-credential-report",
		"--output", "json",
	); err == nil {
		_ = v // trigger report generation
	}
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "iam", "get-credential-report",
		"--query", "Content",
		"--output", "text",
	); err == nil {
		noMFA := []string{}
		for _, line := range splitLinesLimited(v, 300) {
			cols := strings.Split(line, ",")
			// CSV: user,arn,...,password_enabled,...,mfa_active (col 7)
			if len(cols) > 7 && cols[7] == "false" && cols[0] != "user" {
				noMFA = append(noMFA, cols[0])
			}
		}
		if len(noMFA) > 0 {
			out["iamUsersWithoutMFA"] = noMFA
		}
	}

	// --- Route53 health check failures ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "route53", "list-health-checks",
		"--query", "HealthChecks[*].{id:Id,type:HealthCheckConfig.Type}",
		"--output", "json",
	); err == nil {
		out["route53HealthChecks"] = jsonParseList(v)
	}

	// --- CloudWatch log groups (sampling) ---
	if v, err := runCommandOutput(ctx, 4*time.Second,
		"aws", "logs", "describe-log-groups",
		"--query", "logGroups[*].{name:logGroupName,storedBytes:storedBytes,retentionDays:retentionInDays}",
		"--output", "json",
	); err == nil {
		out["cloudwatchLogGroups"] = jsonParseList(v)
	}

	// --- Tagged resources (clanker=sre discovery) ---
	if v, err := runCommandOutput(ctx, 5*time.Second,
		"aws", "resourcegroupstaggingapi", "get-resources",
		"--tag-filters", "Key=clanker,Values=sre",
		"--query", "ResourceTagMappingList[*].{arn:ResourceARN}",
		"--output", "json",
	); err == nil {
		out["taggedSREResources"] = jsonParseList(v)
	}

	return out
}

// collectGCPSignals queries GCP services via gcloud CLI.
// Requires: project set, credentials via gcloud auth or GOOGLE_APPLICATION_CREDENTIALS.
// Scopes: compute, run, functions, sql, storage, pubsub, logging, bigquery, cloudfunctions
