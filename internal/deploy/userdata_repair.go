package deploy

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

// RepairUserDataWithLLM performs a targeted micro-repair on user-data scripts
// inside ec2 run-instances or compute droplet create commands. Instead of
// sending the full plan to the repair LLM (which causes full rewrites), we
// extract the script, send ONLY the script + relevant issues, and splice the
// fix back into the plan.
// Returns the patched plan and nil error on success. Returns the original
// plan unchanged if no user-data issues are found or repair fails.
func RepairUserDataWithLLM(
	ctx context.Context,
	plan *maker.Plan,
	issues []string,
	fixes []string,
	ask AskFunc,
	clean CleanFunc,
	logf func(string, ...any),
) (*maker.Plan, error) {
	if plan == nil || ask == nil || clean == nil {
		return plan, nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// Filter to user-data-related issues only
	udIssues, udFixes := filterUserDataIssues(issues, fixes)
	if len(udIssues) == 0 {
		return plan, nil
	}

	// Find the command with user-data (ec2 run-instances or compute droplet create)
	cmdIdx, argIdx, script := findUserDataInPlan(plan)
	if cmdIdx < 0 || script == "" {
		logf("[deploy] user-data micro-repair: no command with user-data found")
		return plan, nil
	}

	logf("[deploy] user-data micro-repair: targeting command %d with %d issue(s)", cmdIdx+1, len(udIssues))

	// Detect provider for prompt adaptation
	provider := strings.ToLower(strings.TrimSpace(plan.Provider))

	// Build targeted prompt with ONLY the script + issues
	prompt := buildUserDataRepairPrompt(script, udIssues, udFixes, provider)
	resp, err := ask(ctx, prompt)
	if err != nil {
		return plan, fmt.Errorf("user-data micro-repair LLM call failed: %w", err)
	}

	fixedScript := extractScriptFromResponse(clean(resp))
	if fixedScript == "" {
		logf("[deploy] user-data micro-repair: LLM returned empty script; keeping original")
		return plan, nil
	}

	// Validate the fixed script is reasonable
	if !isPlausibleScript(fixedScript) {
		logf("[deploy] user-data micro-repair: LLM response doesn't look like a script; keeping original")
		return plan, nil
	}

	// Splice the fixed script back into the plan.
	// EC2 user-data is base64-encoded; DO user-data is plain text.
	var replacement string
	if provider == "digitalocean" {
		replacement = fixedScript // doctl --user-data accepts plain scripts
	} else {
		mimeWrapped := wrapUserDataInMIME(fixedScript)
		replacement = base64.StdEncoding.EncodeToString([]byte(mimeWrapped))
	}
	patched := clonePlanShallow(plan)
	isEquals := strings.HasPrefix(strings.TrimSpace(patched.Commands[cmdIdx].Args[argIdx]), "--user-data=")
	if isEquals {
		patched.Commands[cmdIdx].Args[argIdx] = "--user-data=" + replacement
	} else {
		// --user-data <value> format
		patched.Commands[cmdIdx].Args[argIdx+1] = replacement
	}

	logf("[deploy] user-data micro-repair: successfully patched command %d", cmdIdx+1)
	return patched, nil
}

// ClassifyUserDataIssues separates validation issues into user-data-specific
// and plan-structural categories.
func ClassifyUserDataIssues(issues []string) (userDataIssues, structuralIssues []string) {
	for _, issue := range issues {
		if isUserDataIssue(issue) {
			userDataIssues = append(userDataIssues, issue)
		} else {
			structuralIssues = append(structuralIssues, issue)
		}
	}
	return
}

// filterUserDataIssues filters issues/fixes to only user-data-related ones
func filterUserDataIssues(issues, fixes []string) (udIssues, udFixes []string) {
	for _, s := range issues {
		if isUserDataIssue(s) {
			udIssues = append(udIssues, s)
		}
	}
	for _, s := range fixes {
		if isUserDataFix(s) {
			udFixes = append(udFixes, s)
		}
	}
	return
}

func isUserDataIssue(s string) bool {
	l := strings.ToLower(s)
	// must explicitly mention user-data context
	if strings.Contains(l, "user-data") ||
		strings.Contains(l, "user data") ||
		strings.Contains(l, "userdata") {
		return true
	}
	// base64-specific issues are always user-data
	if strings.Contains(l, "base64") && (strings.Contains(l, "script") || strings.Contains(l, "corrupt") || strings.Contains(l, "garble")) {
		return true
	}
	return false
}

func isUserDataFix(s string) bool {
	l := strings.ToLower(s)
	if strings.Contains(l, "user-data") ||
		strings.Contains(l, "user data") ||
		strings.Contains(l, "userdata") {
		return true
	}
	if strings.Contains(l, "base64") && strings.Contains(l, "regenerate") {
		return true
	}
	return false
}

// findUserDataInPlan locates a command with user-data (ec2 run-instances or
// compute droplet create) and returns (commandIndex, argIndex, decoded script).
// argIndex points to the --user-data flag itself (value is at argIdx+1 for flag form).
func findUserDataInPlan(plan *maker.Plan) (int, int, string) {
	for ci, cmd := range plan.Commands {
		if len(cmd.Args) < 2 {
			continue
		}
		s0 := strings.TrimSpace(cmd.Args[0])
		s1 := strings.TrimSpace(cmd.Args[1])

		// AWS: ec2 run-instances
		isEC2 := s0 == "ec2" && s1 == "run-instances"
		// DO: compute droplet create
		isDO := s0 == "compute" && s1 == "droplet" && len(cmd.Args) >= 3 && strings.TrimSpace(cmd.Args[2]) == "create"

		if !isEC2 && !isDO {
			continue
		}
		for ai := 0; ai < len(cmd.Args); ai++ {
			arg := strings.TrimSpace(cmd.Args[ai])
			if arg == "--user-data" && ai+1 < len(cmd.Args) {
				var script string
				if isEC2 {
					script = extractEC2UserDataScript(cmd.Args)
				} else {
					script = extractDoctlUserDataScript(cmd.Args)
				}
				return ci, ai, script
			}
			if strings.HasPrefix(arg, "--user-data=") {
				var script string
				if isEC2 {
					script = extractEC2UserDataScript(cmd.Args)
				} else {
					script = extractDoctlUserDataScript(cmd.Args)
				}
				return ci, ai, script
			}
		}
	}
	return -1, -1, ""
}

func buildUserDataRepairPrompt(script string, issues, fixes []string, provider string) string {
	var b strings.Builder
	if provider == "digitalocean" {
		runtimeSpec, _ := inferOpenClawDORuntimeSpec(script)
		b.WriteString("You are a shell script repair agent. Fix the following DigitalOcean Droplet user-data script.\n\n")
		b.WriteString("Rules:\n")
		b.WriteString("- Output ONLY the corrected shell script (no markdown, no code fences, no explanation).\n")
		b.WriteString("- Start with #!/bin/bash shebang line.\n")
		b.WriteString("- Fix ONLY the listed issues. Do NOT rewrite the script from scratch.\n")
		b.WriteString("- Preserve all existing functionality — only change what is broken.\n")
		b.WriteString("- Docker is pre-installed on docker-20-04 images. Do NOT reinstall.\n")
		writeLines(&b, openClawDOUserDataRepairLines()...)
		if runtimeSpec.HasBootstrap || runtimeSpec.HasComposeUp || runtimeSpec.HasOnboarding {
			b.WriteString("- Keep the existing OpenClaw runtime shape intact: clone the repo for Compose files, set OPENCLAW_IMAGE to the upstream GHCR image, seed config non-interactively, pull the image, then start the gateway.\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("You are a shell script repair agent. Fix the following EC2 user-data script.\n\n")
		b.WriteString("Rules:\n")
		b.WriteString("- Output ONLY the corrected shell script (no markdown, no code fences, no explanation).\n")
		b.WriteString("- Start with #!/bin/bash shebang line.\n")
		b.WriteString("- Fix ONLY the listed issues. Do NOT rewrite the script from scratch.\n")
		b.WriteString("- Preserve all existing functionality — only change what is broken.\n")
		b.WriteString("- Use correct Linux paths: /usr/, /opt/, /etc/, /var/lib/.\n")
		b.WriteString("- For ECR login, the correct command is: aws ecr get-login-password --region REGION | docker login --username AWS --password-stdin ACCOUNT.dkr.ecr.REGION.amazonaws.com\n")
		b.WriteString("- For Docker on AL2023: yum install -y docker, then systemctl enable docker && systemctl start docker.\n")
		b.WriteString("- Ensure chmod paths match the actual download/install paths.\n\n")
	}

	b.WriteString("Current script:\n")
	b.WriteString(script)
	b.WriteString("\n\n")

	if len(issues) > 0 {
		b.WriteString("Issues to fix:\n")
		for _, s := range issues {
			b.WriteString("- " + strings.TrimSpace(s) + "\n")
		}
		b.WriteString("\n")
	}
	if len(fixes) > 0 {
		b.WriteString("Suggested fixes:\n")
		for _, s := range fixes {
			b.WriteString("- " + strings.TrimSpace(s) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Output the corrected script ONLY.\n")
	return b.String()
}

// extractScriptFromResponse cleans the LLM response to get just the script
func extractScriptFromResponse(resp string) string {
	s := strings.TrimSpace(resp)
	// strip markdown fences
	s = strings.TrimPrefix(s, "```bash")
	s = strings.TrimPrefix(s, "```sh")
	s = strings.TrimPrefix(s, "```shell")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}

func isPlausibleScript(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 20 {
		return false
	}
	// must start with shebang or at least look like shell commands
	if strings.HasPrefix(s, "#!/") {
		return true
	}
	lower := strings.ToLower(s)
	return strings.Contains(lower, "yum ") ||
		strings.Contains(lower, "apt ") ||
		strings.Contains(lower, "dnf ") ||
		strings.Contains(lower, "docker")
}

// clonePlanShallow creates a shallow copy of the plan with deep-copied commands
func clonePlanShallow(plan *maker.Plan) *maker.Plan {
	out := &maker.Plan{
		Version:   plan.Version,
		CreatedAt: plan.CreatedAt,
		Provider:  plan.Provider,
		Question:  plan.Question,
		Summary:   plan.Summary,
		Notes:     plan.Notes,
		Commands:  make([]maker.Command, len(plan.Commands)),
	}
	for i, cmd := range plan.Commands {
		out.Commands[i] = maker.Command{
			Reason:   cmd.Reason,
			Produces: cmd.Produces,
			Args:     append([]string(nil), cmd.Args...),
		}
	}
	return out
}
