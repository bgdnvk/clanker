package maker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// runSSMShellScript runs a bash script on an instance via SSM RunCommand and returns stdout.
// It is intended for non-destructive runtime diagnostics and safe remediations.
func runSSMShellScript(ctx context.Context, instanceID, profile, region string, commands []string, w io.Writer) (string, error) {
	instanceID = strings.TrimSpace(instanceID)
	profile = strings.TrimSpace(profile)
	region = strings.TrimSpace(region)
	if instanceID == "" {
		return "", fmt.Errorf("missing instance id")
	}
	if profile == "" {
		return "", fmt.Errorf("missing aws profile")
	}
	if region == "" {
		return "", fmt.Errorf("missing aws region")
	}
	if len(commands) == 0 {
		return "", fmt.Errorf("missing ssm commands")
	}

	// Build: --parameters commands=["...","..."]
	// Args are passed without shell interpolation (exec.Command), so we can embed quotes safely.
	quoted := make([]string, 0, len(commands))
	for _, c := range commands {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		c = strings.ReplaceAll(c, "\\", "\\\\")
		c = strings.ReplaceAll(c, "\"", "\\\"")
		quoted = append(quoted, fmt.Sprintf("\"%s\"", c))
	}
	if len(quoted) == 0 {
		return "", fmt.Errorf("missing ssm commands")
	}
	params := "commands=[" + strings.Join(quoted, ",") + "]"

	send := []string{
		"ssm", "send-command",
		"--instance-ids", instanceID,
		"--document-name", "AWS-RunShellScript",
		"--parameters", params,
		"--comment", "clanker post-deploy runtime check",
		"--query", "Command.CommandId",
		"--output", "text",
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}

	cmdID, err := retryWithBackoffOutput(ctx, w, 5, func() (string, error) {
		out, e := runAWSCommandStreaming(ctx, send, nil, io.Discard)
		return strings.TrimSpace(out), e
	})
	if err != nil {
		return "", fmt.Errorf("ssm send-command failed: %w", err)
	}
	cmdID = strings.TrimSpace(cmdID)
	if cmdID == "" || strings.Contains(strings.ToLower(cmdID), "none") {
		return "", fmt.Errorf("ssm send-command returned empty command id")
	}

	deadline := time.Now().Add(5 * time.Minute)
	status := ""
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		getStatus := []string{
			"ssm", "get-command-invocation",
			"--command-id", cmdID,
			"--instance-id", instanceID,
			"--query", "Status",
			"--output", "text",
			"--profile", profile,
			"--region", region,
			"--no-cli-pager",
		}

		stOut, stErr := runAWSCommandStreaming(ctx, getStatus, nil, io.Discard)
		if stErr != nil {
			// Transient while invocation isn't ready.
			time.Sleep(4 * time.Second)
			continue
		}
		status = strings.TrimSpace(stOut)
		lower := strings.ToLower(status)
		if lower == "success" {
			break
		}
		if lower == "failed" || lower == "timedout" || lower == "cancelled" {
			break
		}
		time.Sleep(4 * time.Second)
	}

	getOut := []string{
		"ssm", "get-command-invocation",
		"--command-id", cmdID,
		"--instance-id", instanceID,
		"--query", "StandardOutputContent",
		"--output", "text",
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}
	stdout, _ := runAWSCommandStreaming(ctx, getOut, nil, io.Discard)
	stdout = strings.TrimSpace(stdout)

	if strings.ToLower(strings.TrimSpace(status)) != "success" {
		getErr := []string{
			"ssm", "get-command-invocation",
			"--command-id", cmdID,
			"--instance-id", instanceID,
			"--query", "StandardErrorContent",
			"--output", "text",
			"--profile", profile,
			"--region", region,
			"--no-cli-pager",
		}
		stderr, _ := runAWSCommandStreaming(ctx, getErr, nil, io.Discard)
		stderr = strings.TrimSpace(stderr)
		if stderr != "" {
			return stdout, fmt.Errorf("ssm command failed (status=%s): %s", status, stderr)
		}
		return stdout, fmt.Errorf("ssm command failed (status=%s)", status)
	}

	return stdout, nil
}
