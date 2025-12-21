package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var lambdaArnMissingRegionRe = regexp.MustCompile(`^arn:([^:]+):lambda:(\d{12}):function:(.+)$`)

func maybeRewriteAndRetry(ctx context.Context, opts ExecOptions, args []string, awsArgs []string, stdinBytes []byte, failure AWSFailure, output string) (bool, error) {
	// Generic transient retry (service hiccups / in-progress / timeouts).
	if isTransientFailure(failure, output) {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry after transient failure\n")
		if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
			return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		}); err != nil {
			return true, err
		}
		return true, nil
	}

	// Generic throttling retry.
	if failure.Category == FailureThrottled {
		_, _ = fmt.Fprintf(opts.Writer, "[maker] remediation attempted: retry after throttling\n")
		if err := retryWithBackoff(ctx, opts.Writer, 6, func() (string, error) {
			return runAWSCommandStreaming(ctx, awsArgs, stdinBytes, opts.Writer)
		}); err != nil {
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
			if err := remediateEC2InvalidInstanceProfileAndRetry(ctx, opts, args, stdinBytes, opts.Writer); err != nil {
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

	// When a create command fails due to already existing and it's safe to treat as idempotent,
	// we skip further retries.
	if failure.Category == FailureAlreadyExists {
		if args0(args) == "eks" && (args1(args) == "create-cluster" || args1(args) == "create-nodegroup" || args1(args) == "create-addon") {
			return true, nil
		}
		if args0(args) == "ecs" && (args1(args) == "create-cluster" || args1(args) == "create-service") {
			return true, nil
		}
		if args0(args) == "iam" && args1(args) == "create-instance-profile" {
			return true, nil
		}
		if args0(args) == "logs" && args1(args) == "create-log-group" {
			return true, nil
		}
		if args0(args) == "s3" && args1(args) == "create-bucket" {
			return true, nil
		}
		if args0(args) == "ecr" && args1(args) == "create-repository" {
			return true, nil
		}
		if args0(args) == "dynamodb" && args1(args) == "create-table" {
			return true, nil
		}
		if args0(args) == "sqs" && args1(args) == "create-queue" {
			return true, nil
		}

		// Secrets Manager: create-secret already exists -> put-secret-value (if secret-string provided).
		if args0(args) == "secretsmanager" && args1(args) == "create-secret" {
			name := flagValue(args, "--name")
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
	}

	// Nothing to rewrite.
	return false, nil
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

func retryWithBackoff(ctx context.Context, w io.Writer, attempts int, fn func() (string, error)) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			sleep := time.Duration(1<<uint(attempt-1)) * time.Second
			_, _ = fmt.Fprintf(w, "[maker] note: retrying after backoff (attempt=%d sleep=%s)\n", attempt, sleep)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}
		_, err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

func remediateEC2InvalidInstanceProfileAndRetry(ctx context.Context, opts ExecOptions, args []string, stdinBytes []byte, w io.Writer) error {
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

		_, _ = fmt.Fprintf(w, "[maker] remediation attempted: rewriting ec2 run-instances --iam-instance-profile to use Arn=... and retrying\n")
		rewrittenAWSArgs := append(append([]string{}, rewritten...), "--profile", opts.Profile, "--region", opts.Region, "--no-cli-pager")
		out, err := runAWSCommandStreaming(ctx, rewrittenAWSArgs, stdinBytes, w)
		if err == nil {
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
