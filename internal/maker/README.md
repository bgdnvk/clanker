# maker (maintainers)

This package turns a natural-language request into an AWS CLI plan, lets the user review it, then applies it safely and idempotently.

## Hard invariants (do not break)

-   **AWS CLI only**. Every command is `args[]` (no shell strings, no pipes/redirects/subshell).
-   Runner injects `--profile`, `--region`, `--no-cli-pager`. Plans must NOT include them.
-   If an ARN needs account id, use the literal token `<YOUR_ACCOUNT_ID>` (runner substitutes via STS).
-   **No local artifacts**: Lambda code uses `--zip-file fileb://-` and the runner injects an in-memory zip.
-   **Plan → Apply gating**: planning is read-only; execution happens only on explicit apply.
-   **Destroyer mode**: destructive remediations/ignores must be behind `opts.Destroyer`.

## File structure

| File | Purpose |
|------|---------||
| `plan.go` | Plan JSON schema + normalization |
| `prompt.go` | Planner prompt, constraints, AWS CLI syntax reference |
| `enrich.go` | Planning-time expansion (prereqs, role inference, dedupe) |
| `enrich_sg.go` | Security group enrichment helpers |
| `exec.go` | Execution loop, classification, orchestration, binding learning |
| `resources_glue.go` | Per-service runtime glue (rewrites/waiters/idempotency) |
| `generic_glue.go` | Cross-service glue + LLM escalation |
| `cloudformation_waiter.go` | CloudFormation terminal waiter + failure summarizer |
| `ec2_vpc_cidr_glue.go` | VPC/subnet CIDR remediation helpers |
| `remediate.go` | Built-in remediation pipeline |
| `remediate_ai.go` | AI-powered remediation for prerequisites |
| `agentic_fix.go` | **Agentic failure handling** - sends errors to AI with exponential backoff |
| `resolve_placeholders.go` | AI-powered placeholder resolution before execution |
| `retry.go` | Retry/backoff helpers |

## Flow

```
User Request
     │
     ▼
┌─────────────────┐
│   prompt.go     │  Generate AWS CLI plan via LLM
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│   enrich.go     │  Expand plan (add prereqs, dedupe)
└────────┬────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────┐
│                     exec.go                             │
│  For each command:                                      │
│  1. Apply bindings (applyPlanBindings)                  │
│  2. If unresolved placeholders → resolve_placeholders   │
│  3. Run AWS CLI command                                 │
│  4. On success → learn bindings                         │
│  5. On failure → handleAWSFailure                       │
└────────┬────────────────────────────────────────────────┘
         │ On failure
         ▼
┌─────────────────────────────────────────────────────────┐
│              handleAWSFailure (exec.go)                 │
│  1. Classify error                                      │
│  2. shouldIgnoreFailure? → skip                         │
│  3. maybeRewriteAndRetry (resources_glue.go)            │
│  4. maybeAutoRemediateAndRetry (remediate.go)           │
│  5. maybeAgenticFix (agentic_fix.go)                    │
└─────────────────────────────────────────────────────────┘
```

## Agentic failure handling

When a command fails and built-in fixes don't work, `agentic_fix.go` kicks in:

1. Sends the failed command + error output + current bindings to AI
2. AI returns a structured fix:
    - `skip`: true if command should be skipped (already done)
    - `bindings`: discovered placeholder values
    - `pre_commands`: commands to run before retry
    - `rewritten_args`: corrected command syntax
3. Applies the fix and retries
4. Uses **exponential backoff** (1s, 2s, 4s) up to 3 attempts
5. Each retry sends fresh error context to AI

## Plan placeholders + bindings (runtime)

Plans may contain placeholder tokens like `<IGW_ID>` or `<SUB_PUB_1_ID>`.

At runtime, the executor keeps a `bindings` map and rewrites args by replacing any `<TOKEN>` with `bindings[TOKEN]`.

Bindings are learned from:

-   **Explicit `produces`** on a command (preferred). This is a map of `bindingKey -> jsonPath` extracted from the command JSON output.
    -   Example: `{"IGW_ID": "$.InternetGateway.InternetGatewayId"}`.
    -   JSONPath is intentionally small: object field traversal + `[index]` (e.g. `$.Foo.Bar[0].Baz`).
-   **Heuristics** from successful AWS CLI JSON outputs via `learnPlanBindings()` for common create operations.
-   **Dynamic inference** via `infer*Bindings()` functions that generate multiple placeholder variations from resource names/IDs.
-   **Glue updates**: some remediations discover "real" IDs and populate/override bindings.

### Dynamic binding inference (exec.go)

The following `infer*` functions generate multiple placeholder variations:

| Function                  | Input                          | Example outputs                       |
| ------------------------- | ------------------------------ | ------------------------------------- |
| `inferSGBindings`         | group name "lambdatron-rds-sg" | `SG_RDS_ID`, `SG_RDS`, `RdsSgId`      |
| `inferSubnetBindings`     | subnet ID, AZ                  | `SUBNET_1`, `SUBNET_A`, `SUBNET_1_ID` |
| `inferLambdaBindings`     | function ARN                   | `LAMBDA_ARN`, `FUNCTION_ARN`          |
| `inferAPIGatewayBindings` | API ID                         | `API_ID`, `APIGW_ID`, `HTTP_API_ID`   |
| `inferRDSBindings`        | instance ID, endpoint, ARN     | `RDS_INSTANCE_ID`, `DB_ENDPOINT`      |
| `inferECSClusterBindings` | name, ARN                      | `ECS_CLUSTER`, `ECS_CLUSTER_ARN`      |
| `inferSNSBindings`        | topic ARN                      | `SNS_TOPIC_ARN`, `TOPIC_NAME`         |
| `inferSQSBindings`        | queue URL                      | `SQS_QUEUE_URL`, `QUEUE_NAME`         |
| `inferDynamoDBBindings`   | table name, ARN                | `DYNAMODB_TABLE`, `TABLE_ARN`         |
| `inferS3Bindings`         | bucket name                    | `S3_BUCKET`, `BUCKET_NAME`            |

Common alias keys are supported because planner output varies:

-   Subnets: `SUBNET_PUB_1_ID` / `SUB_PUB_1_ID` / `SUB_PUB_1`, `SUBNET_PUB_2_ID` / `SUB_PUB_2_ID` / `SUB_PUB_2`, `SUBNET_PRIV_1_ID` / `SUB_PRIV_ID` / `SUB_PRIV`
-   Route tables: `RT_PUBLIC_ID` / `RT_PUB_ID` / `RT_PUB`, `RT_PRIVATE_ID` / `RT_PRIV_ID` / `RT_PRIV`
-   Security groups: `SG_ALB_ID` / `ALB_SG_ID`, `SG_WEB_ID` / `WEB_SG_ID`

## Runtime self-healing (examples)

This is not an exhaustive list; it’s here to help maintainers understand the intended direction:

-   **VPC subnet CIDR validity**: if `ec2 create-subnet` fails with `InvalidSubnet.Range`, pick a free CIDR inside the target VPC CIDR and retry.
-   **Missing/invalid route table IDs**: if `create-route` / `associate-route-table` fails due to an invalid route table id, create one, bind it, rewrite `--route-table-id`, retry.
-   **Existing IGW already attached**: if `attach-internet-gateway` says the VPC already has one, discover the attached IGW, bind it, and continue.
-   **Async readiness**: waiters/backoff for long-lived resources (e.g. NAT gateway, CloudFormation).
-   **API Gateway v1/v2 mismatch (destroyer)**: fall back to v2 deletes when a v2 API is deleted via v1 commands.
-   **Duplicate resources**: `InvalidGroup.Duplicate`, `DBSubnetGroupAlreadyExists`, `InvalidSubnet.Conflict` are ignored as idempotent.

## Retry + AI escalation (runtime)

-   The execution loop prefers deterministic, built-in glue first (rewrite/wait/retry).
-   If a command keeps failing after retries/glue, **agentic fix** asks the AI for a structured fix.
-   The AI can propose: skip, new bindings, pre-commands, or a rewritten command.
-   Runner applies the fix and retries with exponential backoff (3 attempts, 1s→2s→4s).

## Adding support for a new AWS quirk

Preferred order:

1. **Glue rule** in `resources_glue.go` (rewrite/wait/retry) for runtime correctness.
2. **Binding inference** in `exec.go` via a new `infer*Bindings()` function for dynamic placeholder support.
3. **Enricher** in `enrich.go` only if you need the user to _review_ extra prereq steps up front.
4. **Ignore rule** in `shouldIgnoreFailure()` only for true idempotent "already gone / already exists" cases.

Checklist:

-   Match narrowly on `service/op` + specific error code/message.
-   Keep changes idempotent and safe on retries.
-   If it’s destructive, require `opts.Destroyer == true`.
-   Don’t add non-AWS commands, don’t add filesystem requirements.
