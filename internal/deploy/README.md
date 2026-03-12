# Deploy Intelligence Pipeline

This package powers the `clanker deploy` intelligence flow from user query to plan/apply.

## Rule-Pack Layer

The deploy package now has a small typed rule-pack layer in [internal/deploy/rule_packs.go](rule_packs.go).

- **Provider packs** hold provider-specific hooks such as DigitalOcean autofix/validation and AWS validation.
- **App packs** hold app-specific hooks such as OpenClaw and WordPress architecture defaults, prompt requirements, and app-aware autofix/validation.
- The current implementation is intentionally thin: it **routes to the existing low-level logic** instead of replacing it.
- Goal: reduce drift between prompt text, autofix, deterministic validation, and future backend parity work while keeping generic one-click deploy behavior intact.

Related shared text/helpers:

- [internal/deploy/openclaw_rules.go](openclaw_rules.go) centralizes repeated OpenClaw DigitalOcean guidance used by prompts, skeleton generation, and user-data repair.

## Current Typed Seams

Most of the package is still intentionally pragmatic and string-heavy, but a few narrow typed seams now exist to keep the highest-risk OpenClaw DigitalOcean paths deterministic without rewriting the whole planner.

- **Rule-pack routing** in [internal/deploy/rule_packs.go](rule_packs.go) is the small typed dispatch layer for provider/app hooks.
- **Skeleton capability hints** in [internal/deploy/skeleton_plan.go](skeleton_plan.go) attach lightweight provider/app/runtime constraints to the high-level plan before hydration.
  Those hints are now also stamped onto hydrated plan JSON as plan-level capabilities metadata for downstream backend normalization/preflight.
  The paged fallback planner now stamps the same inferred plan-level capabilities so backend checks do not lose metadata when skeleton generation is skipped.
  WordPress on AWS now emits richer capability metadata too, so backend review/repair and preflight can keep the EC2 + ALB + Docker Hub runtime shape intact.
  OpenClaw on AWS now emits stronger EC2 + ALB + CloudFront capability metadata as well, so backend checks can keep the required HTTPS pairing shape.
  Backend normalization also uses that metadata now, so OpenClaw AWS autofill for CloudFront outputs/waiters and ECR pull viability still works even when the raw plan text is sparse.
  Backend AWS normalization also uses WordPress capability metadata to strip accidental ECR/build flows and normalize the ALB health-check path back to /wp-login.php.
  Backend app detection for rule-pack routing and advisory normalization is now centralized too, reducing drift between OpenClaw and WordPress matching paths.
  Backend capability step-family matching is centralized now as well, so required/forbidden capability checks and app-specific preflight use the same command-family view.
- **OpenClaw DO bootstrap canonicalization** in [internal/deploy/openclaw_rules.go](openclaw_rules.go) infers a minimal bootstrap spec from droplet user-data and renders one canonical script shape.
- **OpenClaw DO firewall canonicalization** in [internal/deploy/openclaw_rules.go](openclaw_rules.go) lifts firewall rule strings into a small typed firewall spec, then re-renders one canonical `doctl compute firewall` rule layout.
- **DigitalOcean command-schema gating** in [internal/deploy/do_command_schema.go](do_command_schema.go) rejects hallucinated command families early, enforces placeholder production order across paged planning, and blocks strict-schema regressions during repair/review.

Current OpenClaw DigitalOcean invariants:

- OpenClaw runtime image is built on the droplet with `docker build -t openclaw:local .`
- App Platform provides the managed HTTPS front door on a DigitalOcean-owned hostname
- DOCR is allowed only for the small App Platform proxy image, not for the OpenClaw runtime image
- inbound TCP must include `22`, `18789`, and `18790`
- outbound must include `tcp/all`, `udp/all`, and `icmp/all`
- empty `address:` fields are normalized to `0.0.0.0/0`
- repeated `--inbound-rules` / `--outbound-rules` flags are merged into one canonical arg each
- droplet-facing firewall rules stay on the gateway ports only; browser HTTPS comes from App Platform rather than droplet ports `80` or `443`
- plans must include `compute ssh-key import`, `compute firewall create`, `compute droplet create`, `compute firewall add-droplets`, `registry create`, `registry login`, `docker build`, `docker push`, and `apps create`
- after `apps create` resolves the managed HTTPS URL, the executor patches `gateway.controlUi.allowedOrigins` over SSH on the droplet

This is the current architecture direction: keep planning broad and non-deterministic, then lower the riskiest execution surfaces into small typed canonical forms.

## Query → Deploy (Current Flow)

1. **Input + context setup**
    - `cmd/deploy.go` parses flags, provider/profile, and AI settings.
    - Repo is cloned and profiled (`CloneAndAnalyze`) for language, framework, ports, Docker/Compose, env hints.

2. **Intelligence pipeline (`RunIntelligence`)**
    - **Phase 0: Explore repo** (`explorer.go`) — agentic file reads to gather missing context.
    - **Phase 1: Deep analysis** (`intelligence.go`) — app behavior, services, startup/build commands, env requirements.
    - **Phase 1.25: Docker analysis** (`docker_agent.go`) — Docker/Compose topology, primary port, container runtime hints.
    - **Phase 1.5: Infra scan** (`infra_scan.go`, `cf_infra_scan.go`) — existing cloud resources to reuse.
    - **Phase 2: Architecture decision** (`intelligence.go`) — method/provider recommendation (e.g. EC2 for OpenClaw).
    - App rule packs can apply deterministic architecture overrides and append app/provider deployment requirements to the planning prompt.
    - Produces `EnrichedPrompt` for planning.

3. **Skeleton + hydrate plan generation (`skeleton_plan.go`) — primary path**
    - **Phase 3a: Skeleton** — single LLM call produces a lightweight `PlanSkeleton` (service, operation, reason, produces, dependsOn per step). No real CLI args yet.
    - Skeletons now also carry lightweight typed `Capabilities` hints so provider/app/runtime constraints survive into hydration without forcing a rigid full-plan schema.
    - Skeleton is validated (`validateSkeleton`): checks required launch ops are present, flags duplicates using a composite key of `(service, operation, produces, dependsOn)`.
    - Capability-aware validation also checks required/forbidden step families for typed cases such as OpenClaw on DigitalOcean.
    - **Phase 3b: Hydrate** — skeleton steps are batched (max 5 consecutive independent steps per batch) and each batch is hydrated into real `maker.Command` structs via separate LLM calls.
    - Hydrate prompts receive the same capability hints so later detail generation keeps the same execution model.
    - Hydrated commands are now checked structurally against the requested skeleton step family and capability constraints before they are accepted.
    - Hydrate prompts enforce resource name consistency across steps (e.g. ECR repo names in user-data must match earlier `ecr create-repository` commands).
    - If skeleton or hydration fails, falls back to the **paged plan generation** path.

    3b. **Paged plan generation (`paged_plan.go`) — fallback path**
    - Plan is generated in **small command pages** instead of one large response.
    - Each page is parsed (`ParsePlanPage`), normalized via `maker.ParsePlan`, and appended with dedupe (`AppendPlanPage`).
    - Parser tolerates either a page object or a plain command array (`[]commands`) from the LLM.
    - Page prompts include current command tail + produced bindings + required launch operations + unresolved hard issues.
    - When both skeleton and paged plans exist, the one with fewer deterministic issues wins.

4. **Generic plan autofix (`plan_autofix.go`)**
    - Runs after plan generation (both skeleton and paged paths).
    - Before generic cleanup, matching rule packs may run app/provider-specific autofix hooks (for example OpenClaw and DigitalOcean passes).
    - DigitalOcean autofix in [internal/deploy/do_plan_autofix.go](do_plan_autofix.go) is currently the main deterministic cleanup point for droplet user-data and firewall command repair.
    - **SSM semantic dedup** — deduplicates SSM `send-command` / `put-parameter` steps that do the same thing.
    - **Launch cycle dedup** — removes redundant `run-instances` cycles within the same project.
    - **Read-only dedup** — collapses repeated read-only commands (describe/get/list).
    - **Orphan placeholder pruning** — removes commands that reference `<PLACEHOLDER>` values never produced by any earlier command. Accepts `externalBindings` (user-provided env var names) so user env vars are treated as "produced" and not orphan-pruned.
    - **User-data placeholder normalization** — rewrites `<USER_DATA_*>` variants to canonical `<USER_DATA>`.
    - **Critical command protection** — `run-instances`, `create-load-balancer`, `create-distribution` are never removed by autofix.

5. **Deterministic guardrails (`plan_preflight_validate.go`)**
    - Deterministic checks run for hard failures:
        - launch step missing,
        - OpenClaw onboarding/compose requirements,
        - missing compose-required env vars,
        - secret inlining,
        - AWS wiring sanity checks.
    - DigitalOcean strict-schema checks now also reject fake command families, enforce placeholder production order during paging/repair, and verify the OpenClaw droplet plus App Platform proxy image flow stays internally consistent.
    - DigitalOcean validation now also reads firewall rules through the typed OpenClaw DO firewall spec so repeated flags, missing required ports, and bad plain-droplet ingress are checked from one normalized view.
    - Provider/app rule packs contribute deterministic validation hooks so provider-specific and app-specific checks are routed from one place.
    - Waiter/order sanity for AWS runtime wiring (`ec2 wait instance-running` before target registration, `elbv2 wait load-balancer-available` before listener creation).
    - CloudFront command-shape sanity (`create-distribution` must not carry `--tags`) and OpenClaw output contract (`CLOUDFRONT_DOMAIN` + full `HTTPS_URL` with `https://`).
    - **User-data vs plan cross-check** (`crossCheckUserDataVsPlan`) — decodes base64 user-data from `run-instances` commands, extracts ECR image references, and verifies they match ECR repositories created in the plan. Catches hallucinated repo name mismatches.
    - **Bulk invariant checks:**
        - non-empty command list, no unresolved placeholders,
        - IAM instance-profile readiness before EC2 launch,
        - user-data quote sanity (detects unterminated quote breakages).
    - **Project overlay invariants:**
        - OpenClaw: provider-appropriate HTTPS pairing URL, onboarding before gateway start.
    - Stuck detection fails fast in `--apply`; in plan-only mode it logs warnings and returns best-effort output.

6. **Deterministic repair + triage**
    - If planning ends with deterministic issues, repair rounds (`plan_repair_agent.go`) patch the plan JSON and re-validate.
    - Findings are triaged (`plan_issue_triage.go`) into `hard-fixable`, `likely-noise`, and `context-needed`.
    - Repair prompts enforce strict contract: preserve valid commands, minimal diff, fix only listed issues.
    - **User-data micro-repair** — targeted LLM fix for user-data script issues without touching the rest of the plan.

7. **Conservative sanitizer (`plan_sanitize.go`)**
    - Sanitization is **fail-open**: original vs sanitized plans are compared via deterministic issue count.
    - Sanitized plan is used only when it is not worse than original.
    - Includes generic arg normalization and safe command cleanup across providers.

8. **LLM validation + repair (`ValidatePlan`)**
    - Once deterministic checks pass, the LLM validator reviews ordering/missing steps/port/env/IAM chaining.
    - If invalid, repair rounds rewrite plan JSON and re-validate.
    - Repair/review parsing uses LLM JSON-repair helpers (`llm_plan_integrity.go`) before giving up on a candidate.
    - Retention guard is issue-driven: allows focused removals when issues/fixes justify them, blocks broad command collapse.

9. **Final review + integrity passes**
    - **Review agent** (`plan_review_agent.go`) — non-blocking pass that can append missing commands.
    - **Generic integrity pass** (`llm_plan_integrity.go`) — provider-agnostic minimal-diff fixes (tokenization, waiter usage, `run-instances` flag/script boundary, CloudFront config shape) without architecture drift.

10. **Plan finalize + apply orchestration**
    - Placeholder/binding resolution and provider-specific enrichment.
    - In `--apply`, execution is staged (infra → build/push → workload launch → verification).
    - OpenClaw apply path seeds runtime env bindings from collected config and process env.
    - `--enforce-image-deploy` forces ECR image-based deploy (build/push + pull/run).
    - SSH safety rule: plans with SSH ingress on port 22 need explicit CIDR or fall back to SSM-only.
    - Auto-remediation prompts include deployment intent so self-heal stays aligned.

## Compact Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    participant U as User
    participant C as cmd/deploy.go
    participant I as RunIntelligence
    participant S as Skeleton+Hydrate
    participant P as Paged Planner (fallback)
    participant A as Generic Autofix
    participant D as Deterministic Validator
    participant R as Plan Repair Agent
    participant V as LLM Validator
    participant E as Maker Executor

    U->>C: clanker deploy <repo>
    C->>C: Clone + static profile
    C->>I: RunIntelligence(profile, provider)
    I-->>C: enriched prompt + architecture + infra hints

    C->>S: GeneratePlanSkeleton (1 LLM call)
    S-->>C: PlanSkeleton (steps + placeholders)
    C->>S: HydrateSkeleton (batched LLM calls)
    S-->>C: hydrated plan commands

    alt skeleton/hydrate failed
      loop page 1..N
        C->>P: BuildPlanPagePrompt(current state)
        P-->>C: {done, commands[]}
      end
    end

    C->>A: ApplyGenericPlanAutofix(plan, externalBindings)
    A-->>C: deduped + pruned plan

    C->>D: deterministic validate (+ crossCheckUserDataVsPlan)
    D-->>C: hard issues / pass

    alt deterministic issues
      C->>R: user-data micro-repair / full repair
      R-->>C: patched plan
      C->>D: re-validate
    end

    C->>C: SanitizePlanConservative

    C->>V: LLM validate + repair loop
    V-->>C: validated plan

    alt --apply
      C->>E: execute staged plan
      E-->>U: deploy result + outputs
    else plan-only
      C-->>U: plan JSON + warnings
    end
```

## Key Files

- `rule_packs.go` — typed provider/app rule-pack registry and hook routing
- `do_command_helpers.go` — shared DigitalOcean command-shape helpers (droplet detection, flag counting, user-data extraction)
- `openclaw_rules.go` — shared OpenClaw DigitalOcean rule text plus typed bootstrap/firewall canonicalization helpers
- `skeleton_plan.go` — two-phase skeleton+hydrate plan generation (primary path)
- `paged_plan.go` — paginated planning protocol (fallback path)
- `plan_autofix.go` — generic plan autofix (dedup, orphan pruning, critical command protection)
- `do_plan_autofix.go` — DigitalOcean-specific deterministic cleanup for doctl arg repair, user-data repair, and OpenClaw firewall normalization
- `plan_preflight_validate.go` — deterministic hard checks + user-data vs plan cross-check
- `plan_repair_agent.go` — plan rewrite/repair agent
- `plan_issue_triage.go` — triage for hard-fixable vs noise/context findings
- `plan_sanitize.go` — conservative fail-open plan sanitizer
- `plan_review_agent.go` — final non-blocking plan reviewer pass
- `llm_plan_integrity.go` — LLM JSON repair + generic integrity pass
- `intelligence.go` — multi-phase intelligence + LLM validation
- `explorer.go` — agentic file exploration
- `docker_agent.go` — Docker/Compose understanding
- `infra_scan.go` / `cf_infra_scan.go` — cloud inventory snapshots
- `openclaw_plan_autofix.go` — OpenClaw-specific autofix (HTTPS_URL, compose hints)
- `userdata_autofix.go` / `userdata_fixups.go` / `userdata_repair.go` — user-data fixups
- `resolve.go` — placeholder/binding resolution
- `nodejs_userdata.go` — Node.js user-data generation
