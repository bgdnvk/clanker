package maker

import (
	"fmt"
	"strings"
)

// knownAWSServices is a list of valid AWS CLI service names
var knownAWSServices = map[string]bool{
	"accessanalyzer": true, "account": true, "acm": true, "acm-pca": true,
	"amplify": true, "apigateway": true, "apigatewayv2": true, "appconfig": true,
	"application-autoscaling": true, "appmesh": true, "apprunner": true, "appstream": true,
	"appsync": true, "athena": true, "autoscaling": true, "backup": true,
	"batch": true, "bedrock": true, "bedrock-runtime": true, "budgets": true,
	"ce": true, "cloud9": true, "cloudformation": true, "cloudfront": true,
	"cloudhsm": true, "cloudsearch": true, "cloudtrail": true, "cloudwatch": true,
	"codebuild": true, "codecommit": true, "codedeploy": true, "codepipeline": true,
	"codestar": true, "cognito-identity": true, "cognito-idp": true, "comprehend": true,
	"config": true, "connect": true, "cur": true, "datapipeline": true,
	"dax": true, "detective": true, "devicefarm": true, "directconnect": true,
	"discovery": true, "dlm": true, "dms": true, "docdb": true,
	"ds": true, "dynamodb": true, "dynamodbstreams": true, "ebs": true,
	"ec2": true, "ecr": true, "ecr-public": true, "ecs": true,
	"efs": true, "eks": true, "elasticache": true, "elasticbeanstalk": true,
	"elastictranscoder": true, "elb": true, "elbv2": true, "emr": true,
	"es": true, "events": true, "firehose": true, "fms": true,
	"forecast": true, "fsx": true, "gamelift": true, "glacier": true,
	"globalaccelerator": true, "glue": true, "greengrass": true, "groundstation": true,
	"guardduty": true, "health": true, "iam": true, "imagebuilder": true,
	"inspector": true, "inspector2": true, "iot": true, "iot-data": true,
	"kafka": true, "kendra": true, "kinesis": true, "kinesisanalytics": true,
	"kms": true, "lakeformation": true, "lambda": true, "lex-models": true,
	"license-manager": true, "lightsail": true, "logs": true, "machinelearning": true,
	"macie": true, "macie2": true, "managedblockchain": true, "mediaconnect": true,
	"mediaconvert": true, "medialive": true, "mediapackage": true, "mediastore": true,
	"memorydb": true, "mgh": true, "mq": true, "mturk": true,
	"neptune": true, "network-firewall": true, "networkmanager": true, "opsworks": true,
	"organizations": true, "outposts": true, "personalize": true, "pi": true,
	"pinpoint": true, "polly": true, "pricing": true, "qldb": true,
	"quicksight": true, "ram": true, "rds": true, "redshift": true,
	"rekognition": true, "resource-groups": true, "resourcegroupstaggingapi": true, "robomaker": true,
	"route53": true, "route53domains": true, "route53resolver": true, "s3": true,
	"s3api": true, "s3control": true, "sagemaker": true, "sagemaker-runtime": true,
	"savingsplans": true, "scheduler": true, "schemas": true, "sdb": true,
	"secretsmanager": true, "securityhub": true, "serverlessrepo": true, "servicecatalog": true,
	"servicediscovery": true, "ses": true, "sesv2": true, "shield": true,
	"signer": true, "sms": true, "snowball": true, "sns": true,
	"sqs": true, "ssm": true, "sso": true, "sso-admin": true,
	"stepfunctions": true, "storagegateway": true, "sts": true, "support": true,
	"swf": true, "synthetics": true, "textract": true, "timestream-query": true,
	"timestream-write": true, "transcribe": true, "transfer": true, "translate": true,
	"waf": true, "waf-regional": true, "wafv2": true, "wellarchitected": true,
	"workdocs": true, "worklink": true, "workmail": true, "workspaces": true,
	"xray": true,
}

// IsKnownAWSService checks if the given string is a valid AWS service name
func IsKnownAWSService(service string) bool {
	service = strings.ToLower(strings.TrimSpace(service))
	return knownAWSServices[service]
}

// ValidateAWSService checks if the first arg is a valid AWS service
func ValidateAWSService(service string) error {
	service = strings.ToLower(strings.TrimSpace(service))
	if service == "aws" {
		return nil // Will be normalized later
	}
	if !knownAWSServices[service] {
		return fmt.Errorf("unknown AWS service: %q (expected ec2, s3, iam, etc.)", service)
	}
	return nil
}

// LooksLikeShellScript detects if an arg contains multi-line shell script patterns
func LooksLikeShellScript(arg string) bool {
	if !strings.Contains(arg, "\n") {
		return false
	}

	lower := strings.ToLower(arg)
	shellPatterns := []string{
		"set -e", "set -u", "set -o", "set -x",
		"#!/bin/bash", "#!/bin/sh", "#!/usr/bin/env",
		"docker build", "docker run", "docker pull", "docker push",
		"git clone", "git pull", "git checkout",
		"curl ", "wget ", "apt-get", "yum install",
		"npm install", "pip install", "go build",
		"export ", "source ", "eval ",
	}

	for _, pattern := range shellPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	return false
}

// SequencingRule defines a dependency between command types
type SequencingRule struct {
	Before      string // Command pattern that must come first
	After       string // Command pattern that must come after
	Description string // Human-readable description
}

// DefaultSequencingRules defines critical ordering requirements
// KEY PRINCIPLE: Service must be healthy before exposure
var DefaultSequencingRules = []SequencingRule{
	// Phase 1: Infrastructure
	{
		Before:      "ecr create-repository",
		After:       "ec2 run-instances",
		Description: "ECR repository must exist before launching EC2 that pulls from it",
	},
	{
		Before:      "iam create-role",
		After:       "iam create-instance-profile",
		Description: "IAM role must exist before creating instance profile",
	},
	{
		Before:      "iam create-instance-profile",
		After:       "ec2 run-instances",
		Description: "Instance profile must exist before launching EC2",
	},
	{
		Before:      "secretsmanager create-secret",
		After:       "ec2 run-instances",
		Description: "Secrets must exist before EC2 that reads them",
	},

	// Phase 2: Compute - EC2 must be running before ALB
	{
		Before:      "ec2 run-instances",
		After:       "elbv2 create-target-group",
		Description: "EC2 instance must be launched before creating target group",
	},
	{
		Before:      "ec2 run-instances",
		After:       "elbv2 create-load-balancer",
		Description: "EC2 instance must be launched before creating ALB",
	},
	{
		Before:      "ec2 wait instance-running",
		After:       "elbv2 create-target-group",
		Description: "EC2 must be running before creating target group",
	},

	// Phase 4: Load Balancer - internal ordering
	{
		Before:      "elbv2 create-target-group",
		After:       "elbv2 create-listener",
		Description: "Target group must exist before creating listener",
	},
	{
		Before:      "elbv2 create-load-balancer",
		After:       "elbv2 create-listener",
		Description: "Load balancer must exist before creating listener",
	},
	{
		Before:      "elbv2 create-listener",
		After:       "elbv2 register-targets",
		Description: "Listener must exist before registering targets",
	},

	// Phase 5: CDN - ALB must be ready before CloudFront
	{
		Before:      "elbv2 create-load-balancer",
		After:       "cloudfront create-distribution",
		Description: "ALB must exist before creating CloudFront distribution",
	},
	{
		Before:      "elbv2 register-targets",
		After:       "cloudfront create-distribution",
		Description: "Targets must be registered before CloudFront",
	},
}

// ValidatePlanSequencing checks that commands follow required ordering
func ValidatePlanSequencing(plan *Plan) []string {
	var warnings []string

	// Build index of command positions
	cmdPositions := make(map[string]int) // "service operation" -> first occurrence index
	for i, cmd := range plan.Commands {
		if len(cmd.Args) >= 2 {
			service := strings.ToLower(cmd.Args[0])
			op := strings.ToLower(cmd.Args[1])
			if service == "aws" && len(cmd.Args) >= 3 {
				service = strings.ToLower(cmd.Args[1])
				op = strings.ToLower(cmd.Args[2])
			}
			key := service + " " + op
			if _, exists := cmdPositions[key]; !exists {
				cmdPositions[key] = i
			}
		}
	}

	// Check each rule
	for _, rule := range DefaultSequencingRules {
		beforeIdx, beforeExists := findCommandPosition(cmdPositions, rule.Before)
		afterIdx, afterExists := findCommandPosition(cmdPositions, rule.After)

		if beforeExists && afterExists && beforeIdx > afterIdx {
			warnings = append(warnings, fmt.Sprintf(
				"sequencing violation: %s (cmd %d) should come before %s (cmd %d) - %s",
				rule.Before, beforeIdx+1, rule.After, afterIdx+1, rule.Description,
			))
		}
	}

	return warnings
}

func findCommandPosition(positions map[string]int, pattern string) (int, bool) {
	// Exact match
	if idx, ok := positions[pattern]; ok {
		return idx, true
	}

	// Prefix match (e.g., "ecr create" matches "ecr create-repository")
	for key, idx := range positions {
		if strings.HasPrefix(key, pattern) {
			return idx, true
		}
	}

	return -1, false
}
