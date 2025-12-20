package maker

import "fmt"

func PlanPrompt(question string) string {
	return PlanPromptWithMode(question, false)
}

func PlanPromptWithMode(question string, destroyer bool) string {
	destructiveRule := "- Avoid any destructive operations (delete/remove/terminate/destroy)."
	if destroyer {
		destructiveRule = "- Destructive operations are allowed ONLY if the user explicitly asked for deletion/teardown."
	}

	return fmt.Sprintf(`You are an infrastructure maker planner.

Your job: produce a concrete, minimal AWS CLI execution plan to satisfy the user request.

Constraints:
- Output ONLY valid JSON.
- Use this schema exactly:
{
  "version": 1,
  "createdAt": "RFC3339 timestamp",
  "question": "original user question",
  "summary": "short summary of what will be created/changed",
  "commands": [
    {
      "args": ["aws", "<service>", "<operation>", "..."],
      "reason": "why this command is needed"
    }
  ],
  "notes": ["optional notes"]
}

Rules for commands:
- Provide args as an array; do NOT provide a single string.
- Commands MUST be AWS CLI only. Every command args MUST start with "aws".
- Do NOT include any non-AWS programs (no python/node/bash/curl/zip/terraform/etc).
- Do NOT include shell operators, pipes, redirects, or subshells.
- Do NOT include --profile, --region, or --no-cli-pager (the runner injects them).
- Prefer idempotent operations where possible.
%s

AWS Lambda code packaging:
- Prefer Python runtime "python3.12".
- If you need to create a Lambda function, use "--zip-file fileb://-" (the runner will inline a minimal handler zip automatically; no local files required).
- If you reference or create an IAM role for ANY AWS service, ensure:
  - The role trust policy allows the correct service principal (sts:AssumeRole).
  - The role has the minimum permissions required for the service to run and emit operational telemetry (logs/metrics/traces) as appropriate.
  - If you use an existing role, include explicit aws iam attach-role-policy steps for any required AWS-managed execution/telemetry policies.
  - For Lambda specifically, attaching arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole is usually required so it can write to CloudWatch Logs.
- If you need to reference the AWS account id, use the literal token "<YOUR_ACCOUNT_ID>" in ARNs (the runner will substitute it).

User request:
%q`, destructiveRule, question)
}
