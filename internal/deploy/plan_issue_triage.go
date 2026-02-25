package deploy

import "strings"

type RepairIssueTriage struct {
	Hard          *PlanValidation
	LikelyNoise   []string
	ContextNeeded []string
}

func TriageValidationForRepair(v *PlanValidation) RepairIssueTriage {
	out := RepairIssueTriage{Hard: &PlanValidation{IsValid: true}}
	if v == nil {
		return out
	}

	hardIssues := make([]string, 0, len(v.Issues))
	for _, issue := range v.Issues {
		s := strings.TrimSpace(issue)
		if s == "" {
			continue
		}
		switch classifyIssue(s) {
		case "noise":
			out.LikelyNoise = append(out.LikelyNoise, s)
		case "context":
			out.ContextNeeded = append(out.ContextNeeded, s)
		default:
			hardIssues = append(hardIssues, s)
		}
	}

	hardFixes := make([]string, 0, len(v.Fixes))
	for _, fix := range v.Fixes {
		s := strings.TrimSpace(fix)
		if s == "" {
			continue
		}
		switch classifyFix(s) {
		case "noise":
			out.LikelyNoise = append(out.LikelyNoise, s)
		case "context":
			out.ContextNeeded = append(out.ContextNeeded, s)
		default:
			hardFixes = append(hardFixes, s)
		}
	}

	hardWarnings := make([]string, 0, len(v.Warnings))
	for _, warning := range v.Warnings {
		s := strings.TrimSpace(warning)
		if s == "" {
			continue
		}
		switch classifyIssue(s) {
		case "noise":
			out.LikelyNoise = append(out.LikelyNoise, s)
		case "context":
			out.ContextNeeded = append(out.ContextNeeded, s)
		default:
			hardWarnings = append(hardWarnings, s)
		}
	}

	out.Hard = &PlanValidation{
		IsValid:                len(hardIssues) == 0,
		Issues:                 uniqueStrings(hardIssues),
		Fixes:                  uniqueStrings(hardFixes),
		Warnings:               uniqueStrings(hardWarnings),
		UnresolvedPlaceholders: v.UnresolvedPlaceholders,
	}
	out.LikelyNoise = uniqueStrings(out.LikelyNoise)
	out.ContextNeeded = uniqueStrings(out.ContextNeeded)
	return out
}

func classifyIssue(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return "noise"
	}
	if strings.Contains(l, "disregard") || strings.Contains(l, "actually correct") || strings.Contains(l, "let me re-check") {
		return "noise"
	}
	if strings.Contains(l, "cloudfront") && strings.Contains(l, "does not") && strings.Contains(l, "websocket") {
		return "noise"
	}
	if strings.Contains(l, "iam policy arn is malformed") && strings.Contains(l, "arn:aws:iam::aws:policy/") {
		return "noise"
	}
	if strings.Contains(l, "if ") || strings.Contains(l, "depends") || strings.Contains(l, "worth verifying") || strings.Contains(l, "may be") || strings.Contains(l, "might") {
		return "context"
	}
	return "hard"
}

func classifyFix(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return "noise"
	}
	if strings.Contains(l, "consider") || strings.Contains(l, "verify") {
		return "context"
	}
	if strings.Contains(l, "disregard") {
		return "noise"
	}
	return "hard"
}
