package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

func maybeGenericGlueAndRetry(
	ctx context.Context,
	opts ExecOptions,
	args []string,
	awsArgs []string,
	stdinBytes []byte,
	failure AWSFailure,
	output string,
) (bool, error) {
	if ok, err := maybeRemediateSingletonAssociation(ctx, opts, args, awsArgs, stdinBytes, failure, output); ok {
		return true, err
	}

	// Generic cross-service glue ("support all services")
	//
	// 1) Idempotency for delete-like operations: treat not-found as success.
	if failure.Category == FailureNotFound {
		op := strings.ToLower(strings.TrimSpace(args1(args)))
		if strings.HasPrefix(op, "delete-") || strings.HasPrefix(op, "remove-") || strings.HasPrefix(op, "detach-") || strings.HasPrefix(op, "disassociate-") {
			return true, nil
		}
	}

	// 2) Generic create->update/put fallback.
	// Many AWS APIs are create-once and then update/put; model plans often re-apply.
	// If create-* already exists (or conflicts), try update-* then put-* with the same args.
	if (failure.Category == FailureAlreadyExists || failure.Category == FailureConflict) && len(args) >= 2 {
		service := strings.TrimSpace(args0(args))
		op := strings.ToLower(strings.TrimSpace(args1(args)))
		if service != "" && strings.HasPrefix(op, "create-") && service != "s3" && service != "s3api" {
			suffix := strings.TrimPrefix(op, "create-")
			if suffix != "" {
				for _, candidate := range []string{"update-" + suffix, "put-" + suffix} {
					rewritten := append([]string{}, args...)
					rewritten[1] = candidate
					rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
					_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: rewrite %s %s -> %s then retry\n", service, op, candidate)
					out2, err2 := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, opts.Writer)
					if err2 == nil {
						return true, nil
					}
					// If the CLI doesn't know this operation, try the next candidate.
					if isAWSCLIInvalidOperationOutput(out2) {
						continue
					}
					// If it failed for another reason, fall back to the normal pipeline.
					break
				}
			}
		}
	}

	// 3) Generic "describe/get" polling when we have an ARN.
	// For unknown services, the only universal describe-like mechanism is the tagging API.
	// If the command references a resource ARN and we hit not-found/conflict during follow-on ops,
	// wait until the ARN is visible, then retry.
	if (failure.Category == FailureNotFound || failure.Category == FailureConflict) && looksLikeFollowOnOp(failure.Op) {
		arn := firstArnInArgsOrJSON(args)
		if arn != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: wait for arn visible then retry (arn=%s)\n", arn)
			_ = waitForArnVisibleViaTaggingAPI(ctx, opts, arn, opts.Writer)

			err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
				return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
			})
			if err == nil {
				return true, nil
			}

			if err2 := maybeLLMAfterGenericExhausted(ctx, opts, awsArgs, stdinBytes, output, "arn-visibility"); err2 != nil {
				return true, err2
			}
			return true, nil
		}
	}

	// 4) Idempotency for create-like operations: treat already-exists as success.
	if failure.Category == FailureAlreadyExists {
		op := strings.ToLower(strings.TrimSpace(args1(args)))
		// Skip dangerous conversions here; special-case conversions already handled elsewhere.
		if strings.HasPrefix(op, "create-") {
			return true, nil
		}
	}

	// 5) Propagation/in-progress retries across any service.
	// Try to avoid per-service glue explosions: if the output looks like eventual consistency
	// or a resource still processing, retry with backoff.
	if shouldGenericRetryAfterPropagation(failure, output) {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry %s %s after generic propagation/in-progress\n", args0(args), args1(args))
		err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
			return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		})
		if err == nil {
			return true, nil
		}

		if err2 := maybeLLMAfterGenericExhausted(ctx, opts, awsArgs, stdinBytes, output, "generic-propagation"); err2 != nil {
			return true, err2
		}
		return true, nil
	}

	return false, nil
}

func maybeRemediateSingletonAssociation(
	ctx context.Context,
	opts ExecOptions,
	args []string,
	awsArgs []string,
	stdinBytes []byte,
	failure AWSFailure,
	output string,
) (bool, error) {
	if args0(args) != "iam" || args1(args) != "add-role-to-instance-profile" {
		return false, nil
	}

	lower := strings.ToLower(output)
	if failure.Code != "LimitExceeded" && !strings.Contains(lower, "instancesessionsperinstanceprofile") {
		return false, nil
	}

	profileName := strings.TrimSpace(flagValue(args, "--instance-profile-name"))
	desiredRole := strings.TrimSpace(flagValue(args, "--role-name"))
	if profileName == "" || desiredRole == "" {
		return false, nil
	}

	attachedRoles, err := getInstanceProfileRoleNames(ctx, opts, profileName)
	if err != nil {
		return true, err
	}

	for _, roleName := range attachedRoles {
		if roleName == desiredRole {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: instance profile already has desired role attached; treating as success (instanceProfile=%s role=%s)\n", profileName, desiredRole)
			return true, nil
		}
	}

	if len(attachedRoles) == 0 {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: instance profile role appears stale; retrying add-role with backoff (instanceProfile=%s role=%s)\n", profileName, desiredRole)
		err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
			return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		})
		return true, err
	}

	if !opts.Destroyer {
		return true, fmt.Errorf("instance profile %s already has role %s attached; safe-first mode will not replace it without destroyer approval (wanted role=%s)", profileName, attachedRoles[0], desiredRole)
	}

	for _, roleName := range attachedRoles {
		if strings.TrimSpace(roleName) == "" {
			continue
		}
		remove := []string{"iam", "remove-role-from-instance-profile", "--instance-profile-name", profileName, "--role-name", roleName, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: replacing attached role on instance profile (instanceProfile=%s oldRole=%s newRole=%s)\n", profileName, roleName, desiredRole)
		if _, removeErr := runAWSCommandStreaming(ctx, remove, nil, opts.Writer); removeErr != nil {
			return true, removeErr
		}
	}

	if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
		return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
	}); err != nil {
		return true, err
	}

	return true, nil
}

func getInstanceProfileRoleNames(ctx context.Context, opts ExecOptions, profileName string) ([]string, error) {
	query := []string{
		"iam", "get-instance-profile",
		"--instance-profile-name", profileName,
		"--output", "json",
		"--profile", opts.Profile,
		"--region", opts.Region,
		"--no-cli-pager",
	}
	out, err := runAWSCommandStreaming(ctx, query, nil, io.Discard)
	if err != nil {
		return nil, err
	}

	var resp struct {
		InstanceProfile struct {
			Roles []struct {
				RoleName string `json:"RoleName"`
			} `json:"Roles"`
		} `json:"InstanceProfile"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}

	roles := make([]string, 0, len(resp.InstanceProfile.Roles))
	for _, role := range resp.InstanceProfile.Roles {
		name := strings.TrimSpace(role.RoleName)
		if name == "" {
			continue
		}
		roles = append(roles, name)
	}

	return roles, nil
}

func maybeLLMAfterGenericExhausted(
	ctx context.Context,
	opts ExecOptions,
	failedAWSArgs []string,
	stdinBytes []byte,
	failedOutput string,
	reason string,
) error {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return fmt.Errorf("generic glue exhausted (%s), and ai remediation disabled", reason)
	}

	augmentedOut := strings.TrimSpace(failedOutput)
	if augmentedOut != "" {
		augmentedOut += "\n\n"
	}
	augmentedOut += fmt.Sprintf("[maker] note: generic glue exhausted retries (reason=%s). propose minimal aws cli prerequisites to make the failing command succeed. then we will retry the original.", reason)

	rp, err := maybeRemediateWithAI(ctx, opts, failedAWSArgs, augmentedOut)
	if err != nil {
		return err
	}
	if rp == nil || len(rp.Commands) == 0 {
		return fmt.Errorf("generic glue exhausted (%s), ai returned no remediation", reason)
	}

	_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: AI after generic glue exhausted (reason=%s)\n", reason)
	for i, c := range rp.Commands {
		if err := validateCommand(c.Args, opts.Destroyer); err != nil {
			return fmt.Errorf("remediation command %d rejected: %w", i+1, err)
		}
		cmdArgs := append(append([]string{}, c.Args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		if _, err := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer); err != nil {
			return err
		}
	}

	// Retry original with exponential backoff 3 times.
	return retryWithBackoff(ctx, opts.Writer, 3, func() (string, error) {
		return runAWSCommandStreaming(ctx, failedAWSArgs, stdinBytes, opts.Writer)
	})
}

func shouldGenericRetryAfterPropagation(f AWSFailure, output string) bool {
	// Throttling and generic transients are already handled earlier; this is for "in progress"
	// and eventual consistency patterns that span many services.
	if f.Category == FailureThrottled {
		return false
	}
	if isTransientFailure(f, output) {
		return false
	}

	lower := strings.ToLower(output)
	if f.Category == FailureNotFound {
		// Do NOT generic-retry read-only operations when something truly doesn't exist.
		// These are usually hard failures (wrong name/tag/id), not eventual consistency.
		op := strings.ToLower(strings.TrimSpace(f.Op))
		if strings.HasPrefix(op, "describe-") || strings.HasPrefix(op, "get-") || strings.HasPrefix(op, "list-") {
			return false
		}

		// Only retry for follow-on operations that are likely to race with a create.
		// (We avoid retrying delete-like ops here.)
		if looksLikeFollowOnOp(strings.TrimSpace(f.Op)) {
			return true
		}
		// If we don't have an op, fallback to output patterns.
		if strings.TrimSpace(f.Op) == "" {
			if strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "resourcenotfound") {
				return true
			}
			return false
		}

		// Otherwise: known op, not a follow-on -> no generic retry.
		if strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "resourcenotfound") {
			return true
		}
	}

	// In-progress / conflict-ish states.
	if f.Category == FailureConflict {
		return true
	}
	return strings.Contains(lower, "in progress") ||
		strings.Contains(lower, "inprogress") ||
		strings.Contains(lower, "pending") ||
		strings.Contains(lower, "processing") ||
		strings.Contains(lower, "resourceinuse") ||
		strings.Contains(lower, "resource in use") ||
		strings.Contains(lower, "currently being modified") ||
		strings.Contains(lower, "another request") && strings.Contains(lower, "in progress") ||
		strings.Contains(lower, "try again") && strings.Contains(lower, "later") ||
		strings.Contains(lower, "eventual") && strings.Contains(lower, "consisten")
}

func looksLikeFollowOnOp(op string) bool {
	op = strings.ToLower(strings.TrimSpace(op))
	if op == "" {
		return false
	}
	// Common follow-on verbs used across AWS services.
	return strings.HasPrefix(op, "put-") ||
		strings.HasPrefix(op, "add-") ||
		strings.HasPrefix(op, "attach-") ||
		strings.HasPrefix(op, "associate-") ||
		strings.HasPrefix(op, "update-") ||
		strings.HasPrefix(op, "set-") ||
		strings.HasPrefix(op, "register-") ||
		strings.HasPrefix(op, "enable-") ||
		strings.HasPrefix(op, "disable-") ||
		strings.HasPrefix(op, "tag-") ||
		strings.HasPrefix(op, "untag-")
}

func isAWSCLIInvalidOperationOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "invalid choice") ||
		strings.Contains(lower, "unknown options") ||
		strings.Contains(lower, "is not a valid") ||
		strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "unknown operation") ||
		strings.Contains(lower, "invalid command")
}

func firstArnInArgsOrJSON(args []string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "arn:") {
			return a
		}
		if idx := strings.Index(a, " arn:"); idx >= 0 {
			s := strings.TrimSpace(a[idx+1:])
			if strings.HasPrefix(s, "arn:") {
				return strings.Fields(s)[0]
			}
		}
		if strings.Contains(a, "arn:") {
			idx := strings.Index(a, "arn:")
			s := strings.TrimSpace(a[idx:])
			// Truncate on obvious delimiters.
			for _, d := range []string{",", "\"", "'", "]", "}", " ", "\t"} {
				if j := strings.Index(s, d); j > 0 {
					s = s[:j]
				}
			}
			if strings.HasPrefix(s, "arn:") {
				return strings.TrimSpace(s)
			}
		}
	}
	arns := findArnsInArgsJSON(args)
	if len(arns) > 0 {
		return strings.TrimSpace(arns[0])
	}
	return ""
}

func waitForArnVisibleViaTaggingAPI(ctx context.Context, opts ExecOptions, arn string, w io.Writer) error {
	arn = strings.TrimSpace(arn)
	if arn == "" {
		return fmt.Errorf("empty arn")
	}
	for attempt := 1; attempt <= 20; attempt++ {
		q := []string{"resourcegroupstaggingapi", "get-resources", "--resource-arn-list", arn, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
		out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
		if err == nil {
			var resp struct {
				ResourceTagMappingList []struct {
					ResourceARN string `json:"ResourceARN"`
				} `json:"ResourceTagMappingList"`
			}
			if json.Unmarshal([]byte(out), &resp) == nil {
				for _, it := range resp.ResourceTagMappingList {
					if strings.TrimSpace(it.ResourceARN) == arn {
						return nil
					}
				}
			}
		}
		_, _ = fmt.Fprintf(w, "[maker] note: waiting for arn visible via tagging api (attempt=%d)\n", attempt)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 450 * time.Millisecond):
		}
	}
	return nil
}
