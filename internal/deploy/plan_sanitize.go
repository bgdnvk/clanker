package deploy

import (
	"encoding/json"
	"strings"

	"github.com/bgdnvk/clanker/internal/maker"
)

func SanitizePlan(plan *maker.Plan) *maker.Plan {
	if plan == nil {
		return nil
	}
	for i := range plan.Commands {
		plan.Commands[i].Args = sanitizeCommandArgs(plan.Commands[i].Args)
		plan.Commands[i].Reason = strings.TrimSpace(plan.Commands[i].Reason)
	}
	plan.Question = strings.TrimSpace(plan.Question)
	plan.Summary = strings.TrimSpace(plan.Summary)
	return plan
}

// SanitizePlanConservative applies sanitization in a fail-open way:
// it evaluates original vs sanitized with deterministic validation and never picks
// a candidate with more deterministic issues than the original.
func SanitizePlanConservative(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis, logf func(string, ...any)) *maker.Plan {
	if plan == nil {
		return nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	original := clonePlan(plan)
	sanitized := clonePlan(plan)
	SanitizePlan(sanitized)

	originalIssues := deterministicIssueCount(original, profile, deep, docker)
	sanitizedIssues := deterministicIssueCount(sanitized, profile, deep, docker)

	if sanitizedIssues > originalIssues {
		logf("[deploy] sanitizer fallback: sanitized plan is worse (%d -> %d issues), keeping original", originalIssues, sanitizedIssues)
		return original
	}
	if sanitizedIssues < originalIssues {
		logf("[deploy] sanitizer improved deterministic issues (%d -> %d)", originalIssues, sanitizedIssues)
	} else {
		logf("[deploy] sanitizer kept deterministic parity (%d issue(s))", originalIssues)
	}
	return sanitized
}

func deterministicIssueCount(plan *maker.Plan, profile *RepoProfile, deep *DeepAnalysis, docker *DockerAnalysis) int {
	if plan == nil {
		return 0
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return 0
	}
	v := DeterministicValidatePlan(string(planJSON), profile, deep, docker)
	if v == nil {
		return 0
	}
	return len(v.Issues)
}

func clonePlan(plan *maker.Plan) *maker.Plan {
	if plan == nil {
		return nil
	}
	b, err := json.Marshal(plan)
	if err != nil {
		return plan
	}
	var out maker.Plan
	if err := json.Unmarshal(b, &out); err != nil {
		return plan
	}
	return &out
}

func sanitizeCommandArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = sanitizeArgToken(a)
		if a == "" {
			continue
		}
		if isShellOperatorToken(a) {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}

	out = stripShellWrapper(out)
	out = stripProgramPrefix(out)
	if len(out) == 0 {
		return nil
	}

	service := strings.ToLower(strings.TrimSpace(out[0]))
	op := ""
	if len(out) > 1 {
		op = strings.ToLower(strings.TrimSpace(out[1]))
	}
	if service == "iam" && (op == "attach-role-policy" || op == "detach-role-policy" || op == "delete-policy") {
		for i := 0; i < len(out); i++ {
			if strings.TrimSpace(out[i]) == "--policy-arn" && i+1 < len(out) {
				out[i+1] = sanitizeManagedPolicyARN(out[i+1])
			}
			if strings.HasPrefix(strings.TrimSpace(out[i]), "--policy-arn=") {
				v := strings.TrimPrefix(strings.TrimSpace(out[i]), "--policy-arn=")
				out[i] = "--policy-arn=" + sanitizeManagedPolicyARN(v)
			}
		}
	}

	return out
}

func sanitizeArgToken(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "\"'`")
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(v, ",")
	v = strings.TrimSpace(v)
	return v
}

func isShellOperatorToken(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	switch v {
	case "&&", "||", "|", ";", "2>&1", ">", ">>", "<", "<<", "<<<":
		return true
	default:
		return false
	}
}

func stripShellWrapper(args []string) []string {
	if len(args) >= 2 {
		first := strings.ToLower(strings.TrimSpace(args[0]))
		second := strings.ToLower(strings.TrimSpace(args[1]))
		if (first == "bash" || first == "sh" || first == "zsh") && (second == "-c" || second == "-lc") {
			if len(args) > 2 {
				return args[2:]
			}
			return args
		}
	}
	return args
}

func stripProgramPrefix(args []string) []string {
	if len(args) == 0 {
		return args
	}
	first := strings.ToLower(strings.TrimSpace(args[0]))
	switch first {
	case "aws", "gcloud", "az":
		if len(args) > 1 {
			return args[1:]
		}
		return nil
	default:
		return args
	}
}

func sanitizeManagedPolicyARN(value string) string {
	v := strings.TrimSpace(value)
	v = strings.Trim(v, "\"'`")
	v = strings.Trim(v, " ")
	if v == "" {
		return v
	}

	v = strings.ReplaceAll(v, "arn:aws:iam:aws:policy/", "arn:aws:iam::aws:policy/")
	v = strings.ReplaceAll(v, "arn:aws:iam:::aws:policy/", "arn:aws:iam::aws:policy/")
	v = strings.ReplaceAll(v, "arn:aws:iam::aws:policy//", "arn:aws:iam::aws:policy/")
	v = strings.ReplaceAll(v, "arn:aws-us-gov:iam:aws:policy/", "arn:aws-us-gov:iam::aws:policy/")
	v = strings.ReplaceAll(v, "arn:aws-cn:iam:aws:policy/", "arn:aws-cn:iam::aws:policy/")

	if strings.HasPrefix(v, "arn:aws:iam::aws:policy/") || strings.HasPrefix(v, "arn:aws-us-gov:iam::aws:policy/") || strings.HasPrefix(v, "arn:aws-cn:iam::aws:policy/") {
		return v
	}

	if strings.HasPrefix(v, "aws:policy/") {
		return "arn:aws:iam::" + v
	}

	if !strings.HasPrefix(v, "arn:") {
		return "arn:aws:iam::aws:policy/" + strings.TrimPrefix(v, "/")
	}

	return v
}
