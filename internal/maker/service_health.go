package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/bgdnvk/clanker/internal/ai"
)

// ServiceHealthConfig defines health check parameters
type ServiceHealthConfig struct {
	InstanceID    string        // EC2 instance ID for SSM
	Port          int           // Port the service listens on
	HealthPath    string        // Health endpoint path (e.g., "/healthz")
	MaxRetries    int           // Max health check attempts
	RetryInterval time.Duration // Time between retries
	Profile       string        // AWS profile
	Region        string        // AWS region
}

// SSM agent startup grace period delays (exponential backoff)
var ssmBackoffDelays = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
	45 * time.Second,
}

// isSSMNotReadyError checks if the error indicates SSM agent is not ready
func isSSMNotReadyError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "invalidinstanceid") ||
		strings.Contains(errStr, "not in a valid state") ||
		strings.Contains(errStr, "instance not connected") ||
		strings.Contains(errStr, "target not connected")
}

// VerifyServiceHealthViaSSM logs into instance via SSM to check container and service health
func VerifyServiceHealthViaSSM(ctx context.Context, cfg ServiceHealthConfig, opts ExecOptions) error {
	if cfg.InstanceID == "" {
		return fmt.Errorf("missing instance ID for health check")
	}

	if cfg.HealthPath == "" {
		cfg.HealthPath = "/healthz"
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 20
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = 15 * time.Second
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health] verifying service on instance %s via SSM\n", cfg.InstanceID)

	var lastErr error
	ssmNotReadyCount := 0

	for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, _ = fmt.Fprintf(opts.Writer, "[health] check %d/%d: logging into instance...\n", attempt, cfg.MaxRetries)

		// Step 1: Check if docker is running
		dockerStatus, err := runSSMCommand(ctx, cfg, "systemctl is-active docker 2>/dev/null || echo inactive")
		if err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[health] SSM command failed: %v\n", err)
			lastErr = err

			// Handle SSM agent not ready with exponential backoff
			if isSSMNotReadyError(err) {
				ssmNotReadyCount++
				if ssmNotReadyCount <= len(ssmBackoffDelays) {
					delay := ssmBackoffDelays[ssmNotReadyCount-1]
					_, _ = fmt.Fprintf(opts.Writer, "[health] SSM agent not ready (attempt %d), waiting %v for agent startup...\n", ssmNotReadyCount, delay)
					time.Sleep(delay)
					continue
				}
				// After exhausting backoff delays, provide diagnostic context
				_, _ = fmt.Fprintf(opts.Writer, "[health] SSM agent still not ready after %d attempts. Possible issues:\n", ssmNotReadyCount)
				_, _ = fmt.Fprintf(opts.Writer, "[health]   - Instance may not have SSM agent installed\n")
				_, _ = fmt.Fprintf(opts.Writer, "[health]   - IAM role may be missing AmazonSSMManagedInstanceCore policy\n")
				_, _ = fmt.Fprintf(opts.Writer, "[health]   - Instance may be in a private subnet without VPC endpoint\n")
				_, _ = fmt.Fprintf(opts.Writer, "[health]   - Instance ID %s may be invalid or from a stale checkpoint\n", cfg.InstanceID)
			}

			time.Sleep(cfg.RetryInterval)
			continue
		}

		dockerStatus = strings.TrimSpace(dockerStatus)
		if dockerStatus != "active" {
			_, _ = fmt.Fprintf(opts.Writer, "[health] docker daemon not active (%s), attempting fix...\n", dockerStatus)
			if fixErr := fixDockerDaemon(ctx, cfg, opts); fixErr != nil {
				lastErr = fixErr
				time.Sleep(cfg.RetryInterval)
				continue
			}
		}

		// Step 2: Check container status
		containerStatus, err := runSSMCommand(ctx, cfg, "docker ps -a --format '{{.Status}}' 2>/dev/null | head -1")
		if err != nil {
			lastErr = fmt.Errorf("failed to check container status: %w", err)
			_, _ = fmt.Fprintf(opts.Writer, "[health] failed to check containers: %v\n", err)
			time.Sleep(cfg.RetryInterval)
			continue
		}

		containerStatus = strings.TrimSpace(containerStatus)
		_, _ = fmt.Fprintf(opts.Writer, "[health] container status: %s\n", containerStatus)

		// Step 3: If container not running, trigger agentic fix
		if containerStatus == "" || strings.Contains(strings.ToLower(containerStatus), "exited") {
			_, _ = fmt.Fprintf(opts.Writer, "[health] container not running, starting agentic remediation...\n")
			if fixErr := agenticContainerFix(ctx, cfg, opts); fixErr != nil {
				lastErr = fixErr
			}
			// Re-check after fix
			time.Sleep(5 * time.Second)
			continue
		}

		// Step 4: Container is running, verify with local curl
		healthURL := fmt.Sprintf("http://localhost:%d%s", cfg.Port, cfg.HealthPath)
		curlCmd := fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' --connect-timeout 5 %s 2>/dev/null || echo 000", healthURL)
		curlOutput, err := runSSMCommand(ctx, cfg, curlCmd)
		if err != nil {
			lastErr = fmt.Errorf("curl failed: %w", err)
			_, _ = fmt.Fprintf(opts.Writer, "[health] local curl failed: %v\n", err)
			time.Sleep(cfg.RetryInterval)
			continue
		}

		statusCode := strings.TrimSpace(curlOutput)
		_, _ = fmt.Fprintf(opts.Writer, "[health] local curl %s returned: %s\n", healthURL, statusCode)

		if statusCode == "200" || statusCode == "204" || strings.HasPrefix(statusCode, "2") {
			_, _ = fmt.Fprintf(opts.Writer, "[health] SERVICE HEALTHY - proceeding to ALB creation\n")
			return nil
		}

		// Service not responding correctly, get logs and try to fix
		_, _ = fmt.Fprintf(opts.Writer, "[health] service unhealthy (status %s), checking logs...\n", statusCode)
		logs, _ := runSSMCommand(ctx, cfg, "docker logs $(docker ps -q 2>/dev/null | head -1) --tail 30 2>&1 || echo 'no container logs'")
		_, _ = fmt.Fprintf(opts.Writer, "[health] container logs:\n%s\n", truncateForLog(logs, 500))

		lastErr = fmt.Errorf("service returned status %s", statusCode)

		// Trigger agentic fix with logs context
		if fixErr := agenticContainerFix(ctx, cfg, opts); fixErr != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[health] agentic fix failed: %v\n", fixErr)
		}

		time.Sleep(cfg.RetryInterval)
	}

	// Provide specific error context for common failure modes
	if isSSMNotReadyError(lastErr) {
		return fmt.Errorf("SSM agent not ready after %d attempts (instance=%s): %w. Check IAM role has AmazonSSMManagedInstanceCore policy and instance can reach SSM endpoint", cfg.MaxRetries, cfg.InstanceID, lastErr)
	}
	return fmt.Errorf("service unhealthy after %d attempts: %w", cfg.MaxRetries, lastErr)
}

// runSSMCommand executes a command on the instance via SSM send-command
func runSSMCommand(ctx context.Context, cfg ServiceHealthConfig, command string) (string, error) {
	// Escape quotes in the command
	escapedCmd := strings.ReplaceAll(command, `"`, `\"`)

	args := []string{
		"ssm", "send-command",
		"--instance-ids", cfg.InstanceID,
		"--document-name", "AWS-RunShellScript",
		"--parameters", fmt.Sprintf(`commands=["%s"]`, escapedCmd),
		"--output", "json",
		"--profile", cfg.Profile,
		"--region", cfg.Region,
		"--no-cli-pager",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssm send-command failed: %w (%s)", err, truncateForLog(string(out), 200))
	}

	// Parse command ID from response
	var resp struct {
		Command struct {
			CommandID string `json:"CommandId"`
		} `json:"Command"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("failed to parse ssm response: %w", err)
	}

	if resp.Command.CommandID == "" {
		return "", fmt.Errorf("no command ID in response")
	}

	// Wait for command to complete and get output
	time.Sleep(3 * time.Second) // Give command time to execute

	// Poll for completion
	for i := 0; i < 10; i++ {
		getArgs := []string{
			"ssm", "get-command-invocation",
			"--command-id", resp.Command.CommandID,
			"--instance-id", cfg.InstanceID,
			"--profile", cfg.Profile,
			"--region", cfg.Region,
			"--no-cli-pager",
		}

		getCmd := exec.CommandContext(ctx, "aws", getArgs...)
		getOut, err := getCmd.CombinedOutput()
		if err != nil {
			// Command might still be pending
			time.Sleep(2 * time.Second)
			continue
		}

		var invocation struct {
			StandardOutputContent string `json:"StandardOutputContent"`
			StandardErrorContent  string `json:"StandardErrorContent"`
			Status                string `json:"Status"`
		}
		if err := json.Unmarshal(getOut, &invocation); err != nil {
			return "", fmt.Errorf("failed to parse invocation response: %w", err)
		}

		if invocation.Status == "InProgress" || invocation.Status == "Pending" {
			time.Sleep(2 * time.Second)
			continue
		}

		if invocation.Status == "Success" {
			return invocation.StandardOutputContent, nil
		}

		// Failed or other status
		if invocation.StandardErrorContent != "" {
			return invocation.StandardErrorContent, fmt.Errorf("command failed with status %s", invocation.Status)
		}
		return invocation.StandardOutputContent, fmt.Errorf("command failed with status %s", invocation.Status)
	}

	return "", fmt.Errorf("timeout waiting for SSM command to complete")
}

// fixDockerDaemon restarts the docker daemon if it's not running
func fixDockerDaemon(ctx context.Context, cfg ServiceHealthConfig, opts ExecOptions) error {
	_, _ = fmt.Fprintf(opts.Writer, "[health][fix] restarting docker daemon...\n")

	_, err := runSSMCommand(ctx, cfg, "sudo systemctl restart docker && sleep 5")
	if err != nil {
		return fmt.Errorf("failed to restart docker: %w", err)
	}

	return nil
}

// agenticContainerFix uses LLM to diagnose and fix container startup issues
func agenticContainerFix(ctx context.Context, cfg ServiceHealthConfig, opts ExecOptions) error {
	if opts.AIProvider == "" || opts.AIAPIKey == "" {
		// Fall back to simple fixes without AI
		return simpleContainerFixes(ctx, cfg, opts)
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] starting container remediation loop...\n")

	// Gather diagnostic info
	diagnostics := make(map[string]string)

	dockerPs, _ := runSSMCommand(ctx, cfg, "docker ps -a 2>/dev/null || echo 'docker not available'")
	diagnostics["docker_ps"] = dockerPs

	dockerLogs, _ := runSSMCommand(ctx, cfg, "docker logs $(docker ps -aq 2>/dev/null | head -1) --tail 50 2>&1 || echo 'no container logs'")
	diagnostics["docker_logs"] = dockerLogs

	dockerImages, _ := runSSMCommand(ctx, cfg, "docker images 2>/dev/null || echo 'no images'")
	diagnostics["docker_images"] = dockerImages

	systemctlDocker, _ := runSSMCommand(ctx, cfg, "systemctl status docker 2>/dev/null | head -20 || echo 'systemctl not available'")
	diagnostics["systemctl_docker"] = systemctlDocker

	// Build prompt for LLM
	prompt := buildContainerFixPrompt(cfg, diagnostics)

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	resp, err := client.AskPrompt(ctx, prompt)
	if err != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] LLM call failed: %v, falling back to simple fixes\n", err)
		return simpleContainerFixes(ctx, cfg, opts)
	}

	// Parse and execute fixes
	var fixes struct {
		Analysis string `json:"analysis"`
		Commands []struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		} `json:"commands"`
	}

	cleaned := client.CleanJSONResponse(resp)
	if err := json.Unmarshal([]byte(cleaned), &fixes); err != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] failed to parse LLM response, falling back to simple fixes\n")
		return simpleContainerFixes(ctx, cfg, opts)
	}

	_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] analysis: %s\n", fixes.Analysis)

	for i, fix := range fixes.Commands {
		if !isContainerCommandSafe(fix.Command) {
			_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] skipping unsafe command: %s\n", fix.Command)
			continue
		}

		_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] fix %d: %s (%s)\n", i+1, fix.Command, fix.Reason)

		output, runErr := runSSMCommand(ctx, cfg, fix.Command)
		if runErr != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] fix failed: %v\n", runErr)
		} else {
			_, _ = fmt.Fprintf(opts.Writer, "[health][agentic] output: %s\n", truncateForLog(output, 200))
		}
	}

	return nil
}

func buildContainerFixPrompt(cfg ServiceHealthConfig, diagnostics map[string]string) string {
	return fmt.Sprintf(`You are diagnosing why a containerized service is not starting on an EC2 instance.

INSTANCE ID: %s
SERVICE PORT: %d
HEALTH PATH: %s

DIAGNOSTIC OUTPUT:

=== docker ps -a ===
%s

=== docker logs ===
%s

=== docker images ===
%s

=== systemctl status docker ===
%s

Common issues and fixes:
1. Container exited -> docker start <container_id>
2. No containers -> re-run user-data script or manually docker run
3. Image not found -> docker pull and run
4. ECR auth failed -> aws ecr get-login-password | docker login
5. Port conflict -> docker stop conflicting container
6. OOM killed -> check docker inspect, increase memory

Output JSON with analysis and fix commands:
{
  "analysis": "what is wrong",
  "commands": [
    {"command": "docker start abc123", "reason": "restart exited container"},
    {"command": "docker logs abc123 --tail 20", "reason": "check why it failed"}
  ]
}

RULES:
- Commands must be shell commands that can run via SSM
- Maximum 5 commands
- Do NOT include sudo for docker commands (already has permissions)
- Do NOT include dangerous commands (rm -rf, etc.)
`,
		cfg.InstanceID, cfg.Port, cfg.HealthPath,
		truncateForLog(diagnostics["docker_ps"], 500),
		truncateForLog(diagnostics["docker_logs"], 1000),
		truncateForLog(diagnostics["docker_images"], 300),
		truncateForLog(diagnostics["systemctl_docker"], 300))
}

func isContainerCommandSafe(cmd string) bool {
	lower := strings.ToLower(cmd)

	// Block dangerous patterns
	dangerous := []string{
		"rm -rf", "rm -r /", "mkfs", "dd if=",
		"> /dev/", "chmod 777", "curl | sh", "wget | sh",
		"curl | bash", "wget | bash", "| sh", "| bash",
		"reboot", "shutdown", "init 0", "halt",
		"passwd", "useradd", "userdel", "groupadd",
		"iptables -f", "iptables --flush",
	}
	for _, d := range dangerous {
		if strings.Contains(lower, d) {
			return false
		}
	}

	// Allow diagnostic and remediation commands commonly needed for container fixes
	allowedPrefixes := []string{
		// Docker commands
		"docker", "sudo docker",
		// System services
		"systemctl", "sudo systemctl", "service",
		// AWS CLI (all subcommands, not just ecr)
		"aws ",
		// Logging and diagnostics
		"journalctl", "tail", "head", "cat", "grep", "less",
		"test ", "[ ", "[[ ",
		// Cloud-init
		"cloud-init",
		// Basic utilities
		"curl ", "wget ", "echo", "sleep", "true", "false",
		"ls ", "pwd", "whoami", "id ", "ps ", "ss ", "netstat",
		// File operations (read-only)
		"find ", "stat ", "file ", "wc ",
		// Environment
		"env", "printenv", "export ",
		// Conditional/compound
		"if ", "for ", "while ",
	}
	for _, a := range allowedPrefixes {
		if strings.HasPrefix(lower, a) {
			return true
		}
	}

	// Also allow commands that contain these safe patterns anywhere (for compound commands)
	safePatterns := []string{
		"docker pull", "docker run", "docker start", "docker stop",
		"docker ps", "docker logs", "docker images", "docker inspect",
		"aws ecr get-login-password", "aws ecr describe",
		"aws sts get-caller-identity",
	}
	for _, p := range safePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

func simpleContainerFixes(ctx context.Context, cfg ServiceHealthConfig, opts ExecOptions) error {
	_, _ = fmt.Fprintf(opts.Writer, "[health][fix] trying simple container fixes...\n")

	// 1. Try to start any stopped container
	_, _ = fmt.Fprintf(opts.Writer, "[health][fix] trying to start stopped containers...\n")
	_, _ = runSSMCommand(ctx, cfg, "docker start $(docker ps -aq) 2>/dev/null || true")

	// 2. Check if there are any containers at all
	ps, _ := runSSMCommand(ctx, cfg, "docker ps -aq 2>/dev/null | wc -l")
	if strings.TrimSpace(ps) == "0" {
		_, _ = fmt.Fprintf(opts.Writer, "[health][fix] no containers found, trying to pull and run image...\n")
		// Try to re-authenticate and pull
		_, _ = runSSMCommand(ctx, cfg, `
			REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region 2>/dev/null || echo "us-east-1")
			ACCOUNT=$(aws sts get-caller-identity --query Account --output text 2>/dev/null)
			if [ -n "$ACCOUNT" ]; then
				aws ecr get-login-password --region $REGION | docker login --username AWS --password-stdin $ACCOUNT.dkr.ecr.$REGION.amazonaws.com 2>/dev/null || true
			fi
		`)
	}

	return nil
}

func truncateForLog(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// InferServicePort attempts to determine the service port from plan and bindings
func InferServicePort(plan *Plan, bindings map[string]string) int {
	// Check bindings for port
	for k, v := range bindings {
		if strings.Contains(strings.ToUpper(k), "PORT") {
			if p := parsePort(v); p > 0 {
				return p
			}
		}
	}
	// Check target group commands for port
	for _, cmd := range plan.Commands {
		for i, arg := range cmd.Args {
			if arg == "--port" && i+1 < len(cmd.Args) {
				if p := parsePort(cmd.Args[i+1]); p > 0 {
					return p
				}
			}
		}
	}
	return 80 // Default
}

// InferHealthPath attempts to determine the health check path from plan
func InferHealthPath(plan *Plan) string {
	// Check target group commands for health check path
	for _, cmd := range plan.Commands {
		for i, arg := range cmd.Args {
			if arg == "--health-check-path" && i+1 < len(cmd.Args) {
				return cmd.Args[i+1]
			}
		}
	}
	return "/healthz" // Default
}

func parsePort(s string) int {
	var p int
	fmt.Sscanf(s, "%d", &p)
	return p
}

// IsTransitionToALB detects when we're about to start ALB commands after compute
func IsTransitionToALB(cmds []Command, currentIdx int) bool {
	if currentIdx < 0 || currentIdx >= len(cmds) {
		return false
	}

	// Current command should be ec2 wait or similar compute finalization
	current := cmds[currentIdx]
	if !isComputeFinalizeCommand(current) {
		return false
	}

	// Next command should be ALB-related
	if currentIdx+1 < len(cmds) {
		next := cmds[currentIdx+1]
		return isALBCommand(next)
	}

	return false
}

func isComputeFinalizeCommand(cmd Command) bool {
	if len(cmd.Args) < 2 {
		return false
	}
	service := strings.ToLower(cmd.Args[0])
	op := ""
	if len(cmd.Args) > 1 {
		op = strings.ToLower(cmd.Args[1])
	}
	if service == "aws" && len(cmd.Args) >= 3 {
		service = strings.ToLower(cmd.Args[1])
		op = strings.ToLower(cmd.Args[2])
	}

	// EC2 wait commands or run-instances
	if service == "ec2" {
		return op == "wait" || strings.HasPrefix(op, "wait") || op == "run-instances"
	}
	return false
}

func isALBCommand(cmd Command) bool {
	if len(cmd.Args) < 1 {
		return false
	}
	service := strings.ToLower(cmd.Args[0])
	if service == "aws" && len(cmd.Args) >= 2 {
		service = strings.ToLower(cmd.Args[1])
	}
	return service == "elbv2"
}

// WriteHealthCheckpoint writes a health verification checkpoint to the writer
func WriteHealthCheckpoint(w io.Writer, status string) {
	_, _ = fmt.Fprintf(w, "\n[maker] ═══════════════════════════════════════════════════════════\n")
	_, _ = fmt.Fprintf(w, "[maker] %s\n", status)
	_, _ = fmt.Fprintf(w, "[maker] ═══════════════════════════════════════════════════════════\n\n")
}
