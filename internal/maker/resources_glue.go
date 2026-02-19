package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

var lambdaArnMissingRegionRe = regexp.MustCompile(`^arn:([^:]+):lambda:(\d{12}):function:(.+)$`)
var ec2InstanceIDRe = regexp.MustCompile(`\bi-[0-9a-f]{8,32}\b`)

func extractAPIGatewayV2IDFromNotFound(output string) string {
	lower := strings.ToLower(output)
	idx := strings.Index(lower, "invalid api identifier specified")
	if idx < 0 {
		return ""
	}
	frag := strings.TrimSpace(output[idx+len("invalid api identifier specified"):])
	// Expected forms:
	// - "549955691027:allellp9mg"
	// - "549955691027:allellp9mg\n..."
	frag = strings.TrimSpace(strings.SplitN(frag, "\n", 2)[0])
	frag = strings.Trim(frag, ": ")
	if frag == "" {
		return ""
	}
	parts := strings.Split(frag, ":")
	id := strings.TrimSpace(parts[len(parts)-1])
	// API Gateway API IDs are usually 10 chars; avoid being too strict.
	if len(id) < 6 {
		return ""
	}
	return id
}

func extractEC2InstanceIDs(s string) []string {
	matches := ec2InstanceIDRe.FindAllString(s, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func dropEC2TerminateInstanceIDs(args []string, drop map[string]bool) (rewritten []string, changed bool, remaining int) {
	if len(args) < 2 {
		return nil, false, 0
	}
	out := append([]string{}, args...)

	for i := 0; i < len(out); i++ {
		if out[i] == "--instance-ids" {
			start := i + 1
			end := start
			for end < len(out) && !strings.HasPrefix(out[end], "--") {
				end++
			}
			keep := make([]string, 0, end-start)
			for _, id := range out[start:end] {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				if drop[id] {
					changed = true
					continue
				}
				keep = append(keep, id)
			}
			remaining = len(keep)
			if remaining == 0 {
				// Remove flag + values.
				out = append(out[:i], out[end:]...)
				return out, true, 0
			}
			out = append(append(out[:start], keep...), out[end:]...)
			return out, changed, remaining
		}
		if strings.HasPrefix(out[i], "--instance-ids=") {
			val := strings.TrimPrefix(out[i], "--instance-ids=")
			parts := strings.FieldsFunc(val, func(r rune) bool {
				return r == ',' || r == ' '
			})
			keep := make([]string, 0, len(parts))
			for _, id := range parts {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				if drop[id] {
					changed = true
					continue
				}
				keep = append(keep, id)
			}
			remaining = len(keep)
			if remaining == 0 {
				// Remove the flag entirely.
				out = append(out[:i], out[i+1:]...)
				return out, true, 0
			}
			if len(keep) != len(parts) {
				changed = true
			}
			out[i] = "--instance-ids=" + strings.Join(keep, ",")
			return out, changed, remaining
		}
	}

	return nil, false, 0
}

func maybeRewriteAndRetry(ctx context.Context, opts ExecOptions, args []string, awsArgs []string, stdinBytes []byte, failure AWSFailure, output string, bindings map[string]string) (bool, error) {
	// EC2 subnet CIDR: plan may emit a CIDR not inside the target VPC (e.g. 10.0.0.0/16 against default 172.31.0.0/16).
	// On InvalidSubnet.Range, pick a free /24 inside the VPC, rewrite --cidr-block, retry, and learn bindings.
	if args0(args) == "ec2" && args1(args) == "create-subnet" {
		lower := strings.ToLower(output)
		if failure.Code == "InvalidSubnet.Range" || strings.Contains(lower, "invalidsubnet.range") || strings.Contains(lower, "is invalid") && strings.Contains(lower, "cidr") {
			out2, rewritten, err := remediateEC2CreateSubnetInvalidRangeAndRetry(ctx, opts, args, stdinBytes, opts.Writer)
			if err != nil {
				return true, err
			}
			learnPlanBindings(rewritten, out2, bindings)
			return true, nil
		}
	}

	// EC2 route table placeholders: plans sometimes reference a private route table ID that was never created.
	// If create-route/associate-route-table fails due to an invalid route-table-id, create one, bind it, and retry.
	if args0(args) == "ec2" && (args1(args) == "create-route" || args1(args) == "associate-route-table") {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "invalidroutetableid") || (strings.Contains(lower, "route table") && (strings.Contains(lower, "does not exist") || strings.Contains(lower, "not found") || strings.Contains(lower, "malformed"))) {
			rtID := strings.TrimSpace(flagValue(args, "--route-table-id"))
			// If it's already a real ID, don't create extra route tables.
			if strings.HasPrefix(rtID, "rtb-") {
				// no-op
			} else {
				vpcID := ""
				if args1(args) == "create-route" {
					natID := strings.TrimSpace(flagValue(args, "--nat-gateway-id"))
					if natID != "" {
						vpcID, _ = describeNatGatewayVpcID(ctx, opts, natID)
					}
				}
				if vpcID == "" {
					subnetID := strings.TrimSpace(flagValue(args, "--subnet-id"))
					if subnetID != "" {
						vpcID, _ = describeSubnetVpcID(ctx, opts, subnetID)
					}
				}
				if vpcID != "" {
					create := []string{"ec2", "create-route-table", "--vpc-id", vpcID}
					createAWSArgs := append(append([]string{}, create...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: creating missing route table then retry (%s=%s)\n", "vpc", vpcID)
					outRT, errRT := runAWSCommandStreaming(ctx, createAWSArgs, nil, opts.Writer)
					if errRT != nil {
						return true, errRT
					}
					// Prefer binding as private RT.
					var obj map[string]any
					if err := json.Unmarshal([]byte(outRT), &obj); err == nil {
						newID := deepString(obj, "RouteTable", "RouteTableId")
						if strings.HasPrefix(newID, "rtb-") {
							if strings.TrimSpace(bindings["RT_PRIVATE_ID"]) == "" {
								bindings["RT_PRIVATE_ID"] = newID
							}
							if strings.TrimSpace(bindings["RT_PRIV"]) == "" {
								bindings["RT_PRIV"] = newID
							}
							if strings.TrimSpace(bindings["RT_PRIV_ID"]) == "" {
								bindings["RT_PRIV_ID"] = newID
							}
							if strings.TrimSpace(bindings["RT_PRIVATE"]) == "" {
								bindings["RT_PRIVATE"] = newID
							}
							// Rewrite the failing call to use the created route table.
							rewritten := setFlagValue(args, "--route-table-id", newID)
							rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
							if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
								return true, err
							}
							return true, nil
						}
					}
				}
			}
		}
	}

	// Generic service-role permission fix: if an AWS API reports that a ROLE (not the caller)
	// lacks permission, add a minimal inline policy to the role and retry.
	//
	// This is intentionally narrow: it only triggers when we can identify a role ARN and a
	// recognizable action/resource family.
	if ok, err := maybeFixRolePermissionAndRetry(ctx, opts, args, awsArgs, stdinBytes, failure, output); ok {
		return true, err
	}

	// CloudWatch Logs prerequisites + idempotency.
	// Many operations fail if the log group doesn't exist yet; create it and retry.
	if args0(args) == "logs" {
		op := args1(args)
		lower := strings.ToLower(output)

		// Idempotency: create-log-stream already exists.
		if op == "create-log-stream" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict ||
				strings.Contains(lower, "resourcealreadyexistsexception") ||
				(strings.Contains(lower, "already exists") && strings.Contains(lower, "log stream")) {
				return true, nil
			}
		}

		// Missing log group: create it, then retry.
		if op == "create-log-stream" || op == "put-retention-policy" || op == "put-subscription-filter" || op == "put-metric-filter" {
			missingGroup := failure.Category == FailureNotFound || failure.Category == FailureValidation ||
				strings.Contains(lower, "resourcenotfoundexception") ||
				(strings.Contains(lower, "log group") && (strings.Contains(lower, "does not exist") || strings.Contains(lower, "not found")))
			if missingGroup {
				lg := strings.TrimSpace(flagValue(args, "--log-group-name"))
				if lg != "" {
					create := []string{"logs", "create-log-group", "--log-group-name", lg, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: create log group then retry (logGroup=%s)\n", lg)
					// Best-effort: create-log-group is effectively idempotent.
					_, _ = runAWSCommandStreaming(ctx, create, nil, opts.Writer)
					if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
						return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
					}); err != nil {
						return true, err
					}
					return true, nil
				}
			}
		}
	}

	// EventBridge / Scheduler / Pipes: common idempotency + propagation retries.
	// These services frequently fail due to role propagation and cross-resource settling.
	if args0(args) == "events" || args0(args) == "scheduler" || args0(args) == "pipes" {
		lower := strings.ToLower(output)

		// Scheduler create-schedule: already exists -> update-schedule.
		if args0(args) == "scheduler" && args1(args) == "create-schedule" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				rewritten := append([]string{}, args...)
				rewritten[1] = "update-schedule"
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: scheduler create-schedule exists; using update-schedule\n")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// Pipes create-pipe: already exists -> update-pipe.
		if args0(args) == "pipes" && args1(args) == "create-pipe" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				rewritten := append([]string{}, args...)
				rewritten[1] = "update-pipe"
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: pipes create-pipe exists; using update-pipe\n")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// EventBridge: targets/rules often race with IAM/service readiness.
		if args0(args) == "events" {
			op := args1(args)
			if (op == "put-targets" || op == "put-rule" || op == "create-api-destination" || op == "create-connection") &&
				(failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled || strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "not found")) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry events %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// General propagation retry when these services say something doesn't exist yet.
		if failure.Category == FailureNotFound || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry %s %s after propagation\n", args0(args), args1(args))
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// SNS: create-topic is naturally idempotent, but AWS can still return transient/not-found propagation errors.
	if args0(args) == "sns" {
		op := args1(args)
		lower := strings.ToLower(output)

		// Treat already-exists/conflict as idempotent success.
		if op == "create-topic" || op == "subscribe" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				return true, nil
			}
		}

		// Topic propagation: subscribe / set attributes can race right after create-topic.
		if op == "subscribe" || op == "set-topic-attributes" {
			if failure.Category == FailureNotFound || failure.Category == FailureThrottled || failure.Category == FailureConflict ||
				strings.Contains(lower, "notfound") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry sns %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// CloudWatch alarms: put-metric-alarm / put-composite-alarm are upserts.
	// Retry on throttling/transients and on dependency propagation.
	if args0(args) == "cloudwatch" {
		op := args1(args)
		lower := strings.ToLower(output)
		if op == "put-metric-alarm" || op == "put-composite-alarm" {
			if failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
				failure.Category == FailureNotFound || failure.Category == FailureConflict ||
				strings.Contains(lower, "resource not found") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry cloudwatch %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// EC2/VPC building blocks: handle eventual consistency + "already associated" + route replace.
	if args0(args) == "ec2" {
		op := args1(args)
		lower := strings.ToLower(output)

		// attach-internet-gateway: if the VPC already has an IGW attached, detect the existing IGW,
		// bind it for later commands, and continue.
		if op == "attach-internet-gateway" && (strings.Contains(lower, "already has an internet gateway attached") || strings.Contains(lower, "has an internet gateway attached")) {
			vpcID := strings.TrimSpace(flagValue(args, "--vpc-id"))
			if vpcID != "" {
				igwID, _ := findAttachedInternetGatewayForVPC(ctx, opts, vpcID)
				if igwID != "" && bindings != nil {
					bindings["IGW_ID"] = igwID
					bindings["IGW"] = igwID
				}
				// Best-effort cleanup: if this attach used a newly created IGW that isn't the attached one, delete it.
				created := strings.TrimSpace(flagValue(args, "--internet-gateway-id"))
				if created != "" && igwID != "" && created != igwID {
					del := []string{"ec2", "delete-internet-gateway", "--internet-gateway-id", created, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
					_, _ = runAWSCommandStreaming(ctx, del, nil, io.Discard)
				}
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: vpc already has igw attached; using existing igw and continuing (vpc=%s)\n", vpcID)
				return true, nil
			}
		}

		// Teardown: terminate-instances can fail if one of the instance IDs is already gone.
		// Drop missing IDs and retry until the remaining instances are terminated (or none remain).
		if opts.Destroyer && op == "terminate-instances" {
			currentArgs := append([]string{}, args...)
			currentOut := output
			currentFailure := failure
			for iter := 1; iter <= 6; iter++ {
				curLower := strings.ToLower(currentOut)
				if currentFailure.Code != "InvalidInstanceID.NotFound" && !strings.Contains(curLower, "invalidinstanceid.notfound") {
					break
				}
				missingIDs := extractEC2InstanceIDs(currentOut)
				if len(missingIDs) == 0 {
					break
				}
				drop := make(map[string]bool, len(missingIDs))
				for _, id := range missingIDs {
					drop[id] = true
				}
				rewritten, changed, remaining := dropEC2TerminateInstanceIDs(currentArgs, drop)
				if !changed {
					break
				}
				if remaining == 0 {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] note: ec2 terminate-instances: all instance ids already gone; skipping\n")
					return true, nil
				}
				currentArgs = rewritten
				rewrittenAWSArgs := append(append([]string{}, currentArgs...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: ec2 terminate-instances dropping missing instance ids then retry\n")
				out2, err2 := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer)
				if err2 == nil {
					return true, nil
				}
				currentOut = out2
				currentFailure = classifyAWSFailure(currentArgs, currentOut)
			}
		}

		// associate-vpc-cidr-block: if the chosen private range is restricted (wrong RFC1918 block),
		// pick an additional CIDR in the same private range as the VPC and retry.
		if op == "associate-vpc-cidr-block" {
			if failure.Code == "InvalidVpc.Range" || strings.Contains(lower, "invalidvpc.range") || (strings.Contains(lower, "cidr") && strings.Contains(lower, "restricted")) {
				if err := remediateEC2AssociateVpcCidrBlockInvalidRangeAndRetry(ctx, opts, args, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// create-route: RouteAlreadyExists -> replace-route.
		if op == "create-route" {
			if failure.Category == FailureAlreadyExists || strings.Contains(lower, "routealreadyexists") || strings.Contains(lower, "route already exists") {
				rewritten := append([]string{}, args...)
				rewritten[1] = "replace-route"
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: ec2 create-route exists; using replace-route\n")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// Idempotency: attaching/associating something already associated.
		if op == "attach-internet-gateway" || op == "associate-route-table" {
			if failure.Category == FailureConflict || failure.Category == FailureAlreadyExists ||
				strings.Contains(lower, "alreadyassociated") || strings.Contains(lower, "already associated") || strings.Contains(lower, "resource.alreadyassociated") {
				return true, nil
			}
		}

		// Ordering/propagation: many Invalid*NotFound errors right after create.
		if op == "create-vpc" || op == "create-subnet" || op == "create-route-table" || op == "create-route" || op == "replace-route" ||
			op == "associate-route-table" || op == "create-nat-gateway" || op == "allocate-address" || op == "create-internet-gateway" || op == "attach-internet-gateway" {
			if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled ||
				strings.Contains(lower, "invalidvpcid.notfound") || strings.Contains(lower, "invalidsubnetid.notfound") ||
				strings.Contains(lower, "invalidroutetableid.notfound") || strings.Contains(lower, "invalidinternetgatewayid.notfound") ||
				strings.Contains(lower, "invalidallocationid.notfound") || strings.Contains(lower, "not found") ||
				strings.Contains(lower, "incorrectstate") || strings.Contains(lower, "dependencyviolation") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry ec2 %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// CloudFormation create-stack: if subnets in template are outside the VPC CIDR ranges,
	// rewrite subnet CIDRs to free /24s inside the VPC and retry.
	if args0(args) == "cloudformation" && args1(args) == "create-stack" {
		lower := strings.ToLower(output)
		// Idempotency: stack already exists -> update-stack.
		if failure.Category == FailureAlreadyExists || strings.Contains(lower, "already exists") {
			rewritten := append([]string{}, args...)
			rewritten[1] = "update-stack"
			rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: cloudformation create-stack exists; using update-stack\n")
			if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
		if strings.Contains(lower, "cidr") && (strings.Contains(lower, "subnet") || strings.Contains(lower, "vpc")) {
			if err := remediateCloudFormationTemplateSubnetCIDRsAndRetry(ctx, opts, args, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// CloudFormation update-stack: same CIDR remediation.
	if args0(args) == "cloudformation" && args1(args) == "update-stack" {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "cidr") && (strings.Contains(lower, "subnet") || strings.Contains(lower, "vpc")) {
			if err := remediateCloudFormationTemplateSubnetCIDRsAndRetry(ctx, opts, args, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// ACM: describe can race right after request-certificate, and many follow-ons need time.
	if args0(args) == "acm" {
		lower := strings.ToLower(output)
		if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled ||
			strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry acm %s after propagation\n", args1(args))
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// WAFv2: duplicate items and optimistic locks happen frequently; retry on locks/unavailable.
	if args0(args) == "wafv2" {
		lower := strings.ToLower(output)
		if failure.Category == FailureAlreadyExists || strings.Contains(lower, "wafduplicateitem") || strings.Contains(lower, "duplicate") {
			return true, nil
		}
		if failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
			strings.Contains(lower, "wafoptimisticlock") || strings.Contains(lower, "wafunavailableentity") || strings.Contains(lower, "wafinternalerror") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry wafv2 %s after lock/propagation\n", args1(args))
			if err := retryWithBackoff(ctx, opts.Writer, 7, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// EFS: file systems and mount targets are async.
	if args0(args) == "efs" {
		op := args1(args)
		lower := strings.ToLower(output)

		// Idempotency: mount target already exists.
		if op == "create-mount-target" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "mounttargetconflict") || strings.Contains(lower, "already exists") {
				return true, nil
			}
			fsID := strings.TrimSpace(flagValue(args, "--file-system-id"))
			if fsID != "" && (failure.Category == FailureNotFound || failure.Category == FailureConflict ||
				strings.Contains(lower, "filesystemnotfound") || strings.Contains(lower, "incorrectfilesystemlifecycle") || strings.Contains(lower, "incorrect file system") ||
				strings.Contains(lower, "not found")) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: wait for efs file system available then retry (fs=%s)\n", fsID)
				_ = waitForEFSFileSystemAvailable(ctx, opts, fsID, opts.Writer)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
		if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled || strings.Contains(lower, "not found") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry efs %s after propagation\n", op)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// OpenSearch: domains take time and many operations fail while processing.
	if args0(args) == "opensearch" {
		op := args1(args)
		lower := strings.ToLower(output)
		if op == "create-domain" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				return true, nil
			}
		}
		domain := strings.TrimSpace(flagValue(args, "--domain-name"))
		if domain == "" {
			domain = strings.TrimSpace(flagValue(args, "--domain"))
		}
		if domain != "" && (failure.Category == FailureConflict || failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
			strings.Contains(lower, "processing") || strings.Contains(lower, "in progress") || strings.Contains(lower, "is currently being modified")) {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: wait for opensearch domain not processing then retry (domain=%s)\n", domain)
			_ = waitForOpenSearchDomainReady(ctx, opts, domain, opts.Writer)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
		if failure.Category == FailureNotFound || strings.Contains(lower, "not found") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry opensearch %s after propagation\n", op)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// MSK: clusters are slow; retry on in-progress states.
	if args0(args) == "kafka" {
		op := args1(args)
		lower := strings.ToLower(output)
		if op == "create-cluster" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				return true, nil
			}
		}
		arn := strings.TrimSpace(flagValue(args, "--cluster-arn"))
		if arn != "" && (failure.Category == FailureConflict || failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
			strings.Contains(lower, "in progress") || strings.Contains(lower, "conflict") || strings.Contains(lower, "resource is currently") || strings.Contains(lower, "badrequestexception")) {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: wait for msk cluster active then retry\n")
			_ = waitForMSKClusterActive(ctx, opts, arn, opts.Writer)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
		if failure.Category == FailureNotFound || strings.Contains(lower, "not found") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry kafka %s after propagation\n", op)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// Cognito: create calls don't upsert; treat already-exists as idempotent and retry propagation.
	if args0(args) == "cognito-idp" {
		op := args1(args)
		lower := strings.ToLower(output)
		if strings.HasPrefix(op, "create-") {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				return true, nil
			}
		}
		if failure.Category == FailureNotFound || failure.Category == FailureThrottled || failure.Category == FailureConflict || strings.Contains(lower, "not found") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry cognito-idp %s after propagation\n", op)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// Glue + Athena: database/table/workgroup creation often needs retries and should be idempotent.
	if args0(args) == "glue" || args0(args) == "athena" {
		lower := strings.ToLower(output)
		if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") || strings.Contains(lower, "entityalreadyexists") {
			return true, nil
		}
		if failure.Category == FailureNotFound || failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
			strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry %s %s after propagation\n", args0(args), args1(args))
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// API Gateway v2: create-stage already exists -> update-stage.
	if args0(args) == "apigatewayv2" && args1(args) == "create-stage" {
		lower := strings.ToLower(output)
		if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
			rewritten := append([]string{}, args...)
			rewritten[1] = "update-stage"
			rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: apigatewayv2 create-stage exists; using update-stage\n")
			if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// ELBv2: propagation and ordering hiccups (target group / load balancer not ready).
	if args0(args) == "elbv2" {
		op := args1(args)
		lower := strings.ToLower(output)

		// Idempotency: name already exists.
		if op == "create-load-balancer" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "duplicateloadbalancername") || strings.Contains(lower, "already exists") {
				lbName := strings.TrimSpace(flagValue(args, "--name"))
				if lbName != "" {
					if lbArn, lbDNS, err := describeLoadBalancerByName(ctx, opts, lbName); err == nil {
						if strings.TrimSpace(lbArn) != "" {
							bindings["ALB_ARN"] = strings.TrimSpace(lbArn)
							_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: using existing load balancer ARN (name=%s)\n", lbName)
						}
						if strings.TrimSpace(lbDNS) != "" {
							bindings["ALB_DNS"] = strings.TrimSpace(lbDNS)
							bindings["ALB_DNS_NAME"] = strings.TrimSpace(lbDNS)
						}
					} else {
						_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: failed to resolve existing load balancer for %s: %v\n", lbName, err)
					}
				}
				return true, nil
			}
		}
		if op == "create-target-group" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "duplicatetargetgroupname") || strings.Contains(lower, "already exists") {
				tgName := strings.TrimSpace(flagValue(args, "--name"))
				if tgName != "" {
					if tgArn, err := describeTargetGroupArnByName(ctx, opts, tgName); err == nil && strings.TrimSpace(tgArn) != "" {
						bindings["TG_ARN"] = strings.TrimSpace(tgArn)
						_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: using existing target group ARN (name=%s)\n", tgName)
					} else if err != nil {
						_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: failed to resolve existing target group ARN for %s: %v\n", tgName, err)
					}
				}
				return true, nil
			}
		}

		if op == "create-listener" || op == "create-rule" || op == "register-targets" {
			// If the LB is still provisioning, wait for it to be active.
			lbArn := strings.TrimSpace(flagValue(args, "--load-balancer-arn"))
			if lbArn != "" && (strings.Contains(lower, "loadbalancernotfound") || strings.Contains(lower, "not found") || strings.Contains(lower, "provisioning") || strings.Contains(lower, "incorrectstate")) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: wait for elbv2 load balancer active then retry\n")
				_ = waitForELBv2LoadBalancerActive(ctx, opts, lbArn, opts.Writer)
			}

			// If this is a TLS listener and the certificate isn't ready, wait for ISSUED.
			if op == "create-listener" {
				if strings.Contains(lower, "certificate") && (strings.Contains(lower, "not found") || strings.Contains(lower, "must be") || strings.Contains(lower, "not valid") || strings.Contains(lower, "pending")) {
					if certArn := firstACMCertificateArnInArgs(args); certArn != "" {
						_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: wait for acm certificate ISSUED then retry (cert=%s)\n", certArn)
						if err := waitForACMCertificateIssued(ctx, opts, certArn, opts.Writer); err != nil {
							return true, err
						}
					}
				}
			}

			if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled ||
				strings.Contains(lower, "targetgroupnotfound") || strings.Contains(lower, "loadbalancernotfound") || strings.Contains(lower, "not found") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry elbv2 %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// Route53: changes are async; retries help with eventual consistency.
	if args0(args) == "route53" {
		op := args1(args)
		lower := strings.ToLower(output)

		// create-hosted-zone can have propagation and occasional throttling.
		if op == "create-hosted-zone" {
			if failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
				failure.Category == FailureConflict || strings.Contains(lower, "priorrequestnotcomplete") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry route53 create-hosted-zone after propagation\n")
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		if op == "change-resource-record-sets" {
			// If the batch tries to CREATE an RRset that already exists, rewrite to UPSERT and retry.
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict ||
				strings.Contains(lower, "invalidchangebatch") || strings.Contains(lower, "already exists") {
				if rewritten, ok := rewriteRoute53ChangeBatchCreateToUpsert(args, lower); ok {
					rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewrite route53 change-batch CREATE->UPSERT then retry\n")
					if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err == nil {
						return true, nil
					}
				}
			}

			if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled ||
				strings.Contains(lower, "nosuchhostedzone") || strings.Contains(lower, "not found") || strings.Contains(lower, "priorrequestnotcomplete") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry route53 change-resource-record-sets after propagation\n")
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// ECR: repository policy / lifecycle policy are upserts but can race with repo creation.
	if args0(args) == "ecr" {
		op := args1(args)
		lower := strings.ToLower(output)
		if op == "set-repository-policy" || op == "put-lifecycle-policy" {
			if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled || isTransientFailure(failure, output) ||
				strings.Contains(lower, "repositorynotfound") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry ecr %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// KMS: key state/propagation can lag across operations.
	if args0(args) == "kms" {
		lower := strings.ToLower(output)
		if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled ||
			strings.Contains(lower, "notfoundexception") || strings.Contains(lower, "invalidstate") || strings.Contains(lower, "pending") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry kms %s after propagation\n", args1(args))
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// Secrets Manager: replication/creation can race with immediate follow-on operations.
	if args0(args) == "secretsmanager" {
		lower := strings.ToLower(output)
		if failure.Category == FailureNotFound || failure.Category == FailureConflict || failure.Category == FailureThrottled ||
			strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "not found") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry secretsmanager %s after propagation\n", args1(args))
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// Bedrock idempotency: create-* may conflict on re-apply.
	if (args0(args) == "bedrock" || args0(args) == "bedrock-agent") && strings.HasPrefix(args1(args), "create-") {
		lower := strings.ToLower(output)
		if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict ||
			strings.Contains(lower, "already exists") || strings.Contains(lower, "conflictexception") || strings.Contains(lower, "resourceconflict") {
			return true, nil
		}
	}

	// AWS Batch: create-* operations are frequently re-applied; rewrite to update where supported,
	// and ensure required service-linked role exists.
	if args0(args) == "batch" {
		op := args1(args)
		lower := strings.ToLower(output)

		// Missing service-linked role for Batch.
		if strings.Contains(lower, "awsserviceroleforbatch") || strings.Contains(lower, "service-linked role") && strings.Contains(lower, "batch") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: create batch service-linked role then retry\n")
			slr := []string{"iam", "create-service-linked-role", "--aws-service-name", "batch.amazonaws.com", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
			_, _ = runAWSCommandStreaming(ctx, slr, nil, opts.Writer)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}

		// create-compute-environment already exists -> update-compute-environment.
		if op == "create-compute-environment" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				rewritten := append([]string{}, args...)
				rewritten[1] = "update-compute-environment"
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: batch create-compute-environment exists; using update-compute-environment\n")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// create-job-queue already exists -> update-job-queue.
		if op == "create-job-queue" {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				rewritten := append([]string{}, args...)
				rewritten[1] = "update-job-queue"
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: batch create-job-queue exists; using update-job-queue\n")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// SageMaker: many create-* resources are immutable (model/endpoint-config), so "already exists" is effectively idempotent.
	if args0(args) == "sagemaker" {
		op := args1(args)
		lower := strings.ToLower(output)
		if strings.HasPrefix(op, "create-") {
			if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "already exists") {
				// For endpoints, prefer update-endpoint if endpoint config is supplied.
				if op == "create-endpoint" {
					endpointName := strings.TrimSpace(flagValue(args, "--endpoint-name"))
					endpointConfig := strings.TrimSpace(flagValue(args, "--endpoint-config-name"))
					if endpointName != "" && endpointConfig != "" {
						rewritten := []string{"sagemaker", "update-endpoint", "--endpoint-name", endpointName, "--endpoint-config-name", endpointConfig}
						rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
						_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: sagemaker create-endpoint exists; using update-endpoint\n")
						if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
							return true, err
						}
						return true, nil
					}
				}
				return true, nil
			}
		}
	}

	// ECS glue: service-linked role + execution role permissions + propagation retries.
	if args0(args) == "ecs" {
		op := args1(args)
		lower := strings.ToLower(output)

		// Missing ECS service-linked role: create it then retry.
		if strings.Contains(lower, "service-linked role") &&
			(strings.Contains(lower, "awsserviceroleforecs") || strings.Contains(lower, "ecs.amazonaws.com") || strings.Contains(lower, "has not been created") || strings.Contains(lower, "does not exist")) {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: create ecs service-linked role then retry\n")
			slr := []string{"iam", "create-service-linked-role", "--aws-service-name", "ecs.amazonaws.com", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
			out2, err2 := runAWSCommandStreaming(ctx, slr, nil, opts.Writer)
			if err2 != nil {
				l2 := strings.ToLower(out2)
				// Best-effort idempotency: role already exists.
				if !(strings.Contains(l2, "has been taken") || strings.Contains(l2, "already exists") || strings.Contains(l2, "has been created") || strings.Contains(l2, "duplicate")) {
					return true, err2
				}
			}
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
		if strings.Contains(lower, "unable to assume the service linked role") && strings.Contains(lower, "ecs") {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: create ecs service-linked role then retry\n")
			slr := []string{"iam", "create-service-linked-role", "--aws-service-name", "ecs.amazonaws.com", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
			_, _ = runAWSCommandStreaming(ctx, slr, nil, opts.Writer)
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}

		// ECS cluster/service propagation: immediate follow-on calls can race.
		if (op == "create-service" || op == "run-task" || op == "update-service") && failure.Category == FailureNotFound {
			if strings.Contains(lower, "clusternotfound") || strings.Contains(lower, "cluster not found") || strings.Contains(lower, "servicenotfound") || strings.Contains(lower, "service not found") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry ecs %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// Task execution role missing ECR/Logs permissions: attach managed execution policy then retry.
		if (op == "create-service" || op == "run-task" || op == "update-service") && strings.Contains(lower, "execution role") && (strings.Contains(lower, "ecr") || strings.Contains(lower, "logs")) {
			taskDef := strings.TrimSpace(flagValue(args, "--task-definition"))
			if taskDef != "" {
				execRoleArn, err := ecsExecutionRoleArnForTaskDefinition(ctx, opts, taskDef)
				if err == nil {
					roleName := strings.TrimSpace(roleNameFromArn(strings.TrimSpace(execRoleArn)))
					if roleName != "" {
						policyArn := "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
						_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: attach AmazonECSTaskExecutionRolePolicy then retry (role=%s)\n", roleName)
						attach := []string{"iam", "attach-role-policy", "--role-name", roleName, "--policy-arn", policyArn, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
						_, _ = runAWSCommandStreaming(ctx, attach, nil, opts.Writer)
						if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
							return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
						}); err != nil {
							return true, err
						}
						return true, nil
					}
				}
			}
		}
	}

	// EKS network placeholders: planner may emit <SUBNET_ID_*> tokens.
	// Rewrite to real subnet IDs (inferred from provided SG VPC or default VPC) and retry.
	if args0(args) == "eks" && (args1(args) == "create-cluster" || args1(args) == "create-nodegroup") {
		lower := strings.ToLower(output)
		if hasSubnetPlaceholders(args) || strings.Contains(lower, "invalidsubnetid.notfound") || (strings.Contains(lower, "subnet id") && strings.Contains(lower, "does not exist")) {
			rewritten, ok, err := rewriteEKSSubnets(ctx, opts, args)
			if err != nil {
				return true, err
			}
			if ok {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewrite eks subnets to real subnet IDs then retry\n")
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// Generic transient retry (service hiccups / in-progress / timeouts).
	if isTransientFailure(failure, output) {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry after transient failure\n")
		out2, err := retryWithBackoffOutput(ctx, opts.Writer, 6, func() (string, error) {
			return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		})
		if err != nil {
			f2 := classifyAWSFailure(args, out2)
			if shouldIgnoreFailure(args, f2, out2) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] note: ignoring non-fatal error after transient retries\n")
				return true, nil
			}
			return true, err
		}
		return true, nil
	}

	// Generic throttling retry.
	if failure.Category == FailureThrottled {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry after throttling\n")
		out2, err := retryWithBackoffOutput(ctx, opts.Writer, 6, func() (string, error) {
			return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		})
		if err != nil {
			// Special-case: API Gateway v2 APIs incorrectly deleted via v1 command can be hidden behind throttling.
			// If the final error flips to v1 NotFound "Invalid API identifier specified", fall back to apigatewayv2 delete-api.
			if opts.Destroyer && args0(args) == "apigateway" && args1(args) == "delete-rest-api" {
				lower2 := strings.ToLower(out2)
				if strings.Contains(lower2, "invalid api identifier specified") {
					id := strings.TrimSpace(flagValue(args, "--rest-api-id"))
					if id == "" {
						id = strings.TrimSpace(flagValue(args, "--api-id"))
					}
					if id == "" {
						id = strings.TrimSpace(extractAPIGatewayV2IDFromNotFound(out2))
					}
					if id != "" {
						_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: throttled apigateway delete-rest-api ended notfound; trying apigatewayv2 delete-api (apiId=%s)\n", id)
						del := []string{"apigatewayv2", "delete-api", "--api-id", id, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
						out3, err3 := retryWithBackoffOutput(ctx, opts.Writer, 6, func() (string, error) {
							return runAWSCommandStreaming(ctx, del, stdinBytes, opts.Writer)
						})
						if err3 != nil {
							f3 := classifyAWSFailure([]string{"apigatewayv2", "delete-api"}, out3)
							if f3.Category == FailureNotFound || f3.Code == "NotFoundException" {
								return true, nil
							}
							return true, err3
						}
						return true, nil
					}
				}
			}

			f2 := classifyAWSFailure(args, out2)
			if shouldIgnoreFailure(args, f2, out2) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] note: ignoring non-fatal error after throttling retries\n")
				return true, nil
			}
			return true, err
		}
		return true, nil
	}

	// IAM eventual consistency: NoSuchEntity on freshly-created resources.
	if args0(args) == "iam" && (failure.Category == FailureNotFound || failure.Code == "NoSuchEntity") {
		op := args1(args)
		if op == "add-role-to-instance-profile" || op == "attach-role-policy" || op == "put-role-policy" || op == "update-assume-role-policy" {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry IAM operation after propagation\n")
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// S3 delete-bucket: bucket not empty -> empty then retry (destroyer only).
	if opts.Destroyer {
		if args0(args) == "s3api" && args1(args) == "delete-bucket" {
			lower := strings.ToLower(output)
			if failure.Category == FailureConflict || strings.Contains(lower, "bucketnotempty") {
				bucket := flagValue(args, "--bucket")
				if strings.TrimSpace(bucket) != "" {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: emptying s3 bucket before delete (bucket=%s)\n", strings.TrimSpace(bucket))
					if err := emptyS3Bucket(ctx, opts, strings.TrimSpace(bucket), opts.Writer); err != nil {
						return true, err
					}
					if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err != nil {
						return true, err
					}
					return true, nil
				}
			}
		}
		if args0(args) == "s3" && (args1(args) == "rb" || args1(args) == "rmbucket") {
			lower := strings.ToLower(output)
			if failure.Category == FailureConflict || strings.Contains(lower, "bucketnotempty") {
				bucket := extractS3BucketFromURI(args)
				if strings.TrimSpace(bucket) != "" {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: emptying s3 bucket before rb (bucket=%s)\n", strings.TrimSpace(bucket))
					if err := emptyS3Bucket(ctx, opts, strings.TrimSpace(bucket), opts.Writer); err != nil {
						return true, err
					}
					if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err != nil {
						return true, err
					}
					return true, nil
				}
			}
		}
	}

	// IAM delete-policy: DeleteConflict -> detach entities + delete non-default versions then retry (destroyer only).
	if opts.Destroyer && args0(args) == "iam" && args1(args) == "delete-policy" {
		lower := strings.ToLower(output)
		if failure.Category == FailureConflict || strings.Contains(lower, "deleteconflict") {
			policyArn := flagValue(args, "--policy-arn")
			if strings.TrimSpace(policyArn) != "" {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: resolving iam delete-policy conflict (policy=%s)\n", strings.TrimSpace(policyArn))
				if err := resolveAndDeleteIAMPolicy(ctx, opts, strings.TrimSpace(policyArn), opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// Lambda create-event-source-mapping: common role permission propagation/mismatch issues.
	// If AWS says the function execution role lacks event source permissions, attach the
	// corresponding AWS-managed execution policy to the *actual* role configured on the function,
	// then retry with backoff.
	if args0(args) == "lambda" && args1(args) == "create-event-source-mapping" {
		lower := strings.ToLower(output)
		// Idempotency: mapping already exists.
		if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict || strings.Contains(lower, "resourceconflictexception") {
			return true, nil
		}

		if strings.Contains(lower, "function execution role") && strings.Contains(lower, "does not have permissions to call") {
			policyArn := ""
			switch {
			case strings.Contains(lower, "receivemessage") && strings.Contains(lower, "sqs"):
				policyArn = "arn:aws:iam::aws:policy/service-role/AWSLambdaSQSQueueExecutionRole"
			case strings.Contains(lower, "getrecords") && strings.Contains(lower, "kinesis"):
				policyArn = "arn:aws:iam::aws:policy/service-role/AWSLambdaKinesisExecutionRole"
			case strings.Contains(lower, "getrecords") && strings.Contains(lower, "dynamodb"):
				policyArn = "arn:aws:iam::aws:policy/service-role/AWSLambdaDynamoDBExecutionRole"
			}

			fn := strings.TrimSpace(flagValue(args, "--function-name"))
			if fn != "" && policyArn != "" {
				conf, err := getFunctionConfiguration(ctx, opts, fn)
				if err == nil && strings.TrimSpace(conf.Role) != "" {
					roleName := roleNameFromArn(conf.Role)
					if strings.TrimSpace(roleName) != "" {
						_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: attach lambda event source execution policy then retry (function=%s role=%s)\n", fn, strings.TrimSpace(roleName))
						attach := []string{"iam", "attach-role-policy", "--role-name", strings.TrimSpace(roleName), "--policy-arn", policyArn, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
						// Best-effort: attach-role-policy is idempotent.
						_, _ = runAWSCommandStreaming(ctx, attach, nil, opts.Writer)

						if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
							return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
						}); err != nil {
							return true, err
						}
						return true, nil
					}
				}
			}
		}

		// Event source can exist, but IAM propagation can make it look like a validation failure.
		if failure.Category == FailureNotFound {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry create-event-source-mapping after propagation\n")
			if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			}); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// Lambda readiness: immediately-following calls can race with function creation/replication.
	// Retry a few times on "not found" / "invalid" symptoms.
	if args0(args) == "lambda" {
		op := args1(args)
		if op == "add-permission" || op == "create-function-url-config" {
			fn := flagValue(args, "--function-name")
			if fn != "" {
				_ = waitForLambdaFunctionActive(ctx, opts, fn, io.Discard)
			}
			lower := strings.ToLower(output)
			if failure.Category == FailureNotFound ||
				(strings.Contains(lower, "not found") || strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "cannot find") || strings.Contains(lower, "does not exist")) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry lambda %s after propagation\n", op)
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
			if failure.Category == FailureValidation {
				// Some regions/services report validation errors while the function is still settling.
				if strings.Contains(lower, "invalid") && (strings.Contains(lower, "function") || strings.Contains(lower, "arn")) {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry lambda %s after validation/settling\n", op)
					if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
						return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
					}); err != nil {
						return true, err
					}
					return true, nil
				}
			}
		}
	}

	// DynamoDB readiness: table is being created/updated. Wait for ACTIVE, then retry.
	if args0(args) == "dynamodb" {
		tableName := flagValue(args, "--table-name")
		if tableName == "" {
			tableName = flagValue(args, "--table")
		}
		if strings.TrimSpace(tableName) != "" {
			lower := strings.ToLower(output)
			if failure.Category == FailureConflict || failure.Category == FailureThrottled ||
				strings.Contains(lower, "resourceinuse") ||
				strings.Contains(lower, "being created") ||
				strings.Contains(lower, "being updated") ||
				strings.Contains(lower, "table status") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: waiting for dynamodb table ACTIVE (table=%s)\n", strings.TrimSpace(tableName))
				if err := waitForDynamoDBTableActive(ctx, opts, strings.TrimSpace(tableName), opts.Writer); err != nil {
					return true, err
				}
				if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// RDS readiness: DB instance is creating/modifying. Wait for available, then retry.
	if args0(args) == "rds" {
		id := flagValue(args, "--db-instance-identifier")
		if strings.TrimSpace(id) != "" {
			lower := strings.ToLower(output)
			if failure.Category == FailureConflict || failure.Category == FailureThrottled ||
				strings.Contains(lower, "creating") ||
				strings.Contains(lower, "modifying") ||
				strings.Contains(lower, "is not available") ||
				strings.Contains(lower, "invalid db instance state") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: waiting for rds instance available (db=%s)\n", strings.TrimSpace(id))
				if err := waitForRDSInstanceAvailable(ctx, opts, strings.TrimSpace(id), opts.Writer); err != nil {
					return true, err
				}
				if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// Lambda create-function: create conflict -> update.
	if isLambdaCreateFunction(args) && (failure.Category == FailureAlreadyExists || isLambdaAlreadyExists(output)) {
		if err := updateExistingLambda(ctx, opts, args, stdinBytes, opts.Writer); err != nil {
			return true, err
		}
		return true, nil
	}

	// EKS readiness: cluster/nodegroup/addons can take time.
	if args0(args) == "eks" {
		lower := strings.ToLower(output)

		// Teardown: delete-cluster fails when nodegroups are still attached.
		// In destroyer mode, delete all nodegroups first, then retry cluster deletion.
		if opts.Destroyer && args1(args) == "delete-cluster" {
			clusterName := strings.TrimSpace(flagValue(args, "--name"))
			if clusterName == "" {
				clusterName = strings.TrimSpace(flagValue(args, "--cluster-name"))
			}
			if clusterName != "" && (failure.Code == "ResourceInUseException" || strings.Contains(lower, "nodegroups attached") || (strings.Contains(lower, "nodegroup") && strings.Contains(lower, "attached"))) {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: eks delete-cluster blocked by nodegroups; deleting nodegroups then retry\n")
				if err := deleteAllEKSNodegroups(ctx, opts, clusterName, opts.Writer); err != nil {
					return true, err
				}
				if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
				}); err != nil {
					return true, err
				}
				return true, nil
			}
		}

		// Do not run waiters if the request failed due to invalid network params.
		if strings.Contains(lower, "invalidsubnetid.notfound") || (strings.Contains(lower, "subnet id") && strings.Contains(lower, "does not exist")) {
			return false, nil
		}
		clusterName := flagValue(args, "--name")
		if clusterName == "" {
			clusterName = flagValue(args, "--cluster-name")
		}
		if strings.TrimSpace(clusterName) != "" {
			if failure.Category == FailureConflict || failure.Category == FailureNotFound ||
				strings.Contains(lower, "resourceinuse") ||
				strings.Contains(lower, "in progress") ||
				strings.Contains(lower, "not in active") ||
				strings.Contains(lower, "invalid cluster") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: waiting for eks cluster active (name=%s)\n", strings.TrimSpace(clusterName))
				_ = waitForEKSClusterActive(ctx, opts, strings.TrimSpace(clusterName), opts.Writer)
				if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err == nil {
					return true, nil
				}
			}
		}

		nodegroup := flagValue(args, "--nodegroup-name")
		if strings.TrimSpace(clusterName) != "" && strings.TrimSpace(nodegroup) != "" {
			if failure.Category == FailureConflict || failure.Category == FailureNotFound || strings.Contains(lower, "in progress") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: waiting for eks nodegroup active (cluster=%s nodegroup=%s)\n", strings.TrimSpace(clusterName), strings.TrimSpace(nodegroup))
				_ = waitForEKSNodegroupActive(ctx, opts, strings.TrimSpace(clusterName), strings.TrimSpace(nodegroup), opts.Writer)
				if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err == nil {
					return true, nil
				}
			}
		}
	}

	// API Gateway v1: model sometimes emits --api-id (v2-style) instead of --rest-api-id.
	// Rewrite flag name and retry.
	if args0(args) == "apigateway" && args1(args) == "delete-rest-api" {
		rewritten, ok := rewriteFlagName(args, "--api-id", "--rest-api-id")
		if ok {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewriting apigateway delete-rest-api --api-id -> --rest-api-id then retry\n")
			rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
		// Some plans might use --api-id=... form.
		rewritten2, ok2 := rewriteFlagName(args, "--api-id=", "--rest-api-id=")
		if ok2 {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewriting apigateway delete-rest-api --api-id= -> --rest-api-id= then retry\n")
			rewrittenAWSArgs := append(append([]string{}, rewritten2...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}

		// Teardown: sometimes plans try to delete an API Gateway v2 (HTTP/WebSocket) API with the v1 command.
		// If we get NotFound + "Invalid API identifier specified", fall back to apigatewayv2 delete-api.
		if opts.Destroyer && (failure.Category == FailureNotFound || failure.Code == "NotFoundException") {
			id := strings.TrimSpace(flagValue(args, "--rest-api-id"))
			if id == "" {
				id = strings.TrimSpace(flagValue(args, "--api-id"))
			}
			if id == "" {
				id = strings.TrimSpace(extractAPIGatewayV2IDFromNotFound(output))
			}
			if id != "" {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: apigateway delete-rest-api not found; trying apigatewayv2 delete-api (apiId=%s)\n", id)
				del := []string{"apigatewayv2", "delete-api", "--api-id", id, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
				out2, err := retryWithBackoffOutput(ctx, opts.Writer, 6, func() (string, error) {
					return runAWSCommandStreaming(ctx, del, stdinBytes, opts.Writer)
				})
				if err != nil {
					f2 := classifyAWSFailure([]string{"apigatewayv2", "delete-api"}, out2)
					if f2.Category == FailureNotFound || f2.Code == "NotFoundException" {
						return true, nil
					}
					return true, err
				}
				return true, nil
			}
		}
	}

	// CloudFront readiness: distribution updates/deployments are async.
	if args0(args) == "cloudfront" {
		id := flagValue(args, "--id")
		lower := strings.ToLower(output)
		if strings.TrimSpace(id) != "" && (strings.Contains(lower, "inprogress") || strings.Contains(lower, "in progress") || strings.Contains(lower, "deployed") == false) {
			// Only trigger on errors that look like "not deployed yet".
			if failure.Category == FailureConflict || failure.Category == FailureValidation || strings.Contains(lower, "not") && strings.Contains(lower, "deployed") {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: waiting for cloudfront distribution deployed (id=%s)\n", strings.TrimSpace(id))
				_ = waitForCloudFrontDistributionDeployed(ctx, opts, strings.TrimSpace(id), opts.Writer)
				if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err == nil {
					return true, nil
				}
			}
		}
	}

	// API Gateway v2 quick create: model sometimes emits a Lambda ARN missing the region
	// (e.g. arn:aws:lambda:<account>:function:<name>). Rewrite to include the configured region.
	if args0(args) == "apigatewayv2" && args1(args) == "create-api" {
		lower := strings.ToLower(output)
		// If it already exists (or conflicts), treat as idempotent success.
		if failure.Category == FailureAlreadyExists || failure.Category == FailureConflict {
			return true, nil
		}
		if strings.Contains(lower, "invalid function arn") || strings.Contains(lower, "invalid uri") {
			// Try function-name -> full arn first.
			if rewritten, ok := rewriteAPIGatewayV2CreateApiLambdaTargetFunctionNameToArn(ctx, opts, args); ok {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewriting apigatewayv2 create-api --target lambda function name to full ARN\n")
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}

			if rewritten, ok := rewriteAPIGatewayV2CreateApiLambdaTarget(args, opts.Region); ok {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewriting apigatewayv2 create-api --target lambda ARN to include region\n")
				rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
				if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
					return true, err
				}
				return true, nil
			}
		}
	}

	// EC2 run-instances: IAM instance profile propagation/name resolution can lag.
	// When the CLI reports an invalid instance profile name, fetch the instance profile ARN
	// (retrying get-instance-profile), rewrite the run-instances call to use Arn=..., and retry.
	if args0(args) == "ec2" && args1(args) == "run-instances" && failure.Category == FailureValidation {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "iaminstanceprofile") && strings.Contains(lower, "invalid") && strings.Contains(lower, "instance profile") {
			if err := remediateEC2InvalidInstanceProfileAndRetry(ctx, opts, args, stdinBytes, opts.Writer, bindings); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// SSM put-parameter: if it already exists, add --overwrite and retry.
	if args0(args) == "ssm" && args1(args) == "put-parameter" && failure.Category == FailureAlreadyExists {
		if !hasExactFlag(args, "--overwrite") {
			rewritten := append([]string{}, args...)
			rewritten = append(rewritten, "--overwrite")
			rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: ssm put-parameter exists; retrying with --overwrite\n")
			if _, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
		return true, nil
	}

	// KMS alias create: if alias exists, use update-alias.
	if args0(args) == "kms" && args1(args) == "create-alias" && failure.Category == FailureAlreadyExists {
		aliasName := flagValue(args, "--alias-name")
		targetKeyID := flagValue(args, "--target-key-id")
		if strings.TrimSpace(aliasName) != "" && strings.TrimSpace(targetKeyID) != "" {
			upd := []string{"kms", "update-alias", "--alias-name", aliasName, "--target-key-id", targetKeyID}
			updAWSArgs := append(append([]string{}, upd...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: kms create-alias exists; using update-alias\n")
			if _, err := runAWSCommandStreaming(ctx, updAWSArgs, nil, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
		return true, nil
	}

	// Secrets Manager: create-secret already exists -> put-secret-value (if secret-string provided).
	if failure.Category == FailureAlreadyExists && args0(args) == "secretsmanager" && args1(args) == "create-secret" {
		name := flagValue(args, "--name")
		if strings.TrimSpace(name) != "" {
			if arn, err := describeSecretARNByName(ctx, opts, name); err == nil {
				if strings.TrimSpace(arn) != "" {
					inferSecretsBindings(name, arn, bindings)
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: using existing secret ARN (name=%s)\n", name)
				}
			} else {
				_, _ = fmt.Fprintf(opts.Writer, "[maker] warning: failed to resolve existing secret ARN for %s: %v\n", name, err)
			}
		}
		secretString := flagValue(args, "--secret-string")
		if strings.TrimSpace(name) != "" && strings.TrimSpace(secretString) != "" {
			put := []string{"secretsmanager", "put-secret-value", "--secret-id", name, "--secret-string", secretString}
			putAWSArgs := append(append([]string{}, put...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: secretsmanager create-secret exists; using put-secret-value\n")
			if _, err := runAWSCommandStreaming(ctx, putAWSArgs, nil, opts.Writer); err != nil {
				return true, err
			}
			return true, nil
		}
		return true, nil
	}

	// ECR: describe-images with an explicit tag can fail if the tag doesn't exist (common footgun: assuming :latest).
	// Remediation: pick the newest pushed image digest in the repo, bind IMAGE_DIGEST, and continue.
	if args0(args) == "ecr" && args1(args) == "describe-images" && failure.Category == FailureNotFound {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "imagenotfoundexception") || strings.Contains(lower, "requested image not found") || strings.Contains(lower, "does not exist") {
			repoName := strings.TrimSpace(flagValue(args, "--repository-name"))
			if repoName != "" {
				digest, digErr := remediateECRBindLatestDigest(ctx, opts, repoName, bindings)
				if digErr != nil {
					return true, digErr
				}
				if strings.TrimSpace(digest) != "" {
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: ecr describe-images tag missing; using newest image digest (repo=%s)\n", repoName)
					return true, nil
				}
			}
		}
	}

	if ok, err := maybeGenericGlueAndRetry(ctx, opts, args, awsArgs, stdinBytes, failure, output); ok {
		return true, err
	}

	// Nothing to rewrite.
	return false, nil
}

func describeTargetGroupArnByName(ctx context.Context, opts ExecOptions, tgName string) (string, error) {
	tgName = strings.TrimSpace(tgName)
	if tgName == "" {
		return "", nil
	}

	args := []string{
		"elbv2", "describe-target-groups",
		"--names", tgName,
		"--output", "json",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if err != nil {
		return "", err
	}

	var resp struct {
		TargetGroups []struct {
			TargetGroupArn string `json:"TargetGroupArn"`
		} `json:"TargetGroups"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	if len(resp.TargetGroups) == 0 {
		return "", nil
	}
	return strings.TrimSpace(resp.TargetGroups[0].TargetGroupArn), nil
}

func describeLoadBalancerByName(ctx context.Context, opts ExecOptions, lbName string) (string, string, error) {
	lbName = strings.TrimSpace(lbName)
	if lbName == "" {
		return "", "", nil
	}

	args := []string{
		"elbv2", "describe-load-balancers",
		"--names", lbName,
		"--output", "json",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if err != nil {
		return "", "", err
	}

	var resp struct {
		LoadBalancers []struct {
			LoadBalancerArn string `json:"LoadBalancerArn"`
			DNSName         string `json:"DNSName"`
		} `json:"LoadBalancers"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", "", err
	}
	if len(resp.LoadBalancers) == 0 {
		return "", "", nil
	}
	return strings.TrimSpace(resp.LoadBalancers[0].LoadBalancerArn), strings.TrimSpace(resp.LoadBalancers[0].DNSName), nil
}

func remediateECRBindLatestDigest(ctx context.Context, opts ExecOptions, repoName string, bindings map[string]string) (string, error) {
	repoName = strings.TrimSpace(repoName)
	if repoName == "" {
		return "", fmt.Errorf("empty ECR repo name")
	}
	if bindings == nil {
		return "", fmt.Errorf("nil bindings")
	}

	// If ECR_URI isn't already known, fetch it (best-effort).
	if strings.TrimSpace(bindings["ECR_URI"]) == "" {
		descRepo := []string{"ecr", "describe-repositories", "--repository-names", repoName, "--output", "json"}
		awsArgs := buildAWSExecArgs(descRepo, opts, opts.Writer)
		out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
		if err == nil {
			var resp struct {
				Repositories []struct {
					RepositoryURI string `json:"repositoryUri"`
				} `json:"repositories"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				if len(resp.Repositories) > 0 {
					bindings["ECR_URI"] = strings.TrimSpace(resp.Repositories[0].RepositoryURI)
				}
			}
		}
	}

	// Describe newest pushed image (do not assume tag exists).
	q := []string{
		"ecr", "describe-images",
		"--repository-name", repoName,
		"--query", "sort_by(imageDetails,&imagePushedAt)[-1]",
		"--output", "json",
	}
	awsArgs := buildAWSExecArgs(q, opts, opts.Writer)
	out, err := runAWSCommandStreaming(ctx, awsArgs, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		ImageDigest string   `json:"imageDigest"`
		ImageTags   []string `json:"imageTags"`
	}
	if json.Unmarshal([]byte(out), &resp) != nil {
		return "", fmt.Errorf("failed to parse ECR describe-images output")
	}
	digest := strings.TrimSpace(resp.ImageDigest)
	if digest == "" || digest == "null" {
		return "", fmt.Errorf("no images found in ECR repo %s (push an image first)", repoName)
	}
	bindings["IMAGE_DIGEST"] = digest
	if len(resp.ImageTags) > 0 {
		bindings["IMAGE_TAG"] = strings.TrimSpace(resp.ImageTags[0])
	}
	return digest, nil
}

func describeSecretARNByName(ctx context.Context, opts ExecOptions, secretName string) (string, error) {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return "", nil
	}

	args := []string{
		"secretsmanager", "describe-secret",
		"--secret-id", secretName,
		"--output", "json",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if err != nil {
		return "", err
	}

	var resp struct {
		ARN string `json:"ARN"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.ARN), nil
}

func rewriteFlagName(args []string, from string, to string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	out := append([]string{}, args...)
	changed := false
	for i := 0; i < len(out); i++ {
		if out[i] == from {
			out[i] = to
			changed = true
			continue
		}
		if strings.HasPrefix(out[i], from) {
			out[i] = to + strings.TrimPrefix(out[i], from)
			changed = true
			continue
		}
	}
	if !changed {
		return nil, false
	}
	return out, true
}

func findAttachedInternetGatewayForVPC(ctx context.Context, opts ExecOptions, vpcID string) (string, error) {
	vpcID = strings.TrimSpace(vpcID)
	if vpcID == "" {
		return "", nil
	}
	q := []string{"ec2", "describe-internet-gateways", "--filters", fmt.Sprintf("Name=attachment.vpc-id,Values=%s", vpcID), "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		InternetGateways []struct {
			InternetGatewayId string `json:"InternetGatewayId"`
		} `json:"InternetGateways"`
	}
	if json.Unmarshal([]byte(out), &resp) != nil {
		return "", nil
	}
	if len(resp.InternetGateways) == 0 {
		return "", nil
	}
	return strings.TrimSpace(resp.InternetGateways[0].InternetGatewayId), nil
}

func deleteAllEKSNodegroups(ctx context.Context, opts ExecOptions, clusterName string, w io.Writer) error {
	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return fmt.Errorf("empty cluster name")
	}

	listArgs := []string{"eks", "list-nodegroups", "--cluster-name", clusterName, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, listArgs, nil, io.Discard)
	if err != nil {
		return err
	}
	var resp struct {
		Nodegroups []string `json:"nodegroups"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &resp); jsonErr != nil {
		return jsonErr
	}
	for _, ng := range resp.Nodegroups {
		ng = strings.TrimSpace(ng)
		if ng == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "[maker] note: deleting eks nodegroup (cluster=%s nodegroup=%s)\n", clusterName, ng)
		del := []string{"eks", "delete-nodegroup", "--cluster-name", clusterName, "--nodegroup-name", ng, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = runAWSCommandStreaming(ctx, del, nil, w)
		_ = waitForEKSNodegroupDeleted(ctx, opts, clusterName, ng, w)
	}
	_ = waitForEKSNodegroupsEmpty(ctx, opts, clusterName, w)
	return nil
}

func waitForEKSNodegroupDeleted(ctx context.Context, opts ExecOptions, clusterName string, nodegroupName string, w io.Writer) error {
	clusterName = strings.TrimSpace(clusterName)
	nodegroupName = strings.TrimSpace(nodegroupName)
	if clusterName == "" || nodegroupName == "" {
		return nil
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			sleep := time.Duration(attempt) * 1200 * time.Millisecond
			_, _ = fmt.Fprintf(w, "[maker] note: retrying eks nodegroup wait (attempt=%d sleep=%s)\n", attempt, sleep)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}

		wait := []string{"eks", "wait", "nodegroup-deleted", "--cluster-name", clusterName, "--nodegroup-name", nodegroupName, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, wait, nil, w)
		if err == nil {
			return nil
		}
		lower := strings.ToLower(out)
		if strings.Contains(lower, "resourcenotfound") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") {
			return nil
		}
	}

	return nil
}

func waitForEKSNodegroupsEmpty(ctx context.Context, opts ExecOptions, clusterName string, w io.Writer) error {
	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return nil
	}

	start := time.Now()
	for attempt := 1; attempt <= 30; attempt++ {
		if attempt > 1 {
			sleep := time.Duration(attempt) * 2 * time.Second
			if sleep > 30*time.Second {
				sleep = 30 * time.Second
			}
			_, _ = fmt.Fprintf(w, "[maker] note: waiting for eks nodegroups list to empty (attempt=%d sleep=%s)\n", attempt, sleep)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}

		listArgs := []string{"eks", "list-nodegroups", "--cluster-name", clusterName, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, listArgs, nil, io.Discard)
		if err != nil {
			continue
		}
		var resp struct {
			Nodegroups []string `json:"nodegroups"`
		}
		if jsonErr := json.Unmarshal([]byte(out), &resp); jsonErr != nil {
			continue
		}
		if len(resp.Nodegroups) == 0 {
			return nil
		}
		if time.Since(start) > 15*time.Minute {
			return nil
		}
	}

	return nil
}

func hasSubnetPlaceholders(args []string) bool {
	for _, a := range args {
		al := strings.ToLower(strings.TrimSpace(a))
		if strings.Contains(al, "<subnet") {
			return true
		}
		if strings.Contains(al, "subnetids=<subnet") {
			return true
		}
	}
	return false
}

func rewriteEKSSubnets(ctx context.Context, opts ExecOptions, args []string) ([]string, bool, error) {
	service := args0(args)
	op := args1(args)
	if service != "eks" {
		return nil, false, nil
	}

	vpcID, err := inferVPCForEKS(ctx, opts, args)
	if err != nil {
		return nil, false, err
	}
	vpcID = strings.TrimSpace(vpcID)
	if vpcID == "" {
		return nil, false, nil
	}

	subnets, err := pickSubnetsForVPC(ctx, opts, vpcID, 2)
	if err != nil {
		return nil, false, err
	}
	if len(subnets) < 2 {
		return nil, false, fmt.Errorf("not enough subnets found in vpc %s", vpcID)
	}

	rewritten := append([]string{}, args...)
	changed := false

	if op == "create-cluster" {
		idx := indexOfExactFlag(rewritten, "--resources-vpc-config")
		if idx >= 0 && idx+1 < len(rewritten) {
			v := rewritten[idx+1]
			vl := strings.ToLower(v)
			if strings.Contains(vl, "subnetids=") {
				newV, did := replaceSubnetIDsInVPCConfigValue(v, subnets)
				if did {
					rewritten[idx+1] = newV
					changed = true
				}
			}
		}
	}

	if op == "create-nodegroup" {
		idx := indexOfExactFlag(rewritten, "--subnets")
		if idx >= 0 {
			start := idx + 1
			end := start
			for end < len(rewritten) {
				if strings.HasPrefix(strings.TrimSpace(rewritten[end]), "--") {
					break
				}
				end++
			}
			if start < len(rewritten) {
				// If subnets are placeholders or empty, rewrite.
				rewrite := false
				if start == end {
					rewrite = true
				} else {
					for i := start; i < end; i++ {
						s := strings.TrimSpace(rewritten[i])
						if s == "" || strings.HasPrefix(strings.ToLower(s), "<subnet") {
							rewrite = true
							break
						}
						if !strings.HasPrefix(s, "subnet-") {
							rewrite = true
							break
						}
					}
				}
				if rewrite {
					// Replace the values after --subnets with our chosen ones.
					newArgs := make([]string, 0, len(rewritten)+2)
					newArgs = append(newArgs, rewritten[:start]...)
					newArgs = append(newArgs, subnets...)
					newArgs = append(newArgs, rewritten[end:]...)
					rewritten = newArgs
					changed = true
				}
			}
		}
	}

	if !changed {
		return nil, false, nil
	}
	return rewritten, true, nil
}

func replaceSubnetIDsInVPCConfigValue(v string, subnets []string) (string, bool) {
	if len(subnets) < 2 {
		return v, false
	}
	parts := strings.Split(v, ",")
	changed := false

	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		p := strings.TrimSpace(parts[i])
		pl := strings.ToLower(p)
		if strings.HasPrefix(pl, "subnetids=") {
			out = append(out, "subnetIds="+strings.Join(subnets, ","))
			changed = true

			// Skip subsequent comma-separated subnet ids that belong to subnetIds=...
			// until the next key=value segment.
			j := i + 1
			for j < len(parts) {
				seg := strings.TrimSpace(parts[j])
				if strings.Contains(seg, "=") {
					break
				}
				j++
			}
			i = j - 1
			continue
		}
		out = append(out, p)
	}

	if !changed {
		return v, false
	}
	return strings.Join(out, ","), true
}

func inferVPCForEKS(ctx context.Context, opts ExecOptions, args []string) (string, error) {
	// Prefer VPC inferred from security group passed to create-cluster.
	if args1(args) == "create-cluster" {
		idx := indexOfExactFlag(args, "--resources-vpc-config")
		if idx >= 0 && idx+1 < len(args) {
			v := args[idx+1]
			if sgID := extractSecurityGroupIDFromVPCConfigValue(v); sgID != "" {
				if vpc, err := vpcIDFromSecurityGroup(ctx, opts, sgID); err == nil && strings.TrimSpace(vpc) != "" {
					return strings.TrimSpace(vpc), nil
				}
			}
		}
	}
	// Fallback: default VPC.
	return defaultVPCID(ctx, opts)
}

func extractSecurityGroupIDFromVPCConfigValue(v string) string {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		pl := strings.ToLower(p)
		if strings.HasPrefix(pl, "securitygroupids=") {
			val := strings.TrimSpace(p[len("securityGroupIds="):])
			for _, piece := range strings.Split(val, ";") {
				piece = strings.TrimSpace(piece)
				if strings.HasPrefix(piece, "sg-") {
					return piece
				}
			}
			for _, piece := range strings.Split(val, ",") {
				piece = strings.TrimSpace(piece)
				if strings.HasPrefix(piece, "sg-") {
					return piece
				}
			}
		}
	}
	return ""
}

func defaultVPCID(ctx context.Context, opts ExecOptions) (string, error) {
	q := []string{"ec2", "describe-vpcs", "--filters", "Name=isDefault,Values=true", "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		Vpcs []struct {
			VpcID string `json:"VpcId"`
		} `json:"Vpcs"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	if len(resp.Vpcs) == 0 {
		return "", nil
	}
	return strings.TrimSpace(resp.Vpcs[0].VpcID), nil
}

func vpcIDFromSecurityGroup(ctx context.Context, opts ExecOptions, sgID string) (string, error) {
	sgID = strings.TrimSpace(sgID)
	if sgID == "" {
		return "", fmt.Errorf("empty security group id")
	}
	q := []string{"ec2", "describe-security-groups", "--group-ids", sgID, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		SecurityGroups []struct {
			VpcID string `json:"VpcId"`
		} `json:"SecurityGroups"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	if len(resp.SecurityGroups) == 0 {
		return "", nil
	}
	return strings.TrimSpace(resp.SecurityGroups[0].VpcID), nil
}

func pickSubnetsForVPC(ctx context.Context, opts ExecOptions, vpcID string, want int) ([]string, error) {
	vpcID = strings.TrimSpace(vpcID)
	if vpcID == "" {
		return nil, fmt.Errorf("empty vpc id")
	}
	if want <= 0 {
		want = 2
	}
	q := []string{"ec2", "describe-subnets", "--filters", "Name=vpc-id,Values=" + vpcID, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Subnets []struct {
			SubnetID         string `json:"SubnetId"`
			AvailabilityZone string `json:"AvailabilityZone"`
		} `json:"Subnets"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	type subnetInfo struct {
		id string
		az string
	}
	items := make([]subnetInfo, 0, len(resp.Subnets))
	for _, s := range resp.Subnets {
		id := strings.TrimSpace(s.SubnetID)
		az := strings.TrimSpace(s.AvailabilityZone)
		if id == "" {
			continue
		}
		items = append(items, subnetInfo{id: id, az: az})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].az == items[j].az {
			return items[i].id < items[j].id
		}
		return items[i].az < items[j].az
	})

	chosen := []string{}
	seenAZ := map[string]bool{}
	seenID := map[string]bool{}
	for _, it := range items {
		if len(chosen) >= want {
			break
		}
		if it.az != "" && seenAZ[it.az] {
			continue
		}
		if seenID[it.id] {
			continue
		}
		seenID[it.id] = true
		if it.az != "" {
			seenAZ[it.az] = true
		}
		chosen = append(chosen, it.id)
	}
	// If we couldn't get distinct AZs, just fill from remaining.
	for _, it := range items {
		if len(chosen) >= want {
			break
		}
		if seenID[it.id] {
			continue
		}
		seenID[it.id] = true
		chosen = append(chosen, it.id)
	}
	return chosen, nil
}

func indexOfExactFlag(args []string, flag string) int {
	for i, a := range args {
		if strings.TrimSpace(a) == flag {
			return i
		}
	}
	return -1
}

func maybeFixRolePermissionAndRetry(ctx context.Context, opts ExecOptions, args []string, awsArgs []string, stdinBytes []byte, failure AWSFailure, output string) (bool, error) {
	lower := strings.ToLower(output)

	// Only attempt for role-based errors; do not try to “fix” caller AccessDenied.
	roleish := strings.Contains(lower, ":role/") || strings.Contains(lower, "execution role") || strings.Contains(lower, "assumed role")
	if !roleish {
		return false, nil
	}

	// Must look like a missing-permission error.
	if !(failure.Category == FailureAccessDenied || strings.Contains(lower, "not authorized") || strings.Contains(lower, "does not have permissions") || strings.Contains(lower, "permissions to call")) {
		return false, nil
	}

	roleArn := strings.TrimSpace(flagValue(args, "--role-arn"))
	if roleArn == "" {
		// Lambda: discover the actual execution role for the function.
		if args0(args) == "lambda" {
			fn := strings.TrimSpace(flagValue(args, "--function-name"))
			if fn != "" {
				if conf, err := getFunctionConfiguration(ctx, opts, fn); err == nil {
					roleArn = strings.TrimSpace(conf.Role)
				}
			}
		}
	}
	if roleArn == "" {
		// Try inline JSON args (e.g. EventBridge targets, Pipes, Scheduler, etc.).
		arns := findRoleArnsInArgsJSON(args)
		if len(arns) > 0 {
			roleArn = strings.TrimSpace(arns[0])
		}
	}
	if roleArn == "" {
		return false, nil
	}

	roleName := strings.TrimSpace(roleNameFromArn(roleArn))
	if roleName == "" {
		return false, nil
	}

	policyName, actions, resources := inferInlinePolicyForRolePermissionError(args, lower)
	if policyName == "" || len(actions) == 0 {
		return false, nil
	}
	if len(resources) == 0 {
		resources = []string{"*"}
	}

	policyDoc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect":   "Allow",
				"Action":   actions,
				"Resource": resources,
			},
		},
	}
	b, _ := json.Marshal(policyDoc)
	put := []string{"iam", "put-role-policy", "--role-name", roleName, "--policy-name", policyName, "--policy-document", string(b), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}

	_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: add inline role policy then retry (role=%s policy=%s)\n", roleName, policyName)
	// Best-effort: put-role-policy is upsert-like.
	_, _ = runAWSCommandStreaming(ctx, put, nil, opts.Writer)

	if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
		return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
	}); err != nil {
		return true, err
	}
	return true, nil
}

func inferInlinePolicyForRolePermissionError(args []string, lowerOutput string) (policyName string, actions []string, resources []string) {
	// Generic extraction: many services include the required IAM action(s) directly in the error.
	// Example patterns include "is not authorized to perform: bedrock:InvokeModel" or
	// "does not have permissions to call sns:Publish".
	// If we can extract explicit actions, prefer that over service-specific heuristics.
	if extracted, svc := extractIAMActionsFromOutput(lowerOutput); len(extracted) > 0 {
		policyName = "ClankerAutoPerms" + strings.ToUpper(svc)
		actions = extracted
		// Best-effort: restrict resources if we can find ARNs for that service in args; otherwise use '*'.
		if svc != "" {
			resources = findServiceARNsInArgs(args, svc)
		}
		if len(resources) == 0 {
			resources = []string{"*"}
		}
		return
	}

	// SQS-style phrasing: "permissions to call ReceiveMessage on SQS".
	if strings.Contains(lowerOutput, " on sqs") || strings.Contains(lowerOutput, "sqs") {
		if strings.Contains(lowerOutput, "receivemessage") || strings.Contains(lowerOutput, "delete") || strings.Contains(lowerOutput, "change") || strings.Contains(lowerOutput, "getqueueattributes") {
			policyName = "ClankerAutoPermsSQS"
			actions = []string{
				"sqs:ReceiveMessage",
				"sqs:DeleteMessage",
				"sqs:ChangeMessageVisibility",
				"sqs:GetQueueAttributes",
				"sqs:GetQueueUrl",
				"sqs:ListQueues",
			}
			resources = findServiceARNsInArgs(args, "sqs")
			return
		}
	}

	// Kinesis / DynamoDB Streams commonly report GetRecords/GetShardIterator.
	if strings.Contains(lowerOutput, "kinesis") {
		if strings.Contains(lowerOutput, "getrecords") || strings.Contains(lowerOutput, "getsharditerator") || strings.Contains(lowerOutput, "listshards") || strings.Contains(lowerOutput, "describestream") {
			policyName = "ClankerAutoPermsKinesis"
			actions = []string{
				"kinesis:GetRecords",
				"kinesis:GetShardIterator",
				"kinesis:DescribeStream",
				"kinesis:DescribeStreamSummary",
				"kinesis:ListShards",
			}
			resources = findServiceARNsInArgs(args, "kinesis")
			return
		}
	}
	if strings.Contains(lowerOutput, "dynamodb") {
		if strings.Contains(lowerOutput, "getrecords") || strings.Contains(lowerOutput, "getsharditerator") || strings.Contains(lowerOutput, "describe") || strings.Contains(lowerOutput, "list") {
			policyName = "ClankerAutoPermsDynamoDBStreams"
			actions = []string{
				"dynamodb:GetRecords",
				"dynamodb:GetShardIterator",
				"dynamodb:DescribeStream",
				"dynamodb:ListStreams",
			}
			resources = findServiceARNsInArgs(args, "dynamodb")
			return
		}
	}

	// S3 common: List/Get/Put (best-effort when bucket ARN is visible in args).
	if strings.Contains(lowerOutput, "s3") {
		if strings.Contains(lowerOutput, "getobject") || strings.Contains(lowerOutput, "putobject") || strings.Contains(lowerOutput, "listbucket") {
			policyName = "ClankerAutoPermsS3"
			actions = []string{
				"s3:GetObject",
				"s3:PutObject",
				"s3:ListBucket",
			}
			resources = findS3BucketARNsInArgs(args)
			return
		}
	}

	// CloudWatch Logs common for execution roles.
	if strings.Contains(lowerOutput, "logs") {
		if strings.Contains(lowerOutput, "createlogstream") || strings.Contains(lowerOutput, "putlogevents") || strings.Contains(lowerOutput, "describe") {
			policyName = "ClankerAutoPermsLogs"
			actions = []string{
				"logs:CreateLogGroup",
				"logs:CreateLogStream",
				"logs:PutLogEvents",
				"logs:DescribeLogGroups",
				"logs:DescribeLogStreams",
			}
			resources = findServiceARNsInArgs(args, "logs")
			return
		}
	}

	// EventBridge Scheduler / Events often need iam:PassRole and invocation of targets.
	if strings.Contains(lowerOutput, "passrole") || strings.Contains(lowerOutput, "iam:passrole") {
		policyName = "ClankerAutoPermsPassRole"
		actions = []string{"iam:PassRole"}
		resources = findRoleArnsInArgsJSON(args)
		if len(resources) == 0 {
			resources = []string{"*"}
		}
		return
	}

	// ECR common for ECS/Lambda execution roles.
	if strings.Contains(lowerOutput, "ecr") {
		if strings.Contains(lowerOutput, "getauthorizationtoken") || strings.Contains(lowerOutput, "batchgetimage") || strings.Contains(lowerOutput, "getdownloadurlforlayer") || strings.Contains(lowerOutput, "batchchecklayeravailability") {
			policyName = "ClankerAutoPermsECR"
			actions = []string{
				"ecr:GetAuthorizationToken",
				"ecr:BatchCheckLayerAvailability",
				"ecr:GetDownloadUrlForLayer",
				"ecr:BatchGetImage",
			}
			resources = findServiceARNsInArgs(args, "ecr")
			return
		}
	}

	return "", nil, nil
}

var iamActionTokenRe = regexp.MustCompile(`\b([a-z0-9-]+):([a-z0-9*]+)\b`)

func extractIAMActionsFromOutput(lowerOutput string) (actions []string, service string) {
	lowerOutput = strings.TrimSpace(lowerOutput)
	if lowerOutput == "" {
		return nil, ""
	}

	// Only attempt extraction when it looks like an authz/permissions error for an assumed role.
	// (The caller already gated on role-permission style errors, but keep this extra guard cheap.)
	if !(strings.Contains(lowerOutput, "not authorized") || strings.Contains(lowerOutput, "accessdenied") || strings.Contains(lowerOutput, "does not have permissions")) {
		return nil, ""
	}

	seen := map[string]bool{}
	var out []string
	var svc string

	for _, m := range iamActionTokenRe.FindAllStringSubmatch(lowerOutput, -1) {
		if len(m) != 3 {
			continue
		}
		s := strings.TrimSpace(m[1])
		a := strings.TrimSpace(m[2])
		if s == "" || a == "" {
			continue
		}
		// Avoid accidental matches from ARNs like arn:aws:iam::aws:policy/...
		if s == "arn" || s == "aws" {
			continue
		}
		// Some messages include "sts:assumerole" etc; allow it, but keep the set minimal.
		tok := s + ":" + a
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
		if svc == "" {
			svc = s
		}
	}

	// Keep output stable/predictable.
	sort.Strings(out)
	return out, svc
}

func ecsExecutionRoleArnForTaskDefinition(ctx context.Context, opts ExecOptions, taskDefinition string) (string, error) {
	taskDefinition = strings.TrimSpace(taskDefinition)
	if taskDefinition == "" {
		return "", fmt.Errorf("empty task definition")
	}
	q := []string{"ecs", "describe-task-definition", "--task-definition", taskDefinition, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp struct {
		TaskDefinition struct {
			ExecutionRoleArn string `json:"executionRoleArn"`
		} `json:"taskDefinition"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.TaskDefinition.ExecutionRoleArn), nil
}

func findServiceARNsInArgs(args []string, service string) []string {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}

	addArn := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || !strings.HasPrefix(s, "arn:") {
			return
		}
		if !strings.Contains(s, ":"+service+":") {
			return
		}
		if seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	for _, a := range args {
		addArn(a)
	}
	for _, arn := range findArnsInArgsJSON(args) {
		addArn(arn)
	}
	return out
}

func findS3BucketARNsInArgs(args []string) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if !strings.HasPrefix(s, "arn:aws:s3:::") {
			return
		}
		if seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, a := range args {
		add(a)
	}
	for _, arn := range findArnsInArgsJSON(args) {
		add(arn)
	}
	return out
}

func findRoleArnsInArgsJSON(args []string) []string {
	var out []string
	for _, a := range args {
		a = strings.TrimSpace(a)
		if len(a) < 2 {
			continue
		}
		if !(strings.HasPrefix(a, "{") || strings.HasPrefix(a, "[")) {
			continue
		}
		if len(a) > 20000 {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(a), &v); err != nil {
			continue
		}
		out = append(out, glueFindRoleArnsInJSON(v)...)
	}
	return dedupeStrings(out)
}

func findArnsInArgsJSON(args []string) []string {
	var out []string
	for _, a := range args {
		a = strings.TrimSpace(a)
		if len(a) < 2 {
			continue
		}
		if !(strings.HasPrefix(a, "{") || strings.HasPrefix(a, "[")) {
			continue
		}
		if len(a) > 20000 {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(a), &v); err != nil {
			continue
		}
		out = append(out, findArnsInJSON(v)...)
	}
	return dedupeStrings(out)
}

func glueFindRoleArnsInJSON(v any) []string {
	var out []string
	switch vv := v.(type) {
	case map[string]any:
		for k, vvv := range vv {
			kl := strings.ToLower(strings.TrimSpace(k))
			if strings.Contains(kl, "rolearn") {
				if ss, ok := vvv.(string); ok {
					ss = strings.TrimSpace(ss)
					if strings.HasPrefix(ss, "arn:") && strings.Contains(ss, ":role/") {
						out = append(out, ss)
					}
				}
			}
			out = append(out, glueFindRoleArnsInJSON(vvv)...)
		}
	case []any:
		for _, item := range vv {
			out = append(out, glueFindRoleArnsInJSON(item)...)
		}
	}
	return out
}

func findArnsInJSON(v any) []string {
	var out []string
	switch vv := v.(type) {
	case map[string]any:
		for _, vvv := range vv {
			out = append(out, findArnsInJSON(vvv)...)
		}
	case []any:
		for _, item := range vv {
			out = append(out, findArnsInJSON(item)...)
		}
	case string:
		s := strings.TrimSpace(vv)
		if strings.HasPrefix(s, "arn:") {
			out = append(out, s)
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func extractS3BucketFromURI(args []string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "s3://") {
			bucket := strings.TrimPrefix(a, "s3://")
			if idx := strings.Index(bucket, "/"); idx >= 0 {
				bucket = bucket[:idx]
			}
			return strings.TrimSpace(bucket)
		}
	}
	return ""
}

func emptyS3Bucket(ctx context.Context, opts ExecOptions, bucket string, w io.Writer) error {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return fmt.Errorf("empty bucket")
	}

	// Try to delete versioned objects + delete markers.
	if err := deleteAllS3ObjectVersions(ctx, opts, bucket, w); err != nil {
		return err
	}

	// Then delete remaining (non-versioned) objects.
	if err := deleteAllS3Objects(ctx, opts, bucket, w); err != nil {
		return err
	}

	return nil
}

type s3ListObjectVersionsResp struct {
	Versions []struct {
		Key       string `json:"Key"`
		VersionID string `json:"VersionId"`
	} `json:"Versions"`
	DeleteMarkers []struct {
		Key       string `json:"Key"`
		VersionID string `json:"VersionId"`
	} `json:"DeleteMarkers"`
	NextToken string `json:"NextToken"`
}

type s3ListObjectsV2Resp struct {
	Contents []struct {
		Key string `json:"Key"`
	} `json:"Contents"`
	NextToken string `json:"NextToken"`
}

func deleteAllS3ObjectVersions(ctx context.Context, opts ExecOptions, bucket string, w io.Writer) error {
	startingToken := ""
	for {
		args := []string{"s3api", "list-object-versions", "--bucket", bucket, "--output", "json", "--max-items", "1000", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		if startingToken != "" {
			args = append(args, "--starting-token", startingToken)
		}
		out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
		if err != nil {
			lower := strings.ToLower(out)
			if strings.Contains(lower, "nosuchbucket") {
				return nil
			}
			return err
		}

		var resp s3ListObjectVersionsResp
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return err
		}

		objs := make([]map[string]string, 0, len(resp.Versions)+len(resp.DeleteMarkers))
		for _, v := range resp.Versions {
			if strings.TrimSpace(v.Key) == "" || strings.TrimSpace(v.VersionID) == "" {
				continue
			}
			objs = append(objs, map[string]string{"Key": v.Key, "VersionId": v.VersionID})
		}
		for _, d := range resp.DeleteMarkers {
			if strings.TrimSpace(d.Key) == "" || strings.TrimSpace(d.VersionID) == "" {
				continue
			}
			objs = append(objs, map[string]string{"Key": d.Key, "VersionId": d.VersionID})
		}
		if len(objs) > 0 {
			payload := map[string]any{"Objects": objs, "Quiet": true}
			b, _ := json.Marshal(payload)
			del := []string{"s3api", "delete-objects", "--bucket", bucket, "--delete", string(b), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
			_, _ = fmt.Fprintf(w, "[maker] note: deleting s3 object versions (count=%d)\n", len(objs))
			if _, err := runAWSCommandStreaming(ctx, del, nil, w); err != nil {
				return err
			}
		}

		if strings.TrimSpace(resp.NextToken) == "" {
			return nil
		}
		startingToken = strings.TrimSpace(resp.NextToken)
	}
}

func deleteAllS3Objects(ctx context.Context, opts ExecOptions, bucket string, w io.Writer) error {
	startingToken := ""
	for {
		args := []string{"s3api", "list-objects-v2", "--bucket", bucket, "--output", "json", "--max-items", "1000", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		if startingToken != "" {
			args = append(args, "--starting-token", startingToken)
		}
		out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
		if err != nil {
			lower := strings.ToLower(out)
			if strings.Contains(lower, "nosuchbucket") {
				return nil
			}
			return err
		}
		var resp s3ListObjectsV2Resp
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			return err
		}

		objs := make([]map[string]string, 0, len(resp.Contents))
		for _, c := range resp.Contents {
			if strings.TrimSpace(c.Key) == "" {
				continue
			}
			objs = append(objs, map[string]string{"Key": c.Key})
		}
		if len(objs) > 0 {
			payload := map[string]any{"Objects": objs, "Quiet": true}
			b, _ := json.Marshal(payload)
			del := []string{"s3api", "delete-objects", "--bucket", bucket, "--delete", string(b), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
			_, _ = fmt.Fprintf(w, "[maker] note: deleting s3 objects (count=%d)\n", len(objs))
			if _, err := runAWSCommandStreaming(ctx, del, nil, w); err != nil {
				return err
			}
		}

		if strings.TrimSpace(resp.NextToken) == "" {
			return nil
		}
		startingToken = strings.TrimSpace(resp.NextToken)
	}
}

func resolveAndDeleteIAMPolicy(ctx context.Context, opts ExecOptions, policyArn string, w io.Writer) error {
	policyArn = strings.TrimSpace(policyArn)
	if policyArn == "" {
		return fmt.Errorf("empty policy arn")
	}

	// Detach from all entities.
	if err := detachAllEntitiesForPolicy(ctx, opts, policyArn, w); err != nil {
		return err
	}

	// Delete non-default versions.
	if err := deleteAllNonDefaultPolicyVersions(ctx, opts, policyArn, w); err != nil {
		return err
	}

	// Retry delete-policy.
	deleteArgs := []string{"iam", "delete-policy", "--policy-arn", policyArn, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, deleteArgs, nil, w)
	if err != nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "nosuchentity") {
			return nil
		}
		return err
	}
	return nil
}

func detachAllEntitiesForPolicy(ctx context.Context, opts ExecOptions, policyArn string, w io.Writer) error {
	args := []string{"iam", "list-entities-for-policy", "--policy-arn", policyArn, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if err != nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "nosuchentity") {
			return nil
		}
		return err
	}
	var resp struct {
		PolicyGroups []struct{ GroupName string } `json:"PolicyGroups"`
		PolicyUsers  []struct{ UserName string }  `json:"PolicyUsers"`
		PolicyRoles  []struct{ RoleName string }  `json:"PolicyRoles"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return err
	}

	for _, r := range resp.PolicyRoles {
		role := strings.TrimSpace(r.RoleName)
		if role == "" {
			continue
		}
		detach := []string{"iam", "detach-role-policy", "--role-name", role, "--policy-arn", policyArn, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(w, "[maker] note: detaching policy from role (role=%s)\n", role)
		_, _ = runAWSCommandStreaming(ctx, detach, nil, w)
	}
	for _, u := range resp.PolicyUsers {
		user := strings.TrimSpace(u.UserName)
		if user == "" {
			continue
		}
		detach := []string{"iam", "detach-user-policy", "--user-name", user, "--policy-arn", policyArn, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(w, "[maker] note: detaching policy from user (user=%s)\n", user)
		_, _ = runAWSCommandStreaming(ctx, detach, nil, w)
	}
	for _, g := range resp.PolicyGroups {
		group := strings.TrimSpace(g.GroupName)
		if group == "" {
			continue
		}
		detach := []string{"iam", "detach-group-policy", "--group-name", group, "--policy-arn", policyArn, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(w, "[maker] note: detaching policy from group (group=%s)\n", group)
		_, _ = runAWSCommandStreaming(ctx, detach, nil, w)
	}

	return nil
}

func deleteAllNonDefaultPolicyVersions(ctx context.Context, opts ExecOptions, policyArn string, w io.Writer) error {
	args := []string{"iam", "list-policy-versions", "--policy-arn", policyArn, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if err != nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "nosuchentity") {
			return nil
		}
		return err
	}
	var resp struct {
		Versions []struct {
			VersionID        string `json:"VersionId"`
			IsDefaultVersion bool   `json:"IsDefaultVersion"`
		} `json:"Versions"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return err
	}
	for _, v := range resp.Versions {
		vid := strings.TrimSpace(v.VersionID)
		if vid == "" || v.IsDefaultVersion {
			continue
		}
		del := []string{"iam", "delete-policy-version", "--policy-arn", policyArn, "--version-id", vid, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(w, "[maker] note: deleting non-default policy version (version=%s)\n", vid)
		_, _ = runAWSCommandStreaming(ctx, del, nil, w)
	}
	return nil
}

func isTransientFailure(f AWSFailure, output string) bool {
	code := strings.TrimSpace(f.Code)
	if code == "ServiceUnavailableException" ||
		code == "ServiceUnavailable" ||
		code == "InternalFailure" ||
		code == "InternalError" ||
		code == "RequestTimeout" ||
		code == "RequestTimeoutException" ||
		code == "TooManyUpdates" ||
		code == "TooManyUpdatesException" ||
		code == "TransactionInProgressException" {
		return true
	}

	lower := strings.ToLower(output)
	return strings.Contains(lower, "service unavailable") ||
		strings.Contains(lower, "internalerror") ||
		strings.Contains(lower, "internal failure") ||
		strings.Contains(lower, "request timeout") ||
		strings.Contains(lower, "temporarily unavailable")
}

func waitForEKSClusterActive(ctx context.Context, opts ExecOptions, name string, w io.Writer) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty eks cluster name")
	}

	args := []string{"eks", "wait", "cluster-active", "--name", name, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	_, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	return err
}

func waitForELBv2LoadBalancerActive(ctx context.Context, opts ExecOptions, loadBalancerArn string, w io.Writer) error {
	loadBalancerArn = strings.TrimSpace(loadBalancerArn)
	if loadBalancerArn == "" {
		return fmt.Errorf("empty load balancer arn")
	}
	for attempt := 1; attempt <= 40; attempt++ {
		q := []string{"elbv2", "describe-load-balancers", "--load-balancer-arns", loadBalancerArn, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err == nil {
			var resp struct {
				LoadBalancers []struct {
					State struct {
						Code string `json:"Code"`
					} `json:"State"`
				} `json:"LoadBalancers"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				if len(resp.LoadBalancers) > 0 {
					st := strings.ToLower(strings.TrimSpace(resp.LoadBalancers[0].State.Code))
					if st == "active" {
						return nil
					}
					if st == "failed" {
						return fmt.Errorf("load balancer entered failed state")
					}
				}
			}
		}
		_, _ = fmt.Fprintf(w, "[maker] note: waiting for load balancer active (attempt=%d)\n", attempt)
		time.Sleep(time.Duration(attempt) * 650 * time.Millisecond)
	}
	return nil
}

func firstACMCertificateArnInArgs(args []string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if strings.Contains(a, "arn:aws:acm:") {
			idx := strings.Index(a, "arn:aws:acm:")
			if idx >= 0 {
				s := a[idx:]
				// Truncate on obvious delimiters.
				for _, d := range []string{",", "\"", "'", "]", "}", " ", "\t"} {
					if j := strings.Index(s, d); j > 0 {
						s = s[:j]
					}
				}
				return strings.TrimSpace(s)
			}
		}
	}
	for _, arn := range findArnsInArgsJSON(args) {
		if strings.Contains(arn, ":acm:") {
			return strings.TrimSpace(arn)
		}
	}
	return ""
}

func waitForACMCertificateIssued(ctx context.Context, opts ExecOptions, certificateArn string, w io.Writer) error {
	certificateArn = strings.TrimSpace(certificateArn)
	if certificateArn == "" {
		return fmt.Errorf("empty certificate arn")
	}
	attemptedDNS := false
	for attempt := 1; attempt <= 60; attempt++ {
		q := []string{"acm", "describe-certificate", "--certificate-arn", certificateArn, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err == nil {
			var resp struct {
				Certificate struct {
					Status                  string `json:"Status"`
					DomainValidationOptions []struct {
						DomainName       string `json:"DomainName"`
						ValidationStatus string `json:"ValidationStatus"`
						ResourceRecord   struct {
							Name  string `json:"Name"`
							Type  string `json:"Type"`
							Value string `json:"Value"`
						} `json:"ResourceRecord"`
					} `json:"DomainValidationOptions"`
				} `json:"Certificate"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				st := strings.ToUpper(strings.TrimSpace(resp.Certificate.Status))
				switch st {
				case "ISSUED":
					return nil
				case "FAILED", "EXPIRED", "REVOKED":
					return fmt.Errorf("certificate not usable (status=%s)", st)
				case "PENDING_VALIDATION":
					// Best-effort: try to UPSERT DNS validation records into Route53.
					if !attemptedDNS {
						attemptedDNS = true
						if ok, _ := ensureACMDNSValidationRecords(ctx, opts, resp.Certificate.DomainValidationOptions, w); ok {
							_, _ = fmt.Fprintf(w, "[maker] note: acm dns validation records upserted; continuing to wait\n")
						}
					}
				}
			}
		}
		_, _ = fmt.Fprintf(w, "[maker] note: waiting for acm certificate issued (attempt=%d)\n", attempt)
		time.Sleep(time.Duration(attempt) * 700 * time.Millisecond)
	}
	return nil
}

func ensureACMDNSValidationRecords(ctx context.Context, opts ExecOptions, dvos []struct {
	DomainName       string `json:"DomainName"`
	ValidationStatus string `json:"ValidationStatus"`
	ResourceRecord   struct {
		Name  string `json:"Name"`
		Type  string `json:"Type"`
		Value string `json:"Value"`
	} `json:"ResourceRecord"`
}, w io.Writer) (bool, error) {
	// Load all hosted zones once; choose best match per record.
	zones, err := listRoute53HostedZones(ctx, opts)
	if err != nil {
		return false, err
	}
	if len(zones) == 0 {
		return false, nil
	}

	changed := false
	for _, dvo := range dvos {
		rrName := strings.TrimSpace(dvo.ResourceRecord.Name)
		rrType := strings.TrimSpace(dvo.ResourceRecord.Type)
		rrValue := strings.TrimSpace(dvo.ResourceRecord.Value)
		if rrName == "" || rrType == "" || rrValue == "" {
			continue
		}

		zoneID, zoneName := chooseHostedZoneForRecord(zones, rrName)
		if zoneID == "" {
			continue
		}

		batch := map[string]any{
			"Comment": "clanker: auto acm dns validation",
			"Changes": []map[string]any{
				{
					"Action": "UPSERT",
					"ResourceRecordSet": map[string]any{
						"Name":            rrName,
						"Type":            rrType,
						"TTL":             300,
						"ResourceRecords": []map[string]string{{"Value": rrValue}},
					},
				},
			},
		}
		b, _ := json.Marshal(batch)

		cmd := []string{"route53", "change-resource-record-sets", "--hosted-zone-id", zoneID, "--change-batch", string(b), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(w, "[maker] note: upserting acm dns validation record (zone=%s name=%s type=%s)\n", zoneName, rrName, rrType)
		// Best-effort: UPSERT is idempotent.
		_, _ = runAWSCommandStreaming(ctx, cmd, nil, w)
		changed = true
	}

	return changed, nil
}

type route53HostedZone struct {
	ID   string
	Name string
}

func listRoute53HostedZones(ctx context.Context, opts ExecOptions) ([]route53HostedZone, error) {
	q := []string{"route53", "list-hosted-zones", "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return nil, err
	}
	var resp struct {
		HostedZones []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"HostedZones"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	zones := make([]route53HostedZone, 0, len(resp.HostedZones))
	for _, z := range resp.HostedZones {
		id := strings.TrimSpace(z.ID)
		name := strings.TrimSpace(z.Name)
		id = strings.TrimPrefix(id, "/hostedzone/")
		if id == "" || name == "" {
			continue
		}
		zones = append(zones, route53HostedZone{ID: id, Name: name})
	}
	return zones, nil
}

func chooseHostedZoneForRecord(zones []route53HostedZone, recordName string) (zoneID string, zoneName string) {
	rn := strings.ToLower(strings.TrimSpace(recordName))
	rn = strings.TrimSuffix(rn, ".")
	bestLen := -1
	best := route53HostedZone{}
	for _, z := range zones {
		zn := strings.ToLower(strings.TrimSpace(z.Name))
		zn = strings.TrimSuffix(zn, ".")
		if zn == "" {
			continue
		}
		if rn == zn || strings.HasSuffix(rn, "."+zn) {
			if len(zn) > bestLen {
				bestLen = len(zn)
				best = z
			}
		}
	}
	if bestLen < 0 {
		return "", ""
	}
	return strings.TrimSpace(best.ID), strings.TrimSpace(best.Name)
}

func rewriteRoute53ChangeBatchCreateToUpsert(args []string, lowerOutput string) ([]string, bool) {
	// Only trigger when the error looks like an RRset already existing / invalid change batch.
	if !(strings.Contains(lowerOutput, "already") && strings.Contains(lowerOutput, "exist")) && !strings.Contains(lowerOutput, "invalidchangebatch") {
		return nil, false
	}

	idx := indexOfExactFlag(args, "--change-batch")
	if idx < 0 || idx+1 >= len(args) {
		return nil, false
	}
	raw := strings.TrimSpace(args[idx+1])
	if raw == "" {
		return nil, false
	}
	if len(raw) > 20000 {
		return nil, false
	}
	if !(strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[")) {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, false
	}
	changed := false
	vv, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	cb, ok := vv["Changes"].([]any)
	if !ok {
		// Some callers use lowercase keys.
		cb, _ = vv["changes"].([]any)
	}
	for i := range cb {
		m, ok := cb[i].(map[string]any)
		if !ok {
			continue
		}
		act, _ := m["Action"].(string)
		if strings.ToUpper(strings.TrimSpace(act)) == "CREATE" {
			m["Action"] = "UPSERT"
			changed = true
		}
		if act2, _ := m["action"].(string); strings.ToUpper(strings.TrimSpace(act2)) == "CREATE" {
			m["action"] = "UPSERT"
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	// Write back.
	if vv["Changes"] != nil {
		vv["Changes"] = cb
	}
	if vv["changes"] != nil {
		vv["changes"] = cb
	}
	b, err := json.Marshal(vv)
	if err != nil {
		return nil, false
	}
	out := append([]string{}, args...)
	out[idx+1] = string(b)
	return out, true
}

func waitForEFSFileSystemAvailable(ctx context.Context, opts ExecOptions, fileSystemID string, w io.Writer) error {
	fileSystemID = strings.TrimSpace(fileSystemID)
	if fileSystemID == "" {
		return fmt.Errorf("empty efs file system id")
	}
	for attempt := 1; attempt <= 30; attempt++ {
		q := []string{"efs", "describe-file-systems", "--file-system-id", fileSystemID, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err == nil {
			var resp struct {
				FileSystems []struct {
					LifeCycleState string `json:"LifeCycleState"`
				} `json:"FileSystems"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				if len(resp.FileSystems) > 0 {
					st := strings.ToLower(strings.TrimSpace(resp.FileSystems[0].LifeCycleState))
					if st == "available" {
						return nil
					}
				}
			}
		}
		_, _ = fmt.Fprintf(w, "[maker] note: waiting for efs file system available (attempt=%d)\n", attempt)
		time.Sleep(time.Duration(attempt) * 700 * time.Millisecond)
	}
	return nil
}

func waitForOpenSearchDomainReady(ctx context.Context, opts ExecOptions, domainName string, w io.Writer) error {
	domainName = strings.TrimSpace(domainName)
	if domainName == "" {
		return fmt.Errorf("empty opensearch domain name")
	}
	for attempt := 1; attempt <= 40; attempt++ {
		q := []string{"opensearch", "describe-domain", "--domain-name", domainName, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err == nil {
			var resp struct {
				DomainStatus struct {
					Processing bool `json:"Processing"`
					Created    bool `json:"Created"`
				} `json:"DomainStatus"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				if resp.DomainStatus.Created && !resp.DomainStatus.Processing {
					return nil
				}
			}
		}
		_, _ = fmt.Fprintf(w, "[maker] note: waiting for opensearch domain ready (attempt=%d)\n", attempt)
		time.Sleep(time.Duration(attempt) * 800 * time.Millisecond)
	}
	return nil
}

func waitForMSKClusterActive(ctx context.Context, opts ExecOptions, clusterArn string, w io.Writer) error {
	clusterArn = strings.TrimSpace(clusterArn)
	if clusterArn == "" {
		return fmt.Errorf("empty msk cluster arn")
	}
	for attempt := 1; attempt <= 50; attempt++ {
		q := []string{"kafka", "describe-cluster", "--cluster-arn", clusterArn, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err == nil {
			var resp struct {
				ClusterInfo struct {
					State string `json:"State"`
				} `json:"ClusterInfo"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				st := strings.ToLower(strings.TrimSpace(resp.ClusterInfo.State))
				if st == "active" {
					return nil
				}
			}
		}
		_, _ = fmt.Fprintf(w, "[maker] note: waiting for msk cluster active (attempt=%d)\n", attempt)
		time.Sleep(time.Duration(attempt) * 900 * time.Millisecond)
	}
	return nil
}

func waitForEKSNodegroupActive(ctx context.Context, opts ExecOptions, clusterName string, nodegroupName string, w io.Writer) error {
	clusterName = strings.TrimSpace(clusterName)
	nodegroupName = strings.TrimSpace(nodegroupName)
	if clusterName == "" || nodegroupName == "" {
		return fmt.Errorf("empty eks cluster/nodegroup")
	}

	args := []string{"eks", "wait", "nodegroup-active", "--cluster-name", clusterName, "--nodegroup-name", nodegroupName, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	_, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	return err
}

func waitForCloudFrontDistributionDeployed(ctx context.Context, opts ExecOptions, id string, w io.Writer) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("empty cloudfront distribution id")
	}

	// CloudFront is global; the CLI still accepts region flags but ignores them.
	args := []string{"cloudfront", "wait", "distribution-deployed", "--id", id, "--profile", opts.Profile, "--no-cli-pager"}
	_, err := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	return err
}

func waitForLambdaFunctionActive(ctx context.Context, opts ExecOptions, functionName string, w io.Writer) error {
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return fmt.Errorf("empty lambda function name")
	}

	return retryWithBackoff(ctx, w, 7, func() (string, error) {
		q := []string{
			"lambda",
			"get-function-configuration",
			"--function-name",
			functionName,
			"--query",
			"State",
			"--output",
			"text",
			"--profile",
			opts.Profile,
			"--region",
			opts.Region,
			"--no-cli-pager",
		}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err != nil {
			return out, err
		}
		state := strings.TrimSpace(out)
		if strings.EqualFold(state, "Active") {
			return out, nil
		}
		return out, fmt.Errorf("lambda not active yet (state=%s)", state)
	})
}

func waitForDynamoDBTableActive(ctx context.Context, opts ExecOptions, tableName string, w io.Writer) error {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return fmt.Errorf("empty dynamodb table name")
	}

	return retryWithBackoff(ctx, w, 8, func() (string, error) {
		q := []string{
			"dynamodb",
			"describe-table",
			"--table-name",
			tableName,
			"--query",
			"Table.TableStatus",
			"--output",
			"text",
			"--profile",
			opts.Profile,
			"--region",
			opts.Region,
			"--no-cli-pager",
		}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err != nil {
			return out, err
		}
		status := strings.TrimSpace(out)
		if strings.EqualFold(status, "ACTIVE") {
			return out, nil
		}
		return out, fmt.Errorf("dynamodb table not active yet (status=%s)", status)
	})
}

func waitForRDSInstanceAvailable(ctx context.Context, opts ExecOptions, id string, w io.Writer) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("empty rds db instance identifier")
	}

	return retryWithBackoff(ctx, w, 9, func() (string, error) {
		q := []string{
			"rds",
			"describe-db-instances",
			"--db-instance-identifier",
			id,
			"--query",
			"DBInstances[0].DBInstanceStatus",
			"--output",
			"text",
			"--profile",
			opts.Profile,
			"--region",
			opts.Region,
			"--no-cli-pager",
		}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err != nil {
			return out, err
		}
		status := strings.TrimSpace(out)
		if strings.EqualFold(status, "available") {
			return out, nil
		}
		return out, fmt.Errorf("rds instance not available yet (status=%s)", status)
	})
}

func hasExactFlag(args []string, flag string) bool {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return false
	}
	for _, a := range args {
		if a == flag {
			return true
		}
		if strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

func rewriteAPIGatewayV2CreateApiLambdaTargetFunctionNameToArn(ctx context.Context, opts ExecOptions, args []string) ([]string, bool) {
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		return nil, false
	}

	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		val := ""
		isTarget := false
		if out[i] == "--target" {
			isTarget = true
			if i+1 < len(out) {
				val = strings.TrimSpace(out[i+1])
			}
		} else if strings.HasPrefix(out[i], "--target=") {
			isTarget = true
			val = strings.TrimSpace(strings.TrimPrefix(out[i], "--target="))
		}
		if !isTarget || val == "" {
			continue
		}

		// Already an ARN, nothing to do.
		if strings.HasPrefix(val, "arn:") {
			return nil, false
		}

		accountID, err := resolveAWSAccountID(ctx, opts)
		if err != nil {
			return nil, false
		}
		fn := val
		fixed := fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, accountID, fn)
		if out[i] == "--target" {
			out[i+1] = fixed
			return out, true
		}
		out[i] = "--target=" + fixed
		return out, true
	}
	return nil, false
}

func retryWithBackoffOutput(ctx context.Context, w io.Writer, attempts int, fn func() (string, error)) (string, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	lastOut := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			sleep := time.Duration(1<<uint(attempt-1)) * time.Second
			_, _ = fmt.Fprintf(w, "[maker] note: retrying after backoff (attempt=%d sleep=%s)\n", attempt, sleep)
			select {
			case <-ctx.Done():
				return lastOut, ctx.Err()
			case <-time.After(sleep):
			}
		}
		out, err := fn()
		lastOut = out
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return lastOut, lastErr
}

func retryWithBackoff(ctx context.Context, w io.Writer, attempts int, fn func() (string, error)) error {
	_, err := retryWithBackoffOutput(ctx, w, attempts, fn)
	return err
}

func remediateEC2InvalidInstanceProfileAndRetry(ctx context.Context, opts ExecOptions, args []string, stdinBytes []byte, w io.Writer, bindings map[string]string) error {
	name := extractEC2RunInstancesInstanceProfileName(args)
	if name == "" {
		return fmt.Errorf("cannot remediate: missing --iam-instance-profile name")
	}

	for attempt := 1; attempt <= 6; attempt++ {
		if attempt > 1 {
			sleep := time.Duration(1<<uint(attempt-1)) * time.Second
			_, _ = fmt.Fprintf(w, "[maker] note: waiting for IAM instance profile propagation (attempt=%d sleep=%s)\n", attempt, sleep)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}

		arn, arnErr := getInstanceProfileArn(ctx, opts, name)
		if arnErr != nil || strings.TrimSpace(arn) == "" {
			continue
		}

		rewritten, ok := rewriteEC2RunInstancesIamInstanceProfileToArn(args, arn)
		if !ok {
			return fmt.Errorf("cannot remediate: failed to rewrite --iam-instance-profile")
		}

		// Also generate user-data if needed (it may have been skipped earlier due to missing bindings)
		rewritten = maybeGenerateEC2UserData(rewritten, bindings, opts)

		_, _ = fmt.Fprintf(w, "[maker] remediation attempted: rewriting ec2 run-instances --iam-instance-profile to use Arn=... and retrying\n")
		rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, w)
		if err == nil {
			// Learn bindings from successful run-instances output
			learnPlanBindings(rewritten, out, bindings)
			return nil
		}

		lower := strings.ToLower(out)
		if !(strings.Contains(lower, "iaminstanceprofile") && strings.Contains(lower, "invalid") && strings.Contains(lower, "instance profile")) {
			return err
		}
	}

	return fmt.Errorf("instance profile still not usable after retries (name=%s)", name)
}

func extractEC2RunInstancesInstanceProfileName(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--iam-instance-profile" {
			if i+1 >= len(args) {
				return ""
			}
			val := strings.TrimSpace(args[i+1])
			if strings.HasPrefix(val, "Name=") {
				return strings.TrimSpace(strings.TrimPrefix(val, "Name="))
			}
			if strings.HasPrefix(val, "name=") {
				return strings.TrimSpace(strings.TrimPrefix(val, "name="))
			}
			return ""
		}
		if strings.HasPrefix(args[i], "--iam-instance-profile=") {
			val := strings.TrimSpace(strings.TrimPrefix(args[i], "--iam-instance-profile="))
			if strings.HasPrefix(val, "Name=") {
				return strings.TrimSpace(strings.TrimPrefix(val, "Name="))
			}
			if strings.HasPrefix(val, "name=") {
				return strings.TrimSpace(strings.TrimPrefix(val, "name="))
			}
			return ""
		}
	}
	return ""
}

func getInstanceProfileArn(ctx context.Context, opts ExecOptions, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty instance profile name")
	}

	getArgs := []string{
		"iam",
		"get-instance-profile",
		"--instance-profile-name",
		name,
		"--query",
		"InstanceProfile.Arn",
		"--output",
		"text",
		"--profile",
		opts.Profile,
		"--region",
		opts.Region,
		"--no-cli-pager",
	}
	out, err := runAWSCommandStreaming(ctx, getArgs, nil, io.Discard)
	if err != nil {
		return "", err
	}
	arn := strings.TrimSpace(out)
	if arn == "None" {
		arn = ""
	}
	return arn, nil
}

func rewriteEC2RunInstancesIamInstanceProfileToArn(args []string, arn string) ([]string, bool) {
	arn = strings.TrimSpace(arn)
	if arn == "" {
		return nil, false
	}

	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		if out[i] == "--iam-instance-profile" {
			if i+1 >= len(out) {
				return nil, false
			}
			out[i+1] = "Arn=" + arn
			return out, true
		}
		if strings.HasPrefix(out[i], "--iam-instance-profile=") {
			out[i] = "--iam-instance-profile=Arn=" + arn
			return out, true
		}
	}
	return nil, false
}

func rewriteAPIGatewayV2CreateApiLambdaTarget(args []string, region string) ([]string, bool) {
	if strings.TrimSpace(region) == "" {
		return nil, false
	}

	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		var isTarget bool
		val := ""
		if out[i] == "--target" {
			isTarget = true
			if i+1 < len(out) {
				val = out[i+1]
			}
		} else if strings.HasPrefix(out[i], "--target=") {
			isTarget = true
			val = strings.TrimPrefix(out[i], "--target=")
		}

		if !isTarget || strings.TrimSpace(val) == "" {
			continue
		}

		m := lambdaArnMissingRegionRe.FindStringSubmatch(strings.TrimSpace(val))
		if len(m) != 4 {
			continue
		}
		partition := strings.TrimSpace(m[1])
		acct := strings.TrimSpace(m[2])
		fn := strings.TrimSpace(m[3])
		if partition == "" || acct == "" || fn == "" {
			continue
		}

		fixed := fmt.Sprintf("arn:%s:lambda:%s:%s:function:%s", partition, region, acct, fn)
		if out[i] == "--target" {
			out[i+1] = fixed
			return out, true
		}
		out[i] = "--target=" + fixed
		return out, true
	}

	return nil, false
}

func args0(args []string) string {
	if len(args) < 1 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

func args1(args []string) string {
	if len(args) < 2 {
		return ""
	}
	return strings.TrimSpace(args[1])
}
