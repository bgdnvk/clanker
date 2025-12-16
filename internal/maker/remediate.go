package maker

import (
	"context"
	"fmt"
)

func maybeAutoRemediateAndRetry(
	ctx context.Context,
	plan *Plan,
	opts ExecOptions,
	idx int,
	args []string,
	awsArgs []string,
	stdinBytes []byte,
	out string,
) (bool, error) {
	_ = plan
	_ = idx

	// Built-in remediations.
	if isIAMDeleteRole(args) && isIAMDeleteConflict(out) && opts.Destroyer {
		roleName := flagValue(args, "--role-name")
		if roleName != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: drain+delete role\n")
			if err := resolveAndDeleteIAMRole(ctx, opts, roleName, opts.Writer); err == nil {
				return true, nil
			}
		}
	}

	if isLambdaCreateFunction(args) && isLambdaRoleAssumePropagationError(out) {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry create-function after IAM propagation\n")
		if _, err := retryLambdaCreateFunctionOnAssumeRole(ctx, awsArgs, stdinBytes, opts.Writer); err == nil {
			return true, nil
		}
	}

	// AI remediation fallback.
	rp, err := maybeRemediateWithAI(ctx, opts, awsArgs, out)
	if err != nil {
		return false, err
	}
	if rp == nil || len(rp.Commands) == 0 {
		return false, fmt.Errorf("no remediation")
	}

	_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: AI-generated prerequisite steps\n")
	for i, c := range rp.Commands {
		if err := validateCommand(c.Args, opts.Destroyer); err != nil {
			return false, fmt.Errorf("remediation command %d rejected: %w", i+1, err)
		}

		cmdArgs := append(append([]string{}, c.Args...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		if _, err := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer); err != nil {
			return false, err
		}
	}

	// Retry original command once.
	if _, err := runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer); err != nil {
		return false, err
	}

	return true, nil
}
