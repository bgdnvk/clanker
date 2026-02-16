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
	"runtime"
	"strconv"
	"strings"
	"time"
)

var awsErrorCodeRe = regexp.MustCompile(`(?i)an error occurred \(([^)]+)\)`)
var planPlaceholderTokenRe = regexp.MustCompile(`<([A-Z0-9_]+)>`)
var shellStylePlaceholderTokenRe = regexp.MustCompile(`^\$[A-Z][A-Z0-9_]*$`)
var awsARNRegionHintRe = regexp.MustCompile(`arn:aws[a-zA-Z-]*:[a-z0-9-]+:([a-z0-9-]+):\d{12}:[^,\s"']+`)

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

type healingPolicy struct {
	Enabled             bool
	MaxAutoHealAttempts int
	TransientRetries    int
	MaxWindow           time.Duration
}

type healingRuntime struct {
	StartedAt        time.Time
	AutoHealAttempts int
}

func defaultHealingPolicy() healingPolicy {
	return healingPolicy{
		Enabled:             true,
		MaxAutoHealAttempts: 4,
		TransientRetries:    2,
		MaxWindow:           8 * time.Minute,
	}
}

func (p healingPolicy) canAttempt(runtime *healingRuntime) bool {
	if !p.Enabled || runtime == nil {
		return false
	}
	if p.MaxAutoHealAttempts > 0 && runtime.AutoHealAttempts >= p.MaxAutoHealAttempts {
		return false
	}
	if p.MaxWindow > 0 && !runtime.StartedAt.IsZero() && time.Since(runtime.StartedAt) > p.MaxWindow {
		return false
	}
	return true
}

func (p healingPolicy) consumeAttempt(runtime *healingRuntime) bool {
	if !p.canAttempt(runtime) {
		return false
	}
	runtime.AutoHealAttempts++
	return true
}

type ExecOptions struct {
	Profile             string
	Region              string
	GCPProject          string
	AzureSubscriptionID string
	Writer              io.Writer
	Destroyer           bool

	AIProvider string
	AIAPIKey   string
	AIProfile  string
	Debug      bool

	// Cloudflare options
	CloudflareAPIToken  string
	CloudflareAccountID string

	CheckpointKey            string
	DisableDurableCheckpoint bool

	// OutputBindings is populated by ExecutePlan with the final resource bindings
	// (e.g., ALB_DNS, INSTANCE_ID, etc.) for the caller to use
	OutputBindings map[string]string
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
	healPolicy := defaultHealingPolicy()
	healRuntime := &healingRuntime{StartedAt: time.Now()}
	autoImagePrepared := false

	// Initialize bindings from OutputBindings if provided (for multi-phase execution)
	if opts.OutputBindings != nil {
		for k, v := range opts.OutputBindings {
			bindings[k] = v
		}
	}

	if !opts.DisableDurableCheckpoint {
		persisted, loadErr := loadDurableCheckpoint(plan, opts)
		if loadErr != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to load durable checkpoint: %v\n", loadErr)
		} else if len(persisted) > 0 {
			for k, v := range persisted {
				if strings.TrimSpace(bindings[k]) == "" {
					bindings[k] = v
				}
			}
			_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] loaded durable checkpoint state\n")
		}
	}

	resumeFromIndex := 0
	if raw := strings.TrimSpace(bindings["CHECKPOINT_LAST_SUCCESS_INDEX"]); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			resumeFromIndex = parsed
			_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] resuming from command %d/%d\n", resumeFromIndex+1, len(plan.Commands))
		}
	}
	// Pre-populate bindings with account and region info for user-data generation
	if accountID != "" {
		bindings["ACCOUNT_ID"] = accountID
		bindings["AWS_ACCOUNT_ID"] = accountID
	}
	if opts.Region != "" {
		bindings["REGION"] = opts.Region
		bindings["AWS_REGION"] = opts.Region
	}

	if warning := detectDestructiveRegionZigZag(plan, opts.Region); warning != "" {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] preflight warning: %s\n", warning)
	}

	for idx, cmdSpec := range plan.Commands {
		if resumeFromIndex > 0 && idx < resumeFromIndex {
			_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] skipping already-completed command %d/%d\n", idx+1, len(plan.Commands))
			continue
		}
		_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] start command %d/%d\n", idx+1, len(plan.Commands))

		if err := validateCommand(cmdSpec.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("command %d rejected: %w", idx+1, err)
		}

		args := make([]string, 0, len(cmdSpec.Args)+6)
		args = append(args, cmdSpec.Args...)
		args = substituteAccountID(args, accountID)
		args = applyPlanBindings(args, bindings)

		// AI-powered placeholder resolution with exponential backoff
		if hasUnresolvedPlaceholders(args) {
			resolved, resolveErr := maybeResolvePlaceholdersWithAI(ctx, opts, args, bindings, "")
			if resolveErr != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: placeholder resolution failed: %v\n", resolveErr)
			}
			if resolved != nil {
				args = resolved
			}
		}

		zipBytes, updatedArgs, err := maybeInjectLambdaZipBytes(args, opts.Writer)
		if err != nil {
			return fmt.Errorf("command %d prepare failed: %w", idx+1, err)
		}
		args = updatedArgs

		// Handle EC2 user-data generation for run-instances
		args = maybeGenerateEC2UserData(args, bindings, opts)

		if !autoImagePrepared && shouldAutoPrepareImage(args, bindings, opts) {
			if err := autoPrepareImageForOneClickDeploy(ctx, plan.Question, bindings, opts); err != nil {
				return fmt.Errorf("command %d image preparation failed: %w", idx+1, err)
			}
			autoImagePrepared = true
		}
		args = sanitizeCommandArgsForExecution(args, bindings)

		if unresolved := findUnresolvedExecutionTokens(args); len(unresolved) > 0 {
			return fmt.Errorf("command %d has unresolved placeholders: %s", idx+1, strings.Join(unresolved, ", "))
		}

		if handled, localErr := maybeRunLocalPlanStep(ctx, idx+1, len(plan.Commands), args, opts.Writer); handled {
			if localErr != nil {
				return fmt.Errorf("command %d failed: %w", idx+1, localErr)
			}
			continue
		}

		awsArgs := buildAWSExecArgs(args, opts, opts.Writer)

		_, _ = fmt.Fprintf(opts.Writer, "[maker] running %d/%d: %s\n", idx+1, len(plan.Commands), formatAWSArgsForLog(awsArgs))

		out, runErr := runAWSCommandStreaming(ctx, awsArgs, zipBytes, opts.Writer)
		if runErr != nil {
			if handled, handleErr := handleAWSFailure(ctx, plan, opts, idx, args, awsArgs, zipBytes, out, runErr, remediationAttempted, bindings, healPolicy, healRuntime); handled {
				if handleErr != nil {
					return handleErr
				}
				bindings["CHECKPOINT_LAST_FAILURE_INDEX"] = strconv.Itoa(idx)
				if !opts.DisableDurableCheckpoint {
					if persistErr := persistDurableCheckpoint(plan, opts, bindings); persistErr != nil {
						_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to persist durable checkpoint: %v\n", persistErr)
					}
				}
				continue
			}
			bindings["CHECKPOINT_LAST_FAILURE_INDEX"] = strconv.Itoa(idx)
			if !opts.DisableDurableCheckpoint {
				if persistErr := persistDurableCheckpoint(plan, opts, bindings); persistErr != nil {
					_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to persist durable checkpoint: %v\n", persistErr)
				}
			}
			return fmt.Errorf("aws command %d failed: %w", idx+1, runErr)
		}

		// Learn placeholder bindings from successful command outputs.
		learnPlanBindingsFromProduces(cmdSpec.Produces, out, bindings)
		learnPlanBindings(args, out, bindings)
		bindings["CHECKPOINT_LAST_SUCCESS_INDEX"] = strconv.Itoa(idx + 1)
		bindings["CHECKPOINT_LAST_FAILURE_INDEX"] = ""

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
					if handled, handleErr := handleAWSFailure(ctx, plan, opts, idx, args, awsArgs, zipBytes, combined, synthErr, remediationAttempted, bindings, healPolicy, healRuntime); handled {
						if handleErr != nil {
							return handleErr
						}
						bindings["CHECKPOINT_LAST_FAILURE_INDEX"] = strconv.Itoa(idx)
						if !opts.DisableDurableCheckpoint {
							if persistErr := persistDurableCheckpoint(plan, opts, bindings); persistErr != nil {
								_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to persist durable checkpoint: %v\n", persistErr)
							}
						}
						continue
					}
					bindings["CHECKPOINT_LAST_FAILURE_INDEX"] = strconv.Itoa(idx)
					if !opts.DisableDurableCheckpoint {
						if persistErr := persistDurableCheckpoint(plan, opts, bindings); persistErr != nil {
							_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to persist durable checkpoint: %v\n", persistErr)
						}
					}
					return fmt.Errorf("aws command %d failed: %w", idx+1, synthErr)
				}
			}
		}

		_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] success command %d/%d\n", idx+1, len(plan.Commands))
		if !opts.DisableDurableCheckpoint {
			if persistErr := persistDurableCheckpoint(plan, opts, bindings); persistErr != nil {
				_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to persist durable checkpoint: %v\n", persistErr)
			}
		}
	}

	// Populate output bindings for the caller
	if opts.OutputBindings != nil {
		for k, v := range bindings {
			opts.OutputBindings[k] = v
		}
	}

	if !opts.DisableDurableCheckpoint {
		if clearErr := clearDurableCheckpoint(plan, opts); clearErr != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker][checkpoint] warning: failed to clear durable checkpoint: %v\n", clearErr)
		}
	}

	return nil
}

func shouldAutoPrepareImage(args []string, bindings map[string]string, opts ExecOptions) bool {
	if len(args) < 2 || args[0] != "ec2" || args[1] != "run-instances" {
		return false
	}
	if strings.TrimSpace(opts.Profile) == "" || strings.TrimSpace(opts.Region) == "" {
		return false
	}
	ecrURI := strings.TrimSpace(bindings["ECR_URI"])
	if ecrURI == "" {
		return false
	}
	if strings.TrimSpace(bindings["IMAGE_URI"]) != "" {
		return false
	}
	return true
}

func autoPrepareImageForOneClickDeploy(ctx context.Context, question string, bindings map[string]string, opts ExecOptions) error {
	ecrURI := strings.TrimSpace(bindings["ECR_URI"])
	if ecrURI == "" {
		return fmt.Errorf("missing ECR_URI binding")
	}

	if !HasDockerInstalled() {
		return fmt.Errorf("docker is required for one-click image build but is not installed")
	}

	exists, err := ecrImageTagExists(ctx, ecrURI, opts.Profile, opts.Region, "latest")
	if err != nil {
		return err
	}
	if exists {
		bindings["IMAGE_URI"] = ecrURI + ":latest"
		_, _ = fmt.Fprintf(opts.Writer, "[docker] found existing image in ECR, skipping build: %s\n", bindings["IMAGE_URI"])
		return nil
	}

	repoURL := extractRepoURLFromQuestion(question)
	if repoURL == "" {
		return fmt.Errorf("ECR image missing and repo URL could not be inferred from plan question")
	}

	_, _ = fmt.Fprintf(opts.Writer, "[docker] one-click: image missing in ECR, building and pushing from repo %s\n", repoURL)
	clonePath, cleanup, err := cloneRepoForImageBuild(ctx, repoURL)
	if err != nil {
		return err
	}
	defer cleanup()

	imageURI, err := BuildAndPushDockerImage(ctx, clonePath, ecrURI, opts.Profile, opts.Region, opts.Writer)
	if err != nil {
		return err
	}
	bindings["IMAGE_URI"] = imageURI
	_, _ = fmt.Fprintf(opts.Writer, "[docker] one-click: image ready %s\n", imageURI)
	return nil
}

func learnPlanBindingsFromProduces(produces map[string]string, output string, bindings map[string]string) {
	if len(produces) == 0 {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}

	// Handle plain text output (e.g., SSM get-parameters with --output text)
	if !strings.HasPrefix(output, "{") && !strings.HasPrefix(output, "[") {
		// Check if any produce expects this to be a simple value
		for key, path := range produces {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			// If path is $.Output or similar simple path, use raw output
			if path == "$.Output" || path == "$" || path == "." {
				bindings[key] = output
			}
			// Handle AMI_ID from plain text SSM output
			if key == "AMI_ID" && strings.HasPrefix(output, "ami-") {
				bindings[key] = output
			}
		}
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
		if v, ok := jsonPathString(obj, path); ok && v != "" {
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
	path = strings.TrimPrefix(path, "$")
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
	// fix: if --role-name got an ARN, extract just the role name
	out = fixRoleNameArg(out)
	return out
}

// fixRoleNameArg extracts role name from ARN if --role-name was given a full ARN
func fixRoleNameArg(args []string) []string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--role-name" {
			val := args[i+1]
			if strings.HasPrefix(val, "arn:") && strings.Contains(val, ":role/") {
				// extract role name from arn:aws:iam::123456789012:role/RoleName
				parts := strings.Split(val, ":role/")
				if len(parts) == 2 {
					args[i+1] = parts[1]
				}
			}
		}
	}
	return args
}

func learnPlanBindings(args []string, output string, bindings map[string]string) {
	if len(args) < 2 {
		return
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}

	service := strings.TrimSpace(args[0])
	op := strings.TrimSpace(args[1])

	// Handle plain text output for specific commands before trying JSON parse
	if service == "ssm" && op == "get-parameters" {
		// SSM with --output text returns just the value, e.g. "ami-0532be01f26a3de55"
		if strings.HasPrefix(output, "ami-") && !strings.Contains(output, "{") {
			bindings["AMI_ID"] = output
			return
		}
	}

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
			az := deepString(obj, "Subnet", "AvailabilityZone")

			// Dynamic bindings based on AZ and order
			inferSubnetBindings(id, az, bindings)

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

			// Dynamic binding: infer placeholder names from group name
			// e.g., "lambdatron-rds-sg" -> SG_RDS_ID, SG_RDS, RdsSgId
			inferSGBindings(groupName, gid, bindings)

			// Fixed mappings for known names
			switch groupName {
			case "alb-sg":
				bindings["SG_ALB_ID"] = gid
				bindings["SG_ALB"] = gid
			case "web-sg", "web-server-sg":
				bindings["SG_WEB_ID"] = gid
				bindings["SG_WEB"] = gid
			}

			// Fill first empty slot in common placeholders
			for _, k := range []string{"SG_ID", "SG_1", "SG_ALB_ID", "SG_WEB_ID", "SG_RDS_ID", "SG_LAMBDA_ID", "SG_CLIENT_ID"} {
				if strings.TrimSpace(bindings[k]) == "" {
					bindings[k] = gid
					break
				}
			}

			// Common placeholder aliases
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
			dns := deepString(obj, "LoadBalancers", "0", "DNSName")
			if dns != "" {
				bindings["ALB_DNS"] = dns
				bindings["ALB_DNS_NAME"] = dns
			}
		case "create-target-group":
			arn := deepString(obj, "TargetGroups", "0", "TargetGroupArn")
			if arn != "" {
				bindings["TG_ARN"] = arn
			}
		}
	case "ssm":
		switch op {
		case "get-parameters":
			// JSON output: {"Parameters":[{"Name":"...","Value":"ami-..."}]}
			val := deepString(obj, "Parameters", "0", "Value")
			if val != "" && strings.HasPrefix(val, "ami-") {
				bindings["AMI_ID"] = val
			}
		}
	case "lambda":
		switch op {
		case "create-function":
			// {"FunctionArn":"arn:aws:lambda:..."}
			arn := deepString(obj, "FunctionArn")
			if arn != "" {
				inferLambdaBindings(arn, bindings)
			}
		}
	case "apigatewayv2":
		switch op {
		case "create-api":
			// {"ApiId":"abc123"}
			apiID := deepString(obj, "ApiId")
			if apiID != "" {
				inferAPIGatewayBindings(apiID, bindings)
			}
		case "create-integration":
			// {"IntegrationId":"abc123"}
			intID := deepString(obj, "IntegrationId")
			if intID != "" {
				inferIntegrationBindings(intID, bindings)
			}
		case "create-route":
			// {"RouteId":"abc123"}
			routeID := deepString(obj, "RouteId")
			if routeID != "" {
				inferRouteBindings(routeID, bindings)
			}
		case "create-stage":
			// {"StageName":"$default"}
			stageName := deepString(obj, "StageName")
			if stageName != "" {
				inferStageBindings(stageName, bindings)
			}
		}
	case "rds":
		switch op {
		case "create-db-instance":
			// {"DBInstance":{"DBInstanceIdentifier":"...", "Endpoint":{"Address":"..."}}}
			id := deepString(obj, "DBInstance", "DBInstanceIdentifier")
			endpoint := deepString(obj, "DBInstance", "Endpoint", "Address")
			arn := deepString(obj, "DBInstance", "DBInstanceArn")
			inferRDSBindings(id, endpoint, arn, bindings)
		case "create-db-cluster":
			id := deepString(obj, "DBCluster", "DBClusterIdentifier")
			endpoint := deepString(obj, "DBCluster", "Endpoint")
			arn := deepString(obj, "DBCluster", "DBClusterArn")
			inferRDSClusterBindings(id, endpoint, arn, bindings)
		case "create-db-subnet-group":
			name := deepString(obj, "DBSubnetGroup", "DBSubnetGroupName")
			arn := deepString(obj, "DBSubnetGroup", "DBSubnetGroupArn")
			inferDBSubnetGroupBindings(name, arn, bindings)
		}
	case "ecs":
		switch op {
		case "create-cluster":
			arn := deepString(obj, "cluster", "clusterArn")
			name := deepString(obj, "cluster", "clusterName")
			inferECSClusterBindings(name, arn, bindings)
		case "create-service":
			arn := deepString(obj, "service", "serviceArn")
			name := deepString(obj, "service", "serviceName")
			inferECSServiceBindings(name, arn, bindings)
		case "register-task-definition":
			arn := deepString(obj, "taskDefinition", "taskDefinitionArn")
			inferTaskDefBindings(arn, bindings)
		}
	case "ecr":
		switch op {
		case "create-repository":
			uri := deepString(obj, "repository", "repositoryUri")
			arn := deepString(obj, "repository", "repositoryArn")
			name := deepString(obj, "repository", "repositoryName")
			inferECRBindings(name, uri, arn, bindings)
		}
	case "sns":
		switch op {
		case "create-topic":
			arn := deepString(obj, "TopicArn")
			inferSNSBindings(arn, bindings)
		}
	case "sqs":
		switch op {
		case "create-queue":
			url := deepString(obj, "QueueUrl")
			inferSQSBindings(url, bindings)
		}
	case "dynamodb":
		switch op {
		case "create-table":
			arn := deepString(obj, "TableDescription", "TableArn")
			name := deepString(obj, "TableDescription", "TableName")
			inferDynamoDBBindings(name, arn, bindings)
		}
	case "secretsmanager":
		switch op {
		case "create-secret":
			arn := deepString(obj, "ARN")
			name := deepString(obj, "Name")
			inferSecretsBindings(name, arn, bindings)
		}
	case "s3api", "s3":
		switch op {
		case "create-bucket":
			bucket := flagValue(args, "--bucket")
			inferS3Bindings(bucket, bindings)
		}
	case "elasticache":
		switch op {
		case "create-cache-cluster":
			id := deepString(obj, "CacheCluster", "CacheClusterId")
			arn := deepString(obj, "CacheCluster", "ARN")
			inferElastiCacheBindings(id, arn, bindings)
		case "create-replication-group":
			id := deepString(obj, "ReplicationGroup", "ReplicationGroupId")
			arn := deepString(obj, "ReplicationGroup", "ARN")
			endpoint := deepString(obj, "ReplicationGroup", "PrimaryEndpoint", "Address")
			inferElastiCacheReplicationBindings(id, endpoint, arn, bindings)
		}
	case "events":
		switch op {
		case "put-rule":
			arn := deepString(obj, "RuleArn")
			inferEventBridgeBindings(arn, bindings)
		}
	case "stepfunctions", "sfn":
		switch op {
		case "create-state-machine":
			arn := deepString(obj, "stateMachineArn")
			inferStepFunctionBindings(arn, bindings)
		}
	case "cognito-idp":
		switch op {
		case "create-user-pool":
			id := deepString(obj, "UserPool", "Id")
			arn := deepString(obj, "UserPool", "Arn")
			inferCognitoPoolBindings(id, arn, bindings)
		case "create-user-pool-client":
			clientID := deepString(obj, "UserPoolClient", "ClientId")
			inferCognitoClientBindings(clientID, bindings)
		}
	case "logs":
		switch op {
		case "create-log-group":
			name := flagValue(args, "--log-group-name")
			inferLogGroupBindings(name, bindings)
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
	policy healingPolicy,
	runtime *healingRuntime,
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

	if policy.canAttempt(runtime) {
		if retried, retryErr := maybeRetryTransientFailure(ctx, opts, idx, awsArgs, stdinBytes, failure, policy, runtime); retried {
			return true, retryErr
		}
	}

	if handled, handleErr := maybeRewriteAndRetry(ctx, opts, args, awsArgs, stdinBytes, failure, out, bindings); handled {
		return true, handleErr
	}

	if remediationAttempted[idx] {
		return false, nil
	}
	if !policy.canAttempt(runtime) {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] self-heal budget exhausted; escalating failure\n")
		return false, nil
	}

	if policy.consumeAttempt(runtime) {
		if remediated, remErr := maybeAutoRemediateAndRetry(ctx, plan, opts, idx, args, awsArgs, stdinBytes, out, failure); remErr == nil && remediated {
			remediationAttempted[idx] = true
			return true, nil
		}
	}

	if !policy.canAttempt(runtime) {
		return false, nil
	}

	if policy.consumeAttempt(runtime) {
		// Agentic AI fallback: send error to AI, get fix, retry with exponential backoff
		if handled, agentErr := maybeAgenticFix(ctx, opts, args, awsArgs, stdinBytes, out, bindings); handled {
			remediationAttempted[idx] = true
			return true, agentErr
		}
	}

	return false, runErr
}

func maybeRetryTransientFailure(
	ctx context.Context,
	opts ExecOptions,
	idx int,
	awsArgs []string,
	stdinBytes []byte,
	failure AWSFailure,
	policy healingPolicy,
	runtime *healingRuntime,
) (bool, error) {
	if failure.Category != FailureThrottled && failure.Category != FailureConflict {
		return false, nil
	}
	attempts := policy.TransientRetries
	if attempts <= 0 {
		return false, nil
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		if !policy.consumeAttempt(runtime) {
			return false, nil
		}
		delay := time.Duration(300*(1<<uint(attempt-1))) * time.Millisecond
		_, _ = fmt.Fprintf(opts.Writer, "[maker] transient failure retry %d/%d for command %d after %s (category=%s)\n", attempt, attempts, idx+1, delay, failure.Category)
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(delay):
		}

		out, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		if err == nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] transient retry succeeded for command %d\n", idx+1)
			return true, nil
		}
		failure = classifyAWSFailure(awsArgs, out)
		if failure.Category != FailureThrottled && failure.Category != FailureConflict {
			break
		}
	}

	return false, nil
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

func maybeRunLocalPlanStep(ctx context.Context, index, total int, args []string, w io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}

	verb := strings.ToLower(strings.TrimSpace(args[0]))
	if verb != "sleep" {
		return false, nil
	}

	if len(args) != 2 {
		return true, fmt.Errorf("sleep expects exactly one argument")
	}

	seconds, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil {
		return true, fmt.Errorf("invalid sleep duration %q", args[1])
	}
	if seconds < 0 || seconds > 600 {
		return true, fmt.Errorf("sleep duration out of range: %d", seconds)
	}

	_, _ = fmt.Fprintf(w, "[maker] running %d/%d: sleep %d\n", index, total, seconds)
	if seconds == 0 {
		return true, nil
	}

	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-timer.C:
		return true, nil
	}
}

// maybeGenerateEC2UserData handles user-data generation for EC2 run-instances commands.
// If the user-data argument contains a placeholder (like <USER_DATA> or $USER_DATA),
// it generates a proper startup script based on the available bindings.
// Supports both Docker deployment (default) and native Node.js deployment (DEPLOY_MODE=native).
func maybeGenerateEC2UserData(args []string, bindings map[string]string, opts ExecOptions) []string {
	if len(args) < 2 || args[0] != "ec2" || args[1] != "run-instances" {
		return args
	}

	// Find the --user-data argument
	userDataIdx := -1
	for i, arg := range args {
		if arg == "--user-data" && i+1 < len(args) {
			userDataIdx = i + 1
			break
		}
	}
	if userDataIdx < 0 {
		return args
	}

	currentUserData := args[userDataIdx]
	// Check if it needs to be generated (contains placeholder-like values)
	if !strings.Contains(currentUserData, "<USER_DATA>") &&
		!strings.Contains(currentUserData, "$USER_DATA") &&
		!strings.Contains(currentUserData, "<user_data>") &&
		currentUserData != "<USER_DATA>" &&
		currentUserData != "$USER_DATA" {
		// User-data looks real, leave it alone
		return args
	}

	// Check if native Node.js deployment mode (pre-generated user-data)
	deployMode := bindings["DEPLOY_MODE"]
	if deployMode == "native" {
		// Use pre-generated Node.js user-data script
		if script := bindings["NODEJS_USER_DATA"]; script != "" {
			encoded := base64.StdEncoding.EncodeToString([]byte(script))
			newArgs := make([]string, len(args))
			copy(newArgs, args)
			newArgs[userDataIdx] = encoded
			return newArgs
		}
	}

	// Docker deployment mode
	region := opts.Region
	accountID := bindings["ACCOUNT_ID"]
	if accountID == "" {
		accountID = bindings["AWS_ACCOUNT_ID"]
	}

	// Find ECR URI from bindings
	ecrURI := bindings["ECR_URI"]
	if ecrURI == "" {
		// Try to construct from ECR_REPO
		ecrRepo := bindings["ECR_REPO"]
		if ecrRepo == "" {
			ecrRepo = "clanker-app"
		}
		if accountID != "" && region != "" {
			ecrURI = fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com/%s", accountID, region, ecrRepo)
		}
	}

	if ecrURI == "" || region == "" || accountID == "" {
		// Missing required bindings, cannot generate user-data
		return args
	}

	// Get app port from bindings or default to 3000
	appPort := bindings["APP_PORT"]
	if appPort == "" {
		appPort = "3000"
	}

	// Build docker run command with environment variables
	var envFlags strings.Builder
	for key, value := range bindings {
		if strings.HasPrefix(key, "ENV_") {
			envName := strings.TrimPrefix(key, "ENV_")
			// Escape special characters for shell
			escaped := strings.ReplaceAll(value, `"`, `\"`)
			escaped = strings.ReplaceAll(escaped, `$`, `\$`)
			escaped = strings.ReplaceAll(escaped, "`", "\\`")
			envFlags.WriteString(fmt.Sprintf("-e \"%s=%s\" ", envName, escaped))
		}
	}

	// Check if we have a specific start command that includes the port
	// This handles apps that need --port flag instead of PORT env var
	startCmd := bindings["START_COMMAND"]

	var dockerRunCmd string
	if startCmd != "" {
		// Use the detected start command (handles apps needing --port flag)
		dockerRunCmd = fmt.Sprintf("docker run -d --restart unless-stopped -p %s:%s %s%s:latest %s",
			appPort, appPort, envFlags.String(), ecrURI, startCmd)
	} else {
		// Default: just pass env vars and use container's default CMD
		dockerRunCmd = fmt.Sprintf("docker run -d --restart unless-stopped -p %s:%s %s%s:latest",
			appPort, appPort, envFlags.String(), ecrURI)
	}

	// Generate the startup script
	script := fmt.Sprintf(`#!/bin/bash
set -ex
exec > /var/log/user-data.log 2>&1
yum update -y
yum install -y docker
systemctl start docker
systemctl enable docker
aws ecr get-login-password --region %s | docker login --username AWS --password-stdin %s.dkr.ecr.%s.amazonaws.com
docker pull %s:latest
%s
echo 'Deployment complete!'
`, region, accountID, region, ecrURI, dockerRunCmd)

	// Base64 encode the script
	encoded := base64.StdEncoding.EncodeToString([]byte(script))

	// Replace the user-data argument
	newArgs := make([]string, len(args))
	copy(newArgs, args)
	newArgs[userDataIdx] = encoded

	return newArgs
}

func sanitizeCommandArgsForExecution(args []string, bindings map[string]string) []string {
	if len(args) < 2 || args[0] != "ec2" || args[1] != "run-instances" {
		return args
	}

	valueIdx := -1
	inlineIdx := -1
	for i := 0; i < len(args); i++ {
		if args[i] == "--user-data" && i+1 < len(args) {
			valueIdx = i + 1
			break
		}
		if strings.HasPrefix(args[i], "--user-data=") {
			inlineIdx = i
			break
		}
	}

	if valueIdx < 0 && inlineIdx < 0 {
		return args
	}

	newArgs := make([]string, len(args))
	copy(newArgs, args)

	value := ""
	if valueIdx >= 0 {
		value = newArgs[valueIdx]
	} else {
		value = strings.TrimPrefix(newArgs[inlineIdx], "--user-data=")
	}

	trimmed := strings.TrimSpace(value)
	if isUserDataPlaceholderValue(trimmed) {
		if v := strings.TrimSpace(bindings["USER_DATA"]); v != "" {
			trimmed = v
		} else if v := strings.TrimSpace(bindings["NODEJS_USER_DATA"]); v != "" {
			trimmed = v
		}
	}

	if decoded, ok := decodeLikelyBase64UserData(trimmed); ok {
		trimmed = decoded
	}

	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")

	if valueIdx >= 0 {
		newArgs[valueIdx] = trimmed
	} else {
		newArgs[inlineIdx] = "--user-data=" + trimmed
	}

	return newArgs
}

func isUserDataPlaceholderValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if v == "<USER_DATA>" || v == "<user_data>" || v == "$USER_DATA" {
		return true
	}
	return strings.Contains(v, "<USER_DATA>") || strings.Contains(v, "<user_data>") || strings.Contains(v, "$USER_DATA")
}

func decodeLikelyBase64UserData(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "file://") {
		return "", false
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return "", false
	}
	if len(v) < 24 {
		return "", false
	}

	decodeAttempts := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	}
	for _, decodeFn := range decodeAttempts {
		decoded, err := decodeFn(v)
		if err != nil || len(decoded) == 0 {
			continue
		}
		s := strings.TrimSpace(string(decoded))
		if looksLikeUserDataScript(s) {
			return s, true
		}
	}

	return "", false
}

func looksLikeUserDataScript(script string) bool {
	if script == "" {
		return false
	}
	lower := strings.ToLower(script)
	if strings.HasPrefix(script, "#!") {
		return true
	}
	if strings.Contains(lower, "docker") && (strings.Contains(lower, "systemctl") || strings.Contains(lower, "dnf") || strings.Contains(lower, "apt") || strings.Contains(lower, "yum")) {
		return true
	}
	if strings.Contains(script, "\n") && (strings.Contains(lower, "aws ") || strings.Contains(lower, "bash") || strings.Contains(lower, "curl ")) {
		return true
	}
	return false
}

func findUnresolvedExecutionTokens(args []string) []string {
	if len(args) == 0 {
		return nil
	}

	userDataValueIdx := -1
	userDataInlineIdx := -1
	if len(args) >= 2 && strings.EqualFold(strings.TrimSpace(args[0]), "ec2") && strings.EqualFold(strings.TrimSpace(args[1]), "run-instances") {
		for i := 0; i < len(args); i++ {
			if args[i] == "--user-data" && i+1 < len(args) {
				userDataValueIdx = i + 1
				break
			}
			if strings.HasPrefix(args[i], "--user-data=") {
				userDataInlineIdx = i
				break
			}
		}
	}

	seen := map[string]bool{}
	result := make([]string, 0, 4)

	for i, arg := range args {
		for _, m := range planPlaceholderTokenRe.FindAllString(arg, -1) {
			if !seen[m] {
				seen[m] = true
				result = append(result, m)
			}
		}

		if i == userDataValueIdx || i == userDataInlineIdx {
			continue
		}
		if shellStylePlaceholderTokenRe.MatchString(strings.TrimSpace(arg)) {
			token := strings.TrimSpace(arg)
			if !seen[token] {
				seen[token] = true
				result = append(result, token)
			}
		}
	}

	return result
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

const envAWSCLIPath = "CLANKER_AWS_CLI_PATH"

func setEnvVar(env []string, key string, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+value)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

func pathKey(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

func awsExecutableNames() []string {
	if runtime.GOOS == "windows" {
		// Support common Windows launcher formats.
		return []string{"aws.exe", "aws.cmd", "aws.bat"}
	}
	return []string{"aws"}
}

func fileIsUsableExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func preferredPathsForOS() []string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "windows":
		return preferredWindowsPaths()
	case "darwin":
		paths := []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			"/usr/local/aws-cli/v2/current/bin",
			"/usr/bin",
			"/bin",
			"/usr/sbin",
			"/sbin",
		}
		if home != "" {
			paths = append(paths,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, "bin"),
			)
		}
		return paths
	default:
		// Linux and other Unix-like targets.
		paths := []string{
			"/home/linuxbrew/.linuxbrew/bin",
			"/usr/local/aws-cli/v2/current/bin",
			"/snap/bin",
			"/usr/local/bin",
			"/usr/bin",
			"/bin",
			"/usr/sbin",
			"/sbin",
		}
		if home != "" {
			paths = append(paths,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, "bin"),
			)
		}
		return paths
	}
}

func preferredWindowsPaths() []string {
	paths := []string{}

	// Common system locations.
	if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
		paths = append(paths, filepath.Join(systemRoot, "System32"))
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		paths = append(paths, filepath.Join(localAppData, "Microsoft", "WindowsApps"))
	}

	// AWS CLI v2 MSI installer path.
	if programFiles := strings.TrimSpace(os.Getenv("ProgramFiles")); programFiles != "" {
		paths = append(paths,
			filepath.Join(programFiles, "Amazon", "AWSCLIV2"),
			filepath.Join(programFiles, "Amazon", "AWSCLIV2", "bin"),
			filepath.Join(programFiles, "Git", "bin"),
		)
	}
	if programFilesX86 := strings.TrimSpace(os.Getenv("ProgramFiles(x86)")); programFilesX86 != "" {
		paths = append(paths,
			filepath.Join(programFilesX86, "Amazon", "AWSCLIV2"),
			filepath.Join(programFilesX86, "Amazon", "AWSCLIV2", "bin"),
		)
	}

	// Chocolatey.
	if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
		paths = append(paths, filepath.Join(programData, "chocolatey", "bin"))
	}

	// Scoop.
	if userProfile := strings.TrimSpace(os.Getenv("USERPROFILE")); userProfile != "" {
		paths = append(paths, filepath.Join(userProfile, "scoop", "shims"))
	}

	return paths
}

func augmentedPATH() string {
	base := strings.TrimSpace(os.Getenv("PATH"))
	dirs := preferredPathsForOS()
	if base != "" {
		dirs = append(dirs, strings.Split(base, string(os.PathListSeparator))...)
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(dirs)+8)
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		k := pathKey(d)
		if d == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return strings.Join(out, string(os.PathListSeparator))
}

func resolveAWSBinary() (string, []string, error) {
	attempts := []string{}

	if override := strings.TrimSpace(os.Getenv(envAWSCLIPath)); override != "" {
		// If it's a bare command name, resolve it via PATH.
		if !strings.ContainsAny(override, "\\/") {
			p, err := exec.LookPath(override)
			if err == nil {
				return p, []string{"env:" + envAWSCLIPath + " -> " + p}, nil
			}
			return "", []string{"env:" + envAWSCLIPath + " -> " + override}, fmt.Errorf("%s set but not found in PATH: %w", envAWSCLIPath, err)
		}
		attempts = append(attempts, "env:"+envAWSCLIPath+" -> "+override)
		if fileIsUsableExecutable(override) {
			return override, attempts, nil
		}
		return "", attempts, fmt.Errorf("%s points to a missing or non-executable file", envAWSCLIPath)
	}

	if p, err := exec.LookPath("aws"); err == nil {
		return p, []string{"PATH -> " + p}, nil
	}

	// Manual search to be resilient when PATH is sparse (common for GUI apps).
	searchDirs := preferredPathsForOS()
	if base := strings.TrimSpace(os.Getenv("PATH")); base != "" {
		searchDirs = append(searchDirs, strings.Split(base, string(os.PathListSeparator))...)
	}

	seen := map[string]bool{}
	for _, dir := range searchDirs {
		dir = strings.TrimSpace(dir)
		k := pathKey(dir)
		if dir == "" || seen[k] {
			continue
		}
		seen[k] = true
		for _, name := range awsExecutableNames() {
			candidate := filepath.Join(dir, name)
			attempts = append(attempts, candidate)
			if fileIsUsableExecutable(candidate) {
				return candidate, attempts, nil
			}
		}
	}

	// Avoid dumping huge PATH/attempt lists.
	maxAttempts := 25
	if len(attempts) > maxAttempts {
		attempts = append(attempts[:maxAttempts], fmt.Sprintf("... (%d more)", len(attempts)-maxAttempts))
	}

	return "", attempts, fmt.Errorf("aws CLI not found")
}

func runAWSCommandStreaming(ctx context.Context, args []string, stdinBytes []byte, w io.Writer) (string, error) {
	args = sanitizeAWSCLIArgs(args, w)

	awsBin, attempts, resolveErr := resolveAWSBinary()
	if resolveErr != nil {
		return "", fmt.Errorf("%v; install AWS CLI v2 or set %s. Tried: %s", resolveErr, envAWSCLIPath, strings.Join(attempts, ", "))
	}

	cmd := exec.CommandContext(ctx, awsBin, args...)
	cmd.Env = setEnvVar(os.Environ(), "PATH", augmentedPATH())
	if len(stdinBytes) > 0 {
		cmd.Stdin = bytes.NewReader(stdinBytes)
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start aws CLI (%s): %w", awsBin, err)
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

func sanitizeAWSCLIArgs(args []string, w io.Writer) []string {
	if len(args) < 2 {
		return args
	}
	if args[0] != "lightsail" {
		return args
	}

	op := strings.TrimSpace(args[1])
	if op == "" {
		return args
	}

	removeTagsForOp := op == "allocate-static-ip" || op == "attach-static-ip" || op == "open-instance-public-ports"
	out := make([]string, 0, len(args))
	out = append(out, args[0], args[1])

	for i := 2; i < len(args); i++ {
		token := strings.TrimSpace(args[i])

		if op == "allocate-static-ip" && token == "ignore" {
			if w != nil {
				_, _ = fmt.Fprintln(w, "[maker] sanitizing command: removed stray token 'ignore' from lightsail allocate-static-ip")
			}
			continue
		}

		if removeTagsForOp {
			if token == "--tags" {
				if w != nil {
					_, _ = fmt.Fprintf(w, "[maker] sanitizing command: removed unsupported --tags for lightsail %s\n", op)
				}
				if i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "--") {
					i++
				}
				continue
			}
			if strings.HasPrefix(token, "--tags=") {
				if w != nil {
					_, _ = fmt.Fprintf(w, "[maker] sanitizing command: removed unsupported --tags for lightsail %s\n", op)
				}
				continue
			}
		}

		out = append(out, args[i])
	}

	return out
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

	// EC2 subnet conflict means subnet with that CIDR already exists - not fatal if we can find existing.
	if args[0] == "ec2" && args[1] == "create-subnet" {
		return code == "InvalidSubnet.Conflict" || strings.Contains(lower, "invalidsubnet.conflict") || strings.Contains(lower, "conflicts with another subnet")
	}

	// EC2 security group already exists.
	if args[0] == "ec2" && args[1] == "create-security-group" {
		return code == "InvalidGroup.Duplicate" || strings.Contains(lower, "invalidgroup.duplicate") || strings.Contains(lower, "already exists")
	}

	// RDS subnet group already exists.
	if args[0] == "rds" && args[1] == "create-db-subnet-group" {
		return code == "DBSubnetGroupAlreadyExists" || strings.Contains(lower, "dbsubnetgroupalreadyexists") || strings.Contains(lower, "already exists")
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

func stripAWSRuntimeFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		token := strings.TrimSpace(args[i])
		switch {
		case token == "--profile" || token == "--region":
			if i+1 < len(args) {
				i++
			}
			continue
		case strings.HasPrefix(token, "--profile=") || strings.HasPrefix(token, "--region=") || token == "--no-cli-pager":
			continue
		default:
			out = append(out, args[i])
		}
	}
	return out
}

func detectRegionFromARNArgs(args []string) string {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" || !strings.Contains(trimmed, "arn:aws") {
			continue
		}
		matches := awsARNRegionHintRe.FindAllStringSubmatch(trimmed, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			region := strings.TrimSpace(m[1])
			if region != "" {
				return region
			}
		}
	}
	return ""
}

func resolveCommandRegion(args []string, fallbackRegion string) (region string, source string) {
	if explicit := strings.TrimSpace(flagValue(args, "--region")); explicit != "" {
		return explicit, "explicit"
	}
	if fromARN := strings.TrimSpace(detectRegionFromARNArgs(args)); fromARN != "" {
		return fromARN, "arn"
	}
	return strings.TrimSpace(fallbackRegion), "default"
}

func buildAWSExecArgs(args []string, opts ExecOptions, w io.Writer) []string {
	cleaned := stripAWSRuntimeFlags(args)
	region, source := resolveCommandRegion(cleaned, opts.Region)
	if strings.TrimSpace(region) == "" {
		region = strings.TrimSpace(opts.Region)
	}
	if source == "arn" && strings.TrimSpace(opts.Region) != "" && region != strings.TrimSpace(opts.Region) {
		if w != nil {
			service := ""
			op := ""
			if len(cleaned) > 0 {
				service = strings.TrimSpace(cleaned[0])
			}
			if len(cleaned) > 1 {
				op = strings.TrimSpace(cleaned[1])
			}
			_, _ = fmt.Fprintf(w, "[maker] region override detected: %s/%s uses %s from ARN (default=%s)\n", service, op, region, opts.Region)
		}
	}

	out := make([]string, 0, len(cleaned)+6)
	out = append(out, cleaned...)
	out = append(out, "--profile", opts.Profile, "--region", region, "--no-cli-pager")
	return out
}

func detectDestructiveRegionZigZag(plan *Plan, defaultRegion string) string {
	if plan == nil || len(plan.Commands) == 0 {
		return ""
	}

	type regionStep struct {
		index   int
		service string
		op      string
		region  string
	}

	steps := make([]regionStep, 0, len(plan.Commands))
	for idx, cmd := range plan.Commands {
		args := normalizeArgs(cmd.Args)
		if len(args) < 2 {
			continue
		}

		service := strings.ToLower(strings.TrimSpace(args[0]))
		op := strings.ToLower(strings.TrimSpace(args[1]))
		if !isDestructiveOperationForRegionOrdering(op) {
			continue
		}
		if isGlobalAWSServiceForRegionOrdering(service) {
			continue
		}

		region, _ := resolveCommandRegion(args, defaultRegion)
		region = strings.TrimSpace(region)
		if region == "" {
			continue
		}

		steps = append(steps, regionStep{index: idx + 1, service: service, op: op, region: region})
	}

	if len(steps) < 3 {
		return ""
	}

	closed := make(map[string]struct{})
	lastRegion := steps[0].region
	unique := map[string]struct{}{lastRegion: {}}
	zigzagAt := -1

	for i := 1; i < len(steps); i++ {
		current := steps[i].region
		if current == lastRegion {
			continue
		}
		closed[lastRegion] = struct{}{}
		if _, revisited := closed[current]; revisited {
			zigzagAt = i
			break
		}
		lastRegion = current
		unique[current] = struct{}{}
	}

	if zigzagAt == -1 || len(unique) < 2 {
		return ""
	}

	start := zigzagAt - 1
	if start < 0 {
		start = 0
	}
	end := zigzagAt + 1
	if end >= len(steps) {
		end = len(steps) - 1
	}

	segments := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		s := steps[i]
		segments = append(segments, fmt.Sprintf("#%d %s/%s (%s)", s.index, s.service, s.op, s.region))
	}

	return "destructive commands switch back to a previous region (zig-zag ordering). Group teardown commands by region to reduce cross-region failures; around " + strings.Join(segments, " -> ")
}

func isDestructiveOperationForRegionOrdering(op string) bool {
	op = strings.ToLower(strings.TrimSpace(op))
	if op == "" {
		return false
	}
	return strings.HasPrefix(op, "delete") ||
		strings.HasPrefix(op, "remove") ||
		strings.HasPrefix(op, "terminate") ||
		strings.HasPrefix(op, "destroy") ||
		strings.HasPrefix(op, "detach") ||
		strings.HasPrefix(op, "disassociate")
}

func isGlobalAWSServiceForRegionOrdering(service string) bool {
	service = strings.ToLower(strings.TrimSpace(service))
	switch service {
	case "iam", "route53", "cloudfront", "organizations", "support":
		return true
	default:
		return false
	}
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
	awsDeleteArgs := buildAWSExecArgs(deleteArgs, opts, w)

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
		awsListArgs := buildAWSExecArgs(listArgs, opts, w)
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
			awsDetachArgs := buildAWSExecArgs(detachArgs, opts, w)
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
		awsListArgs := buildAWSExecArgs(listArgs, opts, w)
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
			awsDeleteArgs := buildAWSExecArgs(deleteArgs, opts, w)
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
		awsListArgs := buildAWSExecArgs(listArgs, opts, w)
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
			awsRemoveArgs := buildAWSExecArgs(removeArgs, opts, w)
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
		awsArgs := buildAWSExecArgs(args, opts, io.Discard)
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
		awsArgs := buildAWSExecArgs(args, opts, io.Discard)
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
		awsArgs := buildAWSExecArgs(args, opts, io.Discard)
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
	awsArgs := buildAWSExecArgs(args, opts, io.Discard)
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
	awsCodeArgs := buildAWSExecArgs(codeArgs2, opts, w)
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
		awsCfgArgs := buildAWSExecArgs(configArgs, opts, w)
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

	isEC2RunInstances := len(args) >= 2 && strings.EqualFold(strings.TrimSpace(args[0]), "ec2") && strings.EqualFold(strings.TrimSpace(args[1]), "run-instances")

	for i, a := range args {
		if isEC2RunInstances {
			if strings.EqualFold(strings.TrimSpace(a), "--user-data") {
				continue
			}
			if i > 0 && strings.EqualFold(strings.TrimSpace(args[i-1]), "--user-data") {
				continue
			}
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a)), "--user-data=") {
				continue
			}
		}

		lower := strings.ToLower(a)
		if strings.Contains(lower, ";") || strings.Contains(lower, "|") || strings.Contains(lower, "&&") || strings.Contains(lower, "||") {
			return fmt.Errorf("shell operators are not allowed")
		}
	}

	if allowDestructive {
		return nil
	}

	service := strings.ToLower(strings.TrimSpace(args[0]))
	op := ""
	if len(args) > 1 {
		op = strings.ToLower(strings.TrimSpace(args[1]))
	}

	if strings.HasPrefix(op, "delete") || strings.HasPrefix(op, "terminate") || strings.HasPrefix(op, "remove") || strings.HasPrefix(op, "destroy") {
		return fmt.Errorf("destructive operation is blocked: %s %s", service, op)
	}

	return nil
}

// inferSGBindings generates dynamic placeholder bindings from a security group name.
// e.g., "lambdatron-rds-sg" -> SG_RDS, SG_RDS_ID, RdsSgId, etc.
func inferSGBindings(groupName, groupID string, bindings map[string]string) {
	if groupName == "" || groupID == "" {
		return
	}

	// Normalize: remove common suffixes, lowercase, split on hyphens
	name := strings.ToLower(groupName)
	name = strings.TrimSuffix(name, "-sg")
	name = strings.TrimSuffix(name, "-security-group")

	parts := strings.Split(name, "-")

	// Find meaningful keywords
	keywords := []string{}
	skipWords := map[string]bool{"sg": true, "security": true, "group": true, "new": true, "v2": true, "v3": true}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || skipWords[p] {
			continue
		}
		keywords = append(keywords, p)
	}

	// Generate binding variations for each keyword
	for _, kw := range keywords {
		upper := strings.ToUpper(kw)
		title := strings.Title(kw)

		// SG_RDS, SG_RDS_ID, SG_LAMBDA, etc.
		bindings["SG_"+upper] = groupID
		bindings["SG_"+upper+"_ID"] = groupID

		// RdsSgId, LambdaSgId (camelCase variants the LLM might use)
		bindings[title+"SgId"] = groupID
		bindings[title+"_SG_ID"] = groupID

		// <Ec2SgId>, <RdsSgId> style
		bindings[upper+"_SG_ID"] = groupID
		bindings[upper+"_SG"] = groupID
	}

	// If multiple keywords, try combinations: "lambdatron-db" -> SG_LAMBDATRON_DB
	if len(keywords) >= 2 {
		combined := strings.ToUpper(strings.Join(keywords, "_"))
		bindings["SG_"+combined] = groupID
		bindings["SG_"+combined+"_ID"] = groupID
	}
}

// inferSubnetBindings generates dynamic placeholder bindings from subnet creation.
func inferSubnetBindings(subnetID, az string, bindings map[string]string) {
	if subnetID == "" {
		return
	}

	// Sequential: SUBNET_1, SUBNET_2, etc.
	for i := 1; i <= 10; i++ {
		k := fmt.Sprintf("SUBNET_%d", i)
		if strings.TrimSpace(bindings[k]) == "" {
			bindings[k] = subnetID
			bindings[fmt.Sprintf("SUBNET_%d_ID", i)] = subnetID
			break
		}
	}

	// AZ-based: if AZ contains "a" -> SUBNET_A, etc.
	if az != "" {
		azLetter := strings.ToUpper(string(az[len(az)-1]))
		if azLetter >= "A" && azLetter <= "F" {
			bindings["SUBNET_"+azLetter] = subnetID
			bindings["SUBNET_"+azLetter+"_ID"] = subnetID
		}
	}
}

// inferAPIGatewayBindings generates dynamic placeholder bindings for API Gateway.
func inferAPIGatewayBindings(apiID string, bindings map[string]string) {
	if apiID == "" {
		return
	}
	bindings["API_ID"] = apiID
	bindings["APIGW_ID"] = apiID
	bindings["HTTP_API_ID"] = apiID
}

// inferLambdaBindings generates dynamic placeholder bindings for Lambda functions.
func inferLambdaBindings(arn string, bindings map[string]string) {
	if arn == "" {
		return
	}
	bindings["LAMBDA_ARN"] = arn
	bindings["FUNCTION_ARN"] = arn

	// Extract function name from ARN
	parts := strings.Split(arn, ":")
	if len(parts) >= 7 {
		fname := parts[len(parts)-1]
		upper := strings.ToUpper(strings.ReplaceAll(fname, "-", "_"))
		bindings[upper+"_ARN"] = arn
		bindings["LAMBDA_"+upper+"_ARN"] = arn
	}
}

// inferIntegrationBindings generates dynamic placeholder bindings for API Gateway integrations.
func inferIntegrationBindings(integrationID string, bindings map[string]string) {
	if integrationID == "" {
		return
	}
	bindings["INTEGRATION_ID"] = integrationID
	bindings["APIGW_INTEGRATION_ID"] = integrationID
}

// inferRouteBindings generates dynamic bindings for API Gateway routes.
func inferRouteBindings(routeID string, bindings map[string]string) {
	if routeID == "" {
		return
	}
	bindings["ROUTE_ID"] = routeID
	bindings["APIGW_ROUTE_ID"] = routeID
}

// inferStageBindings generates dynamic bindings for API Gateway stages.
func inferStageBindings(stageName string, bindings map[string]string) {
	if stageName == "" {
		return
	}
	bindings["STAGE_NAME"] = stageName
	bindings["APIGW_STAGE"] = stageName
}

// inferRDSBindings generates dynamic bindings for RDS instances.
func inferRDSBindings(id, endpoint, arn string, bindings map[string]string) {
	if id != "" {
		bindings["RDS_INSTANCE_ID"] = id
		bindings["DB_INSTANCE_ID"] = id
		bindings["RDS_ID"] = id
		upper := strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
		bindings["RDS_"+upper] = id
	}
	if endpoint != "" {
		bindings["RDS_ENDPOINT"] = endpoint
		bindings["DB_ENDPOINT"] = endpoint
		bindings["DB_HOST"] = endpoint
	}
	if arn != "" {
		bindings["RDS_ARN"] = arn
		bindings["DB_INSTANCE_ARN"] = arn
	}
}

// inferRDSClusterBindings generates dynamic bindings for RDS clusters (Aurora).
func inferRDSClusterBindings(id, endpoint, arn string, bindings map[string]string) {
	if id != "" {
		bindings["RDS_CLUSTER_ID"] = id
		bindings["DB_CLUSTER_ID"] = id
		bindings["CLUSTER_ID"] = id
	}
	if endpoint != "" {
		bindings["RDS_CLUSTER_ENDPOINT"] = endpoint
		bindings["CLUSTER_ENDPOINT"] = endpoint
		bindings["DB_HOST"] = endpoint
	}
	if arn != "" {
		bindings["RDS_CLUSTER_ARN"] = arn
		bindings["CLUSTER_ARN"] = arn
	}
}

// inferDBSubnetGroupBindings generates dynamic bindings for RDS subnet groups.
func inferDBSubnetGroupBindings(name, arn string, bindings map[string]string) {
	if name != "" {
		bindings["DB_SUBNET_GROUP"] = name
		bindings["RDS_SUBNET_GROUP"] = name
		bindings["DB_SUBNET_GROUP_NAME"] = name
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		bindings["DB_SUBNET_GROUP_"+upper] = name
	}
	if arn != "" {
		bindings["DB_SUBNET_GROUP_ARN"] = arn
	}
}

// inferECSClusterBindings generates dynamic bindings for ECS clusters.
func inferECSClusterBindings(name, arn string, bindings map[string]string) {
	if name != "" {
		bindings["ECS_CLUSTER"] = name
		bindings["ECS_CLUSTER_NAME"] = name
		bindings["CLUSTER_NAME"] = name
	}
	if arn != "" {
		bindings["ECS_CLUSTER_ARN"] = arn
	}
}

// inferECSServiceBindings generates dynamic bindings for ECS services.
func inferECSServiceBindings(name, arn string, bindings map[string]string) {
	if name != "" {
		bindings["ECS_SERVICE"] = name
		bindings["ECS_SERVICE_NAME"] = name
		bindings["SERVICE_NAME"] = name
	}
	if arn != "" {
		bindings["ECS_SERVICE_ARN"] = arn
		bindings["SERVICE_ARN"] = arn
	}
}

// inferTaskDefBindings generates dynamic bindings for ECS task definitions.
func inferTaskDefBindings(arn string, bindings map[string]string) {
	if arn == "" {
		return
	}
	bindings["TASK_DEF_ARN"] = arn
	bindings["TASK_DEFINITION_ARN"] = arn
	bindings["ECS_TASK_DEF_ARN"] = arn

	// Extract family:revision from ARN
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		familyRev := parts[len(parts)-1]
		bindings["TASK_DEFINITION"] = familyRev
		if idx := strings.LastIndex(familyRev, ":"); idx > 0 {
			bindings["TASK_FAMILY"] = familyRev[:idx]
		}
	}
}

// inferECRBindings generates dynamic bindings for ECR repositories.
func inferECRBindings(name, uri, arn string, bindings map[string]string) {
	if name != "" {
		bindings["ECR_REPO"] = name
		bindings["ECR_REPO_NAME"] = name
		bindings["REPO_NAME"] = name
	}
	if uri != "" {
		bindings["ECR_URI"] = uri
		bindings["ECR_REPO_URI"] = uri
		bindings["REPOSITORY_URI"] = uri
	}
	if arn != "" {
		bindings["ECR_ARN"] = arn
	}
}

// inferSNSBindings generates dynamic bindings for SNS topics.
func inferSNSBindings(arn string, bindings map[string]string) {
	if arn == "" {
		return
	}
	bindings["SNS_TOPIC_ARN"] = arn
	bindings["TOPIC_ARN"] = arn
	bindings["SNS_ARN"] = arn

	// Extract topic name from ARN
	parts := strings.Split(arn, ":")
	if len(parts) >= 6 {
		name := parts[len(parts)-1]
		bindings["SNS_TOPIC_NAME"] = name
		bindings["TOPIC_NAME"] = name
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		bindings["SNS_"+upper+"_ARN"] = arn
	}
}

// inferSQSBindings generates dynamic bindings for SQS queues.
func inferSQSBindings(url string, bindings map[string]string) {
	if url == "" {
		return
	}
	bindings["SQS_QUEUE_URL"] = url
	bindings["QUEUE_URL"] = url
	bindings["SQS_URL"] = url

	// Extract queue name from URL
	parts := strings.Split(url, "/")
	if len(parts) >= 1 {
		name := parts[len(parts)-1]
		bindings["SQS_QUEUE_NAME"] = name
		bindings["QUEUE_NAME"] = name
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		bindings["SQS_"+upper+"_URL"] = url
	}
}

// inferDynamoDBBindings generates dynamic bindings for DynamoDB tables.
func inferDynamoDBBindings(name, arn string, bindings map[string]string) {
	if name != "" {
		bindings["DYNAMODB_TABLE"] = name
		bindings["TABLE_NAME"] = name
		bindings["DDB_TABLE"] = name
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		bindings["DYNAMODB_"+upper] = name
	}
	if arn != "" {
		bindings["DYNAMODB_TABLE_ARN"] = arn
		bindings["TABLE_ARN"] = arn
	}
}

// inferSecretsBindings generates dynamic bindings for Secrets Manager secrets.
func inferSecretsBindings(name, arn string, bindings map[string]string) {
	if name != "" {
		bindings["SECRET_NAME"] = name
		bindings["SECRETS_NAME"] = name
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		bindings["SECRET_"+upper] = name
	}
	if arn != "" {
		bindings["SECRET_ARN"] = arn
		bindings["SECRETS_ARN"] = arn
	}
}

// inferS3Bindings generates dynamic bindings for S3 buckets.
func inferS3Bindings(bucket string, bindings map[string]string) {
	if bucket == "" {
		return
	}
	bindings["S3_BUCKET"] = bucket
	bindings["BUCKET_NAME"] = bucket
	bindings["S3_BUCKET_NAME"] = bucket
	upper := strings.ToUpper(strings.ReplaceAll(bucket, "-", "_"))
	bindings["S3_"+upper] = bucket
}

// inferElastiCacheBindings generates dynamic bindings for ElastiCache clusters.
func inferElastiCacheBindings(id, arn string, bindings map[string]string) {
	if id != "" {
		bindings["CACHE_CLUSTER_ID"] = id
		bindings["ELASTICACHE_ID"] = id
		bindings["REDIS_ID"] = id
	}
	if arn != "" {
		bindings["CACHE_CLUSTER_ARN"] = arn
		bindings["ELASTICACHE_ARN"] = arn
	}
}

// inferElastiCacheReplicationBindings generates dynamic bindings for ElastiCache replication groups.
func inferElastiCacheReplicationBindings(id, endpoint, arn string, bindings map[string]string) {
	if id != "" {
		bindings["REPLICATION_GROUP_ID"] = id
		bindings["REDIS_CLUSTER_ID"] = id
	}
	if endpoint != "" {
		bindings["REDIS_ENDPOINT"] = endpoint
		bindings["CACHE_ENDPOINT"] = endpoint
	}
	if arn != "" {
		bindings["REPLICATION_GROUP_ARN"] = arn
	}
}

// inferEventBridgeBindings generates dynamic bindings for EventBridge rules.
func inferEventBridgeBindings(arn string, bindings map[string]string) {
	if arn == "" {
		return
	}
	bindings["EVENTBRIDGE_RULE_ARN"] = arn
	bindings["EVENTS_RULE_ARN"] = arn
	bindings["RULE_ARN"] = arn

	// Extract rule name from ARN
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		name := parts[len(parts)-1]
		bindings["RULE_NAME"] = name
		bindings["EVENT_RULE_NAME"] = name
	}
}

// inferStepFunctionBindings generates dynamic bindings for Step Functions state machines.
func inferStepFunctionBindings(arn string, bindings map[string]string) {
	if arn == "" {
		return
	}
	bindings["STATE_MACHINE_ARN"] = arn
	bindings["SFN_ARN"] = arn
	bindings["STEP_FUNCTION_ARN"] = arn

	// Extract name from ARN
	parts := strings.Split(arn, ":")
	if len(parts) >= 7 {
		name := parts[len(parts)-1]
		bindings["STATE_MACHINE_NAME"] = name
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		bindings["SFN_"+upper+"_ARN"] = arn
	}
}

// inferCognitoPoolBindings generates dynamic bindings for Cognito user pools.
func inferCognitoPoolBindings(id, arn string, bindings map[string]string) {
	if id != "" {
		bindings["USER_POOL_ID"] = id
		bindings["COGNITO_POOL_ID"] = id
		bindings["COGNITO_USER_POOL_ID"] = id
	}
	if arn != "" {
		bindings["USER_POOL_ARN"] = arn
		bindings["COGNITO_POOL_ARN"] = arn
	}
}

// inferCognitoClientBindings generates dynamic bindings for Cognito user pool clients.
func inferCognitoClientBindings(clientID string, bindings map[string]string) {
	if clientID == "" {
		return
	}
	bindings["USER_POOL_CLIENT_ID"] = clientID
	bindings["COGNITO_CLIENT_ID"] = clientID
	bindings["CLIENT_ID"] = clientID
}

// inferLogGroupBindings generates dynamic bindings for CloudWatch log groups.
func inferLogGroupBindings(name string, bindings map[string]string) {
	if name == "" {
		return
	}
	bindings["LOG_GROUP_NAME"] = name
	bindings["LOG_GROUP"] = name
	bindings["CW_LOG_GROUP"] = name

	// Extract key component for dynamic naming
	parts := strings.Split(strings.TrimPrefix(name, "/aws/"), "/")
	if len(parts) >= 1 {
		upper := strings.ToUpper(strings.ReplaceAll(parts[len(parts)-1], "-", "_"))
		bindings["LOG_GROUP_"+upper] = name
	}
}
