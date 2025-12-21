package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type cloudformationDescribeStacksResp struct {
	Stacks []struct {
		StackName   string `json:"StackName"`
		StackId     string `json:"StackId"`
		StackStatus string `json:"StackStatus"`
	} `json:"Stacks"`
}

type cloudformationDescribeStackEventsResp struct {
	StackEvents []struct {
		LogicalResourceId    string `json:"LogicalResourceId"`
		ResourceType         string `json:"ResourceType"`
		ResourceStatus       string `json:"ResourceStatus"`
		ResourceStatusReason string `json:"ResourceStatusReason"`
	} `json:"StackEvents"`
}

func cloudformationStackExists(ctx context.Context, opts ExecOptions, stackName string) bool {
	stackName = strings.TrimSpace(stackName)
	if stackName == "" {
		return false
	}
	q := []string{"cloudformation", "describe-stacks", "--stack-name", stackName, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	_, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	return err == nil
}

func waitForCloudFormationStackTerminal(
	ctx context.Context,
	opts ExecOptions,
	stackName string,
	w io.Writer,
) (terminalStatus string, details string, err error) {
	stackName = strings.TrimSpace(stackName)
	if stackName == "" {
		return "", "", fmt.Errorf("missing stack name")
	}

	deadline := time.Now().Add(60 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("timed out waiting for cloudformation stack %s", stackName)
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		default:
		}

		status, err := getCloudFormationStackStatus(ctx, opts, stackName)
		if err != nil {
			// When the stack doesn't exist, treat as terminal.
			if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
				return "DOES_NOT_EXIST", "stack does not exist", err
			}
			// Retry transient describe issues.
			_, _ = fmt.Fprintf(w, "[maker] note: cloudformation describe-stacks failed; retrying (%v)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if isCloudFormationStackTerminal(status) {
			details = summarizeCloudFormationFailure(ctx, opts, stackName)
			return status, details, nil
		}

		_, _ = fmt.Fprintf(w, "[maker] note: waiting for cloudformation stack (stack=%s status=%s)\n", stackName, status)
		time.Sleep(15 * time.Second)
	}
}

func getCloudFormationStackStatus(ctx context.Context, opts ExecOptions, stackName string) (string, error) {
	q := []string{"cloudformation", "describe-stacks", "--stack-name", stackName, "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return "", err
	}
	var resp cloudformationDescribeStacksResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", err
	}
	if len(resp.Stacks) == 0 {
		return "", fmt.Errorf("stack not found")
	}
	return strings.TrimSpace(resp.Stacks[0].StackStatus), nil
}

func isCloudFormationStackTerminal(status string) bool {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" {
		return false
	}
	if strings.HasSuffix(status, "_IN_PROGRESS") {
		return false
	}
	// All other statuses are terminal enough for our purposes.
	return true
}

func isCloudFormationStackSuccess(status string) bool {
	status = strings.ToUpper(strings.TrimSpace(status))
	return status == "CREATE_COMPLETE" || status == "UPDATE_COMPLETE" || status == "IMPORT_COMPLETE"
}

func summarizeCloudFormationFailure(ctx context.Context, opts ExecOptions, stackName string) string {
	q := []string{"cloudformation", "describe-stack-events", "--stack-name", stackName, "--max-items", "15", "--output", "json", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	out, err := runAWSCommandStreaming(ctx, q, nil, io.Discard)
	if err != nil {
		return ""
	}
	var resp cloudformationDescribeStackEventsResp
	if json.Unmarshal([]byte(out), &resp) != nil {
		return ""
	}
	var lines []string
	for _, ev := range resp.StackEvents {
		reason := strings.TrimSpace(ev.ResourceStatusReason)
		if reason == "" {
			continue
		}
		// Prefer failures and invalid CIDR reasons.
		if strings.Contains(strings.ToUpper(ev.ResourceStatus), "FAILED") || strings.Contains(strings.ToLower(reason), "cidr") || strings.Contains(strings.ToLower(reason), "invalid") {
			lines = append(lines, fmt.Sprintf("%s %s %s: %s", ev.ResourceType, ev.LogicalResourceId, ev.ResourceStatus, reason))
		}
		if len(lines) >= 6 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}
