package resourcedb

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// secretPatterns are key patterns that indicate sensitive data
var secretPatterns = []string{
	"ENV_",
	"API_KEY",
	"TOKEN",
	"PASSWORD",
	"SECRET",
	"CREDENTIAL",
	"PRIVATE_KEY",
	"ACCESS_KEY",
	"USER_DATA",
	"AUTH",
	"BEARER",
}

// IsSecretKey checks if a binding key contains sensitive data
func IsSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, pattern := range secretPatterns {
		if strings.Contains(upper, pattern) {
			return true
		}
	}
	return false
}

// isSecretValue checks if a value looks like sensitive data
func isSecretValue(v string) bool {
	// PEM private keys
	if strings.HasPrefix(v, "-----BEGIN") {
		return true
	}
	// Very long values that aren't ARNs or URLs are likely secrets
	if len(v) > 200 && !strings.HasPrefix(v, "arn:") && !strings.HasPrefix(v, "http") {
		return true
	}
	return false
}

// FilterMetadataSecrets removes sensitive keys and values from metadata
func FilterMetadataSecrets(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	filtered := make(map[string]string)
	for k, v := range m {
		if !IsSecretKey(k) && !isSecretValue(v) {
			filtered[k] = v
		}
	}
	return filtered
}

// creationOperations are AWS CLI operations that create resources
var creationPrefixes = []string{
	"create-",
	"run-instances",
	"allocate-",
	"register-",
	"put-rule",
	"put-metric-alarm",
	"import-",
}

// IsCreationOperation checks if an AWS operation creates a resource
func IsCreationOperation(service, op string) bool {
	op = strings.ToLower(op)
	for _, prefix := range creationPrefixes {
		if strings.HasPrefix(op, prefix) {
			return true
		}
	}
	return false
}

// resourceTypeMap maps service+operation to resource type
var resourceTypeMap = map[string]map[string]string{
	"ec2": {
		"run-instances":            "ec2:instance",
		"create-vpc":               "ec2:vpc",
		"create-subnet":            "ec2:subnet",
		"create-security-group":    "ec2:security-group",
		"create-internet-gateway":  "ec2:internet-gateway",
		"create-nat-gateway":       "ec2:nat-gateway",
		"create-route-table":       "ec2:route-table",
		"allocate-address":         "ec2:elastic-ip",
		"create-key-pair":          "ec2:key-pair",
		"create-launch-template":   "ec2:launch-template",
		"create-network-interface": "ec2:network-interface",
	},
	"elbv2": {
		"create-load-balancer": "elbv2:load-balancer",
		"create-target-group":  "elbv2:target-group",
		"create-listener":      "elbv2:listener",
		"create-rule":          "elbv2:rule",
	},
	"rds": {
		"create-db-instance":        "rds:db-instance",
		"create-db-cluster":         "rds:db-cluster",
		"create-db-subnet-group":    "rds:db-subnet-group",
		"create-db-parameter-group": "rds:db-parameter-group",
	},
	"ecr": {
		"create-repository": "ecr:repository",
	},
	"iam": {
		"create-role":             "iam:role",
		"create-instance-profile": "iam:instance-profile",
		"create-policy":           "iam:policy",
		"create-user":             "iam:user",
		"create-group":            "iam:group",
	},
	"secretsmanager": {
		"create-secret": "secretsmanager:secret",
	},
	"cloudfront": {
		"create-distribution": "cloudfront:distribution",
	},
	"s3api": {
		"create-bucket": "s3:bucket",
	},
	"s3": {
		"mb": "s3:bucket",
	},
	"lambda": {
		"create-function": "lambda:function",
	},
	"ecs": {
		"create-cluster":           "ecs:cluster",
		"create-service":           "ecs:service",
		"register-task-definition": "ecs:task-definition",
	},
	"sns": {
		"create-topic": "sns:topic",
	},
	"sqs": {
		"create-queue": "sqs:queue",
	},
	"dynamodb": {
		"create-table": "dynamodb:table",
	},
	"elasticache": {
		"create-cache-cluster":      "elasticache:cluster",
		"create-replication-group":  "elasticache:replication-group",
		"create-cache-subnet-group": "elasticache:subnet-group",
	},
	"route53": {
		"create-hosted-zone": "route53:hosted-zone",
	},
	"acm": {
		"request-certificate": "acm:certificate",
	},
	"cloudwatch": {
		"put-metric-alarm": "cloudwatch:alarm",
	},
	"logs": {
		"create-log-group": "logs:log-group",
	},
	"events": {
		"put-rule": "events:rule",
	},
	"kms": {
		"create-key": "kms:key",
	},
	"ssm": {
		"put-parameter": "ssm:parameter",
	},
}

// InferResourceType returns the resource type for a service+operation
func InferResourceType(service, op string) string {
	service = strings.ToLower(service)
	op = strings.ToLower(op)

	if serviceMap, ok := resourceTypeMap[service]; ok {
		if resourceType, ok := serviceMap[op]; ok {
			return resourceType
		}
	}

	// Fallback: construct from service and operation
	opClean := strings.TrimPrefix(op, "create-")
	opClean = strings.TrimPrefix(opClean, "register-")
	return service + ":" + opClean
}

// JSON path patterns for extracting resource identifiers
var (
	instanceIDRe         = regexp.MustCompile(`"InstanceId"\s*:\s*"(i-[a-f0-9]+)"`)
	vpcIDRe              = regexp.MustCompile(`"VpcId"\s*:\s*"(vpc-[a-f0-9]+)"`)
	subnetIDRe           = regexp.MustCompile(`"SubnetId"\s*:\s*"(subnet-[a-f0-9]+)"`)
	sgIDRe               = regexp.MustCompile(`"GroupId"\s*:\s*"(sg-[a-f0-9]+)"`)
	igwIDRe              = regexp.MustCompile(`"InternetGatewayId"\s*:\s*"(igw-[a-f0-9]+)"`)
	natgwIDRe            = regexp.MustCompile(`"NatGatewayId"\s*:\s*"(nat-[a-f0-9]+)"`)
	rtbIDRe              = regexp.MustCompile(`"RouteTableId"\s*:\s*"(rtb-[a-f0-9]+)"`)
	eipAllocIDRe         = regexp.MustCompile(`"AllocationId"\s*:\s*"(eipalloc-[a-f0-9]+)"`)
	arnRe                = regexp.MustCompile(`"(?:Arn|ARN|[A-Za-z]+Arn)"\s*:\s*"(arn:aws:[^"]+)"`)
	loadBalancerARNRe    = regexp.MustCompile(`"LoadBalancerArn"\s*:\s*"(arn:aws:elasticloadbalancing:[^"]+)"`)
	targetGroupARNRe     = regexp.MustCompile(`"TargetGroupArn"\s*:\s*"(arn:aws:elasticloadbalancing:[^"]+)"`)
	repositoryURIRe      = regexp.MustCompile(`"repositoryUri"\s*:\s*"([^"]+)"`)
	roleARNRe            = regexp.MustCompile(`"Arn"\s*:\s*"(arn:aws:iam::[^"]+:role/[^"]+)"`)
	instanceProfileARNRe = regexp.MustCompile(`"Arn"\s*:\s*"(arn:aws:iam::[^"]+:instance-profile/[^"]+)"`)
	secretARNRe          = regexp.MustCompile(`"ARN"\s*:\s*"(arn:aws:secretsmanager:[^"]+)"`)
	functionARNRe        = regexp.MustCompile(`"FunctionArn"\s*:\s*"(arn:aws:lambda:[^"]+)"`)
	distributionIDRe     = regexp.MustCompile(`"Id"\s*:\s*"([A-Z0-9]+)"`)
)

// ExtractResource extracts resource information from command args and output
func ExtractResource(args []string, output string, cmdIndex int, runID, region, profile, accountID, parentRunID string) *Resource {
	if len(args) < 2 {
		return nil
	}

	service := strings.ToLower(args[0])
	op := strings.ToLower(args[1])

	// Handle "aws <service> <op>" format
	if service == "aws" && len(args) >= 3 {
		service = strings.ToLower(args[1])
		op = strings.ToLower(args[2])
	}

	if !IsCreationOperation(service, op) {
		return nil
	}

	r := &Resource{
		RunID:        runID,
		CommandIndex: cmdIndex,
		Provider:     "aws",
		Service:      service,
		Operation:    op,
		ResourceType: InferResourceType(service, op),
		Region:       region,
		Profile:      profile,
		AccountID:    accountID,
		ParentRunID:  parentRunID,
		CreatedAt:    time.Now(),
		Metadata:     make(map[string]string),
		Tags:         make(map[string]string),
	}

	// Extract resource IDs and ARNs from output
	extractResourceIdentifiers(output, r)

	// Only record if we actually extracted a resource identifier from the output
	// This confirms the resource was actually created, not just that the command ran
	if r.ResourceID == "" && r.ResourceARN == "" {
		return nil
	}

	// Extract name from args
	r.ResourceName = extractResourceName(args)

	// Extract relevant metadata from args
	extractMetadataFromArgs(args, r)

	// Extract tags from args
	extractTagsFromArgs(args, r)

	return r
}

func extractResourceIdentifiers(output string, r *Resource) {
	switch r.Service {
	case "ec2":
		switch {
		case strings.HasPrefix(r.Operation, "run-instances"):
			if m := instanceIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "create-vpc":
			if m := vpcIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "create-subnet":
			if m := subnetIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "create-security-group":
			if m := sgIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "create-internet-gateway":
			if m := igwIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "create-nat-gateway":
			if m := natgwIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "create-route-table":
			if m := rtbIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		case r.Operation == "allocate-address":
			if m := eipAllocIDRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceID = m[1]
			}
		}

	case "elbv2":
		if strings.Contains(r.Operation, "load-balancer") {
			if m := loadBalancerARNRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceARN = m[1]
				r.ResourceID = extractNameFromARN(m[1])
			}
		} else if strings.Contains(r.Operation, "target-group") {
			if m := targetGroupARNRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceARN = m[1]
				r.ResourceID = extractNameFromARN(m[1])
			}
		}

	case "ecr":
		if m := repositoryURIRe.FindStringSubmatch(output); len(m) > 1 {
			r.ResourceID = m[1]
			r.Metadata["repository_uri"] = m[1]
		}
		if m := arnRe.FindStringSubmatch(output); len(m) > 1 {
			r.ResourceARN = m[1]
		}

	case "iam":
		if strings.Contains(r.Operation, "role") {
			if m := roleARNRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceARN = m[1]
				r.ResourceID = extractNameFromARN(m[1])
			}
		} else if strings.Contains(r.Operation, "instance-profile") {
			if m := instanceProfileARNRe.FindStringSubmatch(output); len(m) > 1 {
				r.ResourceARN = m[1]
				r.ResourceID = extractNameFromARN(m[1])
			}
		}

	case "secretsmanager":
		if m := secretARNRe.FindStringSubmatch(output); len(m) > 1 {
			r.ResourceARN = m[1]
			r.ResourceID = extractNameFromARN(m[1])
		}

	case "lambda":
		if m := functionARNRe.FindStringSubmatch(output); len(m) > 1 {
			r.ResourceARN = m[1]
			r.ResourceID = extractNameFromARN(m[1])
		}

	case "cloudfront":
		if m := distributionIDRe.FindStringSubmatch(output); len(m) > 1 {
			r.ResourceID = m[1]
		}

	default:
		// Generic ARN extraction
		if m := arnRe.FindStringSubmatch(output); len(m) > 1 {
			r.ResourceARN = m[1]
			if r.ResourceID == "" {
				r.ResourceID = extractNameFromARN(m[1])
			}
		}
	}
}

func extractNameFromARN(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	parts = strings.Split(arn, ":")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func extractResourceName(args []string) string {
	for i, arg := range args {
		switch arg {
		case "--name", "--repository-name", "--role-name", "--function-name",
			"--db-instance-identifier", "--cluster-identifier", "--target-group-name",
			"--load-balancer-name", "--topic-name", "--queue-name", "--table-name",
			"--secret-name", "--key-name", "--instance-profile-name", "--group-name",
			"--policy-name", "--user-name", "--log-group-name", "--rule-name",
			"--alarm-name", "--bucket":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				return args[i+1]
			}
		}
	}
	return ""
}

func extractMetadataFromArgs(args []string, r *Resource) {
	for i, arg := range args {
		if i+1 >= len(args) {
			continue
		}
		val := args[i+1]
		if strings.HasPrefix(val, "--") {
			continue
		}

		switch arg {
		case "--instance-type":
			r.Metadata["instance_type"] = val
		case "--vpc-id":
			r.Metadata["vpc_id"] = val
		case "--subnet-id", "--subnet-ids":
			r.Metadata["subnet_id"] = val
		case "--security-group-ids":
			r.Metadata["security_group_ids"] = val
		case "--image-id":
			r.Metadata["image_id"] = val
		case "--cidr-block":
			r.Metadata["cidr_block"] = val
		case "--availability-zone":
			r.Metadata["availability_zone"] = val
		case "--db-instance-class":
			r.Metadata["db_instance_class"] = val
		case "--engine":
			r.Metadata["engine"] = val
		case "--engine-version":
			r.Metadata["engine_version"] = val
		case "--allocated-storage":
			r.Metadata["allocated_storage"] = val
		case "--port":
			r.Metadata["port"] = val
		case "--protocol":
			r.Metadata["protocol"] = val
		case "--target-type":
			r.Metadata["target_type"] = val
		case "--health-check-path":
			r.Metadata["health_check_path"] = val
		case "--runtime":
			r.Metadata["runtime"] = val
		case "--handler":
			r.Metadata["handler"] = val
		case "--memory-size":
			r.Metadata["memory_size"] = val
		case "--timeout":
			r.Metadata["timeout"] = val
		}
	}

	// Filter any secrets that may have slipped in
	r.Metadata = FilterMetadataSecrets(r.Metadata)
}

func extractTagsFromArgs(args []string, r *Resource) {
	for i, arg := range args {
		if arg == "--tags" && i+1 < len(args) {
			tagsArg := args[i+1]
			// Try to parse as JSON array
			var tags []map[string]string
			if err := json.Unmarshal([]byte(tagsArg), &tags); err == nil {
				for _, tag := range tags {
					if k, ok := tag["Key"]; ok {
						if v, ok := tag["Value"]; ok {
							if !IsSecretKey(k) && !isSecretValue(v) {
								r.Tags[k] = v
							}
						}
					}
				}
			} else {
				// Try Key=Value,Key=Value format
				pairs := strings.Split(tagsArg, ",")
				for _, pair := range pairs {
					kv := strings.SplitN(pair, "=", 2)
					if len(kv) == 2 {
						k := strings.TrimSpace(kv[0])
						v := strings.TrimSpace(kv[1])
						if !IsSecretKey(k) && !isSecretValue(v) {
							r.Tags[k] = v
						}
					}
				}
			}
			break
		}
	}
}
