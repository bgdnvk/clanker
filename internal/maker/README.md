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
