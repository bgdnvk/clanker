# maker (maintainers)

This package turns a natural-language request into an AWS CLI plan, lets the user review it, then applies it safely and idempotently.

## Hard invariants (do not break)

-   **AWS CLI only**. Every command is `args[]` (no shell strings, no pipes/redirects/subshell).
-   Runner injects `--profile`, `--region`, `--no-cli-pager`. Plans must NOT include them.
-   If an ARN needs account id, use the literal token `<YOUR_ACCOUNT_ID>` (runner substitutes via STS).
-   **No local artifacts**: Lambda code uses `--zip-file fileb://-` and the runner injects an in-memory zip.
-   **Plan → Apply gating**: planning is read-only; execution happens only on explicit apply.
-   **Destroyer mode**: destructive remediations/ignores must be behind `opts.Destroyer`.

## Flow (where things live)

-   Plan JSON schema + normalization: `plan.go`
-   Planner prompt / constraints: `prompt.go`
-   Planning-time expansion (explicit prereqs, role inference, dedupe): `enrich.go` (+ `enrich_sg.go`)
-   Execution loop + classification + orchestration: `exec.go`
-   Runtime glue (rewrites/waiters/create→update/idempotency): `resources_glue.go`
-   Generic cross-service glue + LLM escalation (after retries): `generic_glue.go`
-   CloudFormation terminal waiter + failure summarizer: `cloudformation_waiter.go`
-   VPC/subnet CIDR remediation helpers: `ec2_vpc_cidr_glue.go`
-   Remediation pipeline (built-in + optional AI prereqs): `remediate.go`, `remediate_ai.go`
-   Retry/backoff helpers: `retry.go`

## Plan placeholders + bindings (runtime)

Plans may contain placeholder tokens like `<IGW_ID>` or `<SUB_PUB_1_ID>`.

At runtime, the executor keeps a `bindings` map and rewrites args by replacing any `<TOKEN>` with `bindings[TOKEN]`.

Bindings are learned from:

-   **Explicit `produces`** on a command (preferred). This is a map of `bindingKey -> jsonPath` extracted from the command JSON output.
    -   Example: `{"IGW_ID": "$.InternetGateway.InternetGatewayId"}`.
    -   JSONPath is intentionally small: object field traversal + `[index]` (e.g. `$.Foo.Bar[0].Baz`).
-   **Heuristics** from successful AWS CLI JSON outputs for common create operations (subnets/IGW/route tables/EIP/NAT/SG/instance/ALB/TG).
-   **Glue updates**: some remediations discover “real” IDs (e.g. existing attached IGW) and populate/override bindings so the plan can continue.

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

## Retry + AI escalation (runtime)

-   The execution loop prefers deterministic, built-in glue first (rewrite/wait/retry).
-   If a command keeps failing after retries/glue, the runner can ask the AI for prerequisite AWS CLI commands.
-   After running those prerequisites, the runner retries the original failing AWS CLI operation with exponential backoff (3 attempts).

## Adding support for a new AWS quirk

Preferred order:

1. **Glue rule** in `resources_glue.go` (rewrite/wait/retry) for runtime correctness.
2. **Enricher** in `enrich.go` only if you need the user to _review_ extra prereq steps up front.
3. **Ignore rule** only for true idempotent “already gone / already exists” cases.

Checklist:

-   Match narrowly on `service/op` + specific error code/message.
-   Keep changes idempotent and safe on retries.
-   If it’s destructive, require `opts.Destroyer == true`.
-   Don’t add non-AWS commands, don’t add filesystem requirements.
