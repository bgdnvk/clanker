package deploy

import (
	"context"
	"fmt"
	"strings"
)

type PlanReviewContext struct {
	Provider                  string
	Method                    string
	RepoURL                   string
	ProjectSummary            string
	ProjectCharacteristics    []string
	IsOpenClaw                bool
	OpenClawCloudFrontMissing bool
	IsWordPress               bool
	Issues                    []string
	Fixes                     []string
	Warnings                  []string
}

type PlanReviewAgent struct {
	ask   AskFunc
	clean CleanFunc
	logf  func(string, ...any)
}

func NewPlanReviewAgent(ask AskFunc, clean CleanFunc, logf func(string, ...any)) *PlanReviewAgent {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &PlanReviewAgent{ask: ask, clean: clean, logf: logf}
}

func (a *PlanReviewAgent) Review(ctx context.Context, planJSON string, c PlanReviewContext) (string, error) {
	if a == nil || a.ask == nil || a.clean == nil {
		return "", fmt.Errorf("plan review agent not configured")
	}
	if strings.TrimSpace(planJSON) == "" {
		return "", fmt.Errorf("missing plan json")
	}

	resp, err := a.ask(ctx, a.buildPrompt(planJSON, c))
	if err != nil {
		return "", err
	}

	reviewed := strings.TrimSpace(a.clean(resp))
	if reviewed == "" {
		reviewed = strings.TrimSpace(planJSON)
	}

	if len(c.Issues) > 0 || len(c.Fixes) > 0 {
		respFix, errFix := a.ask(ctx, a.buildIssueFixPrompt(reviewed, c))
		if errFix == nil {
			patched := strings.TrimSpace(a.clean(respFix))
			if patched != "" {
				reviewed = patched
			}
		} else {
			a.logf("[deploy] final review: issue-fix pass skipped (%v)", errFix)
		}
	}

	if c.IsOpenClaw && c.OpenClawCloudFrontMissing && strings.EqualFold(strings.TrimSpace(c.Provider), "aws") {
		method := strings.ToLower(strings.TrimSpace(c.Method))
		if method == "" || method == "ec2" {
			if !containsCloudFrontCommands(reviewed) {
				a.logf("[deploy] final review: OpenClaw AWS plan missing CloudFront commands; running targeted augmentation pass")
				resp2, err2 := a.ask(ctx, a.buildOpenClawCloudFrontPrompt(reviewed, c))
				if err2 == nil {
					augmented := strings.TrimSpace(a.clean(resp2))
					if augmented != "" {
						reviewed = augmented
					}
				}
			}
		}
	}

	return reviewed, nil
}

func containsCloudFrontCommands(planJSON string) bool {
	lower := strings.ToLower(strings.TrimSpace(planJSON))
	if lower == "" {
		return false
	}
	hasCreate := strings.Contains(lower, "cloudfront") && strings.Contains(lower, "create-distribution")
	hasWait := strings.Contains(lower, "cloudfront") && strings.Contains(lower, "distribution-deployed")
	hasOutput := strings.Contains(lower, "cloudfront_domain") || strings.Contains(lower, "https_url")
	return hasCreate && hasWait && hasOutput
}

func HasOpenClawCloudFront(planJSON string) bool {
	return containsCloudFrontCommands(planJSON)
}

func (a *PlanReviewAgent) buildPrompt(planJSON string, c PlanReviewContext) string {
	prov := strings.ToLower(strings.TrimSpace(c.Provider))
	if prov == "" {
		prov = "aws"
	}

	p := strings.TrimSpace(planJSON)
	if len(p) > 22000 {
		p = p[:22000] + "…"
	}

	var b strings.Builder
	b.WriteString("You are the FINAL deployment plan reviewer.\n")
	b.WriteString("Read the current plan JSON and return a corrected FINAL plan JSON.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Output ONLY one valid plan JSON object (no markdown/prose).\n")
	b.WriteString("- Keep existing valid commands; add only what is missing.\n")
	b.WriteString("- Keep command args as CLI tokens (no shell wrappers, pipes, redirects).\n")
	b.WriteString("- Keep provider constraints: AWS CLI command args without leading 'aws'.\n")
	b.WriteString("- Do NOT add --profile/--region/--no-cli-pager; runtime injects those.\n")
	b.WriteString("- Ensure plan.commands remains non-empty.\n\n")

	b.WriteString("Context:\n")
	b.WriteString("- provider: " + prov + "\n")
	if strings.TrimSpace(c.Method) != "" {
		b.WriteString("- method: " + strings.TrimSpace(c.Method) + "\n")
	}
	if strings.TrimSpace(c.RepoURL) != "" {
		b.WriteString("- repo: " + strings.TrimSpace(c.RepoURL) + "\n")
	}
	if strings.TrimSpace(c.ProjectSummary) != "" {
		b.WriteString("- project_summary: " + strings.TrimSpace(c.ProjectSummary) + "\n")
	}
	if len(c.ProjectCharacteristics) > 0 {
		b.WriteString("- project_characteristics:\n")
		for _, s := range c.ProjectCharacteristics {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			b.WriteString("  - " + s + "\n")
		}
	}
	b.WriteString("- openclaw: ")
	if c.IsOpenClaw {
		b.WriteString("true\n\n")
	} else {
		b.WriteString("false\n")
	}
	b.WriteString("- openclaw_cloudfront_missing: ")
	if c.OpenClawCloudFrontMissing {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
	b.WriteString("- wordpress: ")
	if c.IsWordPress {
		b.WriteString("true\n\n")
	} else {
		b.WriteString("false\n\n")
	}

	b.WriteString("Requirement checklist:\n")
	b.WriteString("- Plan must include a concrete workload launch step for the chosen method.\n")
	if c.IsOpenClaw && c.OpenClawCloudFrontMissing {
		b.WriteString("- If openclaw=true and openclaw_cloudfront_missing=true and provider=aws:\n")
		b.WriteString("  1) deployment method must be EC2 (include ec2 run-instances),\n")
		b.WriteString("  2) include CloudFront HTTPS in front of ALB; this HTTPS URL is REQUIRED for OpenClaw pairing (not just websocket transport):\n")
		b.WriteString("     - cloudfront create-distribution (with ALB origin),\n")
		b.WriteString("     - cloudfront wait distribution-deployed,\n")
		b.WriteString("  3) include produces bindings for CLOUDFRONT_DOMAIN and/or HTTPS_URL when feasible and make HTTPS primary URL in notes.\n")
	} else if c.IsOpenClaw {
		b.WriteString("- If openclaw=true and openclaw_cloudfront_missing=false: do NOT add duplicate CloudFront commands; keep existing CloudFront/HTTPS pairing steps unchanged.\n")
	}
	b.WriteString("- If wordpress=true and provider=aws:\n")
	b.WriteString("  1) deployment method should be EC2 (include ec2 run-instances),\n")
	b.WriteString("  2) include ALB wiring for port 80 with health check path /wp-login.php,\n")
	b.WriteString("  3) include Docker Hub wordpress + mariadb runtime steps and persistent Docker volumes,\n")
	b.WriteString("  4) require WORDPRESS_DB_PASSWORD as user-provided secret and do not persist it to SSM Parameter Store.\n")
	b.WriteString("- If requirements are already satisfied, return the plan unchanged.\n\n")

	if len(c.Issues) > 0 || len(c.Fixes) > 0 || len(c.Warnings) > 0 {
		b.WriteString("Current known plan findings to address:\n")
		for i, issue := range c.Issues {
			if i >= 15 {
				break
			}
			issue = strings.TrimSpace(issue)
			if issue == "" {
				continue
			}
			b.WriteString("- ISSUE: " + issue + "\n")
		}
		for i, fix := range c.Fixes {
			if i >= 15 {
				break
			}
			fix = strings.TrimSpace(fix)
			if fix == "" {
				continue
			}
			b.WriteString("- FIX: " + fix + "\n")
		}
		for i, warning := range c.Warnings {
			if i >= 10 {
				break
			}
			warning = strings.TrimSpace(warning)
			if warning == "" {
				continue
			}
			b.WriteString("- WARNING: " + warning + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Current plan JSON:\n")
	b.WriteString(p)
	b.WriteString("\n\nReturn the final reviewed plan JSON only.\n")

	return b.String()
}

func (a *PlanReviewAgent) buildOpenClawCloudFrontPrompt(planJSON string, c PlanReviewContext) string {
	p := strings.TrimSpace(planJSON)
	if len(p) > 26000 {
		p = p[:26000] + "…"
	}

	var b strings.Builder
	b.WriteString("You are patching a FINAL OpenClaw AWS EC2 plan that is missing CloudFront HTTPS steps.\n")
	b.WriteString("Return ONE corrected plan JSON object only.\n\n")
	b.WriteString("Hard requirements for this patch:\n")
	b.WriteString("- Keep existing valid commands intact; append missing commands only.\n")
	b.WriteString("- Include cloudfront create-distribution in front of ALB origin.\n")
	b.WriteString("- Include cloudfront wait distribution-deployed.\n")
	b.WriteString("- Ensure resulting plan includes produces bindings for CLOUDFRONT_DOMAIN and/or HTTPS_URL.\n")
	b.WriteString("- Ensure notes/state make it explicit that HTTPS via CloudFront is the REQUIRED OpenClaw pairing URL.\n")
	b.WriteString("- Keep AWS CLI args without leading 'aws'.\n")
	b.WriteString("- No markdown, no prose, JSON object only.\n\n")
	b.WriteString("Context:\n")
	b.WriteString("- provider: aws\n")
	b.WriteString("- method: ec2\n")
	if strings.TrimSpace(c.RepoURL) != "" {
		b.WriteString("- repo: " + strings.TrimSpace(c.RepoURL) + "\n")
	}
	b.WriteString("- openclaw: true\n\n")
	b.WriteString("Current plan JSON:\n")
	b.WriteString(p)
	b.WriteString("\n\nReturn corrected plan JSON only.\n")
	return b.String()
}

func (a *PlanReviewAgent) buildIssueFixPrompt(planJSON string, c PlanReviewContext) string {
	p := strings.TrimSpace(planJSON)
	if len(p) > 26000 {
		p = p[:26000] + "…"
	}

	var b strings.Builder
	b.WriteString("You are doing a focused issue-fix pass on a deployment plan JSON.\n")
	b.WriteString("Return ONE corrected plan JSON object only.\n")
	b.WriteString("Apply fixes for the listed issues while preserving valid commands and ordering.\n")
	b.WriteString("Do not output markdown or prose.\n\n")

	b.WriteString("Context:\n")
	b.WriteString("- provider: " + strings.TrimSpace(c.Provider) + "\n")
	b.WriteString("- method: " + strings.TrimSpace(c.Method) + "\n")
	if strings.TrimSpace(c.ProjectSummary) != "" {
		b.WriteString("- project_summary: " + strings.TrimSpace(c.ProjectSummary) + "\n")
	}
	b.WriteString("\nFindings to fix:\n")
	for i, issue := range c.Issues {
		if i >= 20 {
			break
		}
		issue = strings.TrimSpace(issue)
		if issue == "" {
			continue
		}
		b.WriteString("- ISSUE: " + issue + "\n")
	}
	for i, fix := range c.Fixes {
		if i >= 20 {
			break
		}
		fix = strings.TrimSpace(fix)
		if fix == "" {
			continue
		}
		b.WriteString("- FIX: " + fix + "\n")
	}

	b.WriteString("\nCurrent plan JSON:\n")
	b.WriteString(p)
	b.WriteString("\n\nReturn corrected plan JSON only.\n")
	return b.String()
}
