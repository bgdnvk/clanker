package maker

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var awsErrorCodeRe = regexp.MustCompile(`(?i)an error occurred \(([^)]+)\)`)
var planPlaceholderTokenRe = regexp.MustCompile(`<([A-Z0-9_]+)>`)

type AWSFailureCategory string

const (
	FailureUnknown       AWSFailureCategory = "unknown"
	FailureNotFound      AWSFailureCategory = "not_found"
	FailureAlreadyExists AWSFailureCategory = "already_exists"
	FailureConflict      AWSFailureCategory = "conflict"
	FailureAccessDenied  AWSFailureCategory = "access_denied"
	FailureThrottled     AWSFailureCategory = "throttled"
	FailureValidation    AWSFailureCategory = "validation"
)

type AWSFailure struct {
	Service  string
	Op       string
	Code     string
	Category AWSFailureCategory
	Message  string
}

type ExecOptions struct {
	Profile   string
	Region    string
	Writer    io.Writer
	Destroyer bool

	AIProvider string
	AIAPIKey   string
	AIProfile  string
	Debug      bool
}

func ExecutePlan(ctx context.Context, plan *Plan, opts ExecOptions) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if opts.Profile == "" {
		return fmt.Errorf("missing aws profile")
	}
	if opts.Region == "" {
		return fmt.Errorf("missing aws region")
	}
	if opts.Writer == nil {
		return fmt.Errorf("missing output writer")
	}

	accountID, err := resolveAWSAccountID(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: failed to resolve AWS account id via sts: %v\n", err)
	}

	remediationAttempted := make(map[int]bool)
	bindings := make(map[string]string)

	for idx, cmdSpec := range plan.Commands {
		if err := validateCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+6)
		args = append(args, cmdSpec.Args...)
		args = substituteAccountID(args, accountID)
		args = applyPlanBindings(args, bindings)

		zipBytes, updatedArgs, err := maybeInjectLambdaZipBytes(args, opts.Writer)
		if err != nil {
			return fmt.Errorf("command %d prepare failed: %w", idx+1, err)
		}
		args = updatedArgs

		awsArgs := make([]string, 0, len(args)+6)
		awsArgs = append(awsArgs, args...)
		awsArgs = append(awsArgs, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatAWSArgsForLog(awsArgs))

		out, runErr := runAWSCommandStreaming(ctx, awsArgs, zipBytes, opts.Writer)
		if runErr != nil {
			if handled, handleErr := handleAWSFailure(ctx, plan, opts, idx, args, awsArgs, zipBytes, out, runErr, remediationAttempted, bindings); handled {
				if handleErr != nil {
					return handleErr
				}
				continue
			}
			return fmt.Errorf("aws command %d failed: %w", idx+1, runErr)
		}

		// Learn placeholder bindings from successful command outputs.
		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
		learnPlanBindings(args, out, bindings)

		// CloudFormation is async. If we just created/updated a stack, wait for it to complete.
		if len(args) >= 2 && args[0] == "cloudformation" && (args[1] == "create-stack" || args[1] == "update-stack") {
			stackName := strings.TrimSpace(flagValue(args, "--stack-name"))
			if stackName != "" {
				status, details, waitErr := waitForCloudFormationStackTerminal(ctx, opts, stackName, opts.Writer)
				if waitErr != nil {
					return fmt.Errorf("cloudformation wait failed for %s: %w", stackName, waitErr)
				}
				if !isCloudFormationStackSuccess(status) {
					combined := strings.TrimSpace(out)
					if combined != "" {
						combined += "\n"
					}
					combined += fmt.Sprintf("cloudformation stack %s ended in %s%s", stackName, status, details)

					synthErr := fmt.Errorf("cloudformation stack %s failed (status=%s)", stackName, status)
					if handled, handleErr := handleAWSFailure(ctx, plan, opts, idx, args, awsArgs, zipBytes, combined, synthErr, remediationAttempted, bindings); handled {
						if handleErr != nil {
							return handleErr
						}
						continue
					}
					return fmt.Errorf("aws command %d failed: %w", idx+1, synthErr)
				}
			}
		}
	}

	return nil
}

func learnPlanBindingsFromProduces(produces map[string]string, output string, bindings map[string]string) {
	if len(produces) == 0 {
		return
	}
	if strings.TrimSpace(output) == "" {
		return
	}
	var obj any
	if err := json.Unmarshal([]byte(output), &obj); err != nil {
		return
	}
	for key, path := range produces {
		key = strings.TrimSpace(key)
		path = strings.TrimSpace(path)
		if key == "" || path == "" {
			continue
		}
		if v, ok := jsonPathString(obj, path); ok {
			bindings[key] = v
		}
	}
}

func jsonPathString(obj any, path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	if path == "$" {
		// Not useful as a string.
		return "", false
	}
	if strings.HasPrefix(path, "$") {
		path = strings.TrimPrefix(path, "$")
	}
	path = strings.TrimPrefix(path, ".")

	cur := obj
	for len(path) > 0 {
		// Consume one segment: name and optional [idx] parts.
		seg := path
		if i := strings.Index(seg, "."); i >= 0 {
			seg = seg[:i]
		}

		name := seg
		rest := ""
		if i := strings.Index(name, "["); i >= 0 {
			rest = name[i:]
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if name != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return "", false
			}
			cur, ok = m[name]
			if !ok {
				return "", false
			}
		}

		for strings.HasPrefix(rest, "[") {
			end := strings.Index(rest, "]")
			if end < 0 {
				return "", false
			}
			idxStr := strings.TrimSpace(rest[1:end])
			rest = rest[end+1:]
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				return "", false
			}
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return "", false
			}
			cur = arr[idx]
		}

		if len(path) == len(seg) {
			path = ""
		} else {
			path = strings.TrimPrefix(path[len(seg):], ".")
		}
	}

	switch v := cur.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return "", false
		}
		return v, true
	case float64:
		// Only accept integral floats.
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), true
		}
		return "", false
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

func applyPlanBindings(args []string, bindings map[string]string) []string {
	if len(args) == 0 || len(bindings) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.Contains(a, "<") || !strings.Contains(a, ">") {
			out = append(out, a)
			continue
		}
		rewritten := planPlaceholderTokenRe.ReplaceAllStringFunc(a, func(m string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(m, "<"), ">")
			if v, ok := bindings[key]; ok && strings.TrimSpace(v) != "" {
				return v
			}
			return m
		})
		out = append(out, rewritten)
	}
	return out
}

func learnPlanBindings(args []string, output string, bindings map[string]string) {
	if len(args) < 2 {
		return
	}
	if strings.TrimSpace(output) == "" {
		return
	}

	service := strings.TrimSpace(args[0])
	op := strings.TrimSpace(args[1])

	// Most create operations we care about return JSON.
	var obj map[string]any
	if err := json.Unmarshal([]byte(output), &obj); err != nil {
		return
	}

	switch service {
	case "ec2":
		switch op {
		case "describe-availability-zones":
			// {"AvailabilityZones":[{"ZoneName":"us-east-1a"}, ...]}
			az1 := deepString(obj, "AvailabilityZones", "0", "ZoneName")
			az2 := deepString(obj, "AvailabilityZones", "1", "ZoneName")
			if az1 != "" {
				bindings["AZ_1"] = az1
			}
			if az2 != "" {
				bindings["AZ_2"] = az2
			}
		case "create-internet-gateway":
			// {"InternetGateway":{"InternetGatewayId":"igw-...","Tags":[{"Key":"Name","Value":"main-igw"}]}}
			id := deepString(obj, "InternetGateway", "InternetGatewayId")
			if id == "" {
				return
			}
			name := deepTagValue(obj, "InternetGateway", "Tags", "Name")
			if name == "main-igw" {
				bindings["IGW_ID"] = id
				bindings["IGW"] = id
				return
			}
			// Best-effort default.
			if _, ok := bindings["IGW_ID"]; !ok {
				bindings["IGW_ID"] = id
				bindings["IGW"] = id
			}
		case "create-subnet":
			id := deepString(obj, "Subnet", "SubnetId")
			if id == "" {
				return
			}
			name := deepTagValue(obj, "Subnet", "Tags", "Name")
			switch name {
			case "public-subnet-1":
				bindings["SUBNET_PUB_1_ID"] = id
				bindings["SUB_PUB_1_ID"] = id
				bindings["SUB_PUB_1"] = id
			case "public-subnet-2":
				bindings["SUBNET_PUB_2_ID"] = id
				bindings["SUB_PUB_2_ID"] = id
				bindings["SUB_PUB_2"] = id
			case "private-subnet-1":
				bindings["SUBNET_PRIV_1_ID"] = id
				bindings["SUB_PRIV_ID"] = id
				bindings["SUB_PRIV"] = id
			default:
				// Fallback: fill first missing slot in a stable order.
				for _, k := range []string{"SUBNET_PUB_1_ID", "SUBNET_PUB_2_ID", "SUBNET_PRIV_1_ID", "SUB_PUB_1_ID", "SUB_PUB_2_ID", "SUB_PRIV_ID", "SUB_PUB_1", "SUB_PUB_2", "SUB_PRIV"} {
					if strings.TrimSpace(bindings[k]) == "" {
						bindings[k] = id
						if k == "SUBNET_PUB_1_ID" {
							bindings["SUB_PUB_1_ID"] = id
							bindings["SUB_PUB_1"] = id
						}
						if k == "SUBNET_PUB_2_ID" {
							bindings["SUB_PUB_2_ID"] = id
							bindings["SUB_PUB_2"] = id
						}
						if k == "SUBNET_PRIV_1_ID" {
							bindings["SUB_PRIV_ID"] = id
							bindings["SUB_PRIV"] = id
						}
						if k == "SUB_PUB_1_ID" {
							bindings["SUBNET_PUB_1_ID"] = id
							bindings["SUB_PUB_1"] = id
						}
						if k == "SUB_PUB_2_ID" {
							bindings["SUBNET_PUB_2_ID"] = id
							bindings["SUB_PUB_2"] = id
						}
						if k == "SUB_PRIV_ID" {
							bindings["SUBNET_PRIV_1_ID"] = id
							bindings["SUB_PRIV"] = id
						}
						break
					}
				}
			}
		case "allocate-address":
			alloc := deepString(obj, "AllocationId")
			if alloc != "" {
				bindings["EIP_ALLOC_ID"] = alloc
				bindings["EIP_ID"] = alloc
			}
		case "create-nat-gateway":
			ngw := deepString(obj, "NatGateway", "NatGatewayId")
			if ngw != "" {
				bindings["NAT_GW_ID"] = ngw
				bindings["NAT_ID"] = ngw
			}
		case "create-route-table":
			id := deepString(obj, "RouteTable", "RouteTableId")
			if id == "" {
				return
			}
			name := deepTagValue(obj, "RouteTable", "Tags", "Name")
			switch name {
			case "public-rt":
				bindings["RT_PUBLIC_ID"] = id
				bindings["RT_PUB_ID"] = id
				bindings["RT_PUB"] = id
			case "private-rt":
				bindings["RT_PRIVATE_ID"] = id
				bindings["RT_PRIV_ID"] = id
				bindings["RT_PRIV"] = id
			default:
				for _, k := range []string{"RT_PUBLIC_ID", "RT_PRIVATE_ID", "RT_PUB_ID", "RT_PRIV_ID", "RT_PUB", "RT_PRIV"} {
					if strings.TrimSpace(bindings[k]) == "" {
						bindings[k] = id
						if k == "RT_PUBLIC_ID" {
							bindings["RT_PUB_ID"] = id
							bindings["RT_PUB"] = id
						}
						if k == "RT_PRIVATE_ID" {
							bindings["RT_PRIV_ID"] = id
							bindings["RT_PRIV"] = id
						}
						if k == "RT_PUB_ID" {
							bindings["RT_PUBLIC_ID"] = id
							bindings["RT_PUB"] = id
						}
						if k == "RT_PRIV_ID" {
							bindings["RT_PRIVATE_ID"] = id
							bindings["RT_PRIV"] = id
						}
						break
					}
				}
			}
		case "create-security-group":
			gid := deepString(obj, "GroupId")
			if gid == "" {
				return
			}
			groupName := strings.TrimSpace(flagValue(args, "--group-name"))
			switch groupName {
			case "alb-sg":
				bindings["SG_ALB_ID"] = gid
				bindings["SG_ALB"] = gid
			case "web-sg":
				bindings["SG_WEB_ID"] = gid
				bindings["SG_WEB"] = gid
			case "web-server-sg":
				bindings["SG_WEB_ID"] = gid
				bindings["SG_WEB"] = gid
			default:
				for _, k := range []string{"SG_ALB_ID", "SG_WEB_ID", "SG_ALB", "SG_WEB"} {
					if strings.TrimSpace(bindings[k]) == "" {
						bindings[k] = gid
						break
					}
				}
			}

			// Common placeholder aliases used by planner output.
			if strings.TrimSpace(bindings["SG_ALB_ID"]) != "" {
				bindings["ALB_SG_ID"] = bindings["SG_ALB_ID"]
			}
			if strings.TrimSpace(bindings["SG_WEB_ID"]) != "" {
				bindings["WEB_SG_ID"] = bindings["SG_WEB_ID"]
			}
		case "run-instances":
			// {"Instances":[{"InstanceId":"i-..."}]}
			inst := deepString(obj, "Instances", "0", "InstanceId")
			if inst != "" {
				bindings["INSTANCE_ID"] = inst
			}
		}
	case "elbv2":
		switch op {
		case "create-load-balancer":
			arn := deepString(obj, "LoadBalancers", "0", "LoadBalancerArn")
			if arn != "" {
				bindings["ALB_ARN"] = arn
			}
		case "create-target-group":
			arn := deepString(obj, "TargetGroups", "0", "TargetGroupArn")
			if arn != "" {
				bindings["TG_ARN"] = arn
			}
		case "ssm":
			if op == "get-parameters" {
				// {"Parameters":[{"Name":"...","Value":"ami-..."}]}
				val := deepString(obj, "Parameters", "0", "Value")
				if val != "" && strings.HasPrefix(val, "ami-") {
					bindings["AMI_ID"] = val
				}
			}
		}
	}
}

func deepString(obj any, path ...string) string {
	cur := obj
	for _, p := range path {
		switch typed := cur.(type) {
		case map[string]any:
			cur = typed[p]
		case []any:
			idx, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || idx < 0 || idx >= len(typed) {
				return ""
			}
			cur = typed[idx]
		default:
			return ""
		}
	}
	if s, ok := cur.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func deepTagValue(obj map[string]any, rootKey string, tagsKey string, tagName string) string {
	root, ok := obj[rootKey].(map[string]any)
	if !ok {
		return ""
	}
	tags, ok := root[tagsKey].([]any)
	if !ok {
		return ""
	}
	for _, t := range tags {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		k, _ := m["Key"].(string)
		v, _ := m["Value"].(string)
		if strings.TrimSpace(k) == tagName {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseAWSErrorCode(output string) string {
	m := awsErrorCodeRe.FindStringSubmatch(output)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func classifyAWSFailure(args []string, output string) AWSFailure {
	f := AWSFailure{Category: FailureUnknown}
	if len(args) >= 1 {
		f.Service = strings.TrimSpace(args[0])
	}
	if len(args) >= 2 {
		f.Op = strings.TrimSpace(args[1])
	}
	msg := strings.TrimSpace(output)
	if len(msg) > 900 {
		msg = msg[:900]
	}
	f.Message = msg

	code := parseAWSErrorCode(output)
	f.Code = code

	lower := strings.ToLower(output)

	isNotFoundish := strings.Contains(lower, "nosuchentity") ||
		strings.Contains(lower, "resourcenotfound") ||
		strings.Contains(lower, "notfoundexception") ||
		strings.Contains(lower, "nosuchbucket") ||
		strings.Contains(lower, "nosuchkey") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "does not exist")
	if code == "NoSuchEntity" || code == "ResourceNotFoundException" || code == "NoSuchBucket" || code == "NoSuchKey" {
		isNotFoundish = true
	}
	if code == "NotFoundException" {
		isNotFoundish = true
	}

	isAlreadyExistsish := strings.Contains(lower, "entityalreadyexists") ||
		strings.Contains(lower, "resourceconflictexception") ||
		strings.Contains(lower, "resourceexistsexception") ||
		strings.Contains(lower, "repositoryalreadyexistsexception") ||
		strings.Contains(lower, "alreadyexistsexception") ||
		strings.Contains(lower, "parameteralreadyexists") ||
		strings.Contains(lower, "queuealreadyexists") ||
		strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "alreadyownedbyyou") ||
		strings.Contains(lower, "invalidgroup.duplicate") ||
		false
	if code == "EntityAlreadyExists" ||
		code == "ResourceConflictException" ||
		code == "BucketAlreadyOwnedByYou" ||
		code == "ResourceExistsException" ||
		code == "RepositoryAlreadyExistsException" ||
		code == "AlreadyExistsException" ||
		code == "ParameterAlreadyExists" ||
		code == "QueueAlreadyExists" ||
		code == "InvalidGroup.Duplicate" {
		isAlreadyExistsish = true
	}

	isConflictish := strings.Contains(lower, "conflictexception") ||
		strings.Contains(lower, "deleteconflict") ||
		strings.Contains(lower, "dependencyviolation") ||
		strings.Contains(lower, "resourceinuse") ||
		strings.Contains(lower, "dependent object")
	if code == "ConflictException" || code == "DeleteConflict" || code == "DependencyViolation" || code == "OperationAbortedException" || code == "ResourceInUseException" {
		isConflictish = true
	}

	isAccessDeniedish := strings.Contains(lower, "accessdenied") ||
		strings.Contains(lower, "unauthorizedoperation") ||
		strings.Contains(lower, "not authorized")
	if code == "AccessDenied" || code == "AccessDeniedException" || code == "UnauthorizedOperation" {
		isAccessDeniedish = true
	}

	isThrottledish := strings.Contains(lower, "throttl") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "requestlimitexceeded") ||
		strings.Contains(lower, "priorrequestnotcomplete")
	if code == "Throttling" || code == "TooManyRequestsException" || code == "RequestLimitExceeded" || code == "PriorRequestNotComplete" {
		isThrottledish = true
	}

	isValidationish := strings.Contains(lower, "validation") ||
		strings.Contains(lower, "invalidparameter") ||
		strings.Contains(lower, "malformed")
	if code == "ValidationException" ||
		code == "InvalidParameterValueException" ||
		code == "InvalidParameterValue" ||
		code == "BadRequestException" {
		isValidationish = true
	}

	switch {
	case isNotFoundish:
		f.Category = FailureNotFound
	case isAlreadyExistsish:
		f.Category = FailureAlreadyExists
	case isConflictish:
		f.Category = FailureConflict
	case isAccessDeniedish:
		f.Category = FailureAccessDenied
	case isThrottledish:
		f.Category = FailureThrottled
	case isValidationish:
		f.Category = FailureValidation
	default:
		f.Category = FailureUnknown
	}

	return f
}

func formatAWSArgsForLog(awsArgs []string) string {
	// Avoid spewing huge JSON blobs or embedded policy documents.
	const maxArgLen = 160
	const maxTotalLen = 700

	parts := make([]string, 0, len(awsArgs)+1)
	parts = append(parts, "aws")
	for _, a := range awsArgs {
		if len(a) > maxArgLen {
			a = a[:maxArgLen] + "…"
		}
		parts = append(parts, a)
	}
	s := strings.Join(parts, " ")
	if len(s) > maxTotalLen {
		s = s[:maxTotalLen] + "…"
	}
	return s
}

func handleAWSFailure(
	ctx context.Context,
	plan *Plan,
	opts ExecOptions,
	idx int,
	args []string,
	awsArgs []string,
	stdinBytes []byte,
	out string,
	runErr error,
	remediationAttempted map[int]bool,
	bindings map[string]string,
) (handled bool, err error) {
	failure := classifyAWSFailure(args, out)
	if failure.Code != "" {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] error classified service=%s op=%s code=%s category=%s\n", failure.Service, failure.Op, failure.Code, failure.Category)
	} else {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] error classified service=%s op=%s category=%s\n", failure.Service, failure.Op, failure.Category)
	}

	if shouldIgnoreFailure(args, failure, out) {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] note: ignoring non-fatal error for command %d\n", idx+1)
		return true, nil
	}

	if handled, handleErr := maybeRewriteAndRetry(ctx, opts, args, awsArgs, stdinBytes, failure, out, bindings); handled {
		return true, handleErr
	}

	if remediationAttempted[idx] {
		return false, nil
	}

	if remediated, remErr := maybeAutoRemediateAndRetry(ctx, plan, opts, idx, args, awsArgs, stdinBytes, out, failure); remErr == nil && remediated {
		remediationAttempted[idx] = true
		return true, nil
	}

	return false, runErr
}

var accountIDToken = regexp.MustCompile(`(?i)(<\s*(your_)?account[_-]?id\s*>|replace_with_account_id)`)

func substituteAccountID(args []string, accountID string) []string {
	if accountID == "" {
		return args
	}

	out := make([]string, 0, len(args))
	for _, a := range args {
		a = accountIDToken.ReplaceAllString(a, accountID)
		out = append(out, a)
	}
	return out
}

func resolveAWSAccountID(ctx context.Context, opts ExecOptions) (string, error) {
	cmd := exec.CommandContext(
		ctx,
		"aws",
		"sts",
		"get-caller-identity",
		"--query",
		"Account",
		"--output",
		"text",
		"--profile",
		opts.Profile,
		"--region",
		opts.Region,
		"--no-cli-pager",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sts get-caller-identity failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	accountID := strings.TrimSpace(string(out))
	if len(accountID) != 12 {
		return "", fmt.Errorf("unexpected account id output: %q", accountID)
	}
	for _, ch := range accountID {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("unexpected account id output: %q", accountID)
		}
	}

	return accountID, nil
}

func maybeInjectLambdaZipBytes(args []string, w io.Writer) ([]byte, []string, error) {
	if len(args) < 2 {
		return nil, args, nil
	}
	if args[0] != "lambda" {
		return nil, args, nil
	}
	switch args[1] {
	case "create-function", "update-function-code":
		// supported
	default:
		return nil, args, nil
	}

	zipIdx := -1
	zipVal := ""
	runtime := ""
	handler := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--zip-file":
			if i+1 < len(args) {
				zipIdx = i + 1
				zipVal = args[i+1]
			}
		case "--runtime":
			if i+1 < len(args) {
				runtime = args[i+1]
			}
		case "--handler":
			if i+1 < len(args) {
				handler = args[i+1]
			}
		default:
			if strings.HasPrefix(args[i], "--zip-file=") {
				zipIdx = i
				zipVal = strings.TrimPrefix(args[i], "--zip-file=")
			}
		}
	}

	if zipIdx == -1 || zipVal == "" {
		return nil, args, nil
	}

	if !strings.HasPrefix(zipVal, "fileb://") {
		return nil, args, nil
	}

	path := strings.TrimPrefix(zipVal, "fileb://")
	if path == "-" {
		zipBytes, err := buildLambdaZip(runtime, handler)
		if err != nil {
			return nil, args, err
		}
		updated, err := rewriteLambdaZipAsCliInputJSON(args, zipBytes)
		if err != nil {
			return nil, args, err
		}
		_, _ = fmt.Fprintf(w, "[maker] note: generated inline lambda zip (runtime=%s)\n", runtime)
		return nil, updated, nil
	}

	if filepath.IsAbs(path) {
		if _, err := os.Stat(path); err == nil {
			return nil, args, nil
		}
	} else {
		if _, err := os.Stat(path); err == nil {
			return nil, args, nil
		}
	}

	zipBytes, err := buildLambdaZip(runtime, handler)
	if err != nil {
		return nil, args, err
	}

	updated, err := rewriteLambdaZipAsCliInputJSON(args, zipBytes)
	if err != nil {
		return nil, args, err
	}
	_, _ = fmt.Fprintf(w, "[maker] note: generated inline lambda zip (runtime=%s)\n", runtime)
	return nil, updated, nil
}

func rewriteLambdaZipAsCliInputJSON(args []string, zipBytes []byte) ([]string, error) {
	if len(args) < 2 {
		return args, nil
	}
	if args[0] != "lambda" {
		return args, nil
	}

	encodedZip := base64.StdEncoding.EncodeToString(zipBytes)

	switch args[1] {
	case "create-function":
		fnName := flagValue(args, "--function-name")
		runtime := flagValue(args, "--runtime")
		role := flagValue(args, "--role")
		handler := flagValue(args, "--handler")
		if fnName == "" || runtime == "" || role == "" || handler == "" {
			return nil, fmt.Errorf("cannot rewrite create-function without --function-name/--runtime/--role/--handler")
		}

		payload := map[string]any{
			"FunctionName": fnName,
			"Runtime":      runtime,
			"Role":         role,
			"Handler":      handler,
			"Code": map[string]any{
				"ZipFile": encodedZip,
			},
		}

		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		// Build a clean command; avoids AWS CLI complaining about conflicting args.
		return []string{"lambda", "create-function", "--cli-input-json", string(b)}, nil

	case "update-function-code":
		fnName := flagValue(args, "--function-name")
		if fnName == "" {
			return nil, fmt.Errorf("cannot rewrite update-function-code without --function-name")
		}

		payload := map[string]any{
			"FunctionName": fnName,
			"ZipFile":      encodedZip,
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		return []string{"lambda", "update-function-code", "--cli-input-json", string(b)}, nil
	default:
		return args, nil
	}
}

func buildLambdaZip(runtime string, handler string) ([]byte, error) {
	if runtime == "" {
		runtime = "python3.12"
	}

	if strings.HasPrefix(runtime, "python") {
		module := "lambda_function"
		fn := "lambda_handler"
		if strings.Contains(handler, ".") {
			parts := strings.SplitN(handler, ".", 2)
			if strings.TrimSpace(parts[0]) != "" {
				module = strings.TrimSpace(parts[0])
			}
			if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
				fn = strings.TrimSpace(parts[1])
			}
		}

		code := fmt.Sprintf(
			"import json\n\n"+
				"def %s(event, context):\n"+
				"    # Works for Lambda Function URLs and API Gateway event shapes\n"+
				"    path = event.get('rawPath') or event.get('path') or '/'\n"+
				"    if path == '/health':\n"+
				"        return {\n"+
				"            'statusCode': 200,\n"+
				"            'headers': {'content-type': 'application/json'},\n"+
				"            'body': json.dumps({'status': 'healthy'}),\n"+
				"        }\n"+
				"    return {\n"+
				"        'statusCode': 404,\n"+
				"        'headers': {'content-type': 'application/json'},\n"+
				"        'body': json.dumps({'message': 'Not Found'}),\n"+
				"    }\n",
			fn,
		)

		buf := new(bytes.Buffer)
		zw := zip.NewWriter(buf)
		f, err := zw.Create(module + ".py")
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := f.Write([]byte(code)); err != nil {
			_ = zw.Close()
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	return nil, fmt.Errorf("unsupported runtime for inline zip: %q", runtime)
}

func streamMerged(w io.Writer, readers ...io.Reader) (string, error) {
	merged := io.MultiReader(readers...)
	var captured strings.Builder
	scanner := bufio.NewScanner(merged)
	for scanner.Scan() {
		line := scanner.Text()
		captured.WriteString(line)
		captured.WriteString("\n")
		if _, err := fmt.Fprintln(w, line); err != nil {
			return captured.String(), err
		}
	}
	return captured.String(), scanner.Err()
}

func runAWSCommandStreaming(ctx context.Context, args []string, stdinBytes []byte, w io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, "aws", args...)
	if len(stdinBytes) > 0 {
		cmd.Stdin = bytes.NewReader(stdinBytes)
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return "", err
	}

	out, streamErr := streamMerged(w, stdout, stderr)
	if streamErr != nil {
		_ = cmd.Process.Kill()
		return out, streamErr
	}

	if err := cmd.Wait(); err != nil {
		return out, err
	}

	return out, nil
}

func isLambdaCreateFunction(args []string) bool {
	return len(args) >= 2 && args[0] == "lambda" && args[1] == "create-function"
}

func isLambdaAlreadyExists(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "resourceconflictexception") || strings.Contains(lower, "already exists")
}

func shouldIgnoreFailure(args []string, failure AWSFailure, output string) bool {
	if len(args) < 2 {
		return false
	}
	lower := strings.ToLower(output)
	code := failure.Code

	// Common "safe to ignore" error fragments for best-effort prerequisite cleanup.
	isNotFoundish := strings.Contains(lower, "nosuchentity") ||
		strings.Contains(lower, "resourcenotfound") ||
		strings.Contains(lower, "notfoundexception") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "does not exist")
	isNotAttachedish := strings.Contains(lower, "not attached") ||
		strings.Contains(lower, "is not attached") ||
		strings.Contains(lower, "cannot detach")
	if code != "" {
		// Prefer error codes when available.
		if code == "NoSuchEntity" || code == "ResourceNotFoundException" || code == "NotFoundException" {
			isNotFoundish = true
		}
	}

	// Generic idempotency: delete/remove/detach/disassociate operations should not fail if the
	// target is already gone.
	// (This is especially important during teardown when partial deletion has already happened.)
	if failure.Category == FailureNotFound || isNotFoundish {
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if strings.HasPrefix(op, "delete") || strings.HasPrefix(op, "remove") || strings.HasPrefix(op, "detach") || strings.HasPrefix(op, "disassociate") {
			// Special-case: API Gateway v2 IDs deleted via v1 command show up as "Invalid API identifier specified".
			// Don't ignore this; let resources glue fall back to apigatewayv2 delete-api.
			if args[0] == "apigateway" && args[1] == "delete-rest-api" && strings.Contains(lower, "invalid api identifier specified") {
				return false
			}
			return true
		}
	}

	// IAM role creation is effectively idempotent for our use-case.
	if args[0] == "iam" && args[1] == "create-role" {
		return strings.Contains(lower, "entityalreadyexists") || strings.Contains(lower, "already exists")
	}

	// Already-exists is typically idempotent for maker runs.
	// Exception: S3 BucketAlreadyExists means the name is taken by someone else.
	if failure.Category == FailureAlreadyExists {
		if args[0] == "cloudformation" && len(args) >= 2 && args[1] == "create-stack" {
			return false
		}
		if code == "BucketAlreadyExists" {
			return false
		}
		return true
	}

	// IAM detach operations are best-effort prerequisites; missing policies/attachments should not block workflows.
	if args[0] == "iam" {
		switch args[1] {
		case "detach-role-policy", "detach-user-policy", "detach-group-policy":
			return isNotFoundish || isNotAttachedish || code == "NoSuchEntity"
		case "delete-role-policy", "remove-role-from-instance-profile", "delete-role-permissions-boundary":
			return isNotFoundish || code == "NoSuchEntity"
		}
	}

	// Function URL config often already exists on re-apply.
	if args[0] == "lambda" && args[1] == "create-function-url-config" {
		return strings.Contains(lower, "resourceconflictexception") || strings.Contains(lower, "already exists")
	}

	// Deleting a function URL config is best-effort cleanup.
	if args[0] == "lambda" && args[1] == "delete-function-url-config" {
		return isNotFoundish || code == "ResourceNotFoundException"
	}

	// Re-adding a permission statement-id commonly conflicts; safe to ignore.
	if args[0] == "lambda" && args[1] == "add-permission" {
		return strings.Contains(lower, "resourceconflictexception") || strings.Contains(lower, "already exists")
	}

	// Deleting log groups is best-effort cleanup.
	if args[0] == "logs" && args[1] == "delete-log-group" {
		return strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "not found")
	}

	// EC2 security group rule authorization is often re-applied; duplicates are safe to ignore.
	if args[0] == "ec2" && (args[1] == "authorize-security-group-ingress" || args[1] == "authorize-security-group-egress") {
		return code == "InvalidPermission.Duplicate" || strings.Contains(lower, "invalidpermission.duplicate") || strings.Contains(lower, "already exists")
	}

	return false
}

func flagValue(args []string, flag string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(args[i], flag+"=") {
			return strings.TrimPrefix(args[i], flag+"=")
		}
	}
	return ""
}

func isIAMDeleteRole(args []string) bool {
	return len(args) >= 2 && args[0] == "iam" && args[1] == "delete-role"
}

func isIAMDeleteConflict(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "deleteconflict") && strings.Contains(lower, "deleterole")
}

func resolveAndDeleteIAMRole(ctx context.Context, opts ExecOptions, roleName string, w io.Writer) error {
	_, _ = fmt.Fprintf(w, "[maker] note: role delete conflicted; detaching policies and retrying (role=%s)\n", roleName)

	if err := detachAllRolePolicies(ctx, opts, roleName, w); err != nil {
		return err
	}
	if err := deleteAllRoleInlinePolicies(ctx, opts, roleName, w); err != nil {
		return err
	}
	if err := removeRoleFromAllInstanceProfiles(ctx, opts, roleName, w); err != nil {
		return err
	}
	_ = deleteRolePermissionsBoundary(ctx, opts, roleName, w)
	if err := waitForRoleDetachConvergence(ctx, opts, roleName, w); err != nil {
		return err
	}

	deleteArgs := []string{"iam", "delete-role", "--role-name", roleName}
	awsDeleteArgs := append(append([]string{}, deleteArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")

	for attempt := 1; attempt <= 6; attempt++ {
		out, err := runAWSCommandStreaming(ctx, awsDeleteArgs, nil, w)
		if err == nil {
			return nil
		}
		if !isIAMDeleteConflict(out) {
			return err
		}
		_, _ = fmt.Fprintf(w, "[maker] note: delete-role still conflicted; retrying (attempt=%d role=%s)\n", attempt, roleName)
		time.Sleep(time.Duration(attempt) * 600 * time.Millisecond)
	}

	return fmt.Errorf("role still cannot be deleted after cleanup: %s", roleName)
}

func detachAllRolePolicies(ctx context.Context, opts ExecOptions, roleName string, w io.Writer) error {
	marker := ""
	for {
		listArgs := []string{"iam", "list-attached-role-policies", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			listArgs = append(listArgs, "--marker", marker)
		}
		awsListArgs := append(append([]string{}, listArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsListArgs, nil, io.Discard)
		if err != nil {
			return err
		}

		var resp struct {
			AttachedPolicies []struct {
				PolicyArn string `json:"PolicyArn"`
			} `json:"AttachedPolicies"`
			IsTruncated bool   `json:"IsTruncated"`
			Marker      string `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return fmt.Errorf("failed to parse list-attached-role-policies output: %w", err)
		}

		for _, ap := range resp.AttachedPolicies {
			arn := strings.TrimSpace(ap.PolicyArn)
			if arn == "" {
				continue
			}
			_, _ = fmt.Fprintf(w, "[maker] detaching policy from role (role=%s policy=%s)\n", roleName, arn)
			detachArgs := []string{"iam", "detach-role-policy", "--role-name", roleName, "--policy-arn", arn}
			awsDetachArgs := append(append([]string{}, detachArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			if _, err := runAWSCommandStreaming(ctx, awsDetachArgs, nil, w); err != nil {
				return err
			}
		}

		if !resp.IsTruncated {
			break
		}
		if strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}

	return nil
}

func deleteAllRoleInlinePolicies(ctx context.Context, opts ExecOptions, roleName string, w io.Writer) error {
	marker := ""
	for {
		listArgs := []string{"iam", "list-role-policies", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			listArgs = append(listArgs, "--marker", marker)
		}
		awsListArgs := append(append([]string{}, listArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsListArgs, nil, io.Discard)
		if err != nil {
			return err
		}

		var resp struct {
			PolicyNames []string `json:"PolicyNames"`
			IsTruncated bool     `json:"IsTruncated"`
			Marker      string   `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return fmt.Errorf("failed to parse list-role-policies output: %w", err)
		}

		for _, name := range resp.PolicyNames {
			policyName := strings.TrimSpace(name)
			if policyName == "" {
				continue
			}
			_, _ = fmt.Fprintf(w, "[maker] deleting inline role policy (role=%s policy=%s)\n", roleName, policyName)
			deleteArgs := []string{"iam", "delete-role-policy", "--role-name", roleName, "--policy-name", policyName}
			awsDeleteArgs := append(append([]string{}, deleteArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			if _, err := runAWSCommandStreaming(ctx, awsDeleteArgs, nil, w); err != nil {
				return err
			}
		}

		if !resp.IsTruncated {
			break
		}
		if strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}

	return nil
}

func removeRoleFromAllInstanceProfiles(ctx context.Context, opts ExecOptions, roleName string, w io.Writer) error {
	marker := ""
	for {
		listArgs := []string{"iam", "list-instance-profiles-for-role", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			listArgs = append(listArgs, "--marker", marker)
		}
		awsListArgs := append(append([]string{}, listArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsListArgs, nil, io.Discard)
		if err != nil {
			return err
		}

		var resp struct {
			InstanceProfiles []struct {
				InstanceProfileName string `json:"InstanceProfileName"`
			} `json:"InstanceProfiles"`
			IsTruncated bool   `json:"IsTruncated"`
			Marker      string `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return fmt.Errorf("failed to parse list-instance-profiles-for-role output: %w", err)
		}

		for _, ip := range resp.InstanceProfiles {
			name := strings.TrimSpace(ip.InstanceProfileName)
			if name == "" {
				continue
			}
			_, _ = fmt.Fprintf(w, "[maker] removing role from instance profile (role=%s profile=%s)\n", roleName, name)
			removeArgs := []string{"iam", "remove-role-from-instance-profile", "--instance-profile-name", name, "--role-name", roleName}
			awsRemoveArgs := append(append([]string{}, removeArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			if _, err := runAWSCommandStreaming(ctx, awsRemoveArgs, nil, w); err != nil {
				return err
			}
		}

		if !resp.IsTruncated {
			break
		}
		if strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}

	return nil
}

func waitForRoleDetachConvergence(ctx context.Context, opts ExecOptions, roleName string, w io.Writer) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for role policy detach to converge: %s", roleName)
		}

		attached, err := countRoleAttachedPolicies(ctx, opts, roleName)
		if err != nil {
			return err
		}
		inline, err := countRoleInlinePolicies(ctx, opts, roleName)
		if err != nil {
			return err
		}
		profiles, err := countRoleInstanceProfiles(ctx, opts, roleName)
		if err != nil {
			return err
		}

		if attached == 0 && inline == 0 && profiles == 0 {
			return nil
		}

		_, _ = fmt.Fprintf(w, "[maker] note: waiting for IAM detach consistency (role=%s attached=%d inline=%d instanceProfiles=%d)\n", roleName, attached, inline, profiles)
		time.Sleep(700 * time.Millisecond)
	}
}

func countRoleAttachedPolicies(ctx context.Context, opts ExecOptions, roleName string) (int, error) {
	marker := ""
	total := 0
	for {
		args := []string{"iam", "list-attached-role-policies", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return 0, err
		}
		var resp struct {
			AttachedPolicies []any  `json:"AttachedPolicies"`
			IsTruncated      bool   `json:"IsTruncated"`
			Marker           string `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return 0, err
		}
		total += len(resp.AttachedPolicies)
		if !resp.IsTruncated || strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}
	return total, nil
}

func countRoleInlinePolicies(ctx context.Context, opts ExecOptions, roleName string) (int, error) {
	marker := ""
	total := 0
	for {
		args := []string{"iam", "list-role-policies", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return 0, err
		}
		var resp struct {
			PolicyNames []string `json:"PolicyNames"`
			IsTruncated bool     `json:"IsTruncated"`
			Marker      string   `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return 0, err
		}
		total += len(resp.PolicyNames)
		if !resp.IsTruncated || strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}
	return total, nil
}

func countRoleInstanceProfiles(ctx context.Context, opts ExecOptions, roleName string) (int, error) {
	marker := ""
	total := 0
	for {
		args := []string{"iam", "list-instance-profiles-for-role", "--role-name", roleName, "--output", "json"}
		if marker != "" {
			args = append(args, "--marker", marker)
		}
		awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err != nil {
			return 0, err
		}
		var resp struct {
			InstanceProfiles []any  `json:"InstanceProfiles"`
			IsTruncated      bool   `json:"IsTruncated"`
			Marker           string `json:"Marker"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return 0, err
		}
		total += len(resp.InstanceProfiles)
		if !resp.IsTruncated || strings.TrimSpace(resp.Marker) == "" {
			break
		}
		marker = strings.TrimSpace(resp.Marker)
	}
	return total, nil
}

func deleteRolePermissionsBoundary(ctx context.Context, opts ExecOptions, roleName string, w io.Writer) error {
	args := []string{"iam", "delete-role-permissions-boundary", "--role-name", roleName}
	awsArgs := append(append([]string{}, args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	_, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err == nil {
		_, _ = fmt.Fprintf(w, "[maker] deleted role permissions boundary (role=%s)\n", roleName)
		return nil
	}
	return err
}

func updateExistingLambda(ctx context.Context, opts ExecOptions, createArgs []string, zipBytes []byte, w io.Writer) error {
	fnName := flagValue(createArgs, "--function-name")
	if fnName == "" {
		return fmt.Errorf("missing --function-name for lambda update fallback")
	}

	runtime := flagValue(createArgs, "--runtime")
	handler := flagValue(createArgs, "--handler")
	role := flagValue(createArgs, "--role")

	if len(zipBytes) == 0 {
		b, err := buildLambdaZip(runtime, handler)
		if err != nil {
			return err
		}
		zipBytes = b
	}

	_, _ = fmt.Fprintf(w, "[maker] note: lambda already exists; updating code/config\n")

	codeArgs := []string{"lambda", "update-function-code", "--function-name", fnName, "--zip-file", "fileb://function.zip"}
	codeArgs = substituteAccountID(codeArgs, "")
	zipBytes2, codeArgs2, err := maybeInjectLambdaZipBytes(codeArgs, w)
	if err != nil {
		return err
	}
	if len(zipBytes2) > 0 {
		zipBytes = zipBytes2
	}
	awsCodeArgs := append(append([]string{}, codeArgs2...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	if _, err := runAWSCommandStreaming(ctx, awsCodeArgs, zipBytes, w); err != nil {
		return err
	}

	configArgs := []string{"lambda", "update-function-configuration", "--function-name", fnName}
	if runtime != "" {
		configArgs = append(configArgs, "--runtime", runtime)
	}
	if handler != "" {
		configArgs = append(configArgs, "--handler", handler)
	}
	if role != "" {
		configArgs = append(configArgs, "--role", role)
	}

	if len(configArgs) > 3 {
		awsCfgArgs := append(append([]string{}, configArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		if _, err := runAWSCommandStreaming(ctx, awsCfgArgs, nil, w); err != nil {
			return err
		}
	}

	return nil
}

func validateCommand(args []string, allowDestructive bool) error {
	if len(args) == 0 {
		return fmt.Errorf("empty args")
	}

	// Plans must be AWS CLI subcommands only (e.g. ["iam", "create-role", ...]).
	// Disallow common non-AWS executables to avoid accidentally running nonsense like `aws python3 ...`.
	first := strings.ToLower(strings.TrimSpace(args[0]))
	switch {
	case first == "python" || strings.HasPrefix(first, "python"):
		return fmt.Errorf("non-aws command is not allowed: %q", args[0])
	case first == "node" || first == "npm" || first == "npx":
		return fmt.Errorf("non-aws command is not allowed: %q", args[0])
	case first == "bash" || first == "sh" || first == "zsh" || first == "fish":
		return fmt.Errorf("non-aws command is not allowed: %q", args[0])
	case first == "curl" || first == "wget":
		return fmt.Errorf("non-aws command is not allowed: %q", args[0])
	case first == "zip" || first == "unzip":
		return fmt.Errorf("non-aws command is not allowed: %q", args[0])
	case first == "terraform" || first == "tofu" || first == "make":
		return fmt.Errorf("non-aws command is not allowed: %q", args[0])
	}

	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
		if allowDestructive {
			continue
		}
		if strings.Contains(lower, "delete") || strings.Contains(lower, "terminate") || strings.Contains(lower, "remove") || strings.Contains(lower, "destroy") {
			return fmt.Errorf("destructive verbs are blocked")
		}
	}

	return nil
}
