package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/ai"
)

// Blocklist of destructive operations that should NEVER be proposed by AI remediation.
// These operations can cause data loss or remove existing resources.
var remediationBlockedOps = map[string]bool{
	// EC2
	"terminate-instances":           true,
	"delete-snapshot":               true,
	"delete-volume":                 true,
	"delete-key-pair":               true,
	"delete-security-group":         true,
	"delete-vpc":                    true,
	"delete-subnet":                 true,
	"delete-internet-gateway":       true,
	"delete-nat-gateway":            true,
	"delete-route-table":            true,
	"delete-network-acl":            true,
	"delete-vpc-peering-connection": true,

	// IAM - allow delete for roles/policies we're creating, but block user deletion
	"delete-user":               true,
	"delete-group":              true,
	"delete-account-alias":      true,
	"delete-virtual-mfa-device": true,

	// S3 - never delete buckets or objects
	"delete-bucket":  true,
	"delete-object":  true,
	"delete-objects": true,
	"rb":             true, // s3 rb (remove bucket)

	// RDS
	"delete-db-instance":         true,
	"delete-db-cluster":          true,
	"delete-db-snapshot":         true,
	"delete-db-cluster-snapshot": true,

	// DynamoDB
	"delete-table":  true,
	"delete-backup": true,

	// Lambda - protect existing functions
	"delete-function":      true,
	"delete-layer-version": true,

	// ECS
	"delete-cluster":             true,
	"delete-service":             true,
	"deregister-task-definition": true,

	// EKS
	"delete-nodegroup": true,

	// CloudFormation
	"delete-stack":     true,
	"delete-stack-set": true,

	// Route53
	"delete-hosted-zone": true,

	// KMS
	"schedule-key-deletion": true,
	"delete-alias":          true,

	// Secrets Manager
	"delete-secret": true,

	// CloudWatch
	"delete-log-group":  true,
	"delete-log-stream": true,
	"delete-alarms":     true,
	"delete-dashboards": true,

	// SNS/SQS
	"delete-topic": true,
	"delete-queue": true,

	// ElastiCache
	"delete-cache-cluster":     true,
	"delete-replication-group": true,

	// Elasticsearch/OpenSearch
	"delete-domain": true,

	// Kinesis
	"delete-stream": true,

	// Step Functions
	"delete-state-machine": true,

	// API Gateway
	"delete-rest-api": true,
	"delete-api":      true,

	// Cognito
	"delete-user-pool":     true,
	"delete-identity-pool": true,

	// ECR - protect repositories with images
	"delete-repository": true,

	// CodeBuild/CodePipeline/CodeDeploy
	"delete-project":     true,
	"delete-pipeline":    true,
	"delete-application": true,

	// Glue
	"delete-database": true,
	"delete-job":      true,

	// Athena
	"delete-work-group":  true,
	"delete-named-query": true,
}

// Allowlist of safe operations that AI remediation can propose.
// These are primarily create/describe/get/list/put/update operations.
var remediationAllowedPrefixes = []string{
	"create-",
	"describe-",
	"get-",
	"list-",
	"put-",
	"update-",
	"add-",
	"attach-",
	"associate-",
	"register-",
	"tag-",
	"enable-",
	"set-",
	"modify-",
	"authorize-", // for security group rules
	"wait",       // waiter commands
}

// validateRemediationCommand applies strict guardrails to AI-proposed remediation commands.
// Returns an error if the command is not safe to execute.
func validateRemediationCommand(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("remediation command too short: %v", args)
	}

	service := strings.ToLower(strings.TrimSpace(args[0]))
	op := strings.ToLower(strings.TrimSpace(args[1]))

	// Block known destructive operations
	if remediationBlockedOps[op] {
		return fmt.Errorf("blocked destructive remediation: %s %s", service, op)
	}

	// Block any operation starting with delete/terminate/destroy/remove/deregister
	if strings.HasPrefix(op, "delete") ||
		strings.HasPrefix(op, "terminate") ||
		strings.HasPrefix(op, "destroy") ||
		strings.HasPrefix(op, "remove") ||
		strings.HasPrefix(op, "deregister") ||
		strings.HasPrefix(op, "purge") ||
		strings.HasPrefix(op, "revoke") { // revoke can remove permissions
		return fmt.Errorf("blocked destructive remediation prefix: %s %s", service, op)
	}

	// Check against allowlist
	allowed := false
	for _, prefix := range remediationAllowedPrefixes {
		if strings.HasPrefix(op, prefix) {
			allowed = true
			break
		}
	}

	if !allowed {
		return fmt.Errorf("remediation operation not in allowlist: %s %s", service, op)
	}

	// Additional validation: no shell operators
	for _, a := range args {
		trimmed := strings.TrimSpace(a)
		switch strings.ToLower(trimmed) {
		case ";", "|", "||", "&&", ">", ">>", "<", "<<", "`":
			return fmt.Errorf("shell operators not allowed in remediation")
		}
	}

	return nil
}

// genericRemediationPrompt creates a prompt for AI-powered remediation with strict guardrails.
func genericRemediationPrompt(failedArgs []string, failedOutput string, recentLogs string) string {
	// Strip injected flags for cleaner prompt
	trimmed := make([]string, 0, len(failedArgs))
	for i := 0; i < len(failedArgs); i++ {
		if failedArgs[i] == "--profile" || failedArgs[i] == "--region" || failedArgs[i] == "--no-cli-pager" {
			i++
			continue
		}
		trimmed = append(trimmed, failedArgs[i])
	}

	return fmt.Sprintf(`You are an AWS CLI remediation assistant. A command has failed and you need to propose prerequisite commands to fix it.

STRICT RULES:
1. Output ONLY valid JSON
2. Propose ONLY "create-*", "describe-*", "get-*", "put-*", "update-*", "add-*", "attach-*", "associate-*", "register-*", "tag-*", "enable-*", "set-*", or "modify-*" commands
3. NEVER propose delete/terminate/destroy/remove/deregister/purge/revoke commands
4. NEVER propose commands that could delete or modify EXISTING resources
5. Only propose commands to CREATE MISSING prerequisites
6. Commands must be AWS CLI subcommands only (no "aws" prefix, no shell operators)
7. Do NOT include --profile/--region/--no-cli-pager flags

JSON Schema:
{
  "analysis": "brief explanation of what's missing",
  "commands": [
    { "args": ["service", "operation", "--flag", "value"], "reason": "why this fixes it" }
  ]
}

FAILING COMMAND:
%v

ERROR OUTPUT:
%s

RECENT EXECUTION LOGS (for context):
%s

Respond with JSON only. If you cannot safely fix this without destructive operations, return {"commands": [], "analysis": "cannot fix safely"}.
`, trimmed, strings.TrimSpace(failedOutput), strings.TrimSpace(recentLogs))
}

// maybeGenericAIRemediation attempts AI-powered remediation for any failure type.
// It applies strict guardrails to prevent destructive operations.
func maybeGenericAIRemediation(
	ctx context.Context,
	opts ExecOptions,
	failedArgs []string,
	awsArgs []string,
	stdinBytes []byte,
	failedOutput string,
	recentLogs string,
) (bool, error) {
	if strings.TrimSpace(opts.AIProvider) == "" || strings.TrimSpace(opts.AIAPIKey) == "" {
		return false, nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[maker] attempting AI-powered remediation...\n")
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("generic_ai_start", fmt.Sprintf("cmd=%s %s", args0(failedArgs), args1(failedArgs)), "starting generic AI remediation")
	}

	client := ai.NewClient(opts.AIProvider, opts.AIAPIKey, opts.Debug, opts.AIProfile)
	resp, err := client.AskPrompt(ctx, genericRemediationPrompt(failedArgs, failedOutput, recentLogs))
	if err != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] AI remediation failed to get response: %v\n", err)
		return false, nil
	}

	cleaned := client.CleanJSONResponse(resp)

	var parsed struct {
		Analysis string    `json:"analysis"`
		Commands []Command `json:"commands"`
	}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] AI remediation returned invalid JSON: %v\n", err)
		return false, nil
	}

	if len(parsed.Commands) == 0 {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] AI remediation: %s\n", parsed.Analysis)
		return false, nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[maker] AI analysis: %s\n", parsed.Analysis)
	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("generic_ai_analysis", fmt.Sprintf("cmd=%s %s", args0(failedArgs), args1(failedArgs)), parsed.Analysis)
	}

	// Validate ALL commands before executing any
	for i, cmd := range parsed.Commands {
		cmd.Args = normalizeArgs(cmd.Args)
		parsed.Commands[i] = cmd

		if err := validateRemediationCommand(cmd.Args); err != nil {
			_, _ = fmt.Fprintf(opts.Writer, "[maker] AI remediation command %d blocked: %v\n", i+1, err)
			return false, nil
		}
	}

	// Execute remediation commands
	for i, cmd := range parsed.Commands {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation step %d/%d: %s %s (%s)\n",
			i+1, len(parsed.Commands), cmd.Args[0], cmd.Args[1], cmd.Reason)

		cmdArgs := buildAWSExecArgs(cmd.Args, opts, opts.Writer)
		if _, err := runAWSCommandStreaming(ctx, cmdArgs, nil, opts.Writer); err != nil {
			// Log but continue - some prereqs might already exist
			_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation step %d warning: %v\n", i+1, err)
		}
	}

	// Retry original command with backoff for IAM propagation
	_, _ = fmt.Fprintf(opts.Writer, "[maker] retrying original command after remediation...\n")
	err = retryWithBackoff(ctx, opts.Writer, 4, func() (string, error) {
		return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
	})

	if err == nil {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation successful\n")
		if opts.PlanLogger != nil {
			opts.PlanLogger.WriteFixSuccess("generic_ai", fmt.Sprintf("cmd=%s %s", args0(failedArgs), args1(failedArgs)), "generic AI remediation succeeded")
		}
		return true, nil
	}

	if opts.PlanLogger != nil {
		opts.PlanLogger.WriteFix("generic_ai_failed", fmt.Sprintf("cmd=%s %s", args0(failedArgs), args1(failedArgs)), err.Error())
	}
	return false, err
}
