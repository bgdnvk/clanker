package plan

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var placeholderRe = regexp.MustCompile(`<([A-Z0-9_]+)>`)

// Execute runs a K8s plan with progress output
func Execute(ctx context.Context, plan *K8sPlan, opts ExecOptions, w io.Writer) (*ExecResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("nil plan")
	}
	if w == nil {
		w = os.Stdout
	}

	progress := NewProgressWriter(w, len(plan.Steps), opts.Debug)
	bindings := make(map[string]string)
	result := &ExecResult{
		Success:  true,
		Bindings: bindings,
	}

	// Pre-populate bindings from plan
	bindings["CLUSTER_NAME"] = plan.ClusterName
	bindings["REGION"] = plan.Region
	bindings["PROFILE"] = plan.Profile

	for _, step := range plan.Steps {
		progress.StartStep(step)

		stepResult, err := executeStep(ctx, step, opts, bindings, progress)
		if err != nil {
			result.Success = false
			result.Errors = append(result.Errors, fmt.Sprintf("Step %s failed: %v", step.ID, err))
			progress.LogError(err.Error())
			return result, err
		}

		// Merge new bindings
		for k, v := range stepResult.Bindings {
			bindings[k] = v
			progress.LogBinding(k, v)
		}

		// Handle wait conditions
		if step.WaitFor != nil {
			if err := executeWait(ctx, step.WaitFor, opts, bindings, progress); err != nil {
				result.Success = false
				result.Errors = append(result.Errors, fmt.Sprintf("Wait for %s failed: %v", step.WaitFor.Type, err))
				progress.LogError(err.Error())
				return result, err
			}
		}

		// Log config changes
		if step.ConfigChange != nil {
			progress.LogConfigChange(*step.ConfigChange)
		}
	}

	progress.LogDuration()

	// Build connection info
	result.Connection = buildConnectionInfo(plan, bindings)

	return result, nil
}

func executeStep(ctx context.Context, step Step, opts ExecOptions, bindings map[string]string, progress *ProgressWriter) (*StepResult, error) {
	result := &StepResult{
		StepID:   step.ID,
		Bindings: make(map[string]string),
	}

	// Handle SSH steps separately
	if step.SSHConfig != nil {
		return executeSSHStep(ctx, step, opts, bindings, progress)
	}

	// Build command with resolved placeholders
	args := applyBindings(step.Args, bindings)

	// Add profile and region for AWS/eksctl commands
	switch step.Command {
	case "aws":
		args = append(args, "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
	case "eksctl":
		args = append(args, "--profile", opts.Profile, "--region", opts.Region)
	}

	cmdStr := formatCommandForLog(step.Command, args)
	progress.LogCommand(step.Command, cmdStr)

	if opts.DryRun {
		progress.LogNote("dry-run: skipping execution")
		result.Success = true
		return result, nil
	}

	// Execute command
	output, err := runCommandStreaming(ctx, step.Command, args, progress, step.Command)
	result.Output = output

	if err != nil {
		result.Success = false
		result.Error = err
		return result, err
	}

	// Learn bindings from output
	if len(step.Produces) > 0 {
		learnBindingsFromOutput(step.Produces, output, result.Bindings)
	}

	result.Success = true
	return result, nil
}

func executeSSHStep(ctx context.Context, step Step, opts ExecOptions, bindings map[string]string, progress *ProgressWriter) (*StepResult, error) {
	result := &StepResult{
		StepID:   step.ID,
		Bindings: make(map[string]string),
	}

	cfg := step.SSHConfig
	host := applyBindingsToString(cfg.Host, bindings)
	user := cfg.User
	if user == "" {
		user = "ubuntu"
	}
	keyPath := cfg.KeyPath
	if keyPath == "" {
		keyPath = opts.SSHKeyPath
	}
	keyPath = expandPath(keyPath)

	progress.LogSSH(host, user)

	// Wait for SSH to be available
	if err := waitForSSH(ctx, host, user, keyPath, progress); err != nil {
		return result, fmt.Errorf("SSH connection failed: %w", err)
	}

	progress.LogSSHConnected(host)

	// Execute script via SSH
	script := applyBindingsToString(cfg.Script, bindings)
	progress.LogSSHCommand(cfg.ScriptName)

	if opts.DryRun {
		progress.LogNote("dry-run: skipping SSH execution")
		result.Success = true
		return result, nil
	}

	output, err := runSSHCommand(ctx, host, user, keyPath, script, progress)
	result.Output = output

	if err != nil {
		result.Success = false
		result.Error = err
		return result, err
	}

	// Learn bindings from SSH output
	if len(step.Produces) > 0 {
		learnBindingsFromOutput(step.Produces, output, result.Bindings)
	}

	result.Success = true
	return result, nil
}

func executeWait(ctx context.Context, waitCfg *WaitConfig, opts ExecOptions, bindings map[string]string, progress *ProgressWriter) error {
	timeout := waitCfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	interval := waitCfg.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}

	deadline := time.Now().Add(timeout)
	iteration := 0
	maxIterations := int(timeout / interval)

	for {
		iteration++
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s", waitCfg.Type)
		}

		ready, status, err := checkWaitCondition(ctx, waitCfg, opts, bindings)
		if err != nil {
			progress.LogWarning(fmt.Sprintf("wait check failed: %v", err))
		}

		progress.LogWaitProgress(status, iteration, maxIterations)

		if ready {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func checkWaitCondition(ctx context.Context, waitCfg *WaitConfig, opts ExecOptions, bindings map[string]string) (bool, string, error) {
	resource := applyBindingsToString(waitCfg.Resource, bindings)

	switch waitCfg.Type {
	case "cluster-ready":
		return checkEKSClusterReady(ctx, resource, opts)
	case "node-ready":
		return checkNodesReady(ctx, opts)
	case "instance-running":
		return checkInstanceRunning(ctx, resource, opts)
	case "pods-ready":
		return checkPodsReady(ctx, resource, opts)
	default:
		return true, "unknown wait type", nil
	}
}

func checkEKSClusterReady(ctx context.Context, clusterName string, opts ExecOptions) (bool, string, error) {
	args := []string{"eks", "describe-cluster", "--name", clusterName, "--query", "cluster.status", "--output", "text", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "error", err
	}
	status := strings.TrimSpace(string(out))
	return status == "ACTIVE", fmt.Sprintf("cluster status %s", status), nil
}

func checkNodesReady(ctx context.Context, opts ExecOptions) (bool, string, error) {
	args := []string{"get", "nodes", "-o", "jsonpath={.items[*].status.conditions[?(@.type==\"Ready\")].status}"}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "error", err
	}
	statuses := strings.Fields(strings.TrimSpace(string(out)))
	for _, s := range statuses {
		if s != "True" {
			return false, "nodes not ready", nil
		}
	}
	if len(statuses) == 0 {
		return false, "no nodes found", nil
	}
	return true, fmt.Sprintf("%d nodes ready", len(statuses)), nil
}

func checkInstanceRunning(ctx context.Context, instanceID string, opts ExecOptions) (bool, string, error) {
	args := []string{"ec2", "describe-instances", "--instance-ids", instanceID, "--query", "Reservations[0].Instances[0].State.Name", "--output", "text", "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager"}
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "error", err
	}
	status := strings.TrimSpace(string(out))
	return status == "running", fmt.Sprintf("instance %s", status), nil
}

func checkPodsReady(ctx context.Context, selector string, opts ExecOptions) (bool, string, error) {
	args := []string{"get", "pods", "-l", selector, "-o", "jsonpath={.items[*].status.phase}"}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "error", err
	}
	phases := strings.Fields(strings.TrimSpace(string(out)))
	for _, p := range phases {
		if p != "Running" {
			return false, "pods not running", nil
		}
	}
	if len(phases) == 0 {
		return false, "no pods found", nil
	}
	return true, fmt.Sprintf("%d pods running", len(phases)), nil
}

func runCommandStreaming(ctx context.Context, command string, args []string, progress *ProgressWriter, prefix string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var output strings.Builder

	// Stream stdout
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line)
			output.WriteString("\n")
			progress.LogCommandOutput(prefix, line)
		}
	}()

	// Stream stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line)
			output.WriteString("\n")
			progress.LogCommandOutput(prefix, line)
		}
	}()

	if err := cmd.Wait(); err != nil {
		return output.String(), err
	}

	return output.String(), nil
}

func runSSHCommand(ctx context.Context, host, user, keyPath, script string, progress *ProgressWriter) (string, error) {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-i", keyPath,
		fmt.Sprintf("%s@%s", user, host),
		script,
	}

	return runCommandStreaming(ctx, "ssh", args, progress, "ssh")
}

func waitForSSH(ctx context.Context, host, user, keyPath string, progress *ProgressWriter) error {
	maxAttempts := 30
	for i := 0; i < maxAttempts; i++ {
		args := []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=5",
			"-o", "BatchMode=yes",
			"-i", keyPath,
			fmt.Sprintf("%s@%s", user, host),
			"echo ok",
		}

		cmd := exec.CommandContext(ctx, "ssh", args...)
		if err := cmd.Run(); err == nil {
			return nil
		}

		progress.LogWaitProgress("SSH available", i+1, maxAttempts)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("SSH not available after %d attempts", maxAttempts)
}

func applyBindings(args []string, bindings map[string]string) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		result = append(result, applyBindingsToString(arg, bindings))
	}
	return result
}

func applyBindingsToString(s string, bindings map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(m, "<"), ">")
		if v, ok := bindings[key]; ok && v != "" {
			return v
		}
		return m
	})
}

func learnBindingsFromOutput(produces map[string]string, output string, bindings map[string]string) {
	// Simple line-based extraction
	lines := strings.Split(output, "\n")
	for key, pattern := range produces {
		for _, line := range lines {
			if strings.Contains(line, pattern) {
				// Extract value after pattern
				idx := strings.Index(line, pattern)
				if idx >= 0 {
					value := strings.TrimSpace(line[idx+len(pattern):])
					if value != "" {
						bindings[key] = value
					}
				}
			}
		}
	}
}

func buildConnectionInfo(plan *K8sPlan, bindings map[string]string) *Connection {
	conn := &Connection{
		Kubeconfig: expandPath("~/.kube/config"),
	}

	if endpoint, ok := bindings["CLUSTER_ENDPOINT"]; ok {
		conn.Endpoint = endpoint
	}

	conn.Commands = []string{
		"kubectl get nodes",
		"kubectl get pods -A",
	}

	return conn
}

func formatCommandForLog(cmd string, args []string) string {
	const maxLen = 150
	s := strings.Join(args, " ")
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return strings.Replace(path, "~", home, 1)
	}
	return path
}
